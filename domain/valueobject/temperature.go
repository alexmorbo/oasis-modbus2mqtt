package valueobject

import (
	"errors"
	"fmt"
	"math"
)

// ErrInvalidTemperature is returned when a temperature is NaN, infinite, or out of [5, 30] °C.
var ErrInvalidTemperature = errors.New("invalid temperature")

const (
	temperatureMinCelsius = 5.0
	temperatureMaxCelsius = 30.0
	temperatureScale      = 10.0
	temperatureRawMin     = 50
	temperatureRawMax     = 300
)

// Temperature is an immutable Celsius value validated to the controller's accepted write range.
type Temperature struct {
	celsius float64
}

// NewTemperature validates c is finite and within [5, 30] °C and returns a Temperature.
func NewTemperature(c float64) (Temperature, error) {
	if math.IsNaN(c) {
		return Temperature{}, fmt.Errorf("temperature is NaN: %w", ErrInvalidTemperature)
	}
	if math.IsInf(c, 0) {
		return Temperature{}, fmt.Errorf("temperature is infinite: %w", ErrInvalidTemperature)
	}
	if c < temperatureMinCelsius || c > temperatureMaxCelsius {
		return Temperature{}, fmt.Errorf("temperature %g out of range [%g,%g]: %w",
			c, temperatureMinCelsius, temperatureMaxCelsius, ErrInvalidTemperature)
	}
	return Temperature{celsius: c}, nil
}

// TemperatureFromRaw decodes a Modbus uint16 sensor value (×10 scale) into a Temperature.
// No validation is performed: sensor reads may legitimately fall outside the user-write range.
func TemperatureFromRaw(raw uint16) Temperature {
	return Temperature{celsius: float64(raw) / temperatureScale}
}

// Celsius returns the temperature in degrees Celsius.
func (t Temperature) Celsius() float64 {
	return t.celsius
}

// Raw encodes the temperature for a Modbus holding-register write, clamped to [50, 300].
func (t Temperature) Raw() uint16 {
	scaled := math.Round(t.celsius * temperatureScale)
	if scaled < temperatureRawMin {
		return temperatureRawMin
	}
	if scaled > temperatureRawMax {
		return temperatureRawMax
	}
	return uint16(scaled)
}
