---
title: "Feature: ApplyCommand use case"
status: ready
priority: high
complexity: 4
planning_model: opus-4-5
implementation_model: haiku-4
created: 2026-04-25
updated: 2026-04-25
depends_on: 008-poll-controller-poller-availability
risk_areas: [power-dev-edge-sequence, dev-keys-2-rmw, transition-state-handling]
---

## Context

Story 009 — единственная **write-side** use case в bridge'е. Принимает Command DTO (story 004) от MQTT subscriber'а (story 013) и транслирует в правильную последовательность Modbus операций с учётом quirks контроллера.

**4 типа команд:**

1. **SetPowerCommand** (`Power_Dev`, h2):
   - **ON**: одна запись `WriteHolding(2, 1)`.
   - **OFF**: edge sequence — `WriteHolding(2, 1)` → sleep 150ms → `WriteHolding(2, 0)`. Quirk #3 в [`02-controller-quirks.md`](file:.thoughts/oasis-syberia-modbus-bridge/02-controller-quirks.md). Между записями guard interval (100ms) уже обеспечен dispatcher'ом, но мы явно ждём 150ms (≥100ms safe).

2. **SetModeCommand** (`Dev_Keys_2`, h86, биты 0..1):
   - **RMW**: через `dispatcher.ModifyHolding(86, modifyFunc)`. modifyFunc применяет `valueobject.ApplyToRegister(current, cmd.Mode)` — маскирует биты 0..1 + OR'ит mode bits. Quirk #4: остальные биты h86 (timer, humidifier type) НЕ должны быть затронуты.

3. **SetTemperatureCommand** (`Temp_Target`, h31):
   - Прямая запись `WriteHolding(31, cmd.Temperature.Raw())`. `Raw()` уже clamp'ит к [50, 300] в valueobject (story 002).
   - Validation на этапе конструирования Command (`valueobject.NewTemperature(5..30°C)`) — usecase доверяет.

4. **SetFanCommand** (`Fan_Target_1`, h32):
   - Прямая запись `WriteHolding(32, cmd.FanSpeed.Value())`.
   - Validation в Command (`valueobject.NewFanSpeed(1..10)` или `NewFanSpeedClamped(v, 4, 7)` для UX).

**Transition state guard:**

Перед любой командой проверяем `lastSnapshot.Operation.IsTransitional()`. Если контроллер в transition state (preheat, start_fan, fan_coastdown, и т.д.) — отказываем с `dto.ErrTransitionInProgress`. **Кроме**: ON команды Power_Dev — её разрешаем даже в transition (если контроллер уже выключился где-то посредине, ON вернёт его). Это исключение задокументировано в godoc.

**Зачем guard**: писать в Temp_Target во время preheat бессмысленно (контроллер уже определил уставку и греет). Логически безопасно, но даёт юзеру предсказуемый UX — команда либо выполнилась, либо отказана с понятной причиной (HA UI: pending state retry).

**`SnapshotProvider` interface** — новый local interface в `application/usecase/apply_command.go`. Реализуется `*service.Poller` (story 008 уже имеет `Snapshot()` метод). Duck-typed, никаких изменений в poller'е.

Reference: `/Users/alexmorbo/Home/homelab/.thoughts/oasis-syberia-modbus-bridge/04-mqtt-bridge-plan.md` секция `## 5. Watchdog/reconnect псевдокод` → "Command-specific algorithms" subsection.

## User Story

**As a** разработчик oasis-modbus2mqtt,
**I want to** иметь единую entry point для всех write-команд с типобезопасной валидацией, transition-state guard, и корректными edge sequences,
**So that** MQTT subscriber (story 013) мог просто вызвать `applyCommand.Apply(ctx, cmd)` без знания про edge-trigger Power_Dev, RMW Dev_Keys_2, или Temp_Target/Fan_Target raw addresses.

## Acceptance Criteria

### ApplyCommand usecase

