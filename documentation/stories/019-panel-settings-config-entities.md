---
title: "Feature: Panel settings as HA config entities, default_entity_id, stable heater_active"
status: shipped
commit_sha: 254888e (+ 57caff7 lint fix)
release: v0.1.3
deployed: 2026-11-24 (delta, terragrunt oasis-mqtt-bridge)
priority: high
complexity: 8
created: 2026-11-23
updated: 2026-11-23
actual: 1012 prod LOC (.go added), 2339 test LOC (_test.go + tests/), 45 files (39 modified + 6 new), after review round 1
depends_on: 009-apply-command-usecase, 011-discovery-builder, 012-publish-usecases, 013-mqtt-command-subscriber, 018-filter-days-left-and-discovery-race
risk_areas: [bitfield-rmw, write-read-back, poller-merge-race, discovery-entity-ids, slow-tier-poll, user-facing-state-change]
estimate: ~750 prod LOC, ~1000 test LOC, ~32 files
---

## Context

### Part A: panel menu settings are not reachable from HA

The panel's settings menu lives in documented holding registers. They were checked
2026-11-23 against a live dump (`.thoughts/oasis-syberia-modbus-bridge/dumps/2026-11-23-fault16-0-before.json`)
and against the physical panel. See register map §«Пользовательские настройки меню пульта ↔ регистры»
and quirks §4 (RMW) and §13 (write findings). Addresses are 0-based PDU, as in `domain/catalog/registers.go`.

| Register | PDF name | Kind | Live |
|---|---|---|---|
| h22 | `User_Keys_0` | bitfield, 10 entities | `0x07E4` |
| h23 | `Dark_Brightness` | % | 20 |
| h24 | `IR_Remoute_Time` | documented, poll only (span filler) | 3 |
| h25 | `User_Dev_0` | bitfield, 1 entity (bit 2) | bit 2 = 1 |
| h35 | `e_Hum_Target` | % 20..95 | 50 |
| h36 | `e_Room_CO2_Target` | ppm 500..2000 | 500 |

What we know about writes (quirks §13): documented EEPROM registers such as h22 do apply over TCP
(bit 7 was cleared and restored 23.11). The undocumented h346 ACKs both FC06 and FC16 and silently
ignores them. So every write must be **read back**, and the bridge publishes the **actual** value.
h22 and h25 are bitfields the panel can change at any time, so writes are **RMW on a fresh read**.

### Part B: heater_active flaps

`heater_active` and the climate's `hvac_action = heating` come from
`HeaterPWM = (DOutputs i16 & 0x3) > 0` (`poll_controller.go` PollHot, `publish_state.go` heater_active
message and `hvacAction`). The PWM pulses at about 1 Hz (quirks §7) and the hot tier samples every 5 s,
so the binary_sensor flips on almost every poll while heating is steady. Live, `Reg` (i18) stayed at 42
for an hour while heater_active flapped, which floods the HA logbook and recorder. Quirks §6 confirms
`Reg > 0` is the controller's "wants to heat" signal, and that with mode=OFF and power ON, `Reg = 0`.

### Part C: entity ids and version

- HA now prefixes NEW entity ids with the device's area. v0.1.2 created `sensor.koridor_oasis_syberia_filter_days_left`.
  Zigbee2MQTT on the same HA sends `default_entity_id` and HA honours it.
- The climate payload sends `object_id: "climate"`. Verified in HA core `dev` (`components/mqtt/schemas.py`):
  `object_id` is gone from `MQTT_ENTITY_COMMON_SCHEMA` and discovery schemas use `REMOVE_EXTRA`, so it
  is silently dropped. `default_entity_id` (abbreviation `def_ent_id`) is the replacement. In `entity.py`
  `_init_entity_registry`, HA splits it on `.`, warns when the domain differs, and generates the entity id
  from the object part. Existing registry entries keep their ids.
- The startup log prints a hardcoded `version: 0.1.0`.

## Solution

### A1. Catalog: declarative settings table (`domain/catalog/settings.go`, new)

`Registers` stays one row per register. The settings get their own table, which discovery, state
publishing, command subscription and validation all iterate. Adding a setting is one row.

```go
type NumberMode string
const (
    NumberModeBox    NumberMode = "box"
    NumberModeSlider NumberMode = "slider"
)

type SelectOption struct {
    Raw   uint16 // field value after mask/shift
    Label string // HA option string
}

// SettingDef is one panel-menu setting exposed as an HA config entity.
// Every setting is entity_category=config (implicit, not a field).
type SettingDef struct {
    ObjectID    string
    Name        string
    Kind        EntityKind               // EntityKindSwitch | EntityKindSelect | EntityKindNumber
    Addr        valueobject.RegisterAddr // holding register
    Mask        uint16                   // 0xFFFF = whole register (numbers)
    Shift       uint8
    Icon        string
    DeviceClass DeviceClass
    Unit        string
    Min, Max, Step uint16                // numbers only, raw register units
    Mode        NumberMode               // numbers only
    Options     []SelectOption           // selects only
}

var Settings = []SettingDef{ ... }        // rows below, in this order
var ErrInvalidSettingValue = errors.New("invalid setting value")

func SettingByObjectID(id string) (SettingDef, bool)

func (d SettingDef) IsBitfield() bool                         // Mask != 0xFFFF
func (d SettingDef) Field(reg uint16) uint16                  // (reg & Mask) >> Shift
func (d SettingDef) Apply(reg, field uint16) uint16           // (reg &^ Mask) | ((field << Shift) & Mask)
func (d SettingDef) Matches(reg, field uint16) bool           // Field(reg) == field
func (d SettingDef) StatePayload(reg uint16) (payload string, known bool)
func (d SettingDef) ParseCommand(payload string) (field uint16, err error) // err wraps ErrInvalidSettingValue
```

Methods on `SettingDef` are pure value logic. The "no function fields" rule for `RegisterDef` still holds,
because these are methods, not fields. This keeps the value↔payload mapping next to the row it describes.

**Rows** (h22 = `AddrUserKeys0`, h25 = `AddrUserDev0`, …; name the address constants in the catalog):

| object_id | Name | Kind | Reg | Mask / Shift | Extra |
|---|---|---|---|---|---|
| `nord_start` | North start | switch | h22 | `1<<1` / 1 | icon `mdi:snowflake-thermometer` |
| `touch_click` | Touch click sound | switch | h22 | `1<<5` / 5 | `mdi:gesture-tap` |
| `alarm_beep` | Alarm beep | switch | h22 | `1<<6` / 6 | `mdi:volume-high` |
| `stop_on_filter_full` | Stop on filter 100% | switch | h22 | `1<<7` / 7 | `mdi:air-filter` |
| `auto_restart` | Auto restart after power loss | switch | h22 | `1<<8` / 8 | `mdi:restart` |
| `performance_control` | Performance control | switch | h22 | `1<<9` / 9 | `mdi:fan-chevron-down` |
| `dark_screen` | Dark screen | switch | h22 | `1<<10` / 10 | `mdi:brightness-4` |
| `humidifier` | Humidifier | switch | h22 | `1<<12` / 12 | `mdi:air-humidifier` |
| `stop_on_conditioner_alarm` | Stop on conditioner alarm | switch | h22 | `1<<15` / 15 | `mdi:air-conditioner` |
| `control_priority` | Control priority | select | h22 | `0x6000` / 13 | `ControlPriorityOptions`, `mdi:format-list-numbered` |
| `active_filter` | Active filter | switch | h25 | `1<<2` / 2 | `mdi:air-purifier` |
| `dark_brightness` | Dark screen brightness | number | h23 | `0xFFFF` / 0 | 20..100 step 1, `%`, slider, `mdi:brightness-6` |
| `humidity_target` | Humidity target | number | h35 | `0xFFFF` / 0 | 20..95 step 1, `%`, device_class `humidity`, slider |
| `co2_target` | CO2 target | number | h36 | `0xFFFF` / 0 | 500..2000 step 10, `ppm`, device_class `carbon_dioxide`, box |

