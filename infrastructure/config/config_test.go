package config_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/config"
)

// allEnvVars lists every env var read by config.Load.
// Used to neutralize ambient CI environment in tests that need defaults.
var allEnvVars = []string{
	"MODBUS_HOST",
	"MODBUS_PORT",
	"MODBUS_SLAVE_ID",
	"MODBUS_CONNECT_TIMEOUT",
	"MODBUS_READ_TIMEOUT",
	"MODBUS_WRITE_TIMEOUT",
	"MODBUS_GUARD_INTERVAL",
	"MQTT_BROKER",
	"MQTT_CLIENT_ID",
	"MQTT_USERNAME",
	"MQTT_PASSWORD",
	"MQTT_KEEPALIVE",
	"MQTT_QOS",
	"HA_DISCOVERY_PREFIX",
	"HA_DEVICE_PREFIX",
	"HA_DEVICE_NAME",
	"HA_MANUFACTURER",
	"HA_MODEL",
	"POLL_HOT_INTERVAL",
	"POLL_MEDIUM_INTERVAL",
	"POLL_SLOW_INTERVAL",
	"AVAILABILITY_THRESHOLD",
	"HTTP_PORT",
	"LOG_LEVEL",
	"RECONNECT_MIN_DELAY",
	"RECONNECT_MAX_DELAY",
	"RECONNECT_FACTOR",
	"RECONNECT_JITTER_PCT",
}

func clearAllConfigEnvs(t *testing.T) {
	t.Helper()
	for _, k := range allEnvVars {
		t.Setenv(k, "")
	}
}

func TestLoad_Defaults(t *testing.T) {
	clearAllConfigEnvs(t)

	cfg, err := config.Load()
	require.NoError(t, err)
	require.NotNil(t, cfg)

	assert.Equal(t, "10.90.19.7", cfg.Modbus.Host)
	assert.Equal(t, 502, cfg.Modbus.Port)
	assert.Equal(t, byte(1), cfg.Modbus.SlaveID)
	assert.Equal(t, 5*time.Second, cfg.Modbus.ConnectTimeout)
	assert.Equal(t, 2*time.Second, cfg.Modbus.ReadTimeout)
	assert.Equal(t, 2*time.Second, cfg.Modbus.WriteTimeout)
	assert.Equal(t, 100*time.Millisecond, cfg.Modbus.GuardInterval)

	assert.Equal(t, "10.90.19.10:1883", cfg.MQTT.Broker)
	assert.Equal(t, "oasis-modbus2mqtt", cfg.MQTT.ClientID)
	assert.Equal(t, "", cfg.MQTT.Username)
	assert.Equal(t, "", cfg.MQTT.Password)
	assert.Equal(t, 30*time.Second, cfg.MQTT.Keepalive)
	assert.Equal(t, byte(1), cfg.MQTT.QoS)

	assert.Equal(t, "homeassistant", cfg.HomeAssistant.DiscoveryPrefix)
	assert.Equal(t, "oasis_syberia", cfg.HomeAssistant.DevicePrefix)
	assert.Equal(t, "Oasis Syberia", cfg.HomeAssistant.DeviceName)
	assert.Equal(t, "GTC", cfg.HomeAssistant.Manufacturer)
	assert.Equal(t, "Syberia 5", cfg.HomeAssistant.Model)

	assert.Equal(t, 5*time.Second, cfg.Polling.HotInterval)
	assert.Equal(t, 15*time.Second, cfg.Polling.MediumInterval)
	assert.Equal(t, 60*time.Second, cfg.Polling.SlowInterval)
	assert.Equal(t, 30*time.Second, cfg.Polling.AvailabilityThreshold)

	assert.Equal(t, 8080, cfg.HTTP.Port)
	assert.Equal(t, "info", cfg.Logger.Level)

	assert.Equal(t, 1*time.Second, cfg.Reconnect.MinDelay)
	assert.Equal(t, 60*time.Second, cfg.Reconnect.MaxDelay)
	assert.InDelta(t, 2.0, cfg.Reconnect.Factor, 0.0001)
	assert.InDelta(t, 0.2, cfg.Reconnect.JitterPct, 0.0001)
}