- [ ] `application/usecase/apply_command.go`:
  - **Local interfaces**:
    ```go
    // CommandDispatcher is the subset of dispatcher API used for writes.
    type CommandDispatcher interface {
        WriteHolding(ctx context.Context, addr, value uint16) error
        ModifyHolding(ctx context.Context, addr uint16, modify func(uint16) uint16) error
    }

    // SnapshotProvider returns the latest known controller snapshot for transition-state checks.
    type SnapshotProvider interface {
        Snapshot() entity.Snapshot
    }
    ```
  - **Constants** (Modbus addresses, public package-level for clarity):
    ```go
    const (
        AddrPowerDev    uint16 = 2
        AddrTempTarget  uint16 = 31
        AddrFanTarget1  uint16 = 32
        AddrDevKeys2    uint16 = 86
    )
    const DefaultPowerOffPause = 150 * time.Millisecond
    ```
  - **Struct**:
    ```go
    type ApplyCommand struct {
        dispatcher    CommandDispatcher
        snapshots     SnapshotProvider
        powerOffPause time.Duration
        logger        *slog.Logger
    }
    ```
  - **Constructor**:
    ```go
    func NewApplyCommand(d CommandDispatcher, snaps SnapshotProvider, logger *slog.Logger) *ApplyCommand
    ```
    - nil dispatcher → panic "dispatcher must not be nil".
    - nil snaps → panic "snapshots must not be nil".
    - nil logger → use `slog.Default()`.
    - powerOffPause = `DefaultPowerOffPause` (150ms).
    - **Optional setter** `WithPowerOffPause(d time.Duration) *ApplyCommand` — fluent for tests (no validation, caller responsibility).
  - **Public method**:
    ```go
    func (a *ApplyCommand) Apply(ctx context.Context, cmd dto.Command) error
    ```
    - Type switch на 4 known types. **Default case**: `return fmt.Errorf("unknown command type %T: %w", cmd, dto.ErrInvalidPayload)`. (`ErrInvalidPayload` уже в story 004.)
    - log INFO `"applying command"` с `"command", cmd.String()` — Command's String() method из story 004 (plain key-value pair; slog.Stringer не существует в stdlib).
    - На любую error: log WARN `"command failed"`.
  - **Internal methods** (unexported):
    - `setPower(ctx, cmd dto.SetPowerCommand) error`:
      - **ON path**: skip transition guard (allowed), `dispatcher.WriteHolding(ctx, AddrPowerDev, 1)`. Return error verbatim.
      - **OFF path**: check transition guard. If transitional → `dto.ErrTransitionInProgress`.
      - Edge sequence: `WriteHolding(2, 1)`. On error → return wrapped.
      - `select { case <-time.After(powerOffPause): case <-ctx.Done(): return ctx.Err() }`.
      - `WriteHolding(2, 0)`. Return error. Log INFO "power off sequence completed" on success.
    - `setMode(ctx, cmd dto.SetModeCommand) error`:
      - Check transition guard.
      - `dispatcher.ModifyHolding(ctx, AddrDevKeys2, func(cur uint16) uint16 { return valueobject.ApplyToRegister(cur, cmd.Mode) })`.
    - `setTemperature(ctx, cmd dto.SetTemperatureCommand) error`:
      - Check transition guard.
      - `dispatcher.WriteHolding(ctx, AddrTempTarget, cmd.Temperature.Raw())`.
    - `setFan(ctx, cmd dto.SetFanCommand) error`:
      - Check transition guard.
      - `dispatcher.WriteHolding(ctx, AddrFanTarget1, cmd.FanSpeed.Value())`.
    - `transitionGuard() error`:
      - `snap := a.snapshots.Snapshot()`
      - if `snap.Operation.IsTransitional()` → return `fmt.Errorf("operation %s in progress: %w", snap.Operation, dto.ErrTransitionInProgress)`.
      - return nil.

### Tests

