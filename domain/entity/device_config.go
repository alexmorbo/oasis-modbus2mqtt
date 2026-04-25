package entity

import "strings"

// HeaterType encodes the heater installed on the unit (Type_Dev bits 0..3).
type HeaterType int

const (
	// HeaterNone means no heater is installed.
	HeaterNone HeaterType = 0
	// HeaterElectric is an electric calorifier.
	HeaterElectric HeaterType = 1
	// HeaterWater is a hot-water calorifier.
	HeaterWater HeaterType = 2
	// HeaterCombined is a combined electric + water calorifier.
	HeaterCombined HeaterType = 3
)

// String returns the canonical lowercase label for the heater type.
func (h HeaterType) String() string {
	switch h {
	case HeaterNone:
		return "none"
	case HeaterElectric:
		return "electric_heater"
	case HeaterWater:
		return "water_heater"
	case HeaterCombined:
		return "combined_heater"
	default:
		return "unknown_heater"
	}
}

// CoolerType encodes the cooler installed on the unit (Type_Dev bits 4..7).
type CoolerType int

const (
	// CoolerNone means no cooler is installed.
	CoolerNone CoolerType = 0
	// CoolerKKB is an outdoor compressor unit (KKB).
	CoolerKKB CoolerType = 1
	// CoolerFancoil is a fancoil cooler.
	CoolerFancoil CoolerType = 2
)

// String returns the canonical lowercase label for the cooler type.
func (c CoolerType) String() string {
	switch c {
	case CoolerNone:
		return "none"
	case CoolerKKB:
		return "kkb_cooler"
	case CoolerFancoil:
		return "fancoil_cooler"
	default:
		return "unknown_cooler"
	}
}

// RecuperatorType encodes the heat recovery installed on the unit (Type_Dev bits 8..11).
type RecuperatorType int

const (
	// RecuperatorNone means no recuperator is installed.
	RecuperatorNone RecuperatorType = 0
	// RecuperatorPlate is a plate-type heat exchanger.
	RecuperatorPlate RecuperatorType = 1
	// RecuperatorRotor is a rotary recuperator.
	RecuperatorRotor RecuperatorType = 2
)

// String returns the canonical lowercase label for the recuperator type.
func (r RecuperatorType) String() string {
	switch r {
	case RecuperatorNone:
		return "none"
	case RecuperatorPlate:
		return "plate_recuperator"
	case RecuperatorRotor:
		return "rotor_recuperator"
	default:
		return "unknown_recuperator"
	}
}

// DeviceConfig is the decoded form of holding register Type_Dev (h0).
type DeviceConfig struct {
	Heater      HeaterType
	Cooler      CoolerType
	Recuperator RecuperatorType
	Raw         uint16
}

// DeviceConfigFromRegister extracts the heater, cooler and recuperator nibbles
// from a Type_Dev register value. Out-of-range nibbles are stored as-is so the
// String() / Has*() methods can still surface them as "unknown" instead of
// silently mapping to None.
func DeviceConfigFromRegister(raw uint16) DeviceConfig {
	heater := raw & 0xF
	cooler := (raw >> 4) & 0xF
	recup := (raw >> 8) & 0xF
	return DeviceConfig{
		Heater:      HeaterType(heater),
		Cooler:      CoolerType(cooler),
		Recuperator: RecuperatorType(recup),
		Raw:         raw,
	}
}

// HasHeater reports whether a heater of any kind is installed.
func (c DeviceConfig) HasHeater() bool { return c.Heater != HeaterNone }

// HasCooler reports whether a cooler of any kind is installed.
func (c DeviceConfig) HasCooler() bool { return c.Cooler != CoolerNone }

// HasRecuperator reports whether a recuperator of any kind is installed.
func (c DeviceConfig) HasRecuperator() bool { return c.Recuperator != RecuperatorNone }

// String renders the config as a comma-separated list of installed components,
// or "none" if all three nibbles are zero.
func (c DeviceConfig) String() string {
	parts := make([]string, 0, 3)
	if c.Heater != HeaterNone {
		parts = append(parts, c.Heater.String())
	}
	if c.Cooler != CoolerNone {
		parts = append(parts, c.Cooler.String())
	}
	if c.Recuperator != RecuperatorNone {
		parts = append(parts, c.Recuperator.String())
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}
