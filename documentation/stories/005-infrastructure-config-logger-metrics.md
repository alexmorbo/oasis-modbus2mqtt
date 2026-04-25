---
title: "Feature: Infrastructure config, logger, metrics"
status: ready
priority: high
complexity: 4
planning_model: opus-4-5
implementation_model: haiku-4
created: 2026-04-25
updated: 2026-04-25
depends_on: 004-application-ports-and-dtos
risk_areas: []
---

## Context

Story 005 — три инфраструктурных утилитарных пакета:

- **config** — env-based загрузка конфигурации (Modbus host/port, MQTT broker, polling intervals, log level и т.д.). Мирная единая точка для всех настроек.
- **logger** — обёртка над `log/slog` с JSON handler, инициализация по log level из config.
- **metrics** — VictoriaMetrics counters/histograms/gauges, готовые к использованию в infrastructure (modbus client, mqtt client) и application services (poller, dispatcher).

**Эталон для стиля** — `/Users/alexmorbo/Home/homelab/apps/summarizer/infrastructure/{config,logger,metrics}/`. Берём ту же структуру (Config struct + Load() + env helpers, slog JSON wrapper, VictoriaMetrics через `github.com/VictoriaMetrics/metrics`).

**Что НЕ делаем в этой story:**
- НЕ запускаем metrics HTTP endpoint — это story 014 (HTTP handlers).
- НЕ читаем secrets из Vault/файлов — только env vars (Kubernetes secret → env через `valueFrom`).
- НЕ пишем graceful shutdown логики — story 015 (main wiring).

**Найденные внешние факты для дефолтов:**
- Modbus: `10.90.19.7:502`, slave_id=1
- MQTT: `10.90.19.10:1883`, anonymous (no auth)
- HA discovery prefix: `homeassistant`
- HA device prefix (object_id base): `oasis_syberia`
- Polling tiers: 5s / 15s / 60s (плановые)
- Modbus guard interval: 100ms
- Availability threshold: 30s (после = публикуем "offline")
- Reconnect backoff: 1s..60s, factor=2, jitter=20%

## User Story

**As a** оператор oasis-modbus2mqtt,
**I want to** конфигурировать сервис исключительно через env vars (Kubernetes-friendly),
**So that** я мог менять Modbus host или MQTT broker без пересборки контейнера, и логи всегда были structured JSON для VictoriaLogs.

## Acceptance Criteria

### Config

- [ ] `infrastructure/config/config.go`:
  - `type Config struct { Modbus ModbusConfig; MQTT MQTTConfig; HomeAssistant HAConfig; Polling PollingConfig; HTTP HTTPConfig; Logger LoggerConfig; Reconnect ReconnectConfig }`.
  - `type ModbusConfig struct { Host string; Port int; SlaveID byte; ConnectTimeout, ReadTimeout, WriteTimeout, GuardInterval time.Duration }`.
    - Helper `(m ModbusConfig) Addr() string` → `"host:port"`.
  - `type MQTTConfig struct { Broker string; ClientID string; Username, Password string; Keepalive time.Duration; QoS byte }`. Broker формат — `host:port` (без `tcp://` префикса; адаптер добавляет). Username/Password — пустые = anonymous.
  - `type HAConfig struct { DiscoveryPrefix, DevicePrefix, DeviceName, Manufacturer, Model string }`.
  - `type PollingConfig struct { HotInterval, MediumInterval, SlowInterval time.Duration; AvailabilityThreshold time.Duration }`.
  - `type HTTPConfig struct { Port int }`. `Addr()` → `"0.0.0.0:port"`.
  - `type LoggerConfig struct { Level string }`. (Format всегда JSON, не настраиваем.)
  - `type ReconnectConfig struct { MinDelay, MaxDelay time.Duration; Factor float64; JitterPct float64 }`.
  - `func Load() (*Config, error)` — читает env, возвращает заполненный Config. Ошибка если invalid (например, `MODBUS_PORT=not_a_number`). Зависимости должны успешно проверяться: непустой `MODBUS_HOST`, `MQTT_BROKER`, `MQTT_CLIENT_ID`, `HA_DEVICE_PREFIX`. Невалидные значения → ошибка с понятным сообщением (`"invalid MODBUS_PORT: ..."`).
  - **Defaults** — все из секции "Найденные внешние факты" в Context. Все продакшн-значения работают из коробки в delta env.
  - `func (c *Config) Validate() error` — проверка инвариантов (позже зови из Load): порты в [1, 65535], slave_id в [1, 247], Polling intervals > 0, HotInterval ≤ MediumInterval ≤ SlowInterval (логически).

- [ ] `infrastructure/config/helpers.go`:
  - `getEnvOrDefault(key, def string) string`.
  - `getEnvOrDefaultInt(key string, def int) (int, error)` — возвращает error если установлен но не парсится. (Это иначе чем у summarizer — там silently fallback to default; нам строже: invalid env = fail loudly.)
  - `getEnvOrDefaultBool(key string, def bool) (bool, error)`.
  - `getEnvOrDefaultDuration(key string, def time.Duration) (time.Duration, error)`.
  - `getEnvOrDefaultByte(key string, def byte) (byte, error)`.
  - `getEnvOrDefaultFloat(key string, def float64) (float64, error)`.

### ENV vars (полный список)

Дефолты в скобках. Все опциональные — если не задан, берётся default.

| Var | Default | Type | Description |
|---|---|---|---|
| `MODBUS_HOST` | `10.90.19.7` | string | Controller IP |
| `MODBUS_PORT` | `502` | int | Modbus TCP port |
| `MODBUS_SLAVE_ID` | `1` | byte | Slave address |
| `MODBUS_CONNECT_TIMEOUT` | `5s` | duration | TCP connect timeout |
| `MODBUS_READ_TIMEOUT` | `2s` | duration | Per-read timeout |
| `MODBUS_WRITE_TIMEOUT` | `2s` | duration | Per-write timeout |
| `MODBUS_GUARD_INTERVAL` | `100ms` | duration | Min interval between ops |
| `MQTT_BROKER` | `10.90.19.10:1883` | string | host:port |
| `MQTT_CLIENT_ID` | `oasis-modbus2mqtt` | string | MQTT client ID (must be unique per broker) |
| `MQTT_USERNAME` | `` | string | empty = anonymous |
| `MQTT_PASSWORD` | `` | string | empty = anonymous |
| `MQTT_KEEPALIVE` | `30s` | duration | Paho keepalive |
| `MQTT_QOS` | `1` | byte | QoS for state and command topics |
| `HA_DISCOVERY_PREFIX` | `homeassistant` | string | HA MQTT discovery prefix |
| `HA_DEVICE_PREFIX` | `oasis_syberia` | string | object_id namespace |
| `HA_DEVICE_NAME` | `Oasis Syberia` | string | Display name in HA |
| `HA_MANUFACTURER` | `GTC` | string | |
| `HA_MODEL` | `Syberia 5` | string | |
| `POLL_HOT_INTERVAL` | `5s` | duration | Tier 1 |
| `POLL_MEDIUM_INTERVAL` | `15s` | duration | Tier 2 |
| `POLL_SLOW_INTERVAL` | `60s` | duration | Tier 3 |
| `AVAILABILITY_THRESHOLD` | `30s` | duration | Stale → publish "offline" |
| `HTTP_PORT` | `8080` | int | for /health and /metrics |
| `LOG_LEVEL` | `info` | string | debug/info/warn/error |
| `RECONNECT_MIN_DELAY` | `1s` | duration | Backoff start |
| `RECONNECT_MAX_DELAY` | `60s` | duration | Backoff cap |
| `RECONNECT_FACTOR` | `2.0` | float | Backoff multiplier |
| `RECONNECT_JITTER_PCT` | `0.2` | float | ±jitter ratio |

### Logger

- [ ] `infrastructure/logger/logger.go`:
  - `func New(cfg config.LoggerConfig) *slog.Logger` — slog JSON handler в `os.Stdout`.
  - Поддерживает level `debug`/`info`/`warn`/`error`/`warning` (warning = warn alias). Default `info`.
  - Source location НЕ добавляем в default — slow и не нужен. Можно добавить через env позже.
  - Возвращает уже настроенный `*slog.Logger`. **НЕ** делаем `slog.SetDefault(...)` внутри — это решение caller (story 015 main).
  - **Возможный вариант** добавить `func ParseLevel(s string) slog.Level` отдельной публичной функцией — для тестов и переиспользования.

### Metrics

