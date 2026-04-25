// Package entity provides immutable domain entities decoded from Oasis Syberia
// controller register fields. Entities depend only on the Go standard library
// and on github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject.
package entity

// OperatingState represents the current controller operation, decoded from
// bits 0..4 of input register State_1 (i3).
type OperatingState int

const (
	// OpUnknown is the sentinel for an out-of-range operation code.
	OpUnknown OperatingState = -1
	// OpIdle: controller is running steadily (no transition in progress).
	OpIdle OperatingState = 0
	// OpOpenDamper: opening the air damper.
	OpOpenDamper OperatingState = 1
	// OpPreheatCalorifier: pre-heating the calorifier before fan start.
	OpPreheatCalorifier OperatingState = 2
	// OpStartFan: spinning up the supply fan.
	OpStartFan OperatingState = 3
	// OpNorthStart: cold-weather start sequence.
	OpNorthStart OperatingState = 4
	// OpFanCoastdown: fan coast-down after shutdown command.
	OpFanCoastdown OperatingState = 5
	// OpCloseDamper: closing the air damper.
	OpCloseDamper OperatingState = 6
	// OpElectricCalorifierPurge: post-shutdown purge of the electric calorifier.
	OpElectricCalorifierPurge OperatingState = 7
	// OpOpenHotWaterValve: opening the hot-water valve.
	OpOpenHotWaterValve OperatingState = 8
	// OpCloseHotWaterValve: closing the hot-water valve.
	OpCloseHotWaterValve OperatingState = 9
	// OpOpenColdValve: opening the cold-water valve.
	OpOpenColdValve OperatingState = 10
	// OpCloseColdValve: closing the cold-water valve.
	OpCloseColdValve OperatingState = 11
	// OpRotorSpinup: spinning up the recuperator rotor.
	OpRotorSpinup OperatingState = 12
)

// AllOperatingStates lists every defined operating state in numeric order.
// Useful for exhaustive table-driven tests and downstream iteration.
var AllOperatingStates = []OperatingState{
	OpIdle,
	OpOpenDamper,
	OpPreheatCalorifier,
	OpStartFan,
	OpNorthStart,
	OpFanCoastdown,
	OpCloseDamper,
	OpElectricCalorifierPurge,
	OpOpenHotWaterValve,
	OpCloseHotWaterValve,
	OpOpenColdValve,
	OpCloseColdValve,
	OpRotorSpinup,
}

// OperatingStateFromRegister decodes bits 0..4 of a State_1 register read.
// Values 0..12 map to the corresponding OperatingState; anything higher
// (the State_1 bits 0..4 field can technically encode 0..31) yields OpUnknown.
func OperatingStateFromRegister(raw uint16) OperatingState {
	bits := raw & 0x1F
	if bits > 12 {
		return OpUnknown
	}
	return OperatingState(bits)
}

// String returns the canonical snake_case label for the operating state.
func (s OperatingState) String() string {
	switch s {
	case OpIdle:
		return "idle"
	case OpOpenDamper:
		return "open_damper"
	case OpPreheatCalorifier:
		return "preheat_calorifier"
	case OpStartFan:
		return "start_fan"
	case OpNorthStart:
		return "north_start"
	case OpFanCoastdown:
		return "fan_coastdown"
	case OpCloseDamper:
		return "close_damper"
	case OpElectricCalorifierPurge:
		return "electric_calorifier_purge"
	case OpOpenHotWaterValve:
		return "open_hot_water_valve"
	case OpCloseHotWaterValve:
		return "close_hot_water_valve"
	case OpOpenColdValve:
		return "open_cold_valve"
	case OpCloseColdValve:
		return "close_cold_valve"
	case OpRotorSpinup:
		return "rotor_spinup"
	default:
		return "unknown"
	}
}

// IsTransitional reports whether the controller is between steady states.
// OpIdle and OpUnknown are not transitional; everything else is.
func (s OperatingState) IsTransitional() bool {
	return s != OpIdle && s != OpUnknown
}
