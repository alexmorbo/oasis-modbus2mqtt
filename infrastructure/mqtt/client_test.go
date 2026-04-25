package mqtt_test

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/dto"
	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/config"
	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/mqtt"
)

var sharedBroker string

//nolint:misspell // mosquitto is the correct name of the MQTT broker
func TestMain(m *testing.M) {
	// Note: testing.Short() uses the -short flag which is not available during init,
	// so we defer Docker startup. Unit tests run; integration tests skip with skipIfShort().
	if os.Getenv("SKIP_INTEGRATION") != "" {
		os.Exit(m.Run())
	}

	ctx := context.Background()
	req := testcontainers.ContainerRequest{
		Image:        "eclipse-mosquitto:2",
		ExposedPorts: []string{"1883/tcp"},
		Cmd: []string{"sh", "-c",
			"printf 'listener 1883\\nallow_anonymous true\\n' > /mosquitto/config/mosquitto.conf && exec mosquitto -c /mosquitto/config/mosquitto.conf"},
		WaitingFor: wait.ForListeningPort("1883/tcp").WithStartupTimeout(60 * time.Second),
	}
	container, err := testcontainers.GenericContainer(ctx, testcontainers.GenericContainerRequest{
		ContainerRequest: req,
		Started:          true,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "failed to start mosquitto container: %v\n", err)
		os.Exit(1)
	}

	host, err := container.Host(ctx)
	if err != nil {
		_ = container.Terminate(ctx)
		fmt.Fprintf(os.Stderr, "container host: %v\n", err)
		os.Exit(1)
	}
	port, err := container.MappedPort(ctx, "1883/tcp")
	if err != nil {
		_ = container.Terminate(ctx)
		fmt.Fprintf(os.Stderr, "container port: %v\n", err)
		os.Exit(1)
	}
	sharedBroker = fmt.Sprintf("%s:%s", host, port.Port())

	code := m.Run()
	_ = container.Terminate(ctx)
	os.Exit(code)
}

func newTopicBuilder(t *testing.T) *mqtt.TopicBuilder {
	t.Helper()
	return mqtt.NewTopicBuilder(config.HAConfig{
		DiscoveryPrefix: "homeassistant",
		DevicePrefix:    "oasis_syberia",
	})
}

func newTestConfig(t *testing.T) config.MQTTConfig {
	t.Helper()
	return config.MQTTConfig{
		Broker:    sharedBroker,
		ClientID:  "test-" + uuid.NewString(),
		Keepalive: 5 * time.Second,
		QoS:       1,
	}
}

func newTestClient(t *testing.T) *mqtt.Client {
	t.Helper()
	c := mqtt.NewClient(newTestConfig(t), newTopicBuilder(t),
		slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, c.Connect(ctx))
	t.Cleanup(func() { c.Disconnect(500 * time.Millisecond) })
	return c
}

// newRawSubscriber connects a vanilla paho client subscribed to topic and
// returns the channel of received payloads plus a teardown.
func newRawSubscriber(t *testing.T, topic string) <-chan rawMessage {
	t.Helper()
	ch := make(chan rawMessage, 16)
	opts := paho.NewClientOptions().
		AddBroker("tcp://" + sharedBroker).
		SetClientID("raw-sub-" + uuid.NewString()).
		SetCleanSession(true)
	c := paho.NewClient(opts)
	tok := c.Connect()
	require.True(t, tok.WaitTimeout(5*time.Second))
	require.NoError(t, tok.Error())

	stok := c.Subscribe(topic, 1, func(_ paho.Client, msg paho.Message) {
		select {
		case ch <- rawMessage{Topic: msg.Topic(), Payload: append([]byte(nil), msg.Payload()...)}:
		default:
		}
	})
	require.True(t, stok.WaitTimeout(5*time.Second))
	require.NoError(t, stok.Error())

	t.Cleanup(func() { c.Disconnect(100) })
	return ch
}

type rawMessage struct {
	Topic   string
	Payload []byte
}

func newRawPublisher(t *testing.T) paho.Client {
	t.Helper()
	opts := paho.NewClientOptions().
		AddBroker("tcp://" + sharedBroker).
		SetClientID("raw-pub-" + uuid.NewString()).
		SetCleanSession(true)
	c := paho.NewClient(opts)
	tok := c.Connect()
	require.True(t, tok.WaitTimeout(5*time.Second))
	require.NoError(t, tok.Error())
	t.Cleanup(func() { c.Disconnect(100) })
	return c
}

