---
title: "Feature: PollController + Poller + AvailabilityManager"
status: ready
priority: high
complexity: 7
planning_model: opus-4-5
implementation_model: haiku-4
created: 2026-04-25
updated: 2026-04-25
fix_updated: 2026-04-25
depends_on: 007-dispatcher-and-supervisor
risk_areas: [decode-correctness, tier-scheduling, snapshot-mutation-races]
---

## Context

Story 008 — три application-уровневые компоненты, которые превращают модбас читалку (story 006-007) в **continuously updated controller state**:

1. **PollController** (`application/usecase/poll_controller.go`) — три use case метода `PollHot/PollMedium/PollSlow`, каждый делает batched reads через CommandDispatcher и **decode** raw uint16 в типизированные доменные значения. Возвращает partial `Snapshot` (только поля своего tier'а).

2. **Poller** (`application/service/poller.go`) — orchestrator с тремя goroutines (по одной на tier), каждая on tick вызывает соответствующий PollController метод, мерджит результат в shared `Snapshot` под mutex, нотифицирует подписчиков (callback). Subscribers (story 012 PublishStateUseCase) получают полный snapshot после каждого poll.

3. **AvailabilityManager** (`application/service/availability_manager.go`) — отдельная goroutine, каждые ~2с проверяет `Snapshot.PolledAt` против `cfg.AvailabilityThreshold`. На переход online→offline вызывает `onOffline` callback; на переход offline→online — `onOnline` callback. PublishStateUseCase (story 012) подключит callbacks к MQTT availability topic.

**Дополнение к domain/entity/controller.go** (модификация существующего файла из story 003): добавить поле `RawErrors [4]uint16` (Error_Code, Error_Code_1, Error_Code_2, Error_Code_3) в `Snapshot`. Hot tier обновляет `RawErrors[0:2]`, Medium tier — `RawErrors[2:4]`. Метод `DecodeErrorSet()` возвращает `ErrorSet` из raw values on demand. Это решает merge problem (Errors не перезаписывается между tier'ами).

**Polling tiers** (из плана + catalog story 003):
| Tier | Period | Batches |
|---|---|---|
| Hot | 5s | input i2..i18 (17 regs), input i25..i30 (6 regs), holding h31..h32 (2 regs) |
| Medium | 15s | input i57..i58 (2 regs), input i69..i70 (2 regs), holding h86..h86 (1 reg) |
| Slow | 60s | input i0..i0 (1 reg), input i79..i79 (1 reg), holding h0..h0 (1 reg) |

Decode mapping (что обновляется в каждом tier):
- **Hot input i2..i18** (decoded by index relative to start=2):
  - `[0]=i2 State_0` → bits: PowerOn(b0), Switching(b1), HeatCapable(b6), CoolCapable(b7)
  - `[1]=i3 State_1` → `OperatingStateFromRegister`
  - `[2]=i4 Error_Code` → `RawErrors[0]`
  - `[3]=i5 Error_Code_1` → `RawErrors[1]`
  - `[4]=i6 Last_Time` → `OperationTimeLeft = ParseLastTime(raw)`
  - `[5]=i7..[6]=i8` — skip (T1 raw, T2 — N/A)
  - `[7]=i9 TkanK_x10` → `SupplyTemp = TemperatureFromRaw(int16(raw)/10)`
  - `[8]=i10..[11]=i13` — skip
  - `[12]=i14 ZagrFiltr1` → `FilterPct = int16(raw)`
  - `[13]=i15 DInputs` — skip
  - `[14]=i16 DOutputs` → `HeaterPWM = (raw & 0x3) > 0`, `DamperOpen = (raw & 0x20) != 0`
  - `[15]=i17` — skip
  - `[16]=i18 Reg` → `PIDDemand = raw`
- **Hot input i25..i30**:
  - `[0]=i25 Fan_State_1` → `FanState1`
  - `[1..4]=i26..i29` — skip
  - `[5]=i30 Fan_State_2` → `FanState2`
- **Hot holding h31..h32**:
  - `[0]=h31 Temp_Target` → `TargetTemp = TemperatureFromRaw`
  - `[1]=h32 Fan_Target_1` → `FanTarget1 = FanSpeedFromUnsafe(raw)` — нужен helper, потому что raw может быть 0 (если контроллер вернул мусор) — domain `NewFanSpeed` требует 1..10. Решение: helper `FanSpeedClamp(raw)` который clamps to [1,10]; или хранить как plain uint16 в Snapshot и не использовать `FanSpeed`. **Решение**: меняем тип `FanTarget1` в Snapshot с `FanSpeed` на `uint16` (raw). Story 009 (apply commands) вернёт строгий `FanSpeed` через `NewFanSpeed`. Так мы избегаем загрязнения poll path strict validation'ами на read side.
- **Medium input i57..i58**:
  - `[0]=i57 TKomn_x10_P` → `RoomTemp`
  - `[1]=i58 Room_Hum_P` → `RoomHumidity`
- **Medium input i69..i70**:
  - `[0]=i69 Error_Code_2` → `RawErrors[2]`
  - `[1]=i70 Error_Code_3` → `RawErrors[3]`
- **Medium holding h86**:
  - `[0]=h86 Dev_Keys_2` → `CurrentMode = ModeFromRegister(raw)` (биты 0..1)
- **Slow input i0**: `Firmware = FirmwareFromRaw(raw)`
- **Slow input i79**: `DeviceID = raw`
- **Slow holding h0**: `DeviceConfig = DeviceConfigFromRegister(raw)`

Reference: `/Users/alexmorbo/Home/homelab/.thoughts/oasis-syberia-modbus-bridge/01-controller-register-map.md` (полная карта), `/Users/alexmorbo/Home/homelab/.thoughts/oasis-syberia-modbus-bridge/04-mqtt-bridge-plan.md` секции 3, 7.

## User Story

**As a** разработчик oasis-modbus2mqtt,
**I want to** поддерживать актуальный snapshot контроллера через tiered polling, с быстрой реакцией на изменения и низкой нагрузкой на бесполезные регистры,
**So that** publish-state usecase (story 012) мог опубликовать любой field в любой момент без дополнительных reads, а availability manager корректно репортовал stale-state.

## Acceptance Criteria

### Snapshot field addition (modify existing file)

- [ ] Modify `domain/entity/controller.go`:
  - **Add field** `RawErrors [4]uint16` to `Snapshot` struct. Position: after `Errors ErrorSet` field.
  - **Add method** `(s Snapshot) DecodeErrorSet() ErrorSet { return DecodeErrors(s.RawErrors[0], s.RawErrors[1], s.RawErrors[2], s.RawErrors[3]) }`.
  - **Change field type** `FanTarget1` from `valueobject.FanSpeed` to `uint16` (raw register value, no validation on read path).
  - Update godoc comments accordingly. Other Snapshot fields (Errors, etc) untouched.
  - Update `domain/entity/controller_test.go` — IF tests reference `FanTarget1` через `.Value()` метод, change to direct uint16. IF tests construct Snapshot, optionally set `RawErrors`.

### PollController

- [ ] `application/usecase/poll_controller.go`:
  - `type Dispatcher interface` (local — для тестов):
    ```go
    type Dispatcher interface {
        ReadInput(ctx context.Context, start, count uint16) ([]uint16, error)
        ReadHolding(ctx context.Context, start, count uint16) ([]uint16, error)
    }
    ```
    Concrete `*service.CommandDispatcher` удовлетворяет (имеет ReadInput/ReadHolding методы из story 007). Импорт duck-typed.
  - `type PollController struct { dispatcher Dispatcher; logger *slog.Logger; clock port.Clock }`.
  - `func NewPollController(d Dispatcher, clock port.Clock, logger *slog.Logger) *PollController`. nil dispatcher → panic; nil clock → use `port.RealClock{}`; nil logger → `slog.Default()`.
  - `PollHot(ctx context.Context) (entity.Snapshot, error)`:
    - 3 sequential reads:
      1. `inputBatch1 := d.ReadInput(ctx, 2, 17)` — i2..i18
      2. `inputBatch2 := d.ReadInput(ctx, 25, 6)` — i25..i30
      3. `holdingBatch := d.ReadHolding(ctx, 31, 2)` — h31..h32
    - On any read error → return zero Snapshot + wrap error with `"poll_hot read i2..i18: %w"` etc.
    - Build Snapshot with только Hot fields populated. `PolledAt = clock.Now()`.
    - Decode per Context mapping (state bits, operating state, errors raw, T1, filter, dout bits, PID, fans, target_temp, fan_target).
    - Special: int16 values from uint16 raw — `int16(raw)` (preserve sign bit). `TkanK` is signed int (controller returns negative T as 0xFFE7 = -25*10). Use `int16(raw)/10.0` then `valueobject.TemperatureFromRaw(uint16(int16(raw)))` — wait, `TemperatureFromRaw(uint16)` does `raw / 10` as int — does it handle signed? Re-check valueobject.Temperature: `TemperatureFromRaw` (story 002 spec). If it doesn't handle int16, we need a helper here. **Decision**: PollController uses local helper `signedRawToCelsius(raw uint16) float64 { return float64(int16(raw)) / 10.0 }` and constructs `valueobject.Temperature` via `valueobject.NewTemperature` (which accepts float64 5..30, with NaN/Inf rejection). For sensor reads outside 5..30 (e.g., -25 if sensor unplugged), `NewTemperature` returns error → in that case, default to zero-value Temperature и log WARN.
      - Better: add helper `signedTemperatureFromRaw(raw uint16) valueobject.Temperature` in PollController which: `c := float64(int16(raw))/10.0; t, _ := valueobject.NewTemperature(c)` — ignore validation error, accept zero Temperature on out-of-range. Document that raw sensors may briefly produce out-of-range values during boot, OK.
  - `PollMedium(ctx) (entity.Snapshot, error)`:
    - 3 reads: `ReadInput(ctx, 57, 2)`, `ReadInput(ctx, 69, 2)`, `ReadHolding(ctx, 86, 1)`.
    - Decode RoomTemp, RoomHumidity, RawErrors[2:4], CurrentMode.
    - PolledAt = clock.Now() (whole tier, not per-batch).
  - `PollSlow(ctx) (entity.Snapshot, error)`:
    - 3 reads: `ReadInput(ctx, 0, 1)`, `ReadInput(ctx, 79, 1)`, `ReadHolding(ctx, 0, 1)`.
    - Decode Firmware, DeviceID, DeviceConfig.

### Poller

- [ ] `application/service/poller.go`:
  - `type SnapshotMerger interface { MergeHot(prev, partial entity.Snapshot) entity.Snapshot; MergeMedium(prev, partial entity.Snapshot) entity.Snapshot; MergeSlow(prev, partial entity.Snapshot) entity.Snapshot }`. Это интерфейс для тестируемости — конкретная реализация **встроена в тот же файл** как `defaultMerger struct{}` (без exported wrappers, чтобы не плодить файлы).
  - `defaultMerger`:
    - `MergeHot` копирует Hot fields из `partial` в `prev`: PowerOn, Switching, HeatCapable, CoolCapable, Operation, OperationTimeLeft, RawErrors[0], RawErrors[1], SupplyTemp, FilterPct, HeaterPWM, DamperOpen, PIDDemand, FanState1, FanState2, TargetTemp, FanTarget1, PolledAt.
    - `MergeMedium` копирует RoomTemp, RoomHumidity, RawErrors[2], RawErrors[3], CurrentMode, PolledAt.
    - `MergeSlow` копирует Firmware, DeviceID, DeviceConfig, PolledAt.
  - `type PollerConfig struct { HotInterval, MediumInterval, SlowInterval time.Duration }`.
  - `type Poller struct { ctrl pollFns; merger SnapshotMerger; cfg PollerConfig; clock port.Clock; logger *slog.Logger; mu sync.RWMutex; latest entity.Snapshot; subscribers []SnapshotSubscriber; subMu sync.Mutex; done chan struct{} }`.
    - `pollFns` interface — ровно три метода матчат `PollController.PollHot/Medium/Slow`. Чтобы избежать circular type dep:
      ```go
      type PollFns interface {
          PollHot(ctx context.Context) (entity.Snapshot, error)
          PollMedium(ctx context.Context) (entity.Snapshot, error)
          PollSlow(ctx context.Context) (entity.Snapshot, error)
      }
      ```
  - `type SnapshotSubscriber func(s entity.Snapshot)` — callback, вызывается Poller'ом после каждого успешного merge. **Subscribers вызываются под subMu unlock'ом** — copy slice → unlock → loop.
  - Methods:
    - `NewPoller(ctrl PollFns, merger SnapshotMerger, cfg PollerConfig, clock port.Clock, logger *slog.Logger) *Poller`. nil merger → use `defaultMerger{}`. Validate intervals > 0.
    - `Subscribe(sub SnapshotSubscriber)` — append to subscribers (under subMu).
    - `Snapshot() entity.Snapshot` — `mu.RLock(); defer; return p.latest`.
    - `Start(ctx context.Context)` — launches 3 goroutines (one per tier). Returns immediately. Caller cancels via ctx.
    - `Done() <-chan struct{}` — closed when ALL 3 goroutines exited.
  - **Goroutine logic** (per tier):
    ```go
    func (p *Poller) runTier(ctx, tickerD time.Duration, fn func(context.Context) (entity.Snapshot, error), mergeFn func(prev, partial entity.Snapshot) entity.Snapshot, tierName string, wg *sync.WaitGroup) {
        defer wg.Done()
        ticker := time.NewTicker(tickerD)
        defer ticker.Stop()
        // Run one immediate poll, THEN tick.
        p.doPoll(ctx, fn, mergeFn, tierName)
        for {
            select {
            case <-ctx.Done(): return
            case <-ticker.C:
                p.doPoll(ctx, fn, mergeFn, tierName)
            }
        }
    }
    ```
  - `doPoll`:
    - timer start, log DEBUG "tier poll start".
    - `partial, err := fn(ctx)`.
    - if `errors.Is(err, ctx.Err())` → return silent.
    - if other error → log WARN, increment `metrics.JobsByTierDroppedTotal(tierName).Inc()` — **wait**, Jobs Dropped is for queue-full case. Here we want a separate counter for failed polls. **Use** existing `metrics.ModbusErrorsTotal("poll_<tier>")` для увеличения и **all subscribers НЕ дёргаем**. Done.
    - on success: `mu.Lock(); p.latest = mergeFn(p.latest, partial); merged := p.latest; mu.Unlock()`. Notify subscribers (subscribers slice copy под subMu, then iterate без lock). Update `metrics.PollDurationSeconds(tierName).UpdateDuration(start)`. Update `metrics.SetLastSuccessfulPoll(time.Now())`.

### AvailabilityManager

- [ ] `application/service/availability_manager.go`:
  - `type AvailabilitySnapshot interface { Snapshot() entity.Snapshot }` — interface для inj of poller.
  - `type AvailabilityManager struct { src AvailabilitySnapshot; threshold time.Duration; checkInterval time.Duration; clock port.Clock; logger *slog.Logger; onOnline, onOffline func(); mu sync.Mutex; isOffline bool; done chan struct{} }`.
  - `NewAvailabilityManager(src, threshold time.Duration, clock port.Clock, logger *slog.Logger) *AvailabilityManager` — `checkInterval` всегда 2s (hardcoded). Initial state `isOffline = true` (до первого успешного poll = offline).
  - `SetCallbacks(onOnline, onOffline func())` — set after construction (because main wires PublishStateUseCase later).
  - `Start(ctx context.Context)` — launches goroutine. `Done() <-chan struct{}`.
  - **Goroutine logic**:
    ```go
    ticker := time.NewTicker(2*time.Second)
    defer ticker.Stop()
    for {
        select {
        case <-ctx.Done(): return
        case <-ticker.C:
            snap := a.src.Snapshot()
            now := a.clock.Now()
            stale := snap.PolledAt.IsZero() || now.Sub(snap.PolledAt) > a.threshold
            a.mu.Lock()
            transition := false
            if stale && !a.isOffline {
                a.isOffline = true
                transition = true
            } else if !stale && a.isOffline {
                a.isOffline = false
                transition = true
            }
            wasOffline := a.isOffline
            a.mu.Unlock()
            if transition {
                if wasOffline {
                    log INFO "availability: offline"
                    if a.onOffline != nil { a.onOffline() }
                } else {
                    log INFO "availability: online"
                    if a.onOnline != nil { a.onOnline() }
                }
            }
        }
    }
    ```

### Tests

- [ ] `application/usecase/poll_controller_test.go`:
  - Mock dispatcher: stores called args, returns canned `[]uint16` per call.
  - `TestPollHot_DecodesAllFields` — provide 17-reg + 6-reg + 2-reg responses with known bit patterns; assert resulting Snapshot has expected values for every Hot field.
  - `TestPollHot_ReadInputErrorPropagates` — first read returns error; assert PollHot returns wrapped error mentioning "i2..i18".
  - Same for Medium, Slow tiers.
  - `TestPollHot_OutOfRangeTemperature_DefaultsZero` — TkanK_x10 raw = 0xFFFE (-2 → -0.2°C, below 5°C valid range) → SupplyTemp is zero `valueobject.Temperature{}` без панике.
  - `TestPollHot_PolledAt_UsesClock` — fake clock returns fixed time, snapshot.PolledAt == that time.

- [ ] `application/service/poller_test.go`:
  - Mock `PollFns` with controlled per-tier outputs and call counters.
  - `TestStart_RunsAllTiersImmediately` — mock returns canned snapshots, `Start(ctx)`, wait briefly, assert each tier called ≥1.
  - `TestSubscribe_NotifiedAfterEachMerge` — subscribe, start, wait for ticker fires, assert callback invoked with merged snapshot.
  - `TestMerge_TierFieldIsolation` — Hot returns Snapshot with PowerOn=true; Medium returns Snapshot with RoomTemp=20; after both: latest has both. Hot doesn't overwrite RoomTemp; Medium doesn't overwrite PowerOn.
  - `TestPollFailure_DoesNotNotifySubscribers` — PollHot returns error, subscriber callback NOT invoked.
  - `TestDone_ClosesWhenContextCancelled` — start, cancel ctx, Done() closed within 200ms.

- [ ] `application/service/availability_manager_test.go`:
  - Mock `AvailabilitySnapshot` returning controlled `entity.Snapshot`.
  - Use small `checkInterval` for fast tests — **but interval is hardcoded 2s in spec**. Override via test-only constructor `newAvailabilityManagerWithInterval(...)`? Or expose checkInterval via `Set...` method. Решение: добавить unexported `withCheckInterval(d time.Duration)` setter (используется только в тестах — _test.go file в same package). Plan agent — реализовать.
  - Tests:
    - `TestInitial_OfflineUntilFirstFreshPoll` — clock advances, snapshot remains zero PolledAt → onOffline called once after first tick.
    - `TestTransitionsToOnline` — snapshot updates with PolledAt = now, threshold=30s → onOnline called.
    - `TestTransitionsBackToOffline` — first online, then snapshot.PolledAt becomes stale → onOffline called.
    - `TestCallback_NotInvokedWithoutTransition` — repeated checks while online → onOnline called only once total.

### Quality

- [ ] Coverage ≥ 90% для `application/usecase` и `application/service` (objedineно).
- [ ] `go test -race` clean.
- [ ] `golangci-lint run ./...` PASS.
- [ ] godoc one-liner на каждом exported.

## Constraints

- Зависимости: stdlib + project packages (domain, application/port, infrastructure/config, infrastructure/metrics) + testify.
- НЕ импортировать `infrastructure/modbus` напрямую.
- НЕ использовать `time.Tick` (leaks goroutine при cancel).
- Все goroutines ловят `<-ctx.Done()` и выходят.
- Mutex granularity: Poller's `latest` Snapshot — RWMutex (разрешает concurrent reads через `Snapshot()`); subscribers slice — отдельный mutex. AvailabilityManager — обычный sync.Mutex (low contention).
- Subscribe callbacks вызываются sequentially в одной goroutine (poller's tier goroutine после merge). Subscribers НЕ должны блокировать — best effort. Документировать в godoc.
- `t.Parallel()` свободно везде (нет t.Setenv).
- Goimports 3 группы.
- Никаких tickers без explicit Stop в defer.

### Test-time intervals

- Тесты используют **очень малые intervals** (1-10ms) для быстроты. Использовать helper `pollerWithFastIntervals(t, ...)`.

---

## Technical Specification

### Analysis

- `Dispatcher` interface in `application/usecase` is duck-typed (`ReadInput`/`ReadHolding` only) so `PollController` does not depend on `application/service.CommandDispatcher` directly — keeps usecase pkg free of cyclic imports and lets tests inject a fake without `port.ModbusClient`'s full surface.
- `Snapshot.FanTarget1` switches from `valueobject.FanSpeed` to `uint16`: poll path keeps raw register bytes (controller may legitimately return 0 transiently), while `application/dto.Command.FanSpeed` (write path) keeps strict `valueobject.NewFanSpeed` validation. Errors field is preserved untouched for back-compat — callers should prefer `Snapshot.DecodeErrorSet()` for current state, since the merger updates `RawErrors` (not `Errors`).
- `signedTempFromRaw` helper — supply temp register `TkanK` (i9) is signed int16 ×10 (sensor reads can briefly hit -25.0 when a probe is unplugged); `valueobject.TemperatureFromRaw` reinterprets `uint16/10` (always non-negative) so we use `int16(raw)` reinterpret cast then construct via `NewTemperature`, defaulting to zero `Temperature{}` on out-of-range input — keeps decoding total without surfacing transient sensor errors.
- AvailabilityManager initial state is `isOffline = true` so the first availability transition only fires *after* a fresh snapshot arrives from the poller — start-up offline is the safest assumption (no MQTT availability=online before first read).
- Subscriber slice is copied under `subMu` then iterated **without** any lock held; subscribers may run synchronously per tier goroutine but cannot block another tier.
- `withCheckInterval` is the unexported test escape hatch on `AvailabilityManager` — production wiring uses default 2s; `_test.go` in same package overrides to ~5ms for deterministic transitions.
- Decode lookups use indexes relative to batch start (i2..i18 → idx 0..16); defensive length checks return wrapped `ErrUnexpectedReadLength`.
- `gosec G115` — no narrowing conversions in poll path (uint16 → int16 is same-size reinterpret cast). All catalog operations stay in uint16.

### Implementation Order

1. Modify `domain/entity/controller.go` (and test) — add RawErrors, FanTarget1 → uint16, DecodeErrorSet method.
2. `application/usecase/poll_controller.go` + test
3. `application/service/poller.go` + test
4. `application/service/availability_manager.go` + test

---

### 1. Domain controller adjustments

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
// objects (Mode, Temperature, FirmwareVersion).
//
// FanTarget1 is the raw Fan_Target_1 register value (h32). The poll path does
// not validate it through valueobject.FanSpeed because the controller may
// transiently return 0; the write path constructs a strict
// valueobject.FanSpeed via NewFanSpeed instead.
//
// Errors is retained for backwards compatibility but is NOT populated by the
// merger — RawErrors holds the raw register reads and DecodeErrorSet derives
// a fresh ErrorSet on demand. Callers that need current error flags should
// prefer Snapshot.DecodeErrorSet().
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
	FanTarget1        uint16
	FanState1         uint16
	FanState2         uint16
	HeaterPWM         bool
	DamperOpen        bool
	PIDDemand         uint16
	FilterPct         int16
	Errors            ErrorSet
	RawErrors         [4]uint16
	RoomHumidity      uint16
	PolledAt          time.Time
}

// IsHealthy reports whether the snapshot was polled within the supplied freshness
// window (now - PolledAt <= threshold). Used by the availability manager.
func (s Snapshot) IsHealthy(now time.Time, threshold time.Duration) bool {
	return now.Sub(s.PolledAt) <= threshold
}

// DecodeErrorSet returns a freshly decoded ErrorSet from RawErrors. Prefer this
// over the static Errors field, which is left untouched by the poller.
func (s Snapshot) DecodeErrorSet() ErrorSet {
	return DecodeErrors(s.RawErrors[0], s.RawErrors[1], s.RawErrors[2], s.RawErrors[3])
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
	assert.Equal(t, uint16(0), s.FanTarget1)
	assert.Equal(t, [4]uint16{}, s.RawErrors)
	assert.True(t, s.DecodeErrorSet().IsEmpty())
}

func TestSnapshot_DecodeErrorSet(t *testing.T) {
	t.Parallel()

	t.Run("empty raw errors yields empty set", func(t *testing.T) {
		t.Parallel()
		s := entity.Snapshot{RawErrors: [4]uint16{0, 0, 0, 0}}
		assert.True(t, s.DecodeErrorSet().IsEmpty())
	})

	t.Run("Error_Code bit 4 (E04) and bit 10 (E10) set", func(t *testing.T) {
		t.Parallel()
		s := entity.Snapshot{RawErrors: [4]uint16{(1 << 4) | (1 << 10), 0, 0, 0}}
		codes := s.DecodeErrorSet().Codes()
		assert.Contains(t, codes, "E04")
		assert.Contains(t, codes, "E10")
		assert.Len(t, codes, 2)
	})

	t.Run("Error_Code_2 bit 3 set", func(t *testing.T) {
		t.Parallel()
		s := entity.Snapshot{RawErrors: [4]uint16{0, 0, 1 << 3, 0}}
		codes := s.DecodeErrorSet().Codes()
		assert.Contains(t, codes, "E2_b3")
	})

	t.Run("Error_Code_3 reserved bit 6 is filtered", func(t *testing.T) {
		t.Parallel()
		s := entity.Snapshot{RawErrors: [4]uint16{0, 0, 0, 1 << 6}}
		assert.True(t, s.DecodeErrorSet().IsEmpty())
	})
}
```

---

### 2. Poll controller use case

#### File: `application/usecase/poll_controller.go`

```go
// Package usecase contains application use cases that orchestrate domain
// entities and ports. Files in this package depend on domain/* and
// application/port; they never import infrastructure adapters or
// application/service implementations directly.
package usecase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/port"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"
)

// ErrUnexpectedReadLength is returned when a Modbus read returns fewer
// registers than the call requested. PollController wraps it with the batch
// label so callers can identify the offending tier read.
var ErrUnexpectedReadLength = errors.New("unexpected modbus read length")

// Dispatcher is the local read-only view of the Modbus dispatcher used by
// PollController. service.CommandDispatcher (story 007) satisfies it via
// duck typing — defining the interface here breaks the would-be cyclic
// dependency between application/usecase and application/service.
type Dispatcher interface {
	// ReadInput reads count input registers starting at start.
	ReadInput(ctx context.Context, start, count uint16) ([]uint16, error)
	// ReadHolding reads count holding registers starting at start.
	ReadHolding(ctx context.Context, start, count uint16) ([]uint16, error)
}

// PollController performs tiered Modbus reads and decodes raw register
// values into a partially populated entity.Snapshot. Each Poll* method
// returns only the fields owned by its tier; the caller (Poller) merges
// the partial snapshots into a single shared latest snapshot.
type PollController struct {
	dispatcher Dispatcher
	clock      port.Clock
	logger     *slog.Logger
}

// NewPollController constructs a PollController. A nil dispatcher is a
// programming error and panics. A nil clock falls back to port.RealClock; a
// nil logger to slog.Default.
func NewPollController(d Dispatcher, clock port.Clock, logger *slog.Logger) *PollController {
	if d == nil {
		panic("poll controller: dispatcher must not be nil")
	}
	if clock == nil {
		clock = port.RealClock{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &PollController{dispatcher: d, clock: clock, logger: logger}
}

// PollHot reads the hot-tier register set (i2..i18, i25..i30, h31..h32) and
// returns a Snapshot populated with the hot-tier fields. PolledAt is set
// from the injected clock once after all reads succeed.
func (p *PollController) PollHot(ctx context.Context) (entity.Snapshot, error) {
	in1, err := p.dispatcher.ReadInput(ctx, 2, 17)
	if err != nil {
		return entity.Snapshot{}, fmt.Errorf("poll_hot read i2..i18: %w", err)
	}
	if len(in1) != 17 {
		return entity.Snapshot{}, fmt.Errorf("poll_hot read i2..i18: got %d regs: %w", len(in1), ErrUnexpectedReadLength)
	}

	in2, err := p.dispatcher.ReadInput(ctx, 25, 6)
	if err != nil {
		return entity.Snapshot{}, fmt.Errorf("poll_hot read i25..i30: %w", err)
	}
	if len(in2) != 6 {
		return entity.Snapshot{}, fmt.Errorf("poll_hot read i25..i30: got %d regs: %w", len(in2), ErrUnexpectedReadLength)
	}

	hold, err := p.dispatcher.ReadHolding(ctx, 31, 2)
	if err != nil {
		return entity.Snapshot{}, fmt.Errorf("poll_hot read h31..h32: %w", err)
	}
	if len(hold) != 2 {
		return entity.Snapshot{}, fmt.Errorf("poll_hot read h31..h32: got %d regs: %w", len(hold), ErrUnexpectedReadLength)
	}

	state0 := in1[0]
	state1 := in1[1]
	dout := in1[14]

	snap := entity.Snapshot{
		PowerOn:           state0&(1<<0) != 0,
		Switching:         state0&(1<<1) != 0,
		HeatCapable:       state0&(1<<6) != 0,
		CoolCapable:       state0&(1<<7) != 0,
		Operation:         entity.OperatingStateFromRegister(state1),
		OperationTimeLeft: entity.ParseLastTime(in1[4]),
		SupplyTemp:        signedTempFromRaw(in1[7]),
		FilterPct:         int16(in1[12]), //nolint:gosec // same-size reinterpret; controller documented to return int16 percent
		HeaterPWM:         (dout & 0x3) > 0,
		DamperOpen:        (dout & 0x20) != 0,
		PIDDemand:         in1[16],
		FanState1:         in2[0],
		FanState2:         in2[5],
		TargetTemp:        valueobject.TemperatureFromRaw(hold[0]),
		FanTarget1:        hold[1],
		PolledAt:          p.clock.Now(),
	}
	snap.RawErrors[0] = in1[2]
	snap.RawErrors[1] = in1[3]
	return snap, nil
}

// PollMedium reads the medium-tier register set (i57..i58, i69..i70, h86)
// and returns a Snapshot populated with the medium-tier fields.
func (p *PollController) PollMedium(ctx context.Context) (entity.Snapshot, error) {
	in1, err := p.dispatcher.ReadInput(ctx, 57, 2)
	if err != nil {
		return entity.Snapshot{}, fmt.Errorf("poll_medium read i57..i58: %w", err)
	}
	if len(in1) != 2 {
		return entity.Snapshot{}, fmt.Errorf("poll_medium read i57..i58: got %d regs: %w", len(in1), ErrUnexpectedReadLength)
	}

	in2, err := p.dispatcher.ReadInput(ctx, 69, 2)
	if err != nil {
		return entity.Snapshot{}, fmt.Errorf("poll_medium read i69..i70: %w", err)
	}
	if len(in2) != 2 {
		return entity.Snapshot{}, fmt.Errorf("poll_medium read i69..i70: got %d regs: %w", len(in2), ErrUnexpectedReadLength)
	}

	hold, err := p.dispatcher.ReadHolding(ctx, 86, 1)
	if err != nil {
		return entity.Snapshot{}, fmt.Errorf("poll_medium read h86: %w", err)
	}
	if len(hold) != 1 {
		return entity.Snapshot{}, fmt.Errorf("poll_medium read h86: got %d regs: %w", len(hold), ErrUnexpectedReadLength)
	}

	snap := entity.Snapshot{
		RoomTemp:     signedTempFromRaw(in1[0]),
		RoomHumidity: in1[1],
		CurrentMode:  valueobject.ModeFromRegister(hold[0]),
		PolledAt:     p.clock.Now(),
	}
	snap.RawErrors[2] = in2[0]
	snap.RawErrors[3] = in2[1]
	return snap, nil
}

// PollSlow reads the slow-tier register set (i0, i79, h0) and returns a
// Snapshot populated with the slow-tier fields.
func (p *PollController) PollSlow(ctx context.Context) (entity.Snapshot, error) {
	in1, err := p.dispatcher.ReadInput(ctx, 0, 1)
	if err != nil {
		return entity.Snapshot{}, fmt.Errorf("poll_slow read i0: %w", err)
	}
	if len(in1) != 1 {
		return entity.Snapshot{}, fmt.Errorf("poll_slow read i0: got %d regs: %w", len(in1), ErrUnexpectedReadLength)
	}

	in2, err := p.dispatcher.ReadInput(ctx, 79, 1)
	if err != nil {
		return entity.Snapshot{}, fmt.Errorf("poll_slow read i79: %w", err)
	}
	if len(in2) != 1 {
		return entity.Snapshot{}, fmt.Errorf("poll_slow read i79: got %d regs: %w", len(in2), ErrUnexpectedReadLength)
	}

	hold, err := p.dispatcher.ReadHolding(ctx, 0, 1)
	if err != nil {
		return entity.Snapshot{}, fmt.Errorf("poll_slow read h0: %w", err)
	}
	if len(hold) != 1 {
		return entity.Snapshot{}, fmt.Errorf("poll_slow read h0: got %d regs: %w", len(hold), ErrUnexpectedReadLength)
	}

	return entity.Snapshot{
		Firmware:     valueobject.FirmwareFromRaw(in1[0]),
		DeviceID:     in2[0],
		DeviceConfig: entity.DeviceConfigFromRegister(hold[0]),
		PolledAt:     p.clock.Now(),
	}, nil
}

// signedTempFromRaw decodes a signed int16 ×10 °C sensor reading. Out-of-
// range values (NaN/Inf or outside the validated [5,30] °C range used by
// valueobject.NewTemperature) collapse to a zero Temperature instead of
// surfacing as an error: sensors briefly produce stale or unplugged values
// during boot or fault states, and the poll path must remain total.
func signedTempFromRaw(raw uint16) valueobject.Temperature {
	//nolint:gosec // same-size reinterpret of register value as signed int16, controller documented to return int16-encoded temperatures
	c := float64(int16(raw)) / 10.0
	if math.IsNaN(c) || math.IsInf(c, 0) {
		return valueobject.Temperature{}
	}
	t, err := valueobject.NewTemperature(c)
	if err != nil {
		return valueobject.Temperature{}
	}
	return t
}
```

#### File: `application/usecase/poll_controller_test.go`

```go
package usecase_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/dto"
	"github.com/alexmorbo/oasis-modbus2mqtt/application/usecase"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
)

type fakeDispatcher struct {
	inputResp   map[string][]uint16
	holdingResp map[string][]uint16
	inputErr    map[string]error
	holdingErr  map[string]error
	inputCalls  int
	holdCalls   int
}

func newFakeDispatcher() *fakeDispatcher {
	return &fakeDispatcher{
		inputResp:   make(map[string][]uint16),
		holdingResp: make(map[string][]uint16),
		inputErr:    make(map[string]error),
		holdingErr:  make(map[string]error),
	}
}

func key(start, count uint16) string { return fmt.Sprintf("%d:%d", start, count) }

func (f *fakeDispatcher) ReadInput(_ context.Context, start, count uint16) ([]uint16, error) {
	f.inputCalls++
	k := key(start, count)
	if err := f.inputErr[k]; err != nil {
		return nil, err
	}
	if v, ok := f.inputResp[k]; ok {
		return v, nil
	}
	return nil, fmt.Errorf("fakeDispatcher: no input response for %s", k)
}

func (f *fakeDispatcher) ReadHolding(_ context.Context, start, count uint16) ([]uint16, error) {
	f.holdCalls++
	k := key(start, count)
	if err := f.holdingErr[k]; err != nil {
		return nil, err
	}
	if v, ok := f.holdingResp[k]; ok {
		return v, nil
	}
	return nil, fmt.Errorf("fakeDispatcher: no holding response for %s", k)
}

type fakeClock struct{ now time.Time }

func (f fakeClock) Now() time.Time { return f.now }

func TestNewPollController_NilDispatcherPanics(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() {
		_ = usecase.NewPollController(nil, nil, nil)
	})
}

func TestNewPollController_DefaultsClockAndLogger(t *testing.T) {
	t.Parallel()
	d := newFakeDispatcher()
	pc := usecase.NewPollController(d, nil, nil)
	assert.NotNil(t, pc)
}

func TestPollHot_DecodesAllFields(t *testing.T) {
	t.Parallel()

	d := newFakeDispatcher()
	// State_0 = 0b0000_0001_1100_0001 = PowerOn(b0)+HeatCapable(b6)+CoolCapable(b7)+b8 (mode_heat indicator)
	// We use b0|b6|b7 only: 0x00C1.
	state0 := uint16((1 << 0) | (1 << 6) | (1 << 7))
	state1 := uint16(2) // OpPreheatCalorifier
	d.inputResp[key(2, 17)] = []uint16{
		state0, // i2 State_0
		state1, // i3 State_1
		0,      // i4 Error_Code
		0,      // i5 Error_Code_1
		0x0530, // i6 Last_Time = 5min 48s
		0,      // i7 (skip)
		0,      // i8 (skip)
		0x00DC, // i9 TkanK = 220 → 22.0°C
		0,      // i10
		0,      // i11
		0,      // i12
		0,      // i13
		52,     // i14 ZagrFiltr1
		0,      // i15 DInputs (skip)
		0x21,   // i16 DOutputs: heater b0=1, damper b5=1
		0,      // i17 (skip)
		30,     // i18 PID
	}
	d.inputResp[key(25, 6)] = []uint16{5, 0, 0, 0, 0, 7} // FanState1=5, FanState2=7
	d.holdingResp[key(31, 2)] = []uint16{225, 5}         // TargetTemp=22.5, FanTarget1=5

	clk := fakeClock{now: time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)}
	pc := usecase.NewPollController(d, clk, nil)

	snap, err := pc.PollHot(context.Background())
	require.NoError(t, err)

	assert.True(t, snap.PowerOn)
	assert.False(t, snap.Switching)
	assert.True(t, snap.HeatCapable)
	assert.True(t, snap.CoolCapable)
	assert.Equal(t, entity.OpPreheatCalorifier, snap.Operation)
	assert.Equal(t, 5*time.Minute+48*time.Second, snap.OperationTimeLeft)
	assert.InDelta(t, 22.0, snap.SupplyTemp.Celsius(), 0.001)
	assert.Equal(t, int16(52), snap.FilterPct)
	assert.True(t, snap.HeaterPWM)
	assert.True(t, snap.DamperOpen)
	assert.Equal(t, uint16(30), snap.PIDDemand)
	assert.Equal(t, uint16(5), snap.FanState1)
	assert.Equal(t, uint16(7), snap.FanState2)
	assert.InDelta(t, 22.5, snap.TargetTemp.Celsius(), 0.001)
	assert.Equal(t, uint16(5), snap.FanTarget1)
	assert.Equal(t, uint16(0), snap.RawErrors[0])
	assert.Equal(t, uint16(0), snap.RawErrors[1])
	assert.Equal(t, clk.now, snap.PolledAt)
}

