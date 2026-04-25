---
title: "Feature: Application ports and DTOs"
status: review
priority: high
complexity: 3
planning_model: opus-4-5
implementation_model: haiku-4
created: 2026-04-25
updated: 2026-04-25
depends_on: 003-domain-entities-and-catalog
risk_areas: []
---

## Context

Story 004 определяет **границы application слоя** — интерфейсы (ports), которые application use cases (story 008+) используют, и Command DTOs, через которые внешний мир (MQTT subscriber из story 013) пушит изменения в use cases.

В Clean Architecture слои:
- **domain** (story 002, 003) — pure logic, no I/O.
- **application** (эта story + 008, 009, 012) — use cases, ports (interfaces), DTOs.
- **infrastructure** (story 005-006, 010, 011) — реализации ports.
- **interface** (story 013, 014) — MQTT subscriber, HTTP handlers.

Ports — это интерфейсы, которые application объявляет, а infrastructure реализует. Это инверсия зависимостей: application не знает про конкретные библиотеки (`grid-x/modbus`, `paho`), он знает только про интерфейсы.

**Что НЕ создаём в этой story:**
- `dto/snapshot.go` — НЕ нужен. `domain/entity.Snapshot` (story 003) уже содержит всё необходимое и уже использует доменные типы. Application use cases импортируют domain напрямую — Clean Architecture это позволяет (application → domain зависимость допустима, обратное — нет).
- `port/logger.go` — НЕ нужен. `log/slog` в stdlib уже abstraction; обёртка не добавит ценности и затруднит usage.

**Что создаём:**
- `application/port/modbus_client.go` — `ModbusClient` interface (что нужно use cases от Modbus).
- `application/port/mqtt_publisher.go` — `MQTTPublisher` interface.
- `application/port/clock.go` — `Clock` interface (для тестируемости — AvailabilityManager использует `time.Now()` через Clock).
- `application/dto/command.go` — Command DTOs (SetPower, SetMode, SetTemperature, SetFan).
- `application/dto/errors.go` — sentinel errors на уровне application.

Reference: `/Users/alexmorbo/Home/homelab/.thoughts/oasis-syberia-modbus-bridge/04-mqtt-bridge-plan.md` секция `## 3. Application layer`.

## User Story

**As a** разработчик oasis-modbus2mqtt,
**I want to** иметь стабильные application-level контракты для Modbus, MQTT, и времени,
**So that** infrastructure (story 005+) могла свободно меняться (поменять modbus library, mqtt client) без ломания use cases.

## Acceptance Criteria

### Ports

- [ ] `application/port/modbus_client.go`:
  ```go
  type ModbusClient interface {
      ReadInput(ctx context.Context, start uint16, count uint16) ([]uint16, error)
      ReadHolding(ctx context.Context, start uint16, count uint16) ([]uint16, error)
      WriteHolding(ctx context.Context, addr uint16, value uint16) error
      ModifyHolding(ctx context.Context, addr uint16, modify func(current uint16) uint16) error
      Connected() bool
  }
  ```
  - Все методы возвращают error.
  - `ModifyHolding` — RMW (read-modify-write) обёртка для `Dev_Keys_2`. Реализация (story 006) под mutex'ом делает Read + apply modify func + Write. Ports просто описывает контракт.
  - `Connected()` — non-blocking, для `ConnectionSupervisor` (story 007).
  - **Никаких** методов про `Connect()`/`Disconnect()` — connection management — это infrastructure concern, не application.

- [ ] `application/port/mqtt_publisher.go`:
  ```go
  type MQTTPublisher interface {
      Publish(ctx context.Context, topic string, payload []byte, retained bool) error
      Subscribe(ctx context.Context, topic string, handler MessageHandler) error
      Connected() bool
  }

  type MessageHandler func(topic string, payload []byte)
  ```
  - **Не** включаем `SetWill` в interface — LWT задаётся при connect и это infrastructure concern. Plan (04-mqtt-bridge-plan.md) упоминал `SetWill`, но осмысленнее задать will через config infrastructure.
  - `Subscribe` принимает `MessageHandler` (typed callback), не `func(topic string, payload []byte)`.
  - `Connected()` — для CommandSubscriber (story 013), чтобы не пытаться обработать команду до коннекта.