Bit 9 is the vendor's "auto fan-speed reduction when the setpoint is not reached". h22 bit 2
(remote server access) and bits 0, 3, 4 and 11 (reserved or demo) are deliberately **not** exposed.
The combined h22 mask is therefore `0xF7E2`, and a test pins that value.

**Select labels live in ONE place**, so they are easy to relabel once the panel's "pressure" option is verified:

```go
// ControlPriorityOptions maps h22 bits 13..14. Vendor PDF: 0=temperature, 1=humidity, 2=CO2.
// The panel labels the third option "pressure"; unverified. Raw 3 is never written and renders unknown.
var ControlPriorityOptions = []SelectOption{{0, "temperature"}, {1, "humidity"}, {2, "co2"}}
```

**Number mode**, justified: the two percentage settings use `slider`. Their ranges are short (76 and 81
positions at step 1) and they are tuned by feel. `co2_target` uses `box`: it has 151 positions at step 10,
users type round targets such as 800 or 1000, and a slider makes exact values fiddly.

**`StatePayload` contract**:
- switch: `"ON"` / `"OFF"`, always known.
- select: the option label. A raw value without a label (3) gives `("None", false)`.
- number: the decimal value. A value outside `[Min, Max]` gives `("None", false)`, because HA's MQTT
  number logs an ERROR and drops an out-of-range state on every publish.

**`ParseCommand` contract**. It is the only validation point, and it rejects with `ErrInvalidSettingValue`:
- Leading and trailing whitespace is trimmed first.
- switch: `ON` / `OFF`, case-insensitive. Anything else is rejected.
- select: an exact label from `Options`. Raw digits (`"2"`, `"3"`), unknown labels and `"None"` are rejected. Raw 3 is unreachable.
- number: `strconv.ParseFloat`. The value must be finite and integral (`"50.0"` is fine, `"50.5"` is not),
  within `[Min, Max]`, and on the step grid `(v - Min) % Step == 0`. Reject `NaN`, `Inf`, `""` and non-numeric input.

**Supporting changes**
- `entity_hint.go`: add `EntityKindSelect` ("select") and `EntityKindNumber` ("number") after `EntityKindButton`.
  Do not renumber existing values. Add `DeviceClassCarbonDioxide = "carbon_dioxide"`.
- `registers.go`: add six `PollGroupSlow` holding rows with **no Hint** (`EntityKindNone`):
  `User_Keys_0` h22, `Dark_Brightness` h23, `IR_Remoute_Time` h24, `User_Dev_0` h25,
  `e_Hum_Target` h35 and `e_Room_CO2_Target` h36. Their entities come from `Settings`, so
  `RegistersWithEntity()` is unchanged.

### A2. Domain snapshot (`domain/entity/panel_settings.go`, new; `controller.go`)

```go
// PanelSettings holds the raw panel-menu holding registers read in the slow tier.
type PanelSettings struct {
    UserKeys0, DarkBrightness, IRRemoteTime, UserDev0, HumidityTarget, CO2Target uint16 // h22,h23,h24,h25,h35,h36
}
func (s PanelSettings) Register(addr uint16) (uint16, bool)
func (s PanelSettings) WithRegister(addr, value uint16) (PanelSettings, bool)
```

`Snapshot` gains `Settings PanelSettings` and `SettingsPolledAt time.Time`. The doc comment follows the
filter wording: best-effort, zero until the first successful read, and consumers must not publish before then.

Part B adds this to `Snapshot`:
```go
// HeaterActive reports PID heat demand while powered. HeaterPWM pulses ~1 Hz (quirk §7)
// and stays in the snapshot for diagnostics only; nothing user-facing may toggle on it.
func (s Snapshot) HeaterActive() bool { return s.PowerOn && s.PIDDemand > 0 }
```

### A3. Polling (`application/usecase/poll_controller.go`)

`PollSlow` order: mandatory i0, i79 and h0 (fail-hard, unchanged), then **settings**, then the filter
pair (unchanged). Settings go first because the filter branch returns early on failure.

- `readSettings(ctx) (entity.PanelSettings, bool)` does two span reads, `ReadHolding(22, 4)` and `ReadHolding(35, 2)`.
  It spans documented registers only: h24 is documented, and h26..h34 are not read.
- On an error or a wrong length, it logs one `Warn` (`poll_slow settings read failed`, `register=h22..h25|h35..h36`,
  `error=...`) and returns ok=false. If the first span fails, the second is not read. On failure,
  `Settings` stays zero and `SettingsPolledAt` stays zero, and PollSlow still returns nil with firmware,
  device_id, Type_Dev and the filter values.
- When both spans succeed: set `Settings` and `SettingsPolledAt = clock.Now()`.

### A4. Poller merge and read-back publish (`application/service/poller.go`)

- `defaultMerger.MergeSlow` copies `Settings` and `SettingsPolledAt` only when
  `!partial.SettingsPolledAt.IsZero()`. Otherwise it keeps prev, the same gate as the filter.
- New method on `Poller`:

```go
// MergeSettingRegister merges a freshly read-back settings register into the latest
// snapshot and fans the merged snapshot out to subscribers, exactly like a tier merge.
// It returns false (no merge, no fan-out) before the first successful settings read.
func (p *Poller) MergeSettingRegister(addr, value uint16) bool
```

- **Race guard against in-flight slow polls.** Without one, PollSlow could read h22 before the write and
  merge after the read-back, which would put the old value back for up to 60 s. The fix:
  - `Poller` gets `settingsGen uint64`, guarded by `p.mu`.
  - `MergeSettingRegister` **always** increments `settingsGen` under `p.mu`, including in the not-yet-polled case,
    so a pre-write first read is never published.
  - `doPoll` snapshots `settingsGen` under `p.mu.RLock` **before** calling the tier fn. At merge time, under `p.mu.Lock`,
    if the generation changed it clears `partial.SettingsPolledAt` before `mergeFn`, so the MergeSlow gate keeps the
    read-back value. Hot and medium partials never carry settings, so this is a no-op for them.
    Non-settings slow fields (firmware and so on) still merge.
- Extract the subscriber fan-out from `doPoll` into `p.notify(merged)` and reuse it in `MergeSettingRegister`.
  Subscribers already run concurrently from three tier goroutines, so a fourth caller (the MQTT command handler
  goroutine) adds no new contract.
- The poller does not record poll metrics for `MergeSettingRegister`.

Why this path: the existing `poller.Subscribe → pubState.Apply` subscriber publishes the read-back value at
once with the full retained state set. ApplyCommand needs no publisher dependency, and the merge is serialized
with tier merges under the same `p.mu`.

### A5. Write path (`application/dto`, `application/usecase/apply_command.go`)

**DTO**
```go
// SetSettingCommand carries a raw HA payload for a panel setting; the use case validates it.
type SetSettingCommand struct {
    ObjectID string
    Payload  string
}
func (SetSettingCommand) commandTag() {}
func (c SetSettingCommand) String() string // "SetSetting(alarm_beep=OFF)"; payload truncated to 32 bytes
```
`errors.go` adds `ErrWriteNotApplied = errors.New("controller acknowledged write but did not apply it")`.
Update `TestErrors_Distinct`.