- [ ] `infrastructure/metrics/metrics.go`:
  - Используем `github.com/VictoriaMetrics/metrics` (стандарт по `documentation/golang/observability.md` и summarizer).
  - Всё в default registry (`metrics.WritePrometheus`).
  - **Static metrics** (declared as package vars):
    - `ModbusReconnectsTotal` counter
    - `MQTTReconnectsTotal` counter
    - `JobsDroppedTotal` counter (general; per-tier через helper)
    - `MQTTPublishesTotal` counter
    - `DiscoveryRepublishesTotal` counter
  - **Dynamic-labeled** (через helper functions с `GetOrCreateCounter`/`Histogram`/`Gauge`):
    - `ModbusOpsTotal(op string, status string) *metrics.Counter` — где op = `"read_input"`/`"read_holding"`/`"write_holding"`/`"modify_holding"`, status = `"ok"`/`"error"`.
    - `ModbusOpDurationSeconds(op string) *metrics.Histogram`.
    - `ModbusErrorsTotal(errorType string) *metrics.Counter` — тип ошибки (`"timeout"`/`"connection_refused"`/`"illegal_address"`/`"unknown"`).
    - `JobsByTierDroppedTotal(tier string) *metrics.Counter` — tier = `"hot"`/`"medium"`/`"slow"`.
    - `PollDurationSeconds(tier string) *metrics.Histogram`.
    - `MQTTPublishDurationSeconds(topic string) *metrics.Histogram` — НЕТ! topics с retain/non-retain плодят cardinality. Используем agg label: `entity` (по object_id).
    - `MQTTPublishesByEntityTotal(entity string) *metrics.Counter`.
  - **Gauges** (через `metrics.GetOrCreateGauge` с closure-callback):
    - `ModbusConnectedGauge(check func() float64)` — НЕ нужно, лучше использовать `metrics.NewGauge`. Установка: `metrics.GetOrCreateGauge("oasis_modbus_connected", func() float64 { ... })` с callback-style. Это идиоматично VM/metrics — ставим callback при инициализации.
    - **Альтернативно** — простой `*metrics.Gauge` с прямым значением. Для simplicity: callback-style при init на основе ModbusClient.Connected() — НО ModbusClient ещё не создан в момент init metrics! Поэтому — **explicit setter functions**: `SetModbusConnected(connected bool)` который хранит значение в package-level `atomic.Bool` и Gauge callback читает оттуда. Это решает chicken-and-egg.
    - Делаем `SetModbusConnected(bool)` и `SetMQTTConnected(bool)` в этом файле + Gauge'ы с callbacks.
    - `LastSuccessfulPoll` gauge — unix timestamp, set by AvailabilityManager.
  - **Naming convention**: `oasis_<area>_<measure>_<unit>` (Prometheus best practice). Все имена с префиксом `oasis_`.

### Tests

- [ ] `infrastructure/config/config_test.go`:
  - `TestLoad_Defaults` — без env, все поля == дефолты. Используется `t.Setenv` чтобы очистить (но `t.Setenv` устанавливает значение, а не удаляет; используем `os.Unsetenv` через t.Setenv с пустой строкой — нет, t.Setenv устанавливает в "". Для этого теста: используем `t.Run` с очисткой через unset; OR полагаемся на то что env не установлен в CI).
    - Проще: используем helper `clearEnv(t)` который unset всех known env vars, или используем подход с overrides через taibler-driven helpers.
    - Hmm, на самом деле в Go test предсказуемее использовать `t.Setenv("VAR", "value")` для override + проверять что неустановленные = defaults. Просто пишем тест который не trust к глобальной env.
  - `TestLoad_AllFromEnv` — задаём ВСЕ env переменные через `t.Setenv`, проверяем что все поля зачитались.
  - `TestLoad_InvalidPort` — `MODBUS_PORT=abc` → error.
  - `TestLoad_InvalidDuration` — `MQTT_KEEPALIVE=abc` → error.
  - `TestValidate_PortOutOfRange` — Port=0 → error.
  - `TestValidate_PollOrdering` — Hot > Medium → error.
  - `TestModbusConfig_Addr`, `TestHTTPConfig_Addr`.
- [ ] `infrastructure/config/helpers_test.go`:
  - `TestGetEnvOrDefaultInt` — set/unset/invalid.
  - `TestGetEnvOrDefaultDuration` — set/unset/invalid.
  - `TestGetEnvOrDefaultByte`, etc — для каждого helper по 3 cases.
- [ ] `infrastructure/logger/logger_test.go`:
  - `TestParseLevel` — `"debug"`/`"info"`/`"warn"`/`"warning"`/`"error"`/`"INFO"`/`""`/`"unknown"` → expected.
  - `TestNew_DefaultLevel` — log Info → captured output contains the message; log Debug → not captured.
  - `TestNew_DebugLevel` — log Debug → captured.
  - Для capture используем `slog.NewJSONHandler(buf, &Opts{...})` — но мы не хотим переписывать `New`. Вместо этого:
    - `logger.New` принимает `cfg` — добавим вариант с `io.Writer` параметром? Хм, complicates API.
    - Альтернативно: `slog.SetDefault(logger.New(cfg))` НЕ делаем, а в тесте создаём свой logger с known writer.
    - **Решение**: добавить `func NewWithWriter(cfg config.LoggerConfig, w io.Writer) *slog.Logger` для тестов, а `New(cfg)` = `NewWithWriter(cfg, os.Stdout)`.
- [ ] `infrastructure/metrics/metrics_test.go`:
  - `TestModbusOpsTotal` — call helper, then call `metrics.WritePrometheus(buf)` and assert строка `oasis_modbus_ops_total{op="read_input",status="ok"}` присутствует.
  - `TestSetModbusConnected_Gauge` — `SetModbusConnected(true)` → gauge value 1; `SetModbusConnected(false)` → 0.
  - `TestStaticCounters_Increment` — Inc(), then assert WritePrometheus output.

### Quality

- [ ] Coverage:
  - `infrastructure/config` ≥ 80%
  - `infrastructure/logger` ≥ 80%
  - `infrastructure/metrics` ≥ 70% (часть кода — package var declarations, не покрывается)
- [ ] `golangci-lint run ./...` PASS.
- [ ] `gofmt -l infrastructure/` пусто.
- [ ] Все exported идентификаторы — godoc one-liner.

## Constraints

- **Зависимости**: stdlib + `github.com/VictoriaMetrics/metrics` + testify (уже в go.mod).
- `infrastructure/config` импортирует только stdlib. **НЕ** импортирует `domain/*` или `application/*`.
- `infrastructure/logger` импортирует `infrastructure/config` (для типа `LoggerConfig`).
- `infrastructure/metrics` импортирует только VM/metrics + stdlib.
- НЕТ глобальных `init()` функций в этих пакетах. `Load()` — explicit call.
- `Config.Load()` — fail-fast, любая невалидная env возвращает error с **подробным сообщением** (имя var + value + reason).
- Все Duration env vars парсятся через `time.ParseDuration` — это даёт пользователю гибкость (`"5s"`, `"500ms"`, `"1h30m"`).
- Для `byte`-типов (slave_id, qos) используем `uint8` под капотом, парсим как int с проверкой диапазона.
- Static counters в metrics инициализируются на package init (через `var X = metrics.NewCounter(...)`). Это OK: VM/metrics регистрирует в default registry синхронно, без I/O.
- Atomic для gauge state — `sync/atomic.Bool` (Go 1.19+).
- НЕ использовать `os.Getenv` напрямую в `Load()` — только через helpers.

---

## Technical Specification

### Analysis

- **Strict-fail env helpers**: unlike summarizer's silent fallback to default on parse error, our `getEnvOrDefault*` returns `(T, error)` — invalid value set in env produces explicit error wrapped at the call site (`"loading MODBUS_PORT: %w"`). This matches AC "fail-fast, любая невалидная env возвращает error".
- **Byte conversion (gosec G115)**: `getEnvOrDefaultByte` parses via `strconv.Atoi` (returns `int`), bounds-checks `[0, 255]`, then narrows to `byte`. The narrowing line carries `//nolint:gosec // bounded by check above`. Same applies to slave_id `[1, 247]` validated in `Validate()` and QoS `[0, 2]` validated separately by caller via Validate.
- **Gauge atomic state**: `sync/atomic.Bool` for connected flags + `sync/atomic.Int64` for `lastSuccessfulPoll` unix-nanos. Setters mutate atomics; gauge callbacks (registered via `vm.GetOrCreateGauge`) read atomics. Solves the chicken-and-egg: metrics package has no dependency on Modbus/MQTT clients.
- **`Init()` not `init()`**: AC says "НЕТ глобальных `init()` функций". Package-level `var X = vm.NewCounter(...)` is allowed (variable initialization, not `init()` function). Gauge registration that needs side-effects happens in an exported `Init()` function called explicitly by main wiring (story 015) — explicit, testable, idempotent (vm.GetOrCreateGauge is safe to call repeatedly with same name).
- **`NewWithWriter` for testability**: `logger.New(cfg)` is the production entry; tests use `NewWithWriter(cfg, &buf)` to capture JSON output. `New` simply delegates to `NewWithWriter` with `os.Stdout`.
- **Helpers tests in `package config`** (white-box): unexported helpers cannot be tested from `config_test`. We use white-box internal tests for `helpers_test.go` (`package config`), and black-box tests for the public API in `config_test.go` (`package config_test`). This is idiomatic Go for internal utilities.
- **Metrics tests run sequentially**: VM/metrics uses a global default registry, so tests reading `vm.WritePrometheus` may observe other tests' writes. We omit `t.Parallel()` in metrics tests and assert with substring matches tolerant to ordering.