func TestPollHot_RawErrorsCaptured(t *testing.T) {
	t.Parallel()

	d := newFakeDispatcher()
	d.inputResp[key(2, 17)] = []uint16{0, 0, 0x1234, 0xABCD, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	d.inputResp[key(25, 6)] = []uint16{0, 0, 0, 0, 0, 0}
	d.holdingResp[key(31, 2)] = []uint16{0, 0}

	pc := usecase.NewPollController(d, fakeClock{now: time.Now()}, nil)
	snap, err := pc.PollHot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint16(0x1234), snap.RawErrors[0])
	assert.Equal(t, uint16(0xABCD), snap.RawErrors[1])
}

func TestPollHot_OutOfRangeTemperature_DefaultsZero(t *testing.T) {
	t.Parallel()

	d := newFakeDispatcher()
	in1 := make([]uint16, 17)
	in1[7] = 0xFFE7 // -25 → -2.5°C, below the 5..30 range
	d.inputResp[key(2, 17)] = in1
	d.inputResp[key(25, 6)] = []uint16{0, 0, 0, 0, 0, 0}
	d.holdingResp[key(31, 2)] = []uint16{0, 0}

	pc := usecase.NewPollController(d, fakeClock{now: time.Now()}, nil)
	snap, err := pc.PollHot(context.Background())
	require.NoError(t, err)
	assert.InDelta(t, 0.0, snap.SupplyTemp.Celsius(), 0.001)
}

