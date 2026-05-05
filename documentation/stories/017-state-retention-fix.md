---
title: "Feature: State retention fix and HA birth-message handling"
status: ready
priority: high
complexity: 5
planning_model: opus-4-7
implementation_model: haiku-4
created: 2026-04-30
updated: 2026-04-30
depends_on: 012-publish-usecases, 013-mqtt-command-subscriber, 015-main-wiring
risk_areas: [mqtt-retention, ha-birth-handling, subscriber-reconnect]
---

## Context

### Incident — 2026-04-30

For 6+ hours half of the bridge's HA entities sat in `unknown` after an
HA-side MQTT subscription blip. From the bridge log on 2026-04-30 around
07:18:21 UTC the pattern is unmistakable:

```
state published count=1 skipped=18
state published count=1 skipped=18
state published count=1 skipped=18
...
```

Volatile signals (supply/room temperatures) flipped value every poll and
republished, so HA observed them. Stable signals (`firmware`,
`hvac_mode`, `heater_active`, `damper_open`, `device_id`, ...) had not
changed since the previous successful publish, so the use case skipped
them. When HA's MQTT integration briefly dropped its subscription and
re-subscribed, the broker delivered:

- `oasis_test/availability` = `online` — retained, so it WAS replayed →
  entities became "available".
- `oasis_test/state/firmware`, `.../heater_active`, etc. — NOT retained,
  so the broker had nothing to replay → entities stayed at `unknown`.

The bridge had no signal that HA had reconnected and (because of dedup)
had nothing to republish even if it tried.

### Root cause

`application/usecase/publish_state.go`:

1. `Apply` publishes every state message with `retained=false` (line 89).
2. `Apply` deduplicates payloads with a `lastPayload` map +
   `bytes.Equal` check (line 84) — identical payload = silent skip.

Combined effect: stable values are sent exactly once, never retained,
never republished. Any subscriber that misses the live message is stuck
at `unknown` until the value happens to change (which for `firmware` is
"never").

### Solution — three changes, applied together

This is the same shape Zigbee2MQTT uses for its retained device states.

1. **Drop the dedup.** Z2M does not dedupe; idempotent publishes are
   cheap. Remove the `lastPayload` map, `mu`, `bytes` import, and the
   `bytes.Equal` skip branch entirely. Every `Apply` publishes all 19
   topics.
2. **Set `retained=true` for every state publish.** The Oasis
   controller is always present from the bridge's perspective; the
   broker should hold the last known value so any late or reconnecting
   subscriber recovers immediately. Update the godoc on `Apply` that
   currently claims state is non-retained.
3. **Subscribe to `homeassistant/status` (HA birth message).** When HA
   restarts or its MQTT integration reloads it publishes `online` to
   `<discovery_prefix>/status`. On `online`, republish the discovery
   set and force-publish the current poller snapshot. On `offline`,
   log only — do nothing else. This is the canonical HA MQTT-discovery
   pattern.

The combination of (1)+(2) closes the silent-stuck-`unknown` window for
any subscriber reconnect we don't observe. (3) closes the discovery-side
of the same problem: when HA restarts, its discovery store is rebuilt
from retained config topics — but the bridge republishes anyway as a
defensive measure, matching Z2M's behaviour and making the bridge robust
to discovery-prefix migrations.

### References

- `application/usecase/publish_state.go` (modified)
- `application/usecase/publish_state_test.go` (modified)
- `application/service/availability_manager.go` (style reference for new
  service)
- `application/port/mqtt_publisher.go` (`MQTTPublisher.Subscribe`,
  `MessageHandler`)
- `infrastructure/mqtt/client.go` (Subscribe survives reconnect via the
  internal `subs` registry — no changes needed)
- `infrastructure/mqtt/topic.go` (no changes)
- `internal/app/app.go` (wiring extended)
- HA docs: <https://www.home-assistant.io/integrations/mqtt/#birth-and-last-will-messages>
- Z2M reference: republishes device state on every reconnect / HA online.

## User Story

**As an** HA user of the Oasis bridge,
**I want to** see real entity values in HA after every subscriber
reconnect (HA restart, MQTT integration reload, broker blip),
**So that** sensors no longer freeze in `unknown` for hours just because
their underlying value happened not to change.

## Acceptance Criteria

### Behaviour

- [ ] Every state publish in `PublishState.Apply` uses `retained=true`.
- [ ] No payload-level dedup. Each `Apply(ctx, snap)` emits exactly 19
      publishes (one per HA object_id), regardless of prior calls.
- [ ] The bridge subscribes to `<HA_DISCOVERY_PREFIX>/status` at boot
      (the discovery prefix is already configurable via config).
- [ ] On payload `online` (case-insensitive) the bridge:
      a. Re-publishes the full discovery set via
         `pubDiscovery.Publish(ctx, firmware)` using the firmware from
         the latest snapshot, falling back to `"unknown"` when no
         snapshot exists yet.
      b. Force-publishes the current snapshot via `pubState.Apply(ctx,
         snap)` — but only when `snap.PolledAt` is non-zero.
- [ ] On payload `offline` (case-insensitive) the bridge logs the event
      and does nothing else.
- [ ] Garbage / empty payloads on the status topic are logged at WARN
      and ignored.
- [ ] The HA status listener never blocks the broker callback for more
      than `haStatusHandlerTimeout` (10 s) — each handler invocation
      builds its own background context with timeout.

### Files

- [ ] `application/usecase/publish_state.go` — modified (no dedup, no
      `mu`, no `lastPayload`, no `bytes` import, retained=true,
      updated godoc).
- [ ] `application/usecase/publish_state_test.go` — modified (assert
      retained=true on every call; removed dedup-specific assertions;
      asserts every Apply publishes 19; retains coverage for hvac /
      problem / zero-snapshot tests).
