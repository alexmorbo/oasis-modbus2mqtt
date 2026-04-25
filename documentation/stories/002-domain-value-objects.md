---
title: "Feature: Domain value objects"
status: review
priority: high
complexity: 4
planning_model: opus-4-5
implementation_model: haiku-4
created: 2026-04-25
updated: 2026-04-25
depends_on: 001-initial-skeleton
risk_areas: []
---

## Context

Реализуем domain value objects для bridge — иммутабельные типы с валидацией, общие для всего сервиса. Это фундамент: на них будет опираться domain/entity (story 003), application/dto (story 004), infrastructure (modbus decode, mqtt publish).

Все 4 value objects напрямую следуют из реверс-инженеринговой сессии 2026-04-24:
- **Mode** — биты 0..1 регистра `h86 Dev_Keys_2`: 0=OFF, 1=HEAT, 2=COOL, 3=AUTO. У установки реально есть только OFF и HEAT (нет охладителя), но домен моделирует всё, что регистр умеет.
- **Temperature** — `h31 Temp_Target` (write) и `i57 Room_Temp_T*` (read) — uint16 со scale ×10. Валидный пользовательский диапазон 5..30 °C (raw 50..300). Read-side может приходить и шире (sensor), но write strict.
- **FanSpeed** — `h32 Fan_Target_1`, диапазон 1..10. Юзер на пульте ограничивает 4..7, это application-уровень capping (`NewFanSpeedClamped`), не domain invariant.
- **FirmwareVersion** — `i0 Firmware`, формат BCD-like uint16 `0xMmNn` → строка `v{M.m}.{N}.{n}` (пример: `0x5200` → `v5.2.0`). Используется для `sw_version` в MQTT discovery device block.
- **RegisterAddr / RegisterKind** — типизированные обёртки над `uint16` + enum `Holding|Input`. Используются в catalog/registers.go (story 003) и modbus client.

**Важно про адресацию:** все adress в коде — в HA нотации (0-based, PDU). Это уже учтено в reverse-engineering заметках; здесь просто uint16, никаких преобразований.

Полный план — `/Users/alexmorbo/Home/homelab/.thoughts/oasis-syberia-modbus-bridge/04-mqtt-bridge-plan.md`, секция `## 2. Domain layer: ключевые типы`.

## User Story

**As a** Go-разработчик oasis-modbus2mqtt,
**I want to** иметь immutable, валидируемые value objects для всех скалярных понятий контроллера,
**So that** invalid state не мог пробраться в систему ни через MQTT-команды (validation на конструировании), ни через decode из modbus (явный FromRaw с error).

## Acceptance Criteria

- [ ] `domain/valueobject/mode.go`: `Mode` enum (OFF/HEAT/COOL/AUTO) + `Bits()` + `ModeFromRegister(uint16)` + `ApplyToRegister(reg uint16, m Mode) uint16` (RMW helper).
- [ ] `domain/valueobject/temperature.go`: struct `Temperature{ celsius float64 }`, конструкторы `NewTemperature(c float64)` (validates 5..30, rejects NaN/Inf), `TemperatureFromRaw(raw uint16)` (raw / 10, no validation since sensor read), getters `Celsius() float64`, `Raw() uint16` (clamped 50..300).
- [ ] `domain/valueobject/fan_speed.go`: struct `FanSpeed{ value uint16 }`, `NewFanSpeed(v uint16)` (1..10), `NewFanSpeedClamped(v, min, max uint16)` (clamps to [min,max] and to [1,10]), `Value() uint16`.
- [ ] `domain/valueobject/firmware.go`: struct `FirmwareVersion{ raw uint16 }`, `FirmwareFromRaw(uint16)` (any value valid — sensor data), `String() string` форматирует `v{M.m}.{N}.{n}` где M=high nibble of high byte, m=low nibble of high byte, N=high nibble of low byte, n=low nibble of low byte. Для `0x5200`: M=5, m=2, N=0, n=0 → `v5.2.0`. Если все нули — return `unknown`.
- [ ] `domain/valueobject/register.go`: `type RegisterAddr uint16` (just a typed alias with `String()` returning hex `0x%04X`), `type RegisterKind int` constants `RegisterKindInput`/`RegisterKindHolding`, метод `(RegisterKind) String() string` ("input"/"holding").
- [ ] Тесты `_test.go` для каждого файла с table-driven подходом, покрывающие: happy path, граничные значения, ошибки валидации, edge cases (NaN, overflow).
- [ ] **Coverage ≥ 95%** для пакета `domain/valueobject` (Plan agent: проверить через `go test -coverpkg=./domain/valueobject ./domain/valueobject/...` после имплементации).
- [ ] `golangci-lint run ./...` PASS.
- [ ] `go test -short ./...` PASS.
- [ ] Все exported идентификаторы имеют godoc-комментарий (одна строка в стиле `// TypeName does X.`).

