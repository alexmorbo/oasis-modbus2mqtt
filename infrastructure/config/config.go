package config

import (
	"errors"
	"fmt"
	"strconv"
	"time"
)

// Config is the root application configuration loaded from environment variables.
type Config struct {
	Modbus        ModbusConfig
	MQTT          MQTTConfig
	HomeAssistant HAConfig
	Polling       PollingConfig
	HTTP          HTTPConfig
	Logger        LoggerConfig
	Reconnect     ReconnectConfig
}

// ModbusConfig holds Modbus TCP client settings.
type ModbusConfig struct {
	Host           string
	Port           int
	SlaveID        byte
	ConnectTimeout time.Duration
	ReadTimeout    time.Duration
	WriteTimeout   time.Duration
	GuardInterval  time.Duration
}

// Addr returns the Modbus TCP "host:port" address.
func (m ModbusConfig) Addr() string {
	return fmt.Sprintf("%s:%d", m.Host, m.Port)
}

// MQTTConfig holds MQTT broker connection settings.
type MQTTConfig struct {
	Broker    string
	ClientID  string
	Username  string
	Password  string
	Keepalive time.Duration
	QoS       byte
}

// HAConfig holds Home Assistant MQTT discovery settings.
type HAConfig struct {
	DiscoveryPrefix string
	DevicePrefix    string
	DeviceName      string
	Manufacturer    string
	Model           string
}

// PollingConfig holds tier intervals and availability threshold.
type PollingConfig struct {
	HotInterval           time.Duration
	MediumInterval        time.Duration
	SlowInterval          time.Duration
	AvailabilityThreshold time.Duration
}

// HTTPConfig holds the HTTP server port for /health and /metrics.
type HTTPConfig struct {
	Port int
}

// Addr returns the HTTP listen address bound to all interfaces.
func (h HTTPConfig) Addr() string {
	return "0.0.0.0:" + strconv.Itoa(h.Port)
}

// LoggerConfig holds logging level (format is always JSON).
type LoggerConfig struct {
	Level string
}

// ReconnectConfig holds backoff parameters for reconnect loops.
type ReconnectConfig struct {
	MinDelay  time.Duration
	MaxDelay  time.Duration
	Factor    float64
	JitterPct float64
}

