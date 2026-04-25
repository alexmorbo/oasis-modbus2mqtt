package service_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/port"
	"github.com/alexmorbo/oasis-modbus2mqtt/application/service"
)

// fakeClient is a hand-written mock of port.ModbusClient. Each *Fn field
// overrides the default no-op behaviour for that method.
type fakeClient struct {
	mu sync.Mutex

	readInputFn     func(ctx context.Context, start, count uint16) ([]uint16, error)
	readHoldingFn   func(ctx context.Context, start, count uint16) ([]uint16, error)
	writeHoldingFn  func(ctx context.Context, addr, value uint16) error
	modifyHoldingFn func(ctx context.Context, addr uint16, modify func(uint16) uint16) error
	connectedFlag   bool

	readInputCalls     int
	readHoldingCalls   int
	writeHoldingCalls  int
	modifyHoldingCalls int
}

func (f *fakeClient) ReadInput(ctx context.Context, start, count uint16) ([]uint16, error) {
	f.mu.Lock()
	f.readInputCalls++
	fn := f.readInputFn
	f.mu.Unlock()
	if fn == nil {
		return nil, nil
	}
	return fn(ctx, start, count)
}

func (f *fakeClient) ReadHolding(ctx context.Context, start, count uint16) ([]uint16, error) {
	f.mu.Lock()
	f.readHoldingCalls++
	fn := f.readHoldingFn
	f.mu.Unlock()
	if fn == nil {
		return nil, nil
	}
	return fn(ctx, start, count)
}

func (f *fakeClient) WriteHolding(ctx context.Context, addr, value uint16) error {
	f.mu.Lock()
	f.writeHoldingCalls++
	fn := f.writeHoldingFn
	f.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(ctx, addr, value)
}

func (f *fakeClient) ModifyHolding(ctx context.Context, addr uint16, modify func(uint16) uint16) error {
	f.mu.Lock()
	f.modifyHoldingCalls++
	fn := f.modifyHoldingFn
	f.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(ctx, addr, modify)
}

func (f *fakeClient) Connected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connectedFlag
}

// fakeNotifier counts Trigger calls.
type fakeNotifier struct {
	mu    sync.Mutex
	count int
}

func (n *fakeNotifier) Trigger() {
	n.mu.Lock()
	n.count++
	n.mu.Unlock()
}

func (n *fakeNotifier) Count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.count
}

// compile-time assertion that fakeClient satisfies the port.
var _ port.ModbusClient = (*fakeClient)(nil)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newDispatcher(client *fakeClient, notifier *fakeNotifier) *service.CommandDispatcher {
	return service.NewCommandDispatcher(client, notifier, 3, discardLogger())
}

func TestNewCommandDispatcher_PanicsOnNilClient(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() {
		service.NewCommandDispatcher(nil, &fakeNotifier{}, 3, discardLogger())
	})
}

func TestNewCommandDispatcher_PanicsOnNilNotifier(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() {
		service.NewCommandDispatcher(&fakeClient{}, nil, 3, discardLogger())
	})
}

func TestNewCommandDispatcher_PanicsOnZeroThreshold(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() {
		service.NewCommandDispatcher(&fakeClient{}, &fakeNotifier{}, 0, discardLogger())
	})
}

func TestNewCommandDispatcher_PanicsOnNegativeThreshold(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() {
		service.NewCommandDispatcher(&fakeClient{}, &fakeNotifier{}, -1, discardLogger())
	})
}

func TestNewCommandDispatcher_NilLoggerFallsBackToDefault(t *testing.T) {
	t.Parallel()

	client := &fakeClient{
		readInputFn: func(_ context.Context, _, _ uint16) ([]uint16, error) {
			return []uint16{1}, nil
		},
	}
	notifier := &fakeNotifier{}

	d := service.NewCommandDispatcher(client, notifier, 3, nil)
	require.NotNil(t, d)

	got, err := d.ReadInput(context.Background(), 0, 1)
	require.NoError(t, err)
	require.Equal(t, []uint16{1}, got)
}

func TestReadInput_Success(t *testing.T) {
	t.Parallel()

	client := &fakeClient{
		readInputFn: func(_ context.Context, start, count uint16) ([]uint16, error) {
			assert.Equal(t, uint16(10), start)
			assert.Equal(t, uint16(2), count)
			return []uint16{0xAA, 0xBB}, nil
		},
	}
	notifier := &fakeNotifier{}
	d := newDispatcher(client, notifier)

	got, err := d.ReadInput(context.Background(), 10, 2)
	require.NoError(t, err)
	assert.Equal(t, []uint16{0xAA, 0xBB}, got)
	assert.Equal(t, 0, notifier.Count())
}

func TestReadInput_Failure(t *testing.T) {
	t.Parallel()

	want := errors.New("boom")
	client := &fakeClient{
		readInputFn: func(_ context.Context, _, _ uint16) ([]uint16, error) {
			return nil, want
		},
	}
	notifier := &fakeNotifier{}
	d := newDispatcher(client, notifier)

	_, err := d.ReadInput(context.Background(), 0, 1)
	require.ErrorIs(t, err, want)
	assert.Equal(t, 0, notifier.Count())
}

func TestReadHolding_Success(t *testing.T) {
	t.Parallel()

	client := &fakeClient{
		readHoldingFn: func(_ context.Context, _, _ uint16) ([]uint16, error) {
			return []uint16{1, 2, 3}, nil
		},
	}
	notifier := &fakeNotifier{}
	d := newDispatcher(client, notifier)

	got, err := d.ReadHolding(context.Background(), 0, 3)
	require.NoError(t, err)
	assert.Equal(t, []uint16{1, 2, 3}, got)
}

