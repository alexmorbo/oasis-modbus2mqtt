package catalog_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/catalog"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"
)

func TestRegisters_ObjectIDsUnique(t *testing.T) {
	t.Parallel()

	seen := map[string]string{}
	for _, r := range catalog.Registers {
		if r.Hint.Kind == catalog.EntityKindNone {
			continue
		}
		require.NotEmpty(t, r.Hint.ObjectID, "register %s has Kind != None but empty ObjectID", r.Name)
		if prev, dup := seen[r.Hint.ObjectID]; dup {
			t.Fatalf("duplicate ObjectID %q on registers %s and %s", r.Hint.ObjectID, prev, r.Name)
		}
		seen[r.Hint.ObjectID] = r.Name
	}
}

func TestRegisters_AddressesUniquePerKind(t *testing.T) {
	t.Parallel()

	seen := map[string]string{}
	for _, r := range catalog.Registers {
		key := r.Kind.String() + ":" + r.Addr.String()
		if prev, dup := seen[key]; dup {
			t.Fatalf("duplicate %s on registers %s and %s", key, prev, r.Name)
		}
		seen[key] = r.Name
	}
}

func TestRegisters_SensorEntitiesHaveNameAndObjectID(t *testing.T) {
	t.Parallel()

	for _, r := range catalog.Registers {
		if r.Hint.Kind != catalog.EntityKindSensor {
			continue
		}
		assert.NotEmptyf(t, r.Hint.ObjectID, "sensor register %s missing ObjectID", r.Name)
		assert.NotEmptyf(t, r.Hint.Name, "sensor register %s missing Name", r.Name)
	}
}

func TestRegisters_PollGroupNoneOnlyOnPowerDev(t *testing.T) {
	t.Parallel()

	var noneRegisters []string
	for _, r := range catalog.Registers {
		if r.Group == catalog.PollGroupNone {
			noneRegisters = append(noneRegisters, r.Name)
		}
	}
	assert.Equal(t, []string{"Power_Dev"}, noneRegisters)
}

func TestRegistersByGroup(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		group    catalog.PollGroup
		mustHave []string
	}{
		{name: "hot", group: catalog.PollGroupHot, mustHave: []string{"State_0", "TkanK", "Temp_Target", "Fan_Target_1"}},
		{name: "medium", group: catalog.PollGroupMedium, mustHave: []string{"TKomn_x10_P", "Dev_Keys_2"}},
		{name: "slow", group: catalog.PollGroupSlow, mustHave: []string{"Firmware", "DeviceID", "Type_Dev", "Filtr1_Resurs", "Filtr1_Ostatok",
			"User_Keys_0", "Dark_Brightness", "IR_Remoute_Time", "User_Dev_0", "e_Hum_Target", "e_Room_CO2_Target"}},
		{name: "none", group: catalog.PollGroupNone, mustHave: []string{"Power_Dev"}},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := catalog.RegistersByGroup(tc.group)
			names := make([]string, 0, len(got))
			for _, r := range got {
				names = append(names, r.Name)
				assert.Equal(t, tc.group, r.Group)
			}
			for _, name := range tc.mustHave {
				assert.Containsf(t, names, name, "group %s should contain %s", tc.group, name)
			}
		})
	}
}