## Constraints

- НЕТ зависимостей от внешних пакетов — только stdlib (`errors`, `fmt`, `math`).
- НЕТ зависимостей от других подпакетов сервиса (`domain/valueobject` — корень дерева).
- Все типы — **value semantics**, не указатели. Конструкторы возвращают value + error. Геттеры — value receivers.
- Ошибки — sentinel errors на уровне пакета: `ErrInvalidTemperature`, `ErrInvalidFanSpeed`, `ErrInvalidMode`. Без `errors.New` в каждом конструкторе — переиспользуем константы. Конструкторы оборачивают через `fmt.Errorf("...: %w", ErrXxx)` с контекстом.
- `Temperature` НЕ имеет `Equals` или сравнения — float сравнения опасны, не нужны для use cases.
- `Mode` — `int` под капотом (не `uint16`), но `Bits()` всегда возвращает `uint16` — потому что регистр uint16. `ModeFromRegister` маскирует `& 0x3` (только биты 0..1), всё остальное игнорирует.
- Тесты в **отдельном пакете** `valueobject_test` (черный ящик через `import .` или `valueobject.X` — Planning agent выбирает; `import .` нежелателен по `go vet`).
- НЕТ benchmark тестов в этой story.
- НЕ создавать `doc.go` файлы.

---

## Technical Specification

### Analysis

- `Mode` использует `int` под капотом (Go enum convention) с константами `ModeOff..ModeAuto`. `ModeFromRegister` — total function (без error) потому что `& 0x3` всегда даёт валидное значение 0..3; `NewMode(int)` — partial function, валидирует 0..3 явно (нужен для DTO mapping из application слоя).
- `Temperature.NewTemperature` отдельно отлавливает `NaN` и `±Inf` через `math.IsNaN`/`math.IsInf` ДО проверки диапазона — иначе `NaN < 5` всегда false и его пропустит. `TemperatureFromRaw` без валидации (sensor может отдавать 0 при ошибке датчика). `Raw()` clamp в [50, 300] защитный — если домен попал invalid Temperature через прямую инициализацию (не должно но), Modbus write всё равно будет в диапазоне регистра.
- `FanSpeed.NewFanSpeedClamped` — defensive: даже если caller передал `min=0, max=99`, метод сам подтянет к `[1, 10]`. Это application-уровень UX (юзерский пульт ограничивает 4..7), domain не должен пропустить 0 или 11 даже при кривом capping вызове.
- `FirmwareVersion.String()` — BCD nibble decoding. Для `0x5200` (n==0) формат `v5.2.0`, для `0x5201` (n!=0) формат `v5.2.0.1` чтобы не терять информацию. `0x0000` → `unknown` — sentinel для "no data yet", используется в discovery до первого poll.
- `RegisterKind` использует `iota + 1` — zero value считается невалидным (`RegisterKindUnknown` = 0 неявно). `IsValid()` возвращает `true` только для известных kinds. `String()` для unknown → `"unknown"`.

### Implementation Order

1. `domain/valueobject/register.go` (no deps)
2. `domain/valueobject/mode.go`
3. `domain/valueobject/temperature.go`
4. `domain/valueobject/fan_speed.go`
5. `domain/valueobject/firmware.go`
6. Тесты для всех (можно писать параллельно с прод кодом)

**Note about test deps:** Tests use `github.com/stretchr/testify/assert` and `require`. Implementation Agent должен запустить `go mod tidy` ПОСЛЕ записи всех файлов — это автоматически добавит testify в `go.mod`/`go.sum` (стандарт по `documentation/golang/libraries.md`).

---

### 1. Register

#### File: `domain/valueobject/register.go`

