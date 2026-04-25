package service

import (
	"math/rand/v2"
	"time"

	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/config"
)

// Backoff produces an exponentially growing sequence of delays, capped at a
// maximum and optionally perturbed by symmetric jitter.
//
// Backoff is NOT thread-safe; it is designed for single-goroutine use by
// ConnectionSupervisor. Each call to Next advances internal state.
type Backoff struct {
	min     time.Duration
	max     time.Duration
	factor  float64
	jitter  float64
	rng     *rand.Rand
	current time.Duration
}

// NewBackoff constructs a Backoff seeded from the current wall-clock time.
// The first Next call returns approximately cfg.MinDelay (exact when jitter
// is zero).
func NewBackoff(cfg config.ReconnectConfig) *Backoff {
	seed := uint64(time.Now().UnixNano())                                                //nolint:gosec
	return NewBackoffWithRand(cfg, rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))) //nolint:gosec
}

// NewBackoffWithRand constructs a Backoff using the supplied random source.
// It exists so tests can pin jitter to a deterministic seed.
func NewBackoffWithRand(cfg config.ReconnectConfig, rng *rand.Rand) *Backoff {
	return &Backoff{
		min:     cfg.MinDelay,
		max:     cfg.MaxDelay,
		factor:  cfg.Factor,
		jitter:  cfg.JitterPct,
		rng:     rng,
		current: cfg.MinDelay,
	}
}

// Next returns the next delay and advances internal state. The returned
// value is the current base delay perturbed by symmetric jitter
// (actual = base * (1 + (rand_in_-1_+1) * jitter)). The base delay is then
// multiplied by factor and clamped at max for the subsequent call.
func (b *Backoff) Next() time.Duration {
	actual := b.current
	if b.jitter > 0 {
		mult := 1 + (b.rng.Float64()*2-1)*b.jitter
		actual = time.Duration(float64(b.current) * mult)
	}
	if actual < 0 {
		actual = 0
	}

	next := time.Duration(float64(b.current) * b.factor)
	if next > b.max {
		next = b.max
	}
	b.current = next

	return actual
}

// Reset returns the Backoff to its initial state so the next Next call
// returns the configured minimum delay (with jitter applied).
func (b *Backoff) Reset() {
	b.current = b.min
}