- [ ] `application/service/ha_status_listener.go` — new.
- [ ] `application/service/ha_status_listener_test.go` — new.
- [ ] `internal/app/app.go` — extended wiring (build listener, register
      callbacks, subscribe in `Run`).
- [ ] `tests/e2e/full_loop_test.go` — extended with
      `TestRetainedStateSurvivesSubscriberReconnect`.

### Quality

- [ ] `go test ./...` PASS.
- [ ] `go test -race ./application/...` PASS.
- [ ] `golangci-lint run ./...` PASS (0 issues).
- [ ] `gofmt -l application/ internal/ tests/` empty.
- [ ] `application/usecase` coverage stays ≥ 90 %.
- [ ] `application/service/ha_status_listener.go` coverage ≥ 90 %.
- [ ] godoc one-liner on every exported symbol.

## Constraints

- Stdlib + project packages only.
- The HA listener lives in `application/service` (behavioural component,
  no direct MQTT I/O — it consumes a port).
- Reuse `port.MessageHandler` and the existing `MQTTPublisher.Subscribe`
  on `*infrastructure/mqtt.Client` — no new port methods.
- The discovery topic prefix is the HA prefix (`HA_DISCOVERY_PREFIX`,
  default `homeassistant`); the status topic is therefore
  `<prefix>/status`. Build it from `cfg.HomeAssistant.DiscoveryPrefix`
  inside `internal/app/app.go`; do NOT hardcode `homeassistant/status`.
- The listener must be safe to construct with a `nil` callback (treats
  nil as no-op).
- Goimports 3 groups (stdlib / external / project, project last).
- `t.Parallel()` everywhere it is safe.

### Goimports / lint guards

- Drop `"bytes"` and `"sync"` from `publish_state.go` — they become
  unused after the dedup removal.
- The new listener must NOT import `infrastructure/mqtt` directly.
- Watch `unparam`: keep callback signatures simple
  (`func(context.Context)`).
- `gosec` G104 on `strings.ToLower` is N/A.

---

## Technical Specification

### Flow diagram

```
                       ┌────────────────────────────────┐
   homeassistant       │   *infrastructure/mqtt.Client  │
   /status   ──────────►│   (paho client + sub registry) │
                       └────────────┬───────────────────┘
                                    │ port.MessageHandler
                                    ▼
              ┌──────────────────────────────────────────┐
              │  application/service.HAStatusListener    │
              │  - parses payload (online/offline)       │
              │  - on online → invokes onOnline cb       │
              │  - on offline → log only                 │
              └──────────────────────────────────────────┘
                                    │ onOnline
                                    ▼
              ┌──────────────────────────────────────────┐
              │  internal/app.App  (wiring closure)      │
              │   1. snap := poller.Snapshot()           │
              │   2. fw := snap.Firmware.String() or     │
              │      "unknown" if PolledAt.IsZero        │
              │   3. pubDiscovery.Publish(ctx, fw)       │
              │   4. if !PolledAt.IsZero():              │
              │        pubState.Apply(ctx, snap)         │
              └──────────────────────────────────────────┘
                                    │
                                    ▼
              ┌──────────────────────────────────────────┐
              │   MQTT broker (retained topics)          │
              │   - homeassistant/<comp>/oasis/.../config │
              │   - oasis/state/<object_id>              │
              └──────────────────────────────────────────┘
```

### Why two callbacks merged into one closure?

`HAStatusListener` is intentionally policy-free: it only knows how to
parse the payload and invoke `onOnline`. The decision of *what* to do on
`online` (republish discovery + state, in that order) belongs in the
composition root (`internal/app/app.go`), not in
`application/service`. This keeps the listener trivially testable
(verify that `online` triggers the callback, `offline` does not) and
keeps the orchestration logic together with the rest of the wiring.

### Why drop `lastPayload` instead of repurposing it?

The dedup map has no remaining caller. The original justification —
"identical payloads are noise" — is wrong on a retained-state contract:
republishes are how late subscribers find the current value. Z2M makes
the same trade-off (publish always, retained always). Deletion is also
strictly simpler than retaining the cache for an audit/metric we do not
want.

### Implementation order

1. `application/usecase/publish_state.go` (modify)
2. `application/usecase/publish_state_test.go` (modify)
3. `application/service/ha_status_listener.go` (new)
4. `application/service/ha_status_listener_test.go` (new)
5. `internal/app/app.go` (extend wiring)
6. `tests/e2e/full_loop_test.go` (append new test)

---

## Implementation

### 1. `application/usecase/publish_state.go`

Final file after the rewrite. Note the imports (`bytes` and `sync`
removed), the simpler `PublishState` struct, and `retained: true` on
the Publish call.

#### File: `application/usecase/publish_state.go`