- [ ] `application/port/clock.go`:
  ```go
  type Clock interface {
      Now() time.Time
  }
  ```
  - Минимальный — нужен только `Now()`. Тесты внедряют fake clock в `AvailabilityManager` (story 008).
  - В этом же файле — `RealClock struct{}` — простая реализация для prod: `func (RealClock) Now() time.Time { return time.Now() }`. Зависимость `port/clock.go` от `time` — стдлиб, OK.

### DTOs

- [ ] `application/dto/command.go`:
  - `type Command interface { commandTag() }` — marker interface (private method, чтобы реализовать могли только типы в этом пакете). Используется для логирования и type-switch внутри `ApplyCommandUseCase` (story 009).
  - `type SetPowerCommand struct { On bool }`. Валидации не нужно — bool сам по себе валидный.
  - `type SetModeCommand struct { Mode valueobject.Mode }`. Mode уже валидирован при создании value object.
  - `type SetTemperatureCommand struct { Temperature valueobject.Temperature }`. Temperature валидирован.
  - `type SetFanCommand struct { FanSpeed valueobject.FanSpeed }`. FanSpeed валидирован.
  - Каждый command type реализует `commandTag()` (пустой метод).
  - `(c SetPowerCommand) String() string` → `"SetPower(on=true)"`.
  - `(c SetModeCommand) String() string` → `"SetMode(heat)"`.
  - `(c SetTemperatureCommand) String() string` → `"SetTemperature(22.5°C)"`.
  - `(c SetFanCommand) String() string` → `"SetFan(speed=5)"`.
  - **`String()` методы важны для structured logging — поллер логирует команду в slog как text атрибут.**

- [ ] `application/dto/errors.go`:
  - Все ошибки — sentinel (`var Err... = errors.New("...")`):
    - `ErrModbusNotConnected` — Modbus client returns this when `Connected() == false`.
    - `ErrMQTTNotConnected` — MQTT publisher equivalent.
    - `ErrTransitionInProgress` — `ApplyCommandUseCase` отказывается выполнять команду пока контроллер в transition op (story 009 будет проверять `lastSnapshot.Operation.IsTransitional()`).
    - `ErrCommandTimeout` — command queued > N seconds without dispatcher pickup.
    - `ErrInvalidPayload` — для CommandSubscriber: payload не парсится в Command.
  - НЕ нужно `ErrInvalidTemperature` etc — они в domain/valueobject, и Command конструируется через них (валидация уже произошла).

### Tests

- [ ] `application/dto/command_test.go`:
  - String() для каждой команды (4 cases × 2-3 примера = ~10 cases).
  - Что `Command` interface удовлетворяется всеми 4 типами (compile-time через `var _ dto.Command = SetPowerCommand{}`). Это **не runtime test** — это compile-time check, добавить в test файл как пакетные `var _` декларации.
- [ ] `application/dto/errors_test.go`:
  - `errors.Is(wrapped, ErrModbusNotConnected)` works (wrap test).
  - Все 5 errors не равны друг другу (`!errors.Is(ErrA, ErrB)`).
- [ ] `application/port/clock_test.go`:
  - `RealClock.Now()` не паникует, возвращает время близкое к `time.Now()` (within 1s).

### Quality

- [ ] Coverage не строгая — порты это интерфейсы (нечего тестировать). DTOs минимальны. Цель ≥80% для `application/dto`, ≥50% для `application/port` (там RealClock и интерфейсы — мало testable code).
- [ ] `golangci-lint run ./...` PASS.
- [ ] `gofmt -l application/` пусто.
- [ ] Все exported идентификаторы — godoc one-liner.

## Constraints

