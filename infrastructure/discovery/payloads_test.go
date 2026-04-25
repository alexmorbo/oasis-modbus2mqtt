package discovery_test

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/discovery"
)

func marshalToMap(t *testing.T, v any) map[string]any {
	t.Helper()
	bytes, err := json.Marshal(v)
	require.NoError(t, err)
	out := map[string]any{}
	require.NoError(t, json.Unmarshal(bytes, &out))
	return out
}

func TestDeviceInfo_JSONShape(t *testing.T) {
	t.Parallel()
	d := discovery.DeviceInfo{
		Identifiers:  []string{"oasis_syberia"},
		Name:         "Oasis Syberia",
		Manufacturer: "GTC",
		Model:        "Syberia 5",
		SwVersion:    "1.2.3",
	}
	m := marshalToMap(t, d)
	assert.Equal(t, []any{"oasis_syberia"}, m["identifiers"])
	assert.Equal(t, "Oasis Syberia", m["name"])
	assert.Equal(t, "GTC", m["manufacturer"])
	assert.Equal(t, "Syberia 5", m["model"])
	assert.Equal(t, "1.2.3", m["sw_version"])
	_, ok := m["configuration_url"]
	assert.False(t, ok, "configuration_url should be omitted when empty")
}

func TestDeviceInfo_ConfigurationURL_Present(t *testing.T) {
	t.Parallel()
	d := discovery.DeviceInfo{ConfigurationURL: "http://oasis.local"}
	m := marshalToMap(t, d)
	assert.Equal(t, "http://oasis.local", m["configuration_url"])
}

func TestSensorPayload_JSONShape(t *testing.T) {
	t.Parallel()
	p := discovery.SensorPayload{
		StateTopic:                "oasis_syberia/state/supply_temperature",
		DeviceClass:               "temperature",
		UnitOfMeasurement:         "°C",
		StateClass:                "measurement",
		SuggestedDisplayPrecision: 1,
	}
	p.UniqueID = "oasis_syberia_supply_temperature"
	p.AvailabilityTopic = "oasis_syberia/availability"
	p.Name = "Supply temperature"
	p.PayloadAvailable = "online"
	p.PayloadNotAvailable = "offline"
	p.QoS = 1
	p.Device = discovery.DeviceInfo{Identifiers: []string{"oasis_syberia"}}

	m := marshalToMap(t, p)
	assert.Equal(t, "oasis_syberia/state/supply_temperature", m["state_topic"])
	assert.Equal(t, "temperature", m["device_class"])
	assert.Equal(t, "°C", m["unit_of_measurement"])
	assert.Equal(t, "measurement", m["state_class"])
	assert.Equal(t, float64(1), m["suggested_display_precision"])
	assert.Equal(t, "oasis_syberia_supply_temperature", m["unique_id"])
	assert.Equal(t, "Supply temperature", m["name"])
	assert.Equal(t, "online", m["payload_available"])
	assert.Equal(t, "offline", m["payload_not_available"])
	assert.Equal(t, "oasis_syberia/availability", m["availability_topic"])
	assert.Equal(t, float64(1), m["qos"])
}

func TestSensorPayload_OmitemptyOptionalFields(t *testing.T) {
	t.Parallel()
	p := discovery.SensorPayload{StateTopic: "oasis_syberia/state/x"}
	p.UniqueID = "oasis_syberia_x"
	p.AvailabilityTopic = "oasis_syberia/availability"

	m := marshalToMap(t, p)
	for _, k := range []string{
		"device_class", "unit_of_measurement", "state_class",
		"suggested_display_precision", "name", "object_id",
		"payload_available", "payload_not_available", "entity_category",
		"icon", "qos",
	} {
		_, ok := m[k]
		assert.Falsef(t, ok, "%q should be omitted when zero", k)
	}
	assert.Equal(t, "oasis_syberia/state/x", m["state_topic"])
	assert.Equal(t, "oasis_syberia_x", m["unique_id"])
	assert.Equal(t, "oasis_syberia/availability", m["availability_topic"])
}

