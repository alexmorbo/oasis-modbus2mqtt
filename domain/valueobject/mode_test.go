package valueobject_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"
)

func TestNewMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   int
		want    valueobject.Mode
		wantErr bool
	}{
		{name: "off", input: 0, want: valueobject.ModeOff},
		{name: "heat", input: 1, want: valueobject.ModeHeat},
		{name: "cool", input: 2, want: valueobject.ModeCool},
		{name: "auto", input: 3, want: valueobject.ModeAuto},
		{name: "negative", input: -1, wantErr: true},
		{name: "above range", input: 4, wantErr: true},
		{name: "far above range", input: 9999, wantErr: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := valueobject.NewMode(tc.input)
			if tc.wantErr {
				require.Error(t, err)
				assert.True(t, errors.Is(err, valueobject.ErrInvalidMode))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestMode_Bits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mode valueobject.Mode
		want uint16
	}{
		{name: "off", mode: valueobject.ModeOff, want: 0},
		{name: "heat", mode: valueobject.ModeHeat, want: 1},
		{name: "cool", mode: valueobject.ModeCool, want: 2},
		{name: "auto", mode: valueobject.ModeAuto, want: 3},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.mode.Bits())
		})
	}
}

func TestMode_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mode valueobject.Mode
		want string
	}{
		{name: "off", mode: valueobject.ModeOff, want: "off"},
		{name: "heat", mode: valueobject.ModeHeat, want: "heat"},
		{name: "cool", mode: valueobject.ModeCool, want: "cool"},
		{name: "auto", mode: valueobject.ModeAuto, want: "auto"},
		{name: "out of range is unknown", mode: valueobject.Mode(42), want: "unknown"},
		{name: "negative is unknown", mode: valueobject.Mode(-1), want: "unknown"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.mode.String())
		})
	}
}

func TestModeFromRegister(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		reg  uint16
		want valueobject.Mode
	}{
		{name: "zero", reg: 0x0000, want: valueobject.ModeOff},
		{name: "heat low bit", reg: 0x0001, want: valueobject.ModeHeat},
		{name: "cool", reg: 0x0002, want: valueobject.ModeCool},
		{name: "auto", reg: 0x0003, want: valueobject.ModeAuto},
		{name: "high bits ignored", reg: 0xFF01, want: valueobject.ModeHeat},
		{name: "all bits set masks to auto", reg: 0xFFFF, want: valueobject.ModeAuto},
		{name: "only bit 2 set masks to off", reg: 0x0004, want: valueobject.ModeOff},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, valueobject.ModeFromRegister(tc.reg))
		})
	}
}

func TestApplyToRegister(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		reg  uint16
		mode valueobject.Mode
		want uint16
	}{
		{name: "zero reg set heat", reg: 0x0000, mode: valueobject.ModeHeat, want: 0x0001},
		{name: "preserve high bits", reg: 0xFFFC, mode: valueobject.ModeHeat, want: 0xFFFD},
		{name: "preserve high bits set off", reg: 0xFFFF, mode: valueobject.ModeOff, want: 0xFFFC},
		{name: "preserve middle bits", reg: 0x00F0, mode: valueobject.ModeCool, want: 0x00F2},
		{name: "overwrite cool with auto", reg: 0x00A2, mode: valueobject.ModeAuto, want: 0x00A3},
		{name: "overwrite auto with off", reg: 0x00A3, mode: valueobject.ModeOff, want: 0x00A0},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, valueobject.ApplyToRegister(tc.reg, tc.mode))
		})
	}
}