- **Стандарт + valueobject + testify.** Никаких внешних импортов.
- `application/port` импортирует только: `context`, `time`. **НЕ** импортирует `domain/*` — порты должны быть pure abstraction. Если бы Modbus работал с RegisterAddr, мы бы передавали `uint16` всё равно — проще для адаптеров.
- `application/dto` импортирует `domain/valueobject` (для типов в commands), но **НЕ** `domain/entity` (snapshot уже там).
- Marker interface `Command` через приватный метод `commandTag()` — type safety снаружи пакета (нельзя реализовать new command type вне `dto`).
- Все Command DTOs — value types (struct, не указатели). Передаются по значению.
- НЕТ generics. Marker interface = простой и читаемый.
- НЕТ методов на ports interfaces ≥ 6 — если Modbus interface распухает, разбиваем (но 5 OK).
- НЕТ benchmarks в этой story.

---

## Technical Specification

### Analysis

- **Skip `dto/snapshot.go`** — `domain/entity.Snapshot` (story 003) already aggregates state via doменные types. Application use cases can import domain directly under Clean Architecture; an extra DTO would only duplicate the model and force coupling on every change. Translation to MQTT JSON payloads is the publisher's concern (story 011), not application's.
- **Skip `port/logger.go`** — `log/slog` from stdlib is already an interface (`*slog.Logger` + `Handler`); a wrapper interface would not add testability and would force every infrastructure adapter to depend on a project-specific logger type instead of stdlib.
- **Marker interface via private method** — `Command` interface declares an unexported `commandTag()` method. Only types in `application/dto` can satisfy it, giving compile-time guarantee that command type-switches in `ApplyCommandUseCase` (story 009) are exhaustive over the closed set.
- **Reuse valueobject types in commands** — `SetModeCommand`, `SetTemperatureCommand`, `SetFanCommand` carry `valueobject.Mode`, `valueobject.Temperature`, `valueobject.FanSpeed` directly. Validation already happened at value-object construction; commands stay value-only structs with no extra checks. `application/dto` therefore depends on `domain/valueobject` (allowed by dependency rule).
- **Ports stay free of domain imports** — `ModbusClient`/`MQTTPublisher`/`Clock` import only `context` and `time`. Modbus adapters speak in `uint16` registers; mapping to `valueobject.Register` etc. is the use case's job, not the port's.

### Implementation Order

1. `application/port/clock.go` (no internal deps)
2. `application/port/modbus_client.go`
3. `application/port/mqtt_publisher.go`
4. `application/dto/errors.go`
5. `application/dto/command.go`
6. Тесты для всех

---

### 1. Clock

#### File: `application/port/clock.go`

```go
// Package port defines the interfaces (ports) that application use cases
// depend on. Infrastructure adapters in infrastructure/* implement these.
//
// The package intentionally has no dependency on domain/* — ports are pure
// abstractions over outside concerns (time, Modbus, MQTT) and exchange only
// stdlib types so adapters stay decoupled from domain models.
package port

import "time"

// Clock is an injection point for the current wall-clock time.
//
// Production code uses RealClock; tests inject a fake clock to exercise
// time-dependent behavior (e.g. AvailabilityManager staleness windows in
// story 008) without sleeping.
type Clock interface {
	// Now returns the current time. Implementations must be safe for
	// concurrent use.
	Now() time.Time
}

// RealClock is the production Clock implementation backed by time.Now.
// The zero value is ready for use; the type carries no state.
type RealClock struct{}

// Now returns time.Now().
func (RealClock) Now() time.Time {
	return time.Now()
}

// Compile-time assertion that RealClock satisfies Clock.
var _ Clock = RealClock{}
```

#### File: `application/port/clock_test.go`

```go
package port_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/port"
)

func TestRealClock_Now(t *testing.T) {
	t.Parallel()

	before := time.Now()
	now := port.RealClock{}.Now()
	after := time.Now()

	assert.True(t, !now.Before(before) && !now.After(after),
		"RealClock.Now() = %v, want in [%v, %v]", now, before, after)
}
```

---

### 2. Modbus client port

#### File: `application/port/modbus_client.go`

