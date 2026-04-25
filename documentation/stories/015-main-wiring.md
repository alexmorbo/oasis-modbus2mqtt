---
title: "Feature: cmd/server/main.go full wiring"
status: ready
priority: high
complexity: 6
planning_model: opus-4-5
implementation_model: haiku-4
created: 2026-04-25
updated: 2026-04-25
depends_on: 014-http-handlers
risk_areas: [shutdown-ordering, dependency-injection-correctness]
---

## Context

Story 015 — wiring всех компонентов в работающий сервис. Это интеграционная точка между всеми предыдущими stories.

**Структура** (extracted в `cmd/server/app.go` для тестируемости):
1. **`App` struct** — держит все зависимости после конструирования.
2. **`func NewApp(cfg, logger) (*App, error)`** — создаёт всё, кроме сетевых coннектов.
3. **`func (a *App) Run(ctx context.Context) error`** — запускает goroutines, делает initial Connects, ждёт ctx.Done(), graceful shutdown.
4. **`main.go`** — thin: load config, build logger, build app, set up signal handler, call Run.

**Wiring sequence** (стартовый порядок):
1. Build infrastructure components (Modbus client, MQTT client, TopicBuilder, Discovery Builder).
2. Build application services: CommandDispatcher (depends on Modbus client), ConnectionSupervisor (depends on Modbus client + dispatcher через FailureNotifier).
3. Build use cases: PollController, ApplyCommand, PublishDiscovery, PublishState.
4. Build orchestration: Poller (uses PollController), AvailabilityManager (uses Poller via SnapshotProvider).
5. Build interface: CommandSubscriber, HTTP handlers, Router, Server.
6. Wire callbacks:
   - `Poller.Subscribe(publishState.Apply)` — каждое poll update публикует.
   - `AvailabilityManager.SetCallbacks(onOnline, onOffline)` — опubliki availability.
   - **(дополнительно)** Subscribe firmware change → re-publish discovery. **Не делаем в этой story** — overkill для первой версии. Discovery публикуется один раз при старте.

**Startup order** (Run):
1. ConnectionSupervisor.Start(ctx) — synchronous initial Modbus connect (с retry).
2. MQTT.Connect(ctx) — synchronous initial MQTT connect.
3. Wait for first snapshot (опционально — proceed immediately, AvailabilityManager handle stale).
4. PublishDiscovery.Publish — synchronous, requires MQTT connected.
5. CommandSubscriber.Start.
6. Poller.Start (goroutines).
7. AvailabilityManager.Start (goroutine).
8. HTTP Server.Start (goroutine, через `errChan`).
9. Block on `<-ctx.Done()` или error from server.

**Shutdown order** (reverse):
1. HTTP Server.Shutdown(timeout 5s).
2. cancel app context → Poller goroutines exit, AvailabilityManager exits, ConnectionSupervisor exits.
3. Wait for Poller.Done(), AvailMgr.Done(), Supervisor.Done() — short timeout.
4. MQTT.Disconnect(quiesce=500ms) — graceful, publishes offline retained.
5. Modbus.ForceClose.
6. Final log "shutdown complete".

**Не делаем** в этой story:
- Re-publish discovery on firmware change.
- Pre-flight checks (config dry-run mode).
- Profiling endpoints (pprof).
- TLS на HTTP.

Reference: `/Users/alexmorbo/Home/homelab/.thoughts/oasis-syberia-modbus-bridge/04-mqtt-bridge-plan.md` секция 9 step 20.

## User Story

**As a** оператор oasis-modbus2mqtt,
**I want to** запускать сервис одной командой `./oasis-modbus2mqtt`, и быть уверенным, что всё стартует в правильном порядке + graceful shutdown по SIGTERM,
**So that** k8s rolling deploy не оставлял зависших соединений или published "online" в MQTT когда pod уже мёртв.

## Acceptance Criteria

### App