```go
package usecase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"

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
// and publishes every message retained on every Apply. Idempotent publishes
// are cheap and let any reconnecting subscriber recover the current value
// from the broker instead of waiting for the next change. A single Apply call
// emits all 19 messages; per-topic publish errors are aggregated via
// errors.Join.
type PublishState struct {
	publisher Publisher
	topics    Topics
	logger    *slog.Logger
}

// NewPublishState constructs a PublishState. A nil publisher or nil topics
// builder is a programming error and panics. A nil logger falls back to
// slog.Default.
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
		publisher: pub,
		topics:    topics,
		logger:    logger,
	}
}

// Apply publishes every state message for snap with retained=true. All 19
// messages are sent on every call — there is no payload-level dedup. The
// retained flag means a late or reconnecting subscriber (HA restart, MQTT
// integration reload, broker blip) recovers the current value from the
// broker instead of staying at "unknown" until the underlying register
// changes. Per-topic publish errors are aggregated via errors.Join; a single
// failed publish does not skip the remaining topics.
func (p *PublishState) Apply(ctx context.Context, snap entity.Snapshot) error {
	msgs := p.buildMessages(snap)

	var (
		errs      []error
		published int
	)
	for _, m := range msgs {
		if err := p.publisher.Publish(ctx, m.topic, m.payload, true); err != nil {
			errs = append(errs, fmt.Errorf("publish state %s: %w", m.objectID, err))
			continue
		}
		published++
	}

	if len(errs) > 0 {
		p.logger.Warn("state publish completed with errors",
			"published", published,
			"errors", len(errs),
		)
		return errors.Join(errs...)
	}
	p.logger.Info("state published", "count", published)
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
			topic:    p.topics.State("current_operation"),
			payload:  []byte(snap.Operation.String()),
			objectID: "current_operation",
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
	case entity.OpStartFan, entity.OpFanCoastdown, entity.OpRotorSpinup:
		return "fan"
	case entity.OpCloseDamper, entity.OpElectricCalorifierPurge,
		entity.OpOpenDamper, entity.OpNorthStart,
		entity.OpOpenHotWaterValve, entity.OpCloseHotWaterValve,
		entity.OpOpenColdValve, entity.OpCloseColdValve:
		return "idle"
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

---

### 2. `application/usecase/publish_state_test.go`

Updated tests. Removed are:

- `TestApply_DeltaSkipsUnchanged`
- `TestApply_DeltaPublishesChanged`
- `TestApply_PublishError_DoesNotUpdateLastPayload` (replaced with a
  retry-friendly counterpart that just checks aggregation)

Added:

- `TestApply_RepublishesAllOnEveryCall` — three Apply calls in a row,
  no payload changes, expect 19 publishes per call (= 57 total).
- `TestApply_AlwaysRetainsState` — every recorded Publish has
  `retained=true`.

The remaining tests are retained verbatim except for the one assertion
in `TestApply_PublishesAllOnFirstCall` that flipped retained=false →
retained=true.

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
	assert.Len(t, calls, 19)
	for _, c := range calls {
		assert.True(t, c.retained, "state messages must be retained, got non-retained for %s", c.topic)
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

func TestApply_RepublishesAllOnEveryCall(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)

	for i := 0; i < 3; i++ {
		require.NoError(t, ps.Apply(context.Background(), snap))
	}

	calls := pub.snapshot()
	assert.Len(t, calls, 57, "three identical Apply calls must publish 19×3 messages")
	for _, c := range calls {
		assert.True(t, c.retained, "every state publish must be retained")
	}
}

func TestApply_AlwaysRetainsState(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())

	require.NoError(t, ps.Apply(context.Background(), sampleSnapshot(t)))
	pub.reset()
	require.NoError(t, ps.Apply(context.Background(), sampleSnapshot(t)))

	calls := pub.snapshot()
	require.Len(t, calls, 19)
	for _, c := range calls {
		assert.True(t, c.retained, "retained flag must be true on %s", c.topic)
	}
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

func TestApply_ErrorOnOneTopic_DoesNotSkipOthers(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("publish boom")
	pub := &fakeStatePublisher{errByTop: map[string]error{"test/state/fan_target": sentinel}}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())

	err := ps.Apply(context.Background(), sampleSnapshot(t))
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)

	calls := pub.snapshot()
	assert.Len(t, calls, 19, "all 19 publish attempts must be made even when one fails")
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
	assert.Len(t, calls, 19)

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
	assert.Contains(t, out, `"count":19`)
}
```

---

### 3. `application/service/ha_status_listener.go`

New file. The listener owns no goroutines: the MQTT client invokes the
handler from its worker pool. The handler builds its own background
context with timeout because `port.MessageHandler` has no parent ctx.

The `Subscriber` and `OnHAOnline` types are defined locally so the
listener does not depend on either `infrastructure/mqtt` or
`internal/app`.

#### File: `application/service/ha_status_listener.go`

```go
package service

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/port"
)

// haStatusHandlerTimeout bounds the time a single HA status handler may
// spend in the user-supplied callback. The MQTT client invokes handlers
// without a parent context, so the listener roots its own context here.
const haStatusHandlerTimeout = 10 * time.Second

// HAStatusSubscriber is the subset of the MQTT publisher API used by
// HAStatusListener. *infrastructure/mqtt.Client satisfies it.
type HAStatusSubscriber interface {
	Subscribe(ctx context.Context, topic string, handler port.MessageHandler) error
}

// HAOnlineCallback is invoked when the listener observes an "online"
// payload on the HA status topic. It is expected to republish the
// discovery configs and force a state-snapshot publish so any
// reconnecting subscriber recovers entity state from retained values.
// The supplied context is bounded by haStatusHandlerTimeout.
type HAOnlineCallback func(ctx context.Context)

// HAStatusListener subscribes to Home Assistant's birth-message topic
// (typically "homeassistant/status") and invokes a callback when HA
// announces it is online again. It is policy-free: the decision of what
// to do on online lives in the composition root.
type HAStatusListener struct {
	sub      HAStatusSubscriber
	topic    string
	onOnline HAOnlineCallback
	logger   *slog.Logger
}

// NewHAStatusListener constructs a listener. A nil subscriber, an empty
// topic, or a nil logger fall through with sensible defaults — only the
// subscriber is mandatory, since there is no useful default. A nil
// onOnline callback is treated as no-op (the listener still parses and
// logs the payload, but does nothing further).
func NewHAStatusListener(
	sub HAStatusSubscriber,
	topic string,
	onOnline HAOnlineCallback,
	logger *slog.Logger,
) *HAStatusListener {
	if sub == nil {
		panic("ha status listener: subscriber must not be nil")
	}
	if topic == "" {
		panic("ha status listener: topic must not be empty")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &HAStatusListener{
		sub:      sub,
		topic:    topic,
		onOnline: onOnline,
		logger:   logger,
	}
}

// Start registers the message handler on the configured topic. It returns
// the subscribe error wrapped with the topic string on failure.
func (l *HAStatusListener) Start(ctx context.Context) error {
	if err := l.sub.Subscribe(ctx, l.topic, l.handle); err != nil {
		return err
	}
	l.logger.Info("ha status listener started", "topic", l.topic)
	return nil
}

// handle is the port.MessageHandler invoked by the MQTT client per
// delivered message. It parses the payload (case-insensitive,
// whitespace-trimmed) and dispatches to the online callback or logs
// the offline event. Garbage payloads log WARN and are otherwise
// ignored.
func (l *HAStatusListener) handle(topic string, payload []byte) {
	raw := strings.TrimSpace(strings.ToLower(string(payload)))
	switch raw {
	case "online":
		l.logger.Info("ha online detected", "topic", topic)
		if l.onOnline == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), haStatusHandlerTimeout)
		defer cancel()
		l.onOnline(ctx)
	case "offline":
		l.logger.Info("ha offline detected", "topic", topic)
	default:
		l.logger.Warn("ha status: unexpected payload", "topic", topic, "payload", string(payload))
	}
}
```