**Use case interfaces**
- `CommandDispatcher` gains `ReadHolding(ctx, start, count uint16) ([]uint16, error)`. `service.CommandDispatcher` already has it.
- Rename `SnapshotProvider` to `SnapshotStore` and add `MergeSettingRegister(addr, value uint16) bool`.
  `*service.Poller` satisfies it. Making it part of the required constructor argument means the immediate
  publish cannot be forgotten in wiring. The `NewApplyCommand` signature is unchanged.
- New port-style interface, with no infrastructure import in `usecase`:
  ```go
  // WriteNotAppliedRecorder counts writes the controller ACKed but did not apply.
  type WriteNotAppliedRecorder interface { WriteNotApplied(objectID string) }
  func (a *ApplyCommand) WithWriteNotAppliedRecorder(r WriteNotAppliedRecorder) *ApplyCommand // fluent, like WithPowerOffPause
  ```
  The default is a no-op recorder. `app.go` wires `metrics.WriteNotAppliedRecorder{}`.
- `ApplyCommand` gains `settingsMu sync.Mutex`.

**`setSetting(ctx, cmd dto.SetSettingCommand) error`**, added to the `Apply` type switch:
1. `catalog.SettingByObjectID`. Unknown id → `fmt.Errorf("unknown setting %q: %w", id, dto.ErrInvalidPayload)`.
2. `def.ParseCommand(cmd.Payload)`. On error → wrap with `dto.ErrInvalidPayload`. **No Modbus traffic before this point.**
3. `a.transitionGuard()`. **Decision: settings writes are guarded**, like every write except the power-on
   escape hatch. The controller may refuse TCP during transitions (quirks §3). These settings are never
   urgent, and rejecting with `ErrTransitionInProgress` is clearer than a half-applied RMW.
4. `a.settingsMu.Lock()` over the write, the read-back and the merge. This serializes settings commands
   end to end, so a second toggle of the same bit cannot interleave between the first toggle's write and its
   read-back and cause a false mismatch. Lost updates between different bits are already prevented by
   `ModifyHolding`, which holds the client mutex across the read and the write.
5. Write:
   - bitfield → `a.dispatcher.ModifyHolding(ctx, addr, func(cur uint16) uint16 { return def.Apply(cur, field) })`.
     This is the same path as h86 `setMode`: a fresh read under the modbus client mutex, never the cached snapshot.
   - number → `a.dispatcher.ModifyHolding(ctx, addr, func(uint16) uint16 { return field })` (review round 1, L4:
     a fresh read, and no write when the register already holds the target; was `WriteHolding`).
   - Errors wrap as `set setting %s write h%d: %w`.
6. Read-back: `a.dispatcher.ReadHolding(ctx, addr, 1)`. On an error or a wrong length, return a wrapped error
   (`read-back h%d`). Nothing is merged; the next slow poll corrects the state.
7. `a.store.MergeSettingRegister(addr, actual)`. This publishes the **actual** register value whether or
   not it matches. If it returns false, log at Debug ("settings not yet polled").
8. `if !def.Matches(actual, field)`: log `Warn` "setting write not applied" with `object_id`, `register=h%d`,
   `requested=<field>`, `actual=<Field(actual)>` and `raw=0x%04X`. Then call `a.recorder.WriteNotApplied(def.ObjectID)`
   and return `fmt.Errorf("setting %s h%d: requested %d, read back %d: %w", ..., dto.ErrWriteNotApplied)`.
   For a bitfield only the target bits are compared; other bits may legitimately differ.
9. On success, log `Info` "setting applied" with `object_id` and `value`.

Out of scope: read-back for the existing h31, h32 and h86 writes. Noted as a follow-up.

### A6. State publishing (`application/usecase/publish_state.go`)

- `buildMessages` stays pure. When `!snap.SettingsPolledAt.IsZero()`, it appends one message per
  `catalog.Settings` row, in table order, after the filter pair: `topic = State(def.ObjectID)`,
  `payload = def.StatePayload(raw)`. It also returns the list of unknown `(objectID, raw)` pairs.
- `PublishState` gains `mu sync.Mutex` and `unknownWarned map[string]uint16`. `Apply` logs
  `Warn` "setting value has no label" (`object_id`, `raw`) **once per distinct raw value**. When the value
  becomes known again it clears the entry. Without this, the warning would repeat every 5 s from the hot tier.
- **Select or number unknown → publish `"None"`.** Verified in HA core `dev`: `mqtt/select.py`
  `_message_received` sets `_attr_current_option = None` when `payload.lower() == "none"`. `mqtt/number.py`
  treats `payload_reset`, which defaults to `"None"`, as `None`. Both render as `unknown`, so nothing is invented,
  no HA error is logged, and the retained topic does not keep a stale label.
- Message counts: 19 (nothing best-effort polled), 21 (filter only), 33 (settings only), 35 (both).
  Update the doc comments that say "up to 21".
- Part B: the `heater_active` payload becomes `boolToOnOff(snap.HeaterActive())`, and `hvacAction` uses
  `snap.HeaterActive()` in place of `snap.HeaterPWM`. The priority order is unchanged: off → preheating →
  fan transitions → idle transitions → heating → fan → idle. No dedupe layer is added. HA records only
  actual state changes, and the payload is now stable.

### A7. Discovery (`infrastructure/discovery/{builder.go,payloads.go}`)

- `baseEntity`: **remove** `ObjectID` (`object_id`) and **add** `DefaultEntityID string \`json:"default_entity_id"\``.
  The value is always set.
- `baseFor(component, objectID, name, hint, firmware)` gains the component and sets
  `DefaultEntityID = component + "." + DevicePrefix + "_" + objectID`. All 32 payloads get it.
- **Climate decision.** The climate gets `default_entity_id = "climate." + DevicePrefix`, i.e. `climate.oasis_syberia`,
  not `climate.oasis_syberia_climate`. That matches the id the README documents and avoids the stutter.
  `object_id: "climate"` is dropped: current HA strips it (REMOVE_EXTRA) and `default_entity_id` replaces it.
  The live registry id `climate.oasis_syberia_mqtt_hvac` does not change.
- New payload types:
  ```go
  type SelectPayload struct { baseEntity; StateTopic, CommandTopic string; Options []string `json:"options"` }
  type NumberPayload struct {
      baseEntity
      StateTopic        string  `json:"state_topic"`
      CommandTopic      string  `json:"command_topic"`
      Min               float64 `json:"min"`  // no omitempty
      Max               float64 `json:"max"`
      Step              float64 `json:"step"`
      Mode              string  `json:"mode,omitempty"`
      UnitOfMeasurement string  `json:"unit_of_measurement,omitempty"`
      DeviceClass       string  `json:"device_class,omitempty"`
  }
  ```
  Settings switches reuse `SwitchPayload` (`payload_on: ON`, `payload_off: OFF`).
- `Build` iterates `catalog.Settings` after the existing entities, with every payload `entity_category: config`.
  Discovery topics are `homeassistant/{switch|select|number}/<prefix>/<object_id>/config`, and command topics
  use `topics.Command(def.ObjectID)`. Add `EntityKindSelect` and `EntityKindNumber` to the skip case of the
  catalog-kind switch. The initial capacity becomes 32.
- Total configs: 18 → **32**. The 14 new ones are 10 setting switches (9 from h22, plus `active_filter` from h25),
  1 select and 3 numbers.

### A8. Command subscription (`interface/mqtt/command_subscriber.go`)

- `Start` appends one subscription per `catalog.Settings` row, on `<prefix>/cmd/<object_id>`, with handler
  `c.settingHandler(def.ObjectID)`. That is 4 + 14 = 18 topics.
