// Package discovery builds Home Assistant MQTT discovery payloads for every
// entity the bridge exposes (sensors, switches, climate, derived binary
// sensors). It is pure data: no I/O, no goroutines, no global state.
package discovery

// DeviceInfo is the shared "device" block stamped into every HA discovery
// payload so all entities are grouped under a single HA device.
type DeviceInfo struct {
	Identifiers      []string `json:"identifiers"`
	Name             string   `json:"name"`
	Manufacturer     string   `json:"manufacturer"`
	Model            string   `json:"model"`
	SwVersion        string   `json:"sw_version"`
	ConfigurationURL string   `json:"configuration_url,omitempty"`
}

// baseEntity holds the HA discovery fields common to every entity payload
// (sensor, binary_sensor, switch, climate). It is embedded into each public
// payload struct and flattens automatically during json.Marshal.
type baseEntity struct {
	Name                string     `json:"name,omitempty"`
	UniqueID            string     `json:"unique_id"`
	ObjectID            string     `json:"object_id,omitempty"`
	Device              DeviceInfo `json:"device"`
	AvailabilityTopic   string     `json:"availability_topic"`
	PayloadAvailable    string     `json:"payload_available,omitempty"`
	PayloadNotAvailable string     `json:"payload_not_available,omitempty"`
	EntityCategory      string     `json:"entity_category,omitempty"`
	Icon                string     `json:"icon,omitempty"`
	QoS                 int        `json:"qos,omitempty"`
}

// Discovery pairs an HA discovery topic with its serialized JSON payload. It
// is the unit returned by Builder.Build for the publish use case to consume.
type Discovery struct {
	Topic   string
	Payload []byte
}

// SensorPayload is the HA discovery payload for a sensor entity.
type SensorPayload struct {
	baseEntity
	StateTopic                string `json:"state_topic"`
	DeviceClass               string `json:"device_class,omitempty"`
	UnitOfMeasurement         string `json:"unit_of_measurement,omitempty"`
	StateClass                string `json:"state_class,omitempty"`
	SuggestedDisplayPrecision int    `json:"suggested_display_precision,omitempty"`
}

// BinarySensorPayload is the HA discovery payload for a binary_sensor entity.
type BinarySensorPayload struct {
	baseEntity
	StateTopic  string `json:"state_topic"`
	DeviceClass string `json:"device_class,omitempty"`
	PayloadOn   string `json:"payload_on,omitempty"`
	PayloadOff  string `json:"payload_off,omitempty"`
}

// SwitchPayload is the HA discovery payload for a switch entity.
type SwitchPayload struct {
	baseEntity
	StateTopic   string `json:"state_topic"`
	CommandTopic string `json:"command_topic"`
	PayloadOn    string `json:"payload_on,omitempty"`
	PayloadOff   string `json:"payload_off,omitempty"`
}

// ClimatePayload is the HA discovery payload for the composite climate entity.
type ClimatePayload struct {
	baseEntity
	Modes                   []string `json:"modes"`
	ModeStateTopic          string   `json:"mode_state_topic"`
	ModeCommandTopic        string   `json:"mode_command_topic"`
	TemperatureStateTopic   string   `json:"temperature_state_topic"`
	TemperatureCommandTopic string   `json:"temperature_command_topic"`
	TempStep                float64  `json:"temp_step,omitempty"`
	MinTemp                 float64  `json:"min_temp,omitempty"`
	MaxTemp                 float64  `json:"max_temp,omitempty"`
	TemperatureUnit         string   `json:"temperature_unit,omitempty"`
	CurrentTemperatureTopic string   `json:"current_temperature_topic,omitempty"`
	FanModes                []string `json:"fan_modes,omitempty"`
	FanModeStateTopic       string   `json:"fan_mode_state_topic,omitempty"`
	FanModeCommandTopic     string   `json:"fan_mode_command_topic,omitempty"`
	ActionTopic             string   `json:"action_topic,omitempty"`
}
