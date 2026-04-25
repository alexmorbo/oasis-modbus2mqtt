package valueobject_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"
)

func TestFirmwareFromRaw_Raw(t *testing.T) {
	t.Parallel()

	assert.Equal(t, uint16(0), valueobject.FirmwareFromRaw(0).Raw())
	assert.Equal(t, uint16(0x5200), valueobject.FirmwareFromRaw(0x5200).Raw())
	assert.Equal(t, uint16(0xFFFF), valueobject.FirmwareFromRaw(0xFFFF).Raw())
}

func TestFirmwareVersion_IsKnown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  uint16
		want bool
	}{
		{name: "zero is unknown", raw: 0x0000, want: false},
		{name: "non-zero is known", raw: 0x5200, want: true},
		{name: "max value is known", raw: 0xFFFF, want: true},
		{name: "low byte only is known", raw: 0x0001, want: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, valueobject.FirmwareFromRaw(tc.raw).IsKnown())
		})
	}
}

func TestFirmwareVersion_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  uint16
		want string
	}{
		{name: "zero is unknown", raw: 0x0000, want: "unknown"},
		{name: "v5.2.0 from controller", raw: 0x5200, want: "v5.2.0"},
		{name: "v5.2.0.1 with build", raw: 0x5201, want: "v5.2.0.1"},
		{name: "v1.2.3.4 all nibbles", raw: 0x1234, want: "v1.2.3.4"},
		{name: "v0.0.0.1 only build", raw: 0x0001, want: "v0.0.0.1"},
		{name: "v0.0.1.0 only patch", raw: 0x0010, want: "v0.0.1"},
		{name: "v0.1.0.0 only minor", raw: 0x0100, want: "v0.1.0"},
		{name: "v1.0.0.0 only major", raw: 0x1000, want: "v1.0.0"},
		{name: "vF.F.F.F max nibbles", raw: 0xFFFF, want: "v15.15.15.15"},
		{name: "v9.9.9.0 large no build", raw: 0x9990, want: "v9.9.9"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, valueobject.FirmwareFromRaw(tc.raw).String())
		})
	}
}