### Implementation Order

1. `infrastructure/config/helpers.go` (no deps)
2. `infrastructure/config/config.go` (deps: helpers)
3. `infrastructure/logger/logger.go` (deps: config.LoggerConfig)
4. `infrastructure/metrics/metrics.go` (no deps)
5. Все тесты

---

### 1. Config helpers

#### File: `infrastructure/config/helpers.go`

```go
package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// getEnvOrDefault returns env[key] if non-empty, otherwise def.
func getEnvOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// getEnvOrDefaultInt parses env[key] as int. Returns def if unset; error if set but invalid.
func getEnvOrDefaultInt(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid %s=%q: %w", key, v, err)
	}
	return n, nil
}

// getEnvOrDefaultBool parses env[key] as bool. Returns def if unset; error if set but invalid.
func getEnvOrDefaultBool(key string, def bool) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("invalid %s=%q: %w", key, v, err)
	}
	return b, nil
}

// getEnvOrDefaultDuration parses env[key] as time.Duration. Returns def if unset; error if invalid.
func getEnvOrDefaultDuration(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid %s=%q: %w", key, v, err)
	}
	return d, nil
}

// getEnvOrDefaultByte parses env[key] as int and narrows to byte. Bounds check [0, 255].
func getEnvOrDefaultByte(key string, def byte) (byte, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid %s=%q: %w", key, v, err)
	}
	if n < 0 || n > 255 {
		return 0, fmt.Errorf("invalid %s=%d: out of byte range [0,255]", key, n)
	}
	//nolint:gosec // bounded by check above
	return byte(n), nil
}

// getEnvOrDefaultFloat parses env[key] as float64. Returns def if unset; error if invalid.
func getEnvOrDefaultFloat(key string, def float64) (float64, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s=%q: %w", key, v, err)
	}
	return f, nil
}
```

#### File: `infrastructure/config/helpers_test.go`

```go
package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetEnvOrDefault(t *testing.T) {
	t.Run("unset returns default", func(t *testing.T) {
		t.Setenv("TEST_STRING_VAR_UNSET", "")
		assert.Equal(t, "def", getEnvOrDefault("TEST_STRING_VAR_UNSET", "def"))
	})

	t.Run("set returns value", func(t *testing.T) {
		t.Setenv("TEST_STRING_VAR_SET", "hello")
		assert.Equal(t, "hello", getEnvOrDefault("TEST_STRING_VAR_SET", "def"))
	})
}

func TestGetEnvOrDefaultInt(t *testing.T) {
	t.Run("unset returns default", func(t *testing.T) {
		t.Setenv("TEST_INT_UNSET", "")
		v, err := getEnvOrDefaultInt("TEST_INT_UNSET", 42)
		require.NoError(t, err)
		assert.Equal(t, 42, v)
	})

	t.Run("set valid", func(t *testing.T) {
		t.Setenv("TEST_INT_VALID", "123")
		v, err := getEnvOrDefaultInt("TEST_INT_VALID", 0)
		require.NoError(t, err)
		assert.Equal(t, 123, v)
	})

	t.Run("set invalid returns error", func(t *testing.T) {
		t.Setenv("TEST_INT_INVALID", "abc")
		_, err := getEnvOrDefaultInt("TEST_INT_INVALID", 0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "TEST_INT_INVALID")
	})
}

func TestGetEnvOrDefaultBool(t *testing.T) {
	t.Run("unset returns default", func(t *testing.T) {
		t.Setenv("TEST_BOOL_UNSET", "")
		v, err := getEnvOrDefaultBool("TEST_BOOL_UNSET", true)
		require.NoError(t, err)
		assert.True(t, v)
	})

	t.Run("set valid true", func(t *testing.T) {
		t.Setenv("TEST_BOOL_TRUE", "true")
		v, err := getEnvOrDefaultBool("TEST_BOOL_TRUE", false)
		require.NoError(t, err)
		assert.True(t, v)
	})

	t.Run("set invalid", func(t *testing.T) {
		t.Setenv("TEST_BOOL_INVALID", "notabool")
		_, err := getEnvOrDefaultBool("TEST_BOOL_INVALID", false)
		require.Error(t, err)
	})
}

func TestGetEnvOrDefaultDuration(t *testing.T) {
	t.Run("unset returns default", func(t *testing.T) {
		t.Setenv("TEST_DUR_UNSET", "")
		v, err := getEnvOrDefaultDuration("TEST_DUR_UNSET", 5*time.Second)
		require.NoError(t, err)
		assert.Equal(t, 5*time.Second, v)
	})

	t.Run("set valid", func(t *testing.T) {
		t.Setenv("TEST_DUR_VALID", "1m30s")
		v, err := getEnvOrDefaultDuration("TEST_DUR_VALID", 0)
		require.NoError(t, err)
		assert.Equal(t, 90*time.Second, v)
	})

	t.Run("set invalid", func(t *testing.T) {
		t.Setenv("TEST_DUR_INVALID", "abc")
		_, err := getEnvOrDefaultDuration("TEST_DUR_INVALID", 0)
		require.Error(t, err)
	})
}

func TestGetEnvOrDefaultByte(t *testing.T) {
	t.Run("unset returns default", func(t *testing.T) {
		t.Setenv("TEST_BYTE_UNSET", "")
		v, err := getEnvOrDefaultByte("TEST_BYTE_UNSET", 7)
		require.NoError(t, err)
		assert.Equal(t, byte(7), v)
	})

	t.Run("set valid", func(t *testing.T) {
		t.Setenv("TEST_BYTE_VALID", "200")
		v, err := getEnvOrDefaultByte("TEST_BYTE_VALID", 0)
		require.NoError(t, err)
		assert.Equal(t, byte(200), v)
	})

	t.Run("set invalid string", func(t *testing.T) {
		t.Setenv("TEST_BYTE_BAD", "abc")
		_, err := getEnvOrDefaultByte("TEST_BYTE_BAD", 0)
		require.Error(t, err)
	})

	t.Run("out of range high", func(t *testing.T) {
		t.Setenv("TEST_BYTE_HIGH", "256")
		_, err := getEnvOrDefaultByte("TEST_BYTE_HIGH", 0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "out of byte range")
	})

	t.Run("out of range low", func(t *testing.T) {
		t.Setenv("TEST_BYTE_LOW", "-1")
		_, err := getEnvOrDefaultByte("TEST_BYTE_LOW", 0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "out of byte range")
	})
}

func TestGetEnvOrDefaultFloat(t *testing.T) {
	t.Run("unset returns default", func(t *testing.T) {
		t.Setenv("TEST_FLOAT_UNSET", "")
		v, err := getEnvOrDefaultFloat("TEST_FLOAT_UNSET", 1.5)
		require.NoError(t, err)
		assert.InDelta(t, 1.5, v, 0.0001)
	})

	t.Run("set valid", func(t *testing.T) {
		t.Setenv("TEST_FLOAT_VALID", "3.14")
		v, err := getEnvOrDefaultFloat("TEST_FLOAT_VALID", 0)
		require.NoError(t, err)
		assert.InDelta(t, 3.14, v, 0.0001)
	})

	t.Run("set invalid", func(t *testing.T) {
		t.Setenv("TEST_FLOAT_BAD", "abc")
		_, err := getEnvOrDefaultFloat("TEST_FLOAT_BAD", 0)
		require.Error(t, err)
	})
}
```

### 2. Config struct + Load

#### File: `infrastructure/config/config.go`

