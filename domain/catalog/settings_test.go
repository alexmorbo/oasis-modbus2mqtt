package catalog_test

import (
	"math/bits"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/catalog"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"
)

// derivedDiscoveryIDs are the object ids discovery builds outside the catalog.
var derivedDiscoveryIDs = []string{
	"heater_active", "damper_open", "problem", "current_operation", "climate",
	"hvac_mode", "target_temperature", "fan_target", "hvac_action",
}

func mustSetting(t *testing.T, id string) catalog.SettingDef {
	t.Helper()
	d, ok := catalog.SettingByObjectID(id)
	require.Truef(t, ok, "setting %q not found", id)
	return d
}

func TestSettings_ObjectIDsUnique(t *testing.T) {
	t.Parallel()

	seen := map[string]string{}
	add := func(id, src string) {
		require.NotEmpty(t, id)
		if prev, dup := seen[id]; dup {
			t.Fatalf("object id %q used by %s and %s", id, prev, src)
		}
		seen[id] = src
	}
	for _, r := range catalog.RegistersWithEntity() {
		add(r.Hint.ObjectID, "register "+r.Name)
	}
	for _, id := range derivedDiscoveryIDs {
		add(id, "derived discovery")
	}
	for _, d := range catalog.Settings {
		add(d.ObjectID, "setting")
	}
	assert.Len(t, catalog.Settings, 14)
}

func TestSettings_RegistersAreCatalogedSlowHolding(t *testing.T) {
	t.Parallel()
	for _, d := range catalog.Settings {
		r, ok := catalog.RegisterByAddr(valueobject.RegisterKindHolding, d.Addr.Uint16())
		require.Truef(t, ok, "%s: h%d not in Registers", d.ObjectID, d.Addr.Uint16())
		assert.Equalf(t, catalog.PollGroupSlow, r.Group, "%s: h%d must be slow tier", d.ObjectID, d.Addr.Uint16())
		assert.Equalf(t, catalog.EntityKindNone, r.Hint.Kind, "%s: h%d must carry no hint", d.ObjectID, d.Addr.Uint16())
	}
}

func TestSettings_MasksDoNotOverlapPerRegister(t *testing.T) {
	t.Parallel()
	used := map[valueobject.RegisterAddr]uint16{}
	for _, d := range catalog.Settings {
		require.NotZerof(t, d.Mask, "%s: empty mask", d.ObjectID)
		assert.Zerof(t, used[d.Addr]&d.Mask, "%s overlaps another setting on h%d", d.ObjectID, d.Addr.Uint16())
		used[d.Addr] |= d.Mask
	}
}

func TestSettings_UserKeys0CombinedMask(t *testing.T) {
	t.Parallel()
	var combined uint16
	for _, d := range catalog.Settings {
		if d.Addr == catalog.AddrUserKeys0 {
			combined |= d.Mask
		}
	}
	assert.Equal(t, uint16(0xF7E2), combined)
	for _, bit := range []uint{0, 2, 3, 4, 11} {
		assert.Zerof(t, combined&(1<<bit), "h22 bit %d must never be exposed", bit)
	}
}

func TestSettings_SwitchMasksSingleBit(t *testing.T) {
	t.Parallel()
	for _, d := range catalog.Settings {
		if d.Kind != catalog.EntityKindSwitch {
			continue
		}
		assert.Equalf(t, 1, bits.OnesCount16(d.Mask), "%s: switch mask must be one bit", d.ObjectID)
		assert.Equalf(t, uint16(1)<<d.Shift, d.Mask, "%s: shift must equal the bit index", d.ObjectID)
		assert.True(t, d.IsBitfield())
	}
}

func TestSettings_NumberInvariants(t *testing.T) {
	t.Parallel()
	n := 0
	for _, d := range catalog.Settings {
		if d.Kind != catalog.EntityKindNumber {
			continue
		}
		n++
		assert.Equalf(t, uint16(0xFFFF), d.Mask, "%s", d.ObjectID)
		assert.Zerof(t, d.Shift, "%s", d.ObjectID)
		assert.Falsef(t, d.IsBitfield(), "%s", d.ObjectID)
		require.Positivef(t, d.Step, "%s: step", d.ObjectID)
		assert.LessOrEqualf(t, d.Min, d.Max, "%s", d.ObjectID)
		assert.Zerof(t, (d.Max-d.Min)%d.Step, "%s: max must sit on the step grid", d.ObjectID)
		assert.NotEmptyf(t, d.Mode, "%s: mode", d.ObjectID)
		assert.NotEmptyf(t, d.Unit, "%s: unit", d.ObjectID)
	}
	assert.Equal(t, 3, n)
}

func TestSettings_SelectOptionsFitMaskAndNeverRaw3(t *testing.T) {
	t.Parallel()
	n := 0
	for _, d := range catalog.Settings {
		if d.Kind != catalog.EntityKindSelect {
			continue
		}
		n++
		require.NotEmpty(t, d.Options)
		labels := map[string]bool{}
		for _, o := range d.Options {
			assert.NotEqualf(t, uint16(3), o.Raw, "%s: raw 3 must never be written", d.ObjectID)
			assert.Equalf(t, o.Raw, d.Field(d.Apply(0, o.Raw)), "%s: option %q does not fit the mask", d.ObjectID, o.Label)
			assert.Falsef(t, labels[o.Label], "%s: duplicate label %q", d.ObjectID, o.Label)
			labels[o.Label] = true
		}
	}
	assert.Equal(t, 1, n)
	assert.Equal(t, []catalog.SelectOption{{Raw: 0, Label: "temperature"}, {Raw: 1, Label: "humidity"}, {Raw: 2, Label: "co2"}},
		catalog.ControlPriorityOptions)
}