func TestPollHot_FirstReadErrorPropagates(t *testing.T) {
	t.Parallel()
	d := newFakeDispatcher()
	d.inputErr[key(2, 17)] = dto.ErrModbusNotConnected
	pc := usecase.NewPollController(d, fakeClock{now: time.Now()}, nil)
	_, err := pc.PollHot(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, dto.ErrModbusNotConnected)
	assert.Contains(t, err.Error(), "i2..i18")
}

func TestPollHot_SecondReadErrorPropagates(t *testing.T) {
	t.Parallel()
	d := newFakeDispatcher()
	d.inputResp[key(2, 17)] = make([]uint16, 17)
	d.inputErr[key(25, 6)] = dto.ErrModbusNotConnected
	pc := usecase.NewPollController(d, fakeClock{now: time.Now()}, nil)
	_, err := pc.PollHot(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "i25..i30")
}

func TestPollHot_ThirdReadErrorPropagates(t *testing.T) {
	t.Parallel()
	d := newFakeDispatcher()
	d.inputResp[key(2, 17)] = make([]uint16, 17)
	d.inputResp[key(25, 6)] = make([]uint16, 6)
	d.holdingErr[key(31, 2)] = dto.ErrModbusNotConnected
	pc := usecase.NewPollController(d, fakeClock{now: time.Now()}, nil)
	_, err := pc.PollHot(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "h31..h32")
}