func TestLoad_AllFromEnv(t *testing.T) {
	t.Setenv("MODBUS_HOST", "192.168.1.10")
	t.Setenv("MODBUS_PORT", "5020")
	t.Setenv("MODBUS_SLAVE_ID", "5")
	t.Setenv("MODBUS_CONNECT_TIMEOUT", "10s")
	t.Setenv("MODBUS_READ_TIMEOUT", "3s")
	t.Setenv("MODBUS_WRITE_TIMEOUT", "4s")
	t.Setenv("MODBUS_GUARD_INTERVAL", "200ms")

	t.Setenv("MQTT_BROKER", "mqtt.local:1884")
	t.Setenv("MQTT_CLIENT_ID", "test-client")
	t.Setenv("MQTT_USERNAME", "user")
	t.Setenv("MQTT_PASSWORD", "pass")
	t.Setenv("MQTT_KEEPALIVE", "45s")
	t.Setenv("MQTT_QOS", "2")

	t.Setenv("HA_DISCOVERY_PREFIX", "ha")
	t.Setenv("HA_DEVICE_PREFIX", "test_dev")
	t.Setenv("HA_DEVICE_NAME", "Test Device")
	t.Setenv("HA_MANUFACTURER", "Acme")
	t.Setenv("HA_MODEL", "Model X")

	t.Setenv("POLL_HOT_INTERVAL", "1s")
	t.Setenv("POLL_MEDIUM_INTERVAL", "10s")
	t.Setenv("POLL_SLOW_INTERVAL", "120s")
	t.Setenv("AVAILABILITY_THRESHOLD", "45s")

	t.Setenv("HTTP_PORT", "9090")
	t.Setenv("LOG_LEVEL", "debug")

	t.Setenv("RECONNECT_MIN_DELAY", "2s")
	t.Setenv("RECONNECT_MAX_DELAY", "120s")
	t.Setenv("RECONNECT_FACTOR", "3.0")
	t.Setenv("RECONNECT_JITTER_PCT", "0.5")

	cfg, err := config.Load()
	require.NoError(t, err)

	assert.Equal(t, "192.168.1.10", cfg.Modbus.Host)
	assert.Equal(t, 5020, cfg.Modbus.Port)
	assert.Equal(t, byte(5), cfg.Modbus.SlaveID)
	assert.Equal(t, 10*time.Second, cfg.Modbus.ConnectTimeout)
	assert.Equal(t, 3*time.Second, cfg.Modbus.ReadTimeout)
	assert.Equal(t, 4*time.Second, cfg.Modbus.WriteTimeout)
	assert.Equal(t, 200*time.Millisecond, cfg.Modbus.GuardInterval)

	assert.Equal(t, "mqtt.local:1884", cfg.MQTT.Broker)
	assert.Equal(t, "test-client", cfg.MQTT.ClientID)
	assert.Equal(t, "user", cfg.MQTT.Username)
	assert.Equal(t, "pass", cfg.MQTT.Password)
	assert.Equal(t, 45*time.Second, cfg.MQTT.Keepalive)
	assert.Equal(t, byte(2), cfg.MQTT.QoS)

	assert.Equal(t, "ha", cfg.HomeAssistant.DiscoveryPrefix)
	assert.Equal(t, "test_dev", cfg.HomeAssistant.DevicePrefix)
	assert.Equal(t, "Test Device", cfg.HomeAssistant.DeviceName)
	assert.Equal(t, "Acme", cfg.HomeAssistant.Manufacturer)
	assert.Equal(t, "Model X", cfg.HomeAssistant.Model)

	assert.Equal(t, 1*time.Second, cfg.Polling.HotInterval)
	assert.Equal(t, 10*time.Second, cfg.Polling.MediumInterval)
	assert.Equal(t, 120*time.Second, cfg.Polling.SlowInterval)
	assert.Equal(t, 45*time.Second, cfg.Polling.AvailabilityThreshold)

	assert.Equal(t, 9090, cfg.HTTP.Port)
	assert.Equal(t, "debug", cfg.Logger.Level)

	assert.Equal(t, 2*time.Second, cfg.Reconnect.MinDelay)
	assert.Equal(t, 120*time.Second, cfg.Reconnect.MaxDelay)
	assert.InDelta(t, 3.0, cfg.Reconnect.Factor, 0.0001)
	assert.InDelta(t, 0.5, cfg.Reconnect.JitterPct, 0.0001)
}

func TestLoad_InvalidPort(t *testing.T) {
	clearAllConfigEnvs(t)
	t.Setenv("MODBUS_PORT", "abc")

	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MODBUS_PORT")
}

func TestLoad_InvalidDuration(t *testing.T) {
	clearAllConfigEnvs(t)
	t.Setenv("MQTT_KEEPALIVE", "notaduration")

	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MQTT_KEEPALIVE")
}

func TestLoad_InvalidByte(t *testing.T) {
	clearAllConfigEnvs(t)
	t.Setenv("MODBUS_SLAVE_ID", "999")

	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "MODBUS_SLAVE_ID")
}

func TestLoad_InvalidFloat(t *testing.T) {
	clearAllConfigEnvs(t)
	t.Setenv("RECONNECT_FACTOR", "notafloat")

	_, err := config.Load()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "RECONNECT_FACTOR")
}