```go
// Package valueobject provides immutable, validated domain value objects for
// the Oasis Syberia Modbus bridge. All types here are value semantics with no
// dependencies outside the Go standard library.
package valueobject

import "fmt"

// RegisterAddr is a typed wrapper over a 0-based PDU Modbus register address.
type RegisterAddr uint16

// String returns the address formatted as a 4-digit uppercase hex literal.
func (r RegisterAddr) String() string {
	return fmt.Sprintf("0x%04X", uint16(r))
}

// Uint16 returns the address as a raw uint16 for Modbus client calls.
func (r RegisterAddr) Uint16() uint16 {
	return uint16(r)
}

// RegisterKind enumerates Modbus register kinds. The zero value is invalid.
type RegisterKind int

const (
	// RegisterKindInput is a read-only Modbus input register (function code 0x04).
	RegisterKindInput RegisterKind = iota + 1
	// RegisterKindHolding is a read/write Modbus holding register (function codes 0x03/0x06/0x10).
	RegisterKindHolding
)

// IsValid reports whether k is a known register kind.
func (k RegisterKind) IsValid() bool {
	return k == RegisterKindInput || k == RegisterKindHolding
}

// String returns "input", "holding", or "unknown" depending on the kind.
func (k RegisterKind) String() string {
	switch k {
	case RegisterKindInput:
		return "input"
	case RegisterKindHolding:
		return "holding"
	default:
		return "unknown"
	}
}
```

#### File: `domain/valueobject/register_test.go`

```go
package valueobject_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"
)

func TestRegisterAddr_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		addr valueobject.RegisterAddr
		want string
	}{
		{name: "zero", addr: 0x0000, want: "0x0000"},
		{name: "low byte", addr: 0x0002, want: "0x0002"},
		{name: "Temp_Target h31", addr: 0x001F, want: "0x001F"},
		{name: "Dev_Keys_2 h86", addr: 0x0056, want: "0x0056"},
		{name: "max", addr: 0xFFFF, want: "0xFFFF"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.addr.String())
		})
	}
}

func TestRegisterAddr_Uint16(t *testing.T) {
	t.Parallel()

	assert.Equal(t, uint16(0), valueobject.RegisterAddr(0).Uint16())
	assert.Equal(t, uint16(0x1F), valueobject.RegisterAddr(0x1F).Uint16())
	assert.Equal(t, uint16(0xFFFF), valueobject.RegisterAddr(0xFFFF).Uint16())
}

func TestRegisterKind_IsValid(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		kind valueobject.RegisterKind
		want bool
	}{
		{name: "zero invalid", kind: 0, want: false},
		{name: "input valid", kind: valueobject.RegisterKindInput, want: true},
		{name: "holding valid", kind: valueobject.RegisterKindHolding, want: true},
		{name: "out of range high", kind: 99, want: false},
		{name: "negative", kind: -1, want: false},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.kind.IsValid())
		})
	}
}

func TestRegisterKind_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		kind valueobject.RegisterKind
		want string
	}{
		{name: "zero is unknown", kind: 0, want: "unknown"},
		{name: "input", kind: valueobject.RegisterKindInput, want: "input"},
		{name: "holding", kind: valueobject.RegisterKindHolding, want: "holding"},
		{name: "out of range is unknown", kind: 42, want: "unknown"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.kind.String())
		})
	}
}
```

---

### 2. Mode

#### File: `domain/valueobject/mode.go`

```go
package valueobject

import (
	"errors"
	"fmt"
)

// ErrInvalidMode is returned when a mode value is outside 0..3.
var ErrInvalidMode = errors.New("invalid mode")

// Mode encodes the two low bits of holding register Dev_Keys_2 (h86).
type Mode int

const (
	// ModeOff disables active conditioning; fans may still run.
	ModeOff Mode = 0
	// ModeHeat enables heat-only conditioning.
	ModeHeat Mode = 1
	// ModeCool enables cool-only conditioning (no-op on heat-only hardware).
	ModeCool Mode = 2
	// ModeAuto enables automatic mode selection.
	ModeAuto Mode = 3
)

// NewMode validates v and returns a Mode in [0, 3].
func NewMode(v int) (Mode, error) {
	if v < 0 || v > 3 {
		return ModeOff, fmt.Errorf("mode value %d out of range [0,3]: %w", v, ErrInvalidMode)
	}
	return Mode(v), nil
}

// Bits returns the mode as the two low bits of a uint16 register payload.
func (m Mode) Bits() uint16 {
	masked := m & Mode(0x3)
	//nolint:gosec // G115: masked is guaranteed to be in [0, 3], safe conversion
	return uint16(masked)
}

// String returns the canonical lowercase mode label.
func (m Mode) String() string {
	switch m {
	case ModeOff:
		return "off"
	case ModeHeat:
		return "heat"
	case ModeCool:
		return "cool"
	case ModeAuto:
		return "auto"
	default:
		return "unknown"
	}
}

// ModeFromRegister extracts the mode from bits 0..1 of a Dev_Keys_2 register value.
func ModeFromRegister(v uint16) Mode {
	return Mode(v & 0x3)
}

// ApplyToRegister returns reg with bits 0..1 replaced by m.Bits(), preserving all other bits.
func ApplyToRegister(reg uint16, m Mode) uint16 {
	return (reg &^ uint16(0x3)) | m.Bits()
}
```

