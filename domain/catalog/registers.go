package catalog

import "github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"

// RegisterDef is one row of the static register catalog. RegisterDef has no
// function fields by design — decoding logic lives in the poller usecase, not
// in this declarative table.
type RegisterDef struct {
	Addr        valueobject.RegisterAddr
	Kind        valueobject.RegisterKind
	Name        string
	Group       PollGroup
	Hint        EntityHint
	Description string
}

// Registers is the canonical, ordered catalog of every register the bridge
// reads or writes. Adding a register is one entry in this table; discovery,
// polling and metrics all iterate this slice.
var Registers = []RegisterDef{
	// --- Input registers, slow tier ---
	{
		Addr: 0, Kind: valueobject.RegisterKindInput, Name: "Firmware",
		Group: PollGroupSlow,
		Hint: EntityHint{
			Kind: EntityKindSensor, ObjectID: "firmware", Name: "Firmware",
			Category: EntityCategoryDiagnostic, Icon: "mdi:chip",
		},
		Description: "controller firmware version (BCD)",
	},
	{
		Addr: 79, Kind: valueobject.RegisterKindInput, Name: "DeviceID",
		Group: PollGroupSlow,
		Hint: EntityHint{
			Kind: EntityKindSensor, ObjectID: "device_id", Name: "Device ID",
			Category: EntityCategoryDiagnostic,
		},
		Description: "unique device identifier",
	},

	// --- Input registers, hot tier ---
	{
		Addr: 2, Kind: valueobject.RegisterKindInput, Name: "State_0",
		Group:       PollGroupHot,
		Description: "primary controller state bitfield (power, switching, capabilities)",
	},
	{
		Addr: 3, Kind: valueobject.RegisterKindInput, Name: "State_1",
		Group:       PollGroupHot,
		Description: "current operation code (bits 0..4)",
	},
	{
		Addr: 4, Kind: valueobject.RegisterKindInput, Name: "Error_Code",
		Group:       PollGroupHot,
		Description: "primary error mask (sensor faults, freeze threats, fan/fire/overheat)",
	},
	{
		Addr: 5, Kind: valueobject.RegisterKindInput, Name: "Error_Code_1",
		Group:       PollGroupHot,
		Description: "extended error mask 1",
	},
	{
		Addr: 6, Kind: valueobject.RegisterKindInput, Name: "Last_Time",
		Group: PollGroupHot,
		Hint: EntityHint{
			Kind: EntityKindSensor, ObjectID: "operation_time_left", Name: "Operation time left",
			Unit: "s", Category: EntityCategoryDiagnostic,
		},
		Description: "minutes/seconds remaining for current operation (split byte)",
	},
	{
		Addr: 9, Kind: valueobject.RegisterKindInput, Name: "TkanK",
		Group: PollGroupHot,
		Hint: EntityHint{
			Kind: EntityKindSensor, ObjectID: "supply_temperature", Name: "Supply temperature",
			DeviceClass: DeviceClassTemperature, Unit: "°C",
			StateClass: StateClassMeasurement, Precision: 1,
		},
		Description: "T1 corrected supply-air temperature, scale 1/10 degC",
	},
	{
		Addr: 14, Kind: valueobject.RegisterKindInput, Name: "ZagrFiltr1",
		Group: PollGroupHot,
		Hint: EntityHint{
			Kind: EntityKindSensor, ObjectID: "filter_clog", Name: "Filter clog",
			Unit: "%", StateClass: StateClassMeasurement, Icon: "mdi:air-filter",
		},
		Description: "filter1 clog percentage",
	},
	{
		Addr: 16, Kind: valueobject.RegisterKindInput, Name: "DOutputs",
		Group:       PollGroupHot,
		Description: "discrete outputs bitfield (PWM, Y1 damper, fan speeds, valves)",
	},
	{
		Addr: 18, Kind: valueobject.RegisterKindInput, Name: "Reg",
		Group: PollGroupHot,
		Hint: EntityHint{
			Kind: EntityKindSensor, ObjectID: "heat_demand", Name: "Heat demand",
			Unit: "%", StateClass: StateClassMeasurement,
			Category: EntityCategoryDiagnostic,
		},
		Description: "PID heat-demand percentage",
	},
	{
		Addr: 25, Kind: valueobject.RegisterKindInput, Name: "Fan_State_1",
		Group: PollGroupHot,
		Hint: EntityHint{
			Kind: EntityKindSensor, ObjectID: "supply_fan_speed", Name: "Supply fan speed",
			StateClass: StateClassMeasurement,
		},
		Description: "actual supply-fan speed",
	},
	{
		Addr: 30, Kind: valueobject.RegisterKindInput, Name: "Fan_State_2",
		Group: PollGroupHot,
		Hint: EntityHint{
			Kind: EntityKindSensor, ObjectID: "exhaust_fan_speed", Name: "Exhaust fan speed",
			StateClass: StateClassMeasurement,
		},
		Description: "actual exhaust-fan speed",
	},

	// --- Input registers, medium tier ---
	{
		Addr: 57, Kind: valueobject.RegisterKindInput, Name: "TKomn_x10_P",
		Group: PollGroupMedium,
		Hint: EntityHint{
			Kind: EntityKindSensor, ObjectID: "room_temperature", Name: "Room temperature",
			DeviceClass: DeviceClassTemperature, Unit: "°C",
			StateClass: StateClassMeasurement, Precision: 1,
		},
		Description: "remote-panel room temperature, scale 1/10 degC",
	},
	{
		Addr: 58, Kind: valueobject.RegisterKindInput, Name: "Room_Hum_P",
		Group: PollGroupMedium,
		Hint: EntityHint{
			Kind: EntityKindSensor, ObjectID: "room_humidity", Name: "Room humidity",
			DeviceClass: DeviceClassHumidity, Unit: "%", StateClass: StateClassMeasurement,
		},
		Description: "remote-panel relative humidity",
	},
	{
		Addr: 69, Kind: valueobject.RegisterKindInput, Name: "Error_Code_2",
		Group:       PollGroupMedium,
		Description: "extended error mask 2 (T4/T5/filter2/fan2)",
	},
	{
		Addr: 70, Kind: valueobject.RegisterKindInput, Name: "Error_Code_3",
		Group:       PollGroupMedium,
		Description: "extended error mask 3 (bit 6 reserved)",
	},

	// --- Holding registers, slow tier ---
	{
		Addr: 0, Kind: valueobject.RegisterKindHolding, Name: "Type_Dev",
		Group:       PollGroupSlow,
		Description: "device configuration: heater (0..3), cooler (4..7), recuperator (8..11)",
	},

	// --- Holding registers, hot tier ---
	{
		Addr: 31, Kind: valueobject.RegisterKindHolding, Name: "Temp_Target",
		Group:       PollGroupHot,
		Description: "climate target temperature, scale 1/10 degC, range 50..300",
	},
	{
		Addr: 32, Kind: valueobject.RegisterKindHolding, Name: "Fan_Target_1",
		Group:       PollGroupHot,
		Description: "supply-fan target speed (1..10)",
	},

	// --- Holding registers, medium tier ---
	{
		Addr: 86, Kind: valueobject.RegisterKindHolding, Name: "Dev_Keys_2",
		Group:       PollGroupMedium,
		Description: "device keys: bits 0..1 = HVAC mode (off/heat/cool/auto)",
	},

	// --- Holding registers, command-only (write, no poll) ---
	{
		Addr: 2, Kind: valueobject.RegisterKindHolding, Name: "Power_Dev",
		Group: PollGroupNone,
		Hint: EntityHint{
			Kind: EntityKindSwitch, ObjectID: "power", Name: "Power", Icon: "mdi:power",
		},
		Description: "edge-triggered power-on/off command (always reads as 0)",
	},
}

// RegistersByGroup returns every register that belongs to the supplied poll group.
func RegistersByGroup(g PollGroup) []RegisterDef {
	out := make([]RegisterDef, 0, len(Registers))
	for _, r := range Registers {
		if r.Group == g {
			out = append(out, r)
		}
	}
	return out
}

// RegisterByAddr looks up a register by (kind, address). Returns the second
// value as false when no match exists.
func RegisterByAddr(kind valueobject.RegisterKind, addr uint16) (RegisterDef, bool) {
	for _, r := range Registers {
		if r.Kind == kind && uint16(r.Addr) == addr {
			return r, true
		}
	}
	return RegisterDef{}, false
}

// RegistersWithEntity returns every catalog entry whose Hint declares a
// non-None EntityKind — i.e. those that surface as their own HA entity.
func RegistersWithEntity() []RegisterDef {
	out := make([]RegisterDef, 0, len(Registers))
	for _, r := range Registers {
		if r.Hint.Kind != EntityKindNone {
			out = append(out, r)
		}
	}
	return out
}