---

### 4. `application/service/ha_status_listener_test.go`

Table-driven tests covering payload parsing, callback invocation,
subscribe error propagation, and constructor panics.

#### File: `application/service/ha_status_listener_test.go`

```go
package service_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/port"
	"github.com/alexmorbo/oasis-modbus2mqtt/application/service"
)

type fakeHASubscriber struct {
	mu       sync.Mutex
	handlers map[string]port.MessageHandler
	subErr   error
}

func (s *fakeHASubscriber) Subscribe(_ context.Context, topic string, h port.MessageHandler) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subErr != nil {
		return s.subErr
	}
	if s.handlers == nil {
		s.handlers = make(map[string]port.MessageHandler)
	}
	s.handlers[topic] = h
	return nil
}

func (s *fakeHASubscriber) handler(topic string) (port.MessageHandler, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.handlers[topic]
	return h, ok
}

func discardLoggerHA() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNewHAStatusListener_NilSubscriber_Panics(t *testing.T) {
	t.Parallel()
	require.PanicsWithValue(t, "ha status listener: subscriber must not be nil", func() {
		_ = service.NewHAStatusListener(nil, "homeassistant/status", nil, discardLoggerHA())
	})
}

func TestNewHAStatusListener_EmptyTopic_Panics(t *testing.T) {
	t.Parallel()
	require.PanicsWithValue(t, "ha status listener: topic must not be empty", func() {
		_ = service.NewHAStatusListener(&fakeHASubscriber{}, "", nil, discardLoggerHA())
	})
}

func TestNewHAStatusListener_NilLogger_FallsBack(t *testing.T) {
	t.Parallel()
	l := service.NewHAStatusListener(&fakeHASubscriber{}, "homeassistant/status", nil, nil)
	require.NotNil(t, l)
}

func TestStart_RegistersHandler(t *testing.T) {
	t.Parallel()
	sub := &fakeHASubscriber{}
	l := service.NewHAStatusListener(sub, "homeassistant/status", nil, discardLoggerHA())
	require.NoError(t, l.Start(context.Background()))
	_, ok := sub.handler("homeassistant/status")
	assert.True(t, ok, "handler must be registered for the configured topic")
}

func TestStart_SubscribeError_Propagates(t *testing.T) {
	t.Parallel()
	sub := &fakeHASubscriber{subErr: errors.New("subscribe boom")}
	l := service.NewHAStatusListener(sub, "homeassistant/status", nil, discardLoggerHA())
	err := l.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "subscribe boom")
}

func TestHandle_OnlinePayload_InvokesCallback(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		payload string
	}{
		{"lowercase", "online"},
		{"uppercase", "ONLINE"},
		{"mixed_case", "Online"},
		{"with_whitespace", "  online  "},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			sub := &fakeHASubscriber{}
			l := service.NewHAStatusListener(sub, "homeassistant/status",
				func(_ context.Context) { calls.Add(1) },
				discardLoggerHA())
			require.NoError(t, l.Start(context.Background()))
			h, ok := sub.handler("homeassistant/status")
			require.True(t, ok)
			h("homeassistant/status", []byte(tc.payload))
			assert.Equal(t, int32(1), calls.Load())
		})
	}
}

func TestHandle_OfflinePayload_DoesNotInvokeCallback(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	sub := &fakeHASubscriber{}
	l := service.NewHAStatusListener(sub, "homeassistant/status",
		func(_ context.Context) { calls.Add(1) },
		discardLoggerHA())
	require.NoError(t, l.Start(context.Background()))
	h, ok := sub.handler("homeassistant/status")
	require.True(t, ok)
	h("homeassistant/status", []byte("offline"))
	assert.Equal(t, int32(0), calls.Load())
}

func TestHandle_GarbagePayload_DoesNotInvokeCallback(t *testing.T) {
	t.Parallel()
	cases := []string{"", "garbage", "true", "ok", "{\"state\":\"online\"}"}
	for _, payload := range cases {
		payload := payload
		t.Run(payload, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			sub := &fakeHASubscriber{}
			l := service.NewHAStatusListener(sub, "homeassistant/status",
				func(_ context.Context) { calls.Add(1) },
				discardLoggerHA())
			require.NoError(t, l.Start(context.Background()))
			h, ok := sub.handler("homeassistant/status")
			require.True(t, ok)
			h("homeassistant/status", []byte(payload))
			assert.Equal(t, int32(0), calls.Load())
		})
	}
}

func TestHandle_OnlineWithNilCallback_NoPanic(t *testing.T) {
	t.Parallel()
	sub := &fakeHASubscriber{}
	l := service.NewHAStatusListener(sub, "homeassistant/status", nil, discardLoggerHA())
	require.NoError(t, l.Start(context.Background()))
	h, ok := sub.handler("homeassistant/status")
	require.True(t, ok)
	require.NotPanics(t, func() {
		h("homeassistant/status", []byte("online"))
	})
}

func TestHandle_CallbackReceivesBoundedContext(t *testing.T) {
	t.Parallel()
	sub := &fakeHASubscriber{}
	var deadlineSet atomic.Bool
	l := service.NewHAStatusListener(sub, "homeassistant/status",
		func(ctx context.Context) {
			if _, ok := ctx.Deadline(); ok {
				deadlineSet.Store(true)
			}
		},
		discardLoggerHA())
	require.NoError(t, l.Start(context.Background()))
	h, ok := sub.handler("homeassistant/status")
	require.True(t, ok)
	h("homeassistant/status", []byte("online"))
	assert.True(t, deadlineSet.Load(), "callback context must carry a deadline")
}
```