#### File: `domain/valueobject/mode_test.go`

```go
package valueobject_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"
)

func TestNewMode(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   int
		want    valueobject.Mode
		wantErr bool
	}{
		{name: "off", input: 0, want: valueobject.ModeOff},
		{name: "heat", input: 1, want: valueobject.ModeHeat},
		{name: "cool", input: 2, want: valueobject.ModeCool},
		{name: "auto", input: 3, want: valueobject.ModeAuto},
		{name: "negative", input: -1, wantErr: true},
		{name: "above range", input: 4, wantErr: true},
		{name: "far above range", input: 9999, wantErr: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := valueobject.NewMode(tc.input)
			if tc.wantErr {
				require.Error(t, err)
				assert.True(t, errors.Is(err, valueobject.ErrInvalidMode))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestMode_Bits(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mode valueobject.Mode
		want uint16
	}{
		{name: "off", mode: valueobject.ModeOff, want: 0},
		{name: "heat", mode: valueobject.ModeHeat, want: 1},
		{name: "cool", mode: valueobject.ModeCool, want: 2},
		{name: "auto", mode: valueobject.ModeAuto, want: 3},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.mode.Bits())
		})
	}
}

func TestMode_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		mode valueobject.Mode
		want string
	}{
		{name: "off", mode: valueobject.ModeOff, want: "off"},
		{name: "heat", mode: valueobject.ModeHeat, want: "heat"},
		{name: "cool", mode: valueobject.ModeCool, want: "cool"},
		{name: "auto", mode: valueobject.ModeAuto, want: "auto"},
		{name: "out of range is unknown", mode: valueobject.Mode(42), want: "unknown"},
		{name: "negative is unknown", mode: valueobject.Mode(-1), want: "unknown"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.mode.String())
		})
	}
}

func TestModeFromRegister(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		reg  uint16
		want valueobject.Mode
	}{
		{name: "zero", reg: 0x0000, want: valueobject.ModeOff},
		{name: "heat low bit", reg: 0x0001, want: valueobject.ModeHeat},
		{name: "cool", reg: 0x0002, want: valueobject.ModeCool},
		{name: "auto", reg: 0x0003, want: valueobject.ModeAuto},
		{name: "high bits ignored", reg: 0xFF01, want: valueobject.ModeHeat},
		{name: "all bits set masks to auto", reg: 0xFFFF, want: valueobject.ModeAuto},
		{name: "only bit 2 set masks to off", reg: 0x0004, want: valueobject.ModeOff},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, valueobject.ModeFromRegister(tc.reg))
		})
	}
}

func TestApplyToRegister(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		reg  uint16
		mode valueobject.Mode
		want uint16
	}{
		{name: "zero reg set heat", reg: 0x0000, mode: valueobject.ModeHeat, want: 0x0001},
		{name: "preserve high bits", reg: 0xFFFC, mode: valueobject.ModeHeat, want: 0xFFFD},
		{name: "preserve high bits set off", reg: 0xFFFF, mode: valueobject.ModeOff, want: 0xFFFC},
		{name: "preserve middle bits", reg: 0x00F0, mode: valueobject.ModeCool, want: 0x00F2},
		{name: "overwrite cool with auto", reg: 0x00A2, mode: valueobject.ModeAuto, want: 0x00A3},
		{name: "overwrite auto with off", reg: 0x00A3, mode: valueobject.ModeOff, want: 0x00A0},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, valueobject.ApplyToRegister(tc.reg, tc.mode))
		})
	}
}
```

---

### 3. Temperature

#### File: `domain/valueobject/temperature.go`

