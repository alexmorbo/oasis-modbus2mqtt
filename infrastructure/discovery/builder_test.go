package discovery_test

import (
	"encoding/json"
	"io"
	"log/slog"
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/config"
	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/discovery"
	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/mqtt"
)

const (
	testDiscoveryPrefix = "homeassistant"
	testDevicePrefix    = "oasis_syberia"
	testDeviceName      = "Oasis Syberia"
	testManufacturer    = "GTC"
	testModel           = "Syberia 5"
	testFirmware        = "1.2.3"
)

func testHACfg() config.HAConfig {
	return config.HAConfig{
		DiscoveryPrefix: testDiscoveryPrefix,
		DevicePrefix:    testDevicePrefix,
		DeviceName:      testDeviceName,
		Manufacturer:    testManufacturer,
		Model:           testModel,
	}
}

func newBuilder(t *testing.T) *discovery.Builder {
	t.Helper()
	topics := mqtt.NewTopicBuilder(testHACfg())
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	return discovery.NewBuilder(topics, testHACfg(), logger)
}

func buildAll(t *testing.T) []discovery.Discovery {
	t.Helper()
	b := newBuilder(t)
	out, err := b.Build(testFirmware)
	require.NoError(t, err)
	return out
}

func findByTopic(items []discovery.Discovery, suffix string) (discovery.Discovery, bool) {
	for _, d := range items {
		if strings.HasSuffix(d.Topic, suffix) {
			return d, true
		}
	}
	return discovery.Discovery{}, false
}

func unmarshalMap(t *testing.T, payload []byte) map[string]any {
	t.Helper()
	out := map[string]any{}
	require.NoError(t, json.Unmarshal(payload, &out))
	return out
}

func TestNewBuilder_NilTopics_Panics(t *testing.T) {
	t.Parallel()
	require.PanicsWithValue(t,
		"discovery: NewBuilder requires non-nil TopicBuilder",
		func() {
			_ = discovery.NewBuilder(nil, testHACfg(), nil)
		},
	)
}

func TestNewBuilder_NilLogger_FallsBackToDefault(t *testing.T) {
	t.Parallel()
	topics := mqtt.NewTopicBuilder(testHACfg())
	b := discovery.NewBuilder(topics, testHACfg(), nil)
	out, err := b.Build(testFirmware)
	require.NoError(t, err)
	require.NotEmpty(t, out)
}

func TestBuild_Count(t *testing.T) {
	t.Parallel()
	out := buildAll(t)
	// Catalog has 13 entities with Hint.Kind != None (12 sensors + 1 switch),
	// plus 1 climate + 3 binary_sensors + 1 derived current_operation sensor,
	// plus 14 panel settings (10 switches, 1 select, 3 numbers).
	assert.Len(t, out, 32)
}

func TestBuild_TopicsAreCorrect(t *testing.T) {
	t.Parallel()
	out := buildAll(t)
	re := regexp.MustCompile(`^homeassistant/(sensor|binary_sensor|switch|select|number|climate)/oasis_syberia/[a-z0-9_]+/config$`)
	for _, d := range out {
		assert.Truef(t, re.MatchString(d.Topic), "topic %q does not match discovery shape", d.Topic)
	}
}

func TestBuild_TopicsAreUnique(t *testing.T) {
	t.Parallel()
	out := buildAll(t)
	seen := map[string]struct{}{}
	for _, d := range out {
		_, dup := seen[d.Topic]
		assert.Falsef(t, dup, "duplicate topic %q", d.Topic)
		seen[d.Topic] = struct{}{}
	}
}

func TestBuild_PayloadsAreValidJSON(t *testing.T) {
	t.Parallel()
	out := buildAll(t)
	for _, d := range out {
		var v any
		assert.NoErrorf(t, json.Unmarshal(d.Payload, &v), "topic %q has invalid JSON", d.Topic)
	}
}

