package modbus_test

import (
	"context"
	"io"
	"log/slog"
	"net"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tbrandon/mbserver"

	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/config"
	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/modbus"
)

// testServer wraps mbserver.Server to provide idempotent Close via sync.Once,
// preventing panics when both the test body and t.Cleanup call Close.
type testServer struct {
	*mbserver.Server
	addr string
	once sync.Once
}

// StopOnce closes the underlying mbserver exactly once; subsequent calls are
// no-ops. This makes both manual and cleanup-registered close calls safe.
func (ts *testServer) StopOnce() {
	ts.once.Do(func() {
		ts.Close()
	})
}

// freePort returns "127.0.0.1:<port>" bound to a free ephemeral port. The
// listener is closed immediately so the caller can hand the address to
// mbserver. The race window is acceptable for local CI.
func freePort(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	ctx := context.Background()
	l, err := lc.Listen(ctx, "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return addr
}

func startTestServer(t *testing.T) (*testServer, string) {
	t.Helper()
	addr := freePort(t)
	s := mbserver.NewServer()
	require.NoError(t, s.ListenTCP(addr))
	ts := &testServer{Server: s, addr: addr}
	t.Cleanup(func() { ts.StopOnce() })
	return ts, addr
}

func splitHostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	return host, port
}

func newClient(t *testing.T, addr string, guard time.Duration) *modbus.Client {
	t.Helper()
	host, port := splitHostPort(t, addr)
	cfg := config.ModbusConfig{
		Host:           host,
		Port:           port,
		SlaveID:        1,
		ConnectTimeout: 2 * time.Second,
		ReadTimeout:    1 * time.Second,
		WriteTimeout:   1 * time.Second,
		GuardInterval:  guard,
	}
	logger := slog.New(slog.NewJSONHandler(io.Discard, nil))
	return modbus.NewClient(cfg, logger)
}

func newConnectedClient(t *testing.T, addr string, guard time.Duration) *modbus.Client {
	t.Helper()
	c := newClient(t, addr, guard)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, c.Connect(ctx))
	t.Cleanup(func() { _ = c.ForceClose() })
	return c
}