func TestPollHot_UnexpectedReadLength(t *testing.T) {
	t.Parallel()
	d := newFakeDispatcher()
	d.inputResp[key(2, 17)] = make([]uint16, 5) // wrong length
	pc := usecase.NewPollController(d, fakeClock{now: time.Now()}, nil)
	_, err := pc.PollHot(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, usecase.ErrUnexpectedReadLength)
}

func TestPollHot_UnexpectedReadLengthSecondBatch(t *testing.T) {
	t.Parallel()
	d := newFakeDispatcher()
	d.inputResp[key(2, 17)] = make([]uint16, 17)
	d.inputResp[key(25, 6)] = make([]uint16, 3)
	pc := usecase.NewPollController(d, fakeClock{now: time.Now()}, nil)
	_, err := pc.PollHot(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, usecase.ErrUnexpectedReadLength)
}

func TestPollHot_UnexpectedReadLengthHolding(t *testing.T) {
	t.Parallel()
	d := newFakeDispatcher()
	d.inputResp[key(2, 17)] = make([]uint16, 17)
	d.inputResp[key(25, 6)] = make([]uint16, 6)
	d.holdingResp[key(31, 2)] = make([]uint16, 1)
	pc := usecase.NewPollController(d, fakeClock{now: time.Now()}, nil)
	_, err := pc.PollHot(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, usecase.ErrUnexpectedReadLength)
}

func TestPollMedium_DecodesAllFields(t *testing.T) {
	t.Parallel()

	d := newFakeDispatcher()
	d.inputResp[key(57, 2)] = []uint16{210, 45} // RoomTemp = 21.0, RoomHumidity=45
	d.inputResp[key(69, 2)] = []uint16{0xCAFE, 0xBABE}
	d.holdingResp[key(86, 1)] = []uint16{0x0001} // ModeHeat

	clk := fakeClock{now: time.Date(2026, 4, 25, 13, 0, 0, 0, time.UTC)}
	pc := usecase.NewPollController(d, clk, nil)

	snap, err := pc.PollMedium(context.Background())
	require.NoError(t, err)
	assert.InDelta(t, 21.0, snap.RoomTemp.Celsius(), 0.001)
	assert.Equal(t, uint16(45), snap.RoomHumidity)
	assert.Equal(t, uint16(0xCAFE), snap.RawErrors[2])
	assert.Equal(t, uint16(0xBABE), snap.RawErrors[3])
	assert.Equal(t, "heat", snap.CurrentMode.String())
	assert.Equal(t, clk.now, snap.PolledAt)
}

func TestPollMedium_FirstReadError(t *testing.T) {
	t.Parallel()
	d := newFakeDispatcher()
	d.inputErr[key(57, 2)] = dto.ErrModbusNotConnected
	pc := usecase.NewPollController(d, fakeClock{}, nil)
	_, err := pc.PollMedium(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "i57..i58")
}

func TestPollMedium_SecondReadError(t *testing.T) {
	t.Parallel()
	d := newFakeDispatcher()
	d.inputResp[key(57, 2)] = make([]uint16, 2)
	d.inputErr[key(69, 2)] = dto.ErrModbusNotConnected
	pc := usecase.NewPollController(d, fakeClock{}, nil)
	_, err := pc.PollMedium(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "i69..i70")
}

func TestPollMedium_HoldingReadError(t *testing.T) {
	t.Parallel()
	d := newFakeDispatcher()
	d.inputResp[key(57, 2)] = make([]uint16, 2)
	d.inputResp[key(69, 2)] = make([]uint16, 2)
	d.holdingErr[key(86, 1)] = dto.ErrModbusNotConnected
	pc := usecase.NewPollController(d, fakeClock{}, nil)
	_, err := pc.PollMedium(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "h86")
}

