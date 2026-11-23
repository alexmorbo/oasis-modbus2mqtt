package mqtt

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/dto"
	"github.com/alexmorbo/oasis-modbus2mqtt/application/port"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/catalog"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"
)

// handlerTimeout bounds the time a single queued command job may spend
// applying its commands. Each job gets its own context, rooted in the Start
// context so shutdown cancels an in-flight command.
const handlerTimeout = 10 * time.Second

// commandQueueSize is the number of parsed commands that may wait for the
// worker. A burst beyond it is dropped with a WARN instead of blocking the
// MQTT client's router goroutine.
const commandQueueSize = 32

// CommandApplier executes commands. Implemented by *usecase.ApplyCommand.
type CommandApplier interface {
	Apply(ctx context.Context, cmd dto.Command) error
}

// Subscriber is the subset of the MQTT publisher API used by this package.
type Subscriber interface {
	Subscribe(ctx context.Context, topic string, handler port.MessageHandler) error
}

// TopicProvider supplies command topics by HA object_id.
type TopicProvider interface {
	Command(objectID string) string
}

// commandJob is one parsed HA message. run applies its commands in order; a
// multi-command message (hvac_mode heat) is a single job so its
// short-circuit semantics hold.
type commandJob struct {
	topic string
	run   func(ctx context.Context)
}

// CommandSubscriber wires HA command topics to the bridge's command applier.
//
// The MQTT client runs handlers inline on its router goroutine, which also
// processes PUBACKs. Applying a command publishes the full retained state
// set and waits for each PUBACK, so applying on the router would stall every
// publish in the process. Handlers therefore only parse and enqueue; one
// worker goroutine started by Start drains the FIFO, so commands apply in
// arrival order.
type CommandSubscriber struct {
	sub     Subscriber
	applier CommandApplier
	topics  TopicProvider
	logger  *slog.Logger

	jobs      chan commandJob
	done      chan struct{}
	startOnce sync.Once
}

