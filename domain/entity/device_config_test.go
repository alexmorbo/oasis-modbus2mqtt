package entity_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
)

func TestHeaterType_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		h    entity.HeaterType
		want string
	}{
		{name: "none", h: entity.HeaterNone, want: "none"},
		{name: "electric", h: entity.HeaterElectric, want: "electric_heater"},
		{name: "water", h: entity.HeaterWater, want: "water_heater"},
		{name: "combined", h: entity.HeaterCombined, want: "combined_heater"},
		{name: "unknown", h: entity.HeaterType(9), want: "unknown_heater"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.h.String())
		})
	}
}

func TestCoolerType_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		c    entity.CoolerType
		want string
	}{
		{name: "none", c: entity.CoolerNone, want: "none"},
		{name: "kkb", c: entity.CoolerKKB, want: "kkb_cooler"},
		{name: "fancoil", c: entity.CoolerFancoil, want: "fancoil_cooler"},
		{name: "unknown", c: entity.CoolerType(7), want: "unknown_cooler"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.c.String())
		})
	}
}

func TestRecuperatorType_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		r    entity.RecuperatorType
		want string
	}{
		{name: "none", r: entity.RecuperatorNone, want: "none"},
		{name: "plate", r: entity.RecuperatorPlate, want: "plate_recuperator"},
		{name: "rotor", r: entity.RecuperatorRotor, want: "rotor_recuperator"},
		{name: "unknown", r: entity.RecuperatorType(5), want: "unknown_recuperator"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.r.String())
		})
	}
}

func TestDeviceConfigFromRegister(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		raw           uint16
		wantHeater    entity.HeaterType
		wantCooler    entity.CoolerType
		wantRecup     entity.RecuperatorType
		wantHasHeater bool
		wantHasCooler bool
		wantHasRecup  bool
		wantString    string
	}{
		{
			name:       "all zero",
			raw:        0x0000,
			wantString: "none",
		},
		{
			name:          "electric heater only (real hardware)",
			raw:           0x0001,
			wantHeater:    entity.HeaterElectric,
			wantHasHeater: true,
			wantString:    "electric_heater",
		},
		{
			name:          "electric heater plus KKB cooler",
			raw:           0x0011,
			wantHeater:    entity.HeaterElectric,
			wantCooler:    entity.CoolerKKB,
			wantHasHeater: true,
			wantHasCooler: true,
			wantString:    "electric_heater, kkb_cooler",
		},
		{
			name:          "all three installed",
			raw:           0x0111,
			wantHeater:    entity.HeaterElectric,
			wantCooler:    entity.CoolerKKB,
			wantRecup:     entity.RecuperatorPlate,
			wantHasHeater: true,
			wantHasCooler: true,
			wantHasRecup:  true,
			wantString:    "electric_heater, kkb_cooler, plate_recuperator",
		},
		{
			name:          "water heater plus rotor recuperator",
			raw:           0x0202,
			wantHeater:    entity.HeaterWater,
			wantRecup:     entity.RecuperatorRotor,
			wantHasHeater: true,
			wantHasRecup:  true,
			wantString:    "water_heater, rotor_recuperator",
		},
		{
			name:          "out of range nibbles preserved",
			raw:           0x0F77,
			wantHeater:    entity.HeaterType(7),
			wantCooler:    entity.CoolerType(7),
			wantRecup:     entity.RecuperatorType(0xF),
			wantHasHeater: true,
			wantHasCooler: true,
			wantHasRecup:  true,
			wantString:    "unknown_heater, unknown_cooler, unknown_recuperator",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := entity.DeviceConfigFromRegister(tc.raw)
			assert.Equal(t, tc.wantHeater, cfg.Heater)
			assert.Equal(t, tc.wantCooler, cfg.Cooler)
			assert.Equal(t, tc.wantRecup, cfg.Recuperator)
			assert.Equal(t, tc.raw, cfg.Raw)
			assert.Equal(t, tc.wantHasHeater, cfg.HasHeater())
			assert.Equal(t, tc.wantHasCooler, cfg.HasCooler())
			assert.Equal(t, tc.wantHasRecup, cfg.HasRecuperator())
			assert.Equal(t, tc.wantString, cfg.String())
		})
	}
}