```go
package config

import (
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Config is the root application configuration loaded from environment variables.
type Config struct {
	Modbus        ModbusConfig
	MQTT          MQTTConfig
	HomeAssistant HAConfig
	Polling       PollingConfig
	HTTP          HTTPConfig
	Logger        LoggerConfig
	Reconnect     ReconnectConfig
}

// ModbusConfig holds Modbus TCP client settings.
type ModbusConfig struct {
	Host           string
	Port           int
	SlaveID        byte
	ConnectTimeout time.Duration
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
	GuardInterval  time.Duration
}

// Addr returns the Modbus TCP "host:port" address.
func (m ModbusConfig) Addr() string {
	return fmt.Sprintf("%s:%d", m.Host, m.Port)
}

// MQTTConfig holds MQTT broker connection settings.
type MQTTConfig struct {
	Broker    string
	ClientID  string
	Username  string
	Password  string
	Keepalive time.Duration
	QoS       byte
}

// HAConfig holds Home Assistant MQTT discovery settings.
type HAConfig struct {
	DiscoveryPrefix string
	DevicePrefix    string
	DeviceName      string
	Manufacturer    string
	Model           string
}

// PollingConfig holds tier intervals and availability threshold.
type PollingConfig struct {
	HotInterval           time.Duration
	MediumInterval        time.Duration
	SlowInterval          time.Duration
	AvailabilityThreshold time.Duration
}

// HTTPConfig holds the HTTP server port for /health and /metrics.
type HTTPConfig struct {
	Port int
}

// Addr returns the HTTP listen address bound to all interfaces.
func (h HTTPConfig) Addr() string {
	return "0.0.0.0:" + strconv.Itoa(h.Port)
}

// LoggerConfig holds logging level (format is always JSON).
type LoggerConfig struct {
	Level string
}

// ReconnectConfig holds backoff parameters for reconnect loops.
type ReconnectConfig struct {
	MinDelay  time.Duration
	MaxDelay  time.Duration
	Factor    float64
	JitterPct float64
}

// Load reads configuration from environment variables, applies defaults, and validates.
func Load() (*Config, error) {
	cfg := &Config{}
	var err error

	// Modbus
	cfg.Modbus.Host = getEnvOrDefault("MODBUS_HOST", "10.90.19.7")
	if cfg.Modbus.Port, err = getEnvOrDefaultInt("MODBUS_PORT", 502); err != nil {
		return nil, fmt.Errorf("loading MODBUS_PORT: %w", err)
	}
	if cfg.Modbus.SlaveID, err = getEnvOrDefaultByte("MODBUS_SLAVE_ID", 1); err != nil {
		return nil, fmt.Errorf("loading MODBUS_SLAVE_ID: %w", err)
	}
	if cfg.Modbus.ConnectTimeout, err = getEnvOrDefaultDuration("MODBUS_CONNECT_TIMEOUT", 5*time.Second); err != nil {
		return nil, fmt.Errorf("loading MODBUS_CONNECT_TIMEOUT: %w", err)
	}
	if cfg.Modbus.ReadTimeout, err = getEnvOrDefaultDuration("MODBUS_READ_TIMEOUT", 2*time.Second); err != nil {
		return nil, fmt.Errorf("loading MODBUS_READ_TIMEOUT: %w", err)
	}
	if cfg.Modbus.WriteTimeout, err = getEnvOrDefaultDuration("MODBUS_WRITE_TIMEOUT", 2*time.Second); err != nil {
		return nil, fmt.Errorf("loading MODBUS_WRITE_TIMEOUT: %w", err)
	}
	if cfg.Modbus.GuardInterval, err = getEnvOrDefaultDuration("MODBUS_GUARD_INTERVAL", 100*time.Millisecond); err != nil {
		return nil, fmt.Errorf("loading MODBUS_GUARD_INTERVAL: %w", err)
	}

	// MQTT
	cfg.MQTT.Broker = getEnvOrDefault("MQTT_BROKER", "10.90.19.10:1883")
	cfg.MQTT.ClientID = getEnvOrDefault("MQTT_CLIENT_ID", "oasis-modbus2mqtt")
	cfg.MQTT.Username = getEnvOrDefault("MQTT_USERNAME", "")
	cfg.MQTT.Password = getEnvOrDefault("MQTT_PASSWORD", "")
	if cfg.MQTT.Keepalive, err = getEnvOrDefaultDuration("MQTT_KEEPALIVE", 30*time.Second); err != nil {
		return nil, fmt.Errorf("loading MQTT_KEEPALIVE: %w", err)
	}
	if cfg.MQTT.QoS, err = getEnvOrDefaultByte("MQTT_QOS", 1); err != nil {
		return nil, fmt.Errorf("loading MQTT_QOS: %w", err)
	}

	// HomeAssistant
	cfg.HomeAssistant.DiscoveryPrefix = getEnvOrDefault("HA_DISCOVERY_PREFIX", "homeassistant")
	cfg.HomeAssistant.DevicePrefix = getEnvOrDefault("HA_DEVICE_PREFIX", "oasis_syberia")
	cfg.HomeAssistant.DeviceName = getEnvOrDefault("HA_DEVICE_NAME", "Oasis Syberia")
	cfg.HomeAssistant.Manufacturer = getEnvOrDefault("HA_MANUFACTURER", "GTC")
	cfg.HomeAssistant.Model = getEnvOrDefault("HA_MODEL", "Syberia 5")

	// Polling
	if cfg.Polling.HotInterval, err = getEnvOrDefaultDuration("POLL_HOT_INTERVAL", 5*time.Second); err != nil {
		return nil, fmt.Errorf("loading POLL_HOT_INTERVAL: %w", err)
	}
	if cfg.Polling.MediumInterval, err = getEnvOrDefaultDuration("POLL_MEDIUM_INTERVAL", 15*time.Second); err != nil {
		return nil, fmt.Errorf("loading POLL_MEDIUM_INTERVAL: %w", err)
	}
	if cfg.Polling.SlowInterval, err = getEnvOrDefaultDuration("POLL_SLOW_INTERVAL", 60*time.Second); err != nil {
		return nil, fmt.Errorf("loading POLL_SLOW_INTERVAL: %w", err)
	}
	if cfg.Polling.AvailabilityThreshold, err = getEnvOrDefaultDuration("AVAILABILITY_THRESHOLD", 30*time.Second); err != nil {
		return nil, fmt.Errorf("loading AVAILABILITY_THRESHOLD: %w", err)
	}

	// HTTP
	if cfg.HTTP.Port, err = getEnvOrDefaultInt("HTTP_PORT", 8080); err != nil {
		return nil, fmt.Errorf("loading HTTP_PORT: %w", err)
	}

	// Logger
	cfg.Logger.Level = getEnvOrDefault("LOG_LEVEL", "info")

	// Reconnect
	if cfg.Reconnect.MinDelay, err = getEnvOrDefaultDuration("RECONNECT_MIN_DELAY", 1*time.Second); err != nil {
		return nil, fmt.Errorf("loading RECONNECT_MIN_DELAY: %w", err)
	}
	if cfg.Reconnect.MaxDelay, err = getEnvOrDefaultDuration("RECONNECT_MAX_DELAY", 60*time.Second); err != nil {
		return nil, fmt.Errorf("loading RECONNECT_MAX_DELAY: %w", err)
	}
	if cfg.Reconnect.Factor, err = getEnvOrDefaultFloat("RECONNECT_FACTOR", 2.0); err != nil {
		return nil, fmt.Errorf("loading RECONNECT_FACTOR: %w", err)
	}
	if cfg.Reconnect.JitterPct, err = getEnvOrDefaultFloat("RECONNECT_JITTER_PCT", 0.2); err != nil {
		return nil, fmt.Errorf("loading RECONNECT_JITTER_PCT: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	return cfg, nil
}

// Validate checks invariants across all sub-configs.
func (c *Config) Validate() error {
	if c.Modbus.Host == "" {
		return errors.New("validate: MODBUS_HOST is empty")
	}
	if c.Modbus.Port < 1 || c.Modbus.Port > 65535 {
		return fmt.Errorf("validate: MODBUS_PORT out of range [1,65535]: %d", c.Modbus.Port)
	}
	if c.Modbus.SlaveID < 1 || c.Modbus.SlaveID > 247 {
		return fmt.Errorf("validate: MODBUS_SLAVE_ID out of range [1,247]: %d", c.Modbus.SlaveID)
	}
	if c.Modbus.ConnectTimeout <= 0 {
		return fmt.Errorf("validate: MODBUS_CONNECT_TIMEOUT must be > 0: %s", c.Modbus.ConnectTimeout)
	}
	if c.Modbus.ReadTimeout <= 0 {
		return fmt.Errorf("validate: MODBUS_READ_TIMEOUT must be > 0: %s", c.Modbus.ReadTimeout)
	}
	if c.Modbus.WriteTimeout <= 0 {
		return fmt.Errorf("validate: MODBUS_WRITE_TIMEOUT must be > 0: %s", c.Modbus.WriteTimeout)
	}
	if c.Modbus.GuardInterval < 0 {
		return fmt.Errorf("validate: MODBUS_GUARD_INTERVAL must be >= 0: %s", c.Modbus.GuardInterval)
	}

	if c.MQTT.Broker == "" {
		return errors.New("validate: MQTT_BROKER is empty")
	}
	if c.MQTT.ClientID == "" {
		return errors.New("validate: MQTT_CLIENT_ID is empty")
	}
	if c.MQTT.QoS > 2 {
		return fmt.Errorf("validate: MQTT_QOS out of range [0,2]: %d", c.MQTT.QoS)
	}
	if c.MQTT.Keepalive <= 0 {
		return fmt.Errorf("validate: MQTT_KEEPALIVE must be > 0: %s", c.MQTT.Keepalive)
	}

	if c.HomeAssistant.DevicePrefix == "" {
		return errors.New("validate: HA_DEVICE_PREFIX is empty")
	}

	if c.HTTP.Port < 1 || c.HTTP.Port > 65535 {
		return fmt.Errorf("validate: HTTP_PORT out of range [1,65535]: %d", c.HTTP.Port)
	}

	if c.Polling.HotInterval <= 0 {
		return fmt.Errorf("validate: POLL_HOT_INTERVAL must be > 0: %s", c.Polling.HotInterval)
	}
	if c.Polling.MediumInterval <= 0 {
		return fmt.Errorf("validate: POLL_MEDIUM_INTERVAL must be > 0: %s", c.Polling.MediumInterval)
	}
	if c.Polling.SlowInterval <= 0 {
		return fmt.Errorf("validate: POLL_SLOW_INTERVAL must be > 0: %s", c.Polling.SlowInterval)
	}
	if c.Polling.AvailabilityThreshold <= 0 {
		return fmt.Errorf("validate: AVAILABILITY_THRESHOLD must be > 0: %s", c.Polling.AvailabilityThreshold)
	}
	if c.Polling.HotInterval > c.Polling.MediumInterval {
		return fmt.Errorf("validate: POLL_HOT_INTERVAL (%s) must be <= POLL_MEDIUM_INTERVAL (%s)",
			c.Polling.HotInterval, c.Polling.MediumInterval)
	}
	if c.Polling.MediumInterval > c.Polling.SlowInterval {
		return fmt.Errorf("validate: POLL_MEDIUM_INTERVAL (%s) must be <= POLL_SLOW_INTERVAL (%s)",
			c.Polling.MediumInterval, c.Polling.SlowInterval)
	}

	if c.Reconnect.MinDelay <= 0 {
		return fmt.Errorf("validate: RECONNECT_MIN_DELAY must be > 0: %s", c.Reconnect.MinDelay)
	}
	if c.Reconnect.MaxDelay <= 0 {
		return fmt.Errorf("validate: RECONNECT_MAX_DELAY must be > 0: %s", c.Reconnect.MaxDelay)
	}
	if c.Reconnect.MinDelay > c.Reconnect.MaxDelay {
		return fmt.Errorf("validate: RECONNECT_MIN_DELAY (%s) must be <= RECONNECT_MAX_DELAY (%s)",
			c.Reconnect.MinDelay, c.Reconnect.MaxDelay)
	}
	if c.Reconnect.Factor <= 1.0 {
		return fmt.Errorf("validate: RECONNECT_FACTOR must be > 1.0: %f", c.Reconnect.Factor)
	}
	if c.Reconnect.JitterPct < 0 || c.Reconnect.JitterPct > 1.0 {
		return fmt.Errorf("validate: RECONNECT_JITTER_PCT out of range [0,1]: %f", c.Reconnect.JitterPct)
	}

	return nil
}
```

