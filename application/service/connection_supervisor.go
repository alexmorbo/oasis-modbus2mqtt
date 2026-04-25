package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/port"
	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/config"
	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/metrics"
)

// ConnectionSupervisor owns the lifecycle of a port.ModbusConnection. It
// performs the initial connect synchronously from Start, then runs a single
// goroutine that waits for either context cancellation or a Trigger signal
// (typically from CommandDispatcher), tearing down and reconnecting with
// exponential backoff on each trigger.
//
// ConnectionSupervisor satisfies the FailureNotifier interface declared in
// command_dispatcher.go via its Trigger method, by duck typing — neither
// service imports the other.
type ConnectionSupervisor struct {
	conn    port.ModbusConnection
	cfg     config.ReconnectConfig
	logger  *slog.Logger
	backoff *Backoff

	trigger chan struct{}
	done    chan struct{}
}

// NewConnectionSupervisor constructs a ConnectionSupervisor without
// performing I/O. A nil logger falls back to slog.Default.
func NewConnectionSupervisor(
	conn port.ModbusConnection,
	cfg config.ReconnectConfig,
	logger *slog.Logger,
) *ConnectionSupervisor {
	if logger == nil {
		logger = slog.Default()
	}
	return &ConnectionSupervisor{
		conn:    conn,
		cfg:     cfg,
		logger:  logger,
		backoff: NewBackoff(cfg),
		trigger: make(chan struct{}, 1),
		done:    make(chan struct{}),
	}
}

// Start performs the initial connect (with retry+backoff) and, on success,
// launches the supervisor goroutine. It returns nil after a successful
// initial connect; the goroutine continues running until ctx is cancelled.
// If the initial connect cannot succeed before ctx is cancelled, Start
// returns ctx.Err().
func (s *ConnectionSupervisor) Start(ctx context.Context) error {
	if err := s.initialConnect(ctx); err != nil {
		return err
	}
	go s.run(ctx)
	return nil
}

// Trigger asks the supervisor to perform a reconnect. It is non-blocking:
// if a previous trigger is still pending, the call is silently dropped
// (the pending trigger already covers the new request). Safe for use
// across goroutines and satisfies FailureNotifier.
func (s *ConnectionSupervisor) Trigger() {
	select {
	case s.trigger <- struct{}{}:
	default:
	}
}

// Done returns a channel closed when the supervisor goroutine has exited.
// Useful for graceful shutdown coordination and test synchronization.
func (s *ConnectionSupervisor) Done() <-chan struct{} {
	return s.done
}

// initialConnect runs Connect in a backoff loop until it succeeds or ctx
// is cancelled. On success it returns nil and resets the backoff.
func (s *ConnectionSupervisor) initialConnect(ctx context.Context) error {
	for {
		if err := s.conn.Connect(ctx); err == nil {
			s.backoff.Reset()
			s.logger.Info("modbus initial connect ok")
			return nil
		} else {
			metrics.ModbusErrorsTotal("reconnect_failed").Inc()
			s.logger.Warn("modbus initial connect failed", slog.Any("error", err))
		}

		delay := s.backoff.Next()
		s.logger.Info("retry scheduled", slog.Duration("delay", delay))

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// run is the supervisor goroutine. It waits for triggers and ctx
// cancellation. On a trigger it tears down the connection and runs the
// reconnect loop until success or cancellation.
func (s *ConnectionSupervisor) run(ctx context.Context) {
	defer close(s.done)
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.trigger:
			s.logger.Warn("supervisor trigger received")
			if err := s.conn.ForceClose(); err != nil {
				s.logger.Warn("force close failed", slog.Any("error", err))
			}
			s.reconnectLoop(ctx)
		}
	}
}

// reconnectLoop attempts Connect with exponential backoff until success or
// ctx cancellation. On success it resets the backoff. Each failed attempt
// increments the modbus_errors_total{error_type="reconnect_failed"} counter.
func (s *ConnectionSupervisor) reconnectLoop(ctx context.Context) {
	for {
		delay := s.backoff.Next()
		s.logger.Info("reconnect attempt scheduled", slog.Duration("delay", delay))

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		if err := s.conn.Connect(ctx); err != nil {
			metrics.ModbusErrorsTotal("reconnect_failed").Inc()
			s.logger.Warn("reconnect failed", slog.Any("error", err))
			continue
		}
		s.backoff.Reset()
		s.logger.Info("modbus reconnected")
		return
	}
}
