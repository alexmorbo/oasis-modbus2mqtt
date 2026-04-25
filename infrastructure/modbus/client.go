package modbus

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	gridx "github.com/grid-x/modbus"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/dto"
	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/config"
	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/metrics"
)

// maxRegistersPerRead is the Modbus protocol limit for a single
// ReadInputRegisters / ReadHoldingRegisters request.
const maxRegistersPerRead uint16 = 125

// Client is the infrastructure Modbus TCP client. It owns one persistent
// TCP connection, serializes all operations under a single mutex, and
// enforces a configurable guard interval between consecutive operations.
type Client struct {
	cfg    config.ModbusConfig
	logger *slog.Logger

	mu        sync.Mutex
	handler   *gridx.TCPClientHandler
	client    gridx.Client
	connected atomic.Bool
	lastOpAt  time.Time
}

// NewClient constructs a Client without performing any I/O. Call Connect
// before invoking any read/write/modify operation.
func NewClient(cfg config.ModbusConfig, logger *slog.Logger) *Client {
	return &Client{
		cfg:    cfg,
		logger: logger,
	}
}

// Connect opens the TCP transport, configures the handler with the
// configured timeouts and slave id, and marks the client connected. It is
// idempotent: calling Connect on an already-connected client is a no-op.
func (c *Client) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.connected.Load() {
		return nil
	}

	handler := gridx.NewTCPClientHandler(c.cfg.Addr())
	handler.Timeout = c.cfg.ReadTimeout
	handler.SlaveID = c.cfg.SlaveID

	connectCtx, cancel := context.WithTimeout(ctx, c.cfg.ConnectTimeout)
	defer cancel()
	if err := handler.Connect(connectCtx); err != nil {
		c.logger.Warn("modbus connect failed",
			slog.String("addr", c.cfg.Addr()),
			slog.Any("error", err),
		)
		return WrapError("connect", err)
	}

	c.handler = handler
	c.client = gridx.NewClient(handler)
	c.connected.Store(true)
	c.lastOpAt = time.Time{}
	metrics.SetModbusConnected(true)
	metrics.ModbusReconnectsTotal.Inc()
	c.logger.Info("modbus connected", slog.String("addr", c.cfg.Addr()))
	return nil
}

// ForceClose tears down the TCP transport and marks the client
// disconnected. It is idempotent.
func (c *Client) ForceClose() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected.Load() {
		return nil
	}

	if c.handler != nil {
		if err := c.handler.Close(); err != nil {
			c.logger.Warn("modbus close error",
				slog.String("addr", c.cfg.Addr()),
				slog.Any("error", err),
			)
		}
	}
	c.connected.Store(false)
	metrics.SetModbusConnected(false)
	c.logger.Warn("modbus disconnected", slog.String("addr", c.cfg.Addr()))
	return nil
}

// Connected reports whether the underlying transport is currently usable.
// Non-blocking; safe to call concurrently.
func (c *Client) Connected() bool {
	return c.connected.Load()
}

// ReadInput reads count input registers (FC 0x04) starting at start.
func (c *Client) ReadInput(ctx context.Context, start, count uint16) ([]uint16, error) {
	if count == 0 || count > maxRegistersPerRead {
		return nil, fmt.Errorf("read_input: %w: count=%d", ErrIllegalDataValue, count)
	}
	var raw []byte
	err := c.doOp(ctx, "read_input", func(ctx context.Context) error {
		var ferr error
		raw, ferr = c.client.ReadInputRegisters(ctx, start, count)
		return ferr
	})
	if err != nil {
		return nil, err
	}
	regs, derr := BytesToUint16(raw)
	if derr != nil {
		return nil, fmt.Errorf("read_input: %w", derr)
	}
	return regs, nil
}

// ReadHolding reads count holding registers (FC 0x03) starting at start.
func (c *Client) ReadHolding(ctx context.Context, start, count uint16) ([]uint16, error) {
	if count == 0 || count > maxRegistersPerRead {
		return nil, fmt.Errorf("read_holding: %w: count=%d", ErrIllegalDataValue, count)
	}
	var raw []byte
	err := c.doOp(ctx, "read_holding", func(ctx context.Context) error {
		var ferr error
		raw, ferr = c.client.ReadHoldingRegisters(ctx, start, count)
		return ferr
	})
	if err != nil {
		return nil, err
	}
	regs, derr := BytesToUint16(raw)
	if derr != nil {
		return nil, fmt.Errorf("read_holding: %w", derr)
	}
	return regs, nil
}

// WriteHolding writes value to a single holding register at addr (FC 0x06).
func (c *Client) WriteHolding(ctx context.Context, addr, value uint16) error {
	return c.doOp(ctx, "write_holding", func(ctx context.Context) error {
		_, ferr := c.client.WriteSingleRegister(ctx, addr, value)
		return ferr
	})
}

