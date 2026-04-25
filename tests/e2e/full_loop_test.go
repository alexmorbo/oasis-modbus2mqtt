package e2e

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tbrandon/mbserver"

	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/config"
	"github.com/alexmorbo/oasis-modbus2mqtt/internal/app"
)

// preset writes the test fixture register values onto the mbserver. The
// values match the AC: firmware v5.2.0, PowerOn=true with HEAT mode,
// supply temp 22.0 °C, room temp 23.5 °C, target 22.0 °C, electric heater.
func preset(s *mbserver.Server) {
	s.InputRegisters[0] = 0x5200    // Firmware v5.2.0
	s.InputRegisters[2] = 0x0141    // State_0: PowerOn=1, HeatCapable, mode=HEAT
	s.InputRegisters[9] = 220       // T1 supply 22.0 °C (×0.1)
	s.InputRegisters[57] = 235      // Room temperature 23.5 °C (×0.1)
	s.HoldingRegisters[0] = 0x0001  // Type_Dev: electric heater
	s.HoldingRegisters[31] = 220    // Target 22.0 °C (×0.1)
	s.HoldingRegisters[86] = 0x0001 // ModeHeat
}

// TestE2E_FullLoop boots the wired application against an in-process
// mbserver and a mosquitto testcontainer, and asserts the full happy path:
// initial discovery, periodic state, command roundtrip and graceful
// shutdown with a retained "offline" availability message.
//
//nolint:misspell // mosquitto is the broker's correct name
func TestE2E_FullLoop(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e test requires Docker; skipped under -short")
	}
	if raceEnabled {
		t.Skip("e2e: skipping under -race due to known mbserver upstream data race on HoldingRegisters slice")
	}

	server, modbusAddr := startMbserver(t)
	preset(server)

	mqttBroker := startMosquitto(t)

	modbusHost, modbusPort := splitAddr(t, modbusAddr)
	cfg := &config.Config{
		Modbus: config.ModbusConfig{
			Host:           modbusHost,
			Port:           modbusPort,
			SlaveID:        1,
			ConnectTimeout: 2 * time.Second,
			ReadTimeout:    1 * time.Second,
			WriteTimeout:   1 * time.Second,
			GuardInterval:  20 * time.Millisecond,
		},
		MQTT: config.MQTTConfig{
			Broker:    mqttBroker,
			ClientID:  "oasis-e2e-bridge-" + uuid.NewString(),
			Keepalive: 5 * time.Second,
			QoS:       1,
		},
		HomeAssistant: config.HAConfig{
			DiscoveryPrefix: "homeassistant",
			DevicePrefix:    "oasis_test",
			DeviceName:      "Oasis E2E",
			Manufacturer:    "Syberia",
			Model:           "TestModel",
		},
		Polling: config.PollingConfig{
			HotInterval:           200 * time.Millisecond,
			MediumInterval:        400 * time.Millisecond,
			SlowInterval:          1 * time.Second,
			AvailabilityThreshold: 2 * time.Second,
		},
		HTTP:   config.HTTPConfig{Port: 0},
		Logger: config.LoggerConfig{Level: "warn"},
		Reconnect: config.ReconnectConfig{
			MinDelay:  1 * time.Millisecond,
			MaxDelay:  10 * time.Millisecond,
			Factor:    2.0,
			JitterPct: 0,
		},
	}

	testClient := newTestPahoClient(t, mqttBroker, "test-observer-"+uuid.NewString())
	discoveryGetter := subscribeAndCollect(t, testClient, "homeassistant/+/oasis_test/+/config")
	stateGetter := subscribeAndCollect(t, testClient, "oasis_test/state/+")
	availabilityGetter := subscribeAndCollect(t, testClient, "oasis_test/availability")

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := app.New(cfg, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	assert.Eventually(t, func() bool {
		return string(availabilityGetter()["oasis_test/availability"]) == "online"
	}, 30*time.Second, 100*time.Millisecond, "availability=online")

	assert.Eventually(t, func() bool {
		return len(discoveryGetter()) >= 13
	}, 30*time.Second, 200*time.Millisecond, "at least 13 discovery configs published")

	assert.Eventually(t, func() bool {
		states := stateGetter()
		return string(states["oasis_test/state/supply_temperature"]) == "22.0" &&
			string(states["oasis_test/state/firmware"]) == "v5.2.0"
	}, 5*time.Second, 100*time.Millisecond, "supply_temperature=22.0 and firmware=v5.2.0")

	assert.Eventually(t, func() bool {
		return string(stateGetter()["oasis_test/state/room_temperature"]) == "23.5"
	}, 5*time.Second, 100*time.Millisecond, "room_temperature=23.5")

	pubToken := testClient.Publish("oasis_test/cmd/power", 1, false, "OFF")
	require.True(t, pubToken.WaitTimeout(2*time.Second))
	require.NoError(t, pubToken.Error())

	// Power_Dev OFF emits an edge sequence (write 1, then 0) on holding 2.
	// Wait for the trailing zero to confirm the dispatcher completed.
	assert.Eventually(t, func() bool {
		return server.HoldingRegisters[2] == 0
	}, 5*time.Second, 50*time.Millisecond, "Power_Dev OFF edge sequence settles to 0")

	pubToken = testClient.Publish("oasis_test/cmd/target_temperature", 1, false, "23.5")
	require.True(t, pubToken.WaitTimeout(2*time.Second))
	require.NoError(t, pubToken.Error())

	assert.Eventually(t, func() bool {
		return server.HoldingRegisters[31] == 235
	}, 5*time.Second, 50*time.Millisecond, "target temperature written to holding 31 (235)")

	cancel()
	select {
	case runErr := <-done:
		require.NoError(t, runErr, "app.Run should return cleanly on ctx cancel")
	case <-time.After(15 * time.Second):
		t.Fatal("app.Run did not return within shutdown timeout")
	}

	// MQTT Disconnect publishes retained "offline" before tearing the socket
	// down, so any subscriber on `oasis_test/availability` (including ours)
	// receives the final retained payload after shutdown completes.
	assert.Eventually(t, func() bool {
		return string(availabilityGetter()["oasis_test/availability"]) == "offline"
	}, 5*time.Second, 100*time.Millisecond, "retained offline availability after shutdown")
}