func TestPollMedium_LengthChecks(t *testing.T) {
	t.Parallel()

	t.Run("first batch wrong length", func(t *testing.T) {
		t.Parallel()
		d := newFakeDispatcher()
		d.inputResp[key(57, 2)] = []uint16{0}
		pc := usecase.NewPollController(d, fakeClock{}, nil)
		_, err := pc.PollMedium(context.Background())
		assert.ErrorIs(t, err, usecase.ErrUnexpectedReadLength)
	})

	t.Run("second batch wrong length", func(t *testing.T) {
		t.Parallel()
		d := newFakeDispatcher()
		d.inputResp[key(57, 2)] = make([]uint16, 2)
		d.inputResp[key(69, 2)] = []uint16{0}
		pc := usecase.NewPollController(d, fakeClock{}, nil)
		_, err := pc.PollMedium(context.Background())
		assert.ErrorIs(t, err, usecase.ErrUnexpectedReadLength)
	})

	t.Run("holding wrong length", func(t *testing.T) {
		t.Parallel()
		d := newFakeDispatcher()
		d.inputResp[key(57, 2)] = make([]uint16, 2)
		d.inputResp[key(69, 2)] = make([]uint16, 2)
		d.holdingResp[key(86, 1)] = []uint16{}
		pc := usecase.NewPollController(d, fakeClock{}, nil)
		_, err := pc.PollMedium(context.Background())
		assert.ErrorIs(t, err, usecase.ErrUnexpectedReadLength)
	})
}

func TestPollSlow_DecodesAllFields(t *testing.T) {
	t.Parallel()

	d := newFakeDispatcher()
	d.inputResp[key(0, 1)] = []uint16{0x5210}    // Firmware v5.2.1.0 → "v5.2.1"
	d.inputResp[key(79, 1)] = []uint16{0x4242}   // DeviceID
	d.holdingResp[key(0, 1)] = []uint16{0x0121} // heater=1, cooler=2, recup=1

	clk := fakeClock{now: time.Date(2026, 4, 25, 14, 0, 0, 0, time.UTC)}
	pc := usecase.NewPollController(d, clk, nil)

	snap, err := pc.PollSlow(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "v5.2.1", snap.Firmware.String())
	assert.Equal(t, uint16(0x4242), snap.DeviceID)
	assert.Equal(t, entity.HeaterElectric, snap.DeviceConfig.Heater)
	assert.Equal(t, entity.CoolerFancoil, snap.DeviceConfig.Cooler)
	assert.Equal(t, entity.RecuperatorPlate, snap.DeviceConfig.Recuperator)
	assert.Equal(t, clk.now, snap.PolledAt)
}

func TestPollSlow_FirstReadError(t *testing.T) {
	t.Parallel()
	d := newFakeDispatcher()
	d.inputErr[key(0, 1)] = dto.ErrModbusNotConnected
	pc := usecase.NewPollController(d, fakeClock{}, nil)
	_, err := pc.PollSlow(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "i0")
}

func TestPollSlow_SecondReadError(t *testing.T) {
	t.Parallel()
	d := newFakeDispatcher()
	d.inputResp[key(0, 1)] = make([]uint16, 1)
	d.inputErr[key(79, 1)] = dto.ErrModbusNotConnected
	pc := usecase.NewPollController(d, fakeClock{}, nil)
	_, err := pc.PollSlow(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "i79")
}

func TestPollSlow_HoldingReadError(t *testing.T) {
	t.Parallel()
	d := newFakeDispatcher()
	d.inputResp[key(0, 1)] = make([]uint16, 1)
	d.inputResp[key(79, 1)] = make([]uint16, 1)
	d.holdingErr[key(0, 1)] = dto.ErrModbusNotConnected
	pc := usecase.NewPollController(d, fakeClock{}, nil)
	_, err := pc.PollSlow(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "h0")
}

func TestPollSlow_LengthChecks(t *testing.T) {
	t.Parallel()

	t.Run("first wrong", func(t *testing.T) {
		t.Parallel()
		d := newFakeDispatcher()
		d.inputResp[key(0, 1)] = []uint16{}
		pc := usecase.NewPollController(d, fakeClock{}, nil)
		_, err := pc.PollSlow(context.Background())
		assert.ErrorIs(t, err, usecase.ErrUnexpectedReadLength)
	})

	t.Run("second wrong", func(t *testing.T) {
		t.Parallel()
		d := newFakeDispatcher()
		d.inputResp[key(0, 1)] = make([]uint16, 1)
		d.inputResp[key(79, 1)] = []uint16{}
		pc := usecase.NewPollController(d, fakeClock{}, nil)
		_, err := pc.PollSlow(context.Background())
		assert.ErrorIs(t, err, usecase.ErrUnexpectedReadLength)
	})

	t.Run("holding wrong", func(t *testing.T) {
		t.Parallel()
		d := newFakeDispatcher()
		d.inputResp[key(0, 1)] = make([]uint16, 1)
		d.inputResp[key(79, 1)] = make([]uint16, 1)
		d.holdingResp[key(0, 1)] = []uint16{}
		pc := usecase.NewPollController(d, fakeClock{}, nil)
		_, err := pc.PollSlow(context.Background())
		assert.ErrorIs(t, err, usecase.ErrUnexpectedReadLength)
	})
}

func TestPollHot_PolledAtUsesClock(t *testing.T) {
	t.Parallel()
	d := newFakeDispatcher()
	d.inputResp[key(2, 17)] = make([]uint16, 17)
	d.inputResp[key(25, 6)] = make([]uint16, 6)
	d.holdingResp[key(31, 2)] = make([]uint16, 2)

	clk := fakeClock{now: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)}
	pc := usecase.NewPollController(d, clk, nil)
	snap, err := pc.PollHot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, clk.now, snap.PolledAt)
}

func TestPollHot_ContextErrorPropagates(t *testing.T) {
	t.Parallel()

	customErr := errors.New("boom")
	d := newFakeDispatcher()
	d.inputErr[key(2, 17)] = customErr
	pc := usecase.NewPollController(d, fakeClock{}, nil)
	_, err := pc.PollHot(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, customErr)
}

// TestNewPollController_NilFallbacks verifies that nil clock and nil logger
// each fall back to safe defaults without panicking, while a nil dispatcher
// still panics.
func TestNewPollController_NilFallbacks(t *testing.T) {
	t.Parallel()

	t.Run("nil clock uses RealClock no panic", func(t *testing.T) {
		t.Parallel()
		d := newFakeDispatcher()
		pc := usecase.NewPollController(d, nil, nil)
		assert.NotNil(t, pc)
	})

	t.Run("nil logger uses slog.Default no panic", func(t *testing.T) {
		t.Parallel()
		d := newFakeDispatcher()
		pc := usecase.NewPollController(d, fakeClock{now: time.Now()}, nil)
		assert.NotNil(t, pc)
	})

	t.Run("nil dispatcher panics with dispatcher in message", func(t *testing.T) {
		t.Parallel()
		assert.Panics(t, func() {
			_ = usecase.NewPollController(nil, nil, nil)
		})
	})
}

var errFakeRead = errors.New("fake read failure")

// TestPollHot_ReadErrors verifies that an error from any of the three hot-tier
// reads is propagated as a wrapped error with the address-range hint.
func TestPollHot_ReadErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setupErr    func(d *fakeDispatcher)
		wantContain string
	}{
		{
			name: "first read i2..i18 error",
			setupErr: func(d *fakeDispatcher) {
				d.inputErr[key(2, 17)] = errFakeRead
			},
			wantContain: "i2..i18",
		},
		{
			name: "second read i25..i30 error",
			setupErr: func(d *fakeDispatcher) {
				d.inputResp[key(2, 17)] = make([]uint16, 17)
				d.inputErr[key(25, 6)] = errFakeRead
			},
			wantContain: "i25..i30",
		},
		{
			name: "third read h31..h32 error",
			setupErr: func(d *fakeDispatcher) {
				d.inputResp[key(2, 17)] = make([]uint16, 17)
				d.inputResp[key(25, 6)] = make([]uint16, 6)
				d.holdingErr[key(31, 2)] = errFakeRead
			},
			wantContain: "h31..h32",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newFakeDispatcher()
			tc.setupErr(d)
			pc := usecase.NewPollController(d, fakeClock{now: time.Now()}, nil)
			_, err := pc.PollHot(context.Background())
			require.Error(t, err)
			assert.ErrorIs(t, err, errFakeRead)
			assert.Contains(t, err.Error(), tc.wantContain)
		})
	}
}

// TestPollHot_LengthMismatch verifies that a wrong-length response from any
// hot-tier read returns an error wrapping ErrUnexpectedReadLength.
func TestPollHot_LengthMismatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(d *fakeDispatcher)
	}{
		{
			name: "i2..i18 wrong length",
			setup: func(d *fakeDispatcher) {
				d.inputResp[key(2, 17)] = make([]uint16, 16) // 16 instead of 17
			},
		},
		{
			name: "i25..i30 wrong length",
			setup: func(d *fakeDispatcher) {
				d.inputResp[key(2, 17)] = make([]uint16, 17)
				d.inputResp[key(25, 6)] = make([]uint16, 4) // 4 instead of 6
			},
		},
		{
			name: "h31..h32 wrong length",
			setup: func(d *fakeDispatcher) {
				d.inputResp[key(2, 17)] = make([]uint16, 17)
				d.inputResp[key(25, 6)] = make([]uint16, 6)
				d.holdingResp[key(31, 2)] = make([]uint16, 0) // 0 instead of 2
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newFakeDispatcher()
			tc.setup(d)
			pc := usecase.NewPollController(d, fakeClock{now: time.Now()}, nil)
			_, err := pc.PollHot(context.Background())
			require.Error(t, err)
			require.ErrorIs(t, err, usecase.ErrUnexpectedReadLength)
		})
	}
}

// TestPollMedium_ReadErrors verifies that an error from any of the three
// medium-tier reads is propagated with the address-range hint.
func TestPollMedium_ReadErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setupErr    func(d *fakeDispatcher)
		wantContain string
	}{
		{
			name: "first read i57..i58 error",
			setupErr: func(d *fakeDispatcher) {
				d.inputErr[key(57, 2)] = errFakeRead
			},
			wantContain: "i57..i58",
		},
		{
			name: "second read i69..i70 error",
			setupErr: func(d *fakeDispatcher) {
				d.inputResp[key(57, 2)] = make([]uint16, 2)
				d.inputErr[key(69, 2)] = errFakeRead
			},
			wantContain: "i69..i70",
		},
		{
			name: "third read h86 error",
			setupErr: func(d *fakeDispatcher) {
				d.inputResp[key(57, 2)] = make([]uint16, 2)
				d.inputResp[key(69, 2)] = make([]uint16, 2)
				d.holdingErr[key(86, 1)] = errFakeRead
			},
			wantContain: "h86",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newFakeDispatcher()
			tc.setupErr(d)
			pc := usecase.NewPollController(d, fakeClock{}, nil)
			_, err := pc.PollMedium(context.Background())
			require.Error(t, err)
			assert.ErrorIs(t, err, errFakeRead)
			assert.Contains(t, err.Error(), tc.wantContain)
		})
	}
}

