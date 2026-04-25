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