- [ ] `application/usecase/apply_command_test.go`:
  - Mock `CommandDispatcher`:
    ```go
    type fakeDispatcher struct {
        mu sync.Mutex
        writes []writeCall
        modifies []modifyCall
        writeErr, modifyErr error
    }
    type writeCall struct { addr, value uint16 }
    type modifyCall struct { addr uint16; modify func(uint16) uint16 }
    func (d *fakeDispatcher) WriteHolding(ctx context.Context, addr, value uint16) error { ... }
    func (d *fakeDispatcher) ModifyHolding(ctx context.Context, addr uint16, modify func(uint16) uint16) error { ... }
    ```
  - Mock `SnapshotProvider`:
    ```go
    type fakeSnapshots struct{ snap entity.Snapshot }
    func (f *fakeSnapshots) Snapshot() entity.Snapshot { return f.snap }
    ```
  - Tests:
    - `TestNewApplyCommand_NilDispatcher_Panics`.
    - `TestNewApplyCommand_NilSnapshots_Panics`.
    - `TestNewApplyCommand_NilLogger_FallsBack` — no panic, ctor returns valid instance, Apply works.
    - `TestApply_UnknownCommandType_ReturnsErrInvalidPayload` — define a private test command type implementing `dto.Command` would fail compile (commandTag is unexported); instead pass `nil dto.Command` (some commands could be nil? no — Command is interface). Alternative: skip this test or use a `dto.Command(nil)` literal — type switch hits default. `assert.ErrorIs(err, dto.ErrInvalidPayload)`. Decision: pass `nil dto.Command` value, expect `unknown command type <nil>: invalid payload`. NB: `nil` interface satisfies the type switch default.

      **Wait** — `nil dto.Command`: in type switch, `nil` matches `nil` case if you have one. We don't have explicit nil case, so it falls through to default. Confirmed.
    - `TestApply_SetPower_On_SingleWrite`:
      - dispatcher: empty writes.
      - Apply(SetPowerCommand{On: true}).
      - assert: dispatcher.writes == `[{addr: 2, value: 1}]`. No transition check (Snapshot() not called for ON).
    - `TestApply_SetPower_Off_EdgeSequence`:
      - clock-independent: powerOffPause set to 1ms via WithPowerOffPause.
      - Apply(SetPowerCommand{On: false}).
      - assert: dispatcher.writes has 2 entries: `[{2, 1}, {2, 0}]`. Time elapsed ≥ 1ms.
    - `TestApply_SetPower_Off_TransitionGuard_Refuses`:
      - snap.Operation = OpPreheatCalorifier.
      - Apply(SetPowerCommand{On: false}).
      - assert: error `errors.Is(err, dto.ErrTransitionInProgress)`. dispatcher.writes empty.
    - `TestApply_SetPower_On_AllowedDuringTransition`:
      - snap.Operation = OpPreheatCalorifier.
      - Apply(SetPowerCommand{On: true}).
      - assert: write executed.
    - `TestApply_SetPower_Off_FirstWriteFails_NoSecondWrite`:
      - dispatcher.writeErr = errSentinel on FIRST call only (custom logic in mock).
      - Apply(SetPowerCommand{On: false}).
      - assert: error wraps errSentinel, dispatcher.writes has 1 entry only.
    - `TestApply_SetPower_Off_ContextCancelledDuringPause`:
      - powerOffPause = 100ms; ctx cancelled after 10ms.
      - Apply returns ctx.Err()-wrapping error.
      - dispatcher.writes has 1 entry (the first write, no second).
    - `TestApply_SetMode_RMWModifyFn`:
      - Apply(SetModeCommand{Mode: ModeHeat}).
      - assert: dispatcher.modifies has 1 entry, addr=86. Apply modify func to test value `0xC003` (existing high bits + bits 0..1=0x3): `result := modify(0xC003); assert.Equal(0xC001, result)` (preserves 0xC000, sets bits 0..1 to 01).
    - `TestApply_SetMode_TransitionGuard`:
      - snap.Operation = OpStartFan.
      - assert: error ErrTransitionInProgress.
    - `TestApply_SetTemperature_WritesH31_RawValue`:
      - cmd.Temperature.Celsius() = 22.5.
      - Apply.
      - assert: dispatcher.writes == `[{31, 225}]`.
    - `TestApply_SetTemperature_TransitionGuard`.
    - `TestApply_SetFan_WritesH32_Value`:
      - cmd.FanSpeed.Value() = 5.
      - Apply.
      - assert: dispatcher.writes == `[{32, 5}]`.
    - `TestApply_SetFan_TransitionGuard`.
    - `TestApply_DispatcherError_Propagates`:
      - dispatcher.writeErr = errSentinel.
      - SetTemperature → wraps errSentinel.
    - `TestApply_LogsCommand` — capture slog into buffer; assert command's String() appears in log line.
  - All tests `t.Parallel()`.

### Quality

- [ ] Coverage `application/usecase` ≥ 90%. (Combined with story 008 tests — should be high.)
- [ ] `go test -race` clean.
- [ ] `golangci-lint run ./...` PASS.
- [ ] `gofmt -l application/usecase/` empty.
- [ ] godoc one-liner на каждом exported.

## Constraints

- Зависимости: stdlib + project packages. Никаких внешних.
- НЕТ `init()`.
- НЕ использовать `time.Sleep` напрямую — `select { case <-time.After(d): case <-ctx.Done() }` чтобы быть ctx-aware.
- НЕ читать `Snapshot()` в hot loop — один вызов в начале команды (transition guard).
- Mock в test file (inline structs).
- Goimports 3 группы.
- gosec G115 — не должно быть narrowing'ов в этой story.
- НЕ использовать generics.
- НЕ модифицировать file `application/usecase/poll_controller.go` или его тесты.

---

## Technical Specification

### Analysis