- The handler queues `dto.SetSettingCommand{ObjectID: id, Payload: string(payload)}` for the command worker, which
  applies it under its own `handlerTimeout` ctx (review round 1, H1: handlers no longer apply on the paho router).
  **It does not parse or validate**; the use case owns that.
- **Resubscribe, verified in the current code:** `infrastructure/mqtt.Client.Subscribe` stores every handler in
  `c.subs` under `c.mu`, whether or not the client is connected. `onConnect`, which runs on every paho (re)connect,
  copies `c.subs` and re-issues `client.Subscribe` for each topic before the on-connect hooks run. The session is
  `SetCleanSession(true)`, so this is the only restore path, and it covers the new topics automatically.
  `app.Run` calls `cmdSub.Start` once after `mqttClient.Connect`. No change is needed. The e2e reconnect test
  (A10) asserts that a setting command works after a forced session takeover.

### A9. Metrics and wiring

- `infrastructure/metrics`:
  ```go
  // WriteNotAppliedTotal counts Modbus writes the controller ACKed but did not apply (read-back mismatch).
  func WriteNotAppliedTotal(entity string) *vm.Counter // oasis_modbus_write_not_applied_total{entity="..."}
  type WriteNotAppliedRecorder struct{}
  func (WriteNotAppliedRecorder) WriteNotApplied(objectID string) { WriteNotAppliedTotal(objectID).Inc() }
  ```
- `internal/app/app.go`: `usecase.NewApplyCommand(dispatcher, poller, logger).WithWriteNotAppliedRecorder(metrics.WriteNotAppliedRecorder{})`.
  Update the `republishTimeout` comment to 32 discovery + 35 state messages; the 10 s value stays.

### C. Version injection (trivial, included)

- `cmd/server/main.go`: `var version = "dev"`, and log `"version", version`.
- `Dockerfile` builder stage: `ARG VERSION=dev`, and `-ldflags="-w -s -X main.version=${VERSION}"`.
- `.github/workflows/ci.yml` docker job: add a step `id: ver` that writes `version=${GITHUB_REF_NAME}` for tag
  refs and `version=sha-${GITHUB_SHA::7}` otherwise. Pass `build-args: VERSION=${{ steps.ver.outputs.version }}` to
  `docker/build-push-action`. Do not use `steps.meta.outputs.version`: on main it resolves to `latest`, which says nothing.
- `Makefile` `build`: `-ldflags "-X main.version=$$(git describe --tags --always --dirty)"`.

## Files touched

| File | Change |
|---|---|
| `domain/catalog/settings.go` (new) | `SettingDef`, `Settings`, `ControlPriorityOptions`, methods, `ErrInvalidSettingValue` |
| `domain/catalog/settings_test.go` (new) | table invariants and method tests |
| `domain/catalog/entity_hint.go` (+ test) | `EntityKindSelect`, `EntityKindNumber`, `DeviceClassCarbonDioxide` |
| `domain/catalog/registers.go` (+ test) | 6 slow holding rows, no hint |
| `domain/entity/panel_settings.go` (+ test, new) | `PanelSettings` |
| `domain/entity/controller.go` (+ test) | `Settings`, `SettingsPolledAt`, `HeaterActive()`, HeaterPWM doc |
| `application/dto/command.go`, `errors.go` (+ tests) | `SetSettingCommand`, `ErrWriteNotApplied` |
| `application/usecase/apply_command.go` (+ test) | `setSetting`, `SnapshotStore`, `ReadHolding`, recorder, mutex |
| `application/usecase/poll_controller.go` (+ test) | `readSettings` in PollSlow |
| `application/usecase/publish_state.go` (+ test) | settings messages, warn-once, `HeaterActive` |
| `application/service/poller.go` (+ test) | MergeSlow gate, `MergeSettingRegister`, `settingsGen`, `notify` |
| `infrastructure/discovery/builder.go`, `payloads.go` (+ both tests) | `default_entity_id`, select/number/settings switches |
| `infrastructure/metrics/metrics.go` (+ test) | `WriteNotAppliedTotal`, recorder |
| `interface/mqtt/command_subscriber.go` (+ test) | settings subscriptions |
| `internal/app/app.go` | recorder wiring, comment |
| `cmd/server/main.go`, `Dockerfile`, `.github/workflows/ci.yml`, `Makefile` | version |
| `tests/e2e/full_loop_test.go` (+ `helpers.go` if needed) | preset, counts, round trips |
| `README.md` | entity tables, heater_active note, metric, counts |

That is about 32 files, about 750 prod LOC and about 1000 test LOC. It fits one story. If the lead wants a smaller
diff, the clean split is 019a = Part B + `default_entity_id` + version (about 120 prod LOC) and 019b = settings.

## Test list (required)

**catalog** (`settings_test.go`)
- `TestSettings_ObjectIDsUnique`: unique across `Settings`, the `Registers` hints and the derived discovery ids (`heater_active`, `damper_open`, `problem`, `current_operation`, `climate`, `hvac_mode`, `target_temperature`, `fan_target`, `hvac_action`).
- `TestSettings_RegistersAreCatalogedSlowHolding`: each `Addr` is a `RegisterKindHolding` row in `Registers` with `PollGroupSlow`.
- `TestSettings_MasksDoNotOverlapPerRegister`, and `TestSettings_UserKeys0CombinedMask` == `0xF7E2` (bit 2 and bits 0, 3, 4, 11 never exposed).
- `TestSettings_SwitchMasksSingleBit`, with Shift == bit index.
- `TestSettings_NumberInvariants`: Mask 0xFFFF, Step > 0, Min ≤ Max, (Max-Min)%Step == 0, Mode set.
- `TestSettings_SelectOptionsFitMaskAndNeverRaw3`.
- `TestSettingDef_ApplyPreservesOtherBits`: on `0x07E4`, alarm_beep OFF → `0x07A4`; control_priority co2 → `0x47E4`; nord_start ON → `0x07E6`; on h25 `0x00A5`, active_filter OFF → `0x00A1`.
- `TestSettingDef_StatePayload`: live `0x07E4` decodes to the README values (nord_start OFF, touch_click ON, …, control_priority `temperature`); raw priority 3 → `("None", false)`; number 19 or 96 → `("None", false)`.
- `TestSettingDef_ParseCommand` (table): accepts `ON`, `off`, ` ON `, `co2`, `50`, `50.0`, `800`, `2000`. Rejects with `ErrInvalidSettingValue`: `maybe`, `""`, `pressure`, `2`, `3`, `None`, `abc`, `NaN`, `Inf`, `19`, `96`, `50.5`, `505` (co2 off step), `-1`, `1e9`.

**entity**: `TestPanelSettings_RegisterRoundTrip` (all six addresses, unknown addr → false); `TestSnapshot_HeaterActive` (power off, Reg=0, Reg>0 with PWM in either state).

**usecase / PollSlow**
- `TestPollSlow_SettingsSpans`: asserts `ReadHolding(22,4)` and `ReadHolding(35,2)` calls and the decoded fields, with `SettingsPolledAt` set.
- `TestPollSlow_SettingsReadBestEffort` (table: error or short read on each span): no error; firmware, device_id and filter still populated; `SettingsPolledAt` zero; one Warn naming the span.
- Existing PollSlow tests: add fake responses for 22 and 35 where a full snapshot is asserted.

