package mqtt_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/config"
	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/mqtt"
)

func defaultHAConfig() config.HAConfig {
	return config.HAConfig{
		DiscoveryPrefix: "homeassistant",
		DevicePrefix:    "oasis_syberia",
	}
}

func TestTopicBuilder_Discovery(t *testing.T) {
	t.Parallel()
	b := mqtt.NewTopicBuilder(defaultHAConfig())
	assert.Equal(t, "homeassistant/sensor/oasis_syberia/supply_temperature/config",
		b.Discovery("sensor", "supply_temperature"))
	assert.Equal(t, "homeassistant/switch/oasis_syberia/power/config",
		b.Discovery("switch", "power"))
}

func TestTopicBuilder_State(t *testing.T) {
	t.Parallel()
	b := mqtt.NewTopicBuilder(defaultHAConfig())
	assert.Equal(t, "oasis_syberia/state/supply_temperature", b.State("supply_temperature"))
}

func TestTopicBuilder_Command(t *testing.T) {
	t.Parallel()
	b := mqtt.NewTopicBuilder(defaultHAConfig())
	assert.Equal(t, "oasis_syberia/cmd/power", b.Command("power"))
}

func TestTopicBuilder_CommandWildcard(t *testing.T) {
	t.Parallel()
	b := mqtt.NewTopicBuilder(defaultHAConfig())
	assert.Equal(t, "oasis_syberia/cmd/+", b.CommandWildcard())
}

func TestTopicBuilder_Availability(t *testing.T) {
	t.Parallel()
	b := mqtt.NewTopicBuilder(defaultHAConfig())
	assert.Equal(t, "oasis_syberia/availability", b.Availability())
}

func TestNewTopicBuilder_PanicsOnEmptyDiscoveryPrefix(t *testing.T) {
	t.Parallel()
	require.PanicsWithValue(t, "mqtt: HAConfig.DiscoveryPrefix is empty", func() {
		mqtt.NewTopicBuilder(config.HAConfig{DiscoveryPrefix: "", DevicePrefix: "oasis_syberia"})
	})
}

func TestNewTopicBuilder_PanicsOnEmptyDevicePrefix(t *testing.T) {
	t.Parallel()
	require.PanicsWithValue(t, "mqtt: HAConfig.DevicePrefix is empty", func() {
		mqtt.NewTopicBuilder(config.HAConfig{DiscoveryPrefix: "homeassistant", DevicePrefix: ""})
	})
}

func TestNewTopicBuilder_CustomPrefixes(t *testing.T) {
	t.Parallel()
	b := mqtt.NewTopicBuilder(config.HAConfig{DiscoveryPrefix: "ha", DevicePrefix: "dev"})
	assert.Equal(t, "ha/sensor/dev/x/config", b.Discovery("sensor", "x"))
	assert.Equal(t, "dev/state/x", b.State("x"))
	assert.Equal(t, "dev/cmd/x", b.Command("x"))
	assert.Equal(t, "dev/cmd/+", b.CommandWildcard())
	assert.Equal(t, "dev/availability", b.Availability())
}
