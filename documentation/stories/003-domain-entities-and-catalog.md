---
title: "Feature: Domain entities and register catalog"
status: ready
priority: high
complexity: 6
planning_model: opus-4-5
implementation_model: haiku-4
created: 2026-04-25
updated: 2026-04-25
depends_on: 002-domain-value-objects
risk_areas: [single-source-of-truth-for-all-mqtt-entities]
---

## Context

Story 003 — это **сердце домена**. Здесь определяются:

1. **Domain entities** — иммутабельные доменные сущности, расшифровывающие битовые поля контроллера в осмысленные структуры:
   - `OperatingState` — текущая операция (idle / open_damper / preheat / start_fan / ... / rotor_spinup), 13 значений из `i3 State_1[0..4]`.
   - `ErrorCode` — каталог битовых флагов из `i4 Error_Code` + `i5 Error_Code_1` + `i69 Error_Code_2` + `i70 Error_Code_3` с человекочитаемым описанием каждого бита.
   - `DeviceConfig` — расшифровка `h0 Type_Dev`: какой калорифер (electric/water), есть ли охладитель/рекуператер. У нашего железа — `0x0001` (только электрокалорифер).
   - `Snapshot` — aggregate, представляющий полный snapshot одного poll цикла. Это то, что Poller (story 008) производит и передаёт в PublishStateUseCase (story 012).

2. **Register catalog** (`domain/catalog/registers.go`) — **single declarative table** всех регистров с метаданными:
   - адрес (HA notation), kind (input/holding), canonical name (используется в MQTT топиках),
   - poll group (Hot 5s / Medium 15s / Slow 60s),
   - **EntityHint** — что это за HA сущность (sensor/binary_sensor/switch/climate/etc), device_class, unit, entity_category. Этот hint потом используется discovery builder (story 011).
   - decoder — необязательная функция, которая применяет scale/sign к raw uint16. Например, для T1 — `int16(raw) / 10.0`.

   **Зачем catalog:** добавление нового регистра = одна строка в таблице. Discovery, polling, state publishing, метрики — всё итерирует по этой таблице. Без catalog мы бы дублировали "адрес X = sensor с unit °C" в трёх-четырёх местах.

**Reference материалы (mandatory чтение):**
- `/Users/alexmorbo/Home/homelab/.thoughts/oasis-syberia-modbus-bridge/01-controller-register-map.md` — полная карта регистров с расшифровкой битовых полей. Все цифры в catalog должны соответствовать.
- `/Users/alexmorbo/Home/homelab/.thoughts/oasis-syberia-modbus-bridge/02-controller-quirks.md` — поведенческие особенности (Power_Dev edge, Dev_Keys_2 RMW, ~1Hz PWM пульсация, и т.д.).
- `/Users/alexmorbo/Home/homelab/.thoughts/oasis-syberia-modbus-bridge/04-mqtt-bridge-plan.md` секция `## 2. Domain layer` и `## 7. Polling schedule`.

## User Story

**As a** разработчик oasis-modbus2mqtt,
**I want to** иметь декларативный catalog всех регистров и иммутабельные доменные сущности расшифровки,
**So that** добавление нового регистра не требовало бы менять 5 файлов в инфраструктурном слое, а decode bit-полей был бы безопасным (типизированным) и тестируемым изолированно.

## Acceptance Criteria

### Entities

- [ ] `domain/entity/operating_state.go`:
  - `type OperatingState int` с константами `OpIdle=0` ... `OpRotorSpinup=12` (13 значений + неявный `OpUnknown` = -1 для out-of-range).
  - `OperatingStateFromRegister(uint16) OperatingState` — маскирует `& 0x1F` (биты 0..4), валидирует `<=12`, иначе возвращает `OpUnknown`.
  - `String() string` — snake_case ("idle", "open_damper", ..., "rotor_spinup", "unknown").
  - `IsTransitional() bool` — true для всего кроме `OpIdle` и `OpUnknown`.
  - Sentinel: `var AllOperatingStates = []OperatingState{...}` для тестов (exported, доступен из `entity_test` пакета).

- [ ] `domain/entity/error_code.go`:
  - `type ErrorBit struct { Register string; Bit uint8; Code string; Description string }`. `Code` — короткий идентификатор `"E04"`, `"E10"`, `"E13"` (по спецификации). `Description` — человекочитаемая строка ("filter1 100% clogged").
  - `type ErrorSet []ErrorBit` (slice, потому что порядок может быть полезен для UI).
  - `DecodeErrors(e0, e1, e2, e3 uint16) ErrorSet` — итерирует встроенный bit catalog (см. ниже), возвращает ВСЕ установленные биты.
  - `(s ErrorSet) IsEmpty() bool`, `(s ErrorSet) Codes() []string` (sorted), `(s ErrorSet) String() string` (codes joined by ",").
  - **Bit catalog** (внутри пакета): сразу 4 регистра.
    - `Error_Code` (i4): bits 0..15 — sensor faults (T1/T2/T3), filter pressure, freeze threats, fan1 fault (b10), FIRE (b11), CALORIFIER OVERHEAT (b13), KKB1/KKB2 (b14/b15). Используем расшифровку из 01-controller-register-map.md секция `### Error_Code (HA i4)`.
    - `Error_Code_1` (i5): по PDF — extension. У нас в железе видели `0x0000`, поэтому ставим только заглушку (бит 0 = "ec1_b0_unknown"). Для бит 0..15 — `Description: "extended error code 1 bit N (see PDF)"`. Если потом узнаем точную расшифровку — обновим. Это ОК — domain decoder не должен врать о смысле, лучше "unknown bit" чем выдуманное описание.
    - `Error_Code_2` (i69): "T4/T5/filter2/fan2 errors" по карте регистров. То же — заглушка с пометкой "unknown" для всех 16 бит.
    - `Error_Code_3` (i70): мы видели `0x0040` (бит 6) — по PDF это "резерв" по 02-controller-quirks.md. Bit 6 нужно явно отметить как `"reserved"` и **не** включать в ErrorSet (фильтр). Остальные биты — заглушка.
  - **Game rule**: в catalog для известных битов (Error_Code 0..15) — конкретные коды + описания. Для НЕизвестных (всё остальное в Error_Code_1/2/3) — generic placeholder. Бит 6 Error_Code_3 — фильтр (никогда не попадает в ErrorSet).