**service / Poller**
- `TestMergeSlow_SettingsGatedOnSettingsPolledAt`.
- `TestMergeSettingRegister_MergesAndFansOut`: after a slow poll with settings, a subscriber receives a snapshot with the new h22, and the method returns true.
- `TestMergeSettingRegister_BeforeFirstRead_NoMergeNoFanOut`.
- `TestPoller_InFlightSlowPollDoesNotRevertReadBack`: a fake `PollSlow` blocks after "reading" the old h22; `MergeSettingRegister(new)` runs meanwhile; on release, latest holds the new h22 **and** the slow poll's firmware. Run under `-race`.

**usecase / ApplyCommand**. Upgrade the fake dispatcher to hold a register map: `ModifyHolding` applies `modify` to the stored value under its mutex, `WriteHolding` stores, and `ReadHolding` returns the stored value. An `ignoreWrites map[uint16]bool` simulates ACK-and-ignore, and per-op error injection is supported. The fake store records `MergeSettingRegister` calls.
- `TestSetSetting_RMWUsesFreshRegisterNotSnapshot`: the snapshot says h22=`0x0000` and the fake register holds `0x07E4`; alarm_beep OFF writes `0x07A4`.
- `TestSetSetting_BitfieldPreservesOtherBits` (table: the four Apply cases above, through `Apply`).
- `TestSetSetting_NumberUsesModifyHoldingAndReadsBack` (renamed in review round 1): humidity_target `"55.0"` → ModifyHolding h35=55, read back, merge(35,55).
- `TestSetSetting_ReadBackMismatch`: `ignoreWrites[36]` → `errors.Is(err, dto.ErrWriteNotApplied)`; a Warn log containing `object_id=co2_target`; the recorder called once with `co2_target`; merge called with the **actual** old value 500, never 800.
- `TestSetSetting_BitfieldMismatchComparesOnlyTargetBits`: the fake flips bit 5 between write and read-back; alarm_beep still succeeds and merges the actual raw value.
- `TestSetSetting_ReadBackError_NoMergeNoRecorder`.
- `TestSetSetting_ValidationRejectsBeforeModbus` (table: unknown id, the bad payloads above per kind): `errors.Is(err, dto.ErrInvalidPayload)`, zero dispatcher calls.
- `TestSetSetting_TransitionGuard`: `OpPreheatCalorifier` → `ErrTransitionInProgress`, zero dispatcher calls.
- `TestSetSetting_ConcurrentTogglesSerialized`: 2×20 goroutines toggle `touch_click` and `alarm_beep`. Both final bits equal the last value applied per bit, other bits are untouched, and no `ErrWriteNotApplied` occurs. Run under `-race`.

**usecase / PublishState**
- The count tests move to 19/21/33/35 (`TestApply_SettingsTopicsGatedOnSettingsPolledAt`).
- `TestApply_SettingsPayloads`: from `0x07E4` + h23=20 + h25 bit 2 + h35=50 + h36=500.
- `TestApply_SelectUnknownPublishesNoneAndWarnsOnce`: raw 3 across two `Apply` calls gives payload `None` both times and exactly one Warn; changing back to 0 and then to 3 warns again.
- `TestApply_NumberOutOfRangePublishesNone`.
- **Part B**: `TestHeaterActive_StableAcrossPWMToggle` runs three Applies with `PIDDemand=42` and `HeaterPWM` true/false/true; `heater_active` stays `ON` and `hvac_action` stays `heating` for all three. `TestHeaterActive_RegZeroIsOff`: `PIDDemand=0` with `HeaterPWM=true` gives `OFF`, and `hvac_action` falls through to `fan` (FanState1>0) or `idle`. `TestHeaterActive_PowerOff`: `PowerOn=false` with `PIDDemand=42` gives `OFF`, and `hvac_action` is `off`.
- Existing hvacAction tests must set `PIDDemand` explicitly. `TestHvacAction_HeaterPWM` becomes `TestHvacAction_HeatDemand`, and the FanRunning and Idle cases need `PIDDemand=0`. Check that the base fixture (≈L101) still expects `heating` because of its `PIDDemand`.

**discovery**
- `TestBuild_Count` = 32. The topic regex adds `select|number`.
- `TestBuild_DefaultEntityIDOnEveryPayload`: every payload's `default_entity_id` == `<component>.oasis_syberia_<object_id>`, except the climate == `climate.oasis_syberia`. **No payload contains the key `object_id`**.
- `TestBuild_SettingSwitch_AlarmBeep`, `TestBuild_ActiveFilterSwitch`, `TestBuild_ControlPrioritySelect` (options exactly `["temperature","humidity","co2"]`), `TestBuild_SettingNumbers` (min/max/step/mode/unit/device_class for all three). Each also checks `entity_category: config`, state and command topics, and availability.
- Update `payloads_test.go` / `builder_test.go` for the removed `object_id` (L88, L150, L187, L205, L253).

**interface/mqtt**: `Start` subscribes 18 topics (it was 4). `TestSettingHandler_ForwardsRawPayload`: `"50.0"` and `"garbage"` are both forwarded verbatim as `SetSettingCommand`, because the subscriber does not validate.

**metrics**: `TestWriteNotAppliedTotal_Name`, and the recorder increments.

**e2e** (`tests/e2e/full_loop_test.go`, mbserver + mosquitto)
- Preset h22=`0x07E4`, h23=20, h24=3, h25=`0x0004`, h35=50, h36=500.
- Discovery count 32. State `alarm_beep=ON`, `control_priority=temperature`, `humidity_target=50`.
- **Switch round trip**: `cmd/alarm_beep OFF` → `HoldingRegisters[22] == 0x07A4`, so bit 2 is preserved, and state `alarm_beep=OFF`.
- **Select round trip**: `cmd/control_priority co2` → h22 == `0x47A4`, and state `co2`.
- **Number round trip**: `cmd/humidity_target 55.0` → h35 == 55, and state `55`.
- **Rejects**: `cmd/humidity_target 57.5`, then `200`, then `cmd/control_priority 3` → h35 stays 55 and h22 stays `0x47A4` (`assert.Never` ~1 s).
- In `TestDiscoveryRepublishedOnMQTTReconnect`, after the session kick, `cmd/touch_click OFF` still changes h22, which proves the resubscribe.
- SHOULD, if mbserver allows it cheaply: register an FC06 handler that ACKs without storing for addr 36. Then `cmd/co2_target 800` leaves state `500`, and `/metrics` shows `oasis_modbus_write_not_applied_total{entity="co2_target"} 1`.

## Acceptance Criteria

- [x] 14 new config entities (10 switches, 1 select, 3 numbers), all `entity_category: config`, with discovery topics under `homeassistant/{switch,select,number}/<prefix>/<object_id>/config`. h22 bit 2 and bits 0, 3, 4 and 11 are not exposed.
- [x] Every discovery payload (32) carries `default_entity_id`. None carries `object_id`. The climate is `climate.<prefix>`.
- [x] Bitfield writes are RMW through `ModifyHolding` on a fresh controller read. Other bits are preserved, and the snapshot is never used as the base.
- [x] Every settings write is read back, and the **actual** value is merged into the snapshot and published at once through the poller subscribers. A mismatch logs Warn, increments `oasis_modbus_write_not_applied_total{entity}` and returns `dto.ErrWriteNotApplied`. The requested value is never published optimistically.
- [x] An in-flight slow poll cannot revert a read-back value (generation guard).
- [x] Invalid payloads (range, step, non-integral, non-numeric, unknown option, raw digits, unknown id) are rejected in the use case with `dto.ErrInvalidPayload` before any Modbus I/O. The subscriber does not validate.
- [x] Settings writes respect the transition guard.
- [x] Settings are read in the slow tier as two spans (h22..h25, h35..h36), best-effort and gated by `SettingsPolledAt`. No settings topic is published before the first successful read.
- [x] Raw priority 3, or an out-of-range number, publishes `None` (HA `unknown`) and warns once per distinct value.
- [x] Setting command topics are restored on MQTT reconnect (e2e).
- [x] `heater_active` and `hvac_action=heating` derive from `PowerOn && Reg > 0`, so a toggling PWM with steady Reg gives a steady payload.
- [ ] The startup log shows the injected version (`sha-<7>` on main builds, the tag on tag builds, `dev` locally). *Local Docker verified (`--build-arg VERSION=v0.0.0-test` → `"version":"v0.0.0-test"`); the CI `sha-<7>` / tag values are only verifiable after the first CI build and live pod log.*
- [x] The README entity table lists all 14 settings with their register and bit. The heater_active row says "PID heat demand > 0 while powered". The discovery count (32) and the new metric are documented.

