---
title: "Feature: PublishDiscovery + PublishState use cases"
status: review
priority: high
complexity: 6
planning_model: opus-4-5
implementation_model: haiku-4
created: 2026-04-25
updated: 2026-04-25
depends_on: 011-discovery-builder
risk_areas: [delta-detection, hvac-mode-mapping, hvac-action-mapping]
---

## Context

Story 012 — два use case'а, превращающих internal `Snapshot` в MQTT state messages, и инициализирующих HA discovery.

### PublishDiscoveryUseCase
- Берёт `discovery.Builder.Build(firmware)` (story 011) → список Discovery configs.
- Публикует каждый config (retain=true) в его topic через `port.MQTTPublisher`.
- **Idempotent re-publish при изменении firmware** — track `lastFirmware`, на change → `Build(firmware)` + publish all снова.

### PublishStateUseCase
- Принимает `entity.Snapshot` (от Poller subscriber'а, story 008).
- Декодирует в per-entity payloads:
  - **Direct sensors**: supply_temperature, filter_clog, heat_demand, supply_fan_speed, exhaust_fan_speed, room_temperature, room_humidity, firmware, device_id, operation_time_left.
  - **Switch**: power → "ON"/"OFF" из `PowerOn`.
  - **Binary sensors derived**:
    - `heater_active` ← `HeaterPWM` → "ON"/"OFF"
    - `damper_open` ← `DamperOpen` → "OPEN"/"CLOSED"
    - `problem` ← `!DecodeErrorSet().IsEmpty()` → "ON"/"OFF"
  - **Climate composite** (4 sub-states):
    - `hvac_mode`: `PowerOn=false` → "off"; `PowerOn=true && Mode=HEAT` → "heat"; `PowerOn=true && Mode=OFF/COOL/AUTO` → "fan_only".
    - `target_temperature`: `TargetTemp.Celsius()` formatted with 1 decimal (если non-zero).
    - `fan_target`: `FanTarget1` uint16 → string "5" (без unit'а).
    - `hvac_action`: derived per plan section 6:
      - `PowerOn=false` → "off"
      - `Operation == OpPreheatCalorifier` → "preheating"
      - `Operation == OpStartFan || OpFanCoastdown` → "fan"
      - `HeaterPWM=true` → "heating"
      - `FanState1 > 0` → "fan"
      - else → "idle"
- **Delta detection**: per-topic `lastPayload map[string][]byte`. Только если новый payload отличается от last — publish. Иначе skip (молча, лог DEBUG).

### Availability
PublishStateUseCase **НЕ** публикует availability — это делается AvailabilityManager (story 008) через колбэки `onOnline`/`onOffline`. main wiring (story 015) подключит:
```go
availMgr.SetCallbacks(
    func() { mqtt.Publish(topics.Availability(), []byte("online"), true) },
    func() { mqtt.Publish(topics.Availability(), []byte("offline"), true) },
)
```
В этой story (012) availability обработка вне scope.

Reference: `/Users/alexmorbo/Home/homelab/.thoughts/oasis-syberia-modbus-bridge/04-mqtt-bridge-plan.md` секции 6 + climate mapping.

## User Story

**As a** разработчик oasis-modbus2mqtt,
**I want to** иметь use cases, которые трансформируют snapshot в HA-видимые MQTT state messages с delta-detection,
**So that** HA получает только реальные изменения (low broker load), и discovery republish'ится при firmware update без manual intervention.

## Acceptance Criteria

### PublishDiscoveryUseCase

- [ ] `application/usecase/publish_discovery.go`:
  - **Local interfaces**:
    ```go
    type DiscoveryBuilder interface {
        Build(firmware string) ([]discovery.Discovery, error)
    }
    type Publisher interface {
        Publish(ctx context.Context, topic string, payload []byte, retained bool) error
    }
    ```
  - **Struct**:
    ```go
    type PublishDiscovery struct {
        builder       DiscoveryBuilder
        publisher     Publisher
        logger        *slog.Logger
        mu            sync.Mutex
        lastFirmware  string
    }
    ```
  - `func NewPublishDiscovery(b DiscoveryBuilder, p Publisher, logger *slog.Logger) *PublishDiscovery` — panic on nil deps; nil logger → slog.Default().
  - **Public methods**:
    - `Publish(ctx context.Context, firmware string) error` — forces full publish irrespective of lastFirmware. Updates lastFirmware. На любую publish error возвращает first error (но **продолжает** публиковать остальные — best effort). Returns aggregated error если что-то не получилось (через `errors.Join`).
    - `PublishIfChanged(ctx context.Context, firmware string) error` — early return nil если `firmware == lastFirmware && lastFirmware != ""`. Иначе делегирует `Publish`.
  - log INFO "discovery published", "count", len(discoveries), "firmware", firmware.

- [ ] `application/usecase/publish_discovery_test.go`:
  - Mock `DiscoveryBuilder` returning canned slice or error.
  - Mock `Publisher` recording publish calls.
  - Tests:
    - `TestPublish_PublishesAll_AsRetained` — assert all returned configs published with retained=true.
    - `TestPublish_FirstPublishError_ContinuesAndAggregates` — publisher returns error на 2-м topic. Other publishes still attempted. Returned error is non-nil and wraps the publish error.
    - `TestPublish_BuilderError_PropagatesNoPublish` — builder returns error → no publishes attempted, error returned.
    - `TestPublishIfChanged_SkipsWhenSameFirmware` — set lastFirmware via prior Publish, then PublishIfChanged with same → no new publishes.
    - `TestPublishIfChanged_PublishesOnFirstCall` — empty lastFirmware → publish.
    - `TestPublishIfChanged_PublishesOnFirmwareChange` — different firmware → publish.
    - `TestNew_NilDeps_Panic`.
  - All `t.Parallel()`.

### PublishStateUseCase

- [ ] `application/usecase/publish_state.go`:
  - **Local interfaces**:
    ```go
    type Publisher interface {  // same as above; reuse from publish_discovery.go via package-level
        Publish(ctx context.Context, topic string, payload []byte, retained bool) error
    }
    type Topics interface {
        State(objectID string) string
    }
    ```
    NB: package-level Publisher interface — declare once in `publish_discovery.go` или общий файл `publish_ports.go`. Решение: declare в `publish_discovery.go`, использовать в `publish_state.go`. Но если Go откажется компилировать (одно имя в разных файлах одного пакета OK), всё ок.
  - **Struct**:
    ```go
    type PublishState struct {
        publisher  Publisher
        topics     Topics
        logger     *slog.Logger
        mu         sync.Mutex
        lastPayload map[string][]byte
    }
    ```
  - `func NewPublishState(pub Publisher, topics Topics, logger *slog.Logger) *PublishState` — panic on nil deps; nil logger → slog.Default(); init lastPayload map.
  - **Public method**:
    - `Apply(ctx context.Context, snap entity.Snapshot) error`:
      - Build `[]stateMsg` (topic + payload pairs) для всех entities.
      - For each: check `lastPayload[topic] == newPayload` → skip (с DEBUG лог).
      - Else publish (retained=false) → on success update lastPayload.
      - Return aggregated error (через errors.Join).
  - **Internal helpers**:
    - `buildStateMessages(snap entity.Snapshot) []stateMsg` — pure function, returns ALL state messages для snapshot (independent of last). Каждый msg = `{topic string; payload []byte; objectID string}`.
    - `formatTemperature(t valueobject.Temperature) string` — `fmt.Sprintf("%.1f", t.Celsius())`. Для zero-value Temperature (Celsius=0) — `"0.0"`. (HA воспринимает 0 как valid value; bridge публикует what it has.)
    - `boolToOnOff(b bool) string` → `"ON"` / `"OFF"`.
    - `boolToOpenClosed(b bool) string` → `"OPEN"` / `"CLOSED"`.
    - `hvacMode(snap entity.Snapshot) string`:
      ```go
      if !snap.PowerOn { return "off" }
      if snap.CurrentMode == valueobject.ModeHeat { return "heat" }
      return "fan_only"  // OFF, COOL, AUTO all collapse to fan_only on heat-only hardware
      ```
    - `hvacAction(snap entity.Snapshot) string`:
      ```go
      if !snap.PowerOn { return "off" }
      switch snap.Operation {
      case entity.OpPreheatCalorifier: return "preheating"
      case entity.OpStartFan, entity.OpFanCoastdown: return "fan"
      }
      if snap.HeaterPWM { return "heating" }
      if snap.FanState1 > 0 { return "fan" }
      return "idle"
      ```
    - `problemValue(snap entity.Snapshot) string`:
      ```go
      if snap.DecodeErrorSet().IsEmpty() { return "OFF" }
      return "ON"
      ```

  - **Mapping** (что published в каждом case):
    ```
    objectID                state                              type
    -----------------------------------------------------------------
    supply_temperature      formatTemperature(snap.SupplyTemp) string
    filter_clog             snap.FilterPct                     int16 → strconv
    heat_demand             snap.PIDDemand                     uint16 → strconv
    supply_fan_speed        snap.FanState1                     uint16 → strconv
    exhaust_fan_speed       snap.FanState2                     uint16 → strconv
    room_temperature        formatTemperature(snap.RoomTemp)
    room_humidity           snap.RoomHumidity                  uint16 → strconv
    firmware                snap.Firmware.String()
    device_id               fmt.Sprintf("0x%04X", snap.DeviceID)
    operation_time_left     int(snap.OperationTimeLeft.Seconds()) → strconv
    power                   boolToOnOff(snap.PowerOn)
    heater_active           boolToOnOff(snap.HeaterPWM)
    damper_open             boolToOpenClosed(snap.DamperOpen)
    problem                 problemValue(snap)
    hvac_mode               hvacMode(snap)
    target_temperature      formatTemperature(snap.TargetTemp)
    fan_target              strconv.FormatUint(uint64(snap.FanTarget1), 10)
    hvac_action             hvacAction(snap)
    ```
  - log DEBUG для skipped (delta == nil); INFO для published count.

- [ ] `application/usecase/publish_state_test.go`:
  - Mock `Publisher` collecting `(topic, payload, retained)` tuples.
  - Mock `Topics` with stub: `State("X") → "test/state/X"`.
  - Tests:
    - `TestApply_PublishesAllOnFirstCall` — fresh state, all ~18 messages published.
    - `TestApply_DeltaSkipsUnchanged` — call Apply twice with identical snapshot → second call publishes nothing.
    - `TestApply_DeltaPublishesChanged` — снапшот с измененным fan_target → только fan_target topic published в second call.
    - `TestHvacMode_PowerOff` — PowerOn=false → "off".
    - `TestHvacMode_PowerOn_Heat` → "heat".
    - `TestHvacMode_PowerOn_OffMode` → "fan_only".
    - `TestHvacMode_PowerOn_Cool` → "fan_only".
    - `TestHvacMode_PowerOn_Auto` → "fan_only".
    - `TestHvacAction_PowerOff` → "off".
    - `TestHvacAction_PreheatTransition` → "preheating".
    - `TestHvacAction_StartFanTransition` → "fan".
    - `TestHvacAction_FanCoastdown` → "fan".
    - `TestHvacAction_HeaterPWM` → "heating".
    - `TestHvacAction_FanRunning` → "fan".
    - `TestHvacAction_Idle` → "idle".
    - `TestProblem_NoErrors` → "OFF".
    - `TestProblem_WithErrors` — set RawErrors[0] = некий бит → "ON".
    - `TestApply_PublishError_DoesNotUpdateLastPayload` — publisher returns error на одном topic, тот лежит non-cached → next Apply пытается publish тот же topic заново.
    - `TestApply_AggregatesErrors` — multiple publish failures → returned error содержит multiple wrapped errors.
    - `TestNew_NilDeps_Panic`.
  - All `t.Parallel()`.

### Quality

- [ ] Coverage `application/usecase` ≥ 90% (combined с предыдущими story tests).
- [ ] `go test -race` clean.
- [ ] `golangci-lint run ./...` PASS.
- [ ] `gofmt -l application/usecase/` empty.
- [ ] godoc one-liner на каждом exported.

## Constraints

- Зависимости: stdlib + project packages.
- НЕ импортировать `infrastructure/mqtt` напрямую — только interfaces.
- НЕ импортировать `infrastructure/discovery` — wait, нужно для `discovery.Discovery` тип. Решение: импортируется в `publish_discovery.go` (это OK — application использует infrastructure types через interface, но конкретные DTO типы (как Discovery) могут импортироваться). **Альтернатива**: переместить `Discovery{Topic, Payload}` тип в общий пакет (например, `application/dto/discovery.go`). **Решение**: импортировать `infrastructure/discovery` для типа `Discovery` — pragmatic, не циклит, infrastructure не depends на application.
- Goimports 3 группы.
- gosec G115: для `int(snap.OperationTimeLeft.Seconds())` — int64 → int (actually `Seconds()` returns float64 → int; use `int64(secs)`). На macOS/Linux `int` = int64, narrowing невозможен. Безопасно. Если linter воюет — `//nolint:gosec`.
- НЕ использовать `init()`.
- Mock структуры в _test файлах.
- t.Parallel() свободно.

---

## Technical Specification

### Analysis

- `Publisher` interface declared exactly once in `publish_discovery.go`; `publish_state.go` is in the same `usecase` package and references the symbol directly (Go allows this since both files share the package namespace).
- `infrastructure/discovery` import in `publish_discovery.go` is acceptable per Clean Architecture: application layer consumes a pure-data DTO type (`discovery.Discovery`) — infrastructure does not depend back on application, so no cycle. Pragmatic over relocation.
- Delta detection lives in `PublishState` as `map[string][]byte` keyed by topic, guarded by `sync.Mutex`. Only successful publishes update the cache; failed publishes leave the topic untracked so the next cycle retries.
- Multi-error aggregation uses `errors.Join` (Go 1.20+); both use cases continue best-effort across remaining work after a single publish failure.
- `hvacAction` priority follows the plan: PowerOff → preheat transition → fan transitions → heater on → fan running → idle. Implemented as guarded switch + sequential checks for clarity.
- `boolToOpenClosed` returns `"OPEN"`/`"CLOSED"` for the damper binary sensor; HA's standard `device_class: opening` accepts those payload variants when configured by discovery (story 011).
- `int(d.Seconds())` for `OperationTimeLeft`: `time.Duration.Seconds()` returns `float64`, cast to `int` is float→int (no narrowing). On 64-bit platforms this is safe — the operation timer maxes at ~65500 seconds. No `//nolint` needed.

### Implementation Order

1. `application/usecase/publish_discovery.go`
2. `application/usecase/publish_discovery_test.go`
3. `application/usecase/publish_state.go`
4. `application/usecase/publish_state_test.go`

---

### 1. Publish discovery use case

#### File: `application/usecase/publish_discovery.go`

```go
package usecase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/discovery"
)

// DiscoveryBuilder produces the slice of HA discovery configs to publish for a
// given firmware label. Implementations are pure (no I/O) and safe for repeated
// calls; the canonical implementation lives in infrastructure/discovery.
type DiscoveryBuilder interface {
	// Build returns the full set of (topic, payload) pairs that describe every
	// entity the bridge exposes. The firmware string is stamped into the
	// device.sw_version field of each payload.
	Build(firmware string) ([]discovery.Discovery, error)
}

// Publisher is the minimal MQTT publish surface used by both publish use cases.
// Defining the interface in this file (and re-using it from publish_state.go
// via package-level visibility) keeps the use cases independent of the MQTT
// client implementation in infrastructure/mqtt.
type Publisher interface {
	// Publish sends payload to topic. retained=true asks the broker to store
	// the last value for late subscribers; retained=false sends a normal one-
	// shot message. Errors propagate verbatim to the caller.
	Publish(ctx context.Context, topic string, payload []byte, retained bool) error
}

// PublishDiscovery is the use case that publishes Home Assistant MQTT
// discovery configs. It re-publishes the full set on a firmware change so HA
// always reflects the live controller version.
type PublishDiscovery struct {
	builder      DiscoveryBuilder
	publisher    Publisher
	logger       *slog.Logger
	mu           sync.Mutex
	lastFirmware string
}

// NewPublishDiscovery constructs a PublishDiscovery. A nil builder or nil
// publisher is a programming error and panics. A nil logger falls back to
// slog.Default.
func NewPublishDiscovery(b DiscoveryBuilder, p Publisher, logger *slog.Logger) *PublishDiscovery {
	if b == nil {
		panic("publish discovery: builder must not be nil")
	}
	if p == nil {
		panic("publish discovery: publisher must not be nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &PublishDiscovery{
		builder:   b,
		publisher: p,
		logger:    logger,
	}
}

// Publish forces a full discovery republish for the supplied firmware label.
// Every config is sent with retained=true so HA can rebuild entities after a
// broker restart. On any publish error the use case continues with the
// remaining configs and aggregates failures via errors.Join. lastFirmware is
// updated regardless of partial errors so subsequent PublishIfChanged calls
// honour the most recent attempted version.
func (p *PublishDiscovery) Publish(ctx context.Context, firmware string) error {
	configs, err := p.builder.Build(firmware)
	if err != nil {
		return fmt.Errorf("build discovery: %w", err)
	}

	var errs []error
	for _, cfg := range configs {
		if pubErr := p.publisher.Publish(ctx, cfg.Topic, cfg.Payload, true); pubErr != nil {
			errs = append(errs, fmt.Errorf("publish discovery topic %s: %w", cfg.Topic, pubErr))
		}
	}

	p.mu.Lock()
	p.lastFirmware = firmware
	p.mu.Unlock()

	if len(errs) > 0 {
		p.logger.Warn("discovery publish completed with errors",
			"count", len(configs),
			"errors", len(errs),
			"firmware", firmware,
		)
		return errors.Join(errs...)
	}

	p.logger.Info("discovery published",
		"count", len(configs),
		"firmware", firmware,
	)
	return nil
}

// PublishIfChanged publishes the full discovery set only when firmware differs
// from the last successful (or attempted) firmware label. The first call after
// construction always publishes because lastFirmware starts empty. An empty
// firmware argument with an empty cached value is treated as "no change" and
// returns nil without contacting the builder.
func (p *PublishDiscovery) PublishIfChanged(ctx context.Context, firmware string) error {
	p.mu.Lock()
	last := p.lastFirmware
	p.mu.Unlock()

	if firmware == last && last != "" {
		p.logger.Debug("discovery skip: firmware unchanged", "firmware", firmware)
		return nil
	}
	if firmware == "" && last == "" {
		p.logger.Debug("discovery skip: empty firmware on first call")
		return nil
	}
	return p.Publish(ctx, firmware)
}
```

#### File: `application/usecase/publish_discovery_test.go`

```go
package usecase_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/usecase"
	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/discovery"
)

type fakeDiscoveryBuilder struct {
	configs []discovery.Discovery
	err     error
	calls   int
	mu      sync.Mutex
}

func (b *fakeDiscoveryBuilder) Build(_ string) ([]discovery.Discovery, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	if b.err != nil {
		return nil, b.err
	}
	out := make([]discovery.Discovery, len(b.configs))
	copy(out, b.configs)
	return out, nil
}

func (b *fakeDiscoveryBuilder) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

type publishCall struct {
	topic    string
	payload  []byte
	retained bool
}

type fakePublisher struct {
	mu         sync.Mutex
	calls      []publishCall
	errAt      map[string]error
	defaultErr error
}

func (f *fakePublisher) Publish(_ context.Context, topic string, payload []byte, retained bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, publishCall{topic: topic, payload: append([]byte(nil), payload...), retained: retained})
	if err, ok := f.errAt[topic]; ok {
		return err
	}
	return f.defaultErr
}

func (f *fakePublisher) snapshot() []publishCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]publishCall, len(f.calls))
	copy(out, f.calls)
	return out
}

func discardLoggerDiscovery() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

func sampleConfigs() []discovery.Discovery {
	return []discovery.Discovery{
		{Topic: "ha/sensor/a/config", Payload: []byte(`{"a":1}`)},
		{Topic: "ha/sensor/b/config", Payload: []byte(`{"b":2}`)},
		{Topic: "ha/sensor/c/config", Payload: []byte(`{"c":3}`)},
	}
}

func TestNewPublishDiscovery_NilBuilder_Panics(t *testing.T) {
	t.Parallel()
	require.PanicsWithValue(t, "publish discovery: builder must not be nil", func() {
		_ = usecase.NewPublishDiscovery(nil, &fakePublisher{}, nil)
	})
}

func TestNewPublishDiscovery_NilPublisher_Panics(t *testing.T) {
	t.Parallel()
	require.PanicsWithValue(t, "publish discovery: publisher must not be nil", func() {
		_ = usecase.NewPublishDiscovery(&fakeDiscoveryBuilder{}, nil, nil)
	})
}

func TestNewPublishDiscovery_NilLogger_FallsBack(t *testing.T) {
	t.Parallel()
	pd := usecase.NewPublishDiscovery(&fakeDiscoveryBuilder{configs: sampleConfigs()}, &fakePublisher{}, nil)
	require.NotNil(t, pd)
	err := pd.Publish(context.Background(), "v5.2.0")
	require.NoError(t, err)
}

func TestPublish_PublishesAll_AsRetained(t *testing.T) {
	t.Parallel()
	b := &fakeDiscoveryBuilder{configs: sampleConfigs()}
	p := &fakePublisher{}
	pd := usecase.NewPublishDiscovery(b, p, discardLoggerDiscovery())

	err := pd.Publish(context.Background(), "v5.2.0")
	require.NoError(t, err)

	calls := p.snapshot()
	require.Len(t, calls, 3)
	for _, c := range calls {
		assert.True(t, c.retained, "expected retained=true for topic %s", c.topic)
	}
	assert.Equal(t, "ha/sensor/a/config", calls[0].topic)
	assert.Equal(t, []byte(`{"a":1}`), calls[0].payload)
	assert.Equal(t, "ha/sensor/c/config", calls[2].topic)
}

func TestPublish_FirstPublishError_ContinuesAndAggregates(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("broker rejected")
	b := &fakeDiscoveryBuilder{configs: sampleConfigs()}
	p := &fakePublisher{errAt: map[string]error{"ha/sensor/b/config": sentinel}}
	pd := usecase.NewPublishDiscovery(b, p, discardLoggerDiscovery())

	err := pd.Publish(context.Background(), "v5.2.0")
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
	assert.Len(t, p.snapshot(), 3, "all configs should be attempted despite mid-loop error")
}

func TestPublish_BuilderError_PropagatesNoPublish(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("build failed")
	b := &fakeDiscoveryBuilder{err: sentinel}
	p := &fakePublisher{}
	pd := usecase.NewPublishDiscovery(b, p, discardLoggerDiscovery())

	err := pd.Publish(context.Background(), "v5.2.0")
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
	assert.Empty(t, p.snapshot())
}

func TestPublishIfChanged_SkipsWhenSameFirmware(t *testing.T) {
	t.Parallel()
	b := &fakeDiscoveryBuilder{configs: sampleConfigs()}
	p := &fakePublisher{}
	pd := usecase.NewPublishDiscovery(b, p, discardLoggerDiscovery())

	require.NoError(t, pd.PublishIfChanged(context.Background(), "v5.2.0"))
	require.Len(t, p.snapshot(), 3)

	require.NoError(t, pd.PublishIfChanged(context.Background(), "v5.2.0"))
	assert.Len(t, p.snapshot(), 3, "second call should not publish anything new")
	assert.Equal(t, 1, b.callCount())
}

func TestPublishIfChanged_PublishesOnFirstCall(t *testing.T) {
	t.Parallel()
	b := &fakeDiscoveryBuilder{configs: sampleConfigs()}
	p := &fakePublisher{}
	pd := usecase.NewPublishDiscovery(b, p, discardLoggerDiscovery())

	err := pd.PublishIfChanged(context.Background(), "v5.2.0")
	require.NoError(t, err)
	assert.Len(t, p.snapshot(), 3)
}

func TestPublishIfChanged_PublishesOnFirmwareChange(t *testing.T) {
	t.Parallel()
	b := &fakeDiscoveryBuilder{configs: sampleConfigs()}
	p := &fakePublisher{}
	pd := usecase.NewPublishDiscovery(b, p, discardLoggerDiscovery())

	require.NoError(t, pd.PublishIfChanged(context.Background(), "v5.2.0"))
	require.NoError(t, pd.PublishIfChanged(context.Background(), "v5.2.1"))
	assert.Len(t, p.snapshot(), 6)
	assert.Equal(t, 2, b.callCount())
}

func TestPublishIfChanged_EmptyFirmwareOnFirstCall_Skips(t *testing.T) {
	t.Parallel()
	b := &fakeDiscoveryBuilder{configs: sampleConfigs()}
	p := &fakePublisher{}
	pd := usecase.NewPublishDiscovery(b, p, discardLoggerDiscovery())

	require.NoError(t, pd.PublishIfChanged(context.Background(), ""))
	assert.Empty(t, p.snapshot())
	assert.Equal(t, 0, b.callCount())
}

func TestPublish_LogsCount(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	pd := usecase.NewPublishDiscovery(&fakeDiscoveryBuilder{configs: sampleConfigs()}, &fakePublisher{}, logger)
	require.NoError(t, pd.Publish(context.Background(), "v5.2.0"))
	out := buf.String()
	assert.Contains(t, out, "discovery published")
	assert.Contains(t, out, `"count":3`)
	assert.Contains(t, out, `"firmware":"v5.2.0"`)
}

func TestPublish_LogsErrorsCount(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	p := &fakePublisher{errAt: map[string]error{"ha/sensor/b/config": errors.New("nope")}}
	pd := usecase.NewPublishDiscovery(&fakeDiscoveryBuilder{configs: sampleConfigs()}, p, logger)
	require.Error(t, pd.Publish(context.Background(), "v5.2.0"))
	out := buf.String()
	assert.Contains(t, out, "discovery publish completed with errors")
	assert.Contains(t, out, `"errors":1`)
}
```

---

### 2. Publish state use case

#### File: `application/usecase/publish_state.go`

```go
package usecase

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"
)

// Topics produces MQTT topic strings for state messages keyed by HA object_id.
// The canonical implementation lives in infrastructure/mqtt; this interface
// keeps the use case independent of the topic-builder implementation.
type Topics interface {
	// State returns the state topic for the supplied HA object_id (e.g.
	// "supply_temperature" → "<prefix>/state/supply_temperature").
	State(objectID string) string
}

// stateMsg is one (topic, payload) pair produced by buildMessages, plus the
// HA object_id for log correlation. Internal to the use case.
type stateMsg struct {
	topic    string
	payload  []byte
	objectID string
}

// PublishState transforms a domain Snapshot into per-entity MQTT state messages
// and publishes only the deltas relative to the last successful publish. A
// single Apply call may emit zero, one, or many publishes depending on what
// changed since the previous snapshot.
type PublishState struct {
	publisher   Publisher
	topics      Topics
	logger      *slog.Logger
	mu          sync.Mutex
	lastPayload map[string][]byte
}

// NewPublishState constructs a PublishState. A nil publisher or nil topics
// builder is a programming error and panics. A nil logger falls back to
// slog.Default. The internal delta cache is initialised empty.
func NewPublishState(pub Publisher, topics Topics, logger *slog.Logger) *PublishState {
	if pub == nil {
		panic("publish state: publisher must not be nil")
	}
	if topics == nil {
		panic("publish state: topics must not be nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &PublishState{
		publisher:   pub,
		topics:      topics,
		logger:      logger,
		lastPayload: make(map[string][]byte),
	}
}

// Apply publishes every state message whose payload differs from the value
// last successfully published on the same topic. Identical payloads are
// silently skipped (DEBUG-logged). Publish errors are aggregated via
// errors.Join; failed topics are NOT inserted into the cache, so the next
// Apply will retry them. Messages are sent with retained=false because the
// AvailabilityManager (story 008) drives offline transitions out-of-band.
func (p *PublishState) Apply(ctx context.Context, snap entity.Snapshot) error {
	msgs := p.buildMessages(snap)

	p.mu.Lock()
	defer p.mu.Unlock()

	var (
		errs      []error
		published int
		skipped   int
	)
	for _, m := range msgs {
		if prev, ok := p.lastPayload[m.topic]; ok && bytes.Equal(prev, m.payload) {
			skipped++
			p.logger.Debug("state skip: payload unchanged", "object_id", m.objectID, "topic", m.topic)
			continue
		}
		if err := p.publisher.Publish(ctx, m.topic, m.payload, false); err != nil {
			errs = append(errs, fmt.Errorf("publish state %s: %w", m.objectID, err))
			continue
		}
		p.lastPayload[m.topic] = append([]byte(nil), m.payload...)
		published++
	}

	if len(errs) > 0 {
		p.logger.Warn("state publish completed with errors",
			"published", published,
			"skipped", skipped,
			"errors", len(errs),
		)
		return errors.Join(errs...)
	}
	if published > 0 {
		p.logger.Info("state published", "count", published, "skipped", skipped)
	}
	return nil
}

// buildMessages returns the full ordered list of state messages for snap. Pure
// function: no I/O, no goroutines, deterministic ordering for stable tests.
func (p *PublishState) buildMessages(snap entity.Snapshot) []stateMsg {
	return []stateMsg{
		{
			topic:    p.topics.State("supply_temperature"),
			payload:  []byte(formatTemperature(snap.SupplyTemp)),
			objectID: "supply_temperature",
		},
		{
			topic:    p.topics.State("filter_clog"),
			payload:  []byte(strconv.FormatInt(int64(snap.FilterPct), 10)),
			objectID: "filter_clog",
		},
		{
			topic:    p.topics.State("heat_demand"),
			payload:  []byte(strconv.FormatUint(uint64(snap.PIDDemand), 10)),
			objectID: "heat_demand",
		},
		{
			topic:    p.topics.State("supply_fan_speed"),
			payload:  []byte(strconv.FormatUint(uint64(snap.FanState1), 10)),
			objectID: "supply_fan_speed",
		},
		{
			topic:    p.topics.State("exhaust_fan_speed"),
			payload:  []byte(strconv.FormatUint(uint64(snap.FanState2), 10)),
			objectID: "exhaust_fan_speed",
		},
		{
			topic:    p.topics.State("room_temperature"),
			payload:  []byte(formatTemperature(snap.RoomTemp)),
			objectID: "room_temperature",
		},
		{
			topic:    p.topics.State("room_humidity"),
			payload:  []byte(strconv.FormatUint(uint64(snap.RoomHumidity), 10)),
			objectID: "room_humidity",
		},
		{
			topic:    p.topics.State("firmware"),
			payload:  []byte(snap.Firmware.String()),
			objectID: "firmware",
		},
		{
			topic:    p.topics.State("device_id"),
			payload:  []byte(fmt.Sprintf("0x%04X", snap.DeviceID)),
			objectID: "device_id",
		},
		{
			topic:    p.topics.State("operation_time_left"),
			payload:  []byte(strconv.Itoa(int(snap.OperationTimeLeft.Seconds()))),
			objectID: "operation_time_left",
		},
		{
			topic:    p.topics.State("power"),
			payload:  []byte(boolToOnOff(snap.PowerOn)),
			objectID: "power",
		},
		{
			topic:    p.topics.State("heater_active"),
			payload:  []byte(boolToOnOff(snap.HeaterPWM)),
			objectID: "heater_active",
		},
		{
			topic:    p.topics.State("damper_open"),
			payload:  []byte(boolToOpenClosed(snap.DamperOpen)),
			objectID: "damper_open",
		},
		{
			topic:    p.topics.State("problem"),
			payload:  []byte(problemValue(snap)),
			objectID: "problem",
		},
		{
			topic:    p.topics.State("hvac_mode"),
			payload:  []byte(hvacMode(snap)),
			objectID: "hvac_mode",
		},
		{
			topic:    p.topics.State("target_temperature"),
			payload:  []byte(formatTemperature(snap.TargetTemp)),
			objectID: "target_temperature",
		},
		{
			topic:    p.topics.State("fan_target"),
			payload:  []byte(strconv.FormatUint(uint64(snap.FanTarget1), 10)),
			objectID: "fan_target",
		},
		{
			topic:    p.topics.State("hvac_action"),
			payload:  []byte(hvacAction(snap)),
			objectID: "hvac_action",
		},
	}
}

// formatTemperature renders t with exactly one decimal place. A zero-value
// Temperature yields "0.0"; HA accepts that as a valid sensor reading and the
// bridge publishes whatever the controller reports.
func formatTemperature(t valueobject.Temperature) string {
	return fmt.Sprintf("%.1f", t.Celsius())
}

// boolToOnOff renders b as the canonical HA payload pair "ON"/"OFF" used by
// switches and most binary sensors.
func boolToOnOff(b bool) string {
	if b {
		return "ON"
	}
	return "OFF"
}

// boolToOpenClosed renders b as "OPEN"/"CLOSED" — the payload pair used by HA
// binary sensors with device_class=opening for the air damper.
func boolToOpenClosed(b bool) string {
	if b {
		return "OPEN"
	}
	return "CLOSED"
}

// hvacMode collapses the controller's mode + power state into HA's hvac_mode
// vocabulary. The Oasis Syberia ventilation hardware is heat-only; cool/auto
// modes report as fan_only because no cooling element exists.
func hvacMode(snap entity.Snapshot) string {
	if !snap.PowerOn {
		return "off"
	}
	if snap.CurrentMode == valueobject.ModeHeat {
		return "heat"
	}
	return "fan_only"
}

// hvacAction maps the snapshot to HA's hvac_action vocabulary. Priority order
// (top wins): power off → preheat transition → fan transitions → heater PWM
// active → fan running → idle. Matches plan section 6.
func hvacAction(snap entity.Snapshot) string {
	if !snap.PowerOn {
		return "off"
	}
	switch snap.Operation {
	case entity.OpPreheatCalorifier:
		return "preheating"
	case entity.OpStartFan, entity.OpFanCoastdown:
		return "fan"
	}
	if snap.HeaterPWM {
		return "heating"
	}
	if snap.FanState1 > 0 {
		return "fan"
	}
	return "idle"
}

// problemValue is "ON" when the snapshot decodes any active error flag,
// otherwise "OFF". HA renders it via a binary_sensor with device_class=problem.
func problemValue(snap entity.Snapshot) string {
	if snap.DecodeErrorSet().IsEmpty() {
		return "OFF"
	}
	return "ON"
}
```

#### File: `application/usecase/publish_state_test.go`

```go
package usecase_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/usecase"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"
)

type fakeTopics struct{}

func (fakeTopics) State(objectID string) string { return "test/state/" + objectID }

type statePublishCall struct {
	topic    string
	payload  []byte
	retained bool
}

type fakeStatePublisher struct {
	mu       sync.Mutex
	calls    []statePublishCall
	errByTop map[string]error
}

func (f *fakeStatePublisher) Publish(_ context.Context, topic string, payload []byte, retained bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, statePublishCall{
		topic:    topic,
		payload:  append([]byte(nil), payload...),
		retained: retained,
	})
	if err, ok := f.errByTop[topic]; ok {
		return err
	}
	return nil
}

func (f *fakeStatePublisher) snapshot() []statePublishCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]statePublishCall, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *fakeStatePublisher) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

func (f *fakeStatePublisher) findCall(topic string) (statePublishCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c.topic == topic {
			return c, true
		}
	}
	return statePublishCall{}, false
}

func discardLoggerState() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

func mustTempState(t *testing.T, c float64) valueobject.Temperature {
	t.Helper()
	temp, err := valueobject.NewTemperature(c)
	require.NoError(t, err)
	return temp
}

func sampleSnapshot(t *testing.T) entity.Snapshot {
	t.Helper()
	return entity.Snapshot{
		Firmware:          valueobject.FirmwareFromRaw(0x5200),
		DeviceID:          0x1234,
		PowerOn:           true,
		CurrentMode:       valueobject.ModeHeat,
		Operation:         entity.OpIdle,
		OperationTimeLeft: 90 * time.Second,
		TargetTemp:        mustTempState(t, 22.0),
		RoomTemp:          mustTempState(t, 21.5),
		SupplyTemp:        mustTempState(t, 23.0),
		FanTarget1:        5,
		FanState1:         5,
		FanState2:         3,
		HeaterPWM:         true,
		DamperOpen:        true,
		PIDDemand:         42,
		FilterPct:         15,
		RoomHumidity:      55,
	}
}

func TestNewPublishState_NilPublisher_Panics(t *testing.T) {
	t.Parallel()
	require.PanicsWithValue(t, "publish state: publisher must not be nil", func() {
		_ = usecase.NewPublishState(nil, fakeTopics{}, nil)
	})
}

func TestNewPublishState_NilTopics_Panics(t *testing.T) {
	t.Parallel()
	require.PanicsWithValue(t, "publish state: topics must not be nil", func() {
		_ = usecase.NewPublishState(&fakeStatePublisher{}, nil, nil)
	})
}

func TestNewPublishState_NilLogger_FallsBack(t *testing.T) {
	t.Parallel()
	ps := usecase.NewPublishState(&fakeStatePublisher{}, fakeTopics{}, nil)
	require.NotNil(t, ps)
	err := ps.Apply(context.Background(), sampleSnapshot(t))
	require.NoError(t, err)
}

func TestApply_PublishesAllOnFirstCall(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())

	err := ps.Apply(context.Background(), sampleSnapshot(t))
	require.NoError(t, err)

	calls := pub.snapshot()
	assert.Len(t, calls, 18)
	for _, c := range calls {
		assert.False(t, c.retained, "state messages must be non-retained, got %s", c.topic)
	}

	c, ok := pub.findCall("test/state/supply_temperature")
	require.True(t, ok)
	assert.Equal(t, []byte("23.0"), c.payload)

	c, ok = pub.findCall("test/state/device_id")
	require.True(t, ok)
	assert.Equal(t, []byte("0x1234"), c.payload)

	c, ok = pub.findCall("test/state/operation_time_left")
	require.True(t, ok)
	assert.Equal(t, []byte("90"), c.payload)

	c, ok = pub.findCall("test/state/power")
	require.True(t, ok)
	assert.Equal(t, []byte("ON"), c.payload)

	c, ok = pub.findCall("test/state/damper_open")
	require.True(t, ok)
	assert.Equal(t, []byte("OPEN"), c.payload)

	c, ok = pub.findCall("test/state/firmware")
	require.True(t, ok)
	assert.Equal(t, []byte("v5.2.0"), c.payload)

	c, ok = pub.findCall("test/state/hvac_mode")
	require.True(t, ok)
	assert.Equal(t, []byte("heat"), c.payload)

	c, ok = pub.findCall("test/state/hvac_action")
	require.True(t, ok)
	assert.Equal(t, []byte("heating"), c.payload)

	c, ok = pub.findCall("test/state/problem")
	require.True(t, ok)
	assert.Equal(t, []byte("OFF"), c.payload)
}

func TestApply_DeltaSkipsUnchanged(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)

	require.NoError(t, ps.Apply(context.Background(), snap))
	first := len(pub.snapshot())
	assert.Equal(t, 18, first)

	pub.reset()
	require.NoError(t, ps.Apply(context.Background(), snap))
	assert.Empty(t, pub.snapshot(), "second Apply with identical snapshot must publish nothing")
}

func TestApply_DeltaPublishesChanged(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)

	require.NoError(t, ps.Apply(context.Background(), snap))
	pub.reset()

	snap.FanTarget1 = 7
	require.NoError(t, ps.Apply(context.Background(), snap))

	calls := pub.snapshot()
	require.Len(t, calls, 1)
	assert.Equal(t, "test/state/fan_target", calls[0].topic)
	assert.Equal(t, []byte("7"), calls[0].payload)
}

func TestApply_PublishError_DoesNotUpdateLastPayload(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("publish boom")
	pub := &fakeStatePublisher{errByTop: map[string]error{"test/state/fan_target": sentinel}}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)

	err := ps.Apply(context.Background(), snap)
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)

	pub.reset()
	pub.mu.Lock()
	pub.errByTop = nil
	pub.mu.Unlock()

	err = ps.Apply(context.Background(), snap)
	require.NoError(t, err)
	c, ok := pub.findCall("test/state/fan_target")
	require.True(t, ok, "second Apply should retry the previously failed topic")
	assert.Equal(t, []byte("5"), c.payload)
	assert.Len(t, pub.snapshot(), 1, "only the previously failed topic should be retried")
}

func TestApply_AggregatesErrors(t *testing.T) {
	t.Parallel()
	errA := errors.New("err a")
	errB := errors.New("err b")
	pub := &fakeStatePublisher{errByTop: map[string]error{
		"test/state/fan_target": errA,
		"test/state/hvac_mode":  errB,
	}}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())

	err := ps.Apply(context.Background(), sampleSnapshot(t))
	require.Error(t, err)
	assert.ErrorIs(t, err, errA)
	assert.ErrorIs(t, err, errB)
}

func TestHvacMode_PowerOff(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)
	snap.PowerOn = false

	require.NoError(t, ps.Apply(context.Background(), snap))
	c, ok := pub.findCall("test/state/hvac_mode")
	require.True(t, ok)
	assert.Equal(t, []byte("off"), c.payload)
}

func TestHvacMode_PowerOn_Heat(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)
	snap.PowerOn = true
	snap.CurrentMode = valueobject.ModeHeat

	require.NoError(t, ps.Apply(context.Background(), snap))
	c, ok := pub.findCall("test/state/hvac_mode")
	require.True(t, ok)
	assert.Equal(t, []byte("heat"), c.payload)
}

func TestHvacMode_PowerOn_OffMode(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)
	snap.PowerOn = true
	snap.CurrentMode = valueobject.ModeOff

	require.NoError(t, ps.Apply(context.Background(), snap))
	c, ok := pub.findCall("test/state/hvac_mode")
	require.True(t, ok)
	assert.Equal(t, []byte("fan_only"), c.payload)
}

func TestHvacMode_PowerOn_Cool(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)
	snap.PowerOn = true
	snap.CurrentMode = valueobject.ModeCool

	require.NoError(t, ps.Apply(context.Background(), snap))
	c, ok := pub.findCall("test/state/hvac_mode")
	require.True(t, ok)
	assert.Equal(t, []byte("fan_only"), c.payload)
}

func TestHvacMode_PowerOn_Auto(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)
	snap.PowerOn = true
	snap.CurrentMode = valueobject.ModeAuto

	require.NoError(t, ps.Apply(context.Background(), snap))
	c, ok := pub.findCall("test/state/hvac_mode")
	require.True(t, ok)
	assert.Equal(t, []byte("fan_only"), c.payload)
}

func TestHvacAction_PowerOff(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)
	snap.PowerOn = false

	require.NoError(t, ps.Apply(context.Background(), snap))
	c, ok := pub.findCall("test/state/hvac_action")
	require.True(t, ok)
	assert.Equal(t, []byte("off"), c.payload)
}

func TestHvacAction_PreheatTransition(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)
	snap.Operation = entity.OpPreheatCalorifier
	snap.HeaterPWM = false
	snap.FanState1 = 0

	require.NoError(t, ps.Apply(context.Background(), snap))
	c, ok := pub.findCall("test/state/hvac_action")
	require.True(t, ok)
	assert.Equal(t, []byte("preheating"), c.payload)
}

func TestHvacAction_StartFanTransition(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)
	snap.Operation = entity.OpStartFan
	snap.HeaterPWM = false

	require.NoError(t, ps.Apply(context.Background(), snap))
	c, ok := pub.findCall("test/state/hvac_action")
	require.True(t, ok)
	assert.Equal(t, []byte("fan"), c.payload)
}

func TestHvacAction_FanCoastdown(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)
	snap.Operation = entity.OpFanCoastdown
	snap.HeaterPWM = true

	require.NoError(t, ps.Apply(context.Background(), snap))
	c, ok := pub.findCall("test/state/hvac_action")
	require.True(t, ok)
	assert.Equal(t, []byte("fan"), c.payload, "fan transitions outrank heater PWM")
}

func TestHvacAction_HeaterPWM(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)
	snap.Operation = entity.OpIdle
	snap.HeaterPWM = true
	snap.FanState1 = 5

	require.NoError(t, ps.Apply(context.Background(), snap))
	c, ok := pub.findCall("test/state/hvac_action")
	require.True(t, ok)
	assert.Equal(t, []byte("heating"), c.payload)
}

func TestHvacAction_FanRunning(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)
	snap.Operation = entity.OpIdle
	snap.HeaterPWM = false
	snap.FanState1 = 4

	require.NoError(t, ps.Apply(context.Background(), snap))
	c, ok := pub.findCall("test/state/hvac_action")
	require.True(t, ok)
	assert.Equal(t, []byte("fan"), c.payload)
}

func TestHvacAction_Idle(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)
	snap.Operation = entity.OpIdle
	snap.HeaterPWM = false
	snap.FanState1 = 0

	require.NoError(t, ps.Apply(context.Background(), snap))
	c, ok := pub.findCall("test/state/hvac_action")
	require.True(t, ok)
	assert.Equal(t, []byte("idle"), c.payload)
}

func TestProblem_NoErrors(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)
	snap.RawErrors = [4]uint16{0, 0, 0, 0}

	require.NoError(t, ps.Apply(context.Background(), snap))
	c, ok := pub.findCall("test/state/problem")
	require.True(t, ok)
	assert.Equal(t, []byte("OFF"), c.payload)
}

func TestProblem_WithErrors(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)
	snap.RawErrors[0] = 0xFFFF

	require.NoError(t, ps.Apply(context.Background(), snap))
	c, ok := pub.findCall("test/state/problem")
	require.True(t, ok)
	assert.Equal(t, []byte("ON"), c.payload)
}

func TestApply_ZeroSnapshot_PublishesDefaults(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())

	require.NoError(t, ps.Apply(context.Background(), entity.Snapshot{}))
	calls := pub.snapshot()
	assert.Len(t, calls, 18)

	c, ok := pub.findCall("test/state/firmware")
	require.True(t, ok)
	assert.Equal(t, []byte("unknown"), c.payload)

	c, ok = pub.findCall("test/state/device_id")
	require.True(t, ok)
	assert.Equal(t, []byte("0x0000"), c.payload)

	c, ok = pub.findCall("test/state/power")
	require.True(t, ok)
	assert.Equal(t, []byte("OFF"), c.payload)

	c, ok = pub.findCall("test/state/damper_open")
	require.True(t, ok)
	assert.Equal(t, []byte("CLOSED"), c.payload)

	c, ok = pub.findCall("test/state/hvac_mode")
	require.True(t, ok)
	assert.Equal(t, []byte("off"), c.payload)

	c, ok = pub.findCall("test/state/hvac_action")
	require.True(t, ok)
	assert.Equal(t, []byte("off"), c.payload)

	c, ok = pub.findCall("test/state/supply_temperature")
	require.True(t, ok)
	assert.Equal(t, []byte("0.0"), c.payload)
}

func TestApply_LogsPublishedCount(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	ps := usecase.NewPublishState(&fakeStatePublisher{}, fakeTopics{}, logger)
	require.NoError(t, ps.Apply(context.Background(), sampleSnapshot(t)))
	out := buf.String()
	assert.Contains(t, out, "state published")
	assert.Contains(t, out, `"count":18`)
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
    You are an IMPLEMENTATION AGENT for story 012.

    ABSOLUTE RULES:
    - Copy code BYTE-FOR-BYTE.
    - DO NOT modify, fix, add `//nolint`. STOP on failure.

    PROCESS:
    1. Read story.
    2. Write each "#### File: `path`" block (4 files).
    3. From work dir:
       a. gofmt -l application/usecase/   (empty)
       b. go build ./...
       c. go vet ./...
       d. go test -short ./...
       e. go test -race ./application/usecase/...
       f. coverage: go test -coverpkg=./application/usecase -coverprofile=/tmp/oasis_uc.out ./application/usecase/...
          → go tool cover -func=/tmp/oasis_uc.out | tail -1   ≥90%
       g. golangci-lint run ./...   (0 issues)
    4. PASS → status review.
    5. FAIL → append errors verbatim to Issues Found, status in_progress, STOP.

    Work dir: /Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/
```

### Fix Agent Instructions

Standard.

---

## Implementation Notes

### Progress

- [ ] `application/usecase/publish_discovery.go` (+ test)
- [ ] `application/usecase/publish_state.go` (+ test)
- [ ] gofmt clean
- [ ] go build/vet pass
- [ ] go test pass
- [ ] -race pass
- [ ] coverage ≥90%
- [ ] lint clean

### Verification Results

```
gofmt:                       PASS
go build:                    PASS
go vet:                      PASS
go test:                     PASS
go test -race:               PASS
application/usecase cov:     98.2%
golangci-lint:               PASS
```

### Issues Found

[Implementation Agent appends here]

### Fixes Applied

[Fix Agent documents fixes here]

---

## Files Changed

- `application/usecase/publish_discovery.go`
- `application/usecase/publish_discovery_test.go`
- `application/usecase/publish_state.go`
- `application/usecase/publish_state_test.go`