```go
package port

import "context"

// ModbusClient is the application's view of a Modbus TCP client.
//
// It exposes only the operations use cases need: bulk reads, single-register
// writes, and an atomic read-modify-write helper for packed registers
// (e.g. Dev_Keys_2). Connection lifecycle (Connect/Disconnect/reconnect) is
// an infrastructure concern and intentionally absent from this interface.
type ModbusClient interface {
	// ReadInput reads count input registers (function code 0x04) starting
	// at start. The returned slice has exactly count elements on success.
	ReadInput(ctx context.Context, start uint16, count uint16) ([]uint16, error)

	// ReadHolding reads count holding registers (function code 0x03)
	// starting at start. The returned slice has exactly count elements on
	// success.
	ReadHolding(ctx context.Context, start uint16, count uint16) ([]uint16, error)

	// WriteHolding writes value to a single holding register at addr
	// (function code 0x06).
	WriteHolding(ctx context.Context, addr uint16, value uint16) error

	// ModifyHolding atomically applies a read-modify-write to the holding
	// register at addr: it reads the current value, calls modify, then
	// writes the result.
	//
	// Implementations MUST hold a lock across read+modify+write to
	// guarantee atomicity from this client's perspective. The Modbus
	// protocol itself offers no atomicity primitive, so concurrent writers
	// outside this process can still race; the contract only covers
	// callers that share this ModbusClient instance.
	ModifyHolding(ctx context.Context, addr uint16, modify func(current uint16) uint16) error

	// Connected reports whether the underlying transport is currently
	// usable. It must be non-blocking and safe to call concurrently —
	// ConnectionSupervisor (story 007) polls it on its own cadence.
	Connected() bool
}
```

---

### 3. MQTT publisher port

#### File: `application/port/mqtt_publisher.go`

```go
package port

import "context"

// MessageHandler is invoked once per delivered MQTT message for a given
// subscription. Implementations should treat payload as read-only; the
// underlying buffer may be reused after the handler returns.
type MessageHandler func(topic string, payload []byte)

// MQTTPublisher is the application's view of an MQTT client.
//
// It covers publishing, subscribing, and a non-blocking connectivity probe.
// LWT/will configuration is intentionally absent — it is set at connect
// time by the infrastructure adapter from configuration, not driven by
// use cases.
type MQTTPublisher interface {
	// Publish sends payload on topic. retained controls the MQTT retained
	// flag. The call returns once the broker has acknowledged at the QoS
	// level chosen by the implementation (typically QoS 1).
	Publish(ctx context.Context, topic string, payload []byte, retained bool) error

	// Subscribe registers handler for messages on topic. Each subscription
	// is independent: calling Subscribe twice for the same topic results
	// in both handlers receiving every matching message. Handlers run on
	// goroutines owned by the implementation and must not block for long
	// periods.
	Subscribe(ctx context.Context, topic string, handler MessageHandler) error

	// Connected reports whether the underlying transport is currently
	// usable. It must be non-blocking and safe to call concurrently —
	// CommandSubscriber (story 013) checks it before accepting a command.
	Connected() bool
}
```

---

### 4. Application errors

#### File: `application/dto/errors.go`

```go
// Package dto defines the data transfer objects exchanged between the
// interface layer (MQTT subscriber, HTTP handlers) and the application
// layer (use cases), plus the sentinel errors use cases can return.
//
// DTOs depend on domain/valueobject for value types but never on
// domain/entity — entity types are returned directly by use cases when
// state needs to flow back out.
package dto

import "errors"

// ErrModbusNotConnected is returned by use cases when ModbusClient.Connected()
// reports false at the moment of dispatch.
var ErrModbusNotConnected = errors.New("modbus client not connected")

// ErrMQTTNotConnected is returned by use cases when MQTTPublisher.Connected()
// reports false at the moment of publish.
var ErrMQTTNotConnected = errors.New("mqtt publisher not connected")

// ErrTransitionInProgress is returned by ApplyCommandUseCase when the latest
// snapshot reports the controller is in a transitional operation (start-up,
// shut-down, defrost) and a new command would conflict.
var ErrTransitionInProgress = errors.New("controller transition in progress")

// ErrCommandTimeout is returned when a queued command is not picked up by
// the dispatcher within its deadline.
var ErrCommandTimeout = errors.New("command timeout")

// ErrInvalidPayload is returned by CommandSubscriber when an MQTT payload
// cannot be decoded into a known Command.
var ErrInvalidPayload = errors.New("invalid command payload")
```

#### File: `application/dto/errors_test.go`