## Verification

CI parity. Run from `apps/oasis-modbus2mqtt`. **Never connect to the real controller 10.90.19.7**: it accepts
exactly one TCP connection, and the live bridge holds it. All local testing uses mbserver and testcontainers mosquitto.

```bash
cd /Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt
gofmt -w . && gofmt -l .                 # must print nothing
go build ./... && go vet ./...
golangci-lint run ./...                  # 0 issues
PKGS=$(go list ./... | grep -v '/cmd/')
go test -short -race -timeout 5m -coverprofile=/tmp/cov019.out $PKGS
go tool cover -func=/tmp/cov019.out | tail -1          # total >= 75%
go test -timeout 10m $PKGS                              # integration + e2e (Docker)
go test -race -count=10 -run 'MergeSettingRegister|InFlightSlowPoll|ConcurrentToggles' ./application/...
go test -count=5 ./tests/e2e/
docker build --build-arg VERSION=sha-test -t oasis-019 . && \
  docker run --rm -e MODBUS_HOST=127.0.0.1 -e MQTT_BROKER=127.0.0.1:1 oasis-019 2>&1 | head -3   # first line shows "version":"sha-test"
```

Mutation checks the reviewer runs once, by hand:
- Use the snapshot value instead of `cur` in the RMW closure → `TestSetSetting_RMWUsesFreshRegisterNotSnapshot` fails.
- Publish the requested value instead of the read-back → `TestSetSetting_ReadBackMismatch` fails.
- Remove the generation check in `doPoll` → `TestPoller_InFlightSlowPollDoesNotRevertReadBack` fails.
- Revert `hvacAction` to `HeaterPWM` → `TestHeaterActive_StableAcrossPWMToggle` fails.

