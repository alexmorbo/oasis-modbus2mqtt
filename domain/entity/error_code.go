package entity

import (
	"fmt"
	"sort"
	"strings"
)

// ErrorBit describes a single decoded controller error flag.
type ErrorBit struct {
	// Register identifies the source register name (e.g. "Error_Code", "Error_Code_3").
	Register string
	// Bit is the bit position within the register, 0..15.
	Bit uint8
	// Code is the short identifier shown in MQTT / UI ("E04", "E10", "E2_b3").
	Code string
	// Description is a human-readable explanation of the fault.
	Description string
}

// ErrorSet is an ordered collection of decoded error flags. Order follows the
// catalog declaration order (Error_Code first, then Error_Code_1/2/3, by bit).
type ErrorSet []ErrorBit

// Error3ReservedBit is the bit position in Error_Code_3 documented as a
// reserved value (always reads as 1 on the wire) and therefore filtered out
// of any decoded ErrorSet.
const Error3ReservedBit uint8 = 6

type errorBitDef struct {
	register    string
	bit         uint8
	code        string
	description string
}

// errorBitCatalog enumerates every decodable error flag across the four error
// registers. Bit 6 of Error_Code_3 is intentionally absent — it is reserved.
var errorBitCatalog = buildErrorBitCatalog()

func buildErrorBitCatalog() []errorBitDef {
	out := make([]errorBitDef, 0, 64)

	// Error_Code (i4) — documented bits per controller register map.
	documented := []errorBitDef{
		{register: "Error_Code", bit: 0, code: "E00", description: "T1 sensor fault (open or short)"},
		{register: "Error_Code", bit: 1, code: "E01", description: "T2 sensor fault (open or short)"},
		{register: "Error_Code", bit: 2, code: "E02", description: "T3 sensor fault (open or short)"},
		{register: "Error_Code", bit: 3, code: "E03", description: "filter1 pressure sensor fault"},
		{register: "Error_Code", bit: 4, code: "E04", description: "filter1 100% clogged"},
		{register: "Error_Code", bit: 5, code: "E05", description: "no coolant in the system"},
		{register: "Error_Code", bit: 6, code: "E06", description: "freeze threat (water temperature below 5C)"},
		{register: "Error_Code", bit: 7, code: "E07", description: "freeze threat (capillary sensor)"},
		{register: "Error_Code", bit: 8, code: "E08", description: "freeze threat (air temperature below 5C)"},
		{register: "Error_Code", bit: 9, code: "E09", description: "fan1 pressure sensor fault"},
		{register: "Error_Code", bit: 10, code: "E10", description: "FAN1 FAULT"},
		{register: "Error_Code", bit: 11, code: "E11", description: "FIRE"},
		{register: "Error_Code", bit: 13, code: "E13", description: "CALORIFIER OVERHEAT"},
		{register: "Error_Code", bit: 14, code: "E14", description: "KKB1 pressure fault"},
		{register: "Error_Code", bit: 15, code: "E15", description: "KKB2 pressure fault"},
	}
	out = append(out, documented...)

	// Error_Code_1 (i5) — generic placeholders for all 16 bits.
	for bit := 0; bit < 16; bit++ {
		b := uint8(bit) //nolint:gosec // bit is in [0,15]
		out = append(out, errorBitDef{
			register:    "Error_Code_1",
			bit:         b,
			code:        fmt.Sprintf("E1_b%d", b),
			description: fmt.Sprintf("undocumented bit %d (Error_Code_1)", b),
		})
	}

	// Error_Code_2 (i69) — generic placeholders for all 16 bits.
	for bit := 0; bit < 16; bit++ {
		b := uint8(bit) //nolint:gosec // bit is in [0,15]
		out = append(out, errorBitDef{
			register:    "Error_Code_2",
			bit:         b,
			code:        fmt.Sprintf("E2_b%d", b),
			description: fmt.Sprintf("undocumented bit %d (Error_Code_2)", b),
		})
	}

	// Error_Code_3 (i70) — generic placeholders, except bit 6 is reserved and skipped.
	for bit := 0; bit < 16; bit++ {
		b := uint8(bit) //nolint:gosec // bit is in [0,15]
		if b == Error3ReservedBit {
			continue
		}
		out = append(out, errorBitDef{
			register:    "Error_Code_3",
			bit:         b,
			code:        fmt.Sprintf("E3_b%d", b),
			description: fmt.Sprintf("undocumented bit %d (Error_Code_3)", b),
		})
	}

	return out
}

// DecodeErrors walks the error bit catalog and collects every flag set in the
// supplied register reads. The reserved bit 6 of Error_Code_3 is filtered out.
func DecodeErrors(e0, e1, e2, e3 uint16) ErrorSet {
	out := make(ErrorSet, 0)
	for _, def := range errorBitCatalog {
		var raw uint16
		switch def.register {
		case "Error_Code":
			raw = e0
		case "Error_Code_1":
			raw = e1
		case "Error_Code_2":
			raw = e2
		case "Error_Code_3":
			raw = e3
		default:
			continue
		}
		if raw&(uint16(1)<<def.bit) != 0 {
			out = append(out, ErrorBit{
				Register:    def.register,
				Bit:         def.bit,
				Code:        def.code,
				Description: def.description,
			})
		}
	}
	return out
}

// IsEmpty reports whether the ErrorSet contains no flags.
func (s ErrorSet) IsEmpty() bool {
	return len(s) == 0
}

// Codes returns the lexicographically sorted list of error codes in the set.
func (s ErrorSet) Codes() []string {
	codes := make([]string, len(s))
	for i, b := range s {
		codes[i] = b.Code
	}
	sort.Strings(codes)
	return codes
}

// String returns the sorted error codes joined by ",". An empty set yields "".
func (s ErrorSet) String() string {
	return strings.Join(s.Codes(), ",")
}
