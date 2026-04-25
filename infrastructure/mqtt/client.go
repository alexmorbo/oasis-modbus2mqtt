package mqtt

import (
	"context"
	"fmt"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/dto"
	"github.com/alexmorbo/oasis-modbus2mqtt/application/port"
	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/config"
	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/metrics"
)

// Availability payloads published to the device's availability topic. Both
// are sent retained so subscribers joining late see the current state.
const (
	AvailabilityOnline  = "online"
	AvailabilityOffline = "offline"
)

// Client is an MQTTPublisher backed by paho.mqtt.golang v3. It owns a
// single broker connection, restores subscriptions on reconnect from a
// local registry, and exposes a non-blocking Connected() probe.
type Client struct {
	cfg    config.MQTTConfig
	topics *TopicBuilder
	logger *slog.Logger

	mu     sync.Mutex
	client paho.Client
	subs   map[string]port.MessageHandler

	connectedAtomic atomic.Bool
}

// NewClient builds a Client without performing any I/O. It panics if
// topics is nil — that would defeat LWT and HA discovery later. A nil
// logger is replaced with slog.Default().
func NewClient(cfg config.MQTTConfig, topics *TopicBuilder, logger *slog.Logger) *Client {
	if topics == nil {
		panic("mqtt: NewClient requires non-nil TopicBuilder")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Client{
		cfg:    cfg,
		topics: topics,
		logger: logger,
		subs:   make(map[string]port.MessageHandler),
	}
}

// Connect opens the broker connection, configures LWT, and registers
// callbacks. It blocks until the broker accepts the connection or ctx is
// cancelled. Subsequent reconnects are handled by paho automatically.
func (c *Client) Connect(ctx context.Context) error {
	opts := paho.NewClientOptions().
		AddBroker("tcp://"+c.cfg.Broker).
		SetClientID(c.cfg.ClientID).
		SetCleanSession(true).
		SetKeepAlive(c.cfg.Keepalive).
		SetAutoReconnect(true).
		SetMaxReconnectInterval(60*time.Second).
		SetConnectRetry(true).
		SetConnectRetryInterval(5*time.Second).
		SetWill(c.topics.Availability(), AvailabilityOffline, c.cfg.QoS, true).
		SetOnConnectHandler(c.onConnect).
		SetConnectionLostHandler(c.onConnectionLost)

	if c.cfg.Username != "" {
		opts.SetUsername(c.cfg.Username).SetPassword(c.cfg.Password)
	}

	c.mu.Lock()
	c.client = paho.NewClient(opts)
	client := c.client
	c.mu.Unlock()

	token := client.Connect()
	select {
	case <-token.Done():
		if err := token.Error(); err != nil {
			return fmt.Errorf("mqtt connect: %w", err)
		}
	case <-ctx.Done():
		return ctx.Err()
	}
	return nil
}

// Disconnect publishes availability=offline (best effort) and tears the
// connection down, allowing in-flight messages up to quiesce to drain.
// Safe to call when not connected.
func (c *Client) Disconnect(quiesce time.Duration) {
	c.mu.Lock()
	client := c.client
	c.mu.Unlock()

	if client == nil || !c.connectedAtomic.Load() {
		c.logger.Info("mqtt disconnect skipped: not connected")
		return
	}

	token := client.Publish(c.topics.Availability(), c.cfg.QoS, true, AvailabilityOffline)
	if !token.WaitTimeout(1 * time.Second) {
		c.logger.Warn("mqtt offline publish timed out")
	} else if err := token.Error(); err != nil {
		c.logger.Warn("mqtt offline publish failed", "error", err)
	}

	//nolint:gosec // duration in ms is non-negative by construction (caller passes >=0)
	client.Disconnect(uint(quiesce.Milliseconds()))

	c.connectedAtomic.Store(false)
	metrics.SetMQTTConnected(false)
	c.logger.Info("mqtt disconnected", "quiesce", quiesce)
}

// Publish sends payload on topic at the configured QoS. Returns
// dto.ErrMQTTNotConnected if the broker connection is not currently up.
// The call blocks until the broker acknowledges, the context is
// cancelled, or the underlying token completes with an error.
func (c *Client) Publish(ctx context.Context, topic string, payload []byte, retained bool) error {
	if !c.connectedAtomic.Load() {
		return dto.ErrMQTTNotConnected
	}

	c.mu.Lock()
	client := c.client
	c.mu.Unlock()

	if client == nil {
		return dto.ErrMQTTNotConnected
	}

	token := client.Publish(topic, c.cfg.QoS, retained, payload)
	select {
	case <-token.Done():
		if err := token.Error(); err != nil {
			c.logger.Warn("mqtt publish failed", "topic", topic, "error", err)
			return fmt.Errorf("mqtt publish %q: %w", topic, err)
		}
	case <-ctx.Done():
		return ctx.Err()
	}

	metrics.MQTTPublishesTotal.Inc()
	c.logger.Debug("mqtt published", "topic", topic, "retained", retained, "bytes", len(payload))
	return nil
}

// Subscribe registers handler for topic. The handler is stored even when
// the client is disconnected so it can be re-applied by onConnect after
// reconnect. If the client is currently connected the subscription is
// installed on the broker before returning.
func (c *Client) Subscribe(ctx context.Context, topic string, handler port.MessageHandler) error {
	if handler == nil {
		return fmt.Errorf("mqtt subscribe %q: handler is nil", topic)
	}

	c.mu.Lock()
	c.subs[topic] = handler
	client := c.client
	connected := c.connectedAtomic.Load()
	c.mu.Unlock()

	if !connected || client == nil {
		c.logger.Info("mqtt subscribe stored (not connected)", "topic", topic)
		return nil
	}

	token := client.Subscribe(topic, c.cfg.QoS, c.makePahoHandler(handler))
	select {
	case <-token.Done():
		if err := token.Error(); err != nil {
			return fmt.Errorf("mqtt subscribe %q: %w", topic, err)
		}
	case <-ctx.Done():
		return ctx.Err()
	}

	c.logger.Info("mqtt subscribed", "topic", topic)
	return nil
}

// Connected reports whether the broker connection is currently up.
// Lock-free; safe to call from any goroutine.
func (c *Client) Connected() bool {
	return c.connectedAtomic.Load()
}

func (c *Client) onConnect(_ paho.Client) {
	c.connectedAtomic.Store(true)
	metrics.SetMQTTConnected(true)
	metrics.MQTTReconnectsTotal.Inc()
	c.logger.Info("mqtt connected", "broker", c.cfg.Broker, "client_id", c.cfg.ClientID)

	c.mu.Lock()
	client := c.client
	subsCopy := make(map[string]port.MessageHandler, len(c.subs))
	for topic, handler := range c.subs {
		subsCopy[topic] = handler
	}
	c.mu.Unlock()

	if client == nil {
		return
	}

	token := client.Publish(c.topics.Availability(), c.cfg.QoS, true, AvailabilityOnline)
	if !token.WaitTimeout(2 * time.Second) {
		c.logger.Warn("mqtt online publish timed out")
	} else if err := token.Error(); err != nil {
		c.logger.Warn("mqtt online publish failed", "error", err)
	}

	for topic, handler := range subsCopy {
		tok := client.Subscribe(topic, c.cfg.QoS, c.makePahoHandler(handler))
		if !tok.WaitTimeout(5 * time.Second) {
			c.logger.Warn("mqtt resubscribe timed out", "topic", topic)
			continue
		}
		if err := tok.Error(); err != nil {
			c.logger.Warn("mqtt resubscribe failed", "topic", topic, "error", err)
			continue
		}
		c.logger.Info("mqtt resubscribed", "topic", topic)
	}
}

func (c *Client) onConnectionLost(_ paho.Client, err error) {
	c.connectedAtomic.Store(false)
	metrics.SetMQTTConnected(false)
	c.logger.Warn("mqtt connection lost", "error", err)
}

func (c *Client) makePahoHandler(h port.MessageHandler) paho.MessageHandler {
	return func(_ paho.Client, msg paho.Message) {
		h(msg.Topic(), msg.Payload())
	}
}
