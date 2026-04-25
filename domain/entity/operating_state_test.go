package entity_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
)

func TestOperatingStateFromRegister(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  uint16
		want entity.OperatingState
	}{
		{name: "idle", raw: 0x0000, want: entity.OpIdle},
		{name: "open_damper", raw: 0x0001, want: entity.OpOpenDamper},
		{name: "preheat_calorifier", raw: 0x0002, want: entity.OpPreheatCalorifier},
		{name: "start_fan", raw: 0x0003, want: entity.OpStartFan},
		{name: "north_start", raw: 0x0004, want: entity.OpNorthStart},
		{name: "fan_coastdown", raw: 0x0005, want: entity.OpFanCoastdown},
		{name: "close_damper", raw: 0x0006, want: entity.OpCloseDamper},
		{name: "electric_calorifier_purge", raw: 0x0007, want: entity.OpElectricCalorifierPurge},
		{name: "open_hot_water_valve", raw: 0x0008, want: entity.OpOpenHotWaterValve},
		{name: "close_hot_water_valve", raw: 0x0009, want: entity.OpCloseHotWaterValve},
		{name: "open_cold_valve", raw: 0x000A, want: entity.OpOpenColdValve},
		{name: "close_cold_valve", raw: 0x000B, want: entity.OpCloseColdValve},
		{name: "rotor_spinup", raw: 0x000C, want: entity.OpRotorSpinup},
		{name: "high bits ignored masks to idle", raw: 0xFFE0, want: entity.OpIdle},
		{name: "high bits ignored masks to open_damper", raw: 0x0021, want: entity.OpOpenDamper},
		{name: "13 is unknown", raw: 0x000D, want: entity.OpUnknown},
		{name: "14 is unknown", raw: 0x000E, want: entity.OpUnknown},
		{name: "31 is unknown (mask boundary)", raw: 0x001F, want: entity.OpUnknown},
		{name: "32 masks to idle", raw: 0x0020, want: entity.OpIdle},
		{name: "0xFFFF masks to unknown", raw: 0xFFFF, want: entity.OpUnknown},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, entity.OperatingStateFromRegister(tc.raw))
		})
	}
}

func TestOperatingState_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state entity.OperatingState
		want  string
	}{
		{name: "unknown sentinel", state: entity.OpUnknown, want: "unknown"},
		{name: "idle", state: entity.OpIdle, want: "idle"},
		{name: "open_damper", state: entity.OpOpenDamper, want: "open_damper"},
		{name: "preheat_calorifier", state: entity.OpPreheatCalorifier, want: "preheat_calorifier"},
		{name: "start_fan", state: entity.OpStartFan, want: "start_fan"},
		{name: "north_start", state: entity.OpNorthStart, want: "north_start"},
		{name: "fan_coastdown", state: entity.OpFanCoastdown, want: "fan_coastdown"},
		{name: "close_damper", state: entity.OpCloseDamper, want: "close_damper"},
		{name: "electric_calorifier_purge", state: entity.OpElectricCalorifierPurge, want: "electric_calorifier_purge"},
		{name: "open_hot_water_valve", state: entity.OpOpenHotWaterValve, want: "open_hot_water_valve"},
		{name: "close_hot_water_valve", state: entity.OpCloseHotWaterValve, want: "close_hot_water_valve"},
		{name: "open_cold_valve", state: entity.OpOpenColdValve, want: "open_cold_valve"},
		{name: "close_cold_valve", state: entity.OpCloseColdValve, want: "close_cold_valve"},
		{name: "rotor_spinup", state: entity.OpRotorSpinup, want: "rotor_spinup"},
		{name: "out of range positive", state: entity.OperatingState(99), want: "unknown"},
		{name: "out of range negative", state: entity.OperatingState(-99), want: "unknown"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.state.String())
		})
	}
}

func TestOperatingState_IsTransitional(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state entity.OperatingState
		want  bool
	}{
		{name: "idle is not transitional", state: entity.OpIdle, want: false},
		{name: "unknown is not transitional", state: entity.OpUnknown, want: false},
		{name: "open_damper is transitional", state: entity.OpOpenDamper, want: true},
		{name: "preheat is transitional", state: entity.OpPreheatCalorifier, want: true},
		{name: "start_fan is transitional", state: entity.OpStartFan, want: true},
		{name: "rotor_spinup is transitional", state: entity.OpRotorSpinup, want: true},
		{name: "fan_coastdown is transitional", state: entity.OpFanCoastdown, want: true},
		{name: "purge is transitional", state: entity.OpElectricCalorifierPurge, want: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.state.IsTransitional())
		})
	}
}

func TestAllOperatingStates_Exhaustive(t *testing.T) {
	t.Parallel()

	// transitionalStates maps each state in AllOperatingStates to its expected
	// IsTransitional result. OpIdle is the only steady state in the slice.
	transitionalStates := map[entity.OperatingState]bool{
		entity.OpIdle:                    false,
		entity.OpOpenDamper:              true,
		entity.OpPreheatCalorifier:       true,
		entity.OpStartFan:                true,
		entity.OpNorthStart:              true,
		entity.OpFanCoastdown:            true,
		entity.OpCloseDamper:             true,
		entity.OpElectricCalorifierPurge: true,
		entity.OpOpenHotWaterValve:       true,
		entity.OpCloseHotWaterValve:      true,
		entity.OpOpenColdValve:           true,
		entity.OpCloseColdValve:          true,
		entity.OpRotorSpinup:             true,
	}

	assert.Equal(t, len(transitionalStates), len(entity.AllOperatingStates),
		"AllOperatingStates length must match the number of documented states")

	for _, s := range entity.AllOperatingStates {
		s := s
		t.Run(s.String(), func(t *testing.T) {
			t.Parallel()
			assert.NotEmpty(t, s.String(), "every state must have a non-empty String()")
			wantTransitional, ok := transitionalStates[s]
			assert.True(t, ok, "state %v is not in the expected map", s)
			assert.Equal(t, wantTransitional, s.IsTransitional())
		})
	}
}
