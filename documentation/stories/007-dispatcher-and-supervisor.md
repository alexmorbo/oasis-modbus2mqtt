---
title: "Feature: CommandDispatcher and ConnectionSupervisor"
status: ready
priority: high
complexity: 5
planning_model: opus-4-5
implementation_model: haiku-4
created: 2026-04-25
updated: 2026-04-25
depends_on: 006-infrastructure-modbus-client
risk_areas: [reconnect-backoff-correctness, dispatcher-signal-coordination]
issues_found: (none)
---

## Context

Story 007 — две application-level службы поверх Modbus client (story 006):

1. **CommandDispatcher** — типизированная обёртка над `port.ModbusClient`, отвечает за **подсчёт consecutive failures** и сигнал ConnectionSupervisor при достижении threshold (3 подряд). Атомарность отдельной операции уже обеспечена клиентом (mutex + guard interval), dispatcher добавляет cross-operation state.

2. **ConnectionSupervisor** — отдельная goroutine с **backoff loop** для (re)connect. Получает trigger от dispatcher, делает `client.ForceClose()`, потом реконнектит с экспоненциальным backoff (1с..60с, factor=2, jitter ±20%). Запускает initial connect при старте.

3. **Backoff** — утилита для расчёта следующей задержки (детерминистична с seedabletest jitter).

**Архитектурное решение про `Connect`/`ForceClose`:**
Story 004 определила `port.ModbusClient` без `Connect`/`ForceClose` — это infrastructure concern. Но dispatcher и supervisor нуждаются в этих операциях. Решение: добавить новый port `ModbusConnection`, который embed'ит `ModbusClient` + добавляет lifecycle methods. `*modbus.Client` (story 006) автоматически удовлетворяет оба интерфейса (он уже реализует Connect/ForceClose). Никаких изменений в story 004/006 файлах не требуется — добавляется новый файл `application/port/modbus_connection.go`.

**Почему dispatcher НЕ использует job channel pattern**, который упомянут в плане:
Plan section 4 показывает worker + jobs channel, но это избыточно когда модбас клиент уже сериализует через mutex. Прямые synchronous вызовы дают тот же эффект, проще тестируются, не требуют DTO-job типы. Failure counting и supervisor trigger делаем на уровне dispatcher методов. Если позже понадобится buffering или TTL drop — добавим, не заранее.

Reference: `/Users/alexmorbo/Home/homelab/.thoughts/oasis-syberia-modbus-bridge/04-mqtt-bridge-plan.md` секция `## 5. Watchdog/reconnect псевдокод`.

## User Story

**As a** разработчик oasis-modbus2mqtt,
**I want to** иметь self-healing connection layer с автоматическим reconnect и failure-driven supervisor signaling,
**So that** poller (story 008) и apply-command (story 009) могли просто вызывать `dispatcher.ReadInput(...)` без знания про backoff, transient errors, или reconnect logic.

## Acceptance Criteria

### Port

- [ ] `application/port/modbus_connection.go`:
  ```go
  type ModbusConnection interface {
      ModbusClient
      Connect(ctx context.Context) error
      ForceClose() error
  }
  ```
  - Godoc: explains it extends ModbusClient with lifecycle methods used by ConnectionSupervisor.
  - Compile-time check note (in tests) that `*infrastructure/modbus.Client` satisfies it — НЕ в этом story (cross-package compile-time check; story 015 main.go всё равно соберёт).

### Backoff

- [ ] `application/service/backoff.go`:
  - `type Backoff struct { Min, Max time.Duration; Factor, Jitter float64; rng *rand/v2.Rand; current time.Duration }`. Жидер 0..1 (например 0.2 = ±20%).
  - `func NewBackoff(cfg config.ReconnectConfig) *Backoff` — использует `rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))` из `math/rand/v2`.
  - `func NewBackoffWithRand(cfg config.ReconnectConfig, rng *rand/v2.Rand) *Backoff` — для тестов.
  - `(b *Backoff) Next() time.Duration` — возвращает текущее значение, потом обновляет: `current = min(current * factor, max)`. Применяет jitter в момент return: `actual = current ± jitter*current`.
  - `(b *Backoff) Reset()` — `current = Min`.
  - **Rule**: первый `Next()` после `Reset()` (или после конструирования) возвращает `Min` (с jitter). Второй — `Min*Factor` (с jitter, capped Max). И так далее.
  - Нет state mutation race-conditions: НЕ thread-safe, caller (supervisor) — single goroutine. Документируется в godoc.

### CommandDispatcher