func TestSettingByObjectID_Unknown(t *testing.T) {
	t.Parallel()
	_, ok := catalog.SettingByObjectID("power")
	assert.False(t, ok)
}

func TestSettingDef_ApplyPreservesOtherBits(t *testing.T) {
	t.Parallel()
	tests := []struct {
		id    string
		reg   uint16
		field uint16
		want  uint16
	}{
		{id: "alarm_beep", reg: 0x07E4, field: 0, want: 0x07A4},
		{id: "control_priority", reg: 0x07E4, field: 2, want: 0x47E4},
		{id: "nord_start", reg: 0x07E4, field: 1, want: 0x07E6},
		{id: "active_filter", reg: 0x00A5, field: 0, want: 0x00A1},
		{id: "humidity_target", reg: 50, field: 55, want: 55},
	}
	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()
			d := mustSetting(t, tc.id)
			got := d.Apply(tc.reg, tc.field)
			assert.Equal(t, tc.want, got, "0x%04X", got)
			assert.True(t, d.Matches(got, tc.field))
			assert.Equal(t, tc.reg&^d.Mask, got&^d.Mask, "bits outside the mask must be preserved")
		})
	}
}

func TestSettingDef_StatePayload(t *testing.T) {
	t.Parallel()

	live := map[string]string{
		"nord_start":                "OFF",
		"touch_click":               "ON",
		"alarm_beep":                "ON",
		"stop_on_filter_full":       "ON",
		"auto_restart":              "ON",
		"performance_control":       "ON",
		"dark_screen":               "ON",
		"humidifier":                "OFF",
		"stop_on_conditioner_alarm": "OFF",
		"control_priority":          "temperature",
	}
	for id, want := range live {
		got, known := mustSetting(t, id).StatePayload(0x07E4)
		assert.Truef(t, known, "%s", id)
		assert.Equalf(t, want, got, "%s on live 0x07E4", id)
	}

	tests := []struct {
		name      string
		id        string
		reg       uint16
		want      string
		wantKnown bool
	}{
		{name: "active filter on", id: "active_filter", reg: 0x0004, want: "ON", wantKnown: true},
		{name: "priority humidity", id: "control_priority", reg: 0x2000, want: "humidity", wantKnown: true},
		{name: "priority co2", id: "control_priority", reg: 0x47E4, want: "co2", wantKnown: true},
		{name: "priority raw 3", id: "control_priority", reg: 0x67E4, want: "None"},
		{name: "number in range", id: "humidity_target", reg: 50, want: "50", wantKnown: true},
		{name: "number at max", id: "co2_target", reg: 2000, want: "2000", wantKnown: true},
		{name: "number below min", id: "humidity_target", reg: 19, want: "None"},
		{name: "number above max", id: "humidity_target", reg: 96, want: "None"},
		{name: "brightness live", id: "dark_brightness", reg: 20, want: "20", wantKnown: true},
		{name: "unwritable kind", id: "", reg: 1, want: "None"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := catalog.SettingDef{Kind: catalog.EntityKindSensor, Mask: 0xFFFF}
			if tc.id != "" {
				d = mustSetting(t, tc.id)
			}
			got, known := d.StatePayload(tc.reg)
			assert.Equal(t, tc.want, got)
			assert.Equal(t, tc.wantKnown, known)
		})
	}
}

func TestSettingDef_ParseCommand(t *testing.T) {
	t.Parallel()

	accept := []struct {
		id      string
		payload string
		want    uint16
	}{
		{"alarm_beep", "ON", 1},
		{"alarm_beep", "off", 0},
		{"alarm_beep", " ON ", 1},
		{"control_priority", "co2", 2},
		{"control_priority", "temperature", 0},
		{"humidity_target", "50", 50},
		{"humidity_target", "50.0", 50},
		{"co2_target", "800", 800},
		{"co2_target", "2000", 2000},
		{"co2_target", "500", 500},
		{"dark_brightness", "100", 100},
	}
	for _, tc := range accept {
		t.Run("accept "+tc.id+"="+tc.payload, func(t *testing.T) {
			t.Parallel()
			got, err := mustSetting(t, tc.id).ParseCommand(tc.payload)
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}

	reject := []struct {
		id      string
		payload string
	}{
		{"alarm_beep", "maybe"},
		{"alarm_beep", ""},
		{"alarm_beep", "1"},
		{"control_priority", "pressure"},
		{"control_priority", "2"},
		{"control_priority", "3"},
		{"control_priority", "None"},
		{"control_priority", "CO2"},
		{"humidity_target", "abc"},
		{"humidity_target", "NaN"},
		{"humidity_target", "Inf"},
		{"humidity_target", "-Inf"},
		{"humidity_target", ""},
		{"humidity_target", "19"},
		{"humidity_target", "96"},
		{"humidity_target", "50.5"},
		{"humidity_target", "-1"},
		{"humidity_target", "1e9"},
		{"co2_target", "505"},
		{"co2_target", "490"},
		{"co2_target", "2010"},
	}
	for _, tc := range reject {
		t.Run("reject "+tc.id+"="+tc.payload, func(t *testing.T) {
			t.Parallel()
			_, err := mustSetting(t, tc.id).ParseCommand(tc.payload)
			require.Error(t, err)
			assert.ErrorIs(t, err, catalog.ErrInvalidSettingValue)
		})
	}

	t.Run("reject unwritable kind", func(t *testing.T) {
		t.Parallel()
		_, err := catalog.SettingDef{ObjectID: "x", Kind: catalog.EntityKindSensor}.ParseCommand("1")
		assert.ErrorIs(t, err, catalog.ErrInvalidSettingValue)
	})
}