- `SetPower(On=true)` deliberately bypasses the transition guard so the operator can recover a controller that ended up half-stopped mid-transition; OFF must respect the guard because the OFF edge sequence interleaves writes with sleeps and racing it through a transitional state is unsafe.
- `SetMode` uses `dispatcher.ModifyHolding` (RMW) on `Dev_Keys_2` (h86) so the timer/humidifier-type bits in the upper register survive the write; the modify closure delegates to `valueobject.ApplyToRegister`, which masks bits 0..1 and OR's `Mode.Bits()`.
- The OFF edge sequence uses `select { <-time.After(d): <-ctx.Done(): }` so a cancellation between the two writes is observed promptly — the second write is skipped and `ctx.Err()` is returned without further log noise.
- The `Apply` type switch's `default` branch handles both unknown command types and a literal `nil` interface (`var cmd dto.Command = nil`) — `dto.Command` is a closed marker (unexported `commandTag()`) so production callers cannot construct foreign types, but the bridge MUST still degrade safely on a nil from a future MQTT decoder.
- Logging captures `cmdStr := cmd.String()` immediately after the nil-check guard, then passes `"command", cmdStr` as a plain key-value pair to `slog.Info`/`slog.Warn`; this is the idiomatic slog pattern — `slog.Stringer` is not a stdlib function.
- `WithPowerOffPause(d)` is a fluent setter exposed for tests; production code keeps the `DefaultPowerOffPause = 150ms` derived from controller quirk #3.
- The dispatcher mock targets a specific call ordinal via `writeErrAtCall` so the "first write fails, no second write" test can prove the sequence aborts before the pause; later tests reuse the same mock with `writeErr` for the simpler "all writes fail" path.

### Implementation Order

1. `application/usecase/apply_command.go`
2. `application/usecase/apply_command_test.go`

---

### 1. ApplyCommand use case

#### File: `application/usecase/apply_command.go`