func TestBinarySensorPayload_JSONShape(t *testing.T) {
	t.Parallel()
	p := discovery.BinarySensorPayload{
		StateTopic:  "oasis_syberia/state/heater_active",
		DeviceClass: "heat",
		PayloadOn:   "ON",
		PayloadOff:  "OFF",
	}
	p.UniqueID = "oasis_syberia_heater_active"
	p.AvailabilityTopic = "oasis_syberia/availability"

	m := marshalToMap(t, p)
	assert.Equal(t, "oasis_syberia/state/heater_active", m["state_topic"])
	assert.Equal(t, "heat", m["device_class"])
	assert.Equal(t, "ON", m["payload_on"])
	assert.Equal(t, "OFF", m["payload_off"])
}

func TestBinarySensorPayload_Omitempty(t *testing.T) {
	t.Parallel()
	p := discovery.BinarySensorPayload{StateTopic: "oasis_syberia/state/x"}
	p.UniqueID = "oasis_syberia_x"
	p.AvailabilityTopic = "oasis_syberia/availability"

	m := marshalToMap(t, p)
	for _, k := range []string{"device_class", "payload_on", "payload_off"} {
		_, ok := m[k]
		assert.Falsef(t, ok, "%q should be omitted when zero", k)
	}
}

func TestSwitchPayload_JSONShape(t *testing.T) {
	t.Parallel()
	p := discovery.SwitchPayload{
		StateTopic:   "oasis_syberia/state/power",
		CommandTopic: "oasis_syberia/cmd/power",
		PayloadOn:    "ON",
		PayloadOff:   "OFF",
	}
	p.UniqueID = "oasis_syberia_power"
	p.AvailabilityTopic = "oasis_syberia/availability"

	m := marshalToMap(t, p)
	assert.Equal(t, "oasis_syberia/state/power", m["state_topic"])
	assert.Equal(t, "oasis_syberia/cmd/power", m["command_topic"])
	assert.Equal(t, "ON", m["payload_on"])
	assert.Equal(t, "OFF", m["payload_off"])
}

func TestSwitchPayload_OmitemptyPayloadOnOff(t *testing.T) {
	t.Parallel()
	p := discovery.SwitchPayload{
		StateTopic:   "oasis_syberia/state/x",
		CommandTopic: "oasis_syberia/cmd/x",
	}
	p.UniqueID = "oasis_syberia_x"
	p.AvailabilityTopic = "oasis_syberia/availability"

	m := marshalToMap(t, p)
	_, hasOn := m["payload_on"]
	_, hasOff := m["payload_off"]
	assert.False(t, hasOn)
	assert.False(t, hasOff)
	// Required fields still present.
	assert.Equal(t, "oasis_syberia/state/x", m["state_topic"])
	assert.Equal(t, "oasis_syberia/cmd/x", m["command_topic"])
}