func skipIfShort(t *testing.T) {
	t.Helper()
	if testing.Short() {
		t.Skip("integration test requires Docker; skipped under -short")
	}
}

func TestPublish_Roundtrip(t *testing.T) {
	t.Parallel()
	skipIfShort(t)

	c := newTestClient(t)
	topic := "test/roundtrip/" + uuid.NewString()
	ch := newRawSubscriber(t, topic)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, c.Publish(ctx, topic, []byte("hello"), false))

	select {
	case msg := <-ch:
		assert.Equal(t, topic, msg.Topic)
		assert.Equal(t, []byte("hello"), msg.Payload)
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for roundtrip message")
	}
}

func TestSubscribe_AndReceive(t *testing.T) {
	t.Parallel()
	skipIfShort(t)

	c := newTestClient(t)
	topic := "test/subscribe/" + uuid.NewString()

	var received atomic.Int32
	var lastPayload atomic.Value
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	require.NoError(t, c.Subscribe(ctx, topic, func(_ string, payload []byte) {
		lastPayload.Store(append([]byte(nil), payload...))
		received.Add(1)
	}))

	pub := newRawPublisher(t)
	tok := pub.Publish(topic, 1, false, "hi-from-raw")
	require.True(t, tok.WaitTimeout(2*time.Second))
	require.NoError(t, tok.Error())

	assert.Eventually(t, func() bool { return received.Load() >= 1 }, 15*time.Second, 50*time.Millisecond)
	if v := lastPayload.Load(); v != nil {
		assert.Equal(t, []byte("hi-from-raw"), v.([]byte))
	}
}

func TestSubscribe_NilHandler(t *testing.T) {
	t.Parallel()
	skipIfShort(t)

	c := newTestClient(t)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	err := c.Subscribe(ctx, "test/nil/"+uuid.NewString(), nil)
	require.Error(t, err)
}

func TestDisconnect_PublishesOffline(t *testing.T) {
	t.Parallel()
	skipIfShort(t)

	tb := newTopicBuilder(t)
	cfg := newTestConfig(t)
	c := mqtt.NewClient(cfg, tb,
		slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, c.Connect(ctx))

	availabilityCh := newRawSubscriber(t, tb.Availability())

	// Drain the retained "online" sent during onConnect (if any).
	drained := false
	for !drained {
		select {
		case <-availabilityCh:
		case <-time.After(500 * time.Millisecond):
			drained = true
		}
	}

	c.Disconnect(500 * time.Millisecond)

	select {
	case msg := <-availabilityCh:
		assert.Equal(t, []byte(mqtt.AvailabilityOffline), msg.Payload)
	case <-time.After(3 * time.Second):
		t.Fatal("timeout waiting for offline availability")
	}
	assert.False(t, c.Connected())
}

func TestSubscribe_RestoredOnReconnect(t *testing.T) {
	t.Parallel()
	skipIfShort(t)

	tb := newTopicBuilder(t)
	cfg := newTestConfig(t)
	c := mqtt.NewClient(cfg, tb,
		slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, c.Connect(ctx))

	topic := "test/restore/" + uuid.NewString()
	var received atomic.Int32
	var mu sync.Mutex
	var payloads [][]byte
	require.NoError(t, c.Subscribe(ctx, topic, func(_ string, payload []byte) {
		mu.Lock()
		payloads = append(payloads, append([]byte(nil), payload...))
		mu.Unlock()
		received.Add(1)
	}))

	// First publish to confirm subscription is live.
	pub := newRawPublisher(t)
	tok := pub.Publish(topic, 1, false, "before-reconnect")
	require.True(t, tok.WaitTimeout(2*time.Second))
	assert.Eventually(t, func() bool { return received.Load() >= 1 }, 10*time.Second, 50*time.Millisecond)

	// Hard cycle: Disconnect then Connect again. onConnect should re-subscribe.
	c.Disconnect(200 * time.Millisecond)
	require.False(t, c.Connected())

	// New connection requires a fresh ClientID — same id with cleansession=true is fine,
	// but to avoid will/state interference we stick with the original.
	ctx2, cancel2 := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel2()
	require.NoError(t, c.Connect(ctx2))
	assert.Eventually(t, c.Connected, 10*time.Second, 50*time.Millisecond)

	// Give onConnect time to resubscribe.
	time.Sleep(500 * time.Millisecond)

	tok2 := pub.Publish(topic, 1, false, "after-reconnect")
	require.True(t, tok2.WaitTimeout(2*time.Second))

	assert.Eventually(t, func() bool { return received.Load() >= 2 }, 15*time.Second, 100*time.Millisecond)

	c.Disconnect(200 * time.Millisecond)
}

