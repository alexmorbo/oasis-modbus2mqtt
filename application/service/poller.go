package service

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/port"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/metrics"
)

// PollFns is the set of tier-poll callbacks the Poller orchestrates. The
// concrete *usecase.PollController satisfies this via duck typing — defining
// the interface here keeps application/service free of an
// application/usecase import.
type PollFns interface {
	// PollHot returns a Snapshot populated with the hot-tier fields.
	PollHot(ctx context.Context) (entity.Snapshot, error)
	// PollMedium returns a Snapshot populated with the medium-tier fields.
	PollMedium(ctx context.Context) (entity.Snapshot, error)
	// PollSlow returns a Snapshot populated with the slow-tier fields.
	PollSlow(ctx context.Context) (entity.Snapshot, error)
}

// SnapshotMerger merges a per-tier partial Snapshot into the previous full
// Snapshot. Implementations MUST copy only the tier-owned fields and leave
// the rest untouched, otherwise different tiers will overwrite each other.
type SnapshotMerger interface {
	// MergeHot copies hot-tier fields from partial onto prev.
	MergeHot(prev, partial entity.Snapshot) entity.Snapshot
	// MergeMedium copies medium-tier fields from partial onto prev.
	MergeMedium(prev, partial entity.Snapshot) entity.Snapshot
	// MergeSlow copies slow-tier fields from partial onto prev.
	MergeSlow(prev, partial entity.Snapshot) entity.Snapshot
}

// SnapshotSubscriber is invoked once per successful poll-and-merge. The
// callback runs synchronously inside the tier goroutine; it MUST NOT block
// or another tier's poll cadence will be delayed.
type SnapshotSubscriber func(s entity.Snapshot)

// PollerConfig declares the per-tier tick intervals.
type PollerConfig struct {
	HotInterval    time.Duration
	MediumInterval time.Duration
	SlowInterval   time.Duration
}

// Poller orchestrates three tier goroutines (hot/medium/slow) that
// periodically invoke the matching PollFns method, merge the partial
// Snapshot into the latest snapshot under a RWMutex, and notify
// subscribers. The merger is pluggable; defaultMerger is used when nil.
type Poller struct {
	ctrl   PollFns
	merger SnapshotMerger
	cfg    PollerConfig
	clock  port.Clock
	logger *slog.Logger

	mu     sync.RWMutex
	latest entity.Snapshot

	subMu       sync.Mutex
	subscribers []SnapshotSubscriber

	done chan struct{}
}

// NewPoller constructs a Poller. A nil ctrl panics; a nil merger falls back
// to the default merger; a nil clock to port.RealClock; a nil logger to
// slog.Default. Non-positive intervals panic.
func NewPoller(ctrl PollFns, merger SnapshotMerger, cfg PollerConfig, clock port.Clock, logger *slog.Logger) *Poller {
	if ctrl == nil {
		panic("poller: ctrl must not be nil")
	}
	if cfg.HotInterval <= 0 || cfg.MediumInterval <= 0 || cfg.SlowInterval <= 0 {
		panic("poller: tier intervals must be > 0")
	}
	if merger == nil {
		merger = defaultMerger{}
	}
	if clock == nil {
		clock = port.RealClock{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Poller{
		ctrl:   ctrl,
		merger: merger,
		cfg:    cfg,
		clock:  clock,
		logger: logger,
		done:   make(chan struct{}),
	}
}

// Subscribe registers cb to receive every merged Snapshot. Subscribers are
// invoked sequentially by the tier goroutine that produced the snapshot.
func (p *Poller) Subscribe(cb SnapshotSubscriber) {
	if cb == nil {
		return
	}
	p.subMu.Lock()
	p.subscribers = append(p.subscribers, cb)
	p.subMu.Unlock()
}

// Snapshot returns a copy of the latest merged Snapshot. Safe for
// concurrent use.
func (p *Poller) Snapshot() entity.Snapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.latest
}

// Done returns a channel closed after all three tier goroutines have
// exited following ctx cancellation.
func (p *Poller) Done() <-chan struct{} {
	return p.done
}

// Start launches the three tier goroutines and returns immediately. Each
// goroutine performs an immediate poll and then ticks at its configured
// interval until ctx is cancelled.
func (p *Poller) Start(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(3)
	go p.runTier(ctx, p.cfg.HotInterval, p.ctrl.PollHot, p.merger.MergeHot, "hot", &wg)
	go p.runTier(ctx, p.cfg.MediumInterval, p.ctrl.PollMedium, p.merger.MergeMedium, "medium", &wg)
	go p.runTier(ctx, p.cfg.SlowInterval, p.ctrl.PollSlow, p.merger.MergeSlow, "slow", &wg)
	go func() {
		wg.Wait()
		close(p.done)
	}()
}

func (p *Poller) runTier(
	ctx context.Context,
	tick time.Duration,
	fn func(context.Context) (entity.Snapshot, error),
	mergeFn func(prev, partial entity.Snapshot) entity.Snapshot,
	tier string,
	wg *sync.WaitGroup,
) {
	defer wg.Done()
	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	p.doPoll(ctx, fn, mergeFn, tier)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.doPoll(ctx, fn, mergeFn, tier)
		}
	}
}