```go
package valueobject

import (
	"errors"
	"fmt"
	"math"
)

// ErrInvalidTemperature is returned when a temperature is NaN, infinite, or out of [5, 30] °C.
var ErrInvalidTemperature = errors.New("invalid temperature")

const (
	temperatureMinCelsius = 5.0
	temperatureMaxCelsius = 30.0
	temperatureScale      = 10.0
	temperatureRawMin     = 50
	temperatureRawMax     = 300
)

// Temperature is an immutable Celsius value validated to the controller's accepted write range.
type Temperature struct {
	celsius float64
}

// NewTemperature validates c is finite and within [5, 30] °C and returns a Temperature.
func NewTemperature(c float64) (Temperature, error) {
	if math.IsNaN(c) {
		return Temperature{}, fmt.Errorf("temperature is NaN: %w", ErrInvalidTemperature)
	}
	if math.IsInf(c, 0) {
		return Temperature{}, fmt.Errorf("temperature is infinite: %w", ErrInvalidTemperature)
	}
	if c < temperatureMinCelsius || c > temperatureMaxCelsius {
		return Temperature{}, fmt.Errorf("temperature %g out of range [%g,%g]: %w",
			c, temperatureMinCelsius, temperatureMaxCelsius, ErrInvalidTemperature)
	}
	return Temperature{celsius: c}, nil
}

// TemperatureFromRaw decodes a Modbus uint16 sensor value (×10 scale) into a Temperature.
// No validation is performed: sensor reads may legitimately fall outside the user-write range.
func TemperatureFromRaw(raw uint16) Temperature {
	return Temperature{celsius: float64(raw) / temperatureScale}
}

// Celsius returns the temperature in degrees Celsius.
func (t Temperature) Celsius() float64 {
	return t.celsius
}

// Raw encodes the temperature for a Modbus holding-register write, clamped to [50, 300].
func (t Temperature) Raw() uint16 {
	scaled := math.Round(t.celsius * temperatureScale)
	if scaled < temperatureRawMin {
		return temperatureRawMin
	}
	if scaled > temperatureRawMax {
		return temperatureRawMax
	}
	return uint16(scaled)
}
```

#### File: `domain/valueobject/temperature_test.go`

```go
package valueobject_test

import (
	"errors"
	"math"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"
)

func TestNewTemperature(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   float64
		want    float64
		wantErr bool
	}{
		{name: "min boundary", input: 5.0, want: 5.0},
		{name: "just above min", input: 5.01, want: 5.01},
		{name: "typical value", input: 22.5, want: 22.5},
		{name: "max boundary", input: 30.0, want: 30.0},
		{name: "below min", input: 4.99, wantErr: true},
		{name: "well below min", input: -1, wantErr: true},
		{name: "zero", input: 0, wantErr: true},
		{name: "above max", input: 30.01, wantErr: true},
		{name: "well above max", input: 100, wantErr: true},
		{name: "NaN", input: math.NaN(), wantErr: true},
		{name: "positive infinity", input: math.Inf(1), wantErr: true},
		{name: "negative infinity", input: math.Inf(-1), wantErr: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := valueobject.NewTemperature(tc.input)
			if tc.wantErr {
				require.Error(t, err)
				assert.True(t, errors.Is(err, valueobject.ErrInvalidTemperature))
				return
			}
			require.NoError(t, err)
			assert.InDelta(t, tc.want, got.Celsius(), 1e-9)
		})
	}
}

func TestTemperatureFromRaw(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  uint16
		want float64
	}{
		{name: "zero", raw: 0, want: 0.0},
		{name: "min user range", raw: 50, want: 5.0},
		{name: "typical", raw: 225, want: 22.5},
		{name: "max user range", raw: 300, want: 30.0},
		{name: "above user range allowed", raw: 500, want: 50.0},
		{name: "max uint16", raw: 0xFFFF, want: 6553.5},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := valueobject.TemperatureFromRaw(tc.raw)
			assert.InDelta(t, tc.want, got.Celsius(), 1e-9)
		})
	}
}

func TestTemperature_Raw(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		celsius float64
		want    uint16
	}{
		{name: "min boundary", celsius: 5.0, want: 50},
		{name: "typical", celsius: 22.5, want: 225},
		{name: "max boundary", celsius: 30.0, want: 300},
		{name: "rounds up", celsius: 22.55, want: 226},
		{name: "rounds down", celsius: 22.54, want: 225},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			temp, err := valueobject.NewTemperature(tc.celsius)
			require.NoError(t, err)
			assert.Equal(t, tc.want, temp.Raw())
		})
	}
}

func TestTemperature_RawClampsOutOfRangeValues(t *testing.T) {
	t.Parallel()

	// Construct via FromRaw which performs no validation so we can verify Raw() clamping
	// when an out-of-write-range sensor value flows back through.
	low := valueobject.TemperatureFromRaw(0)
	assert.Equal(t, uint16(50), low.Raw(), "below-range value must clamp up to 50")

	high := valueobject.TemperatureFromRaw(1000)
	assert.Equal(t, uint16(300), high.Raw(), "above-range value must clamp down to 300")

	exactlyMin := valueobject.TemperatureFromRaw(50)
	assert.Equal(t, uint16(50), exactlyMin.Raw())

	exactlyMax := valueobject.TemperatureFromRaw(300)
	assert.Equal(t, uint16(300), exactlyMax.Raw())
}
```

