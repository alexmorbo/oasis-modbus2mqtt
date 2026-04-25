// Package valueobject provides immutable, validated domain value objects for
// the Oasis Syberia Modbus bridge. All types here are value semantics with no
// dependencies outside the Go standard library.
package valueobject

import "fmt"

// RegisterAddr is a typed wrapper over a 0-based PDU Modbus register address.
type RegisterAddr uint16

// String returns the address formatted as a 4-digit uppercase hex literal.
func (r RegisterAddr) String() string {
	return fmt.Sprintf("0x%04X", uint16(r))
}

// Uint16 returns the address as a raw uint16 for Modbus client calls.
func (r RegisterAddr) Uint16() uint16 {
	return uint16(r)
}

// RegisterKind enumerates Modbus register kinds. The zero value is invalid.
type RegisterKind int

const (
	// RegisterKindInput is a read-only Modbus input register (function code 0x04).
	RegisterKindInput RegisterKind = iota + 1
	// RegisterKindHolding is a read/write Modbus holding register (function codes 0x03/0x06/0x10).
	RegisterKindHolding
)

// IsValid reports whether k is a known register kind.
func (k RegisterKind) IsValid() bool {
	return k == RegisterKindInput || k == RegisterKindHolding
}

// String returns "input", "holding", or "unknown" depending on the kind.
func (k RegisterKind) String() string {
	switch k {
	case RegisterKindInput:
		return "input"
	case RegisterKindHolding:
		return "holding"
	default:
		return "unknown"
	}
}