- [ ] `cmd/server/app.go`:
  - **Struct**:
    ```go
    type App struct {
        cfg        *config.Config
        logger     *slog.Logger

        modbusClient *infrabus.Client       // *infrastructure/modbus.Client
        mqttClient   *infmqtt.Client        // *infrastructure/mqtt.Client
        topics       *infmqtt.TopicBuilder
        builder      *discovery.Builder

        dispatcher   *service.CommandDispatcher
        supervisor   *service.ConnectionSupervisor
        poller       *service.Poller
        availMgr     *service.AvailabilityManager

        pollCtrl     *usecase.PollController
        applyCmd     *usecase.ApplyCommand
        pubDiscovery *usecase.PublishDiscovery
        pubState     *usecase.PublishState

        cmdSub       *intmqtt.CommandSubscriber  // interface/mqtt
        httpServer   *inthttp.Server              // interface/http
    }
    ```
  - Aliases (импорты):
    ```
    infrabus    "github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/modbus"
    infmqtt     "github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/mqtt"
    intmqtt     "github.com/alexmorbo/oasis-modbus2mqtt/interface/mqtt"
    inthttp     "github.com/alexmorbo/oasis-modbus2mqtt/interface/http"
    ```
  - `func NewApp(cfg *config.Config, logger *slog.Logger) (*App, error)`:
    - Build всё в правильном порядке. Никаких сетевых connect'ов.
    - Wire callbacks: Poller.Subscribe(publishState callback), AvailMgr.SetCallbacks.
    - PublishState callback wraps `pubState.Apply` с background ctx (timeout 5s) и логом ошибки (don't propagate — best effort).
    - AvailMgr onOnline / onOffline callbacks publish availability через `mqttClient.Publish(ctx, topics.Availability(), payload, retained=true)` с background ctx 5s.
    - Validate ничего — все panics уже в конструкторах.
    - Returns App + nil error.
  - `func (a *App) Run(ctx context.Context) error`:
    - **Initial Connects**:
      1. `if err := a.supervisor.Start(ctx); err != nil { return fmt.Errorf("modbus initial connect: %w", err) }`.
      2. `if err := a.mqttClient.Connect(ctx); err != nil { return fmt.Errorf("mqtt initial connect: %w", err) }`.
      3. `if err := a.pubDiscovery.Publish(ctx, "unknown"); err != nil { logger.Warn("initial discovery publish failed", "error", err) }` — non-fatal, retry на firmware update'е (которого мы не делаем — это ОК для v0.1).
      4. `if err := a.cmdSub.Start(ctx); err != nil { return fmt.Errorf("command subscriber start: %w", err) }`.
    - **Goroutines**:
      ```go
      a.poller.Start(ctx)       // returns immediately, goroutines run
      a.availMgr.Start(ctx)     // returns immediately
      ```
    - **HTTP server в goroutine с err channel**:
      ```go
      httpErr := make(chan error, 1)
      go func() {
          if err := a.httpServer.Start(); err != nil {
              httpErr <- err
          }
          close(httpErr)
      }()
      ```
    - **Wait for context done или http error**:
      ```go
      select {
      case <-ctx.Done():
          a.logger.Info("shutdown signal received")
      case err := <-httpErr:
          if err != nil {
              a.logger.Error("http server failed", "error", err)
              return err
          }
      }
      ```
    - **Shutdown sequence**:
      ```go
      shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
      defer cancel()
      
      // 1. HTTP server
      if err := a.httpServer.Shutdown(shutdownCtx); err != nil {
          a.logger.Warn("http shutdown failed", "error", err)
      }
      
      // 2. App ctx already cancelled — wait for goroutines
      select {
      case <-a.poller.Done():
      case <-time.After(5 * time.Second):
          a.logger.Warn("poller shutdown timeout")
      }
      select {
      case <-a.availMgr.Done():
      case <-time.After(2 * time.Second):
          a.logger.Warn("availability manager shutdown timeout")
      }
      
      // 3. MQTT disconnect (graceful, publishes offline)
      a.mqttClient.Disconnect(500 * time.Millisecond)
      
      // 4. Modbus close
      if err := a.modbusClient.ForceClose(); err != nil {
          a.logger.Warn("modbus close failed", "error", err)
      }
      
      a.logger.Info("shutdown complete")
      return nil
      ```

- [ ] `cmd/server/main.go`:
  - Tiny:
    ```go
    package main
    
    import (
        "context"
        "log/slog"
        "os"
        "os/signal"
        "syscall"
    
        "github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/config"
        "github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/logger"
        "github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/metrics"
    )
    
    func main() {
        cfg, err := config.Load()
        if err != nil {
            // Use stderr fallback (logger not built yet)
            slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("config load failed", "error", err)
            os.Exit(1)
        }
    
        log := logger.New(cfg.Logger)
        slog.SetDefault(log)
        log.Info("oasis-modbus2mqtt starting", "version", "0.1.0")
    
        metrics.Init()
    
        app, err := NewApp(cfg, log)
        if err != nil {
            log.Error("app build failed", "error", err)
            os.Exit(1)
        }
    
        ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
        defer cancel()
    
        if err := app.Run(ctx); err != nil {
            log.Error("app run failed", "error", err)
            os.Exit(1)
        }
        log.Info("oasis-modbus2mqtt exited cleanly")
    }
    ```

### Tests

- [ ] `cmd/server/app_test.go`:
  - **Smoke test**:
    - `TestNewApp_BuildsCleanly` — `NewApp(cfg, logger)` с минимально валидным cfg → returns non-nil App, nil error.
    - cfg для теста: всё дефолтное + override Modbus host на нерабочий (например `127.0.0.1:1` чтобы Connect failed) — но мы НЕ вызываем Run, только NewApp.
  - `TestRun_FailsOnModbusConnectFailure`:
    - Use cfg pointing к недоступному Modbus host (e.g., 127.0.0.1:1).
    - Reduce reconnect backoff to fast values (Min=1ms, Max=10ms) so failed initial connect aborts quickly.
    - Run with ctx that cancels after 100ms.
    - Assert error returned (either modbus-specific or context.Canceled).
  - `TestRun_GracefulShutdownOnContextCancel`:
    - НЕТ доступа к real Modbus/MQTT, поэтому нужны fakes. **Слишком сложно для simple test** — skip or использовать testcontainers (overkill для smoke test).
    - **Альтернатива**: Skip `TestRun_GracefulShutdownOnContextCancel`. Cover graceful shutdown в e2e (story 016).
  - **Не нужно**:
    - Mock injection — App is concrete struct. Tests с моками потребовали бы redesign.
  - All `t.Parallel()` (NewApp tests independent).

### Quality

- [ ] Coverage `cmd/server` ≥ 30% (low — main wiring трудно тестировать без integration).
- [ ] `go test -race` clean.
- [ ] `golangci-lint run ./...` PASS.
- [ ] `gofmt -l cmd/server/` empty.
- [ ] godoc one-liner на каждом exported.
- [ ] **Manual smoke test**: после `go build` бинарь стартует с дефолтным cfg, пытается connect (fails если нет controller/MQTT), graceful exit по Ctrl+C.

## Constraints

- Зависимости: только existing project packages. Никаких новых external.
- main.go НЕ должна содержать business logic — только load + signal handler + delegate.
- App.Run() — синхронный, возвращает после shutdown complete.
- Все configurable values — из config (не hardcoded в App).
- НЕ делать `os.Exit` в App — только в main. App возвращает error.
- Все timeouts в shutdown sequence configurable нет — hardcoded в коде (consistent с k8s preStop expectations).
- `signal.NotifyContext` (Go 1.16+) — используем вместо ручного signal handling.
- Goimports 3 группы.
- Aliases короткие и осмысленные.

### Goimports / lint guards

- 3 группы импортов.
- Aliases для conflict resolution: `intmqtt`, `infmqtt`, `inthttp`, `infrabus`.
- gosec: `time.Duration` arithmetic — no narrowing.
- НЕ использовать `init()`.

---

## Technical Specification

### Analysis

- `App` is built I/O-free in `NewApp` (panics from sub-constructors surface programming errors at boot); `Run` performs all network connects in deterministic order so failure modes are easy to attribute (Modbus first because supervisor blocks until first connect; MQTT second so LWT is registered before discovery; discovery best-effort; subscriber registers before poller starts to avoid lost commands during the first hot tick).
- Callback wiring is done inside `NewApp` so `Run` only deals with lifecycle. The `Poller.Subscribe` callback wraps `pubState.Apply` with a 5s background context — best-effort: state publish failure must not stall the next poll tier. Same pattern for online/offline availability publishes.
- `ConnectionSupervisor` doubles as `FailureNotifier` for `CommandDispatcher` via duck-typing on `Trigger()`; threshold of 3 matches the prior story 007 contract.
- Shutdown: HTTP first (5s drain), then cancel app ctx, wait Poller/AvailMgr `Done()` with bounded fallbacks (5s/2s), then MQTT graceful disconnect (publishes retained "offline"), finally Modbus `ForceClose`. All shutdown timeouts hardcoded to match k8s preStop expectations (10s grace).
- No firmware-change re-publish in v0.1 — discovery is published once at boot with `"unknown"` until first slow-tier poll arrives; next iteration adds re-publish when firmware changes.
- No TLS on HTTP — endpoint is k8s-internal only; mTLS at ingress layer if needed.
- Tests: `TestNewApp_BuildsCleanly` verifies wiring is panic-free with a valid cfg; `TestRun_FailsOnModbusConnectFailure` exercises the supervisor's initial-connect failure path against an unreachable port with sub-millisecond backoff so ctx-deadline arrives quickly. Both `t.Parallel()`. Coverage ≥30% comes from `NewApp` running every constructor.

### Implementation Order

1. `cmd/server/app.go`
2. `cmd/server/main.go`
3. `cmd/server/app_test.go`

---

### 1. Application wiring

#### File: `cmd/server/app.go`

```go
package main

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

// App is the wired application root. It owns every long-lived component
// constructed at boot and orchestrates startup and graceful shutdown via
// Run. Build it via NewApp; do not zero-initialise.
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

	pollCtrl     *usecase.PollController
	applyCmd     *usecase.ApplyCommand
	pubDiscovery *usecase.PublishDiscovery
	pubState     *usecase.PublishState

	cmdSub     *intmqtt.CommandSubscriber
	httpServer *inthttp.Server
}

// NewApp builds every component without performing I/O. Constructors panic
// on programming errors (nil arguments, invalid intervals); a successful
// return guarantees the wiring is valid. The returned App is ready for Run.
func NewApp(cfg *config.Config, logger *slog.Logger) (*App, error) {
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
		pollCtrl:     pollCtrl,
		applyCmd:     applyCmd,
		pubDiscovery: pubDiscovery,
		pubState:     pubState,
		cmdSub:       cmdSub,
		httpServer:   httpServer,
	}, nil
}

// Run boots the application: connects Modbus and MQTT, publishes initial HA
// discovery, starts the command subscriber, launches the poll/availability
// goroutines, and serves HTTP. It blocks until ctx is cancelled or the HTTP
// server exits with an error, then performs the graceful shutdown sequence
// before returning.
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

---

### 2. Main entrypoint

#### File: `cmd/server/main.go`

```go
package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/config"
	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/logger"
	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/metrics"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("config load failed", "error", err)
		os.Exit(1)
	}

	log := logger.New(cfg.Logger)
	slog.SetDefault(log)
	log.Info("oasis-modbus2mqtt starting", "version", "0.1.0")

	metrics.Init()

	app, err := NewApp(cfg, log)
	if err != nil {
		log.Error("app build failed", "error", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := app.Run(ctx); err != nil {
		log.Error("app run failed", "error", err)
		os.Exit(1)
	}
	log.Info("oasis-modbus2mqtt exited cleanly")
}
```

---

### 3. Tests

#### File: `cmd/server/app_test.go`

```go
package main

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/config"
)