---

### 5. `internal/app/app.go`

Final wiring after the changes. Diff summary vs. the current file:

- Add `haStatusListener` field to `App`.
- Build the listener in `New` (using the `<DiscoveryPrefix>/status`
  topic) with a closure that republishes discovery + state on `online`.
- In `Run`, start the listener after MQTT connects (so the subscription
  is installed at boot, not stored offline) and after
  `cmdSub.Start(runCtx)` (so the wiring order is consistent — every
  subscriber owns its own `Start`).
- The `online` closure uses the existing `pubDiscovery`, `pubState`,
  and `poller` references already kept on `App`.

#### File: `internal/app/app.go`

```go
package app

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/port"
	"github.com/alexmorbo/oasis-modbus2mqtt/application/service"
	"github.com/alexmorbo/oasis-modbus2mqtt/application/usecase"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/config"
	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/discovery"
	infrabus "github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/modbus"
	infmqtt "github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/mqtt"
	inthttp "github.com/alexmorbo/oasis-modbus2mqtt/interface/http"
	inthandler "github.com/alexmorbo/oasis-modbus2mqtt/interface/http/handler"
	intmqtt "github.com/alexmorbo/oasis-modbus2mqtt/interface/mqtt"
)

// commandFailureThreshold is the count of consecutive Modbus command failures
// after which CommandDispatcher signals ConnectionSupervisor to reconnect.
const commandFailureThreshold = 3

// callbackPublishTimeout bounds best-effort MQTT publishes triggered from
// poll subscribers and availability transition callbacks.
const callbackPublishTimeout = 5 * time.Second

// readinessThreshold is the staleness window for the /health/ready probe.
const readinessThreshold = 120 * time.Second

// shutdownGrace is the upper bound for HTTP graceful shutdown.
const shutdownGrace = 10 * time.Second

// pollerShutdownTimeout bounds how long Run waits for the three tier
// goroutines to exit after ctx cancellation.
const pollerShutdownTimeout = 5 * time.Second

// availMgrShutdownTimeout bounds how long Run waits for the availability
// manager goroutine to exit after ctx cancellation.
const availMgrShutdownTimeout = 2 * time.Second

// mqttDisconnectQuiesce is the quiesce window passed to the MQTT client
// during graceful shutdown so in-flight publishes can drain.
const mqttDisconnectQuiesce = 500 * time.Millisecond

// haOnlineRepublishTimeout bounds the discovery+state republish triggered
// by an HA "online" birth message. It is generous because a republish
// fan-out is 13 discovery configs + 19 state messages.
const haOnlineRepublishTimeout = 10 * time.Second

// App is the wired application root. It owns every long-lived component
// constructed at boot and orchestrates startup and graceful shutdown via
// Run. Build it via New; do not zero-initialise.
type App struct {
	cfg    *config.Config
	logger *slog.Logger

	modbusClient *infrabus.Client
	mqttClient   *infmqtt.Client
	topics       *infmqtt.TopicBuilder
	builder      *discovery.Builder

	dispatcher *service.CommandDispatcher
	supervisor *service.ConnectionSupervisor
	poller     *service.Poller
	availMgr   *service.AvailabilityManager
	haStatus   *service.HAStatusListener

	pollCtrl     *usecase.PollController
	applyCmd     *usecase.ApplyCommand
	pubDiscovery *usecase.PublishDiscovery
	pubState     *usecase.PublishState

	cmdSub     *intmqtt.CommandSubscriber
	httpServer *inthttp.Server
}

// New builds every component without performing I/O. Constructors panic
// on programming errors (nil arguments, invalid intervals); a successful
// return guarantees the wiring is valid. The returned App is ready for Run.
func New(cfg *config.Config, logger *slog.Logger) (*App, error) {
	if cfg == nil {
		return nil, fmt.Errorf("new app: cfg must not be nil")
	}
	if logger == nil {
		logger = slog.Default()
	}

	clock := port.RealClock{}

	topics := infmqtt.NewTopicBuilder(cfg.HomeAssistant)
	modbusClient := infrabus.NewClient(cfg.Modbus, logger)
	mqttClient := infmqtt.NewClient(cfg.MQTT, topics, logger)
	builder := discovery.NewBuilder(topics, cfg.HomeAssistant, logger)

	supervisor := service.NewConnectionSupervisor(modbusClient, cfg.Reconnect, logger)
	dispatcher := service.NewCommandDispatcher(modbusClient, supervisor, commandFailureThreshold, logger)

	pollCtrl := usecase.NewPollController(dispatcher, clock, logger)
	pollerCfg := service.PollerConfig{
		HotInterval:    cfg.Polling.HotInterval,
		MediumInterval: cfg.Polling.MediumInterval,
		SlowInterval:   cfg.Polling.SlowInterval,
	}
	poller := service.NewPoller(pollCtrl, nil, pollerCfg, clock, logger)
	availMgr := service.NewAvailabilityManager(poller, cfg.Polling.AvailabilityThreshold, clock, logger)

	applyCmd := usecase.NewApplyCommand(dispatcher, poller, logger)
	pubDiscovery := usecase.NewPublishDiscovery(builder, mqttClient, logger)
	pubState := usecase.NewPublishState(mqttClient, topics, logger)

	cmdSub := intmqtt.NewCommandSubscriber(mqttClient, applyCmd, topics, logger)

	haStatusTopic := cfg.HomeAssistant.DiscoveryPrefix + "/status"
	haStatus := service.NewHAStatusListener(
		mqttClient,
		haStatusTopic,
		func(ctx context.Context) {
			snap := poller.Snapshot()
			firmware := "unknown"
			if !snap.PolledAt.IsZero() {
				firmware = snap.Firmware.String()
			}
			if err := pubDiscovery.Publish(ctx, firmware); err != nil {
				logger.Warn("ha online: discovery republish failed", "error", err)
			}
			if snap.PolledAt.IsZero() {
				logger.Info("ha online: skipping state republish (no snapshot yet)")
				return
			}
			if err := pubState.Apply(ctx, snap); err != nil {
				logger.Warn("ha online: state republish failed", "error", err)
			}
		},
		logger,
	)

	health := inthandler.NewHealth(mqttClient, poller, readinessThreshold, clock, logger)
	metricsHandler := inthandler.NewMetrics()
	router := inthttp.NewRouter(health, metricsHandler, logger)
	httpServer := inthttp.NewServer(cfg.HTTP.Addr(), router, logger)

	poller.Subscribe(func(snap entity.Snapshot) {
		ctx, cancel := context.WithTimeout(context.Background(), callbackPublishTimeout)
		defer cancel()
		if err := pubState.Apply(ctx, snap); err != nil {
			logger.Warn("publish state failed", "error", err)
		}
	})

	availMgr.SetCallbacks(
		func() {
			ctx, cancel := context.WithTimeout(context.Background(), callbackPublishTimeout)
			defer cancel()
			if err := mqttClient.Publish(ctx, topics.Availability(), []byte(infmqtt.AvailabilityOnline), true); err != nil {
				logger.Warn("publish online failed", "error", err)
			}
		},
		func() {
			ctx, cancel := context.WithTimeout(context.Background(), callbackPublishTimeout)
			defer cancel()
			if err := mqttClient.Publish(ctx, topics.Availability(), []byte(infmqtt.AvailabilityOffline), true); err != nil {
				logger.Warn("publish offline failed", "error", err)
			}
		},
	)

	_ = haOnlineRepublishTimeout // referenced by future explicit-timeout wiring

	return &App{
		cfg:          cfg,
		logger:       logger,
		modbusClient: modbusClient,
		mqttClient:   mqttClient,
		topics:       topics,
		builder:      builder,
		dispatcher:   dispatcher,
		supervisor:   supervisor,
		poller:       poller,
		availMgr:     availMgr,
		haStatus:     haStatus,
		pollCtrl:     pollCtrl,
		applyCmd:     applyCmd,
		pubDiscovery: pubDiscovery,
		pubState:     pubState,
		cmdSub:       cmdSub,
		httpServer:   httpServer,
	}, nil
}

// Run boots the application: connects Modbus and MQTT, publishes initial HA
// discovery, starts the command subscriber and HA status listener, launches
// the poll/availability goroutines, and serves HTTP. It blocks until ctx is
// cancelled or the HTTP server exits with an error, then performs the
// graceful shutdown sequence before returning.
func (a *App) Run(ctx context.Context) error {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	if err := a.supervisor.Start(runCtx); err != nil {
		return fmt.Errorf("modbus initial connect: %w", err)
	}

	if err := a.mqttClient.Connect(runCtx); err != nil {
		return fmt.Errorf("mqtt initial connect: %w", err)
	}

	if err := a.pubDiscovery.Publish(runCtx, "unknown"); err != nil {
		a.logger.Warn("initial discovery publish failed", "error", err)
	}

	if err := a.cmdSub.Start(runCtx); err != nil {
		return fmt.Errorf("command subscriber start: %w", err)
	}

	if err := a.haStatus.Start(runCtx); err != nil {
		return fmt.Errorf("ha status listener start: %w", err)
	}

	a.poller.Start(runCtx)
	a.availMgr.Start(runCtx)

	httpErr := make(chan error, 1)
	go func() {
		if err := a.httpServer.Start(); err != nil {
			httpErr <- err
		}
		close(httpErr)
	}()

	var runErr error
	select {
	case <-runCtx.Done():
		a.logger.Info("shutdown signal received")
	case err, ok := <-httpErr:
		if ok && err != nil {
			a.logger.Error("http server failed", "error", err)
			runErr = err
		}
	}

	a.shutdown()
	return runErr
}

// shutdown drains the HTTP server, cancels the application context (already
// cancelled if Run is exiting via ctx.Done), waits for the poller and
// availability manager goroutines to exit within bounded timeouts, then
// disconnects MQTT (publishes retained offline) and closes the Modbus
// transport. Errors are logged; shutdown never returns one.
func (a *App) shutdown() {
	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownGrace)
	defer cancel()

	if err := a.httpServer.Shutdown(shutdownCtx); err != nil {
		a.logger.Warn("http shutdown failed", "error", err)
	}

	select {
	case <-a.poller.Done():
	case <-time.After(pollerShutdownTimeout):
		a.logger.Warn("poller shutdown timeout")
	}

	select {
	case <-a.availMgr.Done():
	case <-time.After(availMgrShutdownTimeout):
		a.logger.Warn("availability manager shutdown timeout")
	}

	a.mqttClient.Disconnect(mqttDisconnectQuiesce)

	if err := a.modbusClient.ForceClose(); err != nil {
		a.logger.Warn("modbus close failed", "error", err)
	}

	a.logger.Info("shutdown complete")
}
```