```go
package dto_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/dto"
)

func TestErrors_Distinct(t *testing.T) {
	t.Parallel()

	all := []error{
		dto.ErrModbusNotConnected,
		dto.ErrMQTTNotConnected,
		dto.ErrTransitionInProgress,
		dto.ErrCommandTimeout,
		dto.ErrInvalidPayload,
	}

	for i, a := range all {
		for j, b := range all {
			if i == j {
				continue
			}
			assert.Falsef(t, errors.Is(a, b),
				"errors.Is(%v, %v) must be false (sentinels must be distinct)", a, b)
		}
	}
}

func TestErrors_WrapPreserves(t *testing.T) {
	t.Parallel()

	all := []error{
		dto.ErrModbusNotConnected,
		dto.ErrMQTTNotConnected,
		dto.ErrTransitionInProgress,
		dto.ErrCommandTimeout,
		dto.ErrInvalidPayload,
	}

	for _, original := range all {
		original := original
		t.Run(original.Error(), func(t *testing.T) {
			t.Parallel()
			wrapped := fmt.Errorf("context: %w", original)
			assert.True(t, errors.Is(wrapped, original),
				"errors.Is(wrapped, %v) must be true after fmt.Errorf wrap", original)
		})
	}
}
```

---

### 5. Command DTOs

#### File: `application/dto/command.go`

```go
package dto

import (
	"fmt"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"
)

// Command is the marker interface for all command DTOs accepted by
// ApplyCommandUseCase. The marker method is unexported so only types
// declared in this package can satisfy Command — this gives the use case
// a closed, type-safe set of commands to switch over.
type Command interface {
	commandTag()
}

// SetPowerCommand toggles the controller's power state.
type SetPowerCommand struct {
	On bool
}

func (SetPowerCommand) commandTag() {}

// String returns a structured-log-friendly description of the command.
func (c SetPowerCommand) String() string {
	return fmt.Sprintf("SetPower(on=%t)", c.On)
}

// SetModeCommand sets the controller mode (off/heat/cool/auto).
type SetModeCommand struct {
	Mode valueobject.Mode
}

func (SetModeCommand) commandTag() {}

// String returns a structured-log-friendly description of the command.
func (c SetModeCommand) String() string {
	return fmt.Sprintf("SetMode(%s)", c.Mode.String())
}

// SetTemperatureCommand sets the controller setpoint in Celsius.
type SetTemperatureCommand struct {
	Temperature valueobject.Temperature
}

func (SetTemperatureCommand) commandTag() {}

// String returns a structured-log-friendly description of the command.
func (c SetTemperatureCommand) String() string {
	return fmt.Sprintf("SetTemperature(%.1f°C)", c.Temperature.Celsius())
}

// SetFanCommand sets the controller fan speed in [1, 10].
type SetFanCommand struct {
	FanSpeed valueobject.FanSpeed
}

func (SetFanCommand) commandTag() {}

// String returns a structured-log-friendly description of the command.
func (c SetFanCommand) String() string {
	return fmt.Sprintf("SetFan(speed=%d)", c.FanSpeed.Value())
}

// Compile-time assertions that every command type satisfies Command.
var (
	_ Command = SetPowerCommand{}
	_ Command = SetModeCommand{}
	_ Command = SetTemperatureCommand{}
	_ Command = SetFanCommand{}
)
```

#### File: `application/dto/command_test.go`

