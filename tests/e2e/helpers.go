package e2e

import (
	"context"
	"fmt"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/stretchr/testify/require"
	"github.com/tbrandon/mbserver"
	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/wait"
)

// freeAddr binds an ephemeral TCP port on 127.0.0.1, closes the listener and
// returns the address as "host:port". Subsequent users of the address race
// against re-allocation, but the window is short enough for local CI.
func freeAddr(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	l, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := l.Addr().String()
	require.NoError(t, l.Close())
	return addr
}

// splitAddr parses "host:port" into its components.
func splitAddr(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	require.NoError(t, err)
	port, err := strconv.Atoi(portStr)
	require.NoError(t, err)
	return host, port
}

// startMbserver allocates a free port, starts an in-process tbrandon/mbserver
// listening on that port and registers an idempotent close on test cleanup.
// It returns the running server (so the test may pre-set registers and assert
// on writes) and the listening address.
func startMbserver(t *testing.T) (*mbserver.Server, string) {
	t.Helper()
	addr := freeAddr(t)
	srv := mbserver.NewServer()
	require.NoError(t, srv.ListenTCP(addr))
	var once sync.Once
	t.Cleanup(func() { once.Do(srv.Close) })
	return srv, addr
}

// startMosquitto launches an eclipse-mosquitto:2 testcontainer with anonymous
// access on a random host port and returns the broker address as "host:port".
// The container is terminated on test cleanup.
//
//nolint:misspell // mosquitto is the broker's correct name
func startMosquitto(t *testing.T) string {
	t.Helper()
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
	require.NoError(t, err)
	t.Cleanup(func() { _ = container.Terminate(ctx) })

	host, err := container.Host(ctx)
	require.NoError(t, err)
	port, err := container.MappedPort(ctx, "1883/tcp")
	require.NoError(t, err)
	return fmt.Sprintf("%s:%s", host, port.Port())
}

// newTestPahoClient connects a vanilla paho client to broker with the given
// clientID and clean session, and registers a cleanup that disconnects with
// a 100 ms quiesce. The returned client is connected and ready to publish or
// subscribe.
func newTestPahoClient(t *testing.T, broker, clientID string) paho.Client {
	t.Helper()
	opts := paho.NewClientOptions().
		AddBroker("tcp://" + broker).
		SetClientID(clientID).
		SetCleanSession(true).
		SetAutoReconnect(false).
		SetConnectTimeout(5 * time.Second)
	c := paho.NewClient(opts)
	tok := c.Connect()
	require.True(t, tok.WaitTimeout(5*time.Second))
	require.NoError(t, tok.Error())
	t.Cleanup(func() { c.Disconnect(100) })
	return c
}

// subscribeAndCollect subscribes client to topic at QoS 1 and accumulates the
// latest payload per concrete topic in a map. The returned getter takes a
// snapshot of the map under a mutex so callers may safely inspect it from
// `assert.Eventually` callbacks. Older payloads for the same topic are
// overwritten.
func subscribeAndCollect(t *testing.T, client paho.Client, topic string) func() map[string][]byte {
	t.Helper()
	var mu sync.Mutex
	store := make(map[string][]byte)

	tok := client.Subscribe(topic, 1, func(_ paho.Client, msg paho.Message) {
		mu.Lock()
		defer mu.Unlock()
		store[msg.Topic()] = append([]byte(nil), msg.Payload()...)
	})
	require.True(t, tok.WaitTimeout(5*time.Second))
	require.NoError(t, tok.Error())

	return func() map[string][]byte {
		mu.Lock()
		defer mu.Unlock()
		out := make(map[string][]byte, len(store))
		for k, v := range store {
			out[k] = append([]byte(nil), v...)
		}
		return out
	}
}
