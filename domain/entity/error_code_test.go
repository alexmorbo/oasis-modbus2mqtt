package entity_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
)

func TestDecodeErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name           string
		e0, e1, e2, e3 uint16
		wantCodes      []string
	}{
		{
			name:      "all zero is empty",
			wantCodes: []string{},
		},
		{
			name:      "filter1 100 percent clogged (Error_Code bit 4)",
			e0:        0x0010,
			wantCodes: []string{"E04"},
		},
		{
			name:      "fan1 fault and calorifier overheat (Error_Code bits 10 and 13)",
			e0:        0x2400,
			wantCodes: []string{"E10", "E13"},
		},
		{
			name:      "Error_Code_3 bit 6 reserved is filtered",
			e3:        0x0040,
			wantCodes: []string{},
		},
		{
			name: "Error_Code_1 all 16 bits",
			e1:   0xFFFF,
			wantCodes: []string{
				"E1_b0", "E1_b1", "E1_b10", "E1_b11", "E1_b12", "E1_b13",
				"E1_b14", "E1_b15", "E1_b2", "E1_b3", "E1_b4", "E1_b5",
				"E1_b6", "E1_b7", "E1_b8", "E1_b9",
			},
		},
		{
			name:      "Error_Code_3 bit 6 plus a real bit returns only the real bit",
			e3:        0x0041,
			wantCodes: []string{"E3_b0"},
		},
		{
			name:      "Error_Code bit 12 has no entry and is ignored",
			e0:        0x1000,
			wantCodes: []string{},
		},
		{
			name:      "Error_Code KKB1 and KKB2",
			e0:        0xC000,
			wantCodes: []string{"E14", "E15"},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			set := entity.DecodeErrors(tc.e0, tc.e1, tc.e2, tc.e3)
			assert.Equal(t, tc.wantCodes, set.Codes())
		})
	}
}

func TestErrorSet_IsEmpty(t *testing.T) {
	t.Parallel()

	assert.True(t, entity.ErrorSet{}.IsEmpty())
	assert.True(t, entity.DecodeErrors(0, 0, 0, 0).IsEmpty())
	assert.True(t, entity.DecodeErrors(0, 0, 0, 0x0040).IsEmpty(), "reserved bit 6 must not produce entries")
	assert.False(t, entity.DecodeErrors(0x0010, 0, 0, 0).IsEmpty())
}

func TestErrorSet_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		e0   uint16
		want string
	}{
		{name: "empty set yields empty string", e0: 0, want: ""},
		{name: "single code", e0: 0x0010, want: "E04"},
		{name: "two codes joined and sorted", e0: 0x2400, want: "E10,E13"},
		{name: "kkb1 kkb2", e0: 0xC000, want: "E14,E15"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			set := entity.DecodeErrors(tc.e0, 0, 0, 0)
			assert.Equal(t, tc.want, set.String())
		})
	}
}

func TestDecodeErrors_DescriptionPopulated(t *testing.T) {
	t.Parallel()

	set := entity.DecodeErrors(0x0010, 0, 0, 0)
	require.Len(t, set, 1)
	assert.Equal(t, "Error_Code", set[0].Register)
	assert.Equal(t, uint8(4), set[0].Bit)
	assert.Equal(t, "E04", set[0].Code)
	assert.Contains(t, set[0].Description, "100%")
}

func TestDecodeErrors_UndocumentedDescriptionFormat(t *testing.T) {
	t.Parallel()

	set := entity.DecodeErrors(0, 0x0001, 0, 0)
	require.Len(t, set, 1)
	assert.Equal(t, "Error_Code_1", set[0].Register)
	assert.Equal(t, uint8(0), set[0].Bit)
	assert.Equal(t, "E1_b0", set[0].Code)
	assert.Equal(t, "undocumented bit 0 (Error_Code_1)", set[0].Description)
}

func TestError3ReservedBitConstant(t *testing.T) {
	t.Parallel()
	assert.Equal(t, uint8(6), entity.Error3ReservedBit)
}
