package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
)

type fakeAvailSrc struct {
	mu   sync.Mutex
	snap entity.Snapshot
}

func (f *fakeAvailSrc) set(s entity.Snapshot) {
	f.mu.Lock()
	f.snap = s
	f.mu.Unlock()
}

func (f *fakeAvailSrc) Snapshot() entity.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap
}

type mutableClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *mutableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *mutableClock) set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

func newFastManager(t *testing.T, src *fakeAvailSrc, clk *mutableClock, threshold time.Duration) *AvailabilityManager {
	t.Helper()
	a := NewAvailabilityManager(src, threshold, clk, nil)
	a.withCheckInterval(5 * time.Millisecond)
	return a
}

func TestNewAvailabilityManager_NilSrcPanics(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() {
		_ = NewAvailabilityManager(nil, time.Second, nil, nil)
	})
}

func TestNewAvailabilityManager_DefaultsClockAndLogger(t *testing.T) {
	t.Parallel()
	src := &fakeAvailSrc{}
	a := NewAvailabilityManager(src, time.Second, nil, nil)
	assert.NotNil(t, a)
}

func TestWithCheckInterval_IgnoresNonPositive(t *testing.T) {
	t.Parallel()
	src := &fakeAvailSrc{}
	a := NewAvailabilityManager(src, time.Second, nil, nil)
	a.withCheckInterval(0)
	a.withCheckInterval(-time.Second)
	assert.Equal(t, 2*time.Second, a.checkInterval)
}

func TestInitial_OfflineUntilFirstFreshPoll(t *testing.T) {
	t.Parallel()

	src := &fakeAvailSrc{}
	clk := &mutableClock{now: time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)}
	a := newFastManager(t, src, clk, 30*time.Second)

	var offlineCalls atomic.Int32
	var onlineCalls atomic.Int32
	a.SetCallbacks(
		func() { onlineCalls.Add(1) },
		func() { offlineCalls.Add(1) },
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.Start(ctx)

	// snapshot has zero PolledAt, so it stays stale → no transition (already offline).
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(0), offlineCalls.Load())
	assert.Equal(t, int32(0), onlineCalls.Load())
}

func TestTransitionsToOnline(t *testing.T) {
	t.Parallel()

	src := &fakeAvailSrc{}
	clk := &mutableClock{now: time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)}
	a := newFastManager(t, src, clk, 30*time.Second)

	var offlineCalls atomic.Int32
	var onlineCalls atomic.Int32
	a.SetCallbacks(
		func() { onlineCalls.Add(1) },
		func() { offlineCalls.Add(1) },
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.Start(ctx)

	// Inject fresh poll.
	src.set(entity.Snapshot{PolledAt: clk.Now()})

	require.Eventually(t, func() bool {
		return onlineCalls.Load() == 1
	}, 500*time.Millisecond, 5*time.Millisecond)
	assert.Equal(t, int32(0), offlineCalls.Load())
}

func TestTransitionsBackToOffline(t *testing.T) {
	t.Parallel()

	src := &fakeAvailSrc{}
	clk := &mutableClock{now: time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)}
	a := newFastManager(t, src, clk, 100*time.Millisecond)

	var offlineCalls atomic.Int32
	var onlineCalls atomic.Int32
	a.SetCallbacks(
		func() { onlineCalls.Add(1) },
		func() { offlineCalls.Add(1) },
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.Start(ctx)

	// Step 1: fresh snapshot → online.
	src.set(entity.Snapshot{PolledAt: clk.Now()})
	require.Eventually(t, func() bool {
		return onlineCalls.Load() == 1
	}, 500*time.Millisecond, 5*time.Millisecond)

	// Step 2: advance clock past threshold without updating snapshot → offline.
	clk.set(clk.Now().Add(1 * time.Second))
	require.Eventually(t, func() bool {
		return offlineCalls.Load() == 1
	}, 500*time.Millisecond, 5*time.Millisecond)
}

func TestCallback_NotInvokedWithoutTransition(t *testing.T) {
	t.Parallel()

	src := &fakeAvailSrc{}
	clk := &mutableClock{now: time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)}
	a := newFastManager(t, src, clk, 30*time.Second)

	var onlineCalls atomic.Int32
	a.SetCallbacks(
		func() { onlineCalls.Add(1) },
		func() {},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.Start(ctx)

	src.set(entity.Snapshot{PolledAt: clk.Now()})
	require.Eventually(t, func() bool {
		return onlineCalls.Load() == 1
	}, 500*time.Millisecond, 5*time.Millisecond)

	// Many further fresh ticks should not retrigger.
	for i := 0; i < 20; i++ {
		clk.mu.Lock()
		clk.now = clk.now.Add(10 * time.Millisecond)
		clk.mu.Unlock()
		src.set(entity.Snapshot{PolledAt: clk.Now()})
		time.Sleep(7 * time.Millisecond)
	}
	assert.Equal(t, int32(1), onlineCalls.Load())
}

func TestCallbacks_CanBeNil(t *testing.T) {
	t.Parallel()

	src := &fakeAvailSrc{}
	clk := &mutableClock{now: time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)}
	a := newFastManager(t, src, clk, 30*time.Second)
	// no SetCallbacks call.

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.Start(ctx)

	src.set(entity.Snapshot{PolledAt: clk.Now()})
	time.Sleep(50 * time.Millisecond)
	// Smoke: no panic with nil callbacks even after a transition.
}

func TestDone_ClosesAfterCancel(t *testing.T) {
	t.Parallel()

	src := &fakeAvailSrc{}
	clk := &mutableClock{now: time.Now()}
	a := newFastManager(t, src, clk, time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	a.Start(ctx)
	cancel()

	select {
	case <-a.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Done not closed within 500ms")
	}
}
