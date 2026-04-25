package entity_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
)

func TestSnapshot_IsHealthy(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	threshold := 10 * time.Second

	tests := []struct {
		name     string
		polledAt time.Time
		want     bool
	}{
		{name: "fresh poll one second ago", polledAt: now.Add(-1 * time.Second), want: true},
		{name: "exactly at threshold is healthy", polledAt: now.Add(-threshold), want: true},
		{name: "one nanosecond past threshold is stale", polledAt: now.Add(-threshold - 1), want: false},
		{name: "stale poll one minute ago", polledAt: now.Add(-1 * time.Minute), want: false},
		{name: "future poll is healthy", polledAt: now.Add(1 * time.Second), want: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := entity.Snapshot{PolledAt: tc.polledAt}
			assert.Equal(t, tc.want, s.IsHealthy(now, threshold))
		})
	}
}

func TestParseLastTime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  uint16
		want time.Duration
	}{
		{name: "zero", raw: 0x0000, want: 0},
		{name: "5 min 48 s", raw: 0x0530, want: 5*time.Minute + 48*time.Second},
		{name: "1 min 0 s", raw: 0x0100, want: 1 * time.Minute},
		{name: "0 min 30 s", raw: 0x001E, want: 30 * time.Second},
		{name: "max value 255 min 255 s", raw: 0xFFFF, want: 255*time.Minute + 255*time.Second},
		{name: "16 min 32 s", raw: 0x1020, want: 16*time.Minute + 32*time.Second},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, entity.ParseLastTime(tc.raw))
		})
	}
}

func TestSnapshot_ZeroValueIsUsable(t *testing.T) {
	t.Parallel()

	// A zero-value Snapshot must be a valid struct literal — no constructor required.
	s := entity.Snapshot{}
	assert.Equal(t, time.Time{}, s.PolledAt)
	assert.Equal(t, entity.OpIdle, s.Operation)
	assert.True(t, s.Errors.IsEmpty())
	assert.Equal(t, uint16(0), s.FanTarget1)
	assert.Equal(t, [4]uint16{}, s.RawErrors)
	assert.True(t, s.DecodeErrorSet().IsEmpty())
}

func TestSnapshot_DecodeErrorSet(t *testing.T) {
	t.Parallel()

	t.Run("empty raw errors yields empty set", func(t *testing.T) {
		t.Parallel()
		s := entity.Snapshot{RawErrors: [4]uint16{0, 0, 0, 0}}
		assert.True(t, s.DecodeErrorSet().IsEmpty())
	})

	t.Run("Error_Code bit 4 (E04) and bit 10 (E10) set", func(t *testing.T) {
		t.Parallel()
		s := entity.Snapshot{RawErrors: [4]uint16{(1 << 4) | (1 << 10), 0, 0, 0}}
		codes := s.DecodeErrorSet().Codes()
		assert.Contains(t, codes, "E04")
		assert.Contains(t, codes, "E10")
		assert.Len(t, codes, 2)
	})

	t.Run("Error_Code_2 bit 3 set", func(t *testing.T) {
		t.Parallel()
		s := entity.Snapshot{RawErrors: [4]uint16{0, 0, 1 << 3, 0}}
		codes := s.DecodeErrorSet().Codes()
		assert.Contains(t, codes, "E2_b3")
	})

	t.Run("Error_Code_3 reserved bit 6 is filtered", func(t *testing.T) {
		t.Parallel()
		s := entity.Snapshot{RawErrors: [4]uint16{0, 0, 0, 1 << 6}}
		assert.True(t, s.DecodeErrorSet().IsEmpty())
	})
}
