package discovery

import (
	"encoding/json"
	"fmt"
	"log/slog"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/catalog"
	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/config"
	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/mqtt"
)

// Hardcoded object identifiers and component names that are not present in
// the catalog because they are derived from bitfields (binary sensors) or
// composed across multiple registers (climate).
const (
	// BinarySensorHeaterActive is the object id of the derived heater_active binary_sensor.
	BinarySensorHeaterActive = "heater_active"
	// BinarySensorDamperOpen is the object id of the derived damper_open binary_sensor.
	BinarySensorDamperOpen = "damper_open"
	// BinarySensorProblem is the object id of the derived problem binary_sensor.
	BinarySensorProblem = "problem"
	// ClimateObjectID is the object id of the composite climate entity.
	ClimateObjectID = "climate"
)

// Builder turns the static catalog plus a small set of hardcoded extras
// (climate, three derived binary sensors) into a slice of HA discovery
// payloads ready for publication. It is stateless and safe for repeated
// calls; the only input that varies between calls is the firmware string
// stamped into every device block.
type Builder struct {
	topics *mqtt.TopicBuilder
	haCfg  config.HAConfig
	logger *slog.Logger
}

// NewBuilder constructs a Builder. It panics when topics is nil — discovery
// without a topic builder cannot produce valid output. A nil logger falls
// back to slog.Default().
func NewBuilder(topics *mqtt.TopicBuilder, haCfg config.HAConfig, logger *slog.Logger) *Builder {
	if topics == nil {
		panic("discovery: NewBuilder requires non-nil TopicBuilder")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Builder{topics: topics, haCfg: haCfg, logger: logger}
}

// Build produces an ordered slice of Discovery messages: every catalog
// register with a sensor/switch hint, plus the hardcoded climate entity and
// three derived binary sensors. firmware is stamped into device.sw_version
// on every payload.
func (b *Builder) Build(firmware string) ([]Discovery, error) {
	out := make([]Discovery, 0, 16)

	for _, reg := range catalog.RegistersWithEntity() {
		switch reg.Hint.Kind {
		case catalog.EntityKindSensor:
			d, err := b.buildSensor(reg, firmware)
			if err != nil {
				return nil, err
			}
			out = append(out, d)
		case catalog.EntityKindSwitch:
			d, err := b.buildSwitch(reg, firmware)
			if err != nil {
				return nil, err
			}
			out = append(out, d)
		case catalog.EntityKindBinarySensor, catalog.EntityKindClimate,
			catalog.EntityKindButton, catalog.EntityKindNone:
			// Catalog currently has none of these — binary sensors and climate are
			// hardcoded below; button/none are intentional skips.
			b.logger.Debug("discovery: skipping unsupported catalog kind",
				"object_id", reg.Hint.ObjectID, "kind", reg.Hint.Kind.String())
		default:
			b.logger.Debug("discovery: skipping unknown catalog kind",
				"object_id", reg.Hint.ObjectID, "kind", reg.Hint.Kind.String())
		}
	}

	climate, err := b.buildClimate(firmware)
	if err != nil {
		return nil, err
	}
	out = append(out, climate)

	heater, err := b.buildBinarySensor(BinarySensorHeaterActive, "Heater active",
		"heat", "ON", "OFF", catalog.EntityCategoryDefault, "", firmware)
	if err != nil {
		return nil, err
	}
	out = append(out, heater)

	damper, err := b.buildBinarySensor(BinarySensorDamperOpen, "Damper open",
		"opening", "OPEN", "CLOSED", catalog.EntityCategoryDefault, "", firmware)
	if err != nil {
		return nil, err
	}
	out = append(out, damper)

	problem, err := b.buildBinarySensor(BinarySensorProblem, "Problem",
		"problem", "ON", "OFF", catalog.EntityCategoryDiagnostic, "", firmware)
	if err != nil {
		return nil, err
	}
	out = append(out, problem)

	return out, nil
}

func (b *Builder) deviceInfo(firmware string) DeviceInfo {
	return DeviceInfo{
		Identifiers:      []string{b.haCfg.DevicePrefix},
		Name:             b.haCfg.DeviceName,
		Manufacturer:     b.haCfg.Manufacturer,
		Model:            b.haCfg.Model,
		SwVersion:        firmware,
		ConfigurationURL: "",
	}
}

func (b *Builder) baseFor(objectID, name string, hint catalog.EntityHint, firmware string) baseEntity {
	return baseEntity{
		Name:                name,
		UniqueID:            b.haCfg.DevicePrefix + "_" + objectID,
		ObjectID:            "",
		Device:              b.deviceInfo(firmware),
		AvailabilityTopic:   b.topics.Availability(),
		PayloadAvailable:    "online",
		PayloadNotAvailable: "offline",
		EntityCategory:      categoryString(hint.Category),
		Icon:                hint.Icon,
		QoS:                 1,
	}
}

func categoryString(c catalog.EntityCategory) string {
	switch c {
	case catalog.EntityCategoryDiagnostic:
		return "diagnostic"
	case catalog.EntityCategoryConfig:
		return "config"
	case catalog.EntityCategoryDefault:
		return ""
	default:
		return ""
	}
}

func (b *Builder) buildSensor(reg catalog.RegisterDef, firmware string) (Discovery, error) {
	payload := SensorPayload{
		baseEntity:                b.baseFor(reg.Hint.ObjectID, reg.Hint.Name, reg.Hint, firmware),
		StateTopic:                b.topics.State(reg.Hint.ObjectID),
		DeviceClass:               string(reg.Hint.DeviceClass),
		UnitOfMeasurement:         reg.Hint.Unit,
		StateClass:                string(reg.Hint.StateClass),
		SuggestedDisplayPrecision: reg.Hint.Precision,
	}
	return b.marshal("sensor", reg.Hint.ObjectID, payload)
}

func (b *Builder) buildSwitch(reg catalog.RegisterDef, firmware string) (Discovery, error) {
	payload := SwitchPayload{
		baseEntity:   b.baseFor(reg.Hint.ObjectID, reg.Hint.Name, reg.Hint, firmware),
		StateTopic:   b.topics.State(reg.Hint.ObjectID),
		CommandTopic: b.topics.Command(reg.Hint.ObjectID),
		PayloadOn:    "ON",
		PayloadOff:   "OFF",
	}
	return b.marshal("switch", reg.Hint.ObjectID, payload)
}

func (b *Builder) buildClimate(firmware string) (Discovery, error) {
	hint := catalog.EntityHint{Icon: "mdi:hvac"}
	base := b.baseFor(ClimateObjectID, "", hint, firmware)
	base.ObjectID = ClimateObjectID

	payload := ClimatePayload{
		baseEntity:              base,
		Modes:                   []string{"off", "heat", "fan_only"},
		ModeStateTopic:          b.topics.State("hvac_mode"),
		ModeCommandTopic:        b.topics.Command("hvac_mode"),
		TemperatureStateTopic:   b.topics.State("target_temperature"),
		TemperatureCommandTopic: b.topics.Command("target_temperature"),
		TempStep:                0.5,
		MinTemp:                 5,
		MaxTemp:                 30,
		TemperatureUnit:         "C",
		CurrentTemperatureTopic: b.topics.State("room_temperature"),
		FanModes:                []string{"1", "2", "3", "4", "5", "6", "7", "8", "9", "10"},
		FanModeStateTopic:       b.topics.State("fan_target"),
		FanModeCommandTopic:     b.topics.Command("fan_target"),
		ActionTopic:             b.topics.State("hvac_action"),
	}
	return b.marshal("climate", ClimateObjectID, payload)
}

func (b *Builder) buildBinarySensor(
	objectID, name, deviceClass, payloadOn, payloadOff string,
	category catalog.EntityCategory, icon, firmware string,
) (Discovery, error) {
	hint := catalog.EntityHint{Category: category, Icon: icon}
	payload := BinarySensorPayload{
		baseEntity:  b.baseFor(objectID, name, hint, firmware),
		StateTopic:  b.topics.State(objectID),
		DeviceClass: deviceClass,
		PayloadOn:   payloadOn,
		PayloadOff:  payloadOff,
	}
	return b.marshal("binary_sensor", objectID, payload)
}

func (b *Builder) marshal(component, objectID string, payload any) (Discovery, error) {
	bytes, err := json.Marshal(payload)
	if err != nil {
		return Discovery{}, fmt.Errorf("discovery: marshal %s/%s: %w", component, objectID, err)
	}
	return Discovery{Topic: b.topics.Discovery(component, objectID), Payload: bytes}, nil
}