#### File: `infrastructure/config/config_test.go`

```go
package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/config"
)

// allEnvVars lists every env var read by config.Load.
// Used to neutralize ambient CI environment in tests that need defaults.
var allEnvVars = []string{
	"MODBUS_HOST",
	"MODBUS_PORT",
	"MODBUS_SLAVE_ID",
	"MODBUS_CONNECT_TIMEOUT",
	"MODBUS_READ_TIMEOUT",
	"MODBUS_WRITE_TIMEOUT",
	"MODBUS_GUARD_INTERVAL",
	"MQTT_BROKER",
	"MQTT_CLIENT_ID",
	"MQTT_USERNAME",
	"MQTT_PASSWORD",
	"MQTT_KEEPALIVE",
	"MQTT_QOS",
	"HA_DISCOVERY_PREFIX",
	"HA_DEVICE_PREFIX",
	"HA_DEVICE_NAME",
	"HA_MANUFACTURER",
	"HA_MODEL",
	"POLL_HOT_INTERVAL",
	"POLL_MEDIUM_INTERVAL",
	"POLL_SLOW_INTERVAL",
	"AVAILABILITY_THRESHOLD",
	"HTTP_PORT",
	"LOG_LEVEL",
	"RECONNECT_MIN_DELAY",
	"RECONNECT_MAX_DELAY",
	"RECONNECT_FACTOR",
	"RECONNECT_JITTER_PCT",
}

func clearAllConfigEnvs(t *testing.T) {
	t.Helper()
	for _, k := range allEnvVars {
		t.Setenv(k, "")
	}
}

func TestLoad_Defaults(t *testing.T) {
	clearAllConfigEnvs(t)

	cfg, err := config.Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "10.90.19.7", cfg.Modbus.Host)
	assert.Equal(t, 502, cfg.Modbus.Port)
	assert.Equal(t, byte(1), cfg.Modbus.SlaveID)
	assert.Equal(t, 5*time.Second, cfg.Modbus.ConnectTimeout)
	assert.Equal(t, 2*time.Second, cfg.Modbus.ReadTimeout)
	assert.Equal(t, 2*time.Second, cfg.Modbus.WriteTimeout)
	assert.Equal(t, 100*time.Millisecond, cfg.Modbus.GuardInterval)

	assert.Equal(t, "10.90.19.10:1883", cfg.MQTT.Broker)
	assert.Equal(t, "oasis-modbus2mqtt", cfg.MQTT.ClientID)
	assert.Equal(t, "", cfg.MQTT.Username)
	assert.Equal(t, "", cfg.MQTT.Password)
	assert.Equal(t, 30*time.Second, cfg.MQTT.Keepalive)
	assert.Equal(t, byte(1), cfg.MQTT.QoS)

	assert.Equal(t, "homeassistant", cfg.HomeAssistant.DiscoveryPrefix)
	assert.Equal(t, "oasis_syberia", cfg.HomeAssistant.DevicePrefix)
	assert.Equal(t, "Oasis Syberia", cfg.HomeAssistant.DeviceName)
	assert.Equal(t, "GTC", cfg.HomeAssistant.Manufacturer)
	assert.Equal(t, "Syberia 5", cfg.HomeAssistant.Model)

	assert.Equal(t, 5*time.Second, cfg.Polling.HotInterval)
	assert.Equal(t, 15*time.Second, cfg.Polling.MediumInterval)
	assert.Equal(t, 60*time.Second, cfg.Polling.SlowInterval)
	assert.Equal(t, 30*time.Second, cfg.Polling.AvailabilityThreshold)

	assert.Equal(t, 8080, cfg.HTTP.Port)
	assert.Equal(t, "info", cfg.Logger.Level)

	assert.Equal(t, 1*time.Second, cfg.Reconnect.MinDelay)
	assert.Equal(t, 60*time.Second, cfg.Reconnect.MaxDelay)
	assert.InDelta(t, 2.0, cfg.Reconnect.Factor, 0.0001)
	assert.InDelta(t, 0.2, cfg.Reconnect.JitterPct, 0.0001)
}

func TestLoad_AllFromEnv(t *testing.T) {
	t.Setenv("MODBUS_HOST", "192.168.1.10")
	t.Setenv("MODBUS_PORT", "5020")
	t.Setenv("MODBUS_SLAVE_ID", "5")
	t.Setenv("MODBUS_CONNECT_TIMEOUT", "10s")
	t.Setenv("MODBUS_READ_TIMEOUT", "3s")
	t.Setenv("MODBUS_WRITE_TIMEOUT", "4s")
	t.Setenv("MODBUS_GUARD_INTERVAL", "200ms")

	t.Setenv("MQTT_BROKER", "mqtt.local:1884")
	t.Setenv("MQTT_CLIENT_ID", "test-client")
	t.Setenv("MQTT_USERNAME", "user")
	t.Setenv("MQTT_PASSWORD", "pass")
	t.Setenv("MQTT_KEEPALIVE", "45s")
	t.Setenv("MQTT_QOS", "2")

	t.Setenv("HA_DISCOVERY_PREFIX", "ha")
	t.Setenv("HA_DEVICE_PREFIX", "test_dev")
	t.Setenv("HA_DEVICE_NAME", "Test Device")
	t.Setenv("HA_MANUFACTURER", "Acme")
	t.Setenv("HA_MODEL", "Model X")

	t.Setenv("POLL_HOT_INTERVAL", "1s")
	t.Setenv("POLL_MEDIUM_INTERVAL", "10s")
	t.Setenv("POLL_SLOW_INTERVAL", "120s")
	t.Setenv("AVAILABILITY_THRESHOLD", "45s")

	t.Setenv("HTTP_PORT", "9090")
	t.Setenv("LOG_LEVEL", "debug")

	t.Setenv("RECONNECT_MIN_DELAY", "2s")
	t.Setenv("RECONNECT_MAX_DELAY", "120s")
	t.Setenv("RECONNECT_FACTOR", "3.0")
	t.Setenv("RECONNECT_JITTER_PCT", "0.5")

	cfg, err := config.Load()
	require.NoError(t, err)

	assert.Equal(t, "192.168.1.10", cfg.Modbus.Host)
	assert.Equal(t, 5020, cfg.Modbus.Port)
	assert.Equal(t, byte(5), cfg.Modbus.SlaveID)
	assert.Equal(t, 10*time.Second, cfg.Modbus.ConnectTimeout)
	assert.Equal(t, 3*time.Second, cfg.Modbus.ReadTimeout)
	assert.Equal(t, 4*time.Second, cfg.Modbus.WriteTimeout)
	assert.Equal(t, 200*time.Millisecond, cfg.Modbus.GuardInterval)

	assert.Equal(t, "mqtt.local:1884", cfg.MQTT.Broker)
	assert.Equal(t, "test-client", cfg.MQTT.ClientID)
	assert.Equal(t, "user", cfg.MQTT.Username)
	assert.Equal(t, "pass", cfg.MQTT.Password)
	assert.Equal(t, 45*time.Second, cfg.MQTT.Keepalive)
	assert.Equal(t, byte(2), cfg.MQTT.QoS)

	assert.Equal(t, "ha", cfg.HomeAssistant.DiscoveryPrefix)
	assert.Equal(t, "test_dev", cfg.HomeAssistant.DevicePrefix)
	assert.Equal(t, "Test Device", cfg.HomeAssistant.DeviceName)
	assert.Equal(t, "Acme", cfg.HomeAssistant.Manufacturer)
	assert.Equal(t, "Model X", cfg.HomeAssistant.Model)

	assert.Equal(t, 1*time.Second, cfg.Polling.HotInterval)
	assert.Equal(t, 10*time.Second, cfg.Polling.MediumInterval)
	assert.Equal(t, 120*time.Second, cfg.Polling.SlowInterval)
	assert.Equal(t, 45*time.Second, cfg.Polling.AvailabilityThreshold)

	assert.Equal(t, 9090, cfg.HTTP.Port)
	assert.Equal(t, "debug", cfg.Logger.Level)

	assert.Equal(t, 2*time.Second, cfg.Reconnect.MinDelay)
	assert.Equal(t, 120*time.Second, cfg.Reconnect.MaxDelay)
	assert.InDelta(t, 3.0, cfg.Reconnect.Factor, 0.0001)
	assert.InDelta(t, 0.5, cfg.Reconnect.JitterPct, 0.0001)
}

func TestLoad_InvalidPort(t *testing.T) {
	clearAllConfigEnvs(t)
	t.Setenv("MODBUS_PORT", "abc")

	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MODBUS_PORT")
}

func TestLoad_InvalidDuration(t *testing.T) {
	clearAllConfigEnvs(t)
	t.Setenv("MQTT_KEEPALIVE", "notaduration")

	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MQTT_KEEPALIVE")
}

func TestLoad_InvalidByte(t *testing.T) {
	clearAllConfigEnvs(t)
	t.Setenv("MODBUS_SLAVE_ID", "999")

	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MODBUS_SLAVE_ID")
}

func TestLoad_InvalidFloat(t *testing.T) {
	clearAllConfigEnvs(t)
	t.Setenv("RECONNECT_FACTOR", "notafloat")

	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RECONNECT_FACTOR")
}

// validBaseConfig builds an in-memory Config that passes Validate.
func validBaseConfig() *config.Config {
	return &config.Config{
		Modbus: config.ModbusConfig{
			Host:           "10.0.0.1",
			Port:           502,
			SlaveID:        1,
			ConnectTimeout: 5 * time.Second,
			ReadTimeout:    2 * time.Second,
			WriteTimeout:   2 * time.Second,
			GuardInterval:  100 * time.Millisecond,
		},
		MQTT: config.MQTTConfig{
			Broker:    "mqtt:1883",
			ClientID:  "id",
			Keepalive: 30 * time.Second,
			QoS:       1,
		},
		HomeAssistant: config.HAConfig{
			DiscoveryPrefix: "homeassistant",
			DevicePrefix:    "oasis_syberia",
			DeviceName:      "Oasis Syberia",
			Manufacturer:    "GTC",
			Model:           "Syberia 5",
		},
		Polling: config.PollingConfig{
			HotInterval:           5 * time.Second,
			MediumInterval:        15 * time.Second,
			SlowInterval:          60 * time.Second,
			AvailabilityThreshold: 30 * time.Second,
		},
		HTTP:   config.HTTPConfig{Port: 8080},
		Logger: config.LoggerConfig{Level: "info"},
		Reconnect: config.ReconnectConfig{
			MinDelay:  1 * time.Second,
			MaxDelay:  60 * time.Second,
			Factor:    2.0,
			JitterPct: 0.2,
		},
	}
}

func TestValidate_Valid(t *testing.T) {
	t.Parallel()
	cfg := validBaseConfig()
	require.NoError(t, cfg.Validate())
}

func TestValidate_InvalidCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mutate    func(*config.Config)
		wantInErr string
	}{
		{
			name:      "empty modbus host",
			mutate:    func(c *config.Config) { c.Modbus.Host = "" },
			wantInErr: "MODBUS_HOST",
		},
		{
			name:      "modbus port zero",
			mutate:    func(c *config.Config) { c.Modbus.Port = 0 },
			wantInErr: "MODBUS_PORT",
		},
		{
			name:      "modbus port too high",
			mutate:    func(c *config.Config) { c.Modbus.Port = 70000 },
			wantInErr: "MODBUS_PORT",
		},
		{
			name:      "slave id zero",
			mutate:    func(c *config.Config) { c.Modbus.SlaveID = 0 },
			wantInErr: "MODBUS_SLAVE_ID",
		},
		{
			name:      "slave id too high",
			mutate:    func(c *config.Config) { c.Modbus.SlaveID = 248 },
			wantInErr: "MODBUS_SLAVE_ID",
		},
		{
			name:      "empty broker",
			mutate:    func(c *config.Config) { c.MQTT.Broker = "" },
			wantInErr: "MQTT_BROKER",
		},
		{
			name:      "empty client id",
			mutate:    func(c *config.Config) { c.MQTT.ClientID = "" },
			wantInErr: "MQTT_CLIENT_ID",
		},
		{
			name:      "qos too high",
			mutate:    func(c *config.Config) { c.MQTT.QoS = 3 },
			wantInErr: "MQTT_QOS",
		},
		{
			name:      "empty device prefix",
			mutate:    func(c *config.Config) { c.HomeAssistant.DevicePrefix = "" },
			wantInErr: "HA_DEVICE_PREFIX",
		},
		{
			name:      "http port zero",
			mutate:    func(c *config.Config) { c.HTTP.Port = 0 },
			wantInErr: "HTTP_PORT",
		},
		{
			name:      "hot greater than medium",
			mutate:    func(c *config.Config) { c.Polling.HotInterval = 30 * time.Second },
			wantInErr: "POLL_HOT_INTERVAL",
		},
		{
			name:      "medium greater than slow",
			mutate:    func(c *config.Config) { c.Polling.MediumInterval = 120 * time.Second },
			wantInErr: "POLL_MEDIUM_INTERVAL",
		},
		{
			name:      "factor equals one",
			mutate:    func(c *config.Config) { c.Reconnect.Factor = 1.0 },
			wantInErr: "RECONNECT_FACTOR",
		},
		{
			name:      "jitter negative",
			mutate:    func(c *config.Config) { c.Reconnect.JitterPct = -0.1 },
			wantInErr: "RECONNECT_JITTER_PCT",
		},
		{
			name:      "jitter above one",
			mutate:    func(c *config.Config) { c.Reconnect.JitterPct = 1.5 },
			wantInErr: "RECONNECT_JITTER_PCT",
		},
		{
			name:      "min delay greater than max",
			mutate:    func(c *config.Config) { c.Reconnect.MinDelay = 120 * time.Second },
			wantInErr: "RECONNECT_MIN_DELAY",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := validBaseConfig()
			tc.mutate(cfg)
			err := cfg.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantInErr)
		})
	}
}

func TestModbusConfig_Addr(t *testing.T) {
	t.Parallel()
	m := config.ModbusConfig{Host: "1.2.3.4", Port: 502}
	assert.Equal(t, "1.2.3.4:502", m.Addr())
}

func TestHTTPConfig_Addr(t *testing.T) {
	t.Parallel()
	h := config.HTTPConfig{Port: 8080}
	assert.Equal(t, "0.0.0.0:8080", h.Addr())
}
```

