package catalog_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/catalog"
)

func TestEntityKind_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		kind catalog.EntityKind
		want string
	}{
		{name: "none zero value", kind: catalog.EntityKindNone, want: "none"},
		{name: "sensor", kind: catalog.EntityKindSensor, want: "sensor"},
		{name: "binary_sensor", kind: catalog.EntityKindBinarySensor, want: "binary_sensor"},
		{name: "switch", kind: catalog.EntityKindSwitch, want: "switch"},
		{name: "climate", kind: catalog.EntityKindClimate, want: "climate"},
		{name: "button", kind: catalog.EntityKindButton, want: "button"},
		{name: "out of range high", kind: catalog.EntityKind(99), want: "unknown"},
		{name: "negative", kind: catalog.EntityKind(-1), want: "unknown"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.kind.String())
		})
	}
}

func TestEntityCategory_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		category catalog.EntityCategory
		want     string
	}{
		{name: "default is empty string", category: catalog.EntityCategoryDefault, want: ""},
		{name: "diagnostic", category: catalog.EntityCategoryDiagnostic, want: "diagnostic"},
		{name: "config", category: catalog.EntityCategoryConfig, want: "config"},
		{name: "out of range", category: catalog.EntityCategory(99), want: "unknown"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.category.String())
		})
	}
}

func TestDeviceClassConstants(t *testing.T) {
	t.Parallel()

	assert.Equal(t, catalog.DeviceClass(""), catalog.DeviceClassNone)
	assert.Equal(t, catalog.DeviceClass("temperature"), catalog.DeviceClassTemperature)
	assert.Equal(t, catalog.DeviceClass("humidity"), catalog.DeviceClassHumidity)
	assert.Equal(t, catalog.DeviceClass("heat"), catalog.DeviceClassHeat)
	assert.Equal(t, catalog.DeviceClass("problem"), catalog.DeviceClassProblem)
	assert.Equal(t, catalog.DeviceClass("running"), catalog.DeviceClassRunning)
	assert.Equal(t, catalog.DeviceClass("power"), catalog.DeviceClassPower)
}

func TestStateClassConstants(t *testing.T) {
	t.Parallel()

	assert.Equal(t, catalog.StateClass(""), catalog.StateClassNone)
	assert.Equal(t, catalog.StateClass("measurement"), catalog.StateClassMeasurement)
}

func TestEntityHint_ZeroValueIsNone(t *testing.T) {
	t.Parallel()

	// A zero-value EntityHint must be valid: Kind defaults to None, all strings empty.
	var h catalog.EntityHint
	assert.Equal(t, catalog.EntityKindNone, h.Kind)
	assert.Equal(t, catalog.EntityCategoryDefault, h.Category)
	assert.Equal(t, "", h.ObjectID)
	assert.Equal(t, "", h.Name)
	assert.Equal(t, catalog.DeviceClassNone, h.DeviceClass)
	assert.Equal(t, catalog.StateClassNone, h.StateClass)
}
