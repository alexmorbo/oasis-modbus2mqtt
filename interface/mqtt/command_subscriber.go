package mqtt

import (
	"context"
	"fmt"
	"log/slog"
	"strconv"
	"strings"
	"time"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/dto"
	"github.com/alexmorbo/oasis-modbus2mqtt/application/port"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"
)

// handlerTimeout bounds the time a single command handler may spend applying
// the resulting command. The MQTT client invokes handlers without a parent
// context, so the subscriber roots its own context here.
const handlerTimeout = 10 * time.Second

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

// CommandSubscriber wires HA command topics to the bridge's command applier.
// It owns no goroutines: handlers execute on the MQTT client's worker pool
// and apply commands synchronously within a 10s per-message timeout.
type CommandSubscriber struct {
	sub     Subscriber
	applier CommandApplier
	topics  TopicProvider
	logger  *slog.Logger
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
	}
}

// Start registers handlers for every HA command topic. It returns the first
// Subscribe error wrapped with the offending topic; on success it returns nil
// after every subscription has been accepted.
func (c *CommandSubscriber) Start(ctx context.Context) error {
	subscriptions := []struct {
		objectID string
		handler  port.MessageHandler
	}{
		{"power", c.handlePower},
		{"hvac_mode", c.handleHVACMode},
		{"target_temperature", c.handleTargetTemperature},
		{"fan_target", c.handleFanTarget},
	}
	for _, s := range subscriptions {
		topic := c.topics.Command(s.objectID)
		if err := c.sub.Subscribe(ctx, topic, s.handler); err != nil {
			return fmt.Errorf("subscribe %s: %w", topic, err)
		}
	}
	return nil
}

// handlePower parses ON/OFF (case-insensitive) and applies a SetPowerCommand.
func (c *CommandSubscriber) handlePower(topic string, payload []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()

	raw := string(payload)
	switch {
	case strings.EqualFold(raw, "ON"):
		_ = c.apply(ctx, dto.SetPowerCommand{On: true})
	case strings.EqualFold(raw, "OFF"):
		_ = c.apply(ctx, dto.SetPowerCommand{On: false})
	default:
		c.logger.Warn("invalid power payload", "topic", topic, "payload", raw)
	}
}

// handleHVACMode maps HA's hvac_mode payloads to the bridge's command set.
// "heat" and "fan_only" emit two commands sequentially; the second is skipped
// if the first returns an error so the controller is never left with mismatched
// power and mode state.
func (c *CommandSubscriber) handleHVACMode(topic string, payload []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()

	raw := strings.ToLower(string(payload))
	switch raw {
	case "off":
		_ = c.apply(ctx, dto.SetPowerCommand{On: false})
	case "heat":
		if err := c.apply(ctx, dto.SetPowerCommand{On: true}); err != nil {
			return
		}
		_ = c.apply(ctx, dto.SetModeCommand{Mode: valueobject.ModeHeat})
	case "fan_only":
		if err := c.apply(ctx, dto.SetPowerCommand{On: true}); err != nil {
			return
		}
		_ = c.apply(ctx, dto.SetModeCommand{Mode: valueobject.ModeOff})
	default:
		c.logger.Warn("invalid hvac_mode payload", "topic", topic, "payload", string(payload))
	}
}

// handleTargetTemperature parses a Celsius float and applies SetTemperatureCommand.
// Range validation is owned by valueobject.NewTemperature.
func (c *CommandSubscriber) handleTargetTemperature(topic string, payload []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()

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
	_ = c.apply(ctx, dto.SetTemperatureCommand{Temperature: temp})
}

// handleFanTarget parses an unsigned integer fan speed and applies SetFanCommand.
// Range validation is owned by valueobject.NewFanSpeed.
func (c *CommandSubscriber) handleFanTarget(topic string, payload []byte) {
	ctx, cancel := context.WithTimeout(context.Background(), handlerTimeout)
	defer cancel()

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
	_ = c.apply(ctx, dto.SetFanCommand{FanSpeed: speed})
}

// apply forwards cmd to the configured applier and logs WARN on failure.
// The error is returned so multi-command handlers can short-circuit.
func (c *CommandSubscriber) apply(ctx context.Context, cmd dto.Command) error {
	if err := c.applier.Apply(ctx, cmd); err != nil {
		c.logger.Warn("apply command failed", "command", fmt.Sprintf("%T", cmd), "error", err)
		return err
	}
	return nil
}