```go
package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/dto"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"
)

// CommandDispatcher is the subset of the Modbus dispatcher API used by
// ApplyCommand for write-side operations. Defining the interface here (rather
// than importing application/service) keeps the use case independent of the
// service implementation and breaks a would-be cyclic dependency.
type CommandDispatcher interface {
	// WriteHolding writes a single holding register at addr.
	WriteHolding(ctx context.Context, addr, value uint16) error
	// ModifyHolding atomically reads addr, applies modify, and writes the
	// result back. Implementations are expected to serialize the read+write.
	ModifyHolding(ctx context.Context, addr uint16, modify func(uint16) uint16) error
}

// SnapshotProvider returns the latest known controller snapshot. ApplyCommand
// uses it to consult Operation for the transition-state guard before any
// command write. *service.Poller (story 008) satisfies this interface via duck
// typing.
type SnapshotProvider interface {
	// Snapshot returns the most recent merged snapshot. The provider must be
	// safe for concurrent calls; ApplyCommand calls Snapshot once per command.
	Snapshot() entity.Snapshot
}

// Modbus holding-register addresses written by ApplyCommand.
const (
	// AddrPowerDev is holding register Power_Dev (h2): edge-trigger power.
	AddrPowerDev uint16 = 2
	// AddrTempTarget is holding register Temp_Target (h31): setpoint ×10 °C.
	AddrTempTarget uint16 = 31
	// AddrFanTarget1 is holding register Fan_Target_1 (h32): fan-speed [1,10].
	AddrFanTarget1 uint16 = 32
	// AddrDevKeys2 is holding register Dev_Keys_2 (h86): mode in bits 0..1.
	AddrDevKeys2 uint16 = 86
)

// DefaultPowerOffPause is the dwell between the rising and falling edges of
// the Power_Dev OFF sequence. 150 ms exceeds the controller's documented
// minimum guard interval (100 ms, quirk #3) without noticeably delaying UX.
const DefaultPowerOffPause = 150 * time.Millisecond

// ApplyCommand is the single write-side use case of the bridge: it accepts
// validated dto.Command values and translates them into the correct sequence
// of Modbus writes for the Oasis Syberia controller, applying the
// transition-state guard and Power_Dev edge-trigger quirks.
type ApplyCommand struct {
	dispatcher    CommandDispatcher
	snapshots     SnapshotProvider
	powerOffPause time.Duration
	logger        *slog.Logger
}

// NewApplyCommand constructs an ApplyCommand. A nil dispatcher or nil
// snapshots provider is a programming error and panics. A nil logger falls
// back to slog.Default. powerOffPause defaults to DefaultPowerOffPause.
func NewApplyCommand(d CommandDispatcher, snaps SnapshotProvider, logger *slog.Logger) *ApplyCommand {
	if d == nil {
		panic("apply command: dispatcher must not be nil")
	}
	if snaps == nil {
		panic("apply command: snapshots must not be nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ApplyCommand{
		dispatcher:    d,
		snapshots:     snaps,
		powerOffPause: DefaultPowerOffPause,
		logger:        logger,
	}
}

// WithPowerOffPause overrides the dwell between the two Power_Dev writes in
// the OFF edge sequence. The setter is fluent and exists primarily for tests;
// callers are responsible for picking a value at least as long as the
// controller's guard interval.
func (a *ApplyCommand) WithPowerOffPause(d time.Duration) *ApplyCommand {
	a.powerOffPause = d
	return a
}

// Apply dispatches cmd to the appropriate write sequence. It returns an
// error wrapping dto.ErrInvalidPayload for unknown or nil commands,
// dto.ErrTransitionInProgress when the controller is mid-transition (except
// for SetPower ON, which bypasses the guard), or whatever error the
// dispatcher returns verbatim-wrapped with context.
func (a *ApplyCommand) Apply(ctx context.Context, cmd dto.Command) error {
	if cmd == nil {
		a.logger.Warn("apply: nil command")
		return fmt.Errorf("nil command: %w", dto.ErrInvalidPayload)
	}

	cmdStr := "<unknown>"
	if stringer, ok := cmd.(fmt.Stringer); ok {
		cmdStr = stringer.String()
	}
	a.logger.Info("applying command", "command", cmdStr)

	var err error
	switch c := cmd.(type) {
	case dto.SetPowerCommand:
		err = a.setPower(ctx, c)
	case dto.SetModeCommand:
		err = a.setMode(ctx, c)
	case dto.SetTemperatureCommand:
		err = a.setTemperature(ctx, c)
	case dto.SetFanCommand:
		err = a.setFan(ctx, c)
	default:
		return fmt.Errorf("unknown command type %T: %w", cmd, dto.ErrInvalidPayload)
	}

	if err != nil {
		a.logger.Warn("command failed", "command", cmdStr, "error", err)
	}
	return err
}

// setPower writes the Power_Dev (h2) edge sequence. ON is a single rising
// write and bypasses the transition guard so a stuck controller can be
// recovered. OFF is a rising-then-falling sequence with a context-aware dwell
// in between and respects the transition guard.
func (a *ApplyCommand) setPower(ctx context.Context, cmd dto.SetPowerCommand) error {
	if cmd.On {
		if err := a.dispatcher.WriteHolding(ctx, AddrPowerDev, 1); err != nil {
			return fmt.Errorf("power on write h%d: %w", AddrPowerDev, err)
		}
		return nil
	}

	if err := a.transitionGuard(); err != nil {
		return err
	}

	if err := a.dispatcher.WriteHolding(ctx, AddrPowerDev, 1); err != nil {
		return fmt.Errorf("power off rising-edge write h%d: %w", AddrPowerDev, err)
	}

	select {
	case <-time.After(a.powerOffPause):
	case <-ctx.Done():
		return ctx.Err()
	}

	if err := a.dispatcher.WriteHolding(ctx, AddrPowerDev, 0); err != nil {
		return fmt.Errorf("power off falling-edge write h%d: %w", AddrPowerDev, err)
	}

	a.logger.Info("power off sequence completed")
	return nil
}

// setMode performs an atomic read-modify-write on Dev_Keys_2 (h86), replacing
// only bits 0..1 with cmd.Mode and preserving every other bit (timer flags,
// humidifier-type, etc.). Respects the transition guard.
func (a *ApplyCommand) setMode(ctx context.Context, cmd dto.SetModeCommand) error {
	if err := a.transitionGuard(); err != nil {
		return err
	}
	mode := cmd.Mode
	if err := a.dispatcher.ModifyHolding(ctx, AddrDevKeys2, func(cur uint16) uint16 {
		return valueobject.ApplyToRegister(cur, mode)
	}); err != nil {
		return fmt.Errorf("set mode modify h%d: %w", AddrDevKeys2, err)
	}
	return nil
}

// setTemperature writes the validated setpoint to Temp_Target (h31). Respects
// the transition guard. Range validation lives in valueobject.Temperature.
func (a *ApplyCommand) setTemperature(ctx context.Context, cmd dto.SetTemperatureCommand) error {
	if err := a.transitionGuard(); err != nil {
		return err
	}
	if err := a.dispatcher.WriteHolding(ctx, AddrTempTarget, cmd.Temperature.Raw()); err != nil {
		return fmt.Errorf("set temperature write h%d: %w", AddrTempTarget, err)
	}
	return nil
}

// setFan writes the validated fan speed to Fan_Target_1 (h32). Respects the
// transition guard. Range validation lives in valueobject.FanSpeed.
func (a *ApplyCommand) setFan(ctx context.Context, cmd dto.SetFanCommand) error {
	if err := a.transitionGuard(); err != nil {
		return err
	}
	if err := a.dispatcher.WriteHolding(ctx, AddrFanTarget1, cmd.FanSpeed.Value()); err != nil {
		return fmt.Errorf("set fan write h%d: %w", AddrFanTarget1, err)
	}
	return nil
}

// transitionGuard returns a wrapped dto.ErrTransitionInProgress when the
// latest snapshot reports a non-idle, non-unknown operating state. Used by
// every write path except SetPower ON (which is the recovery escape hatch).
func (a *ApplyCommand) transitionGuard() error {
	snap := a.snapshots.Snapshot()
	if snap.Operation.IsTransitional() {
		return fmt.Errorf("operation %s in progress: %w", snap.Operation, dto.ErrTransitionInProgress)
	}
	return nil
}

```