// NewCommandSubscriber constructs a CommandSubscriber. A nil sub, applier, or
// topics is a programming error and panics. A nil logger falls back to
// slog.Default.
func NewCommandSubscriber(sub Subscriber, applier CommandApplier, topics TopicProvider, logger *slog.Logger) *CommandSubscriber {
	if sub == nil {
		panic("command subscriber: subscriber must not be nil")
	}
	if applier == nil {
		panic("command subscriber: applier must not be nil")
	}
	if topics == nil {
		panic("command subscriber: topics must not be nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &CommandSubscriber{
		sub:     sub,
		applier: applier,
		topics:  topics,
		logger:  logger,
		jobs:    make(chan commandJob, commandQueueSize),
		done:    make(chan struct{}),
	}
}

// Start launches the command worker and registers handlers for every HA
// command topic, including one per catalog.Settings row. The MQTT client
// restores them on every reconnect. The worker exits when ctx is cancelled;
// Done reports that. Start must be called once; later calls return an error.
// It returns the first Subscribe error wrapped with the offending topic.
func (c *CommandSubscriber) Start(ctx context.Context) error {
	started := false
	c.startOnce.Do(func() {
		started = true
		go c.worker(ctx)
	})
	if !started {
		return errors.New("command subscriber already started")
	}

	subscriptions := []struct {
		objectID string
		handler  port.MessageHandler
	}{
		{"power", c.handlePower},
		{"hvac_mode", c.handleHVACMode},
		{"target_temperature", c.handleTargetTemperature},
		{"fan_target", c.handleFanTarget},
	}
	for _, def := range catalog.Settings {
		subscriptions = append(subscriptions, struct {
			objectID string
			handler  port.MessageHandler
		}{def.ObjectID, c.settingHandler(def.ObjectID)})
	}
	for _, s := range subscriptions {
		topic := c.topics.Command(s.objectID)
		if err := c.sub.Subscribe(ctx, topic, s.handler); err != nil {
			return fmt.Errorf("subscribe %s: %w", topic, err)
		}
	}
	return nil
}

// Done is closed once the worker started by Start has exited.
func (c *CommandSubscriber) Done() <-chan struct{} {
	return c.done
}

// worker applies queued jobs one at a time, in arrival order, each under its
// own handlerTimeout context. Jobs still queued at cancellation are dropped.
func (c *CommandSubscriber) worker(ctx context.Context) {
	defer close(c.done)
	for {
		select {
		case <-ctx.Done():
			return
		case job := <-c.jobs:
			if ctx.Err() != nil {
				return
			}
			jobCtx, cancel := context.WithTimeout(ctx, handlerTimeout)
			job.run(jobCtx)
			cancel()
		}
	}
}

// enqueue hands a parsed job to the worker without blocking. A full queue or
// a stopped worker drops the job.
func (c *CommandSubscriber) enqueue(topic string, run func(ctx context.Context)) {
	select {
	case <-c.done:
		c.logger.Warn("command dropped: subscriber stopped", "topic", topic)
		return
	default:
	}
	select {
	case c.jobs <- commandJob{topic: topic, run: run}:
	default:
		c.logger.Warn("command queue full, command dropped", "topic", topic, "capacity", commandQueueSize)
	}
}

// handlePower parses ON/OFF (case-insensitive) and queues a SetPowerCommand.
func (c *CommandSubscriber) handlePower(topic string, payload []byte) {
	raw := string(payload)
	switch {
	case strings.EqualFold(raw, "ON"):
		c.enqueueCommand(topic, dto.SetPowerCommand{On: true})
	case strings.EqualFold(raw, "OFF"):
		c.enqueueCommand(topic, dto.SetPowerCommand{On: false})
	default:
		c.logger.Warn("invalid power payload", "topic", topic, "payload", raw)
	}
}

// handleHVACMode maps HA's hvac_mode payloads to the bridge's command set.
// "heat" and "fan_only" queue one job with two commands applied sequentially;
// the second is skipped if the first returns an error so the controller is
// never left with mismatched power and mode state.
func (c *CommandSubscriber) handleHVACMode(topic string, payload []byte) {
	raw := strings.ToLower(string(payload))
	switch raw {
	case "off":
		c.enqueueCommand(topic, dto.SetPowerCommand{On: false})
	case "heat":
		c.enqueueSequence(topic, dto.SetPowerCommand{On: true}, dto.SetModeCommand{Mode: valueobject.ModeHeat})
	case "fan_only":
		c.enqueueSequence(topic, dto.SetPowerCommand{On: true}, dto.SetModeCommand{Mode: valueobject.ModeOff})
	default:
		c.logger.Warn("invalid hvac_mode payload", "topic", topic, "payload", string(payload))
	}
}

// handleTargetTemperature parses a Celsius float and queues SetTemperatureCommand.
// Range validation is owned by valueobject.NewTemperature.
func (c *CommandSubscriber) handleTargetTemperature(topic string, payload []byte) {
	raw := string(payload)
	celsius, err := strconv.ParseFloat(raw, 64)
	if err != nil {
		c.logger.Warn("invalid target temperature payload",
			"topic", topic, "payload", raw, "error", err)
		return
	}
	temp, err := valueobject.NewTemperature(celsius)
	if err != nil {
		c.logger.Warn("invalid target temperature",
			"topic", topic, "payload", raw, "error", err)
		return
	}
	c.enqueueCommand(topic, dto.SetTemperatureCommand{Temperature: temp})
}

// handleFanTarget parses an unsigned integer fan speed and queues SetFanCommand.
// Range validation is owned by valueobject.NewFanSpeed.
func (c *CommandSubscriber) handleFanTarget(topic string, payload []byte) {
	raw := string(payload)
	v, err := strconv.ParseUint(raw, 10, 16)
	if err != nil {
		c.logger.Warn("invalid fan target payload",
			"topic", topic, "payload", raw, "error", err)
		return
	}
	//nolint:gosec // G115: bounded by ParseUint bitSize=16
	speed, err := valueobject.NewFanSpeed(uint16(v))
	if err != nil {
		c.logger.Warn("invalid fan target",
			"topic", topic, "payload", raw, "error", err)
		return
	}
	c.enqueueCommand(topic, dto.SetFanCommand{FanSpeed: speed})
}

// settingHandler forwards the raw payload for one panel setting. It does not
// parse or validate: ApplyCommand owns that, so rejects surface in one place.
func (c *CommandSubscriber) settingHandler(objectID string) port.MessageHandler {
	return func(topic string, payload []byte) {
		c.enqueueCommand(topic, dto.SetSettingCommand{ObjectID: objectID, Payload: string(payload)})
	}
}

// enqueueCommand queues a single-command job.
func (c *CommandSubscriber) enqueueCommand(topic string, cmd dto.Command) {
	c.enqueue(topic, func(ctx context.Context) { _ = c.apply(ctx, cmd) })
}

// enqueueSequence queues one job that applies cmds in order and stops at the
// first failure.
func (c *CommandSubscriber) enqueueSequence(topic string, cmds ...dto.Command) {
	c.enqueue(topic, func(ctx context.Context) {
		for _, cmd := range cmds {
			if err := c.apply(ctx, cmd); err != nil {
				return
			}
		}
	})
}

// apply forwards cmd to the configured applier. A failure is logged at Debug
// only: ApplyCommand already logs it at WARN with full detail. The error is
// returned so multi-command jobs can short-circuit.
func (c *CommandSubscriber) apply(ctx context.Context, cmd dto.Command) error {
	if err := c.applier.Apply(ctx, cmd); err != nil {
		c.logger.Debug("apply command failed", "command", commandString(cmd), "error", err)
		return err
	}
	return nil
}

// commandString renders cmd for logs, preferring its Stringer form.
func commandString(cmd dto.Command) string {
	if s, ok := cmd.(fmt.Stringer); ok {
		return s.String()
	}
	return fmt.Sprintf("%T", cmd)
}