func newTestConfig() *config.Config {
	return &config.Config{
		Modbus: config.ModbusConfig{
			Host:           "127.0.0.1",
			Port:           502,
			SlaveID:        1,
			ConnectTimeout: 5 * time.Second,
			ReadTimeout:    1 * time.Second,
			WriteTimeout:   1 * time.Second,
			GuardInterval:  100 * time.Millisecond,
		},
		MQTT: config.MQTTConfig{
			Broker:    "127.0.0.1:1883",
			ClientID:  "test",
			Keepalive: 30 * time.Second,
			QoS:       1,
		},
		HomeAssistant: config.HAConfig{
			DiscoveryPrefix: "homeassistant",
			DevicePrefix:    "oasis_test",
			DeviceName:      "Test",
			Manufacturer:    "TestMfr",
			Model:           "TestModel",
		},
		Polling: config.PollingConfig{
			HotInterval:           5 * time.Second,
			MediumInterval:        15 * time.Second,
			SlowInterval:          60 * time.Second,
			AvailabilityThreshold: 30 * time.Second,
		},
		HTTP: config.HTTPConfig{Port: 18080},
		Logger: config.LoggerConfig{Level: "info"},
		Reconnect: config.ReconnectConfig{
			MinDelay:  1 * time.Second,
			MaxDelay:  60 * time.Second,
			Factor:    2.0,
			JitterPct: 0.2,
		},
	}
}

func newDiscardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

// TestNewApp_BuildsCleanly verifies that NewApp wires every dependency
// without performing I/O and without panicking on a valid configuration.
func TestNewApp_BuildsCleanly(t *testing.T) {
	t.Parallel()

	cfg := newTestConfig()
	logger := newDiscardLogger()

	app, err := NewApp(cfg, logger)
	require.NoError(t, err)
	require.NotNil(t, app)
	require.NotNil(t, app.modbusClient)
	require.NotNil(t, app.mqttClient)
	require.NotNil(t, app.topics)
	require.NotNil(t, app.builder)
	require.NotNil(t, app.dispatcher)
	require.NotNil(t, app.supervisor)
	require.NotNil(t, app.poller)
	require.NotNil(t, app.availMgr)
	require.NotNil(t, app.pollCtrl)
	require.NotNil(t, app.applyCmd)
	require.NotNil(t, app.pubDiscovery)
	require.NotNil(t, app.pubState)
	require.NotNil(t, app.cmdSub)
	require.NotNil(t, app.httpServer)
}

// TestNewApp_NilConfigReturnsError verifies that NewApp rejects a nil
// configuration with a descriptive error rather than panicking.
func TestNewApp_NilConfigReturnsError(t *testing.T) {
	t.Parallel()

	app, err := NewApp(nil, newDiscardLogger())
	require.Error(t, err)
	require.Nil(t, app)
}

