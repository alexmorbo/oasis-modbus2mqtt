package valueobject_test

import (
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"
)

func TestNewTemperature(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   float64
		want    float64
		wantErr bool
	}{
		{name: "min boundary", input: 5.0, want: 5.0},
		{name: "just above min", input: 5.01, want: 5.01},
		{name: "typical value", input: 22.5, want: 22.5},
		{name: "max boundary", input: 30.0, want: 30.0},
		{name: "below min", input: 4.99, wantErr: true},
		{name: "well below min", input: -1, wantErr: true},
		{name: "zero", input: 0, wantErr: true},
		{name: "above max", input: 30.01, wantErr: true},
		{name: "well above max", input: 100, wantErr: true},
		{name: "NaN", input: math.NaN(), wantErr: true},
		{name: "positive infinity", input: math.Inf(1), wantErr: true},
		{name: "negative infinity", input: math.Inf(-1), wantErr: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := valueobject.NewTemperature(tc.input)
			if tc.wantErr {
				require.Error(t, err)
				assert.True(t, errors.Is(err, valueobject.ErrInvalidTemperature))
				return
			}
			require.NoError(t, err)
			assert.InDelta(t, tc.want, got.Celsius(), 1e-9)
		})
	}
}

func TestTemperatureFromRaw(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  uint16
		want float64
	}{
		{name: "zero", raw: 0, want: 0.0},
		{name: "min user range", raw: 50, want: 5.0},
		{name: "typical", raw: 225, want: 22.5},
		{name: "max user range", raw: 300, want: 30.0},
		{name: "above user range allowed", raw: 500, want: 50.0},
		{name: "max uint16", raw: 0xFFFF, want: 6553.5},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := valueobject.TemperatureFromRaw(tc.raw)
			assert.InDelta(t, tc.want, got.Celsius(), 1e-9)
		})
	}
}

func TestTemperature_Raw(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		celsius float64
		want    uint16
	}{
		{name: "min boundary", celsius: 5.0, want: 50},
		{name: "typical", celsius: 22.5, want: 225},
		{name: "max boundary", celsius: 30.0, want: 300},
		{name: "rounds up", celsius: 22.55, want: 226},
		{name: "rounds down", celsius: 22.54, want: 225},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			temp, err := valueobject.NewTemperature(tc.celsius)
			require.NoError(t, err)
			assert.Equal(t, tc.want, temp.Raw())
		})
	}
}

func TestTemperature_RawClampsOutOfRangeValues(t *testing.T) {
	t.Parallel()

	// Construct via FromRaw which performs no validation so we can verify Raw() clamping
	// when an out-of-write-range sensor value flows back through.
	low := valueobject.TemperatureFromRaw(0)
	assert.Equal(t, uint16(50), low.Raw(), "below-range value must clamp up to 50")

	high := valueobject.TemperatureFromRaw(1000)
	assert.Equal(t, uint16(300), high.Raw(), "above-range value must clamp down to 300")

	exactlyMin := valueobject.TemperatureFromRaw(50)
	assert.Equal(t, uint16(50), exactlyMin.Raw())

	exactlyMax := valueobject.TemperatureFromRaw(300)
	assert.Equal(t, uint16(300), exactlyMax.Raw())
}