// TestPollMedium_LengthMismatch verifies that a wrong-length response from any
// medium-tier read returns an error wrapping ErrUnexpectedReadLength.
func TestPollMedium_LengthMismatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(d *fakeDispatcher)
	}{
		{
			name: "i57..i58 wrong length",
			setup: func(d *fakeDispatcher) {
				d.inputResp[key(57, 2)] = make([]uint16, 1) // 1 instead of 2
			},
		},
		{
			name: "i69..i70 wrong length",
			setup: func(d *fakeDispatcher) {
				d.inputResp[key(57, 2)] = make([]uint16, 2)
				d.inputResp[key(69, 2)] = make([]uint16, 0) // 0 instead of 2
			},
		},
		{
			name: "h86 wrong length",
			setup: func(d *fakeDispatcher) {
				d.inputResp[key(57, 2)] = make([]uint16, 2)
				d.inputResp[key(69, 2)] = make([]uint16, 2)
				d.holdingResp[key(86, 1)] = make([]uint16, 0) // 0 instead of 1
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newFakeDispatcher()
			tc.setup(d)
			pc := usecase.NewPollController(d, fakeClock{}, nil)
			_, err := pc.PollMedium(context.Background())
			require.Error(t, err)
			require.ErrorIs(t, err, usecase.ErrUnexpectedReadLength)
		})
	}
}

// TestPollSlow_ReadErrors verifies that an error from any of the three
// slow-tier reads is propagated with the address-range hint.
func TestPollSlow_ReadErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setupErr    func(d *fakeDispatcher)
		wantContain string
	}{
		{
			name: "first read i0 error",
			setupErr: func(d *fakeDispatcher) {
				d.inputErr[key(0, 1)] = errFakeRead
			},
			wantContain: "i0",
		},
		{
			name: "second read i79 error",
			setupErr: func(d *fakeDispatcher) {
				d.inputResp[key(0, 1)] = make([]uint16, 1)
				d.inputErr[key(79, 1)] = errFakeRead
			},
			wantContain: "i79",
		},
		{
			name: "third read h0 error",
			setupErr: func(d *fakeDispatcher) {
				d.inputResp[key(0, 1)] = make([]uint16, 1)
				d.inputResp[key(79, 1)] = make([]uint16, 1)
				d.holdingErr[key(0, 1)] = errFakeRead
			},
			wantContain: "h0",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newFakeDispatcher()
			tc.setupErr(d)
			pc := usecase.NewPollController(d, fakeClock{}, nil)
			_, err := pc.PollSlow(context.Background())
			require.Error(t, err)
			assert.ErrorIs(t, err, errFakeRead)
			assert.Contains(t, err.Error(), tc.wantContain)
		})
	}
}

// TestPollSlow_LengthMismatch verifies that a wrong-length response from any
// slow-tier read returns an error wrapping ErrUnexpectedReadLength.
func TestPollSlow_LengthMismatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(d *fakeDispatcher)
	}{
		{
			name: "i0 wrong length",
			setup: func(d *fakeDispatcher) {
				d.inputResp[key(0, 1)] = make([]uint16, 0) // empty instead of 1
			},
		},
		{
			name: "i79 wrong length",
			setup: func(d *fakeDispatcher) {
				d.inputResp[key(0, 1)] = make([]uint16, 1)
				d.inputResp[key(79, 1)] = make([]uint16, 0) // empty instead of 1
			},
		},
		{
			name: "h0 wrong length",
			setup: func(d *fakeDispatcher) {
				d.inputResp[key(0, 1)] = make([]uint16, 1)
				d.inputResp[key(79, 1)] = make([]uint16, 1)
				d.holdingResp[key(0, 1)] = make([]uint16, 0) // empty instead of 1
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newFakeDispatcher()
			tc.setup(d)
			pc := usecase.NewPollController(d, fakeClock{}, nil)
			_, err := pc.PollSlow(context.Background())
			require.Error(t, err)
			require.ErrorIs(t, err, usecase.ErrUnexpectedReadLength)
		})
	}
}

// TestSignedTempFromRaw_OutOfRange tests signedTempFromRaw indirectly via
// PollHot's i9 (TkanK_x10) position, which is offset 7 in the i2..i18 batch.
// Out-of-range readings collapse to zero Temperature; valid readings decode
// correctly.
func TestSignedTempFromRaw_OutOfRange(t *testing.T) {
	t.Parallel()

	makeIn1 := func(rawTkanK uint16) []uint16 {
		in1 := make([]uint16, 17)
		in1[7] = rawTkanK
		return in1
	}

	tests := []struct {
		name        string
		rawTkanK    uint16
		wantCelsius float64
		wantZero    bool
	}{
		{
			// int16(0xFFE7) = -25 → -25/10 = -2.5°C, below [5,30] → zero
			name:     "below range 0xFFE7 negative sensor",
			rawTkanK: 0xFFE7,
			wantZero: true,
		},
		{
			// int16(0x0F00) = 3840 → 3840/10 = 384°C, above [5,30] → zero
			name:     "above range 0x0F00 absurd reading",
			rawTkanK: 0x0F00,
			wantZero: true,
		},
		{
			// int16(0x00FA) = 250 → 250/10 = 25.0°C, within [5,30] → valid
			name:        "valid 25.0 degrees",
			rawTkanK:    0x00FA,
			wantCelsius: 25.0,
			wantZero:    false,
		},
		{
			// int16(0x0032) = 50 → 50/10 = 5.0°C, at lower boundary → valid
			name:        "lower boundary 5.0 degrees",
			rawTkanK:    0x0032,
			wantCelsius: 5.0,
			wantZero:    false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newFakeDispatcher()
			d.inputResp[key(2, 17)] = makeIn1(tc.rawTkanK)
			d.inputResp[key(25, 6)] = make([]uint16, 6)
			d.holdingResp[key(31, 2)] = make([]uint16, 2)

			pc := usecase.NewPollController(d, fakeClock{now: time.Now()}, nil)
			snap, err := pc.PollHot(context.Background())
			require.NoError(t, err)

			if tc.wantZero {
				assert.InDelta(t, 0.0, snap.SupplyTemp.Celsius(), 0.001)
			} else {
				assert.InDelta(t, tc.wantCelsius, snap.SupplyTemp.Celsius(), 0.1)
			}
		})
	}
}
```

---

### 3. Poller service

#### File: `application/service/poller.go`

```go
package service

import (
	"context"
	"errors"
	"log/slog"
	"sync"
	"time"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/port"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/metrics"
)

// PollFns is the set of tier-poll callbacks the Poller orchestrates. The
// concrete *usecase.PollController satisfies this via duck typing — defining
// the interface here keeps application/service free of an
// application/usecase import.
type PollFns interface {
	// PollHot returns a Snapshot populated with the hot-tier fields.
	PollHot(ctx context.Context) (entity.Snapshot, error)
	// PollMedium returns a Snapshot populated with the medium-tier fields.
	PollMedium(ctx context.Context) (entity.Snapshot, error)
	// PollSlow returns a Snapshot populated with the slow-tier fields.
	PollSlow(ctx context.Context) (entity.Snapshot, error)
}

// SnapshotMerger merges a per-tier partial Snapshot into the previous full
// Snapshot. Implementations MUST copy only the tier-owned fields and leave
// the rest untouched, otherwise different tiers will overwrite each other.
type SnapshotMerger interface {
	// MergeHot copies hot-tier fields from partial onto prev.
	MergeHot(prev, partial entity.Snapshot) entity.Snapshot
	// MergeMedium copies medium-tier fields from partial onto prev.
	MergeMedium(prev, partial entity.Snapshot) entity.Snapshot
	// MergeSlow copies slow-tier fields from partial onto prev.
	MergeSlow(prev, partial entity.Snapshot) entity.Snapshot
}

// SnapshotSubscriber is invoked once per successful poll-and-merge. The
// callback runs synchronously inside the tier goroutine; it MUST NOT block
// or another tier's poll cadence will be delayed.
type SnapshotSubscriber func(s entity.Snapshot)

// PollerConfig declares the per-tier tick intervals.
type PollerConfig struct {
	HotInterval    time.Duration
	MediumInterval time.Duration
	SlowInterval   time.Duration
}

// Poller orchestrates three tier goroutines (hot/medium/slow) that
// periodically invoke the matching PollFns method, merge the partial
// Snapshot into the latest snapshot under a RWMutex, and notify
// subscribers. The merger is pluggable; defaultMerger is used when nil.
type Poller struct {
	ctrl   PollFns
	merger SnapshotMerger
	cfg    PollerConfig
	clock  port.Clock
	logger *slog.Logger

	mu     sync.RWMutex
	latest entity.Snapshot

	subMu       sync.Mutex
	subscribers []SnapshotSubscriber

	done chan struct{}
}

// NewPoller constructs a Poller. A nil ctrl panics; a nil merger falls back
// to the default merger; a nil clock to port.RealClock; a nil logger to
// slog.Default. Non-positive intervals panic.
func NewPoller(ctrl PollFns, merger SnapshotMerger, cfg PollerConfig, clock port.Clock, logger *slog.Logger) *Poller {
	if ctrl == nil {
		panic("poller: ctrl must not be nil")
	}
	if cfg.HotInterval <= 0 || cfg.MediumInterval <= 0 || cfg.SlowInterval <= 0 {
		panic("poller: tier intervals must be > 0")
	}
	if merger == nil {
		merger = defaultMerger{}
	}
	if clock == nil {
		clock = port.RealClock{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Poller{
		ctrl:   ctrl,
		merger: merger,
		cfg:    cfg,
		clock:  clock,
		logger: logger,
		done:   make(chan struct{}),
	}
}

// Subscribe registers cb to receive every merged Snapshot. Subscribers are
// invoked sequentially by the tier goroutine that produced the snapshot.
func (p *Poller) Subscribe(cb SnapshotSubscriber) {
	if cb == nil {
		return
	}
	p.subMu.Lock()
	p.subscribers = append(p.subscribers, cb)
	p.subMu.Unlock()
}

// Snapshot returns a copy of the latest merged Snapshot. Safe for
// concurrent use.
func (p *Poller) Snapshot() entity.Snapshot {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.latest
}

// Done returns a channel closed after all three tier goroutines have
// exited following ctx cancellation.
func (p *Poller) Done() <-chan struct{} {
	return p.done
}

// Start launches the three tier goroutines and returns immediately. Each
// goroutine performs an immediate poll and then ticks at its configured
// interval until ctx is cancelled.
func (p *Poller) Start(ctx context.Context) {
	var wg sync.WaitGroup
	wg.Add(3)
	go p.runTier(ctx, p.cfg.HotInterval, p.ctrl.PollHot, p.merger.MergeHot, "hot", &wg)
	go p.runTier(ctx, p.cfg.MediumInterval, p.ctrl.PollMedium, p.merger.MergeMedium, "medium", &wg)
	go p.runTier(ctx, p.cfg.SlowInterval, p.ctrl.PollSlow, p.merger.MergeSlow, "slow", &wg)
	go func() {
		wg.Wait()
		close(p.done)
	}()
}

func (p *Poller) runTier(
	ctx context.Context,
	tick time.Duration,
	fn func(context.Context) (entity.Snapshot, error),
	mergeFn func(prev, partial entity.Snapshot) entity.Snapshot,
	tier string,
	wg *sync.WaitGroup,
) {
	defer wg.Done()
	ticker := time.NewTicker(tick)
	defer ticker.Stop()

	p.doPoll(ctx, fn, mergeFn, tier)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			p.doPoll(ctx, fn, mergeFn, tier)
		}
	}
}

