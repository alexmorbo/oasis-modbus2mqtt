package service

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/port"
)

// haStatusHandlerTimeout bounds the time a single HA status handler may
// spend in the user-supplied callback. The MQTT client invokes handlers
// without a parent context, so the listener roots its own context here.
const haStatusHandlerTimeout = 10 * time.Second

// HAStatusSubscriber is the subset of the MQTT publisher API used by
// HAStatusListener. *infrastructure/mqtt.Client satisfies it.
type HAStatusSubscriber interface {
	Subscribe(ctx context.Context, topic string, handler port.MessageHandler) error
}

// HAOnlineCallback is invoked when the listener observes an "online"
// payload on the HA status topic. It is expected to republish the
// discovery configs and force a state-snapshot publish so any
// reconnecting subscriber recovers entity state from retained values.
// The supplied context is bounded by haStatusHandlerTimeout.
type HAOnlineCallback func(ctx context.Context)

// HAStatusListener subscribes to Home Assistant's birth-message topic
// (typically "homeassistant/status") and invokes a callback when HA
// announces it is online again. It is policy-free: the decision of what
// to do on online lives in the composition root.
type HAStatusListener struct {
	sub      HAStatusSubscriber
	topic    string
	onOnline HAOnlineCallback
	logger   *slog.Logger
}

// NewHAStatusListener constructs a listener. A nil subscriber, an empty
// topic, or a nil logger fall through with sensible defaults — only the
// subscriber is mandatory, since there is no useful default. A nil
// onOnline callback is treated as no-op (the listener still parses and
// logs the payload, but does nothing further).
func NewHAStatusListener(
	sub HAStatusSubscriber,
	topic string,
	onOnline HAOnlineCallback,
	logger *slog.Logger,
) *HAStatusListener {
	if sub == nil {
		panic("ha status listener: subscriber must not be nil")
	}
	if topic == "" {
		panic("ha status listener: topic must not be empty")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &HAStatusListener{
		sub:      sub,
		topic:    topic,
		onOnline: onOnline,
		logger:   logger,
	}
}

// Start registers the message handler on the configured topic. It returns
// the subscribe error wrapped with the topic string on failure.
func (l *HAStatusListener) Start(ctx context.Context) error {
	if err := l.sub.Subscribe(ctx, l.topic, l.handle); err != nil {
		return err
	}
	l.logger.Info("ha status listener started", "topic", l.topic)
	return nil
}

// handle is the port.MessageHandler invoked by the MQTT client per
// delivered message. It parses the payload (case-insensitive,
// whitespace-trimmed) and dispatches to the online callback or logs
// the offline event. Garbage payloads log WARN and are otherwise
// ignored.
func (l *HAStatusListener) handle(topic string, payload []byte) {
	raw := strings.TrimSpace(strings.ToLower(string(payload)))
	switch raw {
	case "online":
		l.logger.Info("ha online detected", "topic", topic)
		if l.onOnline == nil {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), haStatusHandlerTimeout)
		defer cancel()
		l.onOnline(ctx)
	case "offline":
		l.logger.Info("ha offline detected", "topic", topic)
	default:
		l.logger.Warn("ha status: unexpected payload", "topic", topic, "payload", string(payload))
	}
}