// ModifyHolding atomically reads, modifies, and writes the holding register
// at addr. The mutex is held across the whole sequence so no other op on
// this Client interleaves between read and write.
func (c *Client) ModifyHolding(ctx context.Context, addr uint16, modify func(current uint16) uint16) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected.Load() {
		return fmt.Errorf("modify_holding: %w", dto.ErrModbusNotConnected)
	}

	if err := c.waitGuardInterval(ctx); err != nil {
		return fmt.Errorf("modify_holding: %w", err)
	}
	readStart := time.Now()
	raw, err := c.client.ReadHoldingRegisters(ctx, addr, 1)
	c.lastOpAt = time.Now()
	metrics.ModbusOpDurationSeconds("modify_holding_read").UpdateDuration(readStart)
	if err != nil {
		c.handleOpError("modify_holding_read", err)
		return WrapError("modify_holding_read", err)
	}
	decoded, derr := BytesToUint16(raw)
	if derr != nil {
		metrics.ModbusOpsTotal("modify_holding_read", "error").Inc()
		metrics.ModbusErrorsTotal("decode").Inc()
		return fmt.Errorf("modify_holding_read: %w", derr)
	}
	if len(decoded) != 1 {
		metrics.ModbusOpsTotal("modify_holding_read", "error").Inc()
		metrics.ModbusErrorsTotal("decode").Inc()
		return fmt.Errorf("modify_holding_read: expected 1 register, got %d", len(decoded))
	}
	metrics.ModbusOpsTotal("modify_holding_read", "ok").Inc()
	current := decoded[0]

	var newVal uint16
	var modifyErr error
	func() {
		defer func() {
			if r := recover(); r != nil {
				modifyErr = fmt.Errorf("modify_holding: modify func panicked: %v", r)
			}
		}()
		newVal = modify(current)
	}()
	if modifyErr != nil {
		c.logger.Error("modbus modify panic",
			slog.Int("addr", int(addr)),
			slog.Any("error", modifyErr),
		)
		metrics.ModbusErrorsTotal("modify_panic").Inc()
		return modifyErr
	}

	if err := c.waitGuardInterval(ctx); err != nil {
		return fmt.Errorf("modify_holding: %w", err)
	}
	writeStart := time.Now()
	_, werr := c.client.WriteSingleRegister(ctx, addr, newVal)
	c.lastOpAt = time.Now()
	metrics.ModbusOpDurationSeconds("modify_holding_write").UpdateDuration(writeStart)
	if werr != nil {
		c.handleOpError("modify_holding_write", werr)
		return WrapError("modify_holding_write", werr)
	}
	metrics.ModbusOpsTotal("modify_holding_write", "ok").Inc()
	c.logger.Debug("modbus modify ok",
		slog.Int("addr", int(addr)),
		slog.Int("old", int(current)),
		slog.Int("new", int(newVal)),
	)
	return nil
}

// doOp is the per-operation template used by single-step ops (Read*,
// WriteHolding). It locks the mutex, checks Connected, waits the guard
// interval, runs fn, records metrics, classifies errors, and updates
// connection state on connection-level failures.
func (c *Client) doOp(ctx context.Context, opName string, fn func(context.Context) error) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if !c.connected.Load() {
		return fmt.Errorf("%s: %w", opName, dto.ErrModbusNotConnected)
	}

	if err := c.waitGuardInterval(ctx); err != nil {
		return fmt.Errorf("%s: %w", opName, err)
	}

	start := time.Now()
	err := fn(ctx)
	duration := time.Since(start)
	c.lastOpAt = time.Now()
	metrics.ModbusOpDurationSeconds(opName).UpdateDuration(start)

	if err != nil {
		c.handleOpError(opName, err)
		wrapped := WrapError(opName, err)
		c.logger.Error("modbus op failed",
			slog.String("op", opName),
			slog.Duration("duration", duration),
			slog.Any("error", err),
		)
		return wrapped
	}
	metrics.ModbusOpsTotal(opName, "ok").Inc()
	c.logger.Debug("modbus op ok",
		slog.String("op", opName),
		slog.Duration("duration", duration),
	)
	return nil
}

// handleOpError classifies err, increments the proper metric counters, and
// flips connected to false on transport-level failures. The caller is
// expected to be holding c.mu.
func (c *Client) handleOpError(opName string, err error) {
	metrics.ModbusOpsTotal(opName, "error").Inc()
	wrapped := WrapError(opName, err)
	switch {
	case errors.Is(wrapped, ErrConnectionLost):
		c.connected.Store(false)
		metrics.SetModbusConnected(false)
		metrics.ModbusErrorsTotal("connection_lost").Inc()
		c.logger.Warn("modbus connection lost",
			slog.String("op", opName),
			slog.Any("error", err),
		)
	case errors.Is(wrapped, ErrTimeout):
		c.connected.Store(false)
		metrics.SetModbusConnected(false)
		metrics.ModbusErrorsTotal("timeout").Inc()
		c.logger.Warn("modbus timeout",
			slog.String("op", opName),
			slog.Any("error", err),
		)
	default:
		metrics.ModbusErrorsTotal("protocol").Inc()
	}
}

// waitGuardInterval blocks until at least cfg.GuardInterval has elapsed
// since the last op completed, or until ctx is cancelled. Returns ctx.Err()
// if cancelled. The caller must be holding c.mu.
func (c *Client) waitGuardInterval(ctx context.Context) error {
	if c.lastOpAt.IsZero() || c.cfg.GuardInterval <= 0 {
		return nil
	}
	elapsed := time.Since(c.lastOpAt)
	if elapsed >= c.cfg.GuardInterval {
		return nil
	}
	remaining := c.cfg.GuardInterval - elapsed
	t := time.NewTimer(remaining)
	defer t.Stop()
	select {
	case <-t.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}