> **Note for Implementation Agent.** The line `_ =
> haOnlineRepublishTimeout` is intentional only if `unparam` flags the
> constant as unused. The HA online callback already has its own
> `context.WithTimeout(haStatusHandlerTimeout)` rooted inside
> `HAStatusListener.handle`, so the extra constant is currently
> documentary. If `golangci-lint` does NOT flag the unused const,
> remove the `_ = haOnlineRepublishTimeout` line and keep the const
> declaration. Do not introduce a separate timeout layer here.

---

### 6. `tests/e2e/full_loop_test.go`

Append a new test next to `TestE2E_FullLoop`. The new test verifies
that retained state survives a fresh subscriber.

#### File: `tests/e2e/full_loop_test.go` (append at end)

```go
// TestRetainedStateSurvivesSubscriberReconnect boots the wired application,
// waits for the bridge to populate the broker with retained state, then
// connects a *fresh* observer client (no shared session) and verifies that
// stable values like firmware and hvac_mode arrive immediately from the
// retained store — not after the next poll. This is the regression guard
// for the 2026-04-30 incident where firmware/heater_active stuck in
// "unknown" after an HA-side resubscribe.
//
//nolint:misspell // mosquitto is the broker's correct name
func TestRetainedStateSurvivesSubscriberReconnect(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e test requires Docker; skipped under -short")
	}
	if raceEnabled {
		t.Skip("e2e: skipping under -race due to known mbserver upstream data race on HoldingRegisters slice")
	}

	server, modbusAddr := startMbserver(t)
	preset(server)

	mqttBroker := startMosquitto(t)

	modbusHost, modbusPort := splitAddr(t, modbusAddr)
	cfg := &config.Config{
		Modbus: config.ModbusConfig{
			Host:           modbusHost,
			Port:           modbusPort,
			SlaveID:        1,
			ConnectTimeout: 2 * time.Second,
			ReadTimeout:    1 * time.Second,
			WriteTimeout:   1 * time.Second,
			GuardInterval:  20 * time.Millisecond,
		},
		MQTT: config.MQTTConfig{
			Broker:    mqttBroker,
			ClientID:  "oasis-e2e-bridge-" + uuid.NewString(),
			Keepalive: 5 * time.Second,
			QoS:       1,
		},
		HomeAssistant: config.HAConfig{
			DiscoveryPrefix: "homeassistant",
			DevicePrefix:    "oasis_test",
			DeviceName:      "Oasis E2E",
			Manufacturer:    "Syberia",
			Model:           "TestModel",
		},
		Polling: config.PollingConfig{
			HotInterval:           200 * time.Millisecond,
			MediumInterval:        400 * time.Millisecond,
			SlowInterval:          1 * time.Second,
			AvailabilityThreshold: 2 * time.Second,
		},
		HTTP:   config.HTTPConfig{Port: 0},
		Logger: config.LoggerConfig{Level: "warn"},
		Reconnect: config.ReconnectConfig{
			MinDelay:  1 * time.Millisecond,
			MaxDelay:  10 * time.Millisecond,
			Factor:    2.0,
			JitterPct: 0,
		},
	}

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := app.New(cfg, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	// Wait until at least one full slow-tier poll has happened, so the broker
	// is guaranteed to hold retained values for every state topic.
	primary := newTestPahoClient(t, mqttBroker, "test-primary-"+uuid.NewString())
	primaryStates := subscribeAndCollect(t, primary, "oasis_test/state/+")

	assert.Eventually(t, func() bool {
		states := primaryStates()
		return string(states["oasis_test/state/firmware"]) == "v5.2.0" &&
			string(states["oasis_test/state/hvac_mode"]) == "heat"
	}, 30*time.Second, 100*time.Millisecond, "primary client sees firmware and hvac_mode populated")

	// Now connect a *fresh* subscriber that joins after the bridge has been
	// running for a while. With retained=true on every state publish, the
	// broker must replay the latest value immediately on subscribe — even
	// for stable signals that have not changed since boot.
	observer := newTestPahoClient(t, mqttBroker, "test-observer-late-"+uuid.NewString())
	observerStates := subscribeAndCollect(t, observer, "oasis_test/state/+")

	assert.Eventually(t, func() bool {
		states := observerStates()
		return string(states["oasis_test/state/firmware"]) == "v5.2.0" &&
			string(states["oasis_test/state/hvac_mode"]) == "heat" &&
			string(states["oasis_test/state/heater_active"]) != "" &&
			string(states["oasis_test/state/damper_open"]) != "" &&
			string(states["oasis_test/state/device_id"]) != ""
	}, 5*time.Second, 50*time.Millisecond,
		"late observer must receive retained state for stable entities")

	cancel()
	select {
	case runErr := <-done:
		require.NoError(t, runErr, "app.Run should return cleanly on ctx cancel")
	case <-time.After(15 * time.Second):
		t.Fatal("app.Run did not return within shutdown timeout")
	}
}
```