---

### 2. Tests

#### File: `application/usecase/apply_command_test.go`

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

	"github.com/alexmorbo/oasis-modbus2mqtt/application/dto"
	"github.com/alexmorbo/oasis-modbus2mqtt/application/usecase"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"
)

type writeCall struct {
	addr  uint16
	value uint16
}

type modifyCall struct {
	addr   uint16
	modify func(uint16) uint16
}

type fakeCommandDispatcher struct {
	mu                sync.Mutex
	writes            []writeCall
	modifies          []modifyCall
	writeErr          error
	modifyErr         error
	writeErrAtCall    int
	writeErrAtCallErr error
}

func (d *fakeCommandDispatcher) WriteHolding(_ context.Context, addr, value uint16) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.writes = append(d.writes, writeCall{addr: addr, value: value})
	if d.writeErrAtCall != 0 && len(d.writes) == d.writeErrAtCall {
		return d.writeErrAtCallErr
	}
	if d.writeErr != nil {
		return d.writeErr
	}
	return nil
}

func (d *fakeCommandDispatcher) ModifyHolding(_ context.Context, addr uint16, modify func(uint16) uint16) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.modifies = append(d.modifies, modifyCall{addr: addr, modify: modify})
	if d.modifyErr != nil {
		return d.modifyErr
	}
	return nil
}

func (d *fakeCommandDispatcher) snapshotWrites() []writeCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]writeCall, len(d.writes))
	copy(out, d.writes)
	return out
}

func (d *fakeCommandDispatcher) snapshotModifies() []modifyCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]modifyCall, len(d.modifies))
	copy(out, d.modifies)
	return out
}

type fakeSnapshots struct {
	mu   sync.Mutex
	snap entity.Snapshot
}

func (f *fakeSnapshots) Snapshot() entity.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap
}

func newSnaps(op entity.OperatingState) *fakeSnapshots {
	return &fakeSnapshots{snap: entity.Snapshot{Operation: op}}
}

func mustTemp(t *testing.T, c float64) valueobject.Temperature {
	t.Helper()
	temp, err := valueobject.NewTemperature(c)
	require.NoError(t, err)
	return temp
}

func mustFan(t *testing.T, v uint16) valueobject.FanSpeed {
	t.Helper()
	f, err := valueobject.NewFanSpeed(v)
	require.NoError(t, err)
	return f
}

func TestNewApplyCommand_NilDispatcher_Panics(t *testing.T) {
	t.Parallel()
	require.PanicsWithValue(t, "apply command: dispatcher must not be nil", func() {
		_ = usecase.NewApplyCommand(nil, newSnaps(entity.OpIdle), nil)
	})
}

func TestNewApplyCommand_NilSnapshots_Panics(t *testing.T) {
	t.Parallel()
	d := &fakeCommandDispatcher{}
	require.PanicsWithValue(t, "apply command: snapshots must not be nil", func() {
		_ = usecase.NewApplyCommand(d, nil, nil)
	})
}

func TestNewApplyCommand_NilLogger_FallsBack(t *testing.T) {
	t.Parallel()
	d := &fakeCommandDispatcher{}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpIdle), nil)
	require.NotNil(t, apply)
	err := apply.Apply(context.Background(), dto.SetPowerCommand{On: true})
	require.NoError(t, err)
	assert.Equal(t, []writeCall{{addr: 2, value: 1}}, d.snapshotWrites())
}

func TestApply_UnknownCommandType_ReturnsErrInvalidPayload(t *testing.T) {
	t.Parallel()
	d := &fakeCommandDispatcher{}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpIdle), discardLogger())

	var cmd dto.Command
	err := apply.Apply(context.Background(), cmd)
	require.Error(t, err)
	assert.ErrorIs(t, err, dto.ErrInvalidPayload)
	assert.Empty(t, d.snapshotWrites())
	assert.Empty(t, d.snapshotModifies())
}