### 3. Logger

#### File: `infrastructure/logger/logger.go`

```go
package logger

import (
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/config"
)

// ParseLevel converts a string level to slog.Level. Unknown values map to LevelInfo.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	case "info":
		return slog.LevelInfo
	default:
		return slog.LevelInfo
	}
}

// New returns a JSON slog.Logger writing to os.Stdout at the level from cfg.
func New(cfg config.LoggerConfig) *slog.Logger {
	return NewWithWriter(cfg, os.Stdout)
}

// NewWithWriter returns a JSON slog.Logger writing to w at the level from cfg.
// Useful for tests that need to capture output.
func NewWithWriter(cfg config.LoggerConfig, w io.Writer) *slog.Logger {
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: ParseLevel(cfg.Level),
	})
	return slog.New(handler)
}
```

#### File: `infrastructure/logger/logger_test.go`

```go
package logger_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/config"
	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/logger"
)

func TestParseLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"INFO", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"ERROR", slog.LevelError},
		{"", slog.LevelInfo},
		{"unknown", slog.LevelInfo},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, logger.ParseLevel(tc.input))
		})
	}
}

func TestNew_DefaultsToStdout(t *testing.T) {
	t.Parallel()
	lg := logger.New(config.LoggerConfig{Level: "info"})
	require.NotNil(t, lg)
}

func TestNewWithWriter_Levels(t *testing.T) {
	t.Parallel()

	cases := []struct {
		level    string
		wantMsgs map[string]bool // msg → expected presence in output
	}{
		{
			level: "debug",
			wantMsgs: map[string]bool{
				"dbg-msg":  true,
				"info-msg": true,
				"warn-msg": true,
				"err-msg":  true,
			},
		},
		{
			level: "info",
			wantMsgs: map[string]bool{
				"dbg-msg":  false,
				"info-msg": true,
				"warn-msg": true,
				"err-msg":  true,
			},
		},
		{
			level: "warn",
			wantMsgs: map[string]bool{
				"dbg-msg":  false,
				"info-msg": false,
				"warn-msg": true,
				"err-msg":  true,
			},
		},
		{
			level: "error",
			wantMsgs: map[string]bool{
				"dbg-msg":  false,
				"info-msg": false,
				"warn-msg": false,
				"err-msg":  true,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.level, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			lg := logger.NewWithWriter(config.LoggerConfig{Level: tc.level}, &buf)

			lg.Debug("dbg-msg")
			lg.Info("info-msg")
			lg.Warn("warn-msg")
			lg.Error("err-msg")

			out := buf.String()
			for msg, want := range tc.wantMsgs {
				if want {
					assert.Contains(t, out, msg, "level=%s expected msg=%s in output", tc.level, msg)
				} else {
					assert.NotContains(t, out, msg, "level=%s expected msg=%s NOT in output", tc.level, msg)
				}
			}
		})
	}
}

func TestNewWithWriter_JSONFormat(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	lg := logger.NewWithWriter(config.LoggerConfig{Level: "info"}, &buf)

	lg.Info("hello", "key", "value")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 1)

	var entry map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &entry))
	assert.Equal(t, "hello", entry["msg"])
	assert.Equal(t, "value", entry["key"])
	assert.Equal(t, "INFO", entry["level"])
}
```

