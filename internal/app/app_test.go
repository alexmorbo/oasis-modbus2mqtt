package app

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
		HTTP:   config.HTTPConfig{Port: 18080},
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

// TestNew_BuildsCleanly verifies that New wires every dependency
// without performing I/O and without panicking on a valid configuration.
func TestNew_BuildsCleanly(t *testing.T) {
	t.Parallel()

	cfg := newTestConfig()
	logger := newDiscardLogger()

	app, err := New(cfg, logger)
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

// TestNew_NilConfigReturnsError verifies that New rejects a nil
// configuration with a descriptive error rather than panicking.
func TestNew_NilConfigReturnsError(t *testing.T) {
	t.Parallel()

	app, err := New(nil, newDiscardLogger())
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

	app, err := New(cfg, newDiscardLogger())
	require.NoError(t, err)
	require.NotNil(t, app)

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	runErr := app.Run(ctx)
	assert.Error(t, runErr)
}
