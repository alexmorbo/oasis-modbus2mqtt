package valueobject

import (
	"errors"
	"fmt"
)

// ErrInvalidFanSpeed is returned when a fan speed is outside [1, 10].
var ErrInvalidFanSpeed = errors.New("invalid fan speed")

const (
	fanSpeedMin uint16 = 1
	fanSpeedMax uint16 = 10
)

// FanSpeed is a validated controller fan speed in [1, 10].
type FanSpeed struct {
	value uint16
}

// NewFanSpeed validates v is in [1, 10] and returns a FanSpeed.
func NewFanSpeed(v uint16) (FanSpeed, error) {
	if v < fanSpeedMin || v > fanSpeedMax {
		return FanSpeed{}, fmt.Errorf("fan speed %d out of range [%d,%d]: %w",
			v, fanSpeedMin, fanSpeedMax, ErrInvalidFanSpeed)
	}
	return FanSpeed{value: v}, nil
}

// NewFanSpeedClamped returns a FanSpeed clamped first into [min, max] and then into [1, 10].
// Defensive: if min/max themselves fall outside [1, 10] the bounds are tightened to the
// hardware-valid range before clamping v.
func NewFanSpeedClamped(v, min, max uint16) FanSpeed {
	if min < fanSpeedMin {
		min = fanSpeedMin
	}
	if max > fanSpeedMax {
		max = fanSpeedMax
	}
	if min > max {
		min, max = max, min
	}
	if v < min {
		v = min
	}
	if v > max {
		v = max
	}
	return FanSpeed{value: v}
}

// Value returns the fan speed as a raw uint16 ready for a Modbus write.
func (f FanSpeed) Value() uint16 {
	return f.value
}