### 4. Metrics

#### File: `infrastructure/metrics/metrics.go`

```go
// Package metrics exposes VictoriaMetrics counters, histograms, and gauges for the service.
package metrics

import (
	"fmt"
	"sync/atomic"
	"time"

	vm "github.com/VictoriaMetrics/metrics"
)

// Static counters registered eagerly at package load.
var (
	// ModbusReconnectsTotal counts Modbus client reconnect events.
	ModbusReconnectsTotal = vm.NewCounter("oasis_modbus_reconnects_total")
	// MQTTReconnectsTotal counts MQTT client reconnect events.
	MQTTReconnectsTotal = vm.NewCounter("oasis_mqtt_reconnects_total")
	// JobsDroppedTotal counts jobs dropped across all tiers.
	JobsDroppedTotal = vm.NewCounter("oasis_jobs_dropped_total")
	// MQTTPublishesTotal counts MQTT publishes across all topics.
	MQTTPublishesTotal = vm.NewCounter("oasis_mqtt_publishes_total")
	// DiscoveryRepublishesTotal counts HA discovery config republish events.
	DiscoveryRepublishesTotal = vm.NewCounter("oasis_discovery_republishes_total")
)

// Atomic state backing gauge callbacks.
var (
	modbusConnectedFlag    atomic.Bool
	mqttConnectedFlag      atomic.Bool
	lastSuccessfulPollNano atomic.Int64
)

// Init registers gauge callbacks. Safe to call multiple times (idempotent).
// Must be called once after config load and before metrics endpoint starts.
func Init() {
	vm.GetOrCreateGauge("oasis_modbus_connected", func() float64 {
		if modbusConnectedFlag.Load() {
			return 1
		}
		return 0
	})
	vm.GetOrCreateGauge("oasis_mqtt_connected", func() float64 {
		if mqttConnectedFlag.Load() {
			return 1
		}
		return 0
	})
	vm.GetOrCreateGauge("oasis_last_successful_poll_unix_nanos", func() float64 {
		return float64(lastSuccessfulPollNano.Load())
	})
}

// SetModbusConnected updates the modbus connection gauge state.
func SetModbusConnected(connected bool) {
	modbusConnectedFlag.Store(connected)
}

// SetMQTTConnected updates the MQTT connection gauge state.
func SetMQTTConnected(connected bool) {
	mqttConnectedFlag.Store(connected)
}

// SetLastSuccessfulPoll records the timestamp of the latest successful poll.
func SetLastSuccessfulPoll(t time.Time) {
	lastSuccessfulPollNano.Store(t.UnixNano())
}

// ModbusOpsTotal returns the labeled counter for Modbus operations.
func ModbusOpsTotal(op, status string) *vm.Counter {
	return vm.GetOrCreateCounter(
		fmt.Sprintf(`oasis_modbus_ops_total{op=%q,status=%q}`, op, status),
	)
}

// ModbusOpDurationSeconds returns the labeled histogram for Modbus op latency.
func ModbusOpDurationSeconds(op string) *vm.Histogram {
	return vm.GetOrCreateHistogram(
		fmt.Sprintf(`oasis_modbus_op_duration_seconds{op=%q}`, op),
	)
}

// ModbusErrorsTotal returns the labeled counter for Modbus errors by type.
func ModbusErrorsTotal(errorType string) *vm.Counter {
	return vm.GetOrCreateCounter(
		fmt.Sprintf(`oasis_modbus_errors_total{error_type=%q}`, errorType),
	)
}

// JobsByTierDroppedTotal returns the labeled counter for dropped jobs per tier.
func JobsByTierDroppedTotal(tier string) *vm.Counter {
	return vm.GetOrCreateCounter(
		fmt.Sprintf(`oasis_jobs_by_tier_dropped_total{tier=%q}`, tier),
	)
}

// PollDurationSeconds returns the labeled histogram for poll duration per tier.
func PollDurationSeconds(tier string) *vm.Histogram {
	return vm.GetOrCreateHistogram(
		fmt.Sprintf(`oasis_poll_duration_seconds{tier=%q}`, tier),
	)
}

// MQTTPublishesByEntityTotal returns the labeled counter for publishes per entity.
func MQTTPublishesByEntityTotal(entity string) *vm.Counter {
	return vm.GetOrCreateCounter(
		fmt.Sprintf(`oasis_mqtt_publishes_by_entity_total{entity=%q}`, entity),
	)
}
```

#### File: `infrastructure/metrics/metrics_test.go`