func TestRegisterByAddr(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		kind     valueobject.RegisterKind
		addr     uint16
		wantName string
		wantOK   bool
	}{
		{name: "input firmware", kind: valueobject.RegisterKindInput, addr: 0, wantName: "Firmware", wantOK: true},
		{name: "input TkanK", kind: valueobject.RegisterKindInput, addr: 9, wantName: "TkanK", wantOK: true},
		{name: "holding type_dev", kind: valueobject.RegisterKindHolding, addr: 0, wantName: "Type_Dev", wantOK: true},
		{name: "holding power_dev", kind: valueobject.RegisterKindHolding, addr: 2, wantName: "Power_Dev", wantOK: true},
		{name: "holding filter resource", kind: valueobject.RegisterKindHolding, addr: 344, wantName: "Filtr1_Resurs", wantOK: true},
		{name: "holding filter days left", kind: valueobject.RegisterKindHolding, addr: 346, wantName: "Filtr1_Ostatok", wantOK: true},
		{name: "holding user keys h22", kind: valueobject.RegisterKindHolding, addr: 22, wantName: "User_Keys_0", wantOK: true},
		{name: "holding co2 target h36", kind: valueobject.RegisterKindHolding, addr: 36, wantName: "e_Room_CO2_Target", wantOK: true},
		{name: "holding h26 not cataloged", kind: valueobject.RegisterKindHolding, addr: 26, wantOK: false},
		{name: "holding h345 not cataloged", kind: valueobject.RegisterKindHolding, addr: 345, wantOK: false},
		{name: "missing input", kind: valueobject.RegisterKindInput, addr: 9999, wantOK: false},
		{name: "kind mismatch", kind: valueobject.RegisterKindHolding, addr: 9, wantOK: false},
		{name: "invalid kind", kind: valueobject.RegisterKind(0), addr: 0, wantOK: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, ok := catalog.RegisterByAddr(tc.kind, tc.addr)
			assert.Equal(t, tc.wantOK, ok)
			if tc.wantOK {
				assert.Equal(t, tc.wantName, got.Name)
				assert.Equal(t, tc.kind, got.Kind)
				assert.Equal(t, tc.addr, got.Addr.Uint16())
			}
		})
	}
}

func TestRegistersWithEntity(t *testing.T) {
	t.Parallel()

	got := catalog.RegistersWithEntity()
	require.NotEmpty(t, got)

	for _, r := range got {
		assert.NotEqualf(t, catalog.EntityKindNone, r.Hint.Kind,
			"RegistersWithEntity returned %s with Kind=None", r.Name)
	}

	names := make(map[string]bool, len(got))
	for _, r := range got {
		names[r.Name] = true
	}
	for _, expected := range []string{"TkanK", "ZagrFiltr1", "Reg", "Fan_State_1", "Fan_State_2",
		"TKomn_x10_P", "Room_Hum_P", "Firmware", "DeviceID", "Last_Time", "Power_Dev",
		"Filtr1_Resurs", "Filtr1_Ostatok"} {
		assert.Truef(t, names[expected], "expected register %s to be returned by RegistersWithEntity", expected)
	}

	// Registers explicitly marked Kind=None must NOT appear.
	for _, excluded := range []string{"State_0", "State_1", "Error_Code", "Error_Code_1",
		"Error_Code_2", "Error_Code_3", "DOutputs", "Type_Dev",
		"Temp_Target", "Fan_Target_1", "Dev_Keys_2",
		"User_Keys_0", "Dark_Brightness", "IR_Remoute_Time", "User_Dev_0", "e_Hum_Target", "e_Room_CO2_Target"} {
		assert.Falsef(t, names[excluded], "register %s must not be returned by RegistersWithEntity", excluded)
	}
}

func TestRegisters_FilterDaysEntries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		addr     uint16
		wantName string
		wantHint catalog.EntityHint
	}{
		{
			name:     "h346 filter days left",
			addr:     346,
			wantName: "Filtr1_Ostatok",
			wantHint: catalog.EntityHint{
				Kind: catalog.EntityKindSensor, ObjectID: "filter_days_left", Name: "Filter days left",
				DeviceClass: catalog.DeviceClassDuration, Unit: "d",
				StateClass: catalog.StateClassMeasurement, Icon: "mdi:air-filter",
			},
		},
		{
			name:     "h344 filter resource",
			addr:     344,
			wantName: "Filtr1_Resurs",
			wantHint: catalog.EntityHint{
				Kind: catalog.EntityKindSensor, ObjectID: "filter_resource", Name: "Filter resource",
				DeviceClass: catalog.DeviceClassDuration, Unit: "d",
				Category: catalog.EntityCategoryDiagnostic, Icon: "mdi:air-filter",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r, ok := catalog.RegisterByAddr(valueobject.RegisterKindHolding, tc.addr)
			require.True(t, ok)
			assert.Equal(t, tc.wantName, r.Name)
			assert.Equal(t, catalog.PollGroupSlow, r.Group)
			assert.Equal(t, tc.wantHint, r.Hint)
			assert.Contains(t, r.Description, "undocumented")
		})
	}
}