func TestPublish_NotConnected_ReturnsError(t *testing.T) {
	t.Parallel()
	// This test does NOT need a real broker — it never connects.
	tb := mqtt.NewTopicBuilder(config.HAConfig{
		DiscoveryPrefix: "homeassistant",
		DevicePrefix:    "oasis_syberia",
	})
	c := mqtt.NewClient(config.MQTTConfig{
		Broker: "127.0.0.1:1", ClientID: "no-conn", Keepalive: time.Second, QoS: 1,
	}, tb, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := c.Publish(ctx, "any/topic", []byte("x"), false)
	require.ErrorIs(t, err, dto.ErrMQTTNotConnected)
}

func TestSubscribe_StoredWhenNotConnected(t *testing.T) {
	t.Parallel()
	tb := mqtt.NewTopicBuilder(config.HAConfig{
		DiscoveryPrefix: "homeassistant",
		DevicePrefix:    "oasis_syberia",
	})
	c := mqtt.NewClient(config.MQTTConfig{
		Broker: "127.0.0.1:1", ClientID: "no-conn-sub", Keepalive: time.Second, QoS: 1,
	}, tb, slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelError})))

	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	err := c.Subscribe(ctx, "x/y", func(_ string, _ []byte) {})
	require.NoError(t, err)
	assert.False(t, c.Connected())
}

func TestConnected_ReflectsState(t *testing.T) {
	t.Parallel()
	skipIfShort(t)

	tb := newTopicBuilder(t)
	cfg := newTestConfig(t)
	c := mqtt.NewClient(cfg, tb,
		slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn})))

	assert.False(t, c.Connected())

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	require.NoError(t, c.Connect(ctx))
	assert.Eventually(t, c.Connected, 10*time.Second, 50*time.Millisecond)

	c.Disconnect(200 * time.Millisecond)
	assert.False(t, c.Connected())
}

func TestNewClient_PanicsOnNilTopics(t *testing.T) {
	t.Parallel()
	require.PanicsWithValue(t, "mqtt: NewClient requires non-nil TopicBuilder", func() {
		mqtt.NewClient(config.MQTTConfig{Broker: "x", ClientID: "y", Keepalive: time.Second, QoS: 1}, nil, nil)
	})
}

func TestNewClient_NilLoggerUsesDefault(t *testing.T) {
	t.Parallel()
	tb := mqtt.NewTopicBuilder(config.HAConfig{DiscoveryPrefix: "h", DevicePrefix: "d"})
	c := mqtt.NewClient(config.MQTTConfig{Broker: "x", ClientID: "y", Keepalive: time.Second, QoS: 1}, tb, nil)
	require.NotNil(t, c)
	// Internal logger field is unexported; just verify we can call Connected without panicking.
	assert.False(t, c.Connected())
}

func TestPublish_ContextCancelled(t *testing.T) {
	t.Parallel()
	skipIfShort(t)

	c := newTestClient(t)
	topic := "test/ctxcancel/" + uuid.NewString()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := c.Publish(ctx, topic, []byte("x"), false)
	// The publish may race with the cancelled context. Accept three outcomes:
	// 1. Success (err == nil) if publish completed before context check
	// 2. context.Canceled if ctx was lost in the token.Done/ctx.Done select
	// 3. dto.ErrMQTTNotConnected in rare timing cases
	// The contract is: do not deadlock.
	if err != nil && !errors.Is(err, context.Canceled) && !errors.Is(err, dto.ErrMQTTNotConnected) {
		t.Fatalf("unexpected error: %v", err)
	}
}