- [ ] `application/service/command_dispatcher.go`:
  - `type FailureNotifier interface { Trigger() }` — sup получает trigger без circular dep.
  - `type CommandDispatcher struct { client port.ModbusClient; notifier FailureNotifier; threshold int; logger *slog.Logger; mu sync.Mutex; consecutiveErrs int }`.
  - `func NewCommandDispatcher(client port.ModbusClient, notifier FailureNotifier, threshold int, logger *slog.Logger) *CommandDispatcher` — threshold default 3 (validate >0).
  - **Public methods** — proxy к client с failure tracking:
    - `ReadInput(ctx, start, count uint16) ([]uint16, error)` — `client.ReadInput(...)`. Success → `recordSuccess()`. Error → `recordFailure(err)`.
    - `ReadHolding(ctx, start, count uint16) ([]uint16, error)`.
    - `WriteHolding(ctx, addr, value uint16) error`.
    - `ModifyHolding(ctx, addr uint16, modify func(uint16) uint16) error`.
    - `Connected() bool` — proxy `client.Connected()`.
  - **Internal**:
    - `recordSuccess()` — `mu.Lock`, `consecutiveErrs = 0`, `mu.Unlock`. Без логирования (slog.Debug на success — на уровне клиента).
    - `recordFailure(err error)`:
      - `mu.Lock(); consecutiveErrs++; current := consecutiveErrs; mu.Unlock()`.
      - log WARN с error + current count.
      - if `current >= threshold`:
        - log WARN "consecutive failures threshold reached, signaling supervisor"
        - `notifier.Trigger()` (non-blocking — Trigger spec'ed below).
        - **NB**: НЕ ресетим counter здесь — supervisor триггерится повторно при каждой следующей ошибке выше threshold. Counter сбрасывается ТОЛЬКО на success. Это приемлемо: trigger non-blocking + idempotent.
  - **Не** делает `client.ForceClose()` сам — supervisor responsibility.
  - **Не** имеет channel/goroutine — это просто метод-обёртки. `mu` защищает только `consecutiveErrs`.

### ConnectionSupervisor

- [ ] `application/service/connection_supervisor.go`:
  - `type ConnectionSupervisor struct { conn port.ModbusConnection; backoff *Backoff; logger *slog.Logger; trigger chan struct{}; cfg config.ReconnectConfig }`.
    - `trigger chan struct{}` buffered cap=1 — non-blocking send (sender drops if full).
  - `func NewConnectionSupervisor(conn port.ModbusConnection, cfg config.ReconnectConfig, logger *slog.Logger) *ConnectionSupervisor` — конструктор без I/O.
  - **Public methods**:
    - `Start(ctx context.Context) error` — синхронно делает initial `Connect`, потом запускает goroutine watching trigger. Возвращает error если initial connect failed после full backoff exhaustion (например ctx cancelled). На success returns nil; goroutine продолжает работать до ctx cancellation.
    - `Trigger()` — non-blocking send в `trigger` channel: `select { case s.trigger <- struct{}{}: default: }`. Удовлетворяет `FailureNotifier`.
  - **Internal goroutine** (`run(ctx)`):
    ```
    for {
        select {
        case <-ctx.Done(): return
        case <-s.trigger:
            // dispatcher signaled failure threshold
            _ = s.conn.ForceClose()
            s.reconnectLoop(ctx)
        }
    }
    ```
  - `reconnectLoop(ctx)`:
    ```
    for {
        delay := s.backoff.Next()
        log INFO "reconnect attempt scheduled"
        select {
        case <-ctx.Done(): return
        case <-time.After(delay):
        }
        if err := s.conn.Connect(ctx); err != nil {
            metrics.ModbusReconnectsTotal already incremented in client.Connect or not — story 006 increments на КАЖДЫЙ успешный Connect. На fail здесь ставим metrics.ModbusErrorsTotal("reconnect_failed").Inc().
            log WARN "reconnect failed"
            continue
        }
        s.backoff.Reset()
        log INFO "modbus reconnected"
        return
    }
    ```
  - **На initial Start**: тоже использует `reconnectLoop` для повторных попыток. Если ctx cancelled до успеха → возвращает error.

### Tests

- [ ] `application/service/backoff_test.go`:
  - `TestBackoff_Sequence` — seeded rng, конструируем cfg{Min: 100ms, Max: 1s, Factor: 2.0, Jitter: 0}, проверяем `Next() = 100ms, 200ms, 400ms, 800ms, 1s, 1s` (capped).
  - `TestBackoff_Reset` — после `Reset()` следующий `Next()` снова Min.
  - `TestBackoff_Jitter` — Jitter=0.5, seeded rng, проверяем что результат в диапазоне `[Min*0.5, Min*1.5]` (50%..150%).
  - `TestBackoff_ZeroJitter` — Jitter=0 → точные значения без jitter.
  - `t.Parallel()` — backoff не shared state между тестами.
- [ ] `application/service/command_dispatcher_test.go`:
  - Mock `port.ModbusClient` (ручной struct mock с counters/closures, без mocking framework).
  - Mock `FailureNotifier` — counts triggers.
  - Tests:
    - `TestReadInput_Success_ResetsCounter` — initial `consecutiveErrs=2`, ReadInput success → counter=0.
    - `TestReadInput_Error_IncrementsCounter` — ReadInput error → counter=1.
    - `TestReadInput_ThresholdReached_TriggersNotifier` — threshold=3, 3 errors подряд → notifier triggered хотя бы 1 раз.
    - `TestReadInput_AboveThreshold_TriggersAgain` — 5 errors, notifier triggered 3 times (на 3-й, 4-й, 5-й).
    - Тесты для ReadHolding, WriteHolding, ModifyHolding (по одному success/error case для каждого).
    - `TestConnected_Proxies` — client.Connected → dispatcher.Connected.
    - `TestThresholdValidation_RejectsZero` — `NewCommandDispatcher(... 0 ...)` panics or returns nil. Решение: panic в конструкторе с `panic("threshold must be > 0")`.
- [ ] `application/service/connection_supervisor_test.go`:
  - Mock `port.ModbusConnection`:
    - Connect — controlled via `connectFunc func(ctx) error`.
    - ForceClose — counts calls, returns nil.
    - Connected — returns based on flag.
    - ReadInput/ReadHolding/WriteHolding/ModifyHolding — return ErrNotImplemented (won't be called).
  - Tests:
    - `TestStart_InitialConnect_Success` — Connect succeeds first try → Start returns nil.
    - `TestStart_InitialConnect_Retries` — first 2 Connect attempts fail, 3rd succeeds → Start returns nil after 3 attempts. Use very small backoff (1ms..10ms) for fast test.
    - `TestStart_InitialConnect_CancelledContext` — Connect always fails. Cancel ctx during retry → Start returns ctx.Err().
    - `TestTrigger_TriggersForceCloseAndReconnect` — Start succeeds, then Trigger() → goroutine sees trigger, calls ForceClose, then Connect (succeeds) → ForceClose call count == 1, Connect call count == 2 (initial + after trigger). Use sync.WaitGroup or polling for ForceClose call.
    - `TestTrigger_NonBlocking_WhenChannelFull` — Trigger() called twice in rapid succession before goroutine consumes; second call doesn't block (test by goroutine count or by checking it returns within 10ms).
    - `TestStop_via_Context_Cancelled` — Start, then cancel ctx. Goroutine exits within reasonable time (test via channel close or done flag).

### Quality

- [ ] Coverage `application/service` ≥ 90% (plain logic, no I/O — easily achievable).
- [ ] `go test -race ./application/service/...` passes.
- [ ] `golangci-lint run ./...` PASS.
- [ ] `gofmt -l application/` пусто.

## Constraints

- **Зависимости**: stdlib + `application/port` + `infrastructure/config`/`metrics` + testify (already in go.mod). НЕ импортировать `infrastructure/modbus` напрямую (только через port interfaces).
- НЕ использовать `init()` в этих пакетах.
- НЕ использовать generics.
- `consecutiveErrs` под mutex; `Trigger()` non-blocking; goroutine read trigger через `<-`.
- `ConnectionSupervisor.Start()` синхронен в смысле initial connect — возвращает только когда либо connected либо ctx cancelled.
- backoff jitter — симметричный (`actual = base * (1 + (rand_in_-1_+1) * jitter)`) → `actual ∈ [base*(1-jitter), base*(1+jitter)]`.
- backoff `current` clamp вверх к Max ДО применения jitter (jitter может слегка вывести за Max — это OK, документируется в godoc).
- НЕТ retry внутри dispatcher — только counter и trigger.
- godoc one-liner на каждом exported.

### Goimports / lint guards (для Planning Agent)

- 3 группы импортов: stdlib | 3rd-party | `github.com/alexmorbo/oasis-modbus2mqtt/...`.
- `t.Parallel()` свободно (никаких t.Setenv в этой story).
- `t.Parallel()` в supervisor тестах — каждый тест строит свой mock conn, нет shared state.
- gosec G115 — не должно быть narrowing'ов в этой story (уже всё типизировано).
- НЕ использовать `time.Tick` (leaks goroutine при cancel) — только `time.NewTimer` с defer Stop().
- Mock structs должны быть встроены в test файлы (не отдельные файлы).

---

## Technical Specification

### Analysis

- **Circular dep break via `FailureNotifier`**: dispatcher imports a tiny `Trigger()`-only interface defined in `command_dispatcher.go`. `ConnectionSupervisor` already exposes `Trigger()`, so it satisfies the interface by duck-typing — neither service imports the other.
- **Direct synchronous proxies, no job channel**: dispatcher methods call `client.X(...)` and update `consecutiveErrs` under `sync.Mutex`. Atomicity per op is guaranteed by the underlying client (story 006); dispatcher only adds cross-op state. Avoids worker/goroutine/DTO complexity for zero throughput gain since the client is mutex-serialized anyway.
- **Single-goroutine backoff ownership**: `Backoff` is mutated only inside `ConnectionSupervisor.run`/`reconnectLoop`/`initialConnect`, all of which run sequentially — initial connect blocks `Start`, then the run-goroutine takes over. So `Backoff` is documented non-thread-safe and uses a private `*rand.Rand`.
- **Initial connect reuses `reconnectLoop`**: the first connect uses the same retry+ctx-cancel loop as a post-trigger reconnect. On ctx cancellation we return `ctx.Err()` from `Start` so wiring (story 015) can fail fast at startup.
- **Trigger non-blocking via buffered cap-1 channel**: dispatcher may signal more often than supervisor consumes; subsequent triggers are silently dropped because the supervisor's reconnectLoop already covers them — the trigger is "edge-triggered, idempotent under load".
- **`done chan struct{}` for test sync**: closed by the run-goroutine on exit; tests poll `<-Done()` to verify shutdown without sleeping. Production `main` may also use it for graceful shutdown coordination.
- **Reset on success only**: `consecutiveErrs` resets on success and is never reset by `recordOutcome` after a trigger, so dispatcher keeps signalling supervisor on each subsequent failure above threshold; the cap-1 trigger channel turns the burst into at most one extra wake-up — acceptable per AC.

### Implementation Order

1. `application/port/modbus_connection.go` (no logic)
2. `application/service/backoff.go` + test (pure logic)
3. `application/service/command_dispatcher.go` + test (depends on port)
4. `application/service/connection_supervisor.go` + test (depends on port + backoff)

---

### 1. Connection port

#### File: `application/port/modbus_connection.go`

```go
package port

import "context"

// ModbusConnection extends ModbusClient with the connection-lifecycle
// operations that ConnectionSupervisor (story 007) needs to (re)establish
// the underlying TCP transport. The infrastructure client (story 006)
// implements both interfaces; application code that does not perform
// lifecycle management depends on ModbusClient only.
type ModbusConnection interface {
	ModbusClient

	// Connect opens (or re-opens) the underlying transport. It must be
	// idempotent: calling Connect on an already-connected client returns
	// nil without side effects.
	Connect(ctx context.Context) error

	// ForceClose tears down the underlying transport unconditionally. It
	// must be idempotent and safe to call from any goroutine.
	ForceClose() error
}
```

---

### 2. Backoff

#### File: `application/service/backoff.go`

```go
package service

import (
	"math/rand/v2"
	"time"

	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/config"
)

// Backoff produces an exponentially growing sequence of delays, capped at a
// maximum and optionally perturbed by symmetric jitter.
//
// Backoff is NOT thread-safe; it is designed for single-goroutine use by
// ConnectionSupervisor. Each call to Next advances internal state.
type Backoff struct {
	min     time.Duration
	max     time.Duration
	factor  float64
	jitter  float64
	rng     *rand.Rand
	current time.Duration
}

// NewBackoff constructs a Backoff seeded from the current wall-clock time.
// The first Next call returns approximately cfg.MinDelay (exact when jitter
// is zero).
func NewBackoff(cfg config.ReconnectConfig) *Backoff {
	seed := uint64(time.Now().UnixNano())                                                //nolint:gosec
	return NewBackoffWithRand(cfg, rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))) //nolint:gosec
}

// NewBackoffWithRand constructs a Backoff using the supplied random source.
// It exists so tests can pin jitter to a deterministic seed.
func NewBackoffWithRand(cfg config.ReconnectConfig, rng *rand.Rand) *Backoff {
	return &Backoff{
		min:     cfg.MinDelay,
		max:     cfg.MaxDelay,
		factor:  cfg.Factor,
		jitter:  cfg.JitterPct,
		rng:     rng,
		current: cfg.MinDelay,
	}
}

// Next returns the next delay and advances internal state. The returned
// value is the current base delay perturbed by symmetric jitter
// (actual = base * (1 + (rand_in_-1_+1) * jitter)). The base delay is then
// multiplied by factor and clamped at max for the subsequent call.
func (b *Backoff) Next() time.Duration {
	actual := b.current
	if b.jitter > 0 {
		mult := 1 + (b.rng.Float64()*2-1)*b.jitter
		actual = time.Duration(float64(b.current) * mult)
	}
	if actual < 0 {
		actual = 0
	}

	next := time.Duration(float64(b.current) * b.factor)
	if next > b.max {
		next = b.max
	}
	b.current = next

	return actual
}

// Reset returns the Backoff to its initial state so the next Next call
// returns the configured minimum delay (with jitter applied).
func (b *Backoff) Reset() {
	b.current = b.min
}
```

#### File: `application/service/backoff_test.go`

```go
package service_test

import (
	"math/rand/v2"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/service"
	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/config"
)

func newCfg(min, max time.Duration, jitter float64) config.ReconnectConfig {
	return config.ReconnectConfig{
		MinDelay:  min,
		MaxDelay:  max,
		Factor:    2.0,
		JitterPct: jitter,
	}
}

func TestBackoff_Sequence(t *testing.T) {
	t.Parallel()

	b := service.NewBackoffWithRand(
		newCfg(100*time.Millisecond, 1*time.Second, 0),
		rand.New(rand.NewPCG(1, 1)), //nolint:gosec
	)

	want := []time.Duration{
		100 * time.Millisecond,
		200 * time.Millisecond,
		400 * time.Millisecond,
		800 * time.Millisecond,
		1 * time.Second,
		1 * time.Second,
	}
	for i, exp := range want {
		got := b.Next()
		assert.Equalf(t, exp, got, "Next call #%d", i+1)
	}
}

func TestBackoff_ZeroJitter(t *testing.T) {
	t.Parallel()

	b := service.NewBackoffWithRand(
		newCfg(50*time.Millisecond, 200*time.Millisecond, 0),
		rand.New(rand.NewPCG(99, 99)), //nolint:gosec
	)

	require.Equal(t, 50*time.Millisecond, b.Next())
	require.Equal(t, 100*time.Millisecond, b.Next())
	require.Equal(t, 200*time.Millisecond, b.Next())
	require.Equal(t, 200*time.Millisecond, b.Next())
}

func TestBackoff_Jitter(t *testing.T) {
	t.Parallel()

	b := service.NewBackoffWithRand(
		newCfg(100*time.Millisecond, 1*time.Second, 0.5),
		rand.New(rand.NewPCG(42, 42)), //nolint:gosec
	)

	got := b.Next()
	assert.GreaterOrEqual(t, got, 50*time.Millisecond, "first delay >= min*0.5")
	assert.LessOrEqual(t, got, 150*time.Millisecond, "first delay <= min*1.5")
}

func TestBackoff_JitterStaysPositive(t *testing.T) {
	t.Parallel()

	b := service.NewBackoffWithRand(
		newCfg(10*time.Millisecond, 100*time.Millisecond, 1.5),
		rand.New(rand.NewPCG(7, 7)), //nolint:gosec
	)

	for i := 0; i < 20; i++ {
		got := b.Next()
		assert.GreaterOrEqualf(t, got, time.Duration(0), "iteration %d", i)
	}
}

func TestBackoff_Reset(t *testing.T) {
	t.Parallel()

	b := service.NewBackoffWithRand(
		newCfg(100*time.Millisecond, 1*time.Second, 0),
		rand.New(rand.NewPCG(1, 1)), //nolint:gosec
	)

	_ = b.Next()
	_ = b.Next()
	_ = b.Next()
	b.Reset()
	assert.Equal(t, 100*time.Millisecond, b.Next())
	assert.Equal(t, 200*time.Millisecond, b.Next())
}

func TestNewBackoff_DefaultsToWallClockSeed(t *testing.T) {
	t.Parallel()

	cfg := newCfg(20*time.Millisecond, 80*time.Millisecond, 0)
	b := service.NewBackoff(cfg)

	require.Equal(t, 20*time.Millisecond, b.Next())
	require.Equal(t, 40*time.Millisecond, b.Next())
	require.Equal(t, 80*time.Millisecond, b.Next())
	b.Reset()
	require.Equal(t, 20*time.Millisecond, b.Next())
}
```

---

### 3. CommandDispatcher

#### File: `application/service/command_dispatcher.go`

```go
package service

import (
	"context"
	"log/slog"
	"sync"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/port"
)

// FailureNotifier is implemented by ConnectionSupervisor and consumed by
// CommandDispatcher. Defining it here breaks the would-be circular
// dependency between the two services: dispatcher signals failures via this
// interface; supervisor satisfies it by exposing a non-blocking Trigger.
type FailureNotifier interface {
	// Trigger asks the supervisor to perform a reconnect. Implementations
	// must be non-blocking and idempotent under load.
	Trigger()
}

// CommandDispatcher is a typed wrapper around port.ModbusClient that tracks
// consecutive failures across operations and signals a FailureNotifier once
// a threshold is reached. Per-operation atomicity is provided by the
// underlying client; the dispatcher only adds cross-op state.
type CommandDispatcher struct {
	client    port.ModbusClient
	notifier  FailureNotifier
	threshold int
	logger    *slog.Logger

	mu              sync.Mutex
	consecutiveErrs int
}

// NewCommandDispatcher constructs a CommandDispatcher. It panics if client
// or notifier is nil, or if threshold is not strictly positive — these are
// programming errors that must surface during wiring (main.go), not at
// runtime. A nil logger falls back to slog.Default.
func NewCommandDispatcher(
	client port.ModbusClient,
	notifier FailureNotifier,
	threshold int,
	logger *slog.Logger,
) *CommandDispatcher {
	if client == nil {
		panic("command dispatcher: client must not be nil")
	}
	if notifier == nil {
		panic("command dispatcher: notifier must not be nil")
	}
	if threshold <= 0 {
		panic("command dispatcher: threshold must be > 0")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &CommandDispatcher{
		client:    client,
		notifier:  notifier,
		threshold: threshold,
		logger:    logger,
	}
}

// ReadInput proxies port.ModbusClient.ReadInput and records the outcome.
func (d *CommandDispatcher) ReadInput(ctx context.Context, start, count uint16) ([]uint16, error) {
	result, err := d.client.ReadInput(ctx, start, count)
	d.recordOutcome(err)
	return result, err
}

// ReadHolding proxies port.ModbusClient.ReadHolding and records the outcome.
func (d *CommandDispatcher) ReadHolding(ctx context.Context, start, count uint16) ([]uint16, error) {
	result, err := d.client.ReadHolding(ctx, start, count)
	d.recordOutcome(err)
	return result, err
}

// WriteHolding proxies port.ModbusClient.WriteHolding and records the outcome.
func (d *CommandDispatcher) WriteHolding(ctx context.Context, addr, value uint16) error {
	err := d.client.WriteHolding(ctx, addr, value)
	d.recordOutcome(err)
	return err
}

// ModifyHolding proxies port.ModbusClient.ModifyHolding and records the outcome.
func (d *CommandDispatcher) ModifyHolding(ctx context.Context, addr uint16, modify func(uint16) uint16) error {
	err := d.client.ModifyHolding(ctx, addr, modify)
	d.recordOutcome(err)
	return err
}

// Connected proxies port.ModbusClient.Connected.
func (d *CommandDispatcher) Connected() bool {
	return d.client.Connected()
}

// recordOutcome updates the consecutive-error counter and triggers the
// notifier when the threshold is reached. Counter is reset only on success;
// the counter is intentionally NOT reset after triggering, so subsequent
// failures keep signalling — the notifier handles idempotency.
func (d *CommandDispatcher) recordOutcome(err error) {
	d.mu.Lock()
	if err == nil {
		d.consecutiveErrs = 0
		d.mu.Unlock()
		return
	}
	d.consecutiveErrs++
	current := d.consecutiveErrs
	d.mu.Unlock()

	d.logger.Warn("modbus op failed",
		slog.Int("consecutive", current),
		slog.Any("error", err),
	)
	if current >= d.threshold {
		d.logger.Warn("consecutive failure threshold reached, signaling supervisor",
			slog.Int("threshold", d.threshold),
			slog.Int("consecutive", current),
		)
		d.notifier.Trigger()
	}
}
```

#### File: `application/service/command_dispatcher_test.go`

```go
package service_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/port"
	"github.com/alexmorbo/oasis-modbus2mqtt/application/service"
)

// fakeClient is a hand-written mock of port.ModbusClient. Each *Fn field
// overrides the default no-op behaviour for that method.
type fakeClient struct {
	mu sync.Mutex

	readInputFn     func(ctx context.Context, start, count uint16) ([]uint16, error)
	readHoldingFn   func(ctx context.Context, start, count uint16) ([]uint16, error)
	writeHoldingFn  func(ctx context.Context, addr, value uint16) error
	modifyHoldingFn func(ctx context.Context, addr uint16, modify func(uint16) uint16) error
	connectedFlag   bool

	readInputCalls     int
	readHoldingCalls   int
	writeHoldingCalls  int
	modifyHoldingCalls int
}

func (f *fakeClient) ReadInput(ctx context.Context, start, count uint16) ([]uint16, error) {
	f.mu.Lock()
	f.readInputCalls++
	fn := f.readInputFn
	f.mu.Unlock()
	if fn == nil {
		return nil, nil
	}
	return fn(ctx, start, count)
}

func (f *fakeClient) ReadHolding(ctx context.Context, start, count uint16) ([]uint16, error) {
	f.mu.Lock()
	f.readHoldingCalls++
	fn := f.readHoldingFn
	f.mu.Unlock()
	if fn == nil {
		return nil, nil
	}
	return fn(ctx, start, count)
}

func (f *fakeClient) WriteHolding(ctx context.Context, addr, value uint16) error {
	f.mu.Lock()
	f.writeHoldingCalls++
	fn := f.writeHoldingFn
	f.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(ctx, addr, value)
}

func (f *fakeClient) ModifyHolding(ctx context.Context, addr uint16, modify func(uint16) uint16) error {
	f.mu.Lock()
	f.modifyHoldingCalls++
	fn := f.modifyHoldingFn
	f.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(ctx, addr, modify)
}

func (f *fakeClient) Connected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connectedFlag
}

// fakeNotifier counts Trigger calls.
type fakeNotifier struct {
	mu    sync.Mutex
	count int
}

func (n *fakeNotifier) Trigger() {
	n.mu.Lock()
	n.count++
	n.mu.Unlock()
}

func (n *fakeNotifier) Count() int {
	n.mu.Lock()
	defer n.mu.Unlock()
	return n.count
}

// compile-time assertion that fakeClient satisfies the port.
var _ port.ModbusClient = (*fakeClient)(nil)

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func newDispatcher(client *fakeClient, notifier *fakeNotifier) *service.CommandDispatcher {
	return service.NewCommandDispatcher(client, notifier, 3, discardLogger())
}

func TestNewCommandDispatcher_PanicsOnNilClient(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() {
		service.NewCommandDispatcher(nil, &fakeNotifier{}, 3, discardLogger())
	})
}

func TestNewCommandDispatcher_PanicsOnNilNotifier(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() {
		service.NewCommandDispatcher(&fakeClient{}, nil, 3, discardLogger())
	})
}

func TestNewCommandDispatcher_PanicsOnZeroThreshold(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() {
		service.NewCommandDispatcher(&fakeClient{}, &fakeNotifier{}, 0, discardLogger())
	})
}

func TestNewCommandDispatcher_PanicsOnNegativeThreshold(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() {
		service.NewCommandDispatcher(&fakeClient{}, &fakeNotifier{}, -1, discardLogger())
	})
}

func TestNewCommandDispatcher_NilLoggerFallsBackToDefault(t *testing.T) {
	t.Parallel()

	client := &fakeClient{
		readInputFn: func(_ context.Context, _, _ uint16) ([]uint16, error) {
			return []uint16{1}, nil
		},
	}
	notifier := &fakeNotifier{}

	d := service.NewCommandDispatcher(client, notifier, 3, nil)
	require.NotNil(t, d)

	got, err := d.ReadInput(context.Background(), 0, 1)
	require.NoError(t, err)
	require.Equal(t, []uint16{1}, got)
}

func TestReadInput_Success(t *testing.T) {
	t.Parallel()

	client := &fakeClient{
		readInputFn: func(_ context.Context, start, count uint16) ([]uint16, error) {
			assert.Equal(t, uint16(10), start)
			assert.Equal(t, uint16(2), count)
			return []uint16{0xAA, 0xBB}, nil
		},
	}
	notifier := &fakeNotifier{}
	d := newDispatcher(client, notifier)

	got, err := d.ReadInput(context.Background(), 10, 2)
	require.NoError(t, err)
	assert.Equal(t, []uint16{0xAA, 0xBB}, got)
	assert.Equal(t, 0, notifier.Count())
}

func TestReadInput_Failure(t *testing.T) {
	t.Parallel()

	want := errors.New("boom")
	client := &fakeClient{
		readInputFn: func(_ context.Context, _, _ uint16) ([]uint16, error) {
			return nil, want
		},
	}
	notifier := &fakeNotifier{}
	d := newDispatcher(client, notifier)

	_, err := d.ReadInput(context.Background(), 0, 1)
	require.ErrorIs(t, err, want)
	assert.Equal(t, 0, notifier.Count())
}

func TestReadHolding_Success(t *testing.T) {
	t.Parallel()

	client := &fakeClient{
		readHoldingFn: func(_ context.Context, _, _ uint16) ([]uint16, error) {
			return []uint16{1, 2, 3}, nil
		},
	}
	notifier := &fakeNotifier{}
	d := newDispatcher(client, notifier)

	got, err := d.ReadHolding(context.Background(), 0, 3)
	require.NoError(t, err)
	assert.Equal(t, []uint16{1, 2, 3}, got)
}

func TestReadHolding_Failure(t *testing.T) {
	t.Parallel()

	want := errors.New("rh-boom")
	client := &fakeClient{
		readHoldingFn: func(_ context.Context, _, _ uint16) ([]uint16, error) {
			return nil, want
		},
	}
	notifier := &fakeNotifier{}
	d := newDispatcher(client, notifier)

	_, err := d.ReadHolding(context.Background(), 0, 1)
	require.ErrorIs(t, err, want)
}

func TestWriteHolding_Success(t *testing.T) {
	t.Parallel()

	client := &fakeClient{
		writeHoldingFn: func(_ context.Context, addr, value uint16) error {
			assert.Equal(t, uint16(7), addr)
			assert.Equal(t, uint16(0xCAFE), value)
			return nil
		},
	}
	notifier := &fakeNotifier{}
	d := newDispatcher(client, notifier)

	require.NoError(t, d.WriteHolding(context.Background(), 7, 0xCAFE))
}

func TestWriteHolding_Failure(t *testing.T) {
	t.Parallel()

	want := errors.New("wh-boom")
	client := &fakeClient{
		writeHoldingFn: func(_ context.Context, _, _ uint16) error {
			return want
		},
	}
	notifier := &fakeNotifier{}
	d := newDispatcher(client, notifier)

	require.ErrorIs(t, d.WriteHolding(context.Background(), 0, 0), want)
}

func TestModifyHolding_Success(t *testing.T) {
	t.Parallel()

	client := &fakeClient{
		modifyHoldingFn: func(_ context.Context, addr uint16, modify func(uint16) uint16) error {
			assert.Equal(t, uint16(3), addr)
			assert.Equal(t, uint16(0x10), modify(0x0F))
			return nil
		},
	}
	notifier := &fakeNotifier{}
	d := newDispatcher(client, notifier)

	err := d.ModifyHolding(context.Background(), 3, func(v uint16) uint16 { return v + 1 })
	require.NoError(t, err)
}

func TestModifyHolding_Failure(t *testing.T) {
	t.Parallel()

	want := errors.New("mh-boom")
	client := &fakeClient{
		modifyHoldingFn: func(_ context.Context, _ uint16, _ func(uint16) uint16) error {
			return want
		},
	}
	notifier := &fakeNotifier{}
	d := newDispatcher(client, notifier)

	err := d.ModifyHolding(context.Background(), 0, func(v uint16) uint16 { return v })
	require.ErrorIs(t, err, want)
}

func TestSuccess_ResetsCounter(t *testing.T) {
	t.Parallel()

	var fail bool
	client := &fakeClient{
		readInputFn: func(_ context.Context, _, _ uint16) ([]uint16, error) {
			if fail {
				return nil, errors.New("x")
			}
			return []uint16{1}, nil
		},
	}
	notifier := &fakeNotifier{}
	d := newDispatcher(client, notifier)

	fail = true
	_, _ = d.ReadInput(context.Background(), 0, 1)
	_, _ = d.ReadInput(context.Background(), 0, 1)
	require.Equal(t, 0, notifier.Count())

	fail = false
	_, err := d.ReadInput(context.Background(), 0, 1)
	require.NoError(t, err)

	fail = true
	_, _ = d.ReadInput(context.Background(), 0, 1)
	_, _ = d.ReadInput(context.Background(), 0, 1)
	assert.Equal(t, 0, notifier.Count(), "counter must have reset, threshold not yet reached")
}

func TestThresholdReached_TriggersNotifier(t *testing.T) {
	t.Parallel()

	client := &fakeClient{
		readInputFn: func(_ context.Context, _, _ uint16) ([]uint16, error) {
			return nil, errors.New("x")
		},
	}
	notifier := &fakeNotifier{}
	d := newDispatcher(client, notifier)

	for i := 0; i < 3; i++ {
		_, _ = d.ReadInput(context.Background(), 0, 1)
	}
	assert.Equal(t, 1, notifier.Count())
}

func TestAboveThreshold_TriggersAgain(t *testing.T) {
	t.Parallel()

	client := &fakeClient{
		readInputFn: func(_ context.Context, _, _ uint16) ([]uint16, error) {
			return nil, errors.New("x")
		},
	}
	notifier := &fakeNotifier{}
	d := newDispatcher(client, notifier)

	for i := 0; i < 5; i++ {
		_, _ = d.ReadInput(context.Background(), 0, 1)
	}
	// 3rd, 4th and 5th errors all trigger.
	assert.Equal(t, 3, notifier.Count())
}

func TestThresholdMixedOps_AccumulatesAcrossMethods(t *testing.T) {
	t.Parallel()

	boom := errors.New("x")
	client := &fakeClient{
		readInputFn:    func(_ context.Context, _, _ uint16) ([]uint16, error) { return nil, boom },
		readHoldingFn:  func(_ context.Context, _, _ uint16) ([]uint16, error) { return nil, boom },
		writeHoldingFn: func(_ context.Context, _, _ uint16) error { return boom },
	}
	notifier := &fakeNotifier{}
	d := newDispatcher(client, notifier)

	_, _ = d.ReadInput(context.Background(), 0, 1)
	_, _ = d.ReadHolding(context.Background(), 0, 1)
	_ = d.WriteHolding(context.Background(), 0, 0)

	assert.Equal(t, 1, notifier.Count())
}

func TestConnected_ProxiesTrue(t *testing.T) {
	t.Parallel()

	client := &fakeClient{connectedFlag: true}
	d := newDispatcher(client, &fakeNotifier{})
	assert.True(t, d.Connected())
}

func TestConnected_ProxiesFalse(t *testing.T) {
	t.Parallel()

	client := &fakeClient{connectedFlag: false}
	d := newDispatcher(client, &fakeNotifier{})
	assert.False(t, d.Connected())
}
```

---

### 4. ConnectionSupervisor

#### File: `application/service/connection_supervisor.go`

```go
package service

import (
	"context"
	"log/slog"
	"time"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/port"
	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/config"
	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/metrics"
)

// ConnectionSupervisor owns the lifecycle of a port.ModbusConnection. It
// performs the initial connect synchronously from Start, then runs a single
// goroutine that waits for either context cancellation or a Trigger signal
// (typically from CommandDispatcher), tearing down and reconnecting with
// exponential backoff on each trigger.
//
// ConnectionSupervisor satisfies the FailureNotifier interface declared in
// command_dispatcher.go via its Trigger method, by duck typing — neither
// service imports the other.
type ConnectionSupervisor struct {
	conn    port.ModbusConnection
	cfg     config.ReconnectConfig
	logger  *slog.Logger
	backoff *Backoff

	trigger chan struct{}
	done    chan struct{}
}

// NewConnectionSupervisor constructs a ConnectionSupervisor without
// performing I/O. A nil logger falls back to slog.Default.
func NewConnectionSupervisor(
	conn port.ModbusConnection,
	cfg config.ReconnectConfig,
	logger *slog.Logger,
) *ConnectionSupervisor {
	if logger == nil {
		logger = slog.Default()
	}
	return &ConnectionSupervisor{
		conn:    conn,
		cfg:     cfg,
		logger:  logger,
		backoff: NewBackoff(cfg),
		trigger: make(chan struct{}, 1),
		done:    make(chan struct{}),
	}
}

// Start performs the initial connect (with retry+backoff) and, on success,
// launches the supervisor goroutine. It returns nil after a successful
// initial connect; the goroutine continues running until ctx is cancelled.
// If the initial connect cannot succeed before ctx is cancelled, Start
// returns ctx.Err().
func (s *ConnectionSupervisor) Start(ctx context.Context) error {
	if err := s.initialConnect(ctx); err != nil {
		return err
	}
	go s.run(ctx)
	return nil
}

// Trigger asks the supervisor to perform a reconnect. It is non-blocking:
// if a previous trigger is still pending, the call is silently dropped
// (the pending trigger already covers the new request). Safe for use
// across goroutines and satisfies FailureNotifier.
func (s *ConnectionSupervisor) Trigger() {
	select {
	case s.trigger <- struct{}{}:
	default:
	}
}

// Done returns a channel closed when the supervisor goroutine has exited.
// Useful for graceful shutdown coordination and test synchronization.
func (s *ConnectionSupervisor) Done() <-chan struct{} {
	return s.done
}

// initialConnect runs Connect in a backoff loop until it succeeds or ctx
// is cancelled. On success it returns nil and resets the backoff.
func (s *ConnectionSupervisor) initialConnect(ctx context.Context) error {
	for {
		if err := s.conn.Connect(ctx); err == nil {
			s.backoff.Reset()
			s.logger.Info("modbus initial connect ok")
			return nil
		} else {
			metrics.ModbusErrorsTotal("reconnect_failed").Inc()
			s.logger.Warn("modbus initial connect failed", slog.Any("error", err))
		}

		delay := s.backoff.Next()
		s.logger.Info("retry scheduled", slog.Duration("delay", delay))

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
}

// run is the supervisor goroutine. It waits for triggers and ctx
// cancellation. On a trigger it tears down the connection and runs the
// reconnect loop until success or cancellation.
func (s *ConnectionSupervisor) run(ctx context.Context) {
	defer close(s.done)
	for {
		select {
		case <-ctx.Done():
			return
		case <-s.trigger:
			s.logger.Warn("supervisor trigger received")
			if err := s.conn.ForceClose(); err != nil {
				s.logger.Warn("force close failed", slog.Any("error", err))
			}
			s.reconnectLoop(ctx)
		}
	}
}

// reconnectLoop attempts Connect with exponential backoff until success or
// ctx cancellation. On success it resets the backoff. Each failed attempt
// increments the modbus_errors_total{error_type="reconnect_failed"} counter.
func (s *ConnectionSupervisor) reconnectLoop(ctx context.Context) {
	for {
		delay := s.backoff.Next()
		s.logger.Info("reconnect attempt scheduled", slog.Duration("delay", delay))

		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}

		if err := s.conn.Connect(ctx); err != nil {
			metrics.ModbusErrorsTotal("reconnect_failed").Inc()
			s.logger.Warn("reconnect failed", slog.Any("error", err))
			continue
		}
		s.backoff.Reset()
		s.logger.Info("modbus reconnected")
		return
	}
}
```

#### File: `application/service/connection_supervisor_test.go`

```go
package service_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/port"
	"github.com/alexmorbo/oasis-modbus2mqtt/application/service"
	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/config"
)

// fakeConn is a hand-written mock of port.ModbusConnection.
type fakeConn struct {
	mu sync.Mutex

	connectFn       func(ctx context.Context) error
	forceCloseFn    func() error
	connectedFlag   bool
	connectCalls    int
	forceCloseCalls int
}

func (f *fakeConn) Connect(ctx context.Context) error {
	f.mu.Lock()
	f.connectCalls++
	fn := f.connectFn
	f.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn(ctx)
}

func (f *fakeConn) ForceClose() error {
	f.mu.Lock()
	f.forceCloseCalls++
	fn := f.forceCloseFn
	f.mu.Unlock()
	if fn == nil {
		return nil
	}
	return fn()
}

func (f *fakeConn) Connected() bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connectedFlag
}

func (f *fakeConn) ReadInput(_ context.Context, _, _ uint16) ([]uint16, error) {
	return nil, errors.New("not implemented in fakeConn")
}

func (f *fakeConn) ReadHolding(_ context.Context, _, _ uint16) ([]uint16, error) {
	return nil, errors.New("not implemented in fakeConn")
}

func (f *fakeConn) WriteHolding(_ context.Context, _, _ uint16) error {
	return errors.New("not implemented in fakeConn")
}

func (f *fakeConn) ModifyHolding(_ context.Context, _ uint16, _ func(uint16) uint16) error {
	return errors.New("not implemented in fakeConn")
}

func (f *fakeConn) ConnectCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.connectCalls
}

func (f *fakeConn) ForceCloseCalls() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.forceCloseCalls
}

var _ port.ModbusConnection = (*fakeConn)(nil)

func fastReconnectCfg() config.ReconnectConfig {
	return config.ReconnectConfig{
		MinDelay:  1 * time.Millisecond,
		MaxDelay:  10 * time.Millisecond,
		Factor:    2.0,
		JitterPct: 0,
	}
}

func discardLog() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNewConnectionSupervisor_NilLoggerFallsBackToDefault(t *testing.T) {
	t.Parallel()

	conn := &fakeConn{}
	s := service.NewConnectionSupervisor(conn, fastReconnectCfg(), nil)
	require.NotNil(t, s)
}

func TestStart_InitialConnect_Success(t *testing.T) {
	t.Parallel()

	conn := &fakeConn{}
	s := service.NewConnectionSupervisor(conn, fastReconnectCfg(), discardLog())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	require.NoError(t, s.Start(ctx))
	assert.Equal(t, 1, conn.ConnectCalls())

	cancel()
	select {
	case <-s.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("supervisor goroutine did not exit after ctx cancellation")
	}
}

func TestStart_InitialConnect_Retries(t *testing.T) {
	t.Parallel()

	var attempts int
	var mu sync.Mutex
	conn := &fakeConn{
		connectFn: func(_ context.Context) error {
			mu.Lock()
			attempts++
			n := attempts
			mu.Unlock()
			if n < 3 {
				return errors.New("transient")
			}
			return nil
		},
	}
	s := service.NewConnectionSupervisor(conn, fastReconnectCfg(), discardLog())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	require.NoError(t, s.Start(ctx))
	assert.Equal(t, 3, conn.ConnectCalls())

	cancel()
	select {
	case <-s.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("supervisor goroutine did not exit")
	}
}

func TestStart_InitialConnect_CancelledContext(t *testing.T) {
	t.Parallel()

	conn := &fakeConn{
		connectFn: func(_ context.Context) error {
			return errors.New("always fails")
		},
	}
	s := service.NewConnectionSupervisor(conn, fastReconnectCfg(), discardLog())

	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()

	err := s.Start(ctx)
	require.Error(t, err)
	assert.ErrorIs(t, err, context.DeadlineExceeded)
	assert.Greater(t, conn.ConnectCalls(), 0)
}

func TestTrigger_TriggersForceCloseAndReconnect(t *testing.T) {
	t.Parallel()

	conn := &fakeConn{}
	s := service.NewConnectionSupervisor(conn, fastReconnectCfg(), discardLog())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	require.NoError(t, s.Start(ctx))
	require.Equal(t, 1, conn.ConnectCalls())

	s.Trigger()

	require.Eventually(t, func() bool {
		return conn.ForceCloseCalls() >= 1 && conn.ConnectCalls() >= 2
	}, 500*time.Millisecond, 5*time.Millisecond, "supervisor did not handle trigger")

	cancel()
	select {
	case <-s.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("supervisor goroutine did not exit")
	}
}

func TestTrigger_ForceCloseError_DoesNotPreventReconnect(t *testing.T) {
	t.Parallel()

	conn := &fakeConn{
		forceCloseFn: func() error { return errors.New("close failed") },
	}
	s := service.NewConnectionSupervisor(conn, fastReconnectCfg(), discardLog())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	require.NoError(t, s.Start(ctx))
	s.Trigger()

	require.Eventually(t, func() bool {
		return conn.ForceCloseCalls() >= 1 && conn.ConnectCalls() >= 2
	}, 500*time.Millisecond, 5*time.Millisecond)

	cancel()
	<-s.Done()
}

func TestTrigger_ReconnectFails_KeepsRetryingUntilSuccess(t *testing.T) {
	t.Parallel()

	var afterTrigger int
	var mu sync.Mutex
	connSucceeded := make(chan struct{})

	conn := &fakeConn{
		connectFn: func(_ context.Context) error {
			mu.Lock()
			afterTrigger++
			n := afterTrigger
			mu.Unlock()
			// First call (initial connect) succeeds; next two fail; fourth
			// (still in reconnectLoop) succeeds and unblocks the test.
			if n == 1 {
				return nil
			}
			if n < 4 {
				return errors.New("transient")
			}
			select {
			case connSucceeded <- struct{}{}:
			default:
			}
			return nil
		},
	}
	s := service.NewConnectionSupervisor(conn, fastReconnectCfg(), discardLog())

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()

	require.NoError(t, s.Start(ctx))
	s.Trigger()

	select {
	case <-connSucceeded:
	case <-time.After(1 * time.Second):
		t.Fatal("reconnect loop never produced a successful connect")
	}

	cancel()
	<-s.Done()
}

func TestTrigger_NonBlocking_WhenChannelFull(t *testing.T) {
	t.Parallel()

	// Do NOT call Start: the trigger channel has cap 1, no consumer; we
	// only verify that repeated Trigger calls never block.
	conn := &fakeConn{}
	s := service.NewConnectionSupervisor(conn, fastReconnectCfg(), discardLog())

	start := time.Now()
	for i := 0; i < 1000; i++ {
		s.Trigger()
	}
	elapsed := time.Since(start)
	assert.Less(t, elapsed, 100*time.Millisecond, "Trigger must be non-blocking under load")
}

func TestStop_via_ContextCancelled(t *testing.T) {
	t.Parallel()

	conn := &fakeConn{}
	s := service.NewConnectionSupervisor(conn, fastReconnectCfg(), discardLog())

	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, s.Start(ctx))

	cancel()

	select {
	case <-s.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Done channel was not closed within timeout after ctx cancellation")
	}
}

func TestStop_DuringReconnectLoop(t *testing.T) {
	t.Parallel()

	connectCalls := 0
	var mu sync.Mutex
	conn := &fakeConn{
		connectFn: func(_ context.Context) error {
			mu.Lock()
			connectCalls++
			n := connectCalls
			mu.Unlock()
			if n == 1 {
				return nil // initial connect succeeds
			}
			return errors.New("perma-fail") // reconnect always fails
		},
	}
	cfg := config.ReconnectConfig{
		MinDelay:  10 * time.Millisecond,
		MaxDelay:  20 * time.Millisecond,
		Factor:    2.0,
		JitterPct: 0,
	}
	s := service.NewConnectionSupervisor(conn, cfg, discardLog())

	ctx, cancel := context.WithCancel(context.Background())
	require.NoError(t, s.Start(ctx))
	s.Trigger()

	// Give the supervisor a moment to enter reconnectLoop and start a
	// timer wait, then cancel.
	time.Sleep(15 * time.Millisecond)
	cancel()

	select {
	case <-s.Done():
	case <-time.After(500 * time.Millisecond):
		t.Fatal("Done not closed after cancellation during reconnect loop")
	}
}
```

---

status: review
updated: 2026-04-25

---

## Agent Execution

### Implementation Agent Instructions

```
Task tool call:
- subagent_type: "general-purpose"
- model: "haiku"
- prompt: |
    You are an IMPLEMENTATION AGENT for story 007-dispatcher-and-supervisor.

    ABSOLUTE RULES:
    - Copy code BYTE-FOR-BYTE.
    - DO NOT modify code, fix imports, add `//nolint`. STOP on failure.

    PROCESS:
    1. Read story.
    2. Write each "#### File: `path`" block.
    3. From work dir:
       a. gofmt -l application/   (must be empty)
       b. go build ./...
       c. go vet ./...
       d. go test -short ./...
       e. go test -race ./application/service/...
       f. coverage: go test -coverpkg=./application/service -coverprofile=/tmp/oasis_svc.out ./application/service/...
          → go tool cover -func=/tmp/oasis_svc.out | tail -1   ≥90%
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

- [ ] `application/port/modbus_connection.go`
- [ ] `application/service/backoff.go` (+ test)
- [ ] `application/service/command_dispatcher.go` (+ test)
- [ ] `application/service/connection_supervisor.go` (+ test)
- [ ] gofmt clean
- [ ] go build/vet pass
- [ ] go test pass
- [ ] go test -race pass
- [ ] coverage ≥90%
- [ ] lint clean

### Verification Results

```
gofmt:                       PASS
go build:                    PASS
go vet:                      PASS
go test:                     PASS
go test -race:               PASS
application/service cov:     100.0%
golangci-lint:               PASS
```

### Issues Found

(none)

### Fixes Applied

**2026-04-25 — Fix Agent (sync nolint:gosec G115)**

Sync `backoff.go` and `backoff_test.go`: Added 7 `//nolint:gosec` directives for G115 (int64→uint64 narrowing in `time.Now().UnixNano()` seed conversion, lines 28–29) and test seed constructors `rand.NewPCG(N, N)` (lines 29, 51, 65, 78, 92). Narrowing is unavoidable—value is bounded by time semantics; not a security issue.

**2026-04-25 — Fix Agent (gosec G404 + unparam)**

1. **G404 → `math/rand/v2` migration** (`backoff.go`, `backoff_test.go`):
   - Changed import from `"math/rand"` to `"math/rand/v2"` in both files.
   - `NewBackoff`: replaced `rand.New(rand.NewSource(time.Now().UnixNano()))` with `rand.New(rand.NewPCG(seed, seed^0x9E3779B97F4A7C15))`.
   - `Backoff.rng` field type and `NewBackoffWithRand` parameter type updated to `*rand.Rand` (now resolved from `math/rand/v2`).
   - Test seeding changed from `rand.New(rand.NewSource(N))` to `rand.New(rand.NewPCG(N, N))` for all seeded tests.

2. **unparam — remove always-same parameters**:
   - `newCfg(min, max, factor, jitter)` → `newCfg(min, max, jitter)`: `factor` was always `2.0`; now hardcoded inside the helper. All 6 call sites updated.
   - `newDispatcher(_ *testing.T, client, notifier, threshold int)` → `newDispatcher(client, notifier)`: `*testing.T` was unused and `threshold` was always `3`; both removed and `3` hardcoded inside the helper. All 13 call sites updated.

**2026-04-25 — Implementation Agent (Re-run post-Fix, all code copied byte-for-byte)**

1. **All 7 files copied from story**, verifying Fix Agent changes were preserved:
   - `math/rand/v2` imports ✓
   - Test helper signatures simplified (`newCfg`, `newDispatcher`) ✓
   - `//nolint:gosec` added for non-crypto backoff jitter (deliberate, per spec) ✓

2. **All verifications passed**:
   - `gofmt -l application/` → empty
   - `go build ./...` → OK
   - `go vet ./...` → OK
   - `go test -short ./...` → OK (service: 0.520s)
   - `go test -race ./application/service/...` → OK (1.459s)
   - Coverage `application/service`: **100.0%** (exceeds 90% requirement)
   - `golangci-lint run ./...` → 0 issues (gosec G115/G404 suppressed appropriately)

---

## Files Changed

- `application/port/modbus_connection.go`
- `application/service/backoff.go`
- `application/service/backoff_test.go`
- `application/service/command_dispatcher.go`
- `application/service/command_dispatcher_test.go`
- `application/service/connection_supervisor.go`
- `application/service/connection_supervisor_test.go`
