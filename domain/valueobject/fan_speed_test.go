package valueobject_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"
)

func TestNewFanSpeed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   uint16
		want    uint16
		wantErr bool
	}{
		{name: "min boundary", input: 1, want: 1},
		{name: "mid range", input: 5, want: 5},
		{name: "max boundary", input: 10, want: 10},
		{name: "zero rejected", input: 0, wantErr: true},
		{name: "above max rejected", input: 11, wantErr: true},
		{name: "max uint16 rejected", input: 0xFFFF, wantErr: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := valueobject.NewFanSpeed(tc.input)
			if tc.wantErr {
				require.Error(t, err)
				assert.True(t, errors.Is(err, valueobject.ErrInvalidFanSpeed))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.Value())
		})
	}
}

func TestNewFanSpeedClamped(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		v    uint16
		min  uint16
		max  uint16
		want uint16
	}{
		{name: "in range", v: 5, min: 4, max: 7, want: 5},
		{name: "below min clamped up", v: 0, min: 4, max: 7, want: 4},
		{name: "above max clamped down", v: 99, min: 4, max: 7, want: 7},
		{name: "below min equal one clamps up", v: 0, min: 1, max: 10, want: 1},
		{name: "above max ten clamps down", v: 11, min: 1, max: 10, want: 10},
		{name: "min below 1 tightens to 1", v: 0, min: 0, max: 7, want: 1},
		{name: "max above 10 tightens to 10", v: 99, min: 4, max: 99, want: 10},
		{name: "both bounds invalid still clamps to hw range", v: 99, min: 0, max: 99, want: 10},
		{name: "swapped bounds normalised", v: 5, min: 7, max: 4, want: 5},
		{name: "swapped bounds with low v", v: 1, min: 7, max: 4, want: 4},
		{name: "swapped bounds with high v", v: 9, min: 7, max: 4, want: 7},
		{name: "v exactly at min", v: 4, min: 4, max: 7, want: 4},
		{name: "v exactly at max", v: 7, min: 4, max: 7, want: 7},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := valueobject.NewFanSpeedClamped(tc.v, tc.min, tc.max)
			assert.Equal(t, tc.want, got.Value())
		})
	}
}
