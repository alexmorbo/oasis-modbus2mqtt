package dto

import (
	"fmt"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"
)

// Command is the marker interface for all command DTOs accepted by
// ApplyCommandUseCase. The marker method is unexported so only types
// declared in this package can satisfy Command — this gives the use case
// a closed, type-safe set of commands to switch over.
type Command interface {
	commandTag()
}

// SetPowerCommand toggles the controller's power state.
type SetPowerCommand struct {
	On bool
}

func (SetPowerCommand) commandTag() {}

// String returns a structured-log-friendly description of the command.
func (c SetPowerCommand) String() string {
	return fmt.Sprintf("SetPower(on=%t)", c.On)
}

// SetModeCommand sets the controller mode (off/heat/cool/auto).
type SetModeCommand struct {
	Mode valueobject.Mode
}

func (SetModeCommand) commandTag() {}

// String returns a structured-log-friendly description of the command.
func (c SetModeCommand) String() string {
	return fmt.Sprintf("SetMode(%s)", c.Mode.String())
}

// SetTemperatureCommand sets the controller setpoint in Celsius.
type SetTemperatureCommand struct {
	Temperature valueobject.Temperature
}

func (SetTemperatureCommand) commandTag() {}

// String returns a structured-log-friendly description of the command.
func (c SetTemperatureCommand) String() string {
	return fmt.Sprintf("SetTemperature(%.1f°C)", c.Temperature.Celsius())
}

// SetFanCommand sets the controller fan speed in [1, 10].
type SetFanCommand struct {
	FanSpeed valueobject.FanSpeed
}

func (SetFanCommand) commandTag() {}

// String returns a structured-log-friendly description of the command.
func (c SetFanCommand) String() string {
	return fmt.Sprintf("SetFan(speed=%d)", c.FanSpeed.Value())
}

// Compile-time assertions that every command type satisfies Command.
var (
	_ Command = SetPowerCommand{}
	_ Command = SetModeCommand{}
	_ Command = SetTemperatureCommand{}
	_ Command = SetFanCommand{}
)
