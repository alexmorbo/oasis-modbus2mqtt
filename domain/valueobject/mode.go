package valueobject

import (
	"errors"
	"fmt"
)

// ErrInvalidMode is returned when a mode value is outside 0..3.
var ErrInvalidMode = errors.New("invalid mode")

// Mode encodes the two low bits of holding register Dev_Keys_2 (h86).
type Mode int

const (
	// ModeOff disables active conditioning; fans may still run.
	ModeOff Mode = 0
	// ModeHeat enables heat-only conditioning.
	ModeHeat Mode = 1
	// ModeCool enables cool-only conditioning (no-op on heat-only hardware).
	ModeCool Mode = 2
	// ModeAuto enables automatic mode selection.
	ModeAuto Mode = 3
)

// NewMode validates v and returns a Mode in [0, 3].
func NewMode(v int) (Mode, error) {
	if v < 0 || v > 3 {
		return ModeOff, fmt.Errorf("mode value %d out of range [0,3]: %w", v, ErrInvalidMode)
	}
	return Mode(v), nil
}

// Bits returns the mode as the two low bits of a uint16 register payload.
func (m Mode) Bits() uint16 {
	masked := m & Mode(0x3)
	//nolint:gosec // G115: masked is guaranteed to be in [0, 3], safe conversion
	return uint16(masked)
}

// String returns the canonical lowercase mode label.
func (m Mode) String() string {
	switch m {
	case ModeOff:
		return "off"
	case ModeHeat:
		return "heat"
	case ModeCool:
		return "cool"
	case ModeAuto:
		return "auto"
	default:
		return "unknown"
	}
}

// ModeFromRegister extracts the mode from bits 0..1 of a Dev_Keys_2 register value.
func ModeFromRegister(v uint16) Mode {
	return Mode(v & 0x3)
}

// ApplyToRegister returns reg with bits 0..1 replaced by m.Bits(), preserving all other bits.
func ApplyToRegister(reg uint16, m Mode) uint16 {
	return (reg &^ uint16(0x3)) | m.Bits()
}
