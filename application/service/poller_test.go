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

func TestMergeSlow_CopiesFilterDays(t *testing.T) {
	t.Parallel()

	fns := &fakePollFns{
		hotSnap:  entity.Snapshot{PowerOn: true, FilterPct: 20},
		medSnap:  entity.Snapshot{RoomHumidity: 70},
		slowSnap: entity.Snapshot{DeviceID: 1234, FilterResource: 150, FilterDaysLeft: 120, FilterPolledAt: time.Unix(100, 0)},
	}
	p := fastPoller(t, fns)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	assert.Eventually(t, func() bool {
		return fns.hotN.Load() >= 3 && fns.slowN.Load() >= 2
	}, 1*time.Second, 1*time.Millisecond)

	s := p.Snapshot()
	assert.Equal(t, uint16(150), s.FilterResource)
	assert.Equal(t, int16(120), s.FilterDaysLeft)
	assert.Equal(t, int16(20), s.FilterPct, "hot-tier filter_clog must survive slow merges")
}

func TestMergeSlow_FilterFieldsGatedOnFilterPolledAt(t *testing.T) {
	t.Parallel()

	prevAt := time.Unix(100, 0)
	newAt := time.Unix(200, 0)
	prev := entity.Snapshot{FilterResource: 150, FilterDaysLeft: 120, FilterPolledAt: prevAt, DeviceID: 1}

	t.Run("partial without filter read keeps prev", func(t *testing.T) {
		t.Parallel()
		got := service.MergeSlowForTest(prev, entity.Snapshot{DeviceID: 2, PolledAt: newAt})
		assert.Equal(t, uint16(2), got.DeviceID, "mandatory slow fields still merge")
		assert.Equal(t, uint16(150), got.FilterResource)
		assert.Equal(t, int16(120), got.FilterDaysLeft)
		assert.Equal(t, prevAt, got.FilterPolledAt)
	})

	t.Run("partial with filter read overwrites", func(t *testing.T) {
		t.Parallel()
		got := service.MergeSlowForTest(prev, entity.Snapshot{
			DeviceID: 2, FilterResource: 180, FilterDaysLeft: -3, FilterPolledAt: newAt, PolledAt: newAt,
		})
		assert.Equal(t, uint16(180), got.FilterResource)
		assert.Equal(t, int16(-3), got.FilterDaysLeft)
		assert.Equal(t, newAt, got.FilterPolledAt)
	})

	t.Run("never read stays zero", func(t *testing.T) {
		t.Parallel()
		got := service.MergeSlowForTest(entity.Snapshot{}, entity.Snapshot{DeviceID: 2, PolledAt: newAt})
		assert.True(t, got.FilterPolledAt.IsZero())
		assert.Zero(t, got.FilterDaysLeft)
	})
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

// gatedSlowFns returns slowSnap from PollSlow. When gate is set, PollSlow
// closes entered and blocks until gate is closed, simulating a slow poll that
// has already "read" the registers but not yet merged.
type gatedSlowFns struct {
	mu       sync.Mutex
	slowSnap entity.Snapshot
	entered  chan struct{}
	gate     chan struct{}
}

func (f *gatedSlowFns) PollHot(context.Context) (entity.Snapshot, error) {
	return entity.Snapshot{}, nil
}
func (f *gatedSlowFns) PollMedium(context.Context) (entity.Snapshot, error) {
	return entity.Snapshot{}, nil
}

func (f *gatedSlowFns) PollSlow(ctx context.Context) (entity.Snapshot, error) {
	f.mu.Lock()
	snap, entered, gate := f.slowSnap, f.entered, f.gate
	f.mu.Unlock()
	if gate != nil {
		close(entered)
		select {
		case <-gate:
		case <-ctx.Done():
			return entity.Snapshot{}, ctx.Err()
		}
	}
	return snap, nil
}

func (f *gatedSlowFns) arm(snap entity.Snapshot) (entered, gate chan struct{}) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.slowSnap = snap
	f.entered = make(chan struct{})
	f.gate = make(chan struct{})
	return f.entered, f.gate
}

