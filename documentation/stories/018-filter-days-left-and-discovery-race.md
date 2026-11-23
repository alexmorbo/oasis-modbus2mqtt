---
title: "Feature: Filter days-left sensors and MQTT on-connect discovery race fix"
status: shipped
commit_sha: fd052b6
release: v0.1.2
deployed: 2026-11-23 (delta, terragrunt oasis-mqtt-bridge)
priority: high
complexity: 5
created: 2026-11-23
updated: 2026-11-23
depends_on: 010-infrastructure-mqtt-client, 011-discovery-builder, 012-publish-usecases, 015-main-wiring, 017-state-retention-fix
risk_areas: [mqtt-connect-lifecycle, discovery-republish, slow-tier-poll, undocumented-registers]
---

## Context

### Part A: undocumented filter registers

Reverse-engineering the v5.2.0 controller turned up two undocumented holding
registers (0-based PDU addresses, same notation as `domain/catalog/registers.go`):

| Register | Meaning | Live value |
|---|---|---|
| h344 | configured filter-1 resource, days | 150 |
| h346 | filter-1 resource remaining, days. Counts down about 1/day; a panel filter reset sets it back to h344 | 150 |

The existing `filter_clog` sensor (i14) is **not** a pressure reading. It is
the calendar countdown `(h344 − h346) / h344`. h346 is read-only over Modbus:
FC06/FC16 writes are acknowledged and silently ignored. So there is no reset
button and no write path.

### Part B: initial discovery publish always failed

Live log on every pod start:

```
WARN "discovery publish completed with errors" count=16 errors=16 firmware=unknown
WARN "initial discovery publish failed" ... mqtt publisher not connected
```

Root cause: `infrastructure/mqtt/client.go` `Connect` returned when paho's
connect token completed, but `connectedAtomic` was only set in `onConnect`.
paho v1.5.1 calls that handler asynchronously (`go c.options.OnConnect(c)`,
`client.go:635`). `internal/app/app.go` `Run` called `pubDiscovery.Publish`
right after `Connect`, so every publish saw `connectedAtomic == false` and
returned `dto.ErrMQTTNotConnected`. Entities only survived because old
retained configs were still on the broker. A new entity would never show up
until HA restarted, and a broker restart without persistence lost discovery
until the next HA birth message.

## Solution

### Part A

- `catalog.DeviceClassDuration = "duration"`.
- Two slow-tier catalog entries (`PollGroupSlow`, 60 s):
  - h346 `Filtr1_Ostatok` → sensor `filter_days_left`, "Filter days left",
    unit `d`, device_class `duration`, state_class `measurement`,
    icon `mdi:air-filter`, default category.
  - h344 `Filtr1_Resurs` → sensor `filter_resource`, "Filter resource",
    unit `d`, device_class `duration`, category `diagnostic`, icon `mdi:air-filter`.
- `PollSlow` reads h344 and h346 as **two separate single-register reads**.
  h345 is undocumented, and a span read would couple the two. Both reads are
  **best-effort** (see "Filter reads: best-effort and gated" below).
- `Snapshot.FilterResource` (uint16), `Snapshot.FilterDaysLeft` (**int16**, so
  an overdue filter reads as negative days instead of 65535) and
  `Snapshot.FilterPolledAt` (time.Time), merged in `defaultMerger.MergeSlow`.
- `PublishState.buildMessages` emits `filter_days_left` and `filter_resource`
  as plain integers, but only once `FilterPolledAt` is set. That is 21 state
  messages with the filter pair and 19 without it.
- Discovery needed no change. `Builder.Build` iterates
  `catalog.RegistersWithEntity()` without filtering by register kind, so there
  are now 18 configs.

### Filter reads: best-effort and gated (review fixes M1 + M2)

Two problems came out of review:

- **M1, bogus 0 at startup.** The hot and medium tiers publish state before
  the slow tier has merged, so the first state publishes carried
  `filter_days_left = 0`. That writes a spurious 0 into HA long-term
  statistics and fires every "days left < N" automation on each pod restart.
- **M2, slow tier coupled to undocumented registers.** A Modbus exception on
  h344 or h346 failed the whole `PollSlow`, so firmware and device_id were
  lost too on controller variants that do not implement those registers.

The design:

- `PollSlow` keeps i0, i79 and h0 (Type_Dev) fail-hard, as before. h344 and
  h346 go through `readFilterRegister`. A read error or a wrong length logs
  one `Warn` (`poll_slow filter register read failed`, `register=h344|h346`,
  `error=...`) and PollSlow returns the snapshot **without** filter values and
  with a zero `FilterPolledAt`. If h344 fails, h346 is not read. Only when both
  reads succeed are the values set, with `FilterPolledAt = clock.Now()`.