---

### 4. FanSpeed

#### File: `domain/valueobject/fan_speed.go`

```go
package valueobject

import (
	"errors"
	"fmt"
)

// ErrInvalidFanSpeed is returned when a fan speed is outside [1, 10].
var ErrInvalidFanSpeed = errors.New("invalid fan speed")

const (
	fanSpeedMin uint16 = 1
	fanSpeedMax uint16 = 10
)

// FanSpeed is a validated controller fan speed in [1, 10].
type FanSpeed struct {
	value uint16
}

// NewFanSpeed validates v is in [1, 10] and returns a FanSpeed.
func NewFanSpeed(v uint16) (FanSpeed, error) {
	if v < fanSpeedMin || v > fanSpeedMax {
		return FanSpeed{}, fmt.Errorf("fan speed %d out of range [%d,%d]: %w",
			v, fanSpeedMin, fanSpeedMax, ErrInvalidFanSpeed)
	}
	return FanSpeed{value: v}, nil
}

// NewFanSpeedClamped returns a FanSpeed clamped first into [min, max] and then into [1, 10].
// Defensive: if min/max themselves fall outside [1, 10] the bounds are tightened to the
// hardware-valid range before clamping v.
func NewFanSpeedClamped(v, min, max uint16) FanSpeed {
	if min < fanSpeedMin {
		min = fanSpeedMin
	}
	if max > fanSpeedMax {
		max = fanSpeedMax
	}
	if min > max {
		min, max = max, min
	}
	if v < min {
		v = min
	}
	if v > max {
		v = max
	}
	return FanSpeed{value: v}
}

// Value returns the fan speed as a raw uint16 ready for a Modbus write.
func (f FanSpeed) Value() uint16 {
	return f.value
}
```

#### File: `domain/valueobject/fan_speed_test.go`

```go
package valueobject_test

import (
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"
)

func TestNewFanSpeed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		input   uint16
		want    uint16
		wantErr bool
	}{
		{name: "min boundary", input: 1, want: 1},
		{name: "mid range", input: 5, want: 5},
		{name: "max boundary", input: 10, want: 10},
		{name: "zero rejected", input: 0, wantErr: true},
		{name: "above max rejected", input: 11, wantErr: true},
		{name: "max uint16 rejected", input: 0xFFFF, wantErr: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := valueobject.NewFanSpeed(tc.input)
			if tc.wantErr {
				require.Error(t, err)
				assert.True(t, errors.Is(err, valueobject.ErrInvalidFanSpeed))
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got.Value())
		})
	}
}

func TestNewFanSpeedClamped(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		v    uint16
		min  uint16
		max  uint16
		want uint16
	}{
		{name: "in range", v: 5, min: 4, max: 7, want: 5},
		{name: "below min clamped up", v: 0, min: 4, max: 7, want: 4},
		{name: "above max clamped down", v: 99, min: 4, max: 7, want: 7},
		{name: "below min equal one clamps up", v: 0, min: 1, max: 10, want: 1},
		{name: "above max ten clamps down", v: 11, min: 1, max: 10, want: 10},
		{name: "min below 1 tightens to 1", v: 0, min: 0, max: 7, want: 1},
		{name: "max above 10 tightens to 10", v: 99, min: 4, max: 99, want: 10},
		{name: "both bounds invalid still clamps to hw range", v: 99, min: 0, max: 99, want: 10},
		{name: "swapped bounds normalised", v: 5, min: 7, max: 4, want: 5},
		{name: "swapped bounds with low v", v: 1, min: 7, max: 4, want: 4},
		{name: "swapped bounds with high v", v: 9, min: 7, max: 4, want: 7},
		{name: "v exactly at min", v: 4, min: 4, max: 7, want: 4},
		{name: "v exactly at max", v: 7, min: 4, max: 7, want: 7},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := valueobject.NewFanSpeedClamped(tc.v, tc.min, tc.max)
			assert.Equal(t, tc.want, got.Value())
		})
	}
}
```