func TestApply_SetPower_On_SingleWrite(t *testing.T) {
	t.Parallel()
	d := &fakeCommandDispatcher{}
	snaps := newSnaps(entity.OpIdle)
	apply := usecase.NewApplyCommand(d, snaps, discardLogger())

	err := apply.Apply(context.Background(), dto.SetPowerCommand{On: true})
	require.NoError(t, err)
	assert.Equal(t, []writeCall{{addr: 2, value: 1}}, d.snapshotWrites())
}

func TestApply_SetPower_Off_EdgeSequence(t *testing.T) {
	t.Parallel()
	d := &fakeCommandDispatcher{}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpIdle), discardLogger()).
		WithPowerOffPause(1 * time.Millisecond)

	start := time.Now()
	err := apply.Apply(context.Background(), dto.SetPowerCommand{On: false})
	elapsed := time.Since(start)
	require.NoError(t, err)
	assert.Equal(t, []writeCall{{addr: 2, value: 1}, {addr: 2, value: 0}}, d.snapshotWrites())
	assert.GreaterOrEqual(t, elapsed, 1*time.Millisecond)
}

func TestApply_SetPower_Off_TransitionGuard_Refuses(t *testing.T) {
	t.Parallel()
	d := &fakeCommandDispatcher{}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpPreheatCalorifier), discardLogger())

	err := apply.Apply(context.Background(), dto.SetPowerCommand{On: false})
	require.Error(t, err)
	assert.ErrorIs(t, err, dto.ErrTransitionInProgress)
	assert.Empty(t, d.snapshotWrites())
}

func TestApply_SetPower_On_AllowedDuringTransition(t *testing.T) {
	t.Parallel()
	d := &fakeCommandDispatcher{}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpPreheatCalorifier), discardLogger())

	err := apply.Apply(context.Background(), dto.SetPowerCommand{On: true})
	require.NoError(t, err)
	assert.Equal(t, []writeCall{{addr: 2, value: 1}}, d.snapshotWrites())
}

func TestApply_SetPower_Off_FirstWriteFails_NoSecondWrite(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("modbus boom")
	d := &fakeCommandDispatcher{
		writeErrAtCall:    1,
		writeErrAtCallErr: sentinel,
	}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpIdle), discardLogger()).
		WithPowerOffPause(1 * time.Millisecond)

	err := apply.Apply(context.Background(), dto.SetPowerCommand{On: false})
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
	assert.Equal(t, 1, len(d.snapshotWrites()))
}

func TestApply_SetPower_Off_ContextCancelledDuringPause(t *testing.T) {
	t.Parallel()
	d := &fakeCommandDispatcher{}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpIdle), discardLogger()).
		WithPowerOffPause(100 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	err := apply.Apply(ctx, dto.SetPowerCommand{On: false})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, len(d.snapshotWrites()))
}

func TestApply_SetMode_RMWModifyFn(t *testing.T) {
	t.Parallel()
	d := &fakeCommandDispatcher{}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpIdle), discardLogger())

	err := apply.Apply(context.Background(), dto.SetModeCommand{Mode: valueobject.ModeHeat})
	require.NoError(t, err)
	mods := d.snapshotModifies()
	require.Len(t, mods, 1)
	assert.Equal(t, uint16(86), mods[0].addr)

	fn := mods[0].modify
	assert.Equal(t, uint16(0xC001), fn(uint16(0xC003)))
	assert.Equal(t, uint16(0x0001), fn(uint16(0x0000)))
	assert.Equal(t, uint16(0xFFFD), fn(uint16(0xFFFF)))
	assert.Empty(t, d.snapshotWrites())
}

func TestApply_SetMode_TransitionGuard(t *testing.T) {
	t.Parallel()
	d := &fakeCommandDispatcher{}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpStartFan), discardLogger())

	err := apply.Apply(context.Background(), dto.SetModeCommand{Mode: valueobject.ModeAuto})
	require.Error(t, err)
	assert.ErrorIs(t, err, dto.ErrTransitionInProgress)
	assert.Empty(t, d.snapshotModifies())
}

func TestApply_SetMode_DispatcherError_Propagates(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("modify boom")
	d := &fakeCommandDispatcher{modifyErr: sentinel}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpIdle), discardLogger())

	err := apply.Apply(context.Background(), dto.SetModeCommand{Mode: valueobject.ModeCool})
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
}

