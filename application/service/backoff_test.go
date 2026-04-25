package service_test

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/service"
	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/config"
)

func newCfg(min, max time.Duration, jitter float64) config.ReconnectConfig {
	return config.ReconnectConfig{
		MinDelay:  min,
		MaxDelay:  max,
		Factor:    2.0,
		JitterPct: jitter,
	}
}

func TestBackoff_Sequence(t *testing.T) {
	t.Parallel()

	b := service.NewBackoffWithRand(
		newCfg(100*time.Millisecond, 1*time.Second, 0),
		rand.New(rand.NewPCG(1, 1)), //nolint:gosec
	)

	want := []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
		1 * time.Second,
		1 * time.Second,
	}
	for i, exp := range want {
		got := b.Next()
		assert.Equalf(t, exp, got, "Next call #%d", i+1)
	}
}

func TestBackoff_ZeroJitter(t *testing.T) {
	t.Parallel()

	b := service.NewBackoffWithRand(
		newCfg(50*time.Millisecond, 200*time.Millisecond, 0),
		rand.New(rand.NewPCG(99, 99)), //nolint:gosec
	)

	require.Equal(t, 50*time.Millisecond, b.Next())
	require.Equal(t, 100*time.Millisecond, b.Next())
	require.Equal(t, 200*time.Millisecond, b.Next())
	require.Equal(t, 200*time.Millisecond, b.Next())
}

func TestBackoff_Jitter(t *testing.T) {
	t.Parallel()

	b := service.NewBackoffWithRand(
		newCfg(100*time.Millisecond, 1*time.Second, 0.5),
		rand.New(rand.NewPCG(42, 42)), //nolint:gosec
	)

	got := b.Next()
	assert.GreaterOrEqual(t, got, 50*time.Millisecond, "first delay >= min*0.5")
	assert.LessOrEqual(t, got, 150*time.Millisecond, "first delay <= min*1.5")
}

func TestBackoff_JitterStaysPositive(t *testing.T) {
	t.Parallel()

	b := service.NewBackoffWithRand(
		newCfg(10*time.Millisecond, 100*time.Millisecond, 1.5),
		rand.New(rand.NewPCG(7, 7)), //nolint:gosec
	)

	for i := 0; i < 20; i++ {
		got := b.Next()
		assert.GreaterOrEqualf(t, got, time.Duration(0), "iteration %d", i)
	}
}

func TestBackoff_Reset(t *testing.T) {
	t.Parallel()

	b := service.NewBackoffWithRand(
		newCfg(100*time.Millisecond, 1*time.Second, 0),
		rand.New(rand.NewPCG(1, 1)), //nolint:gosec
	)

	_ = b.Next()
	_ = b.Next()
	_ = b.Next()
	b.Reset()
	assert.Equal(t, 100*time.Millisecond, b.Next())
	assert.Equal(t, 200*time.Millisecond, b.Next())
}

func TestNewBackoff_DefaultsToWallClockSeed(t *testing.T) {
	t.Parallel()

	cfg := newCfg(20*time.Millisecond, 80*time.Millisecond, 0)
	b := service.NewBackoff(cfg)

	require.Equal(t, 20*time.Millisecond, b.Next())
	require.Equal(t, 40*time.Millisecond, b.Next())
	require.Equal(t, 80*time.Millisecond, b.Next())
	b.Reset()
	require.Equal(t, 20*time.Millisecond, b.Next())
}
