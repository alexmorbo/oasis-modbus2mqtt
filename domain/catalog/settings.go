package catalog

import (
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"
)

// Holding-register addresses of the panel-menu settings (0-based PDU).
const (
	// AddrUserKeys0 is User_Keys_0 (h22), the panel menu keys bitfield.
	AddrUserKeys0 valueobject.RegisterAddr = 22
	// AddrDarkBrightness is Dark_Brightness (h23), percent.
	AddrDarkBrightness valueobject.RegisterAddr = 23
	// AddrIRRemoteTime is IR_Remoute_Time (h24), polled as a span filler only.
	AddrIRRemoteTime valueobject.RegisterAddr = 24
	// AddrUserDev0 is User_Dev_0 (h25), the user device options bitfield.
	AddrUserDev0 valueobject.RegisterAddr = 25
	// AddrHumidityTarget is e_Hum_Target (h35), percent.
	AddrHumidityTarget valueobject.RegisterAddr = 35
	// AddrCO2Target is e_Room_CO2_Target (h36), ppm.
	AddrCO2Target valueobject.RegisterAddr = 36
)

// wholeRegister is the mask of a setting that owns the full register.
const wholeRegister uint16 = 0xFFFF

// ErrInvalidSettingValue is returned by SettingDef.ParseCommand for a payload
// that does not map to a valid field value.
var ErrInvalidSettingValue = errors.New("invalid setting value")

// NumberMode is the HA number entity display mode.
type NumberMode string

const (
	// NumberModeBox renders an input box.
	NumberModeBox NumberMode = "box"
	// NumberModeSlider renders a slider.
	NumberModeSlider NumberMode = "slider"
)

// SelectOption maps a raw field value (after mask/shift) to an HA option label.
type SelectOption struct {
	Raw   uint16
	Label string
}

// SettingDef is one panel-menu setting exposed as an HA config entity.
// Every setting is entity_category=config (implicit, not a field).
type SettingDef struct {
	ObjectID    string
	Name        string
	Kind        EntityKind
	Addr        valueobject.RegisterAddr
	Mask        uint16
	Shift       uint8
	Icon        string
	DeviceClass DeviceClass
	Unit        string
	Min         uint16
	Max         uint16
	Step        uint16
	Mode        NumberMode
	Options     []SelectOption
}

// ControlPriorityOptions maps h22 bits 13..14. Vendor PDF: 0=temperature, 1=humidity, 2=CO2.
// The panel labels the third option "pressure"; unverified. Raw 3 is never written and renders unknown.
var ControlPriorityOptions = []SelectOption{{0, "temperature"}, {1, "humidity"}, {2, "co2"}}

// Settings is the ordered table of panel-menu settings. Discovery, state
// publishing, command subscription and validation all iterate it.
var Settings = []SettingDef{
	settingSwitch("nord_start", "North start", AddrUserKeys0, 1, "mdi:snowflake-thermometer"),
	settingSwitch("touch_click", "Touch click sound", AddrUserKeys0, 5, "mdi:gesture-tap"),
	settingSwitch("alarm_beep", "Alarm beep", AddrUserKeys0, 6, "mdi:volume-high"),
	settingSwitch("stop_on_filter_full", "Stop on filter 100%", AddrUserKeys0, 7, "mdi:air-filter"),
	settingSwitch("auto_restart", "Auto restart after power loss", AddrUserKeys0, 8, "mdi:restart"),
	settingSwitch("performance_control", "Performance control", AddrUserKeys0, 9, "mdi:fan-chevron-down"),
	settingSwitch("dark_screen", "Dark screen", AddrUserKeys0, 10, "mdi:brightness-4"),
	settingSwitch("humidifier", "Humidifier", AddrUserKeys0, 12, "mdi:air-humidifier"),
	settingSwitch("stop_on_conditioner_alarm", "Stop on conditioner alarm", AddrUserKeys0, 15, "mdi:air-conditioner"),
	{
		ObjectID: "control_priority", Name: "Control priority", Kind: EntityKindSelect,
		Addr: AddrUserKeys0, Mask: 0x6000, Shift: 13,
		Icon: "mdi:format-list-numbered", Options: ControlPriorityOptions,
	},
	settingSwitch("active_filter", "Active filter", AddrUserDev0, 2, "mdi:air-purifier"),
	{
		ObjectID: "dark_brightness", Name: "Dark screen brightness", Kind: EntityKindNumber,
		Addr: AddrDarkBrightness, Mask: wholeRegister,
		Icon: "mdi:brightness-6", Unit: "%",
		Min: 20, Max: 100, Step: 1, Mode: NumberModeSlider,
	},
	{
		ObjectID: "humidity_target", Name: "Humidity target", Kind: EntityKindNumber,
		Addr: AddrHumidityTarget, Mask: wholeRegister,
		DeviceClass: DeviceClassHumidity, Unit: "%",
		Min: 20, Max: 95, Step: 1, Mode: NumberModeSlider,
	},
	{
		ObjectID: "co2_target", Name: "CO2 target", Kind: EntityKindNumber,
		Addr: AddrCO2Target, Mask: wholeRegister,
		DeviceClass: DeviceClassCarbonDioxide, Unit: "ppm",
		Min: 500, Max: 2000, Step: 10, Mode: NumberModeBox,
	},
}

