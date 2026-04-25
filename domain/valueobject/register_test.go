package valueobject_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"
)

func TestRegisterAddr_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		addr valueobject.RegisterAddr
		want string
	}{
		{name: "zero", addr: 0x0000, want: "0x0000"},
		{name: "low byte", addr: 0x0002, want: "0x0002"},
		{name: "Temp_Target h31", addr: 0x001F, want: "0x001F"},
		{name: "Dev_Keys_2 h86", addr: 0x0056, want: "0x0056"},
		{name: "max", addr: 0xFFFF, want: "0xFFFF"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.addr.String())
		})
	}
}

func TestRegisterAddr_Uint16(t *testing.T) {
	t.Parallel()

	assert.Equal(t, uint16(0), valueobject.RegisterAddr(0).Uint16())
	assert.Equal(t, uint16(0x1F), valueobject.RegisterAddr(0x1F).Uint16())
	assert.Equal(t, uint16(0xFFFF), valueobject.RegisterAddr(0xFFFF).Uint16())
}

func TestRegisterKind_IsValid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		kind valueobject.RegisterKind
		want bool
	}{
		{name: "zero invalid", kind: 0, want: false},
		{name: "input valid", kind: valueobject.RegisterKindInput, want: true},
		{name: "holding valid", kind: valueobject.RegisterKindHolding, want: true},
		{name: "out of range high", kind: 99, want: false},
		{name: "negative", kind: -1, want: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.kind.IsValid())
		})
	}
}

func TestRegisterKind_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		kind valueobject.RegisterKind
		want string
	}{
		{name: "zero is unknown", kind: 0, want: "unknown"},
		{name: "input", kind: valueobject.RegisterKindInput, want: "input"},
		{name: "holding", kind: valueobject.RegisterKindHolding, want: "holding"},
		{name: "out of range is unknown", kind: 42, want: "unknown"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.kind.String())
		})
	}
}
