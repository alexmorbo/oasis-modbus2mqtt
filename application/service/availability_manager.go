package service

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/port"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
)

// AvailabilitySnapshot is the read-only view of the latest Snapshot used
// by AvailabilityManager. *Poller satisfies it via its Snapshot method.
type AvailabilitySnapshot interface {
	// Snapshot returns the latest Snapshot known to the source.
	Snapshot() entity.Snapshot
}

// AvailabilityManager observes Snapshot.PolledAt staleness and fires
// online/offline transition callbacks. It runs a single goroutine that
// ticks at checkInterval (default 2s; tests use withCheckInterval).
//
// Initial state is offline: a controller has not yet been observed alive
// when Start runs, so we never publish "online" before the first fresh
// snapshot arrives.
type AvailabilityManager struct {
	src           AvailabilitySnapshot
	threshold     time.Duration
	checkInterval time.Duration
	clock         port.Clock
	logger        *slog.Logger

	mu        sync.Mutex
	onOnline  func()
	onOffline func()
	isOffline bool

	done chan struct{}
}

// NewAvailabilityManager constructs an AvailabilityManager. A nil src
// panics. checkInterval defaults to 2s; tests can adjust via
// withCheckInterval. A nil clock falls back to port.RealClock; a nil
// logger to slog.Default. Initial state is offline.
func NewAvailabilityManager(
	src AvailabilitySnapshot,
	threshold time.Duration,
	clock port.Clock,
	logger *slog.Logger,
) *AvailabilityManager {
	if src == nil {
		panic("availability manager: src must not be nil")
	}
	if clock == nil {
		clock = port.RealClock{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &AvailabilityManager{
		src:           src,
		threshold:     threshold,
		checkInterval: 2 * time.Second,
		clock:         clock,
		logger:        logger,
		isOffline:     true,
		done:          make(chan struct{}),
	}
}

// withCheckInterval is a test-only escape hatch (used by _test.go files in
// the same package) to override the default 2s tick.
func (a *AvailabilityManager) withCheckInterval(d time.Duration) {
	if d <= 0 {
		return
	}
	a.checkInterval = d
}

// SetCallbacks installs the transition callbacks. main.go wires these
// after the publisher use case is constructed; either may be nil.
func (a *AvailabilityManager) SetCallbacks(onOnline, onOffline func()) {
	a.mu.Lock()
	a.onOnline = onOnline
	a.onOffline = onOffline
	a.mu.Unlock()
}

// Done returns a channel closed once the goroutine has exited.
func (a *AvailabilityManager) Done() <-chan struct{} {
	return a.done
}

// Start launches the watcher goroutine and returns immediately.
func (a *AvailabilityManager) Start(ctx context.Context) {
	go a.run(ctx)
}

func (a *AvailabilityManager) run(ctx context.Context) {
	defer close(a.done)
	ticker := time.NewTicker(a.checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.tick()
		}
	}
}

func (a *AvailabilityManager) tick() {
	snap := a.src.Snapshot()
	now := a.clock.Now()
	stale := snap.PolledAt.IsZero() || now.Sub(snap.PolledAt) > a.threshold

	a.mu.Lock()
	transition := false
	if stale && !a.isOffline {
		a.isOffline = true
		transition = true
	} else if !stale && a.isOffline {
		a.isOffline = false
		transition = true
	}
	nowOffline := a.isOffline
	onOnline := a.onOnline
	onOffline := a.onOffline
	a.mu.Unlock()

	if !transition {
		return
	}
	if nowOffline {
		a.logger.Info("availability: offline")
		if onOffline != nil {
			onOffline()
		}
	} else {
		a.logger.Info("availability: online")
		if onOnline != nil {
			onOnline()
		}
	}
}