// Load reads configuration from environment variables, applies defaults, and validates.
func Load() (*Config, error) {
	cfg := &Config{}
	var err error

	// Modbus
	cfg.Modbus.Host = getEnvOrDefault("MODBUS_HOST", "10.90.19.7")
	if cfg.Modbus.Port, err = getEnvOrDefaultInt("MODBUS_PORT", 502); err != nil {
		return nil, fmt.Errorf("loading MODBUS_PORT: %w", err)
	}
	if cfg.Modbus.SlaveID, err = getEnvOrDefaultByte("MODBUS_SLAVE_ID", 1); err != nil {
		return nil, fmt.Errorf("loading MODBUS_SLAVE_ID: %w", err)
	}
	if cfg.Modbus.ConnectTimeout, err = getEnvOrDefaultDuration("MODBUS_CONNECT_TIMEOUT", 5*time.Second); err != nil {
		return nil, fmt.Errorf("loading MODBUS_CONNECT_TIMEOUT: %w", err)
	}
	if cfg.Modbus.ReadTimeout, err = getEnvOrDefaultDuration("MODBUS_READ_TIMEOUT", 2*time.Second); err != nil {
		return nil, fmt.Errorf("loading MODBUS_READ_TIMEOUT: %w", err)
	}
	if cfg.Modbus.WriteTimeout, err = getEnvOrDefaultDuration("MODBUS_WRITE_TIMEOUT", 2*time.Second); err != nil {
		return nil, fmt.Errorf("loading MODBUS_WRITE_TIMEOUT: %w", err)
	}
	if cfg.Modbus.GuardInterval, err = getEnvOrDefaultDuration("MODBUS_GUARD_INTERVAL", 100*time.Millisecond); err != nil {
		return nil, fmt.Errorf("loading MODBUS_GUARD_INTERVAL: %w", err)
	}

	// MQTT
	cfg.MQTT.Broker = getEnvOrDefault("MQTT_BROKER", "10.90.19.10:1883")
	cfg.MQTT.ClientID = getEnvOrDefault("MQTT_CLIENT_ID", "oasis-modbus2mqtt")
	cfg.MQTT.Username = getEnvOrDefault("MQTT_USERNAME", "")
	cfg.MQTT.Password = getEnvOrDefault("MQTT_PASSWORD", "")
	if cfg.MQTT.Keepalive, err = getEnvOrDefaultDuration("MQTT_KEEPALIVE", 30*time.Second); err != nil {
		return nil, fmt.Errorf("loading MQTT_KEEPALIVE: %w", err)
	}
	if cfg.MQTT.QoS, err = getEnvOrDefaultByte("MQTT_QOS", 1); err != nil {
		return nil, fmt.Errorf("loading MQTT_QOS: %w", err)
	}

	// HomeAssistant
	cfg.HomeAssistant.DiscoveryPrefix = getEnvOrDefault("HA_DISCOVERY_PREFIX", "homeassistant")
	cfg.HomeAssistant.DevicePrefix = getEnvOrDefault("HA_DEVICE_PREFIX", "oasis_syberia")
	cfg.HomeAssistant.DeviceName = getEnvOrDefault("HA_DEVICE_NAME", "Oasis Syberia")
	cfg.HomeAssistant.Manufacturer = getEnvOrDefault("HA_MANUFACTURER", "GTC")
	cfg.HomeAssistant.Model = getEnvOrDefault("HA_MODEL", "Syberia 5")

	// Polling
	if cfg.Polling.HotInterval, err = getEnvOrDefaultDuration("POLL_HOT_INTERVAL", 5*time.Second); err != nil {
		return nil, fmt.Errorf("loading POLL_HOT_INTERVAL: %w", err)
	}
	if cfg.Polling.MediumInterval, err = getEnvOrDefaultDuration("POLL_MEDIUM_INTERVAL", 15*time.Second); err != nil {
		return nil, fmt.Errorf("loading POLL_MEDIUM_INTERVAL: %w", err)
	}
	if cfg.Polling.SlowInterval, err = getEnvOrDefaultDuration("POLL_SLOW_INTERVAL", 60*time.Second); err != nil {
		return nil, fmt.Errorf("loading POLL_SLOW_INTERVAL: %w", err)
	}
	if cfg.Polling.AvailabilityThreshold, err = getEnvOrDefaultDuration("AVAILABILITY_THRESHOLD", 30*time.Second); err != nil {
		return nil, fmt.Errorf("loading AVAILABILITY_THRESHOLD: %w", err)
	}

	// HTTP
	if cfg.HTTP.Port, err = getEnvOrDefaultInt("HTTP_PORT", 8080); err != nil {
		return nil, fmt.Errorf("loading HTTP_PORT: %w", err)
	}

	// Logger
	cfg.Logger.Level = getEnvOrDefault("LOG_LEVEL", "info")

	// Reconnect
	if cfg.Reconnect.MinDelay, err = getEnvOrDefaultDuration("RECONNECT_MIN_DELAY", 1*time.Second); err != nil {
		return nil, fmt.Errorf("loading RECONNECT_MIN_DELAY: %w", err)
	}
	if cfg.Reconnect.MaxDelay, err = getEnvOrDefaultDuration("RECONNECT_MAX_DELAY", 60*time.Second); err != nil {
		return nil, fmt.Errorf("loading RECONNECT_MAX_DELAY: %w", err)
	}
	if cfg.Reconnect.Factor, err = getEnvOrDefaultFloat("RECONNECT_FACTOR", 2.0); err != nil {
		return nil, fmt.Errorf("loading RECONNECT_FACTOR: %w", err)
	}
	if cfg.Reconnect.JitterPct, err = getEnvOrDefaultFloat("RECONNECT_JITTER_PCT", 0.2); err != nil {
		return nil, fmt.Errorf("loading RECONNECT_JITTER_PCT: %w", err)
	}

	if err := cfg.Validate(); err != nil {
		return nil, fmt.Errorf("loading config: %w", err)
	}
	return cfg, nil
}