- [ ] `domain/entity/device_config.go`:
  - `type HeaterType int`: `HeaterNone=0, HeaterElectric=1, HeaterWater=2, HeaterCombined=3`.
  - `type CoolerType int`: `CoolerNone=0, CoolerKKB=1, CoolerFancoil=2`.
  - `type RecuperatorType int`: `RecuperatorNone=0, RecuperatorPlate=1, RecuperatorRotor=2`.
  - `type DeviceConfig struct { Heater HeaterType; Cooler CoolerType; Recuperator RecuperatorType; Raw uint16 }`.
  - `DeviceConfigFromRegister(raw uint16) DeviceConfig` — биты 0..3 = heater, 4..7 = cooler, 8..11 = recuperator. Невалидные значения (>3 для cooler — например 7) сохраняем как value as-is (не паникуем).
  - Геттеры: `(c DeviceConfig) HasHeater() bool`, `HasCooler() bool`, `HasRecuperator() bool`.
  - `String() string` — `"electric_heater"` / `"electric_heater + kkb_cooler"` / `"none"` etc.
  - HeaterType.String() / CoolerType.String() / RecuperatorType.String().

- [ ] `domain/entity/controller.go`:
  - `type Snapshot struct { ... PolledAt time.Time }` aggregate. Поля точно по плану `04-mqtt-bridge-plan.md` секция 2.4 + добавляем `DeviceID uint16` (i79):
    ```
    Firmware           valueobject.FirmwareVersion
    DeviceID           uint16
    DeviceConfig       DeviceConfig
    PowerOn            bool
    Switching          bool
    HeatCapable        bool
    CoolCapable        bool
    CurrentMode        valueobject.Mode
    Operation          OperatingState
    OperationTimeLeft  time.Duration
    TargetTemp         valueobject.Temperature
    RoomTemp           valueobject.Temperature
    SupplyTemp         valueobject.Temperature
    FanTarget1         valueobject.FanSpeed
    FanState1          uint16
    FanState2          uint16
    HeaterPWM          bool
    DamperOpen         bool
    PIDDemand          uint16
    FilterPct          int16
    Errors             ErrorSet
    RoomHumidity       uint16
    PolledAt           time.Time
    ```
  - **Snapshot — pure value type, без методов-мутаторов.** Конструктор НЕ нужен — это DTO-shape. Поля экспортированы для прямого присваивания poller'ом.
  - Утилитный метод `(s Snapshot) IsHealthy(now time.Time, threshold time.Duration) bool` — `now.Sub(s.PolledAt) <= threshold`. Используется AvailabilityManager (story 008).
  - `OperationTimeLeft` — конвертируется из `i6 Last_Time` (старший байт = минуты, младший = секунды до конца операции). Helper `parseLastTime(raw uint16) time.Duration` локально в пакете catalog (не экспортируется), но decoder для него — в catalog. Snapshot это уже ` time.Duration`.

### Catalog

- [ ] `domain/catalog/poll_group.go`:
  - `type PollGroup int` константы `PollGroupHot=1, PollGroupMedium=2, PollGroupSlow=3`.
  - `String() string` ("hot"/"medium"/"slow"/"unknown").
  - `Period() time.Duration` (5s / 15s / 60s).

- [ ] `domain/catalog/entity_hint.go`:
  - `type EntityKind int`: `EntityKindSensor=1, EntityKindBinarySensor=2, EntityKindSwitch=3, EntityKindClimate=4, EntityKindButton=5, EntityKindNone=6` (None — для регистров, которые поллим но не публикуем как отдельную entity, например Type_Dev).
  - `type EntityCategory int`: `EntityCategoryDefault=0, EntityCategoryDiagnostic=1, EntityCategoryConfig=2`. Default = пустая строка в HA.
  - `type DeviceClass string` — typed string для HA device classes ("temperature", "humidity", "heat", "problem", "running"). Constants: `DeviceClassTemperature`, `DeviceClassHumidity`, `DeviceClassHeat`, `DeviceClassProblem`, `DeviceClassRunning`, `DeviceClassPower`, `DeviceClassNone = ""`.
  - `type StateClass string` constants: `StateClassMeasurement = "measurement"`, `StateClassNone = ""`.
  - `type EntityHint struct { Kind EntityKind; ObjectID string; Name string; DeviceClass DeviceClass; Unit string; StateClass StateClass; Category EntityCategory; Icon string; Precision int }`. Все поля с zero-value = "не задано/default".
  - `(EntityKind) String() string` — для тестов и логов.
  - `(EntityCategory) String() string` — `""` / `"diagnostic"` / `"config"`.

