package mqtt

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/stretchr/testify/assert"

	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/config"
)

// newUnconnectedClient wires c.client to a paho client that never dials, so
// onConnect can be driven directly. paho publish/subscribe on it fail fast
// with ErrNotConnected, which onConnect only logs.
func newUnconnectedClient(t *testing.T) (*Client, *atomic.Int32) {
	t.Helper()
	c := NewClient(
		config.MQTTConfig{Broker: "127.0.0.1:1", ClientID: "internal-test", Keepalive: time.Second},
		NewTopicBuilder(config.HAConfig{DiscoveryPrefix: "homeassistant", DevicePrefix: "oasis_test"}),
		slog.New(slog.NewTextHandler(io.Discard, nil)),
	)
	var runs atomic.Int32
	c.AddOnConnectHook(func(context.Context) { runs.Add(1) })
	c.client = paho.NewClient(paho.NewClientOptions().AddBroker("tcp://127.0.0.1:1"))
	return c, &runs
}

func isClosed(ch chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func TestOnConnect_IgnoresStaleClient(t *testing.T) {
	t.Parallel()
	c, runs := newUnconnectedClient(t)
	stale := paho.NewClient(paho.NewClientOptions().AddBroker("tcp://127.0.0.1:1"))

	c.onConnect(stale)

	assert.False(t, c.Connected(), "stale callback must not flip Connected")
	assert.Equal(t, int32(0), runs.Load(), "stale callback must not run hooks")
	assert.False(t, isClosed(c.ready), "stale callback must not release Connect")
}

func TestOnConnect_CurrentClientRunsHooks(t *testing.T) {
	t.Parallel()
	c, runs := newUnconnectedClient(t)

	c.onConnect(c.client)

	assert.True(t, c.Connected())
	assert.Equal(t, int32(1), runs.Load())
	assert.True(t, isClosed(c.ready))

	c.onConnect(c.client)
	assert.Equal(t, int32(2), runs.Load(), "reconnect reruns hooks without re-closing ready")
}