func (p *Poller) doPoll(
	ctx context.Context,
	fn func(context.Context) (entity.Snapshot, error),
	mergeFn func(prev, partial entity.Snapshot) entity.Snapshot,
	tier string,
) {
	start := p.clock.Now()
	p.logger.Debug("tier poll start", slog.String("tier", tier))
	partial, err := fn(ctx)
	if err != nil {
		if ctx.Err() != nil && errors.Is(err, ctx.Err()) {
			return
		}
		p.logger.Warn("tier poll failed",
			slog.String("tier", tier),
			slog.Any("error", err),
		)
		metrics.ModbusErrorsTotal("poll_" + tier).Inc()
		return
	}

	p.mu.Lock()
	p.latest = mergeFn(p.latest, partial)
	merged := p.latest
	p.mu.Unlock()

	p.subMu.Lock()
	subs := make([]SnapshotSubscriber, len(p.subscribers))
	copy(subs, p.subscribers)
	p.subMu.Unlock()
	for _, cb := range subs {
		cb(merged)
	}

	metrics.PollDurationSeconds(tier).UpdateDuration(start)
	metrics.SetLastSuccessfulPoll(p.clock.Now())
}

// defaultMerger copies tier-owned fields between Snapshot values. It is the
// merger used when NewPoller is given a nil SnapshotMerger.
type defaultMerger struct{}

// MergeHot copies the hot-tier fields from partial onto prev.
func (defaultMerger) MergeHot(prev, partial entity.Snapshot) entity.Snapshot {
	prev.PowerOn = partial.PowerOn
	prev.Switching = partial.Switching
	prev.HeatCapable = partial.HeatCapable
	prev.CoolCapable = partial.CoolCapable
	prev.Operation = partial.Operation
	prev.OperationTimeLeft = partial.OperationTimeLeft
	prev.RawErrors[0] = partial.RawErrors[0]
	prev.RawErrors[1] = partial.RawErrors[1]
	prev.SupplyTemp = partial.SupplyTemp
	prev.FilterPct = partial.FilterPct
	prev.HeaterPWM = partial.HeaterPWM
	prev.DamperOpen = partial.DamperOpen
	prev.PIDDemand = partial.PIDDemand
	prev.FanState1 = partial.FanState1
	prev.FanState2 = partial.FanState2
	prev.TargetTemp = partial.TargetTemp
	prev.FanTarget1 = partial.FanTarget1
	prev.PolledAt = partial.PolledAt
	return prev
}

// MergeMedium copies the medium-tier fields from partial onto prev.
func (defaultMerger) MergeMedium(prev, partial entity.Snapshot) entity.Snapshot {
	prev.RoomTemp = partial.RoomTemp
	prev.RoomHumidity = partial.RoomHumidity
	prev.RawErrors[2] = partial.RawErrors[2]
	prev.RawErrors[3] = partial.RawErrors[3]
	prev.CurrentMode = partial.CurrentMode
	prev.PolledAt = partial.PolledAt
	return prev
}

// MergeSlow copies the slow-tier fields from partial onto prev.
func (defaultMerger) MergeSlow(prev, partial entity.Snapshot) entity.Snapshot {
	prev.Firmware = partial.Firmware
	prev.DeviceID = partial.DeviceID
	prev.DeviceConfig = partial.DeviceConfig
	prev.PolledAt = partial.PolledAt
	return prev
}
```

#### File: `application/service/poller_test.go`

```go
package service_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/port"
	"github.com/alexmorbo/oasis-modbus2mqtt/application/service"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
)

type fakePollFns struct {
	mu       sync.Mutex
	hotN     atomic.Int32
	medN     atomic.Int32
	slowN    atomic.Int32
	hotSnap  entity.Snapshot
	medSnap  entity.Snapshot
	slowSnap entity.Snapshot
	hotErr   error
	medErr   error
	slowErr  error
}

func (f *fakePollFns) setHotErr(err error) {
	f.mu.Lock()
	f.hotErr = err
	f.mu.Unlock()
}

func (f *fakePollFns) PollHot(_ context.Context) (entity.Snapshot, error) {
	f.hotN.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.hotSnap, f.hotErr
}

func (f *fakePollFns) PollMedium(_ context.Context) (entity.Snapshot, error) {
	f.medN.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.medSnap, f.medErr
}

func (f *fakePollFns) PollSlow(_ context.Context) (entity.Snapshot, error) {
	f.slowN.Add(1)
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.slowSnap, f.slowErr
}

func fastPoller(t *testing.T, fns *fakePollFns) *service.Poller {
	t.Helper()
	cfg := service.PollerConfig{
		HotInterval:    2 * time.Millisecond,
		MediumInterval: 3 * time.Millisecond,
		SlowInterval:   4 * time.Millisecond,
	}
	return service.NewPoller(fns, nil, cfg, port.RealClock{}, nil)
}

func TestNewPoller_NilCtrlPanics(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() {
		_ = service.NewPoller(nil, nil, service.PollerConfig{
			HotInterval: time.Second, MediumInterval: time.Second, SlowInterval: time.Second,
		}, nil, nil)
	})
}

func TestNewPoller_ZeroIntervalsPanic(t *testing.T) {
	t.Parallel()
	fns := &fakePollFns{}
	assert.Panics(t, func() {
		_ = service.NewPoller(fns, nil, service.PollerConfig{}, nil, nil)
	})
}

func TestNewPoller_NegativeIntervalPanics(t *testing.T) {
	t.Parallel()
	fns := &fakePollFns{}
	assert.Panics(t, func() {
		_ = service.NewPoller(fns, nil, service.PollerConfig{
			HotInterval: -time.Second, MediumInterval: time.Second, SlowInterval: time.Second,
		}, nil, nil)
	})
}

func TestStart_RunsAllTiersImmediately(t *testing.T) {
	t.Parallel()

	fns := &fakePollFns{
		hotSnap:  entity.Snapshot{PowerOn: true},
		medSnap:  entity.Snapshot{RoomHumidity: 42},
		slowSnap: entity.Snapshot{DeviceID: 99},
	}
	p := fastPoller(t, fns)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	assert.Eventually(t, func() bool {
		return fns.hotN.Load() >= 1 && fns.medN.Load() >= 1 && fns.slowN.Load() >= 1
	}, 1*time.Second, 1*time.Millisecond)
}

func TestSnapshot_ReflectsLatestMerge(t *testing.T) {
	t.Parallel()

	fns := &fakePollFns{
		hotSnap:  entity.Snapshot{PowerOn: true, FanTarget1: 5},
		medSnap:  entity.Snapshot{RoomHumidity: 60},
		slowSnap: entity.Snapshot{DeviceID: 7},
	}
	p := fastPoller(t, fns)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	assert.Eventually(t, func() bool {
		s := p.Snapshot()
		return s.PowerOn && s.RoomHumidity == 60 && s.DeviceID == 7 && s.FanTarget1 == 5
	}, 1*time.Second, 1*time.Millisecond)
}

func TestSubscribe_NotifiedAfterEachMerge(t *testing.T) {
	t.Parallel()

	fns := &fakePollFns{hotSnap: entity.Snapshot{PowerOn: true}}
	p := fastPoller(t, fns)

	var calls atomic.Int32
	p.Subscribe(func(_ entity.Snapshot) {
		calls.Add(1)
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	assert.Eventually(t, func() bool {
		return calls.Load() >= 3
	}, 1*time.Second, 1*time.Millisecond)
}

func TestSubscribe_NilCallbackIgnored(t *testing.T) {
	t.Parallel()

	fns := &fakePollFns{}
	p := fastPoller(t, fns)
	assert.NotPanics(t, func() {
		p.Subscribe(nil)
	})
}

func TestMerge_TierFieldIsolation(t *testing.T) {
	t.Parallel()

	fns := &fakePollFns{
		hotSnap:  entity.Snapshot{PowerOn: true, FanTarget1: 4},
		medSnap:  entity.Snapshot{RoomHumidity: 70},
		slowSnap: entity.Snapshot{DeviceID: 1234},
	}
	p := fastPoller(t, fns)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	assert.Eventually(t, func() bool {
		s := p.Snapshot()
		return s.PowerOn && s.RoomHumidity == 70 && s.DeviceID == 1234
	}, 1*time.Second, 1*time.Millisecond, "all tier fields must coexist after merges")
}

func TestPollFailure_DoesNotNotifySubscribers(t *testing.T) {
	t.Parallel()

	fns := &fakePollFns{}
	fns.setHotErr(errors.New("read fail"))
	fns.medErr = errors.New("read fail")
	fns.slowErr = errors.New("read fail")

	p := fastPoller(t, fns)

	var calls atomic.Int32
	p.Subscribe(func(_ entity.Snapshot) { calls.Add(1) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	// Wait for several attempts.
	assert.Eventually(t, func() bool {
		return fns.hotN.Load() >= 2
	}, 1*time.Second, 1*time.Millisecond)
	cancel()
	<-p.Done()
	assert.Equal(t, int32(0), calls.Load(), "subscribers must not be invoked on poll error")
}

func TestDone_ClosesWhenContextCancelled(t *testing.T) {
	t.Parallel()

	fns := &fakePollFns{}
	p := fastPoller(t, fns)
	ctx, cancel := context.WithCancel(context.Background())
	p.Start(ctx)

	cancel()

	select {
	case <-p.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Done() not closed within 500ms after cancel")
	}
}

type recordingMerger struct {
	hot, med, slow atomic.Int32
}

func (r *recordingMerger) MergeHot(prev, partial entity.Snapshot) entity.Snapshot {
	r.hot.Add(1)
	prev.PowerOn = partial.PowerOn
	return prev
}
func (r *recordingMerger) MergeMedium(prev, partial entity.Snapshot) entity.Snapshot {
	r.med.Add(1)
	prev.RoomHumidity = partial.RoomHumidity
	return prev
}
func (r *recordingMerger) MergeSlow(prev, partial entity.Snapshot) entity.Snapshot {
	r.slow.Add(1)
	prev.DeviceID = partial.DeviceID
	return prev
}

func TestCustomMerger_IsInvoked(t *testing.T) {
	t.Parallel()

	fns := &fakePollFns{
		hotSnap:  entity.Snapshot{PowerOn: true},
		medSnap:  entity.Snapshot{RoomHumidity: 5},
		slowSnap: entity.Snapshot{DeviceID: 9},
	}
	rm := &recordingMerger{}
	cfg := service.PollerConfig{
		HotInterval:    2 * time.Millisecond,
		MediumInterval: 3 * time.Millisecond,
		SlowInterval:   4 * time.Millisecond,
	}
	p := service.NewPoller(fns, rm, cfg, port.RealClock{}, nil)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	p.Start(ctx)

	require.Eventually(t, func() bool {
		return rm.hot.Load() >= 1 && rm.med.Load() >= 1 && rm.slow.Load() >= 1
	}, 1*time.Second, 1*time.Millisecond)
}

func TestNewPoller_DefaultsLoggerAndClock(t *testing.T) {
	t.Parallel()
	fns := &fakePollFns{}
	cfg := service.PollerConfig{
		HotInterval:    10 * time.Millisecond,
		MediumInterval: 10 * time.Millisecond,
		SlowInterval:   10 * time.Millisecond,
	}
	p := service.NewPoller(fns, nil, cfg, nil, nil)
	assert.NotNil(t, p)
}
```

---

### 4. Availability manager

#### File: `application/service/availability_manager.go`

```go
package service

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/port"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
)

// AvailabilitySnapshot is the read-only view of the latest Snapshot used
// by AvailabilityManager. *Poller satisfies it via its Snapshot method.
type AvailabilitySnapshot interface {
	// Snapshot returns the latest Snapshot known to the source.
	Snapshot() entity.Snapshot
}

// AvailabilityManager observes Snapshot.PolledAt staleness and fires
// online/offline transition callbacks. It runs a single goroutine that
// ticks at checkInterval (default 2s; tests use withCheckInterval).
//
// Initial state is offline: a controller has not yet been observed alive
// when Start runs, so we never publish "online" before the first fresh
// snapshot arrives.
type AvailabilityManager struct {
	src           AvailabilitySnapshot
	threshold     time.Duration
	checkInterval time.Duration
	clock         port.Clock
	logger        *slog.Logger

	mu        sync.Mutex
	onOnline  func()
	onOffline func()
	isOffline bool

	done chan struct{}
}

// NewAvailabilityManager constructs an AvailabilityManager. A nil src
// panics. checkInterval defaults to 2s; tests can adjust via
// withCheckInterval. A nil clock falls back to port.RealClock; a nil
// logger to slog.Default. Initial state is offline.
func NewAvailabilityManager(
	src AvailabilitySnapshot,
	threshold time.Duration,
	clock port.Clock,
	logger *slog.Logger,
) *AvailabilityManager {
	if src == nil {
		panic("availability manager: src must not be nil")
	}
	if clock == nil {
		clock = port.RealClock{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &AvailabilityManager{
		src:           src,
		threshold:     threshold,
		checkInterval: 2 * time.Second,
		clock:         clock,
		logger:        logger,
		isOffline:     true,
		done:          make(chan struct{}),
	}
}

// withCheckInterval is a test-only escape hatch (used by _test.go files in
// the same package) to override the default 2s tick.
func (a *AvailabilityManager) withCheckInterval(d time.Duration) {
	if d <= 0 {
		return
	}
	a.checkInterval = d
}

// SetCallbacks installs the transition callbacks. main.go wires these
// after the publisher use case is constructed; either may be nil.
func (a *AvailabilityManager) SetCallbacks(onOnline, onOffline func()) {
	a.mu.Lock()
	a.onOnline = onOnline
	a.onOffline = onOffline
	a.mu.Unlock()
}

// Done returns a channel closed once the goroutine has exited.
func (a *AvailabilityManager) Done() <-chan struct{} {
	return a.done
}

// Start launches the watcher goroutine and returns immediately.
func (a *AvailabilityManager) Start(ctx context.Context) {
	go a.run(ctx)
}

func (a *AvailabilityManager) run(ctx context.Context) {
	defer close(a.done)
	ticker := time.NewTicker(a.checkInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			a.tick()
		}
	}
}

func (a *AvailabilityManager) tick() {
	snap := a.src.Snapshot()
	now := a.clock.Now()
	stale := snap.PolledAt.IsZero() || now.Sub(snap.PolledAt) > a.threshold

	a.mu.Lock()
	transition := false
	if stale && !a.isOffline {
		a.isOffline = true
		transition = true
	} else if !stale && a.isOffline {
		a.isOffline = false
		transition = true
	}
	nowOffline := a.isOffline
	onOnline := a.onOnline
	onOffline := a.onOffline
	a.mu.Unlock()

	if !transition {
		return
	}
	if nowOffline {
		a.logger.Info("availability: offline")
		if onOffline != nil {
			onOffline()
		}
	} else {
		a.logger.Info("availability: online")
		if onOnline != nil {
			onOnline()
		}
	}
}
```

#### File: `application/service/availability_manager_test.go`

```go
package service

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
)

type fakeAvailSrc struct {
	mu   sync.Mutex
	snap entity.Snapshot
}

func (f *fakeAvailSrc) set(s entity.Snapshot) {
	f.mu.Lock()
	f.snap = s
	f.mu.Unlock()
}

func (f *fakeAvailSrc) Snapshot() entity.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap
}

type mutableClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *mutableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *mutableClock) set(t time.Time) {
	c.mu.Lock()
	c.now = t
	c.mu.Unlock()
}

func newFastManager(t *testing.T, src *fakeAvailSrc, clk *mutableClock, threshold time.Duration) *AvailabilityManager {
	t.Helper()
	a := NewAvailabilityManager(src, threshold, clk, nil)
	a.withCheckInterval(5 * time.Millisecond)
	return a
}

func TestNewAvailabilityManager_NilSrcPanics(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() {
		_ = NewAvailabilityManager(nil, time.Second, nil, nil)
	})
}