func settingSwitch(objectID, name string, addr valueobject.RegisterAddr, bit uint8, icon string) SettingDef {
	return SettingDef{
		ObjectID: objectID, Name: name, Kind: EntityKindSwitch,
		Addr: addr, Mask: 1 << bit, Shift: bit, Icon: icon,
	}
}

// SettingByObjectID looks up a setting by its HA object id.
func SettingByObjectID(id string) (SettingDef, bool) {
	for _, d := range Settings {
		if d.ObjectID == id {
			return d, true
		}
	}
	return SettingDef{}, false
}

// IsBitfield reports whether the setting owns only part of its register.
func (d SettingDef) IsBitfield() bool {
	return d.Mask != wholeRegister
}

// Field extracts the setting's field value from a raw register value.
func (d SettingDef) Field(reg uint16) uint16 {
	return (reg & d.Mask) >> d.Shift
}

// Apply returns reg with the setting's bits replaced by field, preserving all other bits.
func (d SettingDef) Apply(reg, field uint16) uint16 {
	return (reg &^ d.Mask) | ((field << d.Shift) & d.Mask)
}

// Matches reports whether reg carries field in the setting's bits.
func (d SettingDef) Matches(reg, field uint16) bool {
	return d.Field(reg) == field
}

// StatePayload renders the HA state payload for a raw register value. known is
// false (payload "None", which HA renders as unknown) for a select value without
// a label or a number outside [Min, Max].
func (d SettingDef) StatePayload(reg uint16) (payload string, known bool) {
	v := d.Field(reg)
	switch d.Kind {
	case EntityKindSwitch:
		if v != 0 {
			return "ON", true
		}
		return "OFF", true
	case EntityKindSelect:
		for _, o := range d.Options {
			if o.Raw == v {
				return o.Label, true
			}
		}
		return "None", false
	case EntityKindNumber:
		if v < d.Min || v > d.Max {
			return "None", false
		}
		return strconv.FormatUint(uint64(v), 10), true
	default:
		return "None", false
	}
}

// ParseCommand validates an HA command payload and returns the field value to
// write. Every rejection wraps ErrInvalidSettingValue.
func (d SettingDef) ParseCommand(payload string) (uint16, error) {
	s := strings.TrimSpace(payload)
	switch d.Kind {
	case EntityKindSwitch:
		switch {
		case strings.EqualFold(s, "ON"):
			return 1, nil
		case strings.EqualFold(s, "OFF"):
			return 0, nil
		}
		return 0, fmt.Errorf("%s: %q is not ON or OFF: %w", d.ObjectID, s, ErrInvalidSettingValue)
	case EntityKindSelect:
		for _, o := range d.Options {
			if o.Label == s {
				return o.Raw, nil
			}
		}
		return 0, fmt.Errorf("%s: %q is not a known option: %w", d.ObjectID, s, ErrInvalidSettingValue)
	case EntityKindNumber:
		return d.parseNumber(s)
	default:
		return 0, fmt.Errorf("%s: kind %s is not writable: %w", d.ObjectID, d.Kind, ErrInvalidSettingValue)
	}
}

func (d SettingDef) parseNumber(s string) (uint16, error) {
	f, err := strconv.ParseFloat(s, 64)
	if err != nil {
		return 0, fmt.Errorf("%s: %q is not a number: %w", d.ObjectID, s, ErrInvalidSettingValue)
	}
	if math.IsNaN(f) || math.IsInf(f, 0) || f != math.Trunc(f) {
		return 0, fmt.Errorf("%s: %q is not an integer: %w", d.ObjectID, s, ErrInvalidSettingValue)
	}
	if f < float64(d.Min) || f > float64(d.Max) {
		return 0, fmt.Errorf("%s: %q outside [%d, %d]: %w", d.ObjectID, s, d.Min, d.Max, ErrInvalidSettingValue)
	}
	v := uint16(f) //nolint:gosec // G115: bounded by the [Min, Max] check above
	if (v-d.Min)%d.Step != 0 {
		return 0, fmt.Errorf("%s: %q is off the step-%d grid from %d: %w", d.ObjectID, s, d.Step, d.Min, ErrInvalidSettingValue)
	}
	return v, nil
}