func TestClimatePayload_JSONShape(t *testing.T) {
	t.Parallel()
	p := discovery.ClimatePayload{
		Modes:                   []string{"off", "heat", "fan_only"},
		ModeStateTopic:          "oasis_syberia/state/hvac_mode",
		ModeCommandTopic:        "oasis_syberia/cmd/hvac_mode",
		TemperatureStateTopic:   "oasis_syberia/state/target_temperature",
		TemperatureCommandTopic: "oasis_syberia/cmd/target_temperature",
		TempStep:                0.5,
		MinTemp:                 5,
		MaxTemp:                 30,
		TemperatureUnit:         "C",
		CurrentTemperatureTopic: "oasis_syberia/state/room_temperature",
		FanModes:                []string{"1", "2", "3"},
		FanModeStateTopic:       "oasis_syberia/state/fan_target",
		FanModeCommandTopic:     "oasis_syberia/cmd/fan_target",
		ActionTopic:             "oasis_syberia/state/hvac_action",
	}
	p.UniqueID = "oasis_syberia_climate"
	p.ObjectID = "climate"
	p.AvailabilityTopic = "oasis_syberia/availability"

	m := marshalToMap(t, p)
	assert.Equal(t, []any{"off", "heat", "fan_only"}, m["modes"])
	assert.Equal(t, "oasis_syberia/state/hvac_mode", m["mode_state_topic"])
	assert.Equal(t, "oasis_syberia/cmd/hvac_mode", m["mode_command_topic"])
	assert.Equal(t, "oasis_syberia/state/target_temperature", m["temperature_state_topic"])
	assert.Equal(t, "oasis_syberia/cmd/target_temperature", m["temperature_command_topic"])
	assert.InDelta(t, 0.5, m["temp_step"], 1e-9)
	assert.InDelta(t, 5.0, m["min_temp"], 1e-9)
	assert.InDelta(t, 30.0, m["max_temp"], 1e-9)
	assert.Equal(t, "C", m["temperature_unit"])
	assert.Equal(t, "oasis_syberia/state/room_temperature", m["current_temperature_topic"])
	assert.Equal(t, []any{"1", "2", "3"}, m["fan_modes"])
	assert.Equal(t, "oasis_syberia/state/fan_target", m["fan_mode_state_topic"])
	assert.Equal(t, "oasis_syberia/cmd/fan_target", m["fan_mode_command_topic"])
	assert.Equal(t, "oasis_syberia/state/hvac_action", m["action_topic"])
	assert.Equal(t, "climate", m["object_id"])
}

func TestClimatePayload_TempStep_NotZero_Serialized(t *testing.T) {
	t.Parallel()

	with := discovery.ClimatePayload{
		Modes:                   []string{"off"},
		ModeStateTopic:          "x",
		ModeCommandTopic:        "x",
		TemperatureStateTopic:   "x",
		TemperatureCommandTopic: "x",
		TempStep:                0.5,
	}
	with.UniqueID = "u"
	with.AvailabilityTopic = "a"
	mWith := marshalToMap(t, with)
	assert.InDelta(t, 0.5, mWith["temp_step"], 1e-9)

	without := discovery.ClimatePayload{
		Modes:                   []string{"off"},
		ModeStateTopic:          "x",
		ModeCommandTopic:        "x",
		TemperatureStateTopic:   "x",
		TemperatureCommandTopic: "x",
	}
	without.UniqueID = "u"
	without.AvailabilityTopic = "a"
	mWithout := marshalToMap(t, without)
	_, ok := mWithout["temp_step"]
	assert.False(t, ok, "temp_step=0 must be omitted")
}

func TestBaseEntity_OmitemptyDefaults(t *testing.T) {
	t.Parallel()
	// Drive the base via a SensorPayload with only required state_topic+unique_id+availability.
	p := discovery.SensorPayload{StateTopic: "t"}
	p.UniqueID = "u"
	p.AvailabilityTopic = "a"

	m := marshalToMap(t, p)
	// Must be present.
	assert.Contains(t, m, "unique_id")
	assert.Contains(t, m, "device")
	assert.Contains(t, m, "availability_topic")
	assert.Contains(t, m, "state_topic")
	// Must be omitted.
	for _, k := range []string{
		"name", "object_id", "payload_available", "payload_not_available",
		"entity_category", "icon", "qos",
	} {
		_, ok := m[k]
		assert.Falsef(t, ok, "%q should be omitted when zero", k)
	}
}

func TestDiscovery_StructFields(t *testing.T) {
	t.Parallel()
	d := discovery.Discovery{Topic: "homeassistant/sensor/x/y/config", Payload: []byte(`{}`)}
	assert.Equal(t, "homeassistant/sensor/x/y/config", d.Topic)
	assert.Equal(t, []byte(`{}`), d.Payload)
}