func idlePoller(fns service.PollFns) *service.Poller {
	return service.NewPoller(fns, nil, service.PollerConfig{
		HotInterval: time.Hour, MediumInterval: time.Hour, SlowInterval: time.Hour,
	}, port.RealClock{}, nil)
}

type snapRecorder struct {
	mu    sync.Mutex
	snaps []entity.Snapshot
}

func (r *snapRecorder) record(s entity.Snapshot) {
	r.mu.Lock()
	r.snaps = append(r.snaps, s)
	r.mu.Unlock()
}

func (r *snapRecorder) all() []entity.Snapshot {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]entity.Snapshot(nil), r.snaps...)
}

func slowWithSettings(fw uint16, keys uint16, at time.Time) entity.Snapshot {
	return entity.Snapshot{
		DeviceID:         fw,
		Settings:         entity.PanelSettings{UserKeys0: keys, HumidityTarget: 50, CO2Target: 500},
		SettingsPolledAt: at,
		PolledAt:         at,
	}
}

func TestMergeSlow_SettingsGatedOnSettingsPolledAt(t *testing.T) {
	t.Parallel()

	prevAt := time.Unix(100, 0)
	newAt := time.Unix(200, 0)
	prev := entity.Snapshot{Settings: entity.PanelSettings{UserKeys0: 0x07E4}, SettingsPolledAt: prevAt, DeviceID: 1}

	t.Run("partial without settings read keeps prev", func(t *testing.T) {
		t.Parallel()
		got := service.MergeSlowForTest(prev, entity.Snapshot{DeviceID: 2, PolledAt: newAt})
		assert.Equal(t, uint16(2), got.DeviceID, "mandatory slow fields still merge")
		assert.Equal(t, uint16(0x07E4), got.Settings.UserKeys0)
		assert.Equal(t, prevAt, got.SettingsPolledAt)
	})

	t.Run("partial with settings read overwrites", func(t *testing.T) {
		t.Parallel()
		got := service.MergeSlowForTest(prev, entity.Snapshot{
			DeviceID: 2, Settings: entity.PanelSettings{UserKeys0: 0x47A4}, SettingsPolledAt: newAt, PolledAt: newAt,
		})
		assert.Equal(t, uint16(0x47A4), got.Settings.UserKeys0)
		assert.Equal(t, newAt, got.SettingsPolledAt)
	})

	t.Run("never read stays zero", func(t *testing.T) {
		t.Parallel()
		got := service.MergeSlowForTest(entity.Snapshot{}, entity.Snapshot{DeviceID: 2, PolledAt: newAt})
		assert.True(t, got.SettingsPolledAt.IsZero())
		assert.Equal(t, entity.PanelSettings{}, got.Settings)
	})
}

func TestMergeSettingRegister_MergesAndFansOut(t *testing.T) {
	t.Parallel()

	fns := &gatedSlowFns{slowSnap: slowWithSettings(7, 0x07E4, time.Unix(100, 0))}
	p := idlePoller(fns)
	rec := &snapRecorder{}
	p.Subscribe(rec.record)

	p.PollSlowOnceForTest(context.Background())
	require.Len(t, rec.all(), 1)

	require.True(t, p.MergeSettingRegister(22, 0x07A4))
	got := rec.all()
	require.Len(t, got, 2, "read-back merge fans out like a tier merge")
	assert.Equal(t, uint16(0x07A4), got[1].Settings.UserKeys0)
	assert.Equal(t, uint16(50), got[1].Settings.HumidityTarget, "other settings registers kept")
	assert.Equal(t, uint16(7), got[1].DeviceID, "non-settings fields kept")
	assert.Equal(t, uint16(0x07A4), p.Snapshot().Settings.UserKeys0)

	assert.False(t, p.MergeSettingRegister(26, 1), "unknown register is not merged")
	assert.Len(t, rec.all(), 2, "no fan-out for an unknown register")
}