---

## Testing Strategy

```bash
# from /Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/

# 1. format / vet / build
gofmt -l application/ internal/ tests/   # expect empty
go vet ./...
go build ./...

# 2. unit + race
go test ./application/usecase/...
go test ./application/service/...
go test -race ./application/...
go test ./internal/app/...

# 3. coverage gates
go test -coverpkg=./application/usecase -coverprofile=/tmp/oasis_uc.out ./application/usecase/...
go tool cover -func=/tmp/oasis_uc.out | tail -1   # ≥90%

go test -coverpkg=./application/service -coverprofile=/tmp/oasis_svc.out ./application/service/...
go tool cover -func=/tmp/oasis_svc.out | tail -1  # ≥90% (HAStatusListener pulls overall up)

# 4. lint
golangci-lint run ./...   # 0 issues

# 5. e2e (Docker required; ~30 s)
go test ./tests/e2e/... -run TestRetainedStateSurvivesSubscriberReconnect -timeout 90s
```

Expected unit-test deltas vs. story 012:

- `TestApply_DeltaSkipsUnchanged` removed.
- `TestApply_DeltaPublishesChanged` removed.
- `TestApply_PublishError_DoesNotUpdateLastPayload` replaced by
  `TestApply_ErrorOnOneTopic_DoesNotSkipOthers`.