func TestBuild_ContainsClimate(t *testing.T) {
	t.Parallel()
	out := buildAll(t)
	d, ok := findByTopic(out, "/climate/oasis_syberia/climate/config")
	require.True(t, ok, "climate discovery not found")

	var p discovery.ClimatePayload
	require.NoError(t, json.Unmarshal(d.Payload, &p))

	assert.Equal(t, []string{"off", "heat", "fan_only"}, p.Modes)
	assert.Len(t, p.FanModes, 10)
	assert.Equal(t, []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"}, p.FanModes)
	assert.InDelta(t, 0.5, p.TempStep, 1e-9)
	assert.InDelta(t, 5.0, p.MinTemp, 1e-9)
	assert.InDelta(t, 30.0, p.MaxTemp, 1e-9)
	assert.Equal(t, "C", p.TemperatureUnit)
	assert.Equal(t, "oasis_syberia/state/hvac_mode", p.ModeStateTopic)
	assert.Equal(t, "oasis_syberia/cmd/hvac_mode", p.ModeCommandTopic)
	assert.Equal(t, "oasis_syberia/state/target_temperature", p.TemperatureStateTopic)
	assert.Equal(t, "oasis_syberia/cmd/target_temperature", p.TemperatureCommandTopic)
	assert.Equal(t, "oasis_syberia/state/room_temperature", p.CurrentTemperatureTopic)
	assert.Equal(t, "oasis_syberia/state/fan_target", p.FanModeStateTopic)
	assert.Equal(t, "oasis_syberia/cmd/fan_target", p.FanModeCommandTopic)
	assert.Equal(t, "oasis_syberia/state/hvac_action", p.ActionTopic)
	assert.Equal(t, "oasis_syberia_climate", p.UniqueID)
	assert.Equal(t, "climate.oasis_syberia", p.DefaultEntityID)
}

func TestBuild_ContainsSwitch(t *testing.T) {
	t.Parallel()
	out := buildAll(t)
	d, ok := findByTopic(out, "/switch/oasis_syberia/power/config")
	require.True(t, ok, "switch power discovery not found")

	var p discovery.SwitchPayload
	require.NoError(t, json.Unmarshal(d.Payload, &p))

	assert.Equal(t, "oasis_syberia/state/power", p.StateTopic)
	assert.Equal(t, "oasis_syberia/cmd/power", p.CommandTopic)
	assert.Equal(t, "ON", p.PayloadOn)
	assert.Equal(t, "OFF", p.PayloadOff)
	assert.Equal(t, "oasis_syberia_power", p.UniqueID)
	assert.Equal(t, "Power", p.Name)
	assert.Equal(t, "mdi:power", p.Icon)
}

func TestBuild_ContainsSensor_SupplyTemperature(t *testing.T) {
	t.Parallel()
	out := buildAll(t)
	d, ok := findByTopic(out, "/sensor/oasis_syberia/supply_temperature/config")
	require.True(t, ok, "supply_temperature sensor not found")

	var p discovery.SensorPayload
	require.NoError(t, json.Unmarshal(d.Payload, &p))
	assert.Equal(t, "oasis_syberia/state/supply_temperature", p.StateTopic)
	assert.Equal(t, "temperature", p.DeviceClass)
	assert.Equal(t, "°C", p.UnitOfMeasurement)
	assert.Equal(t, "measurement", p.StateClass)
	assert.Equal(t, 1, p.SuggestedDisplayPrecision)
}

func TestBuild_DiagnosticCategoriesSet(t *testing.T) {
	t.Parallel()
	out := buildAll(t)
	d, ok := findByTopic(out, "/sensor/oasis_syberia/firmware/config")
	require.True(t, ok, "firmware sensor not found")

	m := unmarshalMap(t, d.Payload)
	assert.Equal(t, "diagnostic", m["entity_category"])
}

func TestBuild_DefaultCategoryOmitted(t *testing.T) {
	t.Parallel()
	out := buildAll(t)
	// supply_temperature has Category=Default — entity_category should be omitted.
	d, ok := findByTopic(out, "/sensor/oasis_syberia/supply_temperature/config")
	require.True(t, ok)

	m := unmarshalMap(t, d.Payload)
	_, has := m["entity_category"]
	assert.False(t, has, "default category must be omitted from JSON")
}

func TestBuild_DeviceBlockConsistent(t *testing.T) {
	t.Parallel()
	out := buildAll(t)
	for _, d := range out {
		m := unmarshalMap(t, d.Payload)
		dev, ok := m["device"].(map[string]any)
		require.Truef(t, ok, "topic %q missing device block", d.Topic)
		assert.Equal(t, []any{testDevicePrefix}, dev["identifiers"])
		assert.Equal(t, testDeviceName, dev["name"])
		assert.Equal(t, testManufacturer, dev["manufacturer"])
		assert.Equal(t, testModel, dev["model"])
		assert.Equal(t, testFirmware, dev["sw_version"])
	}
}

func TestBuild_AvailabilityTopicConsistent(t *testing.T) {
	t.Parallel()
	out := buildAll(t)
	expected := "oasis_syberia/availability"
	for _, d := range out {
		m := unmarshalMap(t, d.Payload)
		assert.Equalf(t, expected, m["availability_topic"], "topic %q wrong availability", d.Topic)
		assert.Equal(t, "online", m["payload_available"])
		assert.Equal(t, "offline", m["payload_not_available"])
	}
}

func TestBuild_UniqueIDPrefixed(t *testing.T) {
	t.Parallel()
	out := buildAll(t)
	for _, d := range out {
		m := unmarshalMap(t, d.Payload)
		uid, ok := m["unique_id"].(string)
		require.Truef(t, ok, "topic %q missing unique_id", d.Topic)
		assert.Truef(t, strings.HasPrefix(uid, testDevicePrefix+"_"),
			"unique_id %q must be prefixed with %q", uid, testDevicePrefix)
	}
}

func TestBuild_QoSAlwaysOne(t *testing.T) {
	t.Parallel()
	out := buildAll(t)
	for _, d := range out {
		m := unmarshalMap(t, d.Payload)
		assert.Equalf(t, float64(1), m["qos"], "topic %q wrong qos", d.Topic)
	}
}

func TestBuild_HardcodedBinarySensors(t *testing.T) {
	t.Parallel()
	out := buildAll(t)

	cases := []struct {
		objectID    string
		deviceClass string
		payloadOn   string
		payloadOff  string
		diagnostic  bool
	}{
		{"heater_active", "heat", "ON", "OFF", false},
		{"damper_open", "opening", "OPEN", "CLOSED", false},
		{"problem", "problem", "ON", "OFF", true},
	}
	for _, tc := range cases {
		suffix := "/binary_sensor/oasis_syberia/" + tc.objectID + "/config"
		d, ok := findByTopic(out, suffix)
		require.Truef(t, ok, "binary_sensor %q not found", tc.objectID)

		var p discovery.BinarySensorPayload
		require.NoError(t, json.Unmarshal(d.Payload, &p))
		assert.Equal(t, "oasis_syberia/state/"+tc.objectID, p.StateTopic)
		assert.Equal(t, tc.deviceClass, p.DeviceClass)
		assert.Equal(t, tc.payloadOn, p.PayloadOn)
		assert.Equal(t, tc.payloadOff, p.PayloadOff)
		assert.Equal(t, "oasis_syberia_"+tc.objectID, p.UniqueID)

		m := unmarshalMap(t, d.Payload)
		if tc.diagnostic {
			assert.Equal(t, "diagnostic", m["entity_category"])
		} else {
			_, has := m["entity_category"]
			assert.False(t, has, "non-diagnostic binary_sensor must omit entity_category")
		}
	}
}

func TestBuild_FirmwareEmptyString_StillBuilds(t *testing.T) {
	t.Parallel()
	b := newBuilder(t)
	out, err := b.Build("")
	require.NoError(t, err)
	require.NotEmpty(t, out)

	// sw_version is in DeviceInfo without omitempty, so it serializes as "" when empty.
	for _, d := range out {
		m := unmarshalMap(t, d.Payload)
		dev, ok := m["device"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "", dev["sw_version"])
	}
}

func TestBuild_SensorCount(t *testing.T) {
	t.Parallel()
	out := buildAll(t)
	var sensors, switches, climates, binSensors, selects, numbers int
	for _, d := range out {
		switch {
		case strings.Contains(d.Topic, "/sensor/"):
			sensors++
		case strings.Contains(d.Topic, "/switch/"):
			switches++
		case strings.Contains(d.Topic, "/climate/"):
			climates++
		case strings.Contains(d.Topic, "/binary_sensor/"):
			binSensors++
		case strings.Contains(d.Topic, "/select/"):
			selects++
		case strings.Contains(d.Topic, "/number/"):
			numbers++
		}
	}
	// 12 sensors from catalog + derived current_operation, power + 10 setting
	// switches, 1 climate, 3 binary_sensors, 1 select, 3 numbers.
	assert.Equal(t, 13, sensors)
	assert.Equal(t, 11, switches)
	assert.Equal(t, 1, climates)
	assert.Equal(t, 3, binSensors)
	assert.Equal(t, 1, selects)
	assert.Equal(t, 3, numbers)
}

func TestBuild_SensorFromCatalog_NoDeviceClass_OmitsField(t *testing.T) {
	t.Parallel()
	out := buildAll(t)
	// filter_clog has no DeviceClass — must be omitted.
	d, ok := findByTopic(out, "/sensor/oasis_syberia/filter_clog/config")
	require.True(t, ok)
	m := unmarshalMap(t, d.Payload)
	_, has := m["device_class"]
	assert.False(t, has, "device_class must be omitted when DeviceClassNone")
	assert.Equal(t, "%", m["unit_of_measurement"])
	assert.Equal(t, "mdi:air-filter", m["icon"])
}

func TestCategoryString_Coverage(t *testing.T) {
	t.Parallel()
	// Default, Diagnostic and Config are all reachable through Build: config
	// via the panel settings (see TestBuild_AllSettingsAreConfigCategory).
	out := buildAll(t)
	require.NotEmpty(t, out)
}

func TestBuild_IntegrationFlow(t *testing.T) {
	t.Parallel()
	b := newBuilder(t)
	out, err := b.Build("2.0.0")
	require.NoError(t, err)
	require.Greater(t, len(out), 0)

	for _, d := range out {
		require.NotEmpty(t, d.Topic)
		require.NotEmpty(t, d.Payload)
		require.True(t, json.Valid(d.Payload))
	}
}

func TestBuild_SensorsAndSwitchesPresent(t *testing.T) {
	t.Parallel()
	out := buildAll(t)

	// Verify at least the key catalog entries are present
	minEntities := map[string]int{
		"supply_temperature": 0,
		"power":              0,
		"climate":            0,
		"heater_active":      0,
	}

	for _, d := range out {
		for key := range minEntities {
			if strings.Contains(d.Topic, key) {
				minEntities[key]++
			}
		}
	}

	for key, count := range minEntities {
		assert.Greaterf(t, count, 0, "expected %q in build output", key)
	}
}

func TestBuild_MarshalErrors_Unlikely(t *testing.T) {
	t.Parallel()
	// Verify all payloads marshal successfully (no circular refs, etc.)
	b := newBuilder(t)
	out, err := b.Build("1.0.0")
	require.NoError(t, err)

	for _, d := range out {
		require.NotEmpty(t, d.Payload)
		var v map[string]any
		require.NoError(t, json.Unmarshal(d.Payload, &v))
	}
}

func TestBuilder_BuildRobustness(t *testing.T) {
	t.Parallel()
	// Test with various firmware strings, including edge cases
	b := newBuilder(t)

	for _, fw := range []string{"", "1.0.0", "v2.3.4-beta", "unknown"} {
		out, err := b.Build(fw)
		require.NoError(t, err, "failed for firmware %q", fw)
		require.Greater(t, len(out), 0)

		for _, d := range out {
			m := unmarshalMap(t, d.Payload)
			dev := m["device"].(map[string]any)
			assert.Equal(t, fw, dev["sw_version"], "firmware %q not preserved", fw)
		}
	}
}

func TestBuilder_AvailabilityPayloads(t *testing.T) {
	t.Parallel()
	out := buildAll(t)
	for _, d := range out {
		m := unmarshalMap(t, d.Payload)
		avail := m["availability_topic"].(string)
		require.NotEmpty(t, avail)
		online := m["payload_available"].(string)
		offline := m["payload_not_available"].(string)
		assert.Equal(t, "online", online)
		assert.Equal(t, "offline", offline)
	}
}

func TestBuilder_AllPayloadTypes(t *testing.T) {
	t.Parallel()
	out := buildAll(t)

	var hasSensor, hasSwitch, hasClimate, hasBinarySensor bool
	for _, d := range out {
		if strings.Contains(d.Topic, "/sensor/") {
			hasSensor = true
		}
		if strings.Contains(d.Topic, "/switch/") {
			hasSwitch = true
		}
		if strings.Contains(d.Topic, "/climate/") {
			hasClimate = true
		}
		if strings.Contains(d.Topic, "/binary_sensor/") {
			hasBinarySensor = true
		}
	}

	assert.True(t, hasSensor, "at least one sensor expected")
	assert.True(t, hasSwitch, "at least one switch expected")
	assert.True(t, hasClimate, "climate entity expected")
	assert.True(t, hasBinarySensor, "at least one binary_sensor expected")
}

func TestBuild_FilterDaysSensors(t *testing.T) {
	t.Parallel()
	out := buildAll(t)

	tests := []struct {
		objectID     string
		name         string
		stateClass   any
		wantCategory any
	}{
		{objectID: "filter_days_left", name: "Filter days left", stateClass: "measurement", wantCategory: nil},
		{objectID: "filter_resource", name: "Filter resource", stateClass: nil, wantCategory: "diagnostic"},
	}

	for _, tc := range tests {
		t.Run(tc.objectID, func(t *testing.T) {
			t.Parallel()
			topic := "homeassistant/sensor/oasis_syberia/" + tc.objectID + "/config"
			var found *discovery.Discovery
			for i := range out {
				if out[i].Topic == topic {
					found = &out[i]
				}
			}
			require.NotNilf(t, found, "discovery topic %s not built", topic)

			m := unmarshalMap(t, found.Payload)
			assert.Equal(t, tc.name, m["name"])
			assert.Equal(t, "oasis_syberia_"+tc.objectID, m["unique_id"])
			assert.Equal(t, "oasis_syberia/state/"+tc.objectID, m["state_topic"])
			assert.Equal(t, "duration", m["device_class"])
			assert.Equal(t, "d", m["unit_of_measurement"])
			assert.Equal(t, "mdi:air-filter", m["icon"])
			assert.Equal(t, tc.stateClass, m["state_class"])
			assert.Equal(t, tc.wantCategory, m["entity_category"])
			_, hasCmd := m["command_topic"]
			assert.False(t, hasCmd, "filter sensors are read-only")
		})
	}
}

func componentOf(t *testing.T, topic string) (component, objectID string) {
	t.Helper()
	parts := strings.Split(topic, "/")
	require.Lenf(t, parts, 5, "topic %q", topic)
	return parts[1], parts[3]
}

func TestBuild_DefaultEntityIDOnEveryPayload(t *testing.T) {
	t.Parallel()
	out := buildAll(t)
	require.Len(t, out, 32)
	for _, d := range out {
		component, objectID := componentOf(t, d.Topic)
		m := unmarshalMap(t, d.Payload)
		want := component + ".oasis_syberia_" + objectID
		if component == "climate" {
			want = "climate.oasis_syberia"
		}
		assert.Equalf(t, want, m["default_entity_id"], "topic %q", d.Topic)
		_, hasObjectID := m["object_id"]
		assert.Falsef(t, hasObjectID, "topic %q must not carry object_id", d.Topic)
	}
}

// settingPayload finds a setting's discovery payload and checks the fields
// every config entity shares.
func settingPayload(t *testing.T, out []discovery.Discovery, component, objectID string) map[string]any {
	t.Helper()
	topic := "homeassistant/" + component + "/oasis_syberia/" + objectID + "/config"
	var found *discovery.Discovery
	for i := range out {
		if out[i].Topic == topic {
			found = &out[i]
		}
	}
	require.NotNilf(t, found, "discovery topic %s not built", topic)
	m := unmarshalMap(t, found.Payload)
	assert.Equal(t, "config", m["entity_category"])
	assert.Equal(t, "oasis_syberia/state/"+objectID, m["state_topic"])
	assert.Equal(t, "oasis_syberia/cmd/"+objectID, m["command_topic"])
	assert.Equal(t, "oasis_syberia/availability", m["availability_topic"])
	assert.Equal(t, "online", m["payload_available"])
	assert.Equal(t, "offline", m["payload_not_available"])
	assert.Equal(t, "oasis_syberia_"+objectID, m["unique_id"])
	assert.Equal(t, component+".oasis_syberia_"+objectID, m["default_entity_id"])
	return m
}

func TestBuild_SettingSwitch_AlarmBeep(t *testing.T) {
	t.Parallel()
	m := settingPayload(t, buildAll(t), "switch", "alarm_beep")
	assert.Equal(t, "Alarm beep", m["name"])
	assert.Equal(t, "ON", m["payload_on"])
	assert.Equal(t, "OFF", m["payload_off"])
	assert.Equal(t, "mdi:volume-high", m["icon"])
}

func TestBuild_ActiveFilterSwitch(t *testing.T) {
	t.Parallel()
	m := settingPayload(t, buildAll(t), "switch", "active_filter")
	assert.Equal(t, "Active filter", m["name"])
	assert.Equal(t, "mdi:air-purifier", m["icon"])
}

func TestBuild_ControlPrioritySelect(t *testing.T) {
	t.Parallel()
	m := settingPayload(t, buildAll(t), "select", "control_priority")
	assert.Equal(t, "Control priority", m["name"])
	assert.Equal(t, []any{"temperature", "humidity", "co2"}, m["options"])
	assert.Equal(t, "mdi:format-list-numbered", m["icon"])
}

func TestBuild_SettingNumbers(t *testing.T) {
	t.Parallel()
	out := buildAll(t)
	tests := []struct {
		objectID    string
		min, max    float64
		step        float64
		mode        string
		unit        string
		deviceClass any
	}{
		{objectID: "dark_brightness", min: 20, max: 100, step: 1, mode: "slider", unit: "%", deviceClass: nil},
		{objectID: "humidity_target", min: 20, max: 95, step: 1, mode: "slider", unit: "%", deviceClass: "humidity"},
		{objectID: "co2_target", min: 500, max: 2000, step: 10, mode: "box", unit: "ppm", deviceClass: "carbon_dioxide"},
	}
	for _, tc := range tests {
		t.Run(tc.objectID, func(t *testing.T) {
			t.Parallel()
			m := settingPayload(t, out, "number", tc.objectID)
			assert.InDelta(t, tc.min, m["min"], 1e-9)
			assert.InDelta(t, tc.max, m["max"], 1e-9)
			assert.InDelta(t, tc.step, m["step"], 1e-9)
			assert.Equal(t, tc.mode, m["mode"])
			assert.Equal(t, tc.unit, m["unit_of_measurement"])
			assert.Equal(t, tc.deviceClass, m["device_class"])
		})
	}
}

func TestBuild_AllSettingsAreConfigCategory(t *testing.T) {
	t.Parallel()
	n := 0
	for _, d := range buildAll(t) {
		component, _ := componentOf(t, d.Topic)
		m := unmarshalMap(t, d.Payload)
		if m["entity_category"] == "config" {
			n++
			assert.Containsf(t, []string{"switch", "select", "number"}, component, "topic %q", d.Topic)
		}
	}
	assert.Equal(t, 14, n)
}