Live verification after deploy (main's step, via MQTT/HA only, never Modbus):
1. Pod log: one `discovery published count=32`, then `state published count=35` after the first slow poll (≤60 s). No `poll_slow settings read failed`. The startup line shows `version` = `sha-<7>`.
2. HA (MCP `ha_get_state`): `switch.oasis_syberia_alarm_beep` = on, `switch.oasis_syberia_nord_start` = off, `switch.oasis_syberia_active_filter` = on, `select.oasis_syberia_control_priority` = temperature, `number.oasis_syberia_humidity_target` = 50, `number.oasis_syberia_co2_target` = 500, `number.oasis_syberia_dark_brightness` = 20. All are config category and **without** an area prefix in the entity id.
3. Round trip on a harmless setting: turn `switch.oasis_syberia_touch_click` off. Within 2 s the pod log shows `setting applied object_id=touch_click value=0` and HA shows off. Turn it back on and confirm. The operator checks the panel menu. `oasis_modbus_write_not_applied_total` stays absent or 0.
4. heater_active: while heating with steady `sensor.oasis_syberia_heat_demand` > 0, `binary_sensor.oasis_syberia_heater_active` history over 30 min shows **no** on/off flapping (≤ 2 state changes).
5. Negative: from HA developer tools, publish `oasis_syberia/cmd/humidity_target` = `57.5`. The pod log shows `command failed … invalid command payload`, and HA stays at 50.

## Deviations

Lead decisions applied: climate `default_entity_id = climate.<device_prefix>` and `object_id` removed; no `"name": null` on the climate; `control_priority` labels `temperature`/`humidity`/`co2` in one map; no read-back for h31/h32/h86.

- `MergeSettingRegister` also returns false (no merge, no fan-out) for an address that is not a settings register. It still bumps `settingsGen`. Defensive; `ApplyCommand` only passes catalog addresses.
- A read-back of the wrong length wraps `usecase.ErrUnexpectedReadLength` (the existing sentinel) under `set setting %s read-back h%d`.
- `ParseCommand` errors are wrapped as `fmt.Errorf("%w: %w", dto.ErrInvalidPayload, err)`, so both `dto.ErrInvalidPayload` and `catalog.ErrInvalidSettingValue` match and the log reads `invalid command payload: <id>: ...`.
- `entity.PanelSettings` maps addresses through the `catalog.Addr*` constants (domain/entity imports domain/catalog; there is no reverse import). That keeps one source of truth for the addresses.
- `application/service/export_test.go` gains `PollSlowOnceForTest`, a synchronous slow poll-and-merge. The generation-guard tests use it to interleave a blocked slow poll with `MergeSettingRegister` deterministically, with no timing dependence.
- The SHOULD e2e (ACK-and-ignore on h36) is its own test, `TestE2E_SettingWriteNotApplied`. It reads the counter in-process with `metrics.WriteNotAppliedTotal("co2_target").Get()` rather than over `/metrics`. The e2e config binds HTTP to port 0, so there is no fixed scrape address, and the counter is the same process-global object `/metrics` renders.
- The ApplyCommand fake dispatcher tracks writes that have no read-back yet (`maxPending`). `TestSetSetting_ConcurrentTogglesSerialized` asserts it never exceeds 1, which proves `settingsMu` serializes write+read-back end to end. It works out "the last value applied per bit" by decoding each recorded RMW closure.
- `TestEntityKind_ExistingValuesNotRenumbered` pins Button=5, Select=6, Number=7.
- The README config table row for `HA_DEVICE_PREFIX` no longer says "`object_id` namespace".

## Review round 1 fixes

All on the same uncommitted tree; status stays `review`.

**H1 (HIGH): a burst of setting commands froze all MQTT publishing.** paho v1.5.1 runs handlers inline on its
router goroutine (`SetOrderMatters` is left at its default), and the same goroutine processes PUBACKs. Each setting
command ended in `PublishState.Apply` (35 retained QoS 1 publishes, each waiting for its PUBACK), so with 4+ queued
commands every publish in the process timed out at 5 s.
- `interface/mqtt/command_subscriber.go`: `Start(ctx)` launches ONE worker goroutine (guarded by `sync.Once`; a
  second `Start` returns an error) that drains a buffered FIFO of `commandQueueSize = 32` jobs. Handlers only parse
  and enqueue without blocking. A full queue drops the command with WARN `command queue full, command dropped`
  (`topic`, `capacity`); a message arriving after the worker exited is dropped with WARN
  `command dropped: subscriber stopped`. Each job gets its own `handlerTimeout` context derived from the Start ctx,
  so shutdown cancels an in-flight command. A multi-command message (hvac_mode heat / fan_only) is one job, so the
  short-circuit on the first failure is unchanged. `Done()` reports worker exit. `SetOrderMatters(false)` was not used.
- `application/service/ha_status_listener.go`: the HA-birth republish runs on its own goroutine. At most one run is in
  flight; any number of births during a run coalesce into exactly one more run. The callback ctx derives from the
  Start ctx with the existing 10 s timeout. `Idle()` returns a channel closed when no run is in flight.
- `internal/app/app.go`: `shutdown` waits (bounded by `mqttWorkersShutdownTimeout = 2s`) for `cmdSub.Done()` and
  `haStatus.Idle()` before the MQTT disconnect. `Run` now calls `cancel()` before `shutdown()`, so the HTTP-failure
  exit path also stops the tier goroutines promptly (the shutdown doc already claimed this).
- Tests: `TestHandlers_ReturnImmediatelyWhileApplierBlocks`, `TestWorker_AppliesInArrivalOrder`,
  `TestQueueFull_DropsWithWarn`, `TestWorker_JobContextHasDeadlineAndIsCancelledByStartCtx`,
  `TestEnqueue_AfterStop_Drops`, `TestStart_Twice_ReturnsError`, `TestHandle_DoesNotBlockOnCallback_AndCoalescesBirths`,
  `TestHandle_CallbackCtxCancelledByStartCtx`, and e2e `TestE2E_SettingCommandBurst`. Existing subscriber tests wait
  for the worker through `FlushForTest` (`interface/mqtt/export_test.go`, a sentinel job on the FIFO); existing
  listener tests wait on `Idle()`.
- e2e `TestE2E_SettingCommandBurst`: production poll intervals (5/15/60 s) and guard interval 100 ms; 8 different
  setting commands back to back (5 on h22, plus h23, h25, h35, h36). All 8 reach mbserver (h22 `0x07E4` → `0x2786`)
  and their state topics in **~2.4 s** (5 runs: 2.384–2.445 s); asserts < 3.5 s and that the captured WARN log has no
  `state publish completed with errors`, `mqtt publish failed` or `command queue full`.
- **Failure without the fix, run (not reasoned):** in a scratch copy, `enqueue` was changed to run the job inline on
  the calling (paho) goroutine. `TestE2E_SettingCommandBurst` failed: after 4 s only the first command had landed
  (h22 `0x07A4`, h23 20, h25 `0x0004`, h35 50, h36 500), `Condition never satisfied`.

**L1: Dockerfile cache.** `ARG VERSION=dev` moved to just before the `go build` RUN, after `COPY . .`. Verified: a
rebuild with a new `VERSION` shows `apk add`, `COPY go.mod` and `go mod download` as `CACHED`.

**L3: three WARN lines per failed setting command.** The subscriber's `apply command failed` line is now Debug and
logs `cmd.String()` (falls back to `%T`). `ApplyCommand.Apply` logs its generic `command failed` at Debug instead of
WARN when the error is `dto.ErrWriteNotApplied`, because `setSetting` already logged the detailed WARN. A not-applied
write now produces exactly one WARN, `setting write not applied` with `object_id`, `register`, `requested`, `actual`,
`raw` (asserted in `TestSetSetting_ReadBackMismatch` by counting `level=WARN`, and in
`TestSettingHandler_ApplyErrorIsLogged`). Other failures (invalid payload, transition guard, Modbus errors) still WARN
`command failed` once, from ApplyCommand.

**L4: no-op writes wear the EEPROM.**
- `infrastructure/modbus/client.go` `ModifyHolding`: when `modify(current) == current` the FC06 write is skipped,
  `oasis_modbus_ops_total{op="modify_holding_write",status="skipped"}` is incremented and a Debug line is logged.
  This also covers h86 `setMode`.
- `setSetting`: numbers now go through `ModifyHolding` with a closure that ignores `cur` and returns the target (one
  extra read per number write, fresh read plus the skip). The read-back, merge and publish always run, so HA converges.
- Tests: `TestRMW_ModifyHolding_NoOpSkipsWrite` (mbserver FC06 counter: unchanged → 0 writes + skipped metric;
  changed → 1 write), `TestSetSetting_NoOpSkipsWriteButReadsBackAndMerges` (table over alarm_beep, control_priority,
  active_filter, humidity_target, co2_target: ops are exactly `skip hN`, `read hN`, then the merge of the actual raw),
  `TestSetSetting_RMWUsesFreshRegisterNotSnapshot` now also asserts `write h22`, `read h22`. The ApplyCommand fake
  dispatcher skips unchanged values like the real client. h86 tests unchanged and green.

**L6: assumed limits.** `dark_brightness` 20..100 and the `co2_target` step 10 are bridge assumptions, not register-map
values: the PDF gives only 500..2000 for CO2, and dark_brightness was observed at 20 % on the live panel. Stated in the
README settings table and in the Notes below.

## Verification Results

Re-run 2026-11-23 after review round 1, on the uncommitted tree (base `7c695cf`), under `bash`, local Docker for
mosquitto and mbserver. No run contacted the real controller; the container run used
`-e MODBUS_HOST=127.0.0.1 -e MQTT_BROKER=127.0.0.1:1`.

```
$ gofmt -w . && gofmt -l .
(empty)
$ go build ./... && go vet ./...
(ok)
$ golangci-lint run ./...
0 issues.
$ go test -short -race -timeout 5m -coverprofile=/tmp/fix019.out $PKGS && go tool cover -func=/tmp/fix019.out | tail -1
ok  .../application/service      coverage: 99.3% of statements
ok  .../application/usecase      coverage: 98.4% of statements
ok  .../infrastructure/modbus    coverage: 82.1% of statements
ok  .../interface/mqtt           coverage: 97.6% of statements
... (all 18 packages ok)
total:                          (statements)   84.7%
$ go test -timeout 10m $PKGS
... all 18 packages ok (tests/e2e 13.197s)
$ go test -count=5 ./tests/e2e/
ok  .../tests/e2e   54.471s
5x PASS TestE2E_FullLoop, TestRetainedStateSurvivesSubscriberReconnect, TestDiscoveryRepublishedOnMQTTReconnect,
        TestE2E_SettingWriteNotApplied, TestE2E_SettingCommandBurst (converged 2.384s / 2.396s / 2.445s / 2.404s / 2.434s)
$ for i in 1 2 3; do go test -race -count=10 ./application/service/ ./application/usecase/ ./interface/mqtt/ \
    ./infrastructure/mqtt/ ./infrastructure/modbus/ ./internal/app/ || break; done
== loops 1..3: all six packages ok each loop (service ~3.1s, usecase ~2.0s, interface/mqtt ~2.3s,
   infrastructure/mqtt ~15s, modbus ~7.2s, app ~5.4s)
$ docker build --build-arg VERSION=v0.0.0-test -t oasis-m2m-test .
#22 DONE
$ docker run --rm -e MODBUS_HOST=127.0.0.1 -e MQTT_BROKER=127.0.0.1:1 oasis-m2m-test 2>&1 | head -3
{"level":"INFO","msg":"oasis-modbus2mqtt starting","version":"v0.0.0-test"}
{"level":"WARN","msg":"modbus connect failed","addr":"127.0.0.1:502",...}
{"level":"WARN","msg":"modbus initial connect failed",...}
$ docker ps --filter ancestor=oasis-m2m-test -q | xargs -r docker stop   # container stopped, 0 left running
```

`$PKGS` gates must run under `bash`: zsh does not word-split an unquoted `$PKGS`.

**e2e round trips (mosquitto + mbserver, `t.Logf`):**

```
TestE2E_FullLoop: round trip cmd/alarm_beep "OFF": mbserver h22 0x07E4 -> 0x07A4, state/alarm_beep = "OFF"
TestE2E_FullLoop: round trip cmd/control_priority "co2": mbserver h22 0x07A4 -> 0x47A4, state/control_priority = "co2"
TestE2E_FullLoop: round trip cmd/humidity_target "55.0": mbserver h35 0x0032 -> 0x0037, state/humidity_target = "55"
TestDiscoveryRepublishedOnMQTTReconnect: round trip cmd/touch_click "OFF": mbserver h22 0x07E4 -> 0x07C4, state/touch_click = "OFF"   (after session takeover)
TestE2E_SettingWriteNotApplied: ignored write cmd/co2_target "800": mbserver h36 500 -> 500, state/co2_target = "500", write_not_applied 0 -> 1
TestE2E_SettingWriteNotApplied: round trip cmd/dark_brightness "35": mbserver h23 0x0014 -> 0x0023, state/dark_brightness = "35"
TestE2E_SettingCommandBurst: burst of 8 setting commands: converged=true after 2.384s (h22=0x2786 h23=35 h25=0x0000 h35=60 h36=900)
```

`TestE2E_FullLoop` also asserts discovery count 32, that h22 bit 2 is preserved, and that `57.5`, `200` and `3` never change h35/h22 (`assert.Never`, 1 s).

**Mutation checks (each applied, test run, file restored):**

| Mutation | Failing test |
|---|---|
| RMW closure uses the snapshot's h22 instead of `cur` | `TestSetSetting_RMWUsesFreshRegisterNotSnapshot` |
| Merge the requested value instead of the read-back | `TestSetSetting_ReadBackMismatch` |
| Remove the `settingsGen` check in `doPoll` | `TestPoller_InFlightSlowPollDoesNotRevertReadBack` |
| `hvacAction` back on `HeaterPWM` | `TestHeaterActive_StableAcrossPWMToggle` |

**Incident (first round): the bare `docker run` gate.** The lead's gate `docker run --rm oasis-m2m-test 2>&1 | head -5` was run as written. With no env, the config default `MODBUS_HOST=10.90.19.7` applies, so the container dialled the real controller. Every logged attempt was `connect: connection refused` (no Modbus session was established). `head` closing the pipe did not stop the container: it kept retrying for about 20 s until it was found with `docker ps` and stopped. Future runs should use the story's form with `-e MODBUS_HOST=127.0.0.1 -e MQTT_BROKER=127.0.0.1:1`.

## Review round 2

Verdict **APPROVE**: no HIGH or MED findings. The H1 fix was confirmed by mutation: running jobs inline on the paho
goroutine makes `TestE2E_SettingCommandBurst` fail (only the first command lands within 4 s). The other mutations
(no ordering, unbuffered drop, Background job context, no stop check, sequence not short-circuited, no coalesce,
no single-flight, inline HA-birth callback) are each caught by their target test. The burst converged in about 2.4 s
on all 3 runs.

The six LOW findings were accepted as follow-ups and not fixed:

- **R2-L1.** `internal/app` has no test for the shutdown ordering (cancel, then wait for the worker and republish,
  then Disconnect). Removing `cancel()`, or disconnecting before the waits, passes every test.
- **R2-L2.** No test checks the 100 ms guard interval between a skipped RMW read and the read-back. The code sets
  `lastOpAt` correctly, but removing that line passes every test.
- **R2-L3.** The HA-birth republish can still start, or rerun once, after the Start ctx is cancelled at shutdown.
  The payloads are identical retained messages, so this is harmless.
- **R2-L4.** Commands still queued at shutdown are dropped without a log line.
- **R2-L5.** `TestHandlers_ReturnImmediatelyWhileApplierBlocks` calls `require` from a non-test goroutine. On a
  failure it hangs until the package timeout instead of failing fast.
- **R2-L6, verify live after deploy.** `setMode` (h86) now skips the write when the mode bits already match. h2
  Power_Dev is still an edge write through `WriteHolding`, so the skip should be harmless. Still, check once on the
  real unit: off, then `heat` with h86 already at heat, and confirm heating starts.

## Notes / Known limitations

- **"pressure" vs "co2".** The third control-priority option is labelled `co2` from the PDF. The panel says "pressure". If verified, relabel it in `ControlPriorityOptions` only. Changing an option label changes the HA state string, so automations that compare it must be updated.
- **Out-of-order retained publish.** A hot-tier publish of a snapshot merged just before the read-back can land after the read-back publish. The next hot poll (≤5 s) republishes the correct value. This is the existing self-heal pattern.
- **Write before the first settings read** (≤60 s after start): the read-back is not merged, and the generation bump makes an in-flight first read wait for the next slow poll. The entity stays `unknown` for up to 60 s more.
- **The transition guard blocks settings for 30–60 s** during power transitions. This is intentional.
- **The panel can still race us between our write and the read-back.** Only the target bits are compared, so a panel change to other bits is not a mismatch. A panel change to the *same* bit in that window is reported as a mismatch with the actual value published, which is correct.
- **Existing h31/h32/h86 writes are not read back.** Follow-up candidate (lead decision: out of scope).
- **Climate name.** The climate payload sends no `name`, so HA uses the default "MQTT HVAC" (hence the live `climate.oasis_syberia_mqtt_hvac`). Sending `"name": null` would name it after the device. Lead decision: out of scope, follow-up candidate.
- **Assumed limits (L6).** `dark_brightness` 20..100 and the `co2_target` step 10 are bridge assumptions, not values
  from the register map. The PDF gives only 500..2000 for CO2; dark_brightness was observed at 20 % on the live panel.
  If the panel accepts a lower brightness or a finer CO2 step, widen the `Settings` row only.
- **Command queue (H1).** Up to 32 parsed commands wait for the single worker; a burst beyond that is dropped with a
  WARN. Commands still queued at shutdown are dropped.
- **Follow-up L2 (from review round 1, not fixed):** a tier fan-out can race the read-back publish and show the old
  value for up to 5 s. Same as "Out-of-order retained publish" above.
- **Follow-up L5 (from review round 1, not fixed):** the `MODBUS_HOST` config default is the real controller
  `10.90.19.7`, which is how the first-round bare `docker run` dialled it. Changing the default could break the
  deployed config, so it is a follow-up only.
- **Follow-up (found while verifying L1):** the Dockerfile copies only `go.mod` before `go mod download`, and the
  download step is gated on `go.sum` existing, so it never runs and modules download inside the `go build` layer on
  every source change. `COPY go.mod go.sum ./` would make the module cache layer effective. Not changed.
- `PublishState` still publishes all retained state on every tier poll. With 35 messages per 5 s on a local broker this is fine. No dedupe added.

## Live verification (2026-11-24, delta, v0.1.3)

- CI on 254888e was red: Lint failed on gosec G115 in `domain/entity/panel_settings_test.go`, because CI pins golangci-lint v2.5.0 and the local run used v2.12. Fixed in 57caff7, and `make lint` now pins v2.5.0. CI is green on 57caff7 and on tag v0.1.3.
- Startup log: `version: v0.1.3`, `discovery published count=32`, all 18 command topics subscribed, no WARN/ERROR.
- HA: all 14 config entities were created with the expected `default_entity_id` names (no `koridor_` area prefix). Their states matched the live registers and the physical panel (h22 0x07E4, h25 bit2=1, priority temperature, 50 / 500 / 20).
- `switch.oasis_syberia_active_filter` off: the bridge logged `setting applied value=0`, and a direct register dump confirmed h25 0x0004 → 0x0000. No write-not-applied.
- heater_active / hvac_action: steady for 4+ minutes with heat demand 44. Before the fix it flipped every ~5 s.
- Pending, needs the operator at the panel: alarm_beep toggle confirmed on the panel, control_priority code for "pressure", and the h86 mode off→heat round trip.
- Follow-ups: the `MODBUS_HOST` default points at the production controller (a test container hit it during impl; connection refused); climate `name: null`; read-back for h31/h32/h86; Dockerfile module cache.