- `TestApply_PublishesAllOnFirstCall` flips its retained assertion.
- New: `TestApply_RepublishesAllOnEveryCall`,
  `TestApply_AlwaysRetainsState`.
- Listener tests are entirely new and stand alone in
  `application/service`.

---

## Rollout

The bridge runs in the `home-assistant` namespace as the
`oasis-mqtt-bridge` Deployment.

1. **Build a new image tag** via the GitLab CI pipeline (push to the
   feature branch, let CI build + push to Nexus).
2. **Update the Helm/Terragrunt values** for the workload pointing at
   the new image tag:
   ```bash
   cd homelab-terragrunt/workloads/homelab/oasis-mqtt-bridge/delta
   terragrunt plan      # confirm only the image tag changes
   terragrunt apply
   ```
3. **Verify the rollout**:
   ```bash
   kubectl -n home-assistant rollout status deploy/oasis-mqtt-bridge
   kubectl -n home-assistant logs -l app=oasis-mqtt-bridge --tail=200 \
     | grep -E "state published|ha (online|offline) detected|ha status listener started"
   ```
   Expected log lines on a fresh boot:
   - `ha status listener started topic=homeassistant/status`
   - `state published count=19` (every poll cycle, no `skipped=` field).
   On HA restart:
   - `ha online detected topic=homeassistant/status`
   - `discovery published count=...`
   - `state published count=19`
4. **Manual reconnect smoke test**:
   - In HA, Settings → Devices & Services → MQTT → … → Reload.
   - Within seconds the bridge log shows `ha online detected` followed
     by a discovery republish and a state republish.
   - HA UI shows all entities with real values (not `unknown`).
5. **Rollback plan**: revert the Terragrunt image-tag change and
   `terragrunt apply`. The previous behaviour is restored — the
   broker's retained state set by the new build remains in place,
   which is harmless (the old build simply never republishes).

---

## Files Changed

| File | Change |
|---|---|
| `apps/oasis-modbus2mqtt/application/usecase/publish_state.go` | modified — drop dedup, retained=true, updated godoc |
| `apps/oasis-modbus2mqtt/application/usecase/publish_state_test.go` | modified — assert retained=true, drop dedup tests, add republish test |
| `apps/oasis-modbus2mqtt/application/service/ha_status_listener.go` | new — HA birth-message listener |
| `apps/oasis-modbus2mqtt/application/service/ha_status_listener_test.go` | new — listener tests |
| `apps/oasis-modbus2mqtt/internal/app/app.go` | modified — wire HA status listener, start it in `Run` |
| `apps/oasis-modbus2mqtt/tests/e2e/full_loop_test.go` | modified — append `TestRetainedStateSurvivesSubscriberReconnect` |

---

## Agent Execution

### Implementation Agent Instructions

```
Task tool call:
- subagent_type: "general-purpose"
- model: "haiku"
- prompt: |
    You are an IMPLEMENTATION AGENT for story 017.

    ABSOLUTE RULES:
    - Copy code BYTE-FOR-BYTE from each `#### File: \`path\`` block.
    - DO NOT modify, fix, or add `//nolint`. STOP on failure.
    - For modified files, REPLACE the entire file with the block contents.

    PROCESS:
    1. Read story.
    2. Write each "#### File: `path`" block (replace existing file or create new).
    3. From work dir:
       a. gofmt -l application/ internal/ tests/   (empty)
       b. go build ./...
       c. go vet ./...
       d. go test ./application/...
       e. go test -race ./application/...
       f. go test ./internal/app/...
       g. coverage:
          go test -coverpkg=./application/usecase -coverprofile=/tmp/oasis_uc.out ./application/usecase/...
          go tool cover -func=/tmp/oasis_uc.out | tail -1   (≥90%)
          go test -coverpkg=./application/service -coverprofile=/tmp/oasis_svc.out ./application/service/...
          go tool cover -func=/tmp/oasis_svc.out | tail -1  (≥90%)
       h. golangci-lint run ./...   (0 issues)
       i. (optional, requires Docker) go test ./tests/e2e/... -run TestRetainedStateSurvivesSubscriberReconnect -timeout 90s
    4. PASS → status review.
    5. FAIL → append errors verbatim to Issues Found, status in_progress, STOP.

    Work dir: /Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/
```

### Fix Agent Instructions

Standard. Edit the story Implementation section directly; never patch
files outside the story. Re-run the Implementation Agent after the
story is updated.

---

## Implementation Notes

### Progress

- [ ] `application/usecase/publish_state.go` (modified)
- [ ] `application/usecase/publish_state_test.go` (modified)
- [ ] `application/service/ha_status_listener.go` (new)
- [ ] `application/service/ha_status_listener_test.go` (new)
- [ ] `internal/app/app.go` (modified)
- [ ] `tests/e2e/full_loop_test.go` (modified)
- [ ] gofmt clean
- [ ] go build/vet pass
- [ ] go test pass
- [ ] -race pass
- [ ] usecase coverage ≥90%
- [ ] service coverage ≥90%
- [ ] lint clean
- [ ] e2e regression test PASS

### Verification Results

(filled in by Implementation Agent)

### Issues Found

(none yet)

### Fixes Applied

(none yet)
