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
	// SensorCurrentOperation is the object id of the derived current_operation
	// sensor (text label of OperatingState — close_damper / preheat_calorifier
	// / electric_calorifier_purge / etc.).
	SensorCurrentOperation = "current_operation"
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
// register with a sensor/switch hint, the hardcoded climate entity, three
// derived binary sensors, the derived current_operation sensor, and one
// config entity per catalog.Settings row. firmware is stamped into
// device.sw_version on every payload.
func (b *Builder) Build(firmware string) ([]Discovery, error) {
	out := make([]Discovery, 0, 32)

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
			catalog.EntityKindButton, catalog.EntityKindSelect,
			catalog.EntityKindNumber, catalog.EntityKindNone:
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

	currentOp, err := b.buildDerivedSensor(SensorCurrentOperation, "Current operation",
		catalog.EntityCategoryDiagnostic, "mdi:cog-sync", firmware)
	if err != nil {
		return nil, err
	}
	out = append(out, currentOp)

	for _, def := range catalog.Settings {
		d, err := b.buildSetting(def, firmware)
		if err != nil {
			return nil, err
		}
		out = append(out, d)
	}

	return out, nil
}

// buildSetting renders one panel setting as an entity_category=config switch,
// select or number.
func (b *Builder) buildSetting(def catalog.SettingDef, firmware string) (Discovery, error) {
	hint := catalog.EntityHint{Category: catalog.EntityCategoryConfig, Icon: def.Icon}
	stateTopic := b.topics.State(def.ObjectID)
	cmdTopic := b.topics.Command(def.ObjectID)
	switch def.Kind {
	case catalog.EntityKindSwitch:
		return b.marshal("switch", def.ObjectID, SwitchPayload{
			baseEntity:   b.baseFor("switch", def.ObjectID, def.Name, hint, firmware),
			StateTopic:   stateTopic,
			CommandTopic: cmdTopic,
			PayloadOn:    "ON",
			PayloadOff:   "OFF",
		})
	case catalog.EntityKindSelect:
		options := make([]string, 0, len(def.Options))
		for _, o := range def.Options {
			options = append(options, o.Label)
		}
		return b.marshal("select", def.ObjectID, SelectPayload{
			baseEntity:   b.baseFor("select", def.ObjectID, def.Name, hint, firmware),
			StateTopic:   stateTopic,
			CommandTopic: cmdTopic,
			Options:      options,
		})
	case catalog.EntityKindNumber:
		return b.marshal("number", def.ObjectID, NumberPayload{
			baseEntity:        b.baseFor("number", def.ObjectID, def.Name, hint, firmware),
			StateTopic:        stateTopic,
			CommandTopic:      cmdTopic,
			Min:               float64(def.Min),
			Max:               float64(def.Max),
			Step:              float64(def.Step),
			Mode:              string(def.Mode),
			UnitOfMeasurement: def.Unit,
			DeviceClass:       string(def.DeviceClass),
		})
	default:
		return Discovery{}, fmt.Errorf("discovery: setting %s has unsupported kind %s", def.ObjectID, def.Kind)
	}
}

func (b *Builder) buildDerivedSensor(
	objectID, name string,
	category catalog.EntityCategory, icon, firmware string,
) (Discovery, error) {
	hint := catalog.EntityHint{Category: category, Icon: icon}
	payload := SensorPayload{
		baseEntity: b.baseFor("sensor", objectID, name, hint, firmware),
		StateTopic: b.topics.State(objectID),
	}
	return b.marshal("sensor", objectID, payload)
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

// baseFor fills the common payload fields. default_entity_id is
// "<component>.<prefix>_<objectID>" so HA does not prefix new entity ids with
// the device's area.
func (b *Builder) baseFor(component, objectID, name string, hint catalog.EntityHint, firmware string) baseEntity {
	return baseEntity{
		Name:                name,
		UniqueID:            b.haCfg.DevicePrefix + "_" + objectID,
		DefaultEntityID:     component + "." + b.haCfg.DevicePrefix + "_" + objectID,
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
		baseEntity:                b.baseFor("sensor", reg.Hint.ObjectID, reg.Hint.Name, reg.Hint, firmware),
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
		baseEntity:   b.baseFor("switch", reg.Hint.ObjectID, reg.Hint.Name, reg.Hint, firmware),
		StateTopic:   b.topics.State(reg.Hint.ObjectID),
		CommandTopic: b.topics.Command(reg.Hint.ObjectID),
		PayloadOn:    "ON",
		PayloadOff:   "OFF",
	}
	return b.marshal("switch", reg.Hint.ObjectID, payload)
}

func (b *Builder) buildClimate(firmware string) (Discovery, error) {
	hint := catalog.EntityHint{Icon: "mdi:hvac"}
	base := b.baseFor("climate", ClimateObjectID, "", hint, firmware)
	// The climate is the device's primary entity: climate.<prefix>, without a
	// "_climate" stutter.
	base.DefaultEntityID = "climate." + b.haCfg.DevicePrefix

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
		baseEntity:  b.baseFor("binary_sensor", objectID, name, hint, firmware),
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