// validBaseConfig builds an in-memory Config that passes Validate.
func validBaseConfig() *config.Config {
	return &config.Config{
		Modbus: config.ModbusConfig{
			Host:           "10.0.0.1",
			Port:           502,
			SlaveID:        1,
			ConnectTimeout: 5 * time.Second,
			ReadTimeout:    2 * time.Second,
			WriteTimeout:   2 * time.Second,
			GuardInterval:  100 * time.Millisecond,
		},
		MQTT: config.MQTTConfig{
			Broker:    "mqtt:1883",
			ClientID:  "id",
			Keepalive: 30 * time.Second,
			QoS:       1,
		},
		HomeAssistant: config.HAConfig{
			DiscoveryPrefix: "homeassistant",
			DevicePrefix:    "oasis_syberia",
			DeviceName:      "Oasis Syberia",
			Manufacturer:    "GTC",
			Model:           "Syberia 5",
		},
		Polling: config.PollingConfig{
			HotInterval:           5 * time.Second,
			MediumInterval:        15 * time.Second,
			SlowInterval:          60 * time.Second,
			AvailabilityThreshold: 30 * time.Second,
		},
		HTTP:   config.HTTPConfig{Port: 8080},
		Logger: config.LoggerConfig{Level: "info"},
		Reconnect: config.ReconnectConfig{
			MinDelay:  1 * time.Second,
			MaxDelay:  60 * time.Second,
			Factor:    2.0,
			JitterPct: 0.2,
		},
	}
}

func TestValidate_Valid(t *testing.T) {
	t.Parallel()
	cfg := validBaseConfig()
	require.NoError(t, cfg.Validate())
}

func TestValidate_InvalidCases(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mutate    func(*config.Config)
		wantInErr string
	}{
		{
			name:      "empty modbus host",
			mutate:    func(c *config.Config) { c.Modbus.Host = "" },
			wantInErr: "MODBUS_HOST",
		},
		{
			name:      "modbus port zero",
			mutate:    func(c *config.Config) { c.Modbus.Port = 0 },
			wantInErr: "MODBUS_PORT",
		},
		{
			name:      "modbus port too high",
			mutate:    func(c *config.Config) { c.Modbus.Port = 70000 },
			wantInErr: "MODBUS_PORT",
		},
		{
			name:      "slave id zero",
			mutate:    func(c *config.Config) { c.Modbus.SlaveID = 0 },
			wantInErr: "MODBUS_SLAVE_ID",
		},
		{
			name:      "slave id too high",
			mutate:    func(c *config.Config) { c.Modbus.SlaveID = 248 },
			wantInErr: "MODBUS_SLAVE_ID",
		},
		{
			name:      "empty broker",
			mutate:    func(c *config.Config) { c.MQTT.Broker = "" },
			wantInErr: "MQTT_BROKER",
		},
		{
			name:      "empty client id",
			mutate:    func(c *config.Config) { c.MQTT.ClientID = "" },
			wantInErr: "MQTT_CLIENT_ID",
		},
		{
			name:      "qos too high",
			mutate:    func(c *config.Config) { c.MQTT.QoS = 3 },
			wantInErr: "MQTT_QOS",
		},
		{
			name:      "empty device prefix",
			mutate:    func(c *config.Config) { c.HomeAssistant.DevicePrefix = "" },
			wantInErr: "HA_DEVICE_PREFIX",
		},
		{
			name:      "http port zero",
			mutate:    func(c *config.Config) { c.HTTP.Port = 0 },
			wantInErr: "HTTP_PORT",
		},
		{
			name:      "hot greater than medium",
			mutate:    func(c *config.Config) { c.Polling.HotInterval = 30 * time.Second },
			wantInErr: "POLL_HOT_INTERVAL",
		},
		{
			name:      "medium greater than slow",
			mutate:    func(c *config.Config) { c.Polling.MediumInterval = 120 * time.Second },
			wantInErr: "POLL_MEDIUM_INTERVAL",
		},
		{
			name:      "factor equals one",
			mutate:    func(c *config.Config) { c.Reconnect.Factor = 1.0 },
			wantInErr: "RECONNECT_FACTOR",
		},
		{
			name:      "jitter negative",
			mutate:    func(c *config.Config) { c.Reconnect.JitterPct = -0.1 },
			wantInErr: "RECONNECT_JITTER_PCT",
		},
		{
			name:      "jitter above one",
			mutate:    func(c *config.Config) { c.Reconnect.JitterPct = 1.5 },
			wantInErr: "RECONNECT_JITTER_PCT",
		},
		{
			name:      "min delay greater than max",
			mutate:    func(c *config.Config) { c.Reconnect.MinDelay = 120 * time.Second },
			wantInErr: "RECONNECT_MIN_DELAY",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			cfg := validBaseConfig()
			tc.mutate(cfg)
			err := cfg.Validate()
			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.wantInErr)
		})
	}
}

func TestModbusConfig_Addr(t *testing.T) {
	t.Parallel()
	m := config.ModbusConfig{Host: "1.2.3.4", Port: 502}
	assert.Equal(t, "1.2.3.4:502", m.Addr())
}

func TestHTTPConfig_Addr(t *testing.T) {
	t.Parallel()
	h := config.HTTPConfig{Port: 8080}
	assert.Equal(t, "0.0.0.0:8080", h.Addr())
}
