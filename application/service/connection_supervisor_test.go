package service_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/port"
	"github.com/alexmorbo/oasis-modbus2mqtt/application/service"
	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/config"
)

// fakeConn is a hand-written mock of port.ModbusConnection.
type fakeConn struct {
	mu sync.Mutex

	connectFn       func(ctx context.Context) error
	forceCloseFn    func() error
	connectedFlag   bool
	connectCalls    int
	forceCloseCalls int
}

func (f *fakeConn) Connect(ctx context.Context) error {
	f.mu.Lock()
	f.connectCalls++
	fn := f.connectFn
	f.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(ctx)
}

func (f *fakeConn) ForceClose() error {
	f.mu.Lock()
	f.forceCloseCalls++
	fn := f.forceCloseFn
	f.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn()
}

func (f *fakeConn) Connected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connectedFlag
}

func (f *fakeConn) ReadInput(_ context.Context, _, _ uint16) ([]uint16, error) {
	return nil, errors.New("not implemented in fakeConn")
}

func (f *fakeConn) ReadHolding(_ context.Context, _, _ uint16) ([]uint16, error) {
	return nil, errors.New("not implemented in fakeConn")
}

func (f *fakeConn) WriteHolding(_ context.Context, _, _ uint16) error {
	return errors.New("not implemented in fakeConn")
}

func (f *fakeConn) ModifyHolding(_ context.Context, _ uint16, _ func(uint16) uint16) error {
	return errors.New("not implemented in fakeConn")
}

func (f *fakeConn) ConnectCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connectCalls
}

func (f *fakeConn) ForceCloseCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.forceCloseCalls
}

var _ port.ModbusConnection = (*fakeConn)(nil)

func fastReconnectCfg() config.ReconnectConfig {
	return config.ReconnectConfig{
		MinDelay:  1 * time.Millisecond,
		MaxDelay:  10 * time.Millisecond,
		Factor:    2.0,
		JitterPct: 0,
	}
}

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNewConnectionSupervisor_NilLoggerFallsBackToDefault(t *testing.T) {
	t.Parallel()

	conn := &fakeConn{}
	s := service.NewConnectionSupervisor(conn, fastReconnectCfg(), nil)
	require.NotNil(t, s)
}

func TestStart_InitialConnect_Success(t *testing.T) {
	t.Parallel()

	conn := &fakeConn{}
	s := service.NewConnectionSupervisor(conn, fastReconnectCfg(), discardLog())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	require.NoError(t, s.Start(ctx))
	assert.Equal(t, 1, conn.ConnectCalls())

	cancel()
	select {
	case <-s.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("supervisor goroutine did not exit after ctx cancellation")
	}
}

func TestStart_InitialConnect_Retries(t *testing.T) {
	t.Parallel()

	var attempts int
	var mu sync.Mutex
	conn := &fakeConn{
		connectFn: func(_ context.Context) error {
			mu.Lock()
			attempts++
			n := attempts
			mu.Unlock()
			if n < 3 {
				return errors.New("transient")
			}
			return nil
		},
	}
	s := service.NewConnectionSupervisor(conn, fastReconnectCfg(), discardLog())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	require.NoError(t, s.Start(ctx))
	assert.Equal(t, 3, conn.ConnectCalls())

	cancel()
	select {
	case <-s.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("supervisor goroutine did not exit")
	}
}