---

### 5. Firmware

#### File: `domain/valueobject/firmware.go`

```go
package valueobject

import "fmt"

const firmwareUnknownLabel = "unknown"

// FirmwareVersion is a BCD-encoded controller firmware version read from input register i0.
//
// The raw uint16 is interpreted as 0xMmNn where each character is a 4-bit nibble:
//
//	M = high nibble of high byte (major)
//	m = low  nibble of high byte (minor)
//	N = high nibble of low  byte (patch)
//	n = low  nibble of low  byte (build, optional)
//
// String formatting:
//
//	0x0000        -> "unknown"
//	build nibble n == 0   -> "v{M}.{m}.{N}"     (e.g. 0x5200 -> "v5.2.0")
//	build nibble n != 0   -> "v{M}.{m}.{N}.{n}" (e.g. 0x5201 -> "v5.2.0.1")
type FirmwareVersion struct {
	raw uint16
}

// FirmwareFromRaw wraps a raw uint16 firmware register value. No validation is performed
// because the value originates from a sensor read.
func FirmwareFromRaw(raw uint16) FirmwareVersion {
	return FirmwareVersion{raw: raw}
}

// Raw returns the underlying uint16 representation of the firmware version.
func (f FirmwareVersion) Raw() uint16 {
	return f.raw
}

// IsKnown reports whether the firmware register has been populated with a non-zero value.
func (f FirmwareVersion) IsKnown() bool {
	return f.raw != 0
}

// String renders the firmware version per the BCD nibble convention documented on the type.
func (f FirmwareVersion) String() string {
	if f.raw == 0 {
		return firmwareUnknownLabel
	}
	major := (f.raw >> 12) & 0xF
	minor := (f.raw >> 8) & 0xF
	patch := (f.raw >> 4) & 0xF
	build := f.raw & 0xF
	if build == 0 {
		return fmt.Sprintf("v%d.%d.%d", major, minor, patch)
	}
	return fmt.Sprintf("v%d.%d.%d.%d", major, minor, patch, build)
}
```

#### File: `domain/valueobject/firmware_test.go`

```go
package valueobject_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"
)

func TestFirmwareFromRaw_Raw(t *testing.T) {
	t.Parallel()

	assert.Equal(t, uint16(0), valueobject.FirmwareFromRaw(0).Raw())
	assert.Equal(t, uint16(0x5200), valueobject.FirmwareFromRaw(0x5200).Raw())
	assert.Equal(t, uint16(0xFFFF), valueobject.FirmwareFromRaw(0xFFFF).Raw())
}

func TestFirmwareVersion_IsKnown(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  uint16
		want bool
	}{
		{name: "zero is unknown", raw: 0x0000, want: false},
		{name: "non-zero is known", raw: 0x5200, want: true},
		{name: "max value is known", raw: 0xFFFF, want: true},
		{name: "low byte only is known", raw: 0x0001, want: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, valueobject.FirmwareFromRaw(tc.raw).IsKnown())
		})
	}
}

func TestFirmwareVersion_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  uint16
		want string
	}{
		{name: "zero is unknown", raw: 0x0000, want: "unknown"},
		{name: "v5.2.0 from controller", raw: 0x5200, want: "v5.2.0"},
		{name: "v5.2.0.1 with build", raw: 0x5201, want: "v5.2.0.1"},
		{name: "v1.2.3.4 all nibbles", raw: 0x1234, want: "v1.2.3.4"},
		{name: "v0.0.0.1 only build", raw: 0x0001, want: "v0.0.0.1"},
		{name: "v0.0.1.0 only patch", raw: 0x0010, want: "v0.0.1"},
		{name: "v0.1.0.0 only minor", raw: 0x0100, want: "v0.1.0"},
		{name: "v1.0.0.0 only major", raw: 0x1000, want: "v1.0.0"},
		{name: "vF.F.F.F max nibbles", raw: 0xFFFF, want: "v15.15.15.15"},
		{name: "v9.9.9.0 large no build", raw: 0x9990, want: "v9.9.9"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, valueobject.FirmwareFromRaw(tc.raw).String())
		})
	}
}
```