func TestNewAvailabilityManager_DefaultsClockAndLogger(t *testing.T) {
	t.Parallel()
	src := &fakeAvailSrc{}
	a := NewAvailabilityManager(src, time.Second, nil, nil)
	assert.NotNil(t, a)
}

func TestWithCheckInterval_IgnoresNonPositive(t *testing.T) {
	t.Parallel()
	src := &fakeAvailSrc{}
	a := NewAvailabilityManager(src, time.Second, nil, nil)
	a.withCheckInterval(0)
	a.withCheckInterval(-time.Second)
	assert.Equal(t, 2*time.Second, a.checkInterval)
}

func TestInitial_OfflineUntilFirstFreshPoll(t *testing.T) {
	t.Parallel()

	src := &fakeAvailSrc{}
	clk := &mutableClock{now: time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)}
	a := newFastManager(t, src, clk, 30*time.Second)

	var offlineCalls atomic.Int32
	var onlineCalls atomic.Int32
	a.SetCallbacks(
		func() { onlineCalls.Add(1) },
		func() { offlineCalls.Add(1) },
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.Start(ctx)

	// snapshot has zero PolledAt, so it stays stale → no transition (already offline).
	time.Sleep(50 * time.Millisecond)
	assert.Equal(t, int32(0), offlineCalls.Load())
	assert.Equal(t, int32(0), onlineCalls.Load())
}

func TestTransitionsToOnline(t *testing.T) {
	t.Parallel()

	src := &fakeAvailSrc{}
	clk := &mutableClock{now: time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)}
	a := newFastManager(t, src, clk, 30*time.Second)

	var offlineCalls atomic.Int32
	var onlineCalls atomic.Int32
	a.SetCallbacks(
		func() { onlineCalls.Add(1) },
		func() { offlineCalls.Add(1) },
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.Start(ctx)

	// Inject fresh poll.
	src.set(entity.Snapshot{PolledAt: clk.Now()})

	require.Eventually(t, func() bool {
		return onlineCalls.Load() == 1
	}, 500*time.Millisecond, 5*time.Millisecond)
	assert.Equal(t, int32(0), offlineCalls.Load())
}

func TestTransitionsBackToOffline(t *testing.T) {
	t.Parallel()

	src := &fakeAvailSrc{}
	clk := &mutableClock{now: time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)}
	a := newFastManager(t, src, clk, 100*time.Millisecond)

	var offlineCalls atomic.Int32
	var onlineCalls atomic.Int32
	a.SetCallbacks(
		func() { onlineCalls.Add(1) },
		func() { offlineCalls.Add(1) },
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.Start(ctx)

	// Step 1: fresh snapshot → online.
	src.set(entity.Snapshot{PolledAt: clk.Now()})
	require.Eventually(t, func() bool {
		return onlineCalls.Load() == 1
	}, 500*time.Millisecond, 5*time.Millisecond)

	// Step 2: advance clock past threshold without updating snapshot → offline.
	clk.set(clk.Now().Add(1 * time.Second))
	require.Eventually(t, func() bool {
		return offlineCalls.Load() == 1
	}, 500*time.Millisecond, 5*time.Millisecond)
}

func TestCallback_NotInvokedWithoutTransition(t *testing.T) {
	t.Parallel()

	src := &fakeAvailSrc{}
	clk := &mutableClock{now: time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)}
	a := newFastManager(t, src, clk, 30*time.Second)

	var onlineCalls atomic.Int32
	a.SetCallbacks(
		func() { onlineCalls.Add(1) },
		func() {},
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.Start(ctx)

	src.set(entity.Snapshot{PolledAt: clk.Now()})
	require.Eventually(t, func() bool {
		return onlineCalls.Load() == 1
	}, 500*time.Millisecond, 5*time.Millisecond)

	// Many further fresh ticks should not retrigger.
	for i := 0; i < 20; i++ {
		clk.mu.Lock()
		clk.now = clk.now.Add(10 * time.Millisecond)
		clk.mu.Unlock()
		src.set(entity.Snapshot{PolledAt: clk.Now()})
		time.Sleep(7 * time.Millisecond)
	}
	assert.Equal(t, int32(1), onlineCalls.Load())
}

func TestCallbacks_CanBeNil(t *testing.T) {
	t.Parallel()

	src := &fakeAvailSrc{}
	clk := &mutableClock{now: time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)}
	a := newFastManager(t, src, clk, 30*time.Second)
	// no SetCallbacks call.

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	a.Start(ctx)

	src.set(entity.Snapshot{PolledAt: clk.Now()})
	time.Sleep(50 * time.Millisecond)
	// Smoke: no panic with nil callbacks even after a transition.
}

func TestDone_ClosesAfterCancel(t *testing.T) {
	t.Parallel()

	src := &fakeAvailSrc{}
	clk := &mutableClock{now: time.Now()}
	a := newFastManager(t, src, clk, time.Second)

	ctx, cancel := context.WithCancel(context.Background())
	a.Start(ctx)
	cancel()

	select {
	case <-a.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Done not closed within 500ms")
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
    You are an IMPLEMENTATION AGENT for story 008.

    ABSOLUTE RULES:
    - Copy code BYTE-FOR-BYTE.
    - DO NOT modify code, fix imports, add `//nolint`, remove t.Parallel. STOP on failure.
    - Story modifies an existing file (domain/entity/controller.go). Overwrite it with the story content.

    PROCESS:
    1. Read story.
    2. For each "#### File: `path`" — overwrite that path with the code block content.
    3. Run from work dir, IN ORDER:
       a. gofmt -l domain/ application/   (must be empty)
       b. go build ./...
       c. go vet ./...
       d. go test -short ./...
       e. go test -race ./application/...   (full -race for new packages)
       f. coverage:
          - go test -coverpkg=./application/usecase,./application/service -coverprofile=/tmp/oasis_app.out ./application/usecase/... ./application/service/...
          - go tool cover -func=/tmp/oasis_app.out | tail -1   ≥90%
       g. golangci-lint run ./...   (0 issues)
    4. PASS → status review, fill Verification Results, refresh Files Changed.
    5. FAIL → append errors verbatim to Issues Found, status in_progress, STOP.

    Work dir: /Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/
```

### Fix Agent Instructions

Standard. Edit story only.

---

## Implementation Notes

### Progress

- [ ] `domain/entity/controller.go` (+ test) modified
- [ ] `application/usecase/poll_controller.go` (+ test)
- [ ] `application/service/poller.go` (+ test)
- [ ] `application/service/availability_manager.go` (+ test)
- [ ] gofmt clean
- [ ] go build/vet pass
- [ ] go test pass
- [ ] -race pass
- [ ] coverage ≥90%
- [ ] lint clean

### Verification Results

```
gofmt:                          [PASS/FAIL]
go build:                       [PASS/FAIL]
go vet:                         [PASS/FAIL]
go test:                        [PASS/FAIL]
go test -race:                  [PASS/FAIL]
application/{usecase+service} cov: [X.X%]
golangci-lint:                  [PASS/FAIL]
```

### Issues Found

(none)

### Fixes Applied

- Add 2× //nolint:gosec for same-size uint16↔int16 reinterprets in poll_controller.go (signedTempFromRaw, filter pct).
- Add comprehensive error-path tests to poll_controller_test.go to lift application/usecase coverage from 50% range to ≥90%.

---

## Files Changed

- `domain/entity/controller.go`
- `domain/entity/controller_test.go`
- `application/usecase/poll_controller.go`
- `application/usecase/poll_controller_test.go`
- `application/service/poller.go`
- `application/service/poller_test.go`
- `application/service/availability_manager.go`
- `application/service/availability_manager_test.go`