// TestRun_FailsOnModbusConnectFailure boots the App against an unreachable
// Modbus host (port 1) and verifies that Run returns an error before the
// short test deadline elapses, as the supervisor's initial connect cannot
// succeed.
func TestRun_FailsOnModbusConnectFailure(t *testing.T) {
	t.Parallel()

	cfg := newTestConfig()
	cfg.Modbus.Host = "127.0.0.1"
	cfg.Modbus.Port = 1
	cfg.Modbus.ConnectTimeout = 50 * time.Millisecond
	cfg.Reconnect.MinDelay = 1 * time.Millisecond
	cfg.Reconnect.MaxDelay = 5 * time.Millisecond
	cfg.Reconnect.Factor = 2.0
	cfg.Reconnect.JitterPct = 0
	cfg.HTTP.Port = 18081

	app, err := NewApp(cfg, newDiscardLogger())
	require.NoError(t, err)
	require.NotNil(t, app)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	runErr := app.Run(ctx)
	assert.Error(t, runErr)
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
    You are an IMPLEMENTATION AGENT for story 015.

    ABSOLUTE RULES:
    - Copy code BYTE-FOR-BYTE.
    - DO NOT modify, fix, add `//nolint`. STOP on failure.

    PROCESS:
    1. Read story.
    2. Write each "#### File: `path`" block.
    3. From work dir:
       a. gofmt -l cmd/server/   (empty)
       b. go build ./...
       c. go vet ./...
       d. go test -short ./...
       e. go test -race ./cmd/server/...
       f. coverage: go test -coverpkg=./cmd/server -coverprofile=/tmp/oasis_cmd.out ./cmd/server/...
          → go tool cover -func=/tmp/oasis_cmd.out | tail -1   ≥30%
       g. golangci-lint run ./...   (0 issues)
       h. (manual) ./bin/oasis-modbus2mqtt — НЕ делаем в Implementation Agent (требует Modbus/MQTT). Smoke test = что бинарь собирается.
    4. PASS → status review.
    5. FAIL → append errors verbatim to Issues Found, status in_progress, STOP.

    Work dir: /Users/alexmorbo/Home/homelab/apps/oasis-modbus2mqtt/
```

### Fix Agent Instructions

Standard.

---

## Implementation Notes

### Progress

- [x] `cmd/server/app.go` (+ test)
- [x] `cmd/server/main.go` (replaces stub from story 001)
- [x] gofmt clean
- [x] go build/vet pass
- [x] go test pass
- [x] -race pass
- [x] coverage ≥30%
- [x] lint clean

### Verification Results

```
gofmt:                  PASS
go build:               PASS
go vet:                 PASS
go test:                PASS
go test -race:          PASS
cmd/server cov:         31.2%
golangci-lint:          PASS
```

### Issues Found

(none)

### Fixes Applied

Sync app.go/main.go/app_test.go after gofmt auto-fixes during initial implementation.

---

## Files Changed

- `apps/oasis-modbus2mqtt/cmd/server/app.go` (new)
- `apps/oasis-modbus2mqtt/cmd/server/main.go` (replaces stub from story 001)
- `apps/oasis-modbus2mqtt/cmd/server/app_test.go` (new)