func (p *Poller) doPoll(
	ctx context.Context,
	fn func(context.Context) (entity.Snapshot, error),
	mergeFn func(prev, partial entity.Snapshot) entity.Snapshot,
	tier string,
) {
	start := p.clock.Now()
	p.logger.Debug("tier poll start", slog.String("tier", tier))
	partial, err := fn(ctx)
	if err != nil {
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			return
		}
		p.logger.Warn("tier poll failed",
			slog.String("tier", tier),
			slog.Any("error", err),
		)
		metrics.ModbusErrorsTotal("poll_" + tier).Inc()
		return
	}

	p.mu.Lock()
	p.latest = mergeFn(p.latest, partial)
	merged := p.latest
	p.mu.Unlock()

	p.subMu.Lock()
	subs := make([]SnapshotSubscriber, len(p.subscribers))
	copy(subs, p.subscribers)
	p.subMu.Unlock()
	for _, cb := range subs {
		cb(merged)
	}

	metrics.PollDurationSeconds(tier).UpdateDuration(start)
	metrics.SetLastSuccessfulPoll(p.clock.Now())
}

// defaultMerger copies tier-owned fields between Snapshot values. It is the
// merger used when NewPoller is given a nil SnapshotMerger.
type defaultMerger struct{}

// MergeHot copies the hot-tier fields from partial onto prev.
func (defaultMerger) MergeHot(prev, partial entity.Snapshot) entity.Snapshot {
	prev.PowerOn = partial.PowerOn
	prev.Switching = partial.Switching
	prev.HeatCapable = partial.HeatCapable
	prev.CoolCapable = partial.CoolCapable
	prev.Operation = partial.Operation
	prev.OperationTimeLeft = partial.OperationTimeLeft
	prev.RawErrors[0] = partial.RawErrors[0]
	prev.RawErrors[1] = partial.RawErrors[1]
	prev.SupplyTemp = partial.SupplyTemp
	prev.FilterPct = partial.FilterPct
	prev.HeaterPWM = partial.HeaterPWM
	prev.DamperOpen = partial.DamperOpen
	prev.PIDDemand = partial.PIDDemand
	prev.FanState1 = partial.FanState1
	prev.FanState2 = partial.FanState2
	prev.TargetTemp = partial.TargetTemp
	prev.FanTarget1 = partial.FanTarget1
	prev.PolledAt = partial.PolledAt
	return prev
}

// MergeMedium copies the medium-tier fields from partial onto prev.
func (defaultMerger) MergeMedium(prev, partial entity.Snapshot) entity.Snapshot {
	prev.RoomTemp = partial.RoomTemp
	prev.RoomHumidity = partial.RoomHumidity
	prev.RawErrors[2] = partial.RawErrors[2]
	prev.RawErrors[3] = partial.RawErrors[3]
	prev.CurrentMode = partial.CurrentMode
	prev.PolledAt = partial.PolledAt
	return prev
}

// MergeSlow copies the slow-tier fields from partial onto prev.
func (defaultMerger) MergeSlow(prev, partial entity.Snapshot) entity.Snapshot {
	prev.Firmware = partial.Firmware
	prev.DeviceID = partial.DeviceID
	prev.DeviceConfig = partial.DeviceConfig
	prev.PolledAt = partial.PolledAt
	return prev
}
