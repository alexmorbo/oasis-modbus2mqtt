package service_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/port"
	"github.com/alexmorbo/oasis-modbus2mqtt/application/service"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
)

type fakePollFns struct {
	mu       sync.Mutex
	hotN     atomic.Int32
	medN     atomic.Int32
	slowN    atomic.Int32
	hotSnap  entity.Snapshot
	medSnap  entity.Snapshot
	slowSnap entity.Snapshot
	hotErr   error
	medErr   error
	slowErr  error
}

func (f *fakePollFns) setHotErr(err error) {
	f.mu.Lock()
	f.hotErr = err
	f.mu.Unlock()
}

func (f *fakePollFns) PollHot(_ context.Context) (entity.Snapshot, error) {
	f.hotN.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hotSnap, f.hotErr
}

func (f *fakePollFns) PollMedium(_ context.Context) (entity.Snapshot, error) {
	f.medN.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.medSnap, f.medErr
}

func (f *fakePollFns) PollSlow(_ context.Context) (entity.Snapshot, error) {
	f.slowN.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.slowSnap, f.slowErr
}

func fastPoller(t *testing.T, fns *fakePollFns) *service.Poller {
	t.Helper()
	cfg := service.PollerConfig{
		HotInterval:    2 * time.Millisecond,
		MediumInterval: 3 * time.Millisecond,
		SlowInterval:   4 * time.Millisecond,
	}
	return service.NewPoller(fns, nil, cfg, port.RealClock{}, nil)
}

func TestNewPoller_NilCtrlPanics(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() {
		_ = service.NewPoller(nil, nil, service.PollerConfig{
			HotInterval: time.Second, MediumInterval: time.Second, SlowInterval: time.Second,
		}, nil, nil)
	})
}

func TestNewPoller_ZeroIntervalsPanic(t *testing.T) {
	t.Parallel()
	fns := &fakePollFns{}
	assert.Panics(t, func() {
		_ = service.NewPoller(fns, nil, service.PollerConfig{}, nil, nil)
	})
}

func TestNewPoller_NegativeIntervalPanics(t *testing.T) {
	t.Parallel()
	fns := &fakePollFns{}
	assert.Panics(t, func() {
		_ = service.NewPoller(fns, nil, service.PollerConfig{
			HotInterval: -time.Second, MediumInterval: time.Second, SlowInterval: time.Second,
		}, nil, nil)
	})
}

func TestStart_RunsAllTiersImmediately(t *testing.T) {
	t.Parallel()

	fns := &fakePollFns{
		hotSnap:  entity.Snapshot{PowerOn: true},
		medSnap:  entity.Snapshot{RoomHumidity: 42},
		slowSnap: entity.Snapshot{DeviceID: 99},
	}
	p := fastPoller(t, fns)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	assert.Eventually(t, func() bool {
		return fns.hotN.Load() >= 1 && fns.medN.Load() >= 1 && fns.slowN.Load() >= 1
	}, 1*time.Second, 1*time.Millisecond)
}

func TestSnapshot_ReflectsLatestMerge(t *testing.T) {
	t.Parallel()

	fns := &fakePollFns{
		hotSnap:  entity.Snapshot{PowerOn: true, FanTarget1: 5},
		medSnap:  entity.Snapshot{RoomHumidity: 60},
		slowSnap: entity.Snapshot{DeviceID: 7},
	}
	p := fastPoller(t, fns)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	assert.Eventually(t, func() bool {
		s := p.Snapshot()
		return s.PowerOn && s.RoomHumidity == 60 && s.DeviceID == 7 && s.FanTarget1 == 5
	}, 1*time.Second, 1*time.Millisecond)
}