func TestApply_SetTemperature_WritesH31_RawValue(t *testing.T) {
	t.Parallel()
	d := &fakeCommandDispatcher{}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpIdle), discardLogger())

	err := apply.Apply(context.Background(), dto.SetTemperatureCommand{Temperature: mustTemp(t, 22.5)})
	require.NoError(t, err)
	assert.Equal(t, []writeCall{{addr: 31, value: 225}}, d.snapshotWrites())
}

func TestApply_SetTemperature_TransitionGuard(t *testing.T) {
	t.Parallel()
	d := &fakeCommandDispatcher{}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpOpenDamper), discardLogger())

	err := apply.Apply(context.Background(), dto.SetTemperatureCommand{Temperature: mustTemp(t, 21.0)})
	require.Error(t, err)
	assert.ErrorIs(t, err, dto.ErrTransitionInProgress)
	assert.Empty(t, d.snapshotWrites())
}

func TestApply_SetFan_WritesH32_Value(t *testing.T) {
	t.Parallel()
	d := &fakeCommandDispatcher{}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpIdle), discardLogger())

	err := apply.Apply(context.Background(), dto.SetFanCommand{FanSpeed: mustFan(t, 5)})
	require.NoError(t, err)
	assert.Equal(t, []writeCall{{addr: 32, value: 5}}, d.snapshotWrites())
}

func TestApply_SetFan_TransitionGuard(t *testing.T) {
	t.Parallel()
	d := &fakeCommandDispatcher{}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpRotorSpinup), discardLogger())

	err := apply.Apply(context.Background(), dto.SetFanCommand{FanSpeed: mustFan(t, 7)})
	require.Error(t, err)
	assert.ErrorIs(t, err, dto.ErrTransitionInProgress)
	assert.Empty(t, d.snapshotWrites())
}

func TestApply_DispatcherError_Propagates(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("write boom")
	d := &fakeCommandDispatcher{writeErr: sentinel}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpIdle), discardLogger())

	err := apply.Apply(context.Background(), dto.SetTemperatureCommand{Temperature: mustTemp(t, 20.0)})
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
}

func TestApply_LogsCommand(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	d := &fakeCommandDispatcher{}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpIdle), logger)

	err := apply.Apply(context.Background(), dto.SetTemperatureCommand{Temperature: mustTemp(t, 22.5)})
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "applying command")
	assert.Contains(t, out, "SetTemperature(22.5°C)")
}

func TestApply_LogsCommandFailure(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	d := &fakeCommandDispatcher{writeErr: errors.New("nope")}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpIdle), logger)

	err := apply.Apply(context.Background(), dto.SetFanCommand{FanSpeed: mustFan(t, 4)})
	require.Error(t, err)
	out := buf.String()
	assert.Contains(t, out, "command failed")
	assert.Contains(t, out, "SetFan(speed=4)")
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}
```

---

---

## Status: READY

Fix applied: `cmd.String()` replaced with `fmt.Stringer` type assertion. Story ready for re-implementation.

---

## Agent Execution

### Implementation Agent Instructions

```
Task tool call:
- subagent_type: "general-purpose"
- model: "haiku"
- prompt: |
    You are an IMPLEMENTATION AGENT for story 009.

    ABSOLUTE RULES:
    - Copy code BYTE-FOR-BYTE.
    - DO NOT modify code, fix imports, add `//nolint`, remove t.Parallel. STOP on failure.

    PROCESS:
    1. Read story.
    2. Write each "#### File: `path`" block.
    3. From work dir:
       a. gofmt -l application/usecase/   (must be empty)
       b. go build ./...
       c. go vet ./...
       d. go test -short ./...
       e. go test -race ./application/usecase/...
       f. coverage: go test -coverpkg=./application/usecase -coverprofile=/tmp/oasis_uc.out ./application/usecase/...
          → go tool cover -func=/tmp/oasis_uc.out | tail -1   ≥90%
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

- [x] `application/usecase/apply_command.go`
- [x] `application/usecase/apply_command_test.go`
- [x] gofmt clean
- [x] go build/vet pass
- [x] go test pass
- [x] go test -race pass
- [x] coverage ≥90%
- [x] lint clean

### Verification Results

```
gofmt:                       PASS
go build:                    PASS
go vet:                      PASS
go test:                     PASS
go test -race:               PASS
application/usecase cov:     97.0%
golangci-lint:               PASS
```

### Issues Found

(none)

### Fixes Applied

- Remove unused `set()` helper on fakeSnapshots — was declared but never called; lint `unused` would flag it.

---

## Files Changed

- `application/usecase/apply_command.go`
- `application/usecase/apply_command_test.go`
