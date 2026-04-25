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