func TestSubscribe_NotifiedAfterEachMerge(t *testing.T) {
	t.Parallel()

	fns := &fakePollFns{hotSnap: entity.Snapshot{PowerOn: true}}
	p := fastPoller(t, fns)

	var calls atomic.Int32
	p.Subscribe(func(_ entity.Snapshot) {
		calls.Add(1)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	assert.Eventually(t, func() bool {
		return calls.Load() >= 3
	}, 1*time.Second, 1*time.Millisecond)
}

func TestSubscribe_NilCallbackIgnored(t *testing.T) {
	t.Parallel()

	fns := &fakePollFns{}
	p := fastPoller(t, fns)
	assert.NotPanics(t, func() {
		p.Subscribe(nil)
	})
}

func TestMerge_TierFieldIsolation(t *testing.T) {
	t.Parallel()

	fns := &fakePollFns{
		hotSnap:  entity.Snapshot{PowerOn: true, FanTarget1: 4},
		medSnap:  entity.Snapshot{RoomHumidity: 70},
		slowSnap: entity.Snapshot{DeviceID: 1234},
	}
	p := fastPoller(t, fns)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	assert.Eventually(t, func() bool {
		s := p.Snapshot()
		return s.PowerOn && s.RoomHumidity == 70 && s.DeviceID == 1234
	}, 1*time.Second, 1*time.Millisecond, "all tier fields must coexist after merges")
}

func TestPollFailure_DoesNotNotifySubscribers(t *testing.T) {
	t.Parallel()

	fns := &fakePollFns{}
	fns.setHotErr(errors.New("read fail"))
	fns.medErr = errors.New("read fail")
	fns.slowErr = errors.New("read fail")

	p := fastPoller(t, fns)

	var calls atomic.Int32
	p.Subscribe(func(_ entity.Snapshot) { calls.Add(1) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	// Wait for several attempts.
	assert.Eventually(t, func() bool {
		return fns.hotN.Load() >= 2
	}, 1*time.Second, 1*time.Millisecond)
	cancel()
	<-p.Done()
	assert.Equal(t, int32(0), calls.Load(), "subscribers must not be invoked on poll error")
}

func TestDone_ClosesWhenContextCancelled(t *testing.T) {
	t.Parallel()

	fns := &fakePollFns{}
	p := fastPoller(t, fns)
	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)

	cancel()

	select {
	case <-p.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Done() not closed within 500ms after cancel")
	}
}

type recordingMerger struct {
	hot, med, slow atomic.Int32
}

func (r *recordingMerger) MergeHot(prev, partial entity.Snapshot) entity.Snapshot {
	r.hot.Add(1)
	prev.PowerOn = partial.PowerOn
	return prev
}
func (r *recordingMerger) MergeMedium(prev, partial entity.Snapshot) entity.Snapshot {
	r.med.Add(1)
	prev.RoomHumidity = partial.RoomHumidity
	return prev
}
func (r *recordingMerger) MergeSlow(prev, partial entity.Snapshot) entity.Snapshot {
	r.slow.Add(1)
	prev.DeviceID = partial.DeviceID
	return prev
}

func TestCustomMerger_IsInvoked(t *testing.T) {
	t.Parallel()

	fns := &fakePollFns{
		hotSnap:  entity.Snapshot{PowerOn: true},
		medSnap:  entity.Snapshot{RoomHumidity: 5},
		slowSnap: entity.Snapshot{DeviceID: 9},
	}
	rm := &recordingMerger{}
	cfg := service.PollerConfig{
		HotInterval:    2 * time.Millisecond,
		MediumInterval: 3 * time.Millisecond,
		SlowInterval:   4 * time.Millisecond,
	}
	p := service.NewPoller(fns, rm, cfg, port.RealClock{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	require.Eventually(t, func() bool {
		return rm.hot.Load() >= 1 && rm.med.Load() >= 1 && rm.slow.Load() >= 1
	}, 1*time.Second, 1*time.Millisecond)
}

func TestNewPoller_DefaultsLoggerAndClock(t *testing.T) {
	t.Parallel()
	fns := &fakePollFns{}
	cfg := service.PollerConfig{
		HotInterval:    10 * time.Millisecond,
		MediumInterval: 10 * time.Millisecond,
		SlowInterval:   10 * time.Millisecond,
	}
	p := service.NewPoller(fns, nil, cfg, nil, nil)
	assert.NotNil(t, p)
}