---

## Agent Execution

### Implementation Agent Instructions

```
Task tool call:
- subagent_type: "general-purpose"
- model: "haiku"
- prompt: |
    You are an IMPLEMENTATION AGENT for story 002-domain-value-objects.

    YOUR ONLY JOB: Copy code from the story file to actual files. Run verification.

    RULES:
    - DO NOT modify code
    - DO NOT add anything
    - DO NOT fix errors
    - ONLY copy each "#### File: `path`" code block to that path

    PROCESS:
    1. Read story: /Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/documentation/stories/002-domain-value-objects.md
    2. For each "#### File: `path`" section, write the code to that path (relative to apps/oasis-modbus2mqtt/)
    3. Run `go mod tidy` to pull testify into go.mod/go.sum
    4. Run verification:
       - cd /Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt
       - gofmt -l domain/valueobject/ (must be empty output)
       - go build ./...
       - go vet ./...
       - go test -short ./...
       - go test -coverpkg=./domain/valueobject ./domain/valueobject/... (parse coverage line, must be ≥95%)
       - golangci-lint run ./...
    5. If ALL PASS: status to "review", fill Verification Results
    6. If FAIL: write errors verbatim to "Issues Found", status stays "in_progress"

    Work directory: /Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/
```

### Fix Agent Instructions

Standard. Edit story only, then re-launch Implementation Agent.

---

## Implementation Notes

### Progress

- [x] `domain/valueobject/register.go` created
- [x] `domain/valueobject/mode.go` created
- [x] `domain/valueobject/temperature.go` created
- [x] `domain/valueobject/fan_speed.go` created
- [x] `domain/valueobject/firmware.go` created
- [x] All `_test.go` files created
- [x] gofmt clean
- [x] go build/vet pass
- [x] go test pass
- [x] coverage ≥95% (actual: 100.0%)
- [x] golangci-lint pass (0 issues)

### Verification Results

```
gofmt:        PASS (no output = clean)
go build:     PASS
go vet:       PASS
go test:      PASS (all tests in domain/valueobject passed in 0.471s)
coverage:     100.0% (statements in ./domain/valueobject)
golangci-lint: PASS (0 issues)
```

go mod tidy output:
```
go: downloading github.com/stretchr/testify v1.11.1
go: found github.com/stretchr/testify/assert in github.com/stretchr/testify v1.11.1
go: found github.com/stretchr/testify/require in github.com/stretchr/testify v1.11.1
go: downloading github.com/davecgh/go-spew v1.1.1
go: downloading github.com/pmezard/go-difflib v1.0.0
go: downloading gopkg.in/yaml.v3 v3.0.1
go: downloading gopkg.in/check.v1 v0.0.0-20161208181325-20d25e280405
```

### Issues Found

(none)

### Fixes Applied

**gosec G115 (1 fix — int→uint16 conversion in Mode.Bits):**

- `mode.go` line 35: Applied `//nolint:gosec // G115` comment. The mask `& Mode(0x3)` is applied while still in Mode (int) type, bounding the value to [0, 3] before widening to `uint16`. The conversion is safe but gosec cannot statically verify it without the nolint directive.

**Story sync:**

- Story sync: `mode.go` block updated to match the lint-passing implementation. Fix Agent's earlier `uint16(m & 0x3)` did not satisfy gosec G115 because `Mode` is int-backed; the working version masks via Mode type, narrows once, and uses `//nolint:gosec` with explanation.

---

## Files Changed

- `/Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/domain/valueobject/register.go`
- `/Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/domain/valueobject/register_test.go`
- `/Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/domain/valueobject/mode.go`
- `/Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/domain/valueobject/mode_test.go`
- `/Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/domain/valueobject/temperature.go`
- `/Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/domain/valueobject/temperature_test.go`
- `/Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/domain/valueobject/fan_speed.go`
- `/Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/domain/valueobject/fan_speed_test.go`
- `/Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/domain/valueobject/firmware.go`
- `/Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/domain/valueobject/firmware_test.go`
- `/Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/go.mod` (testify v1.11.1 added)
- `/Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/go.sum` (testify + transitive deps)
