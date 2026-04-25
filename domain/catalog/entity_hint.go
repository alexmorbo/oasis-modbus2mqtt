package catalog

// EntityKind classifies a register's role in Home Assistant. The zero value
// is EntityKindNone — registers that we poll but do not publish as their own
// HA entity (e.g. State_0, Type_Dev, Error_Code_*).
type EntityKind int

const (
	// EntityKindNone means the register is polled but does not surface as a standalone HA entity.
	EntityKindNone EntityKind = iota
	// EntityKindSensor is a numeric/string sensor entity.
	EntityKindSensor
	// EntityKindBinarySensor is an on/off sensor entity.
	EntityKindBinarySensor
	// EntityKindSwitch is a writable on/off switch entity.
	EntityKindSwitch
	// EntityKindClimate is a thermostat-style climate entity.
	EntityKindClimate
	// EntityKindButton is a stateless button entity.
	EntityKindButton
)

// String returns the canonical lowercase label for the EntityKind.
func (k EntityKind) String() string {
	switch k {
	case EntityKindNone:
		return "none"
	case EntityKindSensor:
		return "sensor"
	case EntityKindBinarySensor:
		return "binary_sensor"
	case EntityKindSwitch:
		return "switch"
	case EntityKindClimate:
		return "climate"
	case EntityKindButton:
		return "button"
	default:
		return "unknown"
	}
}

// EntityCategory mirrors the Home Assistant entity_category attribute.
type EntityCategory int

const (
	// EntityCategoryDefault renders to the empty string (HA default category).
	EntityCategoryDefault EntityCategory = iota
	// EntityCategoryDiagnostic marks the entity as diagnostic.
	EntityCategoryDiagnostic
	// EntityCategoryConfig marks the entity as configuration.
	EntityCategoryConfig
)

// String returns the HA-compatible category label ("", "diagnostic", "config").
func (c EntityCategory) String() string {
	switch c {
	case EntityCategoryDefault:
		return ""
	case EntityCategoryDiagnostic:
		return "diagnostic"
	case EntityCategoryConfig:
		return "config"
	default:
		return "unknown"
	}
}

// DeviceClass is a typed string of the HA sensor/binary_sensor device_class attribute.
type DeviceClass string

const (
	// DeviceClassNone is the absence of a device class.
	DeviceClassNone DeviceClass = ""
	// DeviceClassTemperature is the HA "temperature" device_class.
	DeviceClassTemperature DeviceClass = "temperature"
	// DeviceClassHumidity is the HA "humidity" device_class.
	DeviceClassHumidity DeviceClass = "humidity"
	// DeviceClassHeat is the HA "heat" binary_sensor device_class.
	DeviceClassHeat DeviceClass = "heat"
	// DeviceClassProblem is the HA "problem" binary_sensor device_class.
	DeviceClassProblem DeviceClass = "problem"
	// DeviceClassRunning is the HA "running" binary_sensor device_class.
	DeviceClassRunning DeviceClass = "running"
	// DeviceClassPower is the HA "power" device_class.
	DeviceClassPower DeviceClass = "power"
)

// StateClass is a typed string of the HA sensor state_class attribute.
type StateClass string

const (
	// StateClassNone is the absence of a state class.
	StateClassNone StateClass = ""
	// StateClassMeasurement is the HA "measurement" state_class for instantaneous readings.
	StateClassMeasurement StateClass = "measurement"
)

// EntityHint describes how a polled register surfaces as an HA entity. All
// fields default to a usable zero value: an empty hint with Kind=EntityKindNone
// means "poll only, no entity".
type EntityHint struct {
	Kind        EntityKind
	ObjectID    string
	Name        string
	DeviceClass DeviceClass
	Unit        string
	StateClass  StateClass
	Category    EntityCategory
	Icon        string
	Precision   int
}