func TestMergeSettingRegister_BeforeFirstRead_NoMergeNoFanOut(t *testing.T) {
	t.Parallel()

	t.Run("nothing polled yet", func(t *testing.T) {
		t.Parallel()
		p := idlePoller(&gatedSlowFns{})
		rec := &snapRecorder{}
		p.Subscribe(rec.record)

		assert.False(t, p.MergeSettingRegister(22, 0x07A4))
		assert.Empty(t, rec.all())
		assert.True(t, p.Snapshot().SettingsPolledAt.IsZero())
	})

	t.Run("in-flight first read is not published after a write", func(t *testing.T) {
		t.Parallel()
		fns := &gatedSlowFns{}
		p := idlePoller(fns)
		entered, gate := fns.arm(slowWithSettings(7, 0x07E4, time.Unix(100, 0)))

		done := make(chan struct{})
		go func() {
			p.PollSlowOnceForTest(context.Background())
			close(done)
		}()
		<-entered
		assert.False(t, p.MergeSettingRegister(22, 0x07A4))
		close(gate)
		<-done

		s := p.Snapshot()
		assert.True(t, s.SettingsPolledAt.IsZero(), "a pre-write first read must wait for the next slow poll")
		assert.Equal(t, uint16(7), s.DeviceID, "non-settings slow fields still merge")
	})
}

// TestPoller_InFlightSlowPollDoesNotRevertReadBack: PollSlow has read the old
// h22 when a write is read back and merged; the late slow merge must keep the
// read-back h22 while still merging the slow poll's other fields.
func TestPoller_InFlightSlowPollDoesNotRevertReadBack(t *testing.T) {
	t.Parallel()

	fns := &gatedSlowFns{slowSnap: slowWithSettings(1, 0x07E4, time.Unix(100, 0))}
	p := idlePoller(fns)
	rec := &snapRecorder{}
	p.Subscribe(rec.record)
	p.PollSlowOnceForTest(context.Background())
	require.Equal(t, uint16(0x07E4), p.Snapshot().Settings.UserKeys0)

	entered, gate := fns.arm(slowWithSettings(2, 0x07E4, time.Unix(200, 0)))
	done := make(chan struct{})
	go func() {
		p.PollSlowOnceForTest(context.Background())
		close(done)
	}()
	<-entered

	require.True(t, p.MergeSettingRegister(22, 0x07A4))
	close(gate)
	<-done

	s := p.Snapshot()
	assert.Equal(t, uint16(0x07A4), s.Settings.UserKeys0, "stale in-flight read must not revert the read-back")
	assert.Equal(t, uint16(2), s.DeviceID, "the slow poll's non-settings fields still merge")
	assert.Equal(t, time.Unix(100, 0), s.SettingsPolledAt)

	all := rec.all()
	require.Len(t, all, 3)
	assert.Equal(t, uint16(0x07A4), all[2].Settings.UserKeys0, "the late slow fan-out carries the read-back value")

	fns.mu.Lock()
	fns.gate, fns.entered = nil, nil
	fns.slowSnap = slowWithSettings(3, 0x07A4, time.Unix(300, 0))
	fns.mu.Unlock()
	p.PollSlowOnceForTest(context.Background())
	assert.Equal(t, time.Unix(300, 0), p.Snapshot().SettingsPolledAt, "the next slow poll merges settings again")
}

func TestPoller_ConcurrentMergeSettingRegisterAndPolls(t *testing.T) {
	t.Parallel()

	fns := &fakePollFns{slowSnap: slowWithSettings(1, 0x07E4, time.Unix(100, 0))}
	p := fastPoller(t, fns)
	var fanOut atomic.Int32
	p.Subscribe(func(entity.Snapshot) { fanOut.Add(1) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)
	require.Eventually(t, func() bool { return !p.Snapshot().SettingsPolledAt.IsZero() }, time.Second, time.Millisecond)

	var wg sync.WaitGroup
	for i := range uint16(50) {
		wg.Add(1)
		go func() {
			defer wg.Done()
			p.MergeSettingRegister(35, 20+i)
		}()
	}
	wg.Wait()
	cancel()
	<-p.Done()
	assert.Positive(t, fanOut.Load())
}