func TestReadHolding_Failure(t *testing.T) {
	t.Parallel()

	want := errors.New("rh-boom")
	client := &fakeClient{
		readHoldingFn: func(_ context.Context, _, _ uint16) ([]uint16, error) {
			return nil, want
		},
	}
	notifier := &fakeNotifier{}
	d := newDispatcher(client, notifier)

	_, err := d.ReadHolding(context.Background(), 0, 1)
	require.ErrorIs(t, err, want)
}

func TestWriteHolding_Success(t *testing.T) {
	t.Parallel()

	client := &fakeClient{
		writeHoldingFn: func(_ context.Context, addr, value uint16) error {
			assert.Equal(t, uint16(7), addr)
			assert.Equal(t, uint16(0xCAFE), value)
			return nil
		},
	}
	notifier := &fakeNotifier{}
	d := newDispatcher(client, notifier)

	require.NoError(t, d.WriteHolding(context.Background(), 7, 0xCAFE))
}

func TestWriteHolding_Failure(t *testing.T) {
	t.Parallel()

	want := errors.New("wh-boom")
	client := &fakeClient{
		writeHoldingFn: func(_ context.Context, _, _ uint16) error {
			return want
		},
	}
	notifier := &fakeNotifier{}
	d := newDispatcher(client, notifier)

	require.ErrorIs(t, d.WriteHolding(context.Background(), 0, 0), want)
}

func TestModifyHolding_Success(t *testing.T) {
	t.Parallel()

	client := &fakeClient{
		modifyHoldingFn: func(_ context.Context, addr uint16, modify func(uint16) uint16) error {
			assert.Equal(t, uint16(3), addr)
			assert.Equal(t, uint16(0x10), modify(0x0F))
			return nil
		},
	}
	notifier := &fakeNotifier{}
	d := newDispatcher(client, notifier)

	err := d.ModifyHolding(context.Background(), 3, func(v uint16) uint16 { return v + 1 })
	require.NoError(t, err)
}

func TestModifyHolding_Failure(t *testing.T) {
	t.Parallel()

	want := errors.New("mh-boom")
	client := &fakeClient{
		modifyHoldingFn: func(_ context.Context, _ uint16, _ func(uint16) uint16) error {
			return want
		},
	}
	notifier := &fakeNotifier{}
	d := newDispatcher(client, notifier)

	err := d.ModifyHolding(context.Background(), 0, func(v uint16) uint16 { return v })
	require.ErrorIs(t, err, want)
}

func TestSuccess_ResetsCounter(t *testing.T) {
	t.Parallel()

	var fail bool
	client := &fakeClient{
		readInputFn: func(_ context.Context, _, _ uint16) ([]uint16, error) {
			if fail {
				return nil, errors.New("x")
			}
			return []uint16{1}, nil
		},
	}
	notifier := &fakeNotifier{}
	d := newDispatcher(client, notifier)

	fail = true
	_, _ = d.ReadInput(context.Background(), 0, 1)
	_, _ = d.ReadInput(context.Background(), 0, 1)
	require.Equal(t, 0, notifier.Count())

	fail = false
	_, err := d.ReadInput(context.Background(), 0, 1)
	require.NoError(t, err)

	fail = true
	_, _ = d.ReadInput(context.Background(), 0, 1)
	_, _ = d.ReadInput(context.Background(), 0, 1)
	assert.Equal(t, 0, notifier.Count(), "counter must have reset, threshold not yet reached")
}

func TestThresholdReached_TriggersNotifier(t *testing.T) {
	t.Parallel()

	client := &fakeClient{
		readInputFn: func(_ context.Context, _, _ uint16) ([]uint16, error) {
			return nil, errors.New("x")
		},
	}
	notifier := &fakeNotifier{}
	d := newDispatcher(client, notifier)

	for i := 0; i < 3; i++ {
		_, _ = d.ReadInput(context.Background(), 0, 1)
	}
	assert.Equal(t, 1, notifier.Count())
}

func TestAboveThreshold_TriggersAgain(t *testing.T) {
	t.Parallel()

	client := &fakeClient{
		readInputFn: func(_ context.Context, _, _ uint16) ([]uint16, error) {
			return nil, errors.New("x")
		},
	}
	notifier := &fakeNotifier{}
	d := newDispatcher(client, notifier)

	for i := 0; i < 5; i++ {
		_, _ = d.ReadInput(context.Background(), 0, 1)
	}
	// 3rd, 4th and 5th errors all trigger.
	assert.Equal(t, 3, notifier.Count())
}

func TestThresholdMixedOps_AccumulatesAcrossMethods(t *testing.T) {
	t.Parallel()

	boom := errors.New("x")
	client := &fakeClient{
		readInputFn:    func(_ context.Context, _, _ uint16) ([]uint16, error) { return nil, boom },
		readHoldingFn:  func(_ context.Context, _, _ uint16) ([]uint16, error) { return nil, boom },
		writeHoldingFn: func(_ context.Context, _, _ uint16) error { return boom },
	}
	notifier := &fakeNotifier{}
	d := newDispatcher(client, notifier)

	_, _ = d.ReadInput(context.Background(), 0, 1)
	_, _ = d.ReadHolding(context.Background(), 0, 1)
	_ = d.WriteHolding(context.Background(), 0, 0)

	assert.Equal(t, 1, notifier.Count())
}

func TestConnected_ProxiesTrue(t *testing.T) {
	t.Parallel()

	client := &fakeClient{connectedFlag: true}
	d := newDispatcher(client, &fakeNotifier{})
	assert.True(t, d.Connected())
}

func TestConnected_ProxiesFalse(t *testing.T) {
	t.Parallel()

	client := &fakeClient{connectedFlag: false}
	d := newDispatcher(client, &fakeNotifier{})
	assert.False(t, d.Connected())
}