- [ ] `domain/catalog/registers.go`:
  - `type RegisterDef struct { Addr valueobject.RegisterAddr; Kind valueobject.RegisterKind; Name string; Group PollGroup; Hint EntityHint; Description string }`. Без `Decoder func(...)` поля — decode логика будет в poller usecase, не в catalog (избегаем function values в declarative table — их не сериализовать, не сравнить).
  - `var Registers = []RegisterDef{...}` — полная таблица. Минимум следующие записи (все остальные можно отложить, но эти **обязательны** для рабочего bridge):
    - **Input regs (poll Hot):** i2 State_0, i3 State_1, i4 Error_Code, i5 Error_Code_1, i6 Last_Time, i9 TkanK (T1), i14 ZagrFiltr1, i16 DOutputs, i18 Reg, i25 Fan_State_1, i30 Fan_State_2.
    - **Input regs (poll Medium):** i57 TKomn_x10_P (room temp), i58 Room_Hum_P, i69 Error_Code_2, i70 Error_Code_3.
    - **Input regs (poll Slow):** i0 Firmware, i79 DeviceID.
    - **Holding regs (poll Hot):** h31 Temp_Target, h32 Fan_Target_1.
    - **Holding regs (poll Medium):** h86 Dev_Keys_2.
    - **Holding regs (poll Slow):** h0 Type_Dev.
    - **Holding regs (commands, poll None):** h2 Power_Dev — `Group: PollGroupNone` (we'll add a dedicated constant `PollGroupNone = 0` for write-only). Это write-only command, не поллим (всё равно читается как 0 — quirk #3). EntityHint.Kind = EntityKindSwitch, потому что HA UI это переключатель.
  - `EntityHint` для каждого:
    - i9 TkanK → `Kind: Sensor, ObjectID: "supply_temperature", Name: "Supply temperature", DeviceClass: Temperature, Unit: "°C", StateClass: Measurement, Precision: 1`.
    - i14 ZagrFiltr1 → `Kind: Sensor, ObjectID: "filter_clog", Name: "Filter clog", Unit: "%", StateClass: Measurement, Icon: "mdi:air-filter"`.
    - i18 Reg → `Kind: Sensor, ObjectID: "heat_demand", Name: "Heat demand", Unit: "%", StateClass: Measurement, Category: Diagnostic`.
    - i25 Fan_State_1 → `Kind: Sensor, ObjectID: "supply_fan_speed", Name: "Supply fan speed", StateClass: Measurement`.
    - i30 Fan_State_2 → `Kind: Sensor, ObjectID: "exhaust_fan_speed", Name: "Exhaust fan speed", StateClass: Measurement`.
    - i57 TKomn → `Kind: Sensor, ObjectID: "room_temperature", Name: "Room temperature", DeviceClass: Temperature, Unit: "°C", StateClass: Measurement, Precision: 1`.
    - i58 Room_Hum_P → `Kind: Sensor, ObjectID: "room_humidity", Name: "Room humidity", DeviceClass: Humidity, Unit: "%", StateClass: Measurement`.
    - i16 DOutputs → `Kind: None` (не публикуем сам regis, но из него derived heater_active/damper_open в otherwise. Catalog держит запись для poll, но Hint.Kind=None).
    - i2 State_0, i3 State_1, i4/5/69/70 Error_Code* → `Kind: None` (используются для derived state, не отдельные entities).
    - i0 Firmware → `Kind: Sensor, ObjectID: "firmware", Name: "Firmware", Category: Diagnostic, Icon: "mdi:chip"`.
    - i79 DeviceID → `Kind: Sensor, ObjectID: "device_id", Name: "Device ID", Category: Diagnostic`.
    - h31 Temp_Target → `Kind: None` (виден через climate entity, не отдельный).
    - h32 Fan_Target_1 → `Kind: None` (видим через climate fan_mode).
    - h86 Dev_Keys_2 → `Kind: None`.
    - h2 Power_Dev → `Kind: Switch, ObjectID: "power", Name: "Power", Icon: "mdi:power"`.
    - h0 Type_Dev → `Kind: None`.
    - i6 Last_Time → `Kind: Sensor, ObjectID: "operation_time_left", Name: "Operation time left", Unit: "s", Category: Diagnostic`.
  - **Helpers**:
    - `RegistersByGroup(g PollGroup) []RegisterDef` — фильтрует.
    - `RegisterByAddr(kind RegisterKind, addr uint16) (RegisterDef, bool)`.
    - `RegistersWithEntity() []RegisterDef` — фильтр Kind != EntityKindNone (для discovery).

- [ ] `domain/catalog/registers_test.go` sanity checks:
  - Все ObjectID уникальны (для тех, у кого Kind != None).
  - Все Addr уникальны в рамках своего Kind (нет двух holding с addr=86).
  - Каждый Hint с Kind=Sensor имеет осмысленный ObjectID и Name (non-empty).
  - PollGroupNone есть только у Power_Dev в текущей таблице.

### Tests

- [ ] Все entities — table-driven тесты со 100% coverage паттерна (как в story 002).
- [ ] `OperatingState`: тестировать каждое значение из 0..12 + out-of-range (13, 31, 32, 65535) + bit masking (0x21 → 1, потому что 0x21 & 0x1F = 1).
- [ ] `ErrorCode.DecodeErrors`: специфические битовые паттерны:
  - `(0x0000, 0x0000, 0x0000, 0x0000)` → empty set.
  - `(0x0010, 0, 0, 0)` (бит 4) → 1 entry "filter1 100% clogged" code "E04".
  - `(0x2400, 0, 0, 0)` (биты 10, 13) → 2 entries (FAN1 FAULT, CALORIFIER OVERHEAT).
  - `(0, 0, 0, 0x0040)` → empty set (бит 6 reserved). Важно: подтверждаем что фильтр работает.
  - `(0, 0xFFFF, 0, 0)` → 16 entries (все unknown).
- [ ] `DeviceConfig`: `0x0001` → electric heater, `0x0011` → electric + KKB cooler, `0x0111` → all three, `0x0000` → none.
- [ ] `Snapshot.IsHealthy`: `now.Sub(PolledAt)` < threshold = true; > threshold = false.
- [ ] `RegisterByAddr`/`RegistersByGroup` — табличные тесты на 3-4 keys.
- [ ] `EntityKind/EntityCategory.String()` — всех вариантов включая unknown.

### Quality

- [ ] Coverage ≥ 90% для `domain/entity` и ≥ 80% для `domain/catalog` (catalog — это в основном data, ниже coverage OK).
- [ ] `golangci-lint run ./...` PASS.
- [ ] `gofmt -l domain/` пусто.
- [ ] Все exported идентификаторы — godoc comment одной строкой.

## Constraints

- **Стдлиб + valueobject + testify**. Никаких других зависимостей.
- `domain/entity` импортирует `domain/valueobject`. `domain/catalog` импортирует `domain/valueobject`. **Циклов нет.**
- НЕ создавать функциональные значения (`func` поля) в `RegisterDef`. Decoder логика — в poller usecase (story 008).
- Тесты в отдельных пакетах: `entity_test`, `catalog_test`.
- В `error_code.go` для НЕизвестных битов формат описания: `"undocumented bit %d (Error_Code_%d)"` — единый формат для тестируемости.
- Бит 6 `Error_Code_3` — захардкожен как "reserved" и **исключается** из `DecodeErrors` (отдельная константа, фильтр). Тест на это поведение обязателен.
- `Snapshot` поля экспортированы (struct literal initialization вместо setter'ов). Это сознательно — Snapshot это снимок, не aggregate с инвариантами. Инварианты валидируются на конструировании value objects, не на сборке Snapshot.
- НЕТ generics. НЕТ интерфейсов в этой story (порты — story 004).
- Не использовать `math.MaxUint8` вместо магических чисел в decode — для битов это менее читаемо, оставить hex literals.

---

## Technical Specification

### Analysis

- **OperatingState** uses widening uint16 to int casts only (`OperatingState(bits)` after `bits := raw & 0x1F`); no narrowing, no gosec G115. Out-of-range values map to `OpUnknown = -1`.
- **ErrorBit catalog** is a private package-level slice of `errorBitDef` with two flavours of entries: documented bits (Error_Code i4 only, 15 entries with concrete codes `E00..E15`) and undocumented placeholders (Error_Code_1/2/3, 16 bits each, generic `E1_b%d`/`E2_b%d`/`E3_b%d` codes with description `"undocumented bit %d (Error_Code_%d)"`). Bit 6 of Error_Code_3 is omitted from the catalog entirely; it is a documented reserved bit per quirks doc, so DecodeErrors literally never sees it. A package-level `Error3ReservedBit = uint8(6)` constant is exported for tests.
- **`RegisterDef` deliberately has no `Decoder func` field** because function values are not comparable, not serialisable, and hard to diff in PRs. Decoding lives in poller usecases (story 008). The catalog is pure declarative data.
- **AC deviation: `EntityKindNone = 0`.** AC literally listed `EntityKindNone = 6` last. Idiomatic Go uses zero-value for absent, and roughly half the catalog entries are Kind=None (used for poll-but-don't-publish registers like State_0/State_1). Making None the zero value keeps struct literals minimal, and a struct that omits Kind is a valid no-entity hint by default. Other constants shift to 1..5.
- **`ParseLastTime` exported from entity** because story 008 (poller) needs it, and exporting from `entity` is cleaner than the AC's "local in catalog" suggestion (catalog has no other helpers). `domain/entity/controller.go` is the natural home.
- **Snapshot is a pure DTO** with exported fields, no constructor, and no setters. Validation invariants live in value objects (Temperature, FanSpeed, Mode), so by the time fields are assigned to Snapshot they are already valid; Snapshot is purely a transport shape.
- **Test packages** are external (`entity_test`, `catalog_test`); `t.Parallel()` everywhere; sentinel errors checked via `errors.Is`; `require` for fatal preconditions, `assert` for value comparisons.

### Implementation Order

1. `domain/entity/operating_state.go` + test (no deps)
2. `domain/entity/error_code.go` + test (no deps)
3. `domain/entity/device_config.go` + test (no deps)
4. `domain/entity/controller.go` + test (depends on valueobject + entity sub-types)
5. `domain/catalog/poll_group.go` + test
6. `domain/catalog/entity_hint.go` + test
7. `domain/catalog/registers.go` + test (depends on valueobject + catalog/entity_hint + catalog/poll_group)

---

### 1. Operating state

#### File: `domain/entity/operating_state.go`

```go
// Package entity provides immutable domain entities decoded from Oasis Syberia
// controller register fields. Entities depend only on the Go standard library
// and on github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject.
package entity

// OperatingState represents the current controller operation, decoded from
// bits 0..4 of input register State_1 (i3).
type OperatingState int

const (
	// OpUnknown is the sentinel for an out-of-range operation code.
	OpUnknown OperatingState = -1
	// OpIdle: controller is running steadily (no transition in progress).
	OpIdle OperatingState = 0
	// OpOpenDamper: opening the air damper.
	OpOpenDamper OperatingState = 1
	// OpPreheatCalorifier: pre-heating the calorifier before fan start.
	OpPreheatCalorifier OperatingState = 2
	// OpStartFan: spinning up the supply fan.
	OpStartFan OperatingState = 3
	// OpNorthStart: cold-weather start sequence.
	OpNorthStart OperatingState = 4
	// OpFanCoastdown: fan coast-down after shutdown command.
	OpFanCoastdown OperatingState = 5
	// OpCloseDamper: closing the air damper.
	OpCloseDamper OperatingState = 6
	// OpElectricCalorifierPurge: post-shutdown purge of the electric calorifier.
	OpElectricCalorifierPurge OperatingState = 7
	// OpOpenHotWaterValve: opening the hot-water valve.
	OpOpenHotWaterValve OperatingState = 8
	// OpCloseHotWaterValve: closing the hot-water valve.
	OpCloseHotWaterValve OperatingState = 9
	// OpOpenColdValve: opening the cold-water valve.
	OpOpenColdValve OperatingState = 10
	// OpCloseColdValve: closing the cold-water valve.
	OpCloseColdValve OperatingState = 11
	// OpRotorSpinup: spinning up the recuperator rotor.
	OpRotorSpinup OperatingState = 12
)

// AllOperatingStates lists every defined operating state in numeric order.
// Useful for exhaustive table-driven tests and downstream iteration.
var AllOperatingStates = []OperatingState{
	OpIdle,
	OpOpenDamper,
	OpPreheatCalorifier,
	OpStartFan,
	OpNorthStart,
	OpFanCoastdown,
	OpCloseDamper,
	OpElectricCalorifierPurge,
	OpOpenHotWaterValve,
	OpCloseHotWaterValve,
	OpOpenColdValve,
	OpCloseColdValve,
	OpRotorSpinup,
}

// OperatingStateFromRegister decodes bits 0..4 of a State_1 register read.
// Values 0..12 map to the corresponding OperatingState; anything higher
// (the State_1 bits 0..4 field can technically encode 0..31) yields OpUnknown.
func OperatingStateFromRegister(raw uint16) OperatingState {
	bits := raw & 0x1F
	if bits > 12 {
		return OpUnknown
	}
	return OperatingState(bits)
}

// String returns the canonical snake_case label for the operating state.
func (s OperatingState) String() string {
	switch s {
	case OpIdle:
		return "idle"
	case OpOpenDamper:
		return "open_damper"
	case OpPreheatCalorifier:
		return "preheat_calorifier"
	case OpStartFan:
		return "start_fan"
	case OpNorthStart:
		return "north_start"
	case OpFanCoastdown:
		return "fan_coastdown"
	case OpCloseDamper:
		return "close_damper"
	case OpElectricCalorifierPurge:
		return "electric_calorifier_purge"
	case OpOpenHotWaterValve:
		return "open_hot_water_valve"
	case OpCloseHotWaterValve:
		return "close_hot_water_valve"
	case OpOpenColdValve:
		return "open_cold_valve"
	case OpCloseColdValve:
		return "close_cold_valve"
	case OpRotorSpinup:
		return "rotor_spinup"
	default:
		return "unknown"
	}
}

// IsTransitional reports whether the controller is between steady states.
// OpIdle and OpUnknown are not transitional; everything else is.
func (s OperatingState) IsTransitional() bool {
	return s != OpIdle && s != OpUnknown
}
```

#### File: `domain/entity/operating_state_test.go`

```go
package entity_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
)

func TestOperatingStateFromRegister(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  uint16
		want entity.OperatingState
	}{
		{name: "idle", raw: 0x0000, want: entity.OpIdle},
		{name: "open_damper", raw: 0x0001, want: entity.OpOpenDamper},
		{name: "preheat_calorifier", raw: 0x0002, want: entity.OpPreheatCalorifier},
		{name: "start_fan", raw: 0x0003, want: entity.OpStartFan},
		{name: "north_start", raw: 0x0004, want: entity.OpNorthStart},
		{name: "fan_coastdown", raw: 0x0005, want: entity.OpFanCoastdown},
		{name: "close_damper", raw: 0x0006, want: entity.OpCloseDamper},
		{name: "electric_calorifier_purge", raw: 0x0007, want: entity.OpElectricCalorifierPurge},
		{name: "open_hot_water_valve", raw: 0x0008, want: entity.OpOpenHotWaterValve},
		{name: "close_hot_water_valve", raw: 0x0009, want: entity.OpCloseHotWaterValve},
		{name: "open_cold_valve", raw: 0x000A, want: entity.OpOpenColdValve},
		{name: "close_cold_valve", raw: 0x000B, want: entity.OpCloseColdValve},
		{name: "rotor_spinup", raw: 0x000C, want: entity.OpRotorSpinup},
		{name: "high bits ignored masks to idle", raw: 0xFFE0, want: entity.OpIdle},
		{name: "high bits ignored masks to open_damper", raw: 0x0021, want: entity.OpOpenDamper},
		{name: "13 is unknown", raw: 0x000D, want: entity.OpUnknown},
		{name: "14 is unknown", raw: 0x000E, want: entity.OpUnknown},
		{name: "31 is unknown (mask boundary)", raw: 0x001F, want: entity.OpUnknown},
		{name: "32 masks to idle", raw: 0x0020, want: entity.OpIdle},
		{name: "0xFFFF masks to unknown", raw: 0xFFFF, want: entity.OpUnknown},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, entity.OperatingStateFromRegister(tc.raw))
		})
	}
}

func TestOperatingState_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state entity.OperatingState
		want  string
	}{
		{name: "unknown sentinel", state: entity.OpUnknown, want: "unknown"},
		{name: "idle", state: entity.OpIdle, want: "idle"},
		{name: "open_damper", state: entity.OpOpenDamper, want: "open_damper"},
		{name: "preheat_calorifier", state: entity.OpPreheatCalorifier, want: "preheat_calorifier"},
		{name: "start_fan", state: entity.OpStartFan, want: "start_fan"},
		{name: "north_start", state: entity.OpNorthStart, want: "north_start"},
		{name: "fan_coastdown", state: entity.OpFanCoastdown, want: "fan_coastdown"},
		{name: "close_damper", state: entity.OpCloseDamper, want: "close_damper"},
		{name: "electric_calorifier_purge", state: entity.OpElectricCalorifierPurge, want: "electric_calorifier_purge"},
		{name: "open_hot_water_valve", state: entity.OpOpenHotWaterValve, want: "open_hot_water_valve"},
		{name: "close_hot_water_valve", state: entity.OpCloseHotWaterValve, want: "close_hot_water_valve"},
		{name: "open_cold_valve", state: entity.OpOpenColdValve, want: "open_cold_valve"},
		{name: "close_cold_valve", state: entity.OpCloseColdValve, want: "close_cold_valve"},
		{name: "rotor_spinup", state: entity.OpRotorSpinup, want: "rotor_spinup"},
		{name: "out of range positive", state: entity.OperatingState(99), want: "unknown"},
		{name: "out of range negative", state: entity.OperatingState(-99), want: "unknown"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.state.String())
		})
	}
}

func TestOperatingState_IsTransitional(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		state entity.OperatingState
		want  bool
	}{
		{name: "idle is not transitional", state: entity.OpIdle, want: false},
		{name: "unknown is not transitional", state: entity.OpUnknown, want: false},
		{name: "open_damper is transitional", state: entity.OpOpenDamper, want: true},
		{name: "preheat is transitional", state: entity.OpPreheatCalorifier, want: true},
		{name: "start_fan is transitional", state: entity.OpStartFan, want: true},
		{name: "rotor_spinup is transitional", state: entity.OpRotorSpinup, want: true},
		{name: "fan_coastdown is transitional", state: entity.OpFanCoastdown, want: true},
		{name: "purge is transitional", state: entity.OpElectricCalorifierPurge, want: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.state.IsTransitional())
		})
	}
}

func TestAllOperatingStates_Exhaustive(t *testing.T) {
	t.Parallel()

	// transitionalStates maps each state in AllOperatingStates to its expected
	// IsTransitional result. OpIdle is the only steady state in the slice.
	transitionalStates := map[entity.OperatingState]bool{
		entity.OpIdle:                    false,
		entity.OpOpenDamper:              true,
		entity.OpPreheatCalorifier:       true,
		entity.OpStartFan:                true,
		entity.OpNorthStart:              true,
		entity.OpFanCoastdown:            true,
		entity.OpCloseDamper:             true,
		entity.OpElectricCalorifierPurge: true,
		entity.OpOpenHotWaterValve:       true,
		entity.OpCloseHotWaterValve:      true,
		entity.OpOpenColdValve:           true,
		entity.OpCloseColdValve:          true,
		entity.OpRotorSpinup:             true,
	}

	assert.Equal(t, len(transitionalStates), len(entity.AllOperatingStates),
		"AllOperatingStates length must match the number of documented states")

	for _, s := range entity.AllOperatingStates {
		s := s
		t.Run(s.String(), func(t *testing.T) {
			t.Parallel()
			assert.NotEmpty(t, s.String(), "every state must have a non-empty String()")
			wantTransitional, ok := transitionalStates[s]
			assert.True(t, ok, "state %v is not in the expected map", s)
			assert.Equal(t, wantTransitional, s.IsTransitional())
		})
	}
}
```

---

### 2. Error codes

#### File: `domain/entity/error_code.go`

```go
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
```

#### File: `domain/entity/error_code_test.go`

```go
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
```

---

### 3. Device config

#### File: `domain/entity/device_config.go`

```go
package entity

import "strings"

// HeaterType encodes the heater installed on the unit (Type_Dev bits 0..3).
type HeaterType int

const (
	// HeaterNone means no heater is installed.
	HeaterNone HeaterType = 0
	// HeaterElectric is an electric calorifier.
	HeaterElectric HeaterType = 1
	// HeaterWater is a hot-water calorifier.
	HeaterWater HeaterType = 2
	// HeaterCombined is a combined electric + water calorifier.
	HeaterCombined HeaterType = 3
)

// String returns the canonical lowercase label for the heater type.
func (h HeaterType) String() string {
	switch h {
	case HeaterNone:
		return "none"
	case HeaterElectric:
		return "electric_heater"
	case HeaterWater:
		return "water_heater"
	case HeaterCombined:
		return "combined_heater"
	default:
		return "unknown_heater"
	}
}

// CoolerType encodes the cooler installed on the unit (Type_Dev bits 4..7).
type CoolerType int

const (
	// CoolerNone means no cooler is installed.
	CoolerNone CoolerType = 0
	// CoolerKKB is an outdoor compressor unit (KKB).
	CoolerKKB CoolerType = 1
	// CoolerFancoil is a fancoil cooler.
	CoolerFancoil CoolerType = 2
)

// String returns the canonical lowercase label for the cooler type.
func (c CoolerType) String() string {
	switch c {
	case CoolerNone:
		return "none"
	case CoolerKKB:
		return "kkb_cooler"
	case CoolerFancoil:
		return "fancoil_cooler"
	default:
		return "unknown_cooler"
	}
}

// RecuperatorType encodes the heat recovery installed on the unit (Type_Dev bits 8..11).
type RecuperatorType int

const (
	// RecuperatorNone means no recuperator is installed.
	RecuperatorNone RecuperatorType = 0
	// RecuperatorPlate is a plate-type heat exchanger.
	RecuperatorPlate RecuperatorType = 1
	// RecuperatorRotor is a rotary recuperator.
	RecuperatorRotor RecuperatorType = 2
)

// String returns the canonical lowercase label for the recuperator type.
func (r RecuperatorType) String() string {
	switch r {
	case RecuperatorNone:
		return "none"
	case RecuperatorPlate:
		return "plate_recuperator"
	case RecuperatorRotor:
		return "rotor_recuperator"
	default:
		return "unknown_recuperator"
	}
}

// DeviceConfig is the decoded form of holding register Type_Dev (h0).
type DeviceConfig struct {
	Heater      HeaterType
	Cooler      CoolerType
	Recuperator RecuperatorType
	Raw         uint16
}

// DeviceConfigFromRegister extracts the heater, cooler and recuperator nibbles
// from a Type_Dev register value. Out-of-range nibbles are stored as-is so the
// String() / Has*() methods can still surface them as "unknown" instead of
// silently mapping to None.
func DeviceConfigFromRegister(raw uint16) DeviceConfig {
	heater := raw & 0xF
	cooler := (raw >> 4) & 0xF
	recup := (raw >> 8) & 0xF
	return DeviceConfig{
		Heater:      HeaterType(heater),
		Cooler:      CoolerType(cooler),
		Recuperator: RecuperatorType(recup),
		Raw:         raw,
	}
}

// HasHeater reports whether a heater of any kind is installed.
func (c DeviceConfig) HasHeater() bool { return c.Heater != HeaterNone }

// HasCooler reports whether a cooler of any kind is installed.
func (c DeviceConfig) HasCooler() bool { return c.Cooler != CoolerNone }

// HasRecuperator reports whether a recuperator of any kind is installed.
func (c DeviceConfig) HasRecuperator() bool { return c.Recuperator != RecuperatorNone }

// String renders the config as a comma-separated list of installed components,
// or "none" if all three nibbles are zero.
func (c DeviceConfig) String() string {
	parts := make([]string, 0, 3)
	if c.Heater != HeaterNone {
		parts = append(parts, c.Heater.String())
	}
	if c.Cooler != CoolerNone {
		parts = append(parts, c.Cooler.String())
	}
	if c.Recuperator != RecuperatorNone {
		parts = append(parts, c.Recuperator.String())
	}
	if len(parts) == 0 {
		return "none"
	}
	return strings.Join(parts, ", ")
}
```

#### File: `domain/entity/device_config_test.go`

```go
package entity_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
)

func TestHeaterType_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		h    entity.HeaterType
		want string
	}{
		{name: "none", h: entity.HeaterNone, want: "none"},
		{name: "electric", h: entity.HeaterElectric, want: "electric_heater"},
		{name: "water", h: entity.HeaterWater, want: "water_heater"},
		{name: "combined", h: entity.HeaterCombined, want: "combined_heater"},
		{name: "unknown", h: entity.HeaterType(9), want: "unknown_heater"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.h.String())
		})
	}
}

func TestCoolerType_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		c    entity.CoolerType
		want string
	}{
		{name: "none", c: entity.CoolerNone, want: "none"},
		{name: "kkb", c: entity.CoolerKKB, want: "kkb_cooler"},
		{name: "fancoil", c: entity.CoolerFancoil, want: "fancoil_cooler"},
		{name: "unknown", c: entity.CoolerType(7), want: "unknown_cooler"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.c.String())
		})
	}
}

func TestRecuperatorType_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		r    entity.RecuperatorType
		want string
	}{
		{name: "none", r: entity.RecuperatorNone, want: "none"},
		{name: "plate", r: entity.RecuperatorPlate, want: "plate_recuperator"},
		{name: "rotor", r: entity.RecuperatorRotor, want: "rotor_recuperator"},
		{name: "unknown", r: entity.RecuperatorType(5), want: "unknown_recuperator"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.r.String())
		})
	}
}

func TestDeviceConfigFromRegister(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		raw           uint16
		wantHeater    entity.HeaterType
		wantCooler    entity.CoolerType
		wantRecup     entity.RecuperatorType
		wantHasHeater bool
		wantHasCooler bool
		wantHasRecup  bool
		wantString    string
	}{
		{
			name:       "all zero",
			raw:        0x0000,
			wantString: "none",
		},
		{
			name:          "electric heater only (real hardware)",
			raw:           0x0001,
			wantHeater:    entity.HeaterElectric,
			wantHasHeater: true,
			wantString:    "electric_heater",
		},
		{
			name:          "electric heater plus KKB cooler",
			raw:           0x0011,
			wantHeater:    entity.HeaterElectric,
			wantCooler:    entity.CoolerKKB,
			wantHasHeater: true,
			wantHasCooler: true,
			wantString:    "electric_heater, kkb_cooler",
		},
		{
			name:          "all three installed",
			raw:           0x0111,
			wantHeater:    entity.HeaterElectric,
			wantCooler:    entity.CoolerKKB,
			wantRecup:     entity.RecuperatorPlate,
			wantHasHeater: true,
			wantHasCooler: true,
			wantHasRecup:  true,
			wantString:    "electric_heater, kkb_cooler, plate_recuperator",
		},
		{
			name:          "water heater plus rotor recuperator",
			raw:           0x0202,
			wantHeater:    entity.HeaterWater,
			wantRecup:     entity.RecuperatorRotor,
			wantHasHeater: true,
			wantHasRecup:  true,
			wantString:    "water_heater, rotor_recuperator",
		},
		{
			name:          "out of range nibbles preserved",
			raw:           0x0F77,
			wantHeater:    entity.HeaterType(7),
			wantCooler:    entity.CoolerType(7),
			wantRecup:     entity.RecuperatorType(0xF),
			wantHasHeater: true,
			wantHasCooler: true,
			wantHasRecup:  true,
			wantString:    "unknown_heater, unknown_cooler, unknown_recuperator",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := entity.DeviceConfigFromRegister(tc.raw)
			assert.Equal(t, tc.wantHeater, cfg.Heater)
			assert.Equal(t, tc.wantCooler, cfg.Cooler)
			assert.Equal(t, tc.wantRecup, cfg.Recuperator)
			assert.Equal(t, tc.raw, cfg.Raw)
			assert.Equal(t, tc.wantHasHeater, cfg.HasHeater())
			assert.Equal(t, tc.wantHasCooler, cfg.HasCooler())
			assert.Equal(t, tc.wantHasRecup, cfg.HasRecuperator())
			assert.Equal(t, tc.wantString, cfg.String())
		})
	}
}
```

---

### 4. Controller snapshot

#### File: `domain/entity/controller.go`

```go
package entity

import (
	"time"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"
)

// Snapshot is an immutable single-poll-cycle view of the controller. Fields
// are exported so the poller can populate them with struct literals; the type
// has no constructor because all invariants live in the underlying value
// objects (Mode, Temperature, FanSpeed, FirmwareVersion).
type Snapshot struct {
	Firmware          valueobject.FirmwareVersion
	DeviceID          uint16
	DeviceConfig      DeviceConfig
	PowerOn           bool
	Switching         bool
	HeatCapable       bool
	CoolCapable       bool
	CurrentMode       valueobject.Mode
	Operation         OperatingState
	OperationTimeLeft time.Duration
	TargetTemp        valueobject.Temperature
	RoomTemp          valueobject.Temperature
	SupplyTemp        valueobject.Temperature
	FanTarget1        valueobject.FanSpeed
	FanState1         uint16
	FanState2         uint16
	HeaterPWM         bool
	DamperOpen        bool
	PIDDemand         uint16
	FilterPct         int16
	Errors            ErrorSet
	RoomHumidity      uint16
	PolledAt          time.Time
}

// IsHealthy reports whether the snapshot was polled within the supplied freshness
// window (now - PolledAt <= threshold). Used by the availability manager.
func (s Snapshot) IsHealthy(now time.Time, threshold time.Duration) bool {
	return now.Sub(s.PolledAt) <= threshold
}

// ParseLastTime decodes the Last_Time register (i6): high byte = minutes,
// low byte = seconds remaining for the current operation.
func ParseLastTime(raw uint16) time.Duration {
	mins := (raw >> 8) & 0xFF
	secs := raw & 0xFF
	return time.Duration(mins)*time.Minute + time.Duration(secs)*time.Second
}
```

#### File: `domain/entity/controller_test.go`

```go
package entity_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
)

func TestSnapshot_IsHealthy(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	threshold := 10 * time.Second

	tests := []struct {
		name     string
		polledAt time.Time
		want     bool
	}{
		{name: "fresh poll one second ago", polledAt: now.Add(-1 * time.Second), want: true},
		{name: "exactly at threshold is healthy", polledAt: now.Add(-threshold), want: true},
		{name: "one nanosecond past threshold is stale", polledAt: now.Add(-threshold - 1), want: false},
		{name: "stale poll one minute ago", polledAt: now.Add(-1 * time.Minute), want: false},
		{name: "future poll is healthy", polledAt: now.Add(1 * time.Second), want: true},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			s := entity.Snapshot{PolledAt: tc.polledAt}
			assert.Equal(t, tc.want, s.IsHealthy(now, threshold))
		})
	}
}

func TestParseLastTime(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		raw  uint16
		want time.Duration
	}{
		{name: "zero", raw: 0x0000, want: 0},
		{name: "5 min 48 s", raw: 0x0530, want: 5*time.Minute + 48*time.Second},
		{name: "1 min 0 s", raw: 0x0100, want: 1 * time.Minute},
		{name: "0 min 30 s", raw: 0x001E, want: 30 * time.Second},
		{name: "max value 255 min 255 s", raw: 0xFFFF, want: 255*time.Minute + 255*time.Second},
		{name: "16 min 32 s", raw: 0x1020, want: 16*time.Minute + 32*time.Second},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, entity.ParseLastTime(tc.raw))
		})
	}
}

func TestSnapshot_ZeroValueIsUsable(t *testing.T) {
	t.Parallel()

	// A zero-value Snapshot must be a valid struct literal — no constructor required.
	s := entity.Snapshot{}
	assert.Equal(t, time.Time{}, s.PolledAt)
	assert.Equal(t, entity.OpIdle, s.Operation)
	assert.True(t, s.Errors.IsEmpty())
}
```

---

### 5. Poll group

#### File: `domain/catalog/poll_group.go`

```go
// Package catalog declares the static register catalog and the small types
// (poll groups, entity hints) that decorate each register definition.
package catalog

import "time"

// PollGroup classifies how often a register is read by the poller.
type PollGroup int

const (
	// PollGroupNone marks command-only registers that are never polled.
	PollGroupNone PollGroup = 0
	// PollGroupHot is the 5-second read tier (fast-changing state).
	PollGroupHot PollGroup = 1
	// PollGroupMedium is the 15-second read tier (slowly-changing state).
	PollGroupMedium PollGroup = 2
	// PollGroupSlow is the 60-second read tier (effectively static state).
	PollGroupSlow PollGroup = 3
)

// String returns the canonical lowercase label.
func (g PollGroup) String() string {
	switch g {
	case PollGroupNone:
		return "none"
	case PollGroupHot:
		return "hot"
	case PollGroupMedium:
		return "medium"
	case PollGroupSlow:
		return "slow"
	default:
		return "unknown"
	}
}

// Period returns the polling interval for the group, or 0 for None / Unknown.
func (g PollGroup) Period() time.Duration {
	switch g {
	case PollGroupHot:
		return 5 * time.Second
	case PollGroupMedium:
		return 15 * time.Second
	case PollGroupSlow:
		return 60 * time.Second
	default:
		return 0
	}
}
```

#### File: `domain/catalog/poll_group_test.go`

```go
package catalog_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/catalog"
)

func TestPollGroup_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		group catalog.PollGroup
		want  string
	}{
		{name: "none", group: catalog.PollGroupNone, want: "none"},
		{name: "hot", group: catalog.PollGroupHot, want: "hot"},
		{name: "medium", group: catalog.PollGroupMedium, want: "medium"},
		{name: "slow", group: catalog.PollGroupSlow, want: "slow"},
		{name: "unknown high", group: catalog.PollGroup(99), want: "unknown"},
		{name: "unknown negative", group: catalog.PollGroup(-1), want: "unknown"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.group.String())
		})
	}
}

func TestPollGroup_Period(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		group catalog.PollGroup
		want  time.Duration
	}{
		{name: "none has zero period", group: catalog.PollGroupNone, want: 0},
		{name: "hot is 5 seconds", group: catalog.PollGroupHot, want: 5 * time.Second},
		{name: "medium is 15 seconds", group: catalog.PollGroupMedium, want: 15 * time.Second},
		{name: "slow is 60 seconds", group: catalog.PollGroupSlow, want: 60 * time.Second},
		{name: "unknown has zero period", group: catalog.PollGroup(99), want: 0},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.group.Period())
		})
	}
}
```

---

### 6. Entity hint

#### File: `domain/catalog/entity_hint.go`

```go
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
```

#### File: `domain/catalog/entity_hint_test.go`

```go
package catalog_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/catalog"
)

func TestEntityKind_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		kind catalog.EntityKind
		want string
	}{
		{name: "none zero value", kind: catalog.EntityKindNone, want: "none"},
		{name: "sensor", kind: catalog.EntityKindSensor, want: "sensor"},
		{name: "binary_sensor", kind: catalog.EntityKindBinarySensor, want: "binary_sensor"},
		{name: "switch", kind: catalog.EntityKindSwitch, want: "switch"},
		{name: "climate", kind: catalog.EntityKindClimate, want: "climate"},
		{name: "button", kind: catalog.EntityKindButton, want: "button"},
		{name: "out of range high", kind: catalog.EntityKind(99), want: "unknown"},
		{name: "negative", kind: catalog.EntityKind(-1), want: "unknown"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.kind.String())
		})
	}
}

func TestEntityCategory_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		category catalog.EntityCategory
		want     string
	}{
		{name: "default is empty string", category: catalog.EntityCategoryDefault, want: ""},
		{name: "diagnostic", category: catalog.EntityCategoryDiagnostic, want: "diagnostic"},
		{name: "config", category: catalog.EntityCategoryConfig, want: "config"},
		{name: "out of range", category: catalog.EntityCategory(99), want: "unknown"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.category.String())
		})
	}
}

func TestDeviceClassConstants(t *testing.T) {
	t.Parallel()

	assert.Equal(t, catalog.DeviceClass(""), catalog.DeviceClassNone)
	assert.Equal(t, catalog.DeviceClass("temperature"), catalog.DeviceClassTemperature)
	assert.Equal(t, catalog.DeviceClass("humidity"), catalog.DeviceClassHumidity)
	assert.Equal(t, catalog.DeviceClass("heat"), catalog.DeviceClassHeat)
	assert.Equal(t, catalog.DeviceClass("problem"), catalog.DeviceClassProblem)
	assert.Equal(t, catalog.DeviceClass("running"), catalog.DeviceClassRunning)
	assert.Equal(t, catalog.DeviceClass("power"), catalog.DeviceClassPower)
}

func TestStateClassConstants(t *testing.T) {
	t.Parallel()

	assert.Equal(t, catalog.StateClass(""), catalog.StateClassNone)
	assert.Equal(t, catalog.StateClass("measurement"), catalog.StateClassMeasurement)
}

func TestEntityHint_ZeroValueIsNone(t *testing.T) {
	t.Parallel()

	// A zero-value EntityHint must be valid: Kind defaults to None, all strings empty.
	var h catalog.EntityHint
	assert.Equal(t, catalog.EntityKindNone, h.Kind)
	assert.Equal(t, catalog.EntityCategoryDefault, h.Category)
	assert.Equal(t, "", h.ObjectID)
	assert.Equal(t, "", h.Name)
	assert.Equal(t, catalog.DeviceClassNone, h.DeviceClass)
	assert.Equal(t, catalog.StateClassNone, h.StateClass)
}
```

---

### 7. Registers catalog

#### File: `domain/catalog/registers.go`

```go
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
```

#### File: `domain/catalog/registers_test.go`

```go
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
		{name: "slow", group: catalog.PollGroupSlow, mustHave: []string{"Firmware", "DeviceID", "Type_Dev"}},
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
		"TKomn_x10_P", "Room_Hum_P", "Firmware", "DeviceID", "Last_Time", "Power_Dev"} {
		assert.Truef(t, names[expected], "expected register %s to be returned by RegistersWithEntity", expected)
	}

	// Registers explicitly marked Kind=None must NOT appear.
	for _, excluded := range []string{"State_0", "State_1", "Error_Code", "Error_Code_1",
		"Error_Code_2", "Error_Code_3", "DOutputs", "Type_Dev",
		"Temp_Target", "Fan_Target_1", "Dev_Keys_2"} {
		assert.Falsef(t, names[excluded], "register %s must not be returned by RegistersWithEntity", excluded)
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
    You are an IMPLEMENTATION AGENT for story 003-domain-entities-and-catalog.

    YOUR ONLY JOB: Copy code from the story file to actual files. Run verification.

    CRITICAL RULES (read carefully):
    - Copy code VERBATIM. Do NOT modify, "improve", or fix anything you notice.
    - If lint fails — STOP. Append errors verbatim to "Issues Found", set status to "in_progress". DO NOT invent fixes (a Fix Agent will be launched).
    - Do NOT add `//nolint` directives. Do NOT touch the story.
    - Process every "#### File: `path`" block.

    PROCESS:
    1. Read story: /Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/documentation/stories/003-domain-entities-and-catalog.md
    2. For each "#### File: `path`" section, write code to that path (relative to apps/oasis-modbus2mqtt/)
    3. From work dir, run verification IN ORDER:
       a. gofmt -l domain/   (must be empty)
       b. go build ./...
       c. go vet ./...
       d. go test -short ./...
       e. coverage check:
          - go test -coverpkg=./domain/entity   -coverprofile=/tmp/oasis_entity_cov.out  ./domain/entity/...
          - go test -coverpkg=./domain/catalog  -coverprofile=/tmp/oasis_catalog_cov.out ./domain/catalog/...
          - go tool cover -func=/tmp/oasis_entity_cov.out  | tail -1   (≥90%)
          - go tool cover -func=/tmp/oasis_catalog_cov.out | tail -1   (≥80%)
       f. golangci-lint run ./...   (0 issues)
    4. If ALL pass: set status: review, fill Verification Results, tick Progress, append Files Changed.
    5. If ANY fail: append errors verbatim to Issues Found, status stays in_progress. STOP.

    Work dir: /Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/
```

### Fix Agent Instructions

Standard. Edit story only. Re-launch Implementation Agent after.

---

## Implementation Notes

### Progress

- [ ] `domain/entity/operating_state.go` (+ test)
- [ ] `domain/entity/error_code.go` (+ test)
- [ ] `domain/entity/device_config.go` (+ test)
- [ ] `domain/entity/controller.go` (+ test)
- [ ] `domain/catalog/poll_group.go`
- [ ] `domain/catalog/entity_hint.go` (+ test)
- [ ] `domain/catalog/registers.go` (+ test)
- [ ] gofmt clean
- [ ] go build/vet pass
- [ ] go test pass
- [ ] entity coverage ≥90%
- [ ] catalog coverage ≥80%
- [ ] lint clean

### Verification Results

```
gofmt:                 PASS (0 files needing format)
go build:              PASS
go vet:                PASS
go test:               PASS
domain/entity coverage:  98.9%
domain/catalog coverage: 100.0%
golangci-lint:         FAIL (1 issue: unused allOperatingStates variable — fixed: renamed to AllOperatingStates, used in TestAllOperatingStates_Exhaustive)
```

### Issues Found

(none)

### Fixes Applied

**2026-04-25 — Fix Agent (Sonnet)**

Option chosen: **rename `allOperatingStates` → `AllOperatingStates`** (export the slice).

Rationale: the variable is package-private, so the `entity_test` package cannot reference it — the linter correctly flags it as unused. Exporting the symbol (capital A) makes it visible to external test packages without requiring any `//nolint` directive or test-package gymnastics. It also serves as a useful public API for downstream consumers iterating all states.

Changes made (story `.md` only):
1. `operating_state.go` block: renamed `allOperatingStates` → `AllOperatingStates` and updated the godoc line.
2. `operating_state_test.go` block: added `TestAllOperatingStates_Exhaustive` — a parallel sub-test loop over `entity.AllOperatingStates` that asserts `String() != ""` and `IsTransitional()` matches a per-state expectation map. This satisfies the AC "for exhaustive table-driven tests" intent and resolves the lint error.
3. AC bullet updated to reflect the exported name.
4. `status` set to `ready`, `Issues Found` cleared.

---

## Files Changed

- `/Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/domain/entity/operating_state.go`
- `/Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/domain/entity/operating_state_test.go`
- `/Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/domain/entity/error_code.go`
- `/Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/domain/entity/error_code_test.go`
- `/Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/domain/entity/device_config.go`
- `/Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/domain/entity/device_config_test.go`
- `/Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/domain/entity/controller.go`
- `/Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/domain/entity/controller_test.go`
- `/Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/domain/catalog/poll_group.go`
- `/Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/domain/catalog/poll_group_test.go`
- `/Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/domain/catalog/entity_hint.go`
- `/Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/domain/catalog/entity_hint_test.go`
- `/Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/domain/catalog/registers.go`
- `/Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/domain/catalog/registers_test.go`