func TestStart_InitialConnect_CancelledContext(t *testing.T) {
	t.Parallel()

	conn := &fakeConn{
		connectFn: func(_ context.Context) error {
			return errors.New("always fails")
		},
	}
	s := service.NewConnectionSupervisor(conn, fastReconnectCfg(), discardLog())

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := s.Start(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Greater(t, conn.ConnectCalls(), 0)
}

func TestTrigger_TriggersForceCloseAndReconnect(t *testing.T) {
	t.Parallel()

	conn := &fakeConn{}
	s := service.NewConnectionSupervisor(conn, fastReconnectCfg(), discardLog())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	require.NoError(t, s.Start(ctx))
	require.Equal(t, 1, conn.ConnectCalls())

	s.Trigger()

	require.Eventually(t, func() bool {
		return conn.ForceCloseCalls() >= 1 && conn.ConnectCalls() >= 2
	}, 500*time.Millisecond, 5*time.Millisecond, "supervisor did not handle trigger")

	cancel()
	select {
	case <-s.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("supervisor goroutine did not exit")
	}
}

func TestTrigger_ForceCloseError_DoesNotPreventReconnect(t *testing.T) {
	t.Parallel()

	conn := &fakeConn{
		forceCloseFn: func() error { return errors.New("close failed") },
	}
	s := service.NewConnectionSupervisor(conn, fastReconnectCfg(), discardLog())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	require.NoError(t, s.Start(ctx))
	s.Trigger()

	require.Eventually(t, func() bool {
		return conn.ForceCloseCalls() >= 1 && conn.ConnectCalls() >= 2
	}, 500*time.Millisecond, 5*time.Millisecond)

	cancel()
	<-s.Done()
}

func TestTrigger_ReconnectFails_KeepsRetryingUntilSuccess(t *testing.T) {
	t.Parallel()

	var afterTrigger int
	var mu sync.Mutex
	connSucceeded := make(chan struct{})

	conn := &fakeConn{
		connectFn: func(_ context.Context) error {
			mu.Lock()
			afterTrigger++
			n := afterTrigger
			mu.Unlock()
			// First call (initial connect) succeeds; next two fail; fourth
			// (still in reconnectLoop) succeeds and unblocks the test.
			if n == 1 {
				return nil
			}
			if n < 4 {
				return errors.New("transient")
			}
			select {
			case connSucceeded <- struct{}{}:
			default:
			}
			return nil
		},
	}
	s := service.NewConnectionSupervisor(conn, fastReconnectCfg(), discardLog())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	require.NoError(t, s.Start(ctx))
	s.Trigger()

	select {
	case <-connSucceeded:
	case <-time.After(1 * time.Second):
		t.Fatal("reconnect loop never produced a successful connect")
	}

	cancel()
	<-s.Done()
}

func TestTrigger_NonBlocking_WhenChannelFull(t *testing.T) {
	t.Parallel()

	// Do NOT call Start: the trigger channel has cap 1, no consumer; we
	// only verify that repeated Trigger calls never block.
	conn := &fakeConn{}
	s := service.NewConnectionSupervisor(conn, fastReconnectCfg(), discardLog())

	start := time.Now()
	for i := 0; i < 1000; i++ {
		s.Trigger()
	}
	elapsed := time.Since(start)
	assert.Less(t, elapsed, 100*time.Millisecond, "Trigger must be non-blocking under load")
}

func TestStop_via_ContextCancelled(t *testing.T) {
	t.Parallel()

	conn := &fakeConn{}
	s := service.NewConnectionSupervisor(conn, fastReconnectCfg(), discardLog())

	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, s.Start(ctx))

	cancel()

	select {
	case <-s.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Done channel was not closed within timeout after ctx cancellation")
	}
}

func TestStop_DuringReconnectLoop(t *testing.T) {
	t.Parallel()

	connectCalls := 0
	var mu sync.Mutex
	conn := &fakeConn{
		connectFn: func(_ context.Context) error {
			mu.Lock()
			connectCalls++
			n := connectCalls
			mu.Unlock()
			if n == 1 {
				return nil // initial connect succeeds
			}
			return errors.New("perma-fail") // reconnect always fails
		},
	}
	cfg := config.ReconnectConfig{
		MinDelay:  10 * time.Millisecond,
		MaxDelay:  20 * time.Millisecond,
		Factor:    2.0,
		JitterPct: 0,
	}
	s := service.NewConnectionSupervisor(conn, cfg, discardLog())

	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, s.Start(ctx))
	s.Trigger()

	// Give the supervisor a moment to enter reconnectLoop and start a
	// timer wait, then cancel.
	time.Sleep(15 * time.Millisecond)
	cancel()

	select {
	case <-s.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Done not closed after cancellation during reconnect loop")
	}
}
