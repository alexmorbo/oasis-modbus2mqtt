package service

import (
	"context"
	"log/slog"
	"sync"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/port"
)

// FailureNotifier is implemented by ConnectionSupervisor and consumed by
// CommandDispatcher. Defining it here breaks the would-be circular
// dependency between the two services: dispatcher signals failures via this
// interface; supervisor satisfies it by exposing a non-blocking Trigger.
type FailureNotifier interface {
	// Trigger asks the supervisor to perform a reconnect. Implementations
	// must be non-blocking and idempotent under load.
	Trigger()
}

// CommandDispatcher is a typed wrapper around port.ModbusClient that tracks
// consecutive failures across operations and signals a FailureNotifier once
// a threshold is reached. Per-operation atomicity is provided by the
// underlying client; the dispatcher only adds cross-op state.
type CommandDispatcher struct {
	client    port.ModbusClient
	notifier  FailureNotifier
	threshold int
	logger    *slog.Logger

	mu              sync.Mutex
	consecutiveErrs int
}

// NewCommandDispatcher constructs a CommandDispatcher. It panics if client
// or notifier is nil, or if threshold is not strictly positive — these are
// programming errors that must surface during wiring (main.go), not at
// runtime. A nil logger falls back to slog.Default.
func NewCommandDispatcher(
	client port.ModbusClient,
	notifier FailureNotifier,
	threshold int,
	logger *slog.Logger,
) *CommandDispatcher {
	if client == nil {
		panic("command dispatcher: client must not be nil")
	}
	if notifier == nil {
		panic("command dispatcher: notifier must not be nil")
	}
	if threshold <= 0 {
		panic("command dispatcher: threshold must be > 0")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &CommandDispatcher{
		client:    client,
		notifier:  notifier,
		threshold: threshold,
		logger:    logger,
	}
}

// ReadInput proxies port.ModbusClient.ReadInput and records the outcome.
func (d *CommandDispatcher) ReadInput(ctx context.Context, start, count uint16) ([]uint16, error) {
	result, err := d.client.ReadInput(ctx, start, count)
	d.recordOutcome(err)
	return result, err
}

// ReadHolding proxies port.ModbusClient.ReadHolding and records the outcome.
func (d *CommandDispatcher) ReadHolding(ctx context.Context, start, count uint16) ([]uint16, error) {
	result, err := d.client.ReadHolding(ctx, start, count)
	d.recordOutcome(err)
	return result, err
}

// WriteHolding proxies port.ModbusClient.WriteHolding and records the outcome.
func (d *CommandDispatcher) WriteHolding(ctx context.Context, addr, value uint16) error {
	err := d.client.WriteHolding(ctx, addr, value)
	d.recordOutcome(err)
	return err
}

// ModifyHolding proxies port.ModbusClient.ModifyHolding and records the outcome.
func (d *CommandDispatcher) ModifyHolding(ctx context.Context, addr uint16, modify func(uint16) uint16) error {
	err := d.client.ModifyHolding(ctx, addr, modify)
	d.recordOutcome(err)
	return err
}

// Connected proxies port.ModbusClient.Connected.
func (d *CommandDispatcher) Connected() bool {
	return d.client.Connected()
}

// recordOutcome updates the consecutive-error counter and triggers the
// notifier when the threshold is reached. Counter is reset only on success;
// the counter is intentionally NOT reset after triggering, so subsequent
// failures keep signalling — the notifier handles idempotency.
func (d *CommandDispatcher) recordOutcome(err error) {
	d.mu.Lock()
	if err == nil {
		d.consecutiveErrs = 0
		d.mu.Unlock()
		return
	}
	d.consecutiveErrs++
	current := d.consecutiveErrs
	d.mu.Unlock()

	d.logger.Warn("modbus op failed",
		slog.Int("consecutive", current),
		slog.Any("error", err),
	)
	if current >= d.threshold {
		d.logger.Warn("consecutive failure threshold reached, signaling supervisor",
			slog.Int("threshold", d.threshold),
			slog.Int("consecutive", current),
		)
		d.notifier.Trigger()
	}
}