func TestRead_HoldingRegisters_Success(t *testing.T) {
	t.Parallel()

	s, addr := startTestServer(t)
	s.HoldingRegisters[10] = 0x1234
	s.HoldingRegisters[11] = 0xABCD

	c := newConnectedClient(t, addr, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	regs, err := c.ReadHolding(ctx, 10, 2)
	require.NoError(t, err)
	assert.Equal(t, []uint16{0x1234, 0xABCD}, regs)
	assert.True(t, c.Connected())
}

func TestRead_InputRegisters_Success(t *testing.T) {
	t.Parallel()

	s, addr := startTestServer(t)
	s.InputRegisters[5] = 0xBEEF

	c := newConnectedClient(t, addr, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	regs, err := c.ReadInput(ctx, 5, 1)
	require.NoError(t, err)
	assert.Equal(t, []uint16{0xBEEF}, regs)
}

func TestRead_RejectsZeroCount(t *testing.T) {
	t.Parallel()

	_, addr := startTestServer(t)
	c := newConnectedClient(t, addr, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := c.ReadHolding(ctx, 0, 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, modbus.ErrIllegalDataValue)

	_, err = c.ReadInput(ctx, 0, 0)
	require.Error(t, err)
	assert.ErrorIs(t, err, modbus.ErrIllegalDataValue)
}

func TestRead_RejectsTooLargeCount(t *testing.T) {
	t.Parallel()

	_, addr := startTestServer(t)
	c := newConnectedClient(t, addr, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	_, err := c.ReadHolding(ctx, 0, 200)
	require.Error(t, err)
	assert.ErrorIs(t, err, modbus.ErrIllegalDataValue)
}

func TestWrite_HoldingRegister_Success(t *testing.T) {
	t.Parallel()

	s, addr := startTestServer(t)
	c := newConnectedClient(t, addr, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	require.NoError(t, c.WriteHolding(ctx, 20, 0xCAFE))
	assert.Equal(t, uint16(0xCAFE), s.HoldingRegisters[20])
}

func TestRMW_ModifyHolding_Atomic(t *testing.T) {
	t.Parallel()

	s, addr := startTestServer(t)
	s.HoldingRegisters[5] = 0xAAAA

	c := newConnectedClient(t, addr, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := c.ModifyHolding(ctx, 5, func(current uint16) uint16 {
		assert.Equal(t, uint16(0xAAAA), current)
		return current | 0x0001
	})
	require.NoError(t, err)
	assert.Equal(t, uint16(0xAAAB), s.HoldingRegisters[5])
}

func TestRMW_ModifyFuncPanic(t *testing.T) {
	t.Parallel()

	s, addr := startTestServer(t)
	s.HoldingRegisters[7] = 0x1111

	c := newConnectedClient(t, addr, 0)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	err := c.ModifyHolding(ctx, 7, func(_ uint16) uint16 {
		panic("boom")
	})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "panicked")
	assert.Equal(t, uint16(0x1111), s.HoldingRegisters[7])

	regs, err := c.ReadHolding(ctx, 7, 1)
	require.NoError(t, err)
	assert.Equal(t, []uint16{0x1111}, regs)
}

func TestGuardInterval_Enforced(t *testing.T) {
	t.Parallel()

	s, addr := startTestServer(t)
	s.HoldingRegisters[0] = 0x0000

	const guard = 200 * time.Millisecond
	c := newConnectedClient(t, addr, guard)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	start := time.Now()
	for i := 0; i < 3; i++ {
		_, err := c.ReadHolding(ctx, 0, 1)
		require.NoError(t, err)
	}
	elapsed := time.Since(start)
	assert.GreaterOrEqual(t, elapsed, 2*guard,
		"three reads must take at least 2*guard, got %s", elapsed)
}

func TestGuardInterval_NoWaitAfterIdle(t *testing.T) {
	t.Parallel()

	s, addr := startTestServer(t)
	s.HoldingRegisters[0] = 0x0000

	const guard = 100 * time.Millisecond
	c := newConnectedClient(t, addr, guard)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	_, err := c.ReadHolding(ctx, 0, 1)
	require.NoError(t, err)
	time.Sleep(2 * guard)

	start := time.Now()
	_, err = c.ReadHolding(ctx, 0, 1)
	require.NoError(t, err)
	elapsed := time.Since(start)
	assert.Less(t, elapsed, guard,
		"idle period > guard must not introduce extra wait, got %s", elapsed)
}

func TestRead_AfterServerClose_FailsAndMarksDisconnected(t *testing.T) {
	t.Parallel()
	// Skipped: tbrandon/mbserver.Close() stops the listener but does NOT close
	// existing TCP connections. The client's socket therefore remains "open"
	// from the kernel's perspective and subsequent reads can succeed against
	// stale buffers — making this test fundamentally non-deterministic.
	//
	// Connection-loss handling is exercised by:
	//   - TestConnect_Fails_WhenNoServer (no server at all)
	//   - TestReconnect_IncrementsCounter (explicit ForceClose + reconnect)
	//   - decode_test.go WrapError classification tests for io.EOF / *net.OpError
	t.Skip("mbserver does not close active TCP conns on Close(); see comment")
}

func TestReconnect_IncrementsCounter(t *testing.T) {
	t.Parallel()

	_, addr := startTestServer(t)
	c := newClient(t, addr, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	require.NoError(t, c.Connect(ctx))
	assert.True(t, c.Connected())
	require.NoError(t, c.ForceClose())
	assert.False(t, c.Connected())

	require.NoError(t, c.Connect(ctx))
	assert.True(t, c.Connected())

	t.Cleanup(func() { _ = c.ForceClose() })
}

func TestConnect_Idempotent(t *testing.T) {
	t.Parallel()

	_, addr := startTestServer(t)
	c := newClient(t, addr, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	require.NoError(t, c.Connect(ctx))
	require.NoError(t, c.Connect(ctx))
	assert.True(t, c.Connected())
	t.Cleanup(func() { _ = c.ForceClose() })
}

func TestForceClose_Idempotent(t *testing.T) {
	t.Parallel()

	_, addr := startTestServer(t)
	c := newClient(t, addr, 0)

	require.NoError(t, c.ForceClose())
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	require.NoError(t, c.Connect(ctx))
	require.NoError(t, c.ForceClose())
	require.NoError(t, c.ForceClose())
}

func TestConnect_Fails_WhenNoServer(t *testing.T) {
	t.Parallel()

	addr := freePort(t)
	c := newClient(t, addr, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := c.Connect(ctx)
	require.Error(t, err)
	assert.False(t, c.Connected())
}

func TestRead_NotConnected_ReturnsErrModbusNotConnected(t *testing.T) {
	t.Parallel()

	_, addr := startTestServer(t)
	c := newClient(t, addr, 0)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_, err := c.ReadHolding(ctx, 0, 1)
	require.Error(t, err)
	assert.Contains(t, strings.ToLower(err.Error()), "not connected")
}

func TestConcurrent_OpsSerialized(t *testing.T) {
	t.Parallel()

	s, addr := startTestServer(t)
	for i := 0; i < 16; i++ {
		//nolint:gosec // i is bounded [0,15] by the loop, safe to narrow to uint16
		s.HoldingRegisters[i] = uint16(i + 1)
	}

	const guard = 50 * time.Millisecond
	c := newConnectedClient(t, addr, guard)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	const n = 8
	start := time.Now()
	var wg sync.WaitGroup
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, err := c.ReadHolding(ctx, 0, 4)
			assert.NoError(t, err)
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	assert.GreaterOrEqual(t, elapsed, time.Duration(n-1)*guard,
		"concurrent ops must serialize: got %s for %d ops with guard %s",
		elapsed, n, guard)
}

func TestContext_Cancelled_DuringGuardInterval(t *testing.T) {
	t.Parallel()

	s, addr := startTestServer(t)
	s.HoldingRegisters[0] = 0x0000

	const guard = 1 * time.Second
	c := newConnectedClient(t, addr, guard)

	primingCtx, primingCancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer primingCancel()
	_, err := c.ReadHolding(primingCtx, 0, 1)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(50 * time.Millisecond)
		cancel()
	}()
	start := time.Now()
	_, err = c.ReadHolding(ctx, 0, 1)
	elapsed := time.Since(start)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Less(t, elapsed, guard,
		"ctx cancel during guard wait should return immediately, got %s", elapsed)
}