// Validate checks invariants across all sub-configs.
func (c *Config) Validate() error {
	if c.Modbus.Host == "" {
		return errors.New("validate: MODBUS_HOST is empty")
	}
	if c.Modbus.Port < 1 || c.Modbus.Port > 65535 {
		return fmt.Errorf("validate: MODBUS_PORT out of range [1,65535]: %d", c.Modbus.Port)
	}
	if c.Modbus.SlaveID < 1 || c.Modbus.SlaveID > 247 {
		return fmt.Errorf("validate: MODBUS_SLAVE_ID out of range [1,247]: %d", c.Modbus.SlaveID)
	}
	if c.Modbus.ConnectTimeout <= 0 {
		return fmt.Errorf("validate: MODBUS_CONNECT_TIMEOUT must be > 0: %s", c.Modbus.ConnectTimeout)
	}
	if c.Modbus.ReadTimeout <= 0 {
		return fmt.Errorf("validate: MODBUS_READ_TIMEOUT must be > 0: %s", c.Modbus.ReadTimeout)
	}
	if c.Modbus.WriteTimeout <= 0 {
		return fmt.Errorf("validate: MODBUS_WRITE_TIMEOUT must be > 0: %s", c.Modbus.WriteTimeout)
	}
	if c.Modbus.GuardInterval < 0 {
		return fmt.Errorf("validate: MODBUS_GUARD_INTERVAL must be >= 0: %s", c.Modbus.GuardInterval)
	}

	if c.MQTT.Broker == "" {
		return errors.New("validate: MQTT_BROKER is empty")
	}
	if c.MQTT.ClientID == "" {
		return errors.New("validate: MQTT_CLIENT_ID is empty")
	}
	if c.MQTT.QoS > 2 {
		return fmt.Errorf("validate: MQTT_QOS out of range [0,2]: %d", c.MQTT.QoS)
	}
	if c.MQTT.Keepalive <= 0 {
		return fmt.Errorf("validate: MQTT_KEEPALIVE must be > 0: %s", c.MQTT.Keepalive)
	}

	if c.HomeAssistant.DevicePrefix == "" {
		return errors.New("validate: HA_DEVICE_PREFIX is empty")
	}

	if c.HTTP.Port < 1 || c.HTTP.Port > 65535 {
		return fmt.Errorf("validate: HTTP_PORT out of range [1,65535]: %d", c.HTTP.Port)
	}

	if c.Polling.HotInterval <= 0 {
		return fmt.Errorf("validate: POLL_HOT_INTERVAL must be > 0: %s", c.Polling.HotInterval)
	}
	if c.Polling.MediumInterval <= 0 {
		return fmt.Errorf("validate: POLL_MEDIUM_INTERVAL must be > 0: %s", c.Polling.MediumInterval)
	}
	if c.Polling.SlowInterval <= 0 {
		return fmt.Errorf("validate: POLL_SLOW_INTERVAL must be > 0: %s", c.Polling.SlowInterval)
	}
	if c.Polling.AvailabilityThreshold <= 0 {
		return fmt.Errorf("validate: AVAILABILITY_THRESHOLD must be > 0: %s", c.Polling.AvailabilityThreshold)
	}
	if c.Polling.HotInterval > c.Polling.MediumInterval {
		return fmt.Errorf("validate: POLL_HOT_INTERVAL (%s) must be <= POLL_MEDIUM_INTERVAL (%s)",
			c.Polling.HotInterval, c.Polling.MediumInterval)
	}
	if c.Polling.MediumInterval > c.Polling.SlowInterval {
		return fmt.Errorf("validate: POLL_MEDIUM_INTERVAL (%s) must be <= POLL_SLOW_INTERVAL (%s)",
			c.Polling.MediumInterval, c.Polling.SlowInterval)
	}

	if c.Reconnect.MinDelay <= 0 {
		return fmt.Errorf("validate: RECONNECT_MIN_DELAY must be > 0: %s", c.Reconnect.MinDelay)
	}
	if c.Reconnect.MaxDelay <= 0 {
		return fmt.Errorf("validate: RECONNECT_MAX_DELAY must be > 0: %s", c.Reconnect.MaxDelay)
	}
	if c.Reconnect.MinDelay > c.Reconnect.MaxDelay {
		return fmt.Errorf("validate: RECONNECT_MIN_DELAY (%s) must be <= RECONNECT_MAX_DELAY (%s)",
			c.Reconnect.MinDelay, c.Reconnect.MaxDelay)
	}
	if c.Reconnect.Factor <= 1.0 {
		return fmt.Errorf("validate: RECONNECT_FACTOR must be > 1.0: %f", c.Reconnect.Factor)
	}
	if c.Reconnect.JitterPct < 0 || c.Reconnect.JitterPct > 1.0 {
		return fmt.Errorf("validate: RECONNECT_JITTER_PCT out of range [0,1]: %f", c.Reconnect.JitterPct)
	}

	return nil
}