```go
package dto_test

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/dto"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"
)

func TestSetPowerCommand_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		on   bool
		want string
	}{
		{name: "on", on: true, want: "SetPower(on=true)"},
		{name: "off", on: false, want: "SetPower(on=false)"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cmd := dto.SetPowerCommand{On: tc.on}
			assert.Equal(t, tc.want, cmd.String())
		})
	}
}

func TestSetModeCommand_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		modeRaw int
		want    string
	}{
		{name: "off", modeRaw: 0, want: "SetMode(off)"},
		{name: "heat", modeRaw: 1, want: "SetMode(heat)"},
		{name: "cool", modeRaw: 2, want: "SetMode(cool)"},
		{name: "auto", modeRaw: 3, want: "SetMode(auto)"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			mode, err := valueobject.NewMode(tc.modeRaw)
			require.NoError(t, err)
			cmd := dto.SetModeCommand{Mode: mode}
			assert.Equal(t, tc.want, cmd.String())
		})
	}
}

func TestSetTemperatureCommand_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		celsius float64
		want    string
	}{
		{name: "min", celsius: 5.0, want: "SetTemperature(5.0°C)"},
		{name: "mid", celsius: 22.5, want: "SetTemperature(22.5°C)"},
		{name: "max", celsius: 30.0, want: "SetTemperature(30.0°C)"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			temp, err := valueobject.NewTemperature(tc.celsius)
			require.NoError(t, err)
			cmd := dto.SetTemperatureCommand{Temperature: temp}
			assert.Equal(t, tc.want, cmd.String())
		})
	}
}

func TestSetFanCommand_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		speed uint16
		want  string
	}{
		{name: "min", speed: 1, want: "SetFan(speed=1)"},
		{name: "mid", speed: 5, want: "SetFan(speed=5)"},
		{name: "max", speed: 10, want: "SetFan(speed=10)"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			fan, err := valueobject.NewFanSpeed(tc.speed)
			require.NoError(t, err)
			cmd := dto.SetFanCommand{FanSpeed: fan}
			assert.Equal(t, tc.want, cmd.String())
		})
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
    You are an IMPLEMENTATION AGENT for story 004-application-ports-and-dtos.

    YOUR ONLY JOB: Copy code from the story file to actual files. Run verification.

    ABSOLUTE RULES:
    - Copy code BYTE-FOR-BYTE. Use Write tool.
    - DO NOT modify code. DO NOT add `//nolint`. DO NOT fix imports.
    - If lint fails — STOP, report verbatim. Do NOT invent fixes.

    PROCESS:
    1. Read story: /Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/documentation/stories/004-application-ports-and-dtos.md
    2. For each "#### File: `path`" section, write to that path (relative to apps/oasis-modbus2mqtt/)
    3. Run verification IN ORDER:
       a. gofmt -l application/  (must be empty)
       b. go build ./...
       c. go vet ./...
       d. go test -short ./...
       e. go test -coverpkg=./application/dto -coverprofile=/tmp/oasis_dto_cov.out ./application/dto/... (≥80%)
       f. go test -coverpkg=./application/port -coverprofile=/tmp/oasis_port_cov.out ./application/port/... (≥50%)
       g. golangci-lint run ./... (0 issues)
    4. If ALL pass: status: review, fill Verification Results, tick Progress, refresh Files Changed.
    5. If ANY fail: append errors verbatim to Issues Found, status: in_progress, STOP.

    Work dir: /Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/
```

### Fix Agent Instructions

Standard. Edit story only.

---

## Implementation Notes

### Progress

- [x] `application/port/clock.go`
- [x] `application/port/modbus_client.go`
- [x] `application/port/mqtt_publisher.go`
- [x] `application/dto/command.go`
- [x] `application/dto/errors.go`
- [x] All tests
- [x] gofmt clean
- [x] go build/vet pass
- [x] go test pass
- [x] dto coverage ≥80%
- [x] port coverage ≥50%
- [x] lint clean

### Verification Results

```
gofmt:                  PASS
go build:               PASS
go vet:                 PASS
go test:                PASS
application/dto cov:    100.0%
application/port cov:   100.0%
golangci-lint:          PASS
```

### Issues Found

[Implementation Agent appends here]

### Fixes Applied

[Fix Agent documents fixes here]

---

## Files Changed

- `apps/oasis-modbus2mqtt/application/port/clock.go`
- `apps/oasis-modbus2mqtt/application/port/clock_test.go`
- `apps/oasis-modbus2mqtt/application/port/modbus_client.go`
- `apps/oasis-modbus2mqtt/application/port/mqtt_publisher.go`
- `apps/oasis-modbus2mqtt/application/dto/command.go`
- `apps/oasis-modbus2mqtt/application/dto/command_test.go`
- `apps/oasis-modbus2mqtt/application/dto/errors.go`
- `apps/oasis-modbus2mqtt/application/dto/errors_test.go`