- h346 is decoded as `int16(raw)` (same-size reinterpret, `//nolint:gosec`,
  matching the repo's existing pattern). h344 stays `uint16`.
- `MergeSlow` copies `FilterResource`, `FilterDaysLeft` and `FilterPolledAt`
  only when `partial.FilterPolledAt` is non-zero. Otherwise it keeps prev, so a
  transient filter-read failure does not wipe a known value.
- `buildMessages` omits `filter_days_left` and `filter_resource` while
  `snap.FilterPolledAt.IsZero()`. Discovery still announces both sensors, so HA
  shows them as `unknown` until the first successful read, which is correct.
- Out of scope, unchanged: firmware, device_id and room_* still publish their
  zero defaults before their tier has polled.

### Part B

- `Client.Connect` waits on a per-session `ready` channel, after the token,
  honouring ctx. `onConnect` first takes `c.mu` and compares the callback's
  paho client with the current `c.client`. If they differ (a stale callback
  from a client replaced by Connect → Disconnect → Connect), it logs at Debug
  and returns before touching `connectedAtomic`, the availability publish or
  the hooks (review fix LOW1). Otherwise, still under the lock, it stores
  `connectedAtomic = true` and then closes `ready`; a non-blocking select
  guards against a double close on reconnects. `Connect` replaces the channel
  before dialling, so Disconnect → Connect works.
- `Client.AddOnConnectHook(OnConnectHook)` keeps a hook registry. `onConnect`
  copies the hooks under the mutex, releases it, publishes availability,
  resubscribes, and then runs the hooks one after another. Each hook gets
  `context.WithTimeout(Background, 30s)`. The hooks run on paho's own
  `OnConnect` goroutine, so they never block paho's comms loop and never run
  under `c.mu`.
- `internal/app/app.go` has one shared `newRepublisher(...)` that
  - bounds its context with `republishTimeout` (10 s; renamed from
    `haOnlineRepublishTimeout`, since it now covers both triggers),
  - publishes discovery using the firmware from the current snapshot
    (`unknown` before the first poll),
  - publishes state only when `PolledAt` is non-zero.

  The HA status listener (`trigger=ha online`) and the MQTT on-connect hook
  (`trigger=mqtt connected`) both call it.
- `Run` no longer publishes discovery itself. The hook covers the initial
  connect, so nothing is published twice at startup.

## Acceptance Criteria

- [x] `homeassistant/sensor/<device_prefix>/filter_days_left/config` carries `device_class: duration`, `unit_of_measurement: d`, `state_class: measurement` and no `entity_category`.
- [x] `homeassistant/sensor/<device_prefix>/filter_resource/config` carries `device_class: duration`, `unit_of_measurement: d` and `entity_category: diagnostic`.
- [x] State topics `<device_prefix>/state/filter_days_left` and `<device_prefix>/state/filter_resource` are retained and hold plain integers.
- [x] A read error or a wrong length on h344 or h346 does **not** fail `PollSlow`: firmware, device_id and Type_Dev are still returned, `FilterPolledAt` stays zero, and one `Warn` names the register.
- [x] `filter_days_left` / `filter_resource` state is never published before the first successful filter read (19 messages until then, 21 after). The e2e test asserts no `0` ever appears on `filter_days_left`.
- [x] `MergeSlow` keeps the previous filter values when a slow poll carries no filter read.
- [x] h346 is signed: an overdue filter renders as e.g. `-3`.
- [x] A stale paho on-connect callback (from a replaced client) neither flips `Connected()` nor runs hooks.
- [x] `Connect` returns only once `Connected() == true`, so the first `Publish` right after it succeeds.
- [x] Discovery, and state once a snapshot exists, is republished on every MQTT (re)connect and on every HA birth `online`.
- [x] No discovery double publish at startup.
- [x] README entity table updated, and `filter_clog` documented as a calendar countdown.

## Verification

```bash
cd apps/oasis-modbus2mqtt
gofmt -l .                         # empty
go build ./... && go vet ./...
golangci-lint run ./...
PKGS=$(go list ./... | grep -v '/cmd/')
go test -short -race -timeout 5m -coverprofile=/tmp/cov.out $PKGS && go tool cover -func=/tmp/cov.out | tail -1
go test -timeout 10m $PKGS          # integration + e2e (Docker)
for i in 1 2 3; do go test -race -count=10 ./infrastructure/mqtt/ ./internal/app/ || break; done
go test -count=5 ./tests/e2e/
```

The regression guards are these tests:

- `TestConnect_ReturnsOnlyWhenUsable` (mqtt). It fails on the pre-fix code at iteration 2 or 3.
- `TestOnConnectHook_RunsOnConnectAndReconnect` (mqtt, session takeover).
- `TestDiscoveryRepublishedOnMQTTReconnect` (e2e). It wipes the retained config, kicks the bridge session and expects the config to come back.
- `TestOnConnect_IgnoresStaleClient` / `TestOnConnect_CurrentClientRunsHooks` (mqtt, internal unit tests driving `onConnect` directly, no broker).
- `TestPollSlow_FilterReadsBestEffort`, `TestPollSlow_FilterDaysLeftSigned` (usecase).
- `TestMergeSlow_FilterFieldsGatedOnFilterPolledAt` (service).
- `TestApply_FilterTopicsGatedOnFilterPolledAt`, `TestApply_ZeroSnapshot_PublishesDefaults` (usecase, 19 vs 21 messages, `-3`).
- `TestE2E_FullLoop` records every `filter_days_left` payload and asserts `0` never appears. Mutation check: with the `FilterPolledAt` gate in `buildMessages` forced open, this assertion fails ("must never publish a placeholder 0").

After deploy, the pod log should show `discovery published count=18` once at
startup. There should be no `discovery republish failed` warning, no
`initial discovery publish failed` and no `mqtt publisher not connected`.
In HA, `sensor.oasis_syberia_filter_days_left` should read 150 (live h346)
and `sensor.oasis_syberia_filter_resource` should read 150.

## Verification Results

Run 2026-11-23 on the uncommitted tree (base `0309e02`), local Docker for
mosquitto and mbserver. No contact with the real controller or broker.

```
$ gofmt -w . && gofmt -l .
(empty)
$ go build ./... && go vet ./...
(ok)
$ golangci-lint run ./...
0 issues.
$ go test -short -race -timeout 5m -coverprofile=/tmp/fix_cov.out $PKGS && go tool cover -func=/tmp/fix_cov.out | tail -1
ok  .../infrastructure/mqtt   8.970s  coverage: 53.5% of statements
ok  .../internal/app          4.692s  coverage: 49.5% of statements
... (all 18 packages ok)
total:                        (statements)   83.1%
$ go test -timeout 10m $PKGS
... all 18 packages ok (mqtt 2.826s, internal/app 2.433s, tests/e2e 6.696s)
$ for i in 1 2 3; do go test -race -count=10 ./infrastructure/mqtt/ ./internal/app/ || break; done
== loop 1
ok  .../infrastructure/mqtt   14.111s
ok  .../internal/app           3.983s
== loop 2
ok  .../infrastructure/mqtt   13.696s
ok  .../internal/app           3.876s
== loop 3
ok  .../infrastructure/mqtt   13.534s
ok  .../internal/app           3.840s
$ go test -count=5 ./tests/e2e/
5x PASS TestE2E_FullLoop, TestDiscoveryRepublishedOnMQTTReconnect, TestRetainedStateSurvivesSubscriberReconnect
ok  .../tests/e2e   17.902s
```

## Notes / Known limitations

- **paho QoS 1 resume race (test only).** In paho v1.5.1, `resume()`
  (client.go:1114) sets `p.Dup` on in-flight QoS 1 packets while the outgoing
  comms goroutine packs the same packet on reconnect. `-race -count=10`
  flagged it in `TestOnConnectHook_RunsOnConnectAndReconnect`. In production
  this is harmless: at worst a retained message is delivered twice. The test
  sets `cfg.QoS = 0` so nothing is in flight across the takeover, and its wait
  for `run-2` tolerates a redelivered `run-1`.
- **Overlapping republishes on rapid reconnects.** Each reconnect runs the
  hook on its own paho `OnConnect` goroutine, so two quick reconnects can run
  two republishes concurrently. Both publish the same retained discovery and
  the latest snapshot, so the outcome converges, and the next poll's state
  publish self-heals any interleaving.
- **Double republish if HA's birth message is retained.** If something
  publishes `homeassistant/status online` retained, our resubscribe on every
  reconnect delivers it again, so the HA-status listener and the on-connect
  hook both republish. Idempotent, just redundant traffic.
- **HA-status handler is synchronous on paho's router goroutine.** This is
  pre-existing. A slow republish from the HA birth path (up to
  `republishTimeout`, 10 s) blocks delivery of other inbound messages,
  commands included, for that time.
- **Availability `online` is published even when Modbus is down.** This is
  pre-existing. `onConnect` always publishes `online`. The availability
  manager only publishes on an online/offline transition, so if it is already
  in the offline state, an MQTT reconnect while Modbus is down leaves HA
  showing `online` until the next transition.
- **`sw_version` stays `unknown` until a reconnect or HA birth.**
  `PublishDiscovery.PublishIfChanged` is still not wired, so discovery
  published at startup (before the first slow poll) carries
  `sw_version: unknown`. A possible follow-up is to call `PublishIfChanged`
  after the first slow-tier merge.
- **Stale `onConnectionLost`.** LOW1 only guards `onConnect`. A late
  connection-lost callback from a replaced client could still flip
  `Connected()` to false; paho auto-reconnect of the current client restores
  it. Not seen in practice.
- Two pre-existing `noctx` lint findings in `interface/http/handler/*_test.go`
  are fixed (`httptest.NewRequestWithContext`). Local golangci-lint v2.12
  reports them; the CI pin v2.5 does not.

## Live verification (2026-11-23, delta, v0.1.2)

- Pod start log: one `discovery published count=18`, no `mqtt publisher not connected` warnings. `state published count=21`.
- HA: `sensor.oasis_syberia_filter_days_left` = 150 d (duration, measurement) and `sensor.oasis_syberia_filter_resource` = 150 d (diagnostic) appeared without restarting HA.
- HA created them with an area prefix (`sensor.koridor_oasis_syberia_*`, because the device is in the "Коридор" area). They were renamed in the registry to the `sensor.oasis_syberia_*` form. Follow-up: send `default_entity_id` in discovery payloads so new entities get stable ids.
- Startup log still reports `version: 0.1.0` (pre-existing: version is not injected at build time).

