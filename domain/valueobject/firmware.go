package valueobject

import "fmt"

const firmwareUnknownLabel = "unknown"

// FirmwareVersion is a BCD-encoded controller firmware version read from input register i0.
//
// The raw uint16 is interpreted as 0xMmNn where each character is a 4-bit nibble:
//
//	M = high nibble of high byte (major)
//	m = low  nibble of high byte (minor)
//	N = high nibble of low  byte (patch)
//	n = low  nibble of low  byte (build, optional)
//
// String formatting:
//
//	0x0000        -> "unknown"
//	build nibble n == 0   -> "v{M}.{m}.{N}"     (e.g. 0x5200 -> "v5.2.0")
//	build nibble n != 0   -> "v{M}.{m}.{N}.{n}" (e.g. 0x5201 -> "v5.2.0.1")
type FirmwareVersion struct {
	raw uint16
}

// FirmwareFromRaw wraps a raw uint16 firmware register value. No validation is performed
// because the value originates from a sensor read.
func FirmwareFromRaw(raw uint16) FirmwareVersion {
	return FirmwareVersion{raw: raw}
}

// Raw returns the underlying uint16 representation of the firmware version.
func (f FirmwareVersion) Raw() uint16 {
	return f.raw
}

// IsKnown reports whether the firmware register has been populated with a non-zero value.
func (f FirmwareVersion) IsKnown() bool {
	return f.raw != 0
}

// String renders the firmware version per the BCD nibble convention documented on the type.
func (f FirmwareVersion) String() string {
	if f.raw == 0 {
		return firmwareUnknownLabel
	}
	major := (f.raw >> 12) & 0xF
	minor := (f.raw >> 8) & 0xF
	patch := (f.raw >> 4) & 0xF
	build := f.raw & 0xF
	if build == 0 {
		return fmt.Sprintf("v%d.%d.%d", major, minor, patch)
	}
	return fmt.Sprintf("v%d.%d.%d.%d", major, minor, patch, build)
}