```go
package metrics_test

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
	"time"

	vm "github.com/VictoriaMetrics/metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/metrics"
)

func dump(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	vm.WritePrometheus(&buf, false)
	return buf.String()
}

func TestInit_Idempotent(t *testing.T) {
	metrics.Init()
	metrics.Init()
	out := dump(t)
	assert.Contains(t, out, "oasis_modbus_connected")
	assert.Contains(t, out, "oasis_mqtt_connected")
	assert.Contains(t, out, "oasis_last_successful_poll_unix_nanos")
}

func TestStaticCounters_Increment(t *testing.T) {
	metrics.Init()
	before := metrics.ModbusReconnectsTotal.Get()
	metrics.ModbusReconnectsTotal.Inc()
	metrics.ModbusReconnectsTotal.Inc()
	assert.Equal(t, before+2, metrics.ModbusReconnectsTotal.Get())

	out := dump(t)
	assert.Contains(t, out, "oasis_modbus_reconnects_total")
}

func TestStaticCounters_AllNamesPresent(t *testing.T) {
	metrics.Init()
	metrics.MQTTReconnectsTotal.Inc()
	metrics.JobsDroppedTotal.Inc()
	metrics.MQTTPublishesTotal.Inc()
	metrics.DiscoveryRepublishesTotal.Inc()

	out := dump(t)
	for _, name := range []string{
		"oasis_mqtt_reconnects_total",
		"oasis_jobs_dropped_total",
		"oasis_mqtt_publishes_total",
		"oasis_discovery_republishes_total",
	} {
		assert.Contains(t, out, name, "missing %s in metrics dump", name)
	}
}

func TestModbusOpsTotal_Labeled(t *testing.T) {
	metrics.Init()
	c := metrics.ModbusOpsTotal("read_input", "ok")
	require.NotNil(t, c)
	c.Inc()

	c2 := metrics.ModbusOpsTotal("read_input", "ok")
	c2.Inc()

	out := dump(t)
	assert.Contains(t, out, `oasis_modbus_ops_total{op="read_input",status="ok"}`)
}

func TestModbusOpDurationSeconds_Labeled(t *testing.T) {
	metrics.Init()
	h := metrics.ModbusOpDurationSeconds("read_holding")
	require.NotNil(t, h)
	h.Update(0.5)

	out := dump(t)
	assert.Contains(t, out, "oasis_modbus_op_duration_seconds")
	assert.Contains(t, out, `op="read_holding"`)
}

func TestModbusErrorsTotal_Labeled(t *testing.T) {
	metrics.Init()
	metrics.ModbusErrorsTotal("timeout").Inc()
	out := dump(t)
	assert.Contains(t, out, `oasis_modbus_errors_total{error_type="timeout"}`)
}

func TestJobsByTierDroppedTotal_Labeled(t *testing.T) {
	metrics.Init()
	metrics.JobsByTierDroppedTotal("hot").Inc()
	out := dump(t)
	assert.Contains(t, out, `oasis_jobs_by_tier_dropped_total{tier="hot"}`)
}

func TestPollDurationSeconds_Labeled(t *testing.T) {
	metrics.Init()
	metrics.PollDurationSeconds("medium").Update(0.123)
	out := dump(t)
	assert.Contains(t, out, "oasis_poll_duration_seconds")
	assert.Contains(t, out, `tier="medium"`)
}

func TestMQTTPublishesByEntityTotal_Labeled(t *testing.T) {
	metrics.Init()
	metrics.MQTTPublishesByEntityTotal("oasis_syberia_temperature").Inc()
	out := dump(t)
	assert.Contains(t, out, `oasis_mqtt_publishes_by_entity_total{entity="oasis_syberia_temperature"}`)
}

func TestSetModbusConnected_Gauge(t *testing.T) {
	metrics.Init()

	metrics.SetModbusConnected(true)
	out := dump(t)
	assert.True(t, hasExactGaugeLine(out, "oasis_modbus_connected", 1),
		"expected oasis_modbus_connected 1 in:\n%s", out)

	metrics.SetModbusConnected(false)
	out = dump(t)
	assert.True(t, hasExactGaugeLine(out, "oasis_modbus_connected", 0),
		"expected oasis_modbus_connected 0 in:\n%s", out)
}

func TestSetMQTTConnected_Gauge(t *testing.T) {
	metrics.Init()

	metrics.SetMQTTConnected(true)
	out := dump(t)
	assert.True(t, hasExactGaugeLine(out, "oasis_mqtt_connected", 1))

	metrics.SetMQTTConnected(false)
	out = dump(t)
	assert.True(t, hasExactGaugeLine(out, "oasis_mqtt_connected", 0))
}

func TestSetLastSuccessfulPoll_Gauge(t *testing.T) {
	metrics.Init()

	known := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	metrics.SetLastSuccessfulPoll(known)

	out := dump(t)
	assert.Contains(t, out, "oasis_last_successful_poll_unix_nanos")
	expected := float64(known.UnixNano())
	require.True(t, gaugeValueEquals(out, "oasis_last_successful_poll_unix_nanos", expected),
		"expected gauge == %v, output:\n%s", expected, out)
}

// hasExactGaugeLine checks for a line exactly matching "<name> <numericValue>".
func hasExactGaugeLine(out, name string, value float64) bool {
	prefix := name + " "
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		got, err := strconv.ParseFloat(strings.TrimPrefix(line, prefix), 64)
		if err != nil {
			continue
		}
		if got == value {
			return true
		}
	}
	return false
}

// gaugeValueEquals checks for a "<name> <num>" line where num equals want.
func gaugeValueEquals(out, name string, want float64) bool {
	return hasExactGaugeLine(out, name, want)
}
```

---

status: ready
updated: 2026-04-25

---

## Agent Execution

### Implementation Agent Instructions

```
Task tool call:
- subagent_type: "general-purpose"
- model: "haiku"
- prompt: |
    You are an IMPLEMENTATION AGENT for story 005-infrastructure-config-logger-metrics.

    YOUR ONLY JOB: Copy code from the story file to actual files. Run verification.

    ABSOLUTE RULES:
    - Copy code BYTE-FOR-BYTE.
    - DO NOT modify code. DO NOT add `//nolint`. DO NOT fix imports.
    - If lint fails — STOP, report verbatim. Do NOT invent fixes.

    PROCESS:
    1. Read story: /Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/documentation/stories/005-infrastructure-config-logger-metrics.md
    2. For each "#### File: `path`" section, write to that path
    3. Run verification IN ORDER from work dir:
       a. go mod tidy   (will pull github.com/VictoriaMetrics/metrics)
       b. gofmt -l infrastructure/   (must be empty)
       c. go build ./...
       d. go vet ./...
       e. go test -short ./...
       f. coverage:
          - infrastructure/config ≥80%
          - infrastructure/logger ≥80%
          - infrastructure/metrics ≥70%
       g. golangci-lint run ./...   (0 issues)
    4. If ALL pass: status: review, fill Verification Results, tick Progress, refresh Files Changed (include go.mod, go.sum).
    5. If ANY fail: append errors verbatim to Issues Found, status: in_progress, STOP.

    Work dir: /Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/
```

### Fix Agent Instructions

Standard. Edit story only.

---

## Implementation Notes

### Progress

- [ ] `infrastructure/config/helpers.go` (+ test)
- [ ] `infrastructure/config/config.go` (+ test)
- [ ] `infrastructure/logger/logger.go` (+ test)
- [ ] `infrastructure/metrics/metrics.go` (+ test)
- [ ] gofmt clean
- [ ] go build/vet pass
- [ ] go test pass
- [ ] config cov ≥80%
- [ ] logger cov ≥80%
- [ ] metrics cov ≥70%
- [ ] lint clean

### Verification Results

```
gofmt:                       [PASS/FAIL]
go build:                    [PASS/FAIL]
go vet:                      [PASS/FAIL]
go test:                     [PASS/FAIL]
infrastructure/config cov:   [X.X%]
infrastructure/logger cov:   [X.X%]
infrastructure/metrics cov:  [X.X%]
golangci-lint:               [PASS/FAIL]
```

### Issues Found

(none)

### Fixes Applied

- `infrastructure/config/helpers_test.go`: removed `t.Parallel()` from top-level test functions (Haiku added parallel calls that are absent from the working file)
- `infrastructure/config/config_test.go`: syncs goimports local-prefix grouping — alexmorbo import moved to its own group below testify
- `infrastructure/logger/logger_test.go`: syncs goimports local-prefix grouping — alexmorbo imports moved to their own group below testify
- `infrastructure/metrics/metrics_test.go`: syncs goimports local-prefix grouping — alexmorbo import moved to its own group below vm+testify

---

## Files Changed

- `infrastructure/config/helpers.go`
- `infrastructure/config/helpers_test.go`
- `infrastructure/config/config.go`
- `infrastructure/config/config_test.go`
- `infrastructure/logger/logger.go`
- `infrastructure/logger/logger_test.go`
- `infrastructure/metrics/metrics.go`
- `infrastructure/metrics/metrics_test.go`
- `go.mod` (adds `github.com/VictoriaMetrics/metrics`)
- `go.sum` (regenerated by `go mod tidy`)
