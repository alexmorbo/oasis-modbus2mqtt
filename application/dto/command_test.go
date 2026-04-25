package dto_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/dto"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"
)

func TestSetPowerCommand_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		on   bool
		want string
	}{
		{name: "on", on: true, want: "SetPower(on=true)"},
		{name: "off", on: false, want: "SetPower(on=false)"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmd := dto.SetPowerCommand{On: tc.on}
			assert.Equal(t, tc.want, cmd.String())
		})
	}
}

func TestSetModeCommand_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		modeRaw int
		want    string
	}{
		{name: "off", modeRaw: 0, want: "SetMode(off)"},
		{name: "heat", modeRaw: 1, want: "SetMode(heat)"},
		{name: "cool", modeRaw: 2, want: "SetMode(cool)"},
		{name: "auto", modeRaw: 3, want: "SetMode(auto)"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mode, err := valueobject.NewMode(tc.modeRaw)
			require.NoError(t, err)
			cmd := dto.SetModeCommand{Mode: mode}
			assert.Equal(t, tc.want, cmd.String())
		})
	}
}

func TestSetTemperatureCommand_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		celsius float64
		want    string
	}{
		{name: "min", celsius: 5.0, want: "SetTemperature(5.0°C)"},
		{name: "mid", celsius: 22.5, want: "SetTemperature(22.5°C)"},
		{name: "max", celsius: 30.0, want: "SetTemperature(30.0°C)"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			temp, err := valueobject.NewTemperature(tc.celsius)
			require.NoError(t, err)
			cmd := dto.SetTemperatureCommand{Temperature: temp}
			assert.Equal(t, tc.want, cmd.String())
		})
	}
}

func TestSetFanCommand_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		speed uint16
		want  string
	}{
		{name: "min", speed: 1, want: "SetFan(speed=1)"},
		{name: "mid", speed: 5, want: "SetFan(speed=5)"},
		{name: "max", speed: 10, want: "SetFan(speed=10)"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fan, err := valueobject.NewFanSpeed(tc.speed)
			require.NoError(t, err)
			cmd := dto.SetFanCommand{FanSpeed: fan}
			assert.Equal(t, tc.want, cmd.String())
		})
	}
}
