# oasis-modbus2mqtt

[![CI](https://github.com/alexmorbo/oasis-modbus2mqtt/actions/workflows/ci.yml/badge.svg)](https://github.com/alexmorbo/oasis-modbus2mqtt/actions/workflows/ci.yml)
[![Go Reference](https://pkg.go.dev/badge/github.com/alexmorbo/oasis-modbus2mqtt.svg)](https://pkg.go.dev/github.com/alexmorbo/oasis-modbus2mqtt)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](#license)

A Go service that bridges a **GTC Oasis Syberia v5** HVAC controller (Modbus TCP) into **Home Assistant** via MQTT discovery — one HA device with climate, switch, sensors, and binary sensors automatically registered.

Replaces Home Assistant's native `modbus:` YAML integration with a self-healing service that handles the controller's quirks (single-TCP-connection, edge-triggered Power register, RMW for mode bits, transition states) so the rest of HA doesn't have to.

## Screenshots

| Home Assistant overview | Climate control | Diagnostics |
|---|---|---|
| ![HA overview](static/ha-overview.png) | ![HA climate](static/ha-climate.png) | ![HA diagnostic](static/ha-diagnostic.png) |

Grafana dashboard (one device → all bridge metrics on one screen):

![Grafana dashboard](static/grafana.png)

The Grafana dashboard JSON ships in [`grafana/oasis.json`](grafana/oasis.json) — drop it into Grafana via *Dashboards → New → Import*, or apply it via grafana-operator / Terraform `grafana_dashboard` resource. It assumes a Prometheus-compatible datasource named `vm` and the bridge metrics on `oasis_*` (the default).

## Status

Running in production on the author's homelab against a real GTC Oasis Syberia 5 (firmware v5.2.0). Treat as **0.x — API may evolve**. Hardware-specific decoding is reverse-engineered; PRs welcome for other Oasis variants.

## What it does

- Maintains exactly **one** Modbus TCP connection to the controller (the device refuses concurrent clients).
- Polls registers on a tiered schedule (5 s / 15 s / 60 s) and decodes them into a typed `Snapshot`.
- Publishes `homeassistant/.../config` MQTT discovery for 32 entities — climate, power switch, supply/room temperature, room humidity, supply/exhaust fan speed, filter clog %, filter days left / filter resource, heat demand, current operation, firmware, device id, heater-active / damper-open / problem binary sensors, plus 14 panel-menu settings as HA **config** entities (10 switches, 1 select, 3 numbers).
- Every discovery payload carries `default_entity_id` (`<domain>.<device_prefix>_<object_id>`; the climate is `climate.<device_prefix>`), so HA does not prefix new entity ids with the device's area. HA honours it only when it first creates an entity; existing registry ids are kept.
- Subscribes to HA command topics (`cmd/power`, `cmd/hvac_mode`, `cmd/target_temperature`, `cmd/fan_target`, and one `cmd/<object_id>` per panel setting) and translates them to Modbus writes — including the **edge sequence** required by `Power_Dev` and the **read-modify-write** required by `Dev_Keys_2`, `User_Keys_0` and `User_Dev_0`.
- Reads every panel-setting write back and publishes the **actual** register value. The controller can acknowledge a write and silently ignore it; that case logs a warning, increments `oasis_modbus_write_not_applied_total{entity}` and HA keeps showing the real value.
- Republishes discovery (and the current state, once polled) on every MQTT (re)connect and on every HA birth message, so entities come back after a broker restart without persistence.
- Holds an automatic reconnect supervisor with exponential backoff + jitter for both Modbus and MQTT, surfaces `availability=online/offline` (LWT-backed) so HA correctly greys-out entities when the bridge is down.
- Refuses writes while the controller is in a transition phase (`close_damper`, `electric_calorifier_purge`, …) and exposes the current operation as a sensor so users can see *why*.
- Ships `/health/live`, `/health/ready`, and `/metrics` (VictoriaMetrics-compatible) for k8s probes and Prometheus scraping.

## Architecture (one screen)

```
                +---------------------+
                | Home Assistant      |
                | (MQTT discovery)    |
                +----------+----------+
                           | MQTT
                           v
                +---------------------+
                | MQTT broker         |
                | (e.g. Mosquitto)    |
                +----------+----------+
                           ^
                  publish  | subscribe
                  state +  | cmd/*
                  discovery|
                           |
+---------+   poll/write   v       Modbus TCP    +-------------------+
| Poller  +---->  Bridge  +---------------------->| GTC Oasis Syberia |
+---------+                                       | v5 controller     |
   3 tiers      |   ConnectionSupervisor          +-------------------+
                |   reconnect + backoff
                v
            CommandDispatcher
            (failure threshold → trigger supervisor)
```

Code organisation follows Clean Architecture (`domain` → `application` → `infrastructure` → `interface`). `cmd/server/main.go` is a thin wiring entry point.

## Quick start

### Docker

```bash
docker run --rm \
  -p 8080:8080 \
  -e MODBUS_HOST=<controller-ip> \
  -e MQTT_BROKER=<broker-host:1883> \
  -e MQTT_CLIENT_ID=oasis-modbus2mqtt \
  -e HA_DEVICE_PREFIX=oasis_syberia \
  ghcr.io/alexmorbo/oasis-modbus2mqtt:latest
```

Multi-arch images (`linux/amd64`, `linux/arm64`) are published to `ghcr.io/alexmorbo/oasis-modbus2mqtt`. Tags include `latest` (default branch), `<sha-short>` (every push), and `vX.Y.Z` / `X.Y.Z` / `X.Y` for git tags matching `v*`.

### Build from source

```bash
make build           # ./bin/oasis-modbus2mqtt (version from git describe)
make docker-build    # oasis-modbus2mqtt:latest
make test            # unit + race
make test-coverage   # coverage report
make lint            # golangci-lint v2
make tidy            # go mod tidy
```

Requires Go 1.25+. The startup log line reports the build version: the tag on tag images, `sha-<7>` on `main` images, `dev` for a plain `go build`. Pass `--build-arg VERSION=...` to `docker build` to set it.

## Configuration

All configuration is via environment variables. Defaults in parens.

| Var | Default | Description |
|---|---|---|
| `MODBUS_HOST` | _required_ | Controller IP / hostname |
| `MODBUS_PORT` | `502` | Modbus TCP port |
| `MODBUS_SLAVE_ID` | `1` | Slave address |
| `MODBUS_CONNECT_TIMEOUT` | `5s` | TCP connect timeout |
| `MODBUS_READ_TIMEOUT` | `2s` | Per-read timeout |
| `MODBUS_WRITE_TIMEOUT` | `2s` | Per-write timeout |
| `MODBUS_GUARD_INTERVAL` | `100ms` | Min interval between Modbus ops (vendor recommendation) |
| `MQTT_BROKER` | _required_ | `host:port` |
| `MQTT_CLIENT_ID` | `oasis-modbus2mqtt` | Must be unique per broker |
| `MQTT_USERNAME` | `` | Empty → anonymous |
| `MQTT_PASSWORD` | `` | Empty → anonymous |
| `MQTT_KEEPALIVE` | `30s` | Paho keepalive |
| `MQTT_QOS` | `1` | QoS for state and command topics |
| `HA_DISCOVERY_PREFIX` | `homeassistant` | HA MQTT discovery prefix |
| `HA_DEVICE_PREFIX` | `oasis_syberia` | namespace for unique ids, topics and `default_entity_id` |
| `HA_DEVICE_NAME` | `Oasis Syberia` | Display name |
| `HA_MANUFACTURER` | `GTC` | |
| `HA_MODEL` | `Syberia 5` | |
| `POLL_HOT_INTERVAL` | `5s` | Tier 1: state + filter + fans + target |
| `POLL_MEDIUM_INTERVAL` | `15s` | Tier 2: room temp/hum + extra errors + mode |
| `POLL_SLOW_INTERVAL` | `60s` | Tier 3: firmware + device id + Type_Dev + filter resource / days left (h344/h346, best-effort) |
| `AVAILABILITY_THRESHOLD` | `30s` | Stale snapshot → publish `offline` |
| `HTTP_PORT` | `8080` | `/health/*` + `/metrics` |
| `LOG_LEVEL` | `info` | `debug` / `info` / `warn` / `error` |
| `RECONNECT_MIN_DELAY` | `1s` | Modbus reconnect backoff start |
| `RECONNECT_MAX_DELAY` | `60s` | Backoff cap |
| `RECONNECT_FACTOR` | `2.0` | Backoff multiplier |
| `RECONNECT_JITTER_PCT` | `0.2` | ±jitter ratio |

## Migrating from HA's native `modbus:` integration

The Oasis Syberia controller permits **only one** TCP client — you cannot run HA's `modbus:` block and this bridge at the same time. Sequence:

1. **Back up** `configuration.yaml`, `scripts.yaml`, and any related `automations.yaml` entries.
2. Comment out the `modbus:` top-level block, the `template:` ventilation entries, and any `script:`s that called `modbus.write_register`.
3. Disable any automation that polls / restarts the modbus integration (if you have one — set `initial_state: false`).
4. Restart Home Assistant — the controller's TCP socket is now free.
5. Deploy this bridge (Docker, k8s, systemd — pointed at the same controller).
6. Within ~60 s the bridge connects, publishes discovery, and a single device "Oasis Syberia" appears in HA with its entities.
7. Verify the climate entity (mode change, target temperature, fan speed) and dashboard sensors before removing the old YAML permanently.

## HA entities published

| Entity | Type | Notes |
|---|---|---|
| `climate.oasis_syberia` | climate | modes `off`/`heat`/`fan_only`, fan modes `1..10`, target 5–30 °C |
| `switch.oasis_syberia_power` | switch | `Power_Dev` master toggle |
| `sensor.oasis_syberia_supply_temperature` | sensor (°C) | corrected supply duct T1 |
| `sensor.oasis_syberia_room_temperature` | sensor (°C) | from controller's wall remote |
| `sensor.oasis_syberia_room_humidity` | sensor (%) | from wall remote |
| `sensor.oasis_syberia_supply_fan_speed` | sensor | actual speed reported by controller |
| `sensor.oasis_syberia_exhaust_fan_speed` | sensor | actual exhaust speed |
| `sensor.oasis_syberia_filter_clog` | sensor (%) | calendar filter wear, not a pressure sensor: `(h344 − h346) / h344` |
| `sensor.oasis_syberia_filter_days_left` | sensor (d) | days of filter resource remaining (h346, signed: negative when overdue); resets to the configured resource on a panel filter reset. Read-only over Modbus. `unknown` until the first successful slow-tier poll |
| `sensor.oasis_syberia_filter_resource` | sensor (d, diagnostic) | configured filter resource (h344). `unknown` until the first successful slow-tier poll |
| `sensor.oasis_syberia_heat_demand` | sensor (%) | PID demand for heating |
| `sensor.oasis_syberia_operation_time_left` | sensor (s) | seconds until current transition completes |
| `sensor.oasis_syberia_current_operation` | sensor (text) | `idle` / `close_damper` / `preheat_calorifier` / … |
| `sensor.oasis_syberia_firmware` | sensor (diagnostic) | e.g. `v5.2.0` |
| `sensor.oasis_syberia_device_id` | sensor (diagnostic) | hex device id |
| `binary_sensor.oasis_syberia_heater_active` | binary_sensor (heat) | PID heat demand > 0 while powered (`Reg` i18). Stable while heating: it no longer follows the ~1 Hz heater PWM output. `hvac_action=heating` uses the same signal |
| `binary_sensor.oasis_syberia_damper_open` | binary_sensor (opening) | inlet damper |
| `binary_sensor.oasis_syberia_problem` | binary_sensor (problem, diag.) | any `Error_Code_*` bit set |

Entity ids above are what `default_entity_id` asks for on a fresh install. An HA that already registered an entity keeps its id (for example a climate created before `default_entity_id` existed may be `climate.oasis_syberia_mqtt_hvac`).

### Panel settings (config entities)

The panel's settings menu, read every 60 s (slow tier) as two spans, h22..h25 and h35..h36, and shown under the device's **Configuration** section. Bitfield writes are read-modify-write on a fresh controller read, so bits the bridge does not expose are preserved. Every write is read back; HA shows the actual value. Settings stay `unknown` until the first successful read, and writes are refused while the controller is in a transition phase.

| Entity | Type | Register / bits | Notes |
|---|---|---|---|
| `switch.oasis_syberia_nord_start` | switch | h22 bit 1 | North start (cold-climate start-up) |
| `switch.oasis_syberia_touch_click` | switch | h22 bit 5 | Touch click sound |
| `switch.oasis_syberia_alarm_beep` | switch | h22 bit 6 | Alarm beep |
| `switch.oasis_syberia_stop_on_filter_full` | switch | h22 bit 7 | Stop when the filter reaches 100 % |
| `switch.oasis_syberia_auto_restart` | switch | h22 bit 8 | Auto restart after power loss |
| `switch.oasis_syberia_performance_control` | switch | h22 bit 9 | Auto fan-speed reduction when the setpoint is not reached |
| `switch.oasis_syberia_dark_screen` | switch | h22 bit 10 | Dark screen |
| `switch.oasis_syberia_humidifier` | switch | h22 bit 12 | Humidifier |
| `switch.oasis_syberia_stop_on_conditioner_alarm` | switch | h22 bit 15 | Stop on conditioner alarm |
| `select.oasis_syberia_control_priority` | select | h22 bits 13..14 | `temperature` / `humidity` / `co2` (vendor PDF; the panel calls the third option "pressure", unverified). Raw value 3 shows `unknown` and is never written |
| `switch.oasis_syberia_active_filter` | switch | h25 bit 2 | Active filter |
| `number.oasis_syberia_dark_brightness` | number (%, slider) | h23 | 20..100, step 1. Bridge assumption: the register map gives no range; 20 % is the value observed on the live panel |
| `number.oasis_syberia_humidity_target` | number (%, humidity, slider) | h35 | 20..95, step 1 |
| `number.oasis_syberia_co2_target` | number (ppm, carbon_dioxide, box) | h36 | 500..2000 (vendor PDF), step 10. The step is a bridge assumption; the register map gives only the range |

h22 bit 2 (remote server access) and bits 0, 3, 4 and 11 (reserved or demo) are deliberately not exposed. h24 (`IR_Remoute_Time`) is read only to keep the h22..h25 span contiguous. Invalid commands (out of range, off the step grid, non-integral, unknown option) are rejected before any Modbus write and logged as `invalid command payload`. A command that asks for the value the register already holds skips the Modbus write (these registers are EEPROM-backed) but is still read back and published.

Commands are applied by one worker in arrival order, off the MQTT client's network goroutine. Up to 32 commands may wait; a burst beyond that is dropped and logged as `command queue full, command dropped`.

## Observability

- **Logs** — structured JSON via `log/slog` to stdout. Levels: `INFO` for lifecycle (connect, discovery, online/offline, command applied), `WARN` for transient errors and command rejections, `ERROR` for fatal.
- **Metrics** — Prometheus scrape on `:8080/metrics`. Notable series:
  - `oasis_modbus_ops_total{op,status}` (`status=skipped` on `op=modify_holding_write` counts no-op writes not sent), `oasis_modbus_op_duration_seconds{op}`, `oasis_modbus_errors_total{type}`
  - `oasis_modbus_reconnects_total`, `oasis_modbus_connected` (gauge 0/1)
  - `oasis_mqtt_publishes_total`, `oasis_mqtt_reconnects_total`, `oasis_mqtt_connected`
  - `oasis_poll_duration_seconds{tier}`, `oasis_last_successful_poll_unix_nanos`
  - `oasis_modbus_write_not_applied_total{entity}` — settings writes the controller acknowledged but did not apply (read-back mismatch); should stay absent or 0
- **Health probes** — `/health/live` always 200; `/health/ready` 503 when MQTT is disconnected or the latest snapshot is older than 120 s.

## Hardware compatibility

Tested against firmware `v5.2.0` (`0x5200`) of the GTC Oasis Syberia 5. The bit layouts and register addresses are encoded in `domain/catalog/registers.go` and the bit decoders in `domain/entity/`.

The controller has several quirks the bridge handles for you:

- **Single TCP client only** — concurrent connections fail with `Connection refused`.
- **Power_Dev (h2) is edge-triggered** — turning OFF requires `write 1 → wait ≥100 ms → write 0`; turning ON is a single `write 1`. The register always reads back as 0.
- **Dev_Keys_2 (h86) needs RMW** — bits 0..1 are mode; bits 2..15 carry timer/humidifier/CO₂ settings that must be preserved.
- **TCP idle timeout ~5–10 s** — the bridge's hot-tier 5 s poll doubles as a keepalive.
- **Filter counter is read-only over Modbus** — h346 (days left) acknowledges FC06/FC16 writes and ignores them, so the filter reset is only possible from the panel; the bridge exposes no reset button.
- **Transition states are not writeable** — during `close_damper` / `electric_calorifier_purge` / `preheat_calorifier` the controller acknowledges writes but ignores them. The bridge rejects commands with a clear error during these phases; the `current_operation` sensor surfaces the reason in HA.

If your Oasis variant has different register addresses, edit `domain/catalog/registers.go` and the bit catalog in `domain/entity/error_code.go`.

## Project layout

```
cmd/server/         — main entrypoint
internal/app/       — DI wiring + lifecycle
domain/             — value objects, entities, register catalog (no I/O)
application/
  port/             — interfaces (ModbusClient, MQTTPublisher, Clock, …)
  dto/              — Command DTOs
  usecase/          — PollController, ApplyCommand, PublishState, PublishDiscovery
  service/          — CommandDispatcher, ConnectionSupervisor, Poller, AvailabilityManager
infrastructure/
  config/           — env-var loader
  logger/           — slog setup
  metrics/          — VictoriaMetrics counters/histograms/gauges
  modbus/           — grid-x/modbus client wrapper (mutex, guard interval, RMW)
  mqtt/             — paho client wrapper (LWT, auto-reconnect, sub registry)
  discovery/        — HA discovery payload builder
interface/
  http/             — Gin router, /health, /metrics
  mqtt/             — command subscriber → Apply
tests/e2e/          — full-loop test (mbserver + mosquitto via testcontainers)
documentation/      — design notes, per-feature stories
```

## Contributing

Issues and PRs welcome. Coverage is currently above 80 % overall, with most application/usecase and domain packages at 95 %+. Please:

- Run `make lint test` before pushing.
- Add or update unit tests for any behavioural change.
- Tag releases with `vX.Y.Z` — CI builds and pushes the corresponding image automatically.

## License

MIT — see [LICENSE](LICENSE).
