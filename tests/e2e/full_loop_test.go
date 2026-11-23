package e2e

import (
	"context"
	"encoding/binary"
	"io"
	"log/slog"
	"testing"
	"time"

	paho "github.com/eclipse/paho.mqtt.golang"
	"github.com/google/uuid"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"github.com/tbrandon/mbserver"

	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/config"
	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/metrics"
	"github.com/alexmorbo/oasis-modbus2mqtt/internal/app"
)

// preset writes the test fixture register values onto the mbserver. The
// values match the AC: firmware v5.2.0, PowerOn=true with HEAT mode,
// supply temp 22.0 °C, room temp 23.5 °C, target 22.0 °C, electric heater,
// filter resource 150 days with 120 days left, and the panel settings of the
// 2026-11-23 live dump.
func preset(s *mbserver.Server) {
	s.InputRegisters[0] = 0x5200    // Firmware v5.2.0
	s.InputRegisters[2] = 0x0141    // State_0: PowerOn=1, HeatCapable, mode=HEAT
	s.InputRegisters[9] = 220       // T1 supply 22.0 °C (×0.1)
	s.InputRegisters[57] = 235      // Room temperature 23.5 °C (×0.1)
	s.HoldingRegisters[0] = 0x0001  // Type_Dev: electric heater
	s.HoldingRegisters[31] = 220    // Target 22.0 °C (×0.1)
	s.HoldingRegisters[86] = 0x0001 // ModeHeat
	s.HoldingRegisters[344] = 150   // Filter resource, days
	s.HoldingRegisters[346] = 120   // Filter days left
	s.HoldingRegisters[22] = 0x07E4 // User_Keys_0 (live)
	s.HoldingRegisters[23] = 20     // Dark_Brightness
	s.HoldingRegisters[24] = 3      // IR_Remoute_Time
	s.HoldingRegisters[25] = 0x0004 // User_Dev_0: active filter
	s.HoldingRegisters[35] = 50     // e_Hum_Target
	s.HoldingRegisters[36] = 500    // e_Room_CO2_Target
}

// publishCmd publishes one HA command payload to the bridge.
func publishCmd(t *testing.T, c paho.Client, objectID, payload string) {
	t.Helper()
	tok := c.Publish("oasis_test/cmd/"+objectID, 1, false, payload)
	require.True(t, tok.WaitTimeout(2*time.Second))
	require.NoError(t, tok.Error())
}

// settingRoundTrip sends a setting command and waits until both the holding
// register on the mbserver and the retained state topic show the result.
func settingRoundTrip(
	t *testing.T, server *mbserver.Server, c paho.Client, states func() map[string][]byte,
	objectID, payload string, addr int, wantReg uint16, wantState string,
) {
	t.Helper()
	before := server.HoldingRegisters[addr]
	publishCmd(t, c, objectID, payload)
	assert.Eventuallyf(t, func() bool {
		return server.HoldingRegisters[addr] == wantReg &&
			string(states()["oasis_test/state/"+objectID]) == wantState
	}, 5*time.Second, 20*time.Millisecond, "%s=%s: h%d=0x%04X and state %q", objectID, payload, addr, wantReg, wantState)
	t.Logf("round trip cmd/%s %q: mbserver h%d 0x%04X -> 0x%04X, state/%s = %q",
		objectID, payload, addr, before, server.HoldingRegisters[addr], objectID, states()["oasis_test/state/"+objectID])
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

	cfg := e2eConfig(t, modbusAddr, mqttBroker)

	testClient := newTestPahoClient(t, mqttBroker, "test-observer-"+uuid.NewString())
	discoveryGetter := subscribeAndCollect(t, testClient, "homeassistant/+/oasis_test/+/config")
	stateGetter := subscribeAndCollect(t, testClient, "oasis_test/state/+")
	availabilityGetter := subscribeAndCollect(t, testClient, "oasis_test/availability")
	daysLeftHistory := subscribeAndRecordAll(t, testClient, "oasis_test/state/filter_days_left")

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
		return len(discoveryGetter()) == 32
	}, 30*time.Second, 200*time.Millisecond, "32 discovery configs published on initial connect")

	configs := discoveryGetter()
	assert.Contains(t, string(configs["homeassistant/select/oasis_test/control_priority/config"]), `"entity_category":"config"`)
	assert.Contains(t, string(configs["homeassistant/number/oasis_test/co2_target/config"]), `"default_entity_id":"number.oasis_test_co2_target"`)
	assert.Contains(t, string(configs["homeassistant/climate/oasis_test/climate/config"]), `"default_entity_id":"climate.oasis_test"`)
	assert.Contains(t, string(configs["homeassistant/sensor/oasis_test/filter_days_left/config"]), `"device_class":"duration"`)
	assert.Contains(t, string(configs["homeassistant/sensor/oasis_test/filter_resource/config"]), `"entity_category":"diagnostic"`)

	assert.Eventually(t, func() bool {
		states := stateGetter()
		return string(states["oasis_test/state/filter_days_left"]) == "120" &&
			string(states["oasis_test/state/filter_resource"]) == "150"
	}, 5*time.Second, 100*time.Millisecond, "filter_days_left=120 and filter_resource=150")
	assert.NotContains(t, daysLeftHistory(), "0",
		"filter_days_left must never publish a placeholder 0 before the slow tier has read h346")

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

	assert.Eventually(t, func() bool {
		states := stateGetter()
		return string(states["oasis_test/state/alarm_beep"]) == "ON" &&
			string(states["oasis_test/state/control_priority"]) == "temperature" &&
			string(states["oasis_test/state/humidity_target"]) == "50" &&
			string(states["oasis_test/state/active_filter"]) == "ON"
	}, 5*time.Second, 50*time.Millisecond, "panel settings published after the slow-tier read")

	settingRoundTrip(t, server, testClient, stateGetter, "alarm_beep", "OFF", 22, 0x07A4, "OFF")
	assert.NotZero(t, server.HoldingRegisters[22]&(1<<2), "unexposed h22 bit 2 preserved")
	settingRoundTrip(t, server, testClient, stateGetter, "control_priority", "co2", 22, 0x47A4, "co2")
	settingRoundTrip(t, server, testClient, stateGetter, "humidity_target", "55.0", 35, 55, "55")

	publishCmd(t, testClient, "humidity_target", "57.5")
	publishCmd(t, testClient, "humidity_target", "200")
	publishCmd(t, testClient, "control_priority", "3")
	assert.Never(t, func() bool {
		return server.HoldingRegisters[35] != 55 || server.HoldingRegisters[22] != 0x47A4
	}, time.Second, 20*time.Millisecond, "invalid setting payloads never reach Modbus")
	assert.Equal(t, "55", string(stateGetter()["oasis_test/state/humidity_target"]))
	assert.Equal(t, "co2", string(stateGetter()["oasis_test/state/control_priority"]))

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

// TestRetainedStateSurvivesSubscriberReconnect boots the wired application,
// waits for the bridge to populate the broker with retained state, then
// connects a *fresh* observer client (no shared session) and verifies that
// stable values like firmware and hvac_mode arrive immediately from the
// retained store — not after the next poll. This is the regression guard
// for the 2026-04-30 incident where firmware/heater_active stuck in
// "unknown" after an HA-side resubscribe.
//
//nolint:misspell // mosquitto is the broker's correct name
func TestRetainedStateSurvivesSubscriberReconnect(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e test requires Docker; skipped under -short")
	}
	if raceEnabled {
		t.Skip("e2e: skipping under -race due to known mbserver upstream data race on HoldingRegisters slice")
	}

	server, modbusAddr := startMbserver(t)
	preset(server)

	mqttBroker := startMosquitto(t)

	cfg := e2eConfig(t, modbusAddr, mqttBroker)

	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	a, err := app.New(cfg, logger)
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	// Wait until at least one full slow-tier poll has happened, so the broker
	// is guaranteed to hold retained values for every state topic.
	primary := newTestPahoClient(t, mqttBroker, "test-primary-"+uuid.NewString())
	primaryStates := subscribeAndCollect(t, primary, "oasis_test/state/+")

	assert.Eventually(t, func() bool {
		states := primaryStates()
		return string(states["oasis_test/state/firmware"]) == "v5.2.0" &&
			string(states["oasis_test/state/hvac_mode"]) == "heat"
	}, 30*time.Second, 100*time.Millisecond, "primary client sees firmware and hvac_mode populated")

	// Now connect a *fresh* subscriber that joins after the bridge has been
	// running for a while. With retained=true on every state publish, the
	// broker must replay the latest value immediately on subscribe — even
	// for stable signals that have not changed since boot.
	observer := newTestPahoClient(t, mqttBroker, "test-observer-late-"+uuid.NewString())
	observerStates := subscribeAndCollect(t, observer, "oasis_test/state/+")

	assert.Eventually(t, func() bool {
		states := observerStates()
		return string(states["oasis_test/state/firmware"]) == "v5.2.0" &&
			string(states["oasis_test/state/hvac_mode"]) == "heat" &&
			string(states["oasis_test/state/heater_active"]) != "" &&
			string(states["oasis_test/state/damper_open"]) != "" &&
			string(states["oasis_test/state/device_id"]) != ""
	}, 5*time.Second, 50*time.Millisecond,
		"late observer must receive retained state for stable entities")

	cancel()
	select {
	case runErr := <-done:
		require.NoError(t, runErr, "app.Run should return cleanly on ctx cancel")
	case <-time.After(15 * time.Second):
		t.Fatal("app.Run did not return within shutdown timeout")
	}
}

// TestDiscoveryRepublishedOnMQTTReconnect drops the bridge's broker session
// (session takeover by a client with the same ClientID) after wiping a
// retained discovery config, and asserts the on-connect hook republishes
// discovery without any HA birth message. Regression guard for discovery
// lost after a broker restart without persistence.
//
//nolint:misspell // mosquitto is the broker's correct name
func TestDiscoveryRepublishedOnMQTTReconnect(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e test requires Docker; skipped under -short")
	}
	if raceEnabled {
		t.Skip("e2e: skipping under -race due to known mbserver upstream data race on HoldingRegisters slice")
	}

	server, modbusAddr := startMbserver(t)
	preset(server)
	mqttBroker := startMosquitto(t)
	cfg := e2eConfig(t, modbusAddr, mqttBroker)

	a, err := app.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	const configTopic = "homeassistant/sensor/oasis_test/filter_days_left/config"
	observer := newTestPahoClient(t, mqttBroker, "test-observer-"+uuid.NewString())
	discoveryGetter := subscribeAndCollect(t, observer, "homeassistant/+/oasis_test/+/config")

	assert.Eventually(t, func() bool {
		return len(discoveryGetter()[configTopic]) > 0
	}, 30*time.Second, 100*time.Millisecond, "initial discovery for filter_days_left")

	// Wipe the retained config, as a broker restart without persistence would.
	tok := observer.Publish(configTopic, 1, true, []byte{})
	require.True(t, tok.WaitTimeout(2*time.Second))
	require.NoError(t, tok.Error())
	assert.Eventually(t, func() bool {
		return len(discoveryGetter()[configTopic]) == 0
	}, 5*time.Second, 50*time.Millisecond, "retained config cleared")

	intruder := newTestPahoClient(t, mqttBroker, cfg.MQTT.ClientID)
	intruder.Disconnect(0)

	assert.Eventually(t, func() bool {
		return len(discoveryGetter()[configTopic]) > 0
	}, 30*time.Second, 100*time.Millisecond, "discovery republished after MQTT reconnect")

	late := newTestPahoClient(t, mqttBroker, "test-late-"+uuid.NewString())
	lateGetter := subscribeAndCollect(t, late, configTopic)
	assert.Eventually(t, func() bool {
		return len(lateGetter()[configTopic]) > 0
	}, 5*time.Second, 50*time.Millisecond, "republished config is retained")

	// The setting command topics must be resubscribed on the new session.
	states := subscribeAndCollect(t, observer, "oasis_test/state/+")
	assert.Eventually(t, func() bool {
		return string(states()["oasis_test/state/touch_click"]) == "ON"
	}, 5*time.Second, 50*time.Millisecond, "touch_click state before the post-reconnect command")
	settingRoundTrip(t, server, observer, states, "touch_click", "OFF", 22, 0x07C4, "OFF")

	cancel()
	select {
	case runErr := <-done:
		require.NoError(t, runErr, "app.Run should return cleanly on ctx cancel")
	case <-time.After(15 * time.Second):
		t.Fatal("app.Run did not return within shutdown timeout")
	}
}

// TestE2E_SettingWriteNotApplied registers an FC06 handler that ACKs writes
// to h36 without storing them, like the controller does for undocumented
// registers. The bridge must publish the actual value, not the request, and
// count the mismatch.
//
//nolint:misspell // mosquitto is the broker's correct name
func TestE2E_SettingWriteNotApplied(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e test requires Docker; skipped under -short")
	}
	if raceEnabled {
		t.Skip("e2e: skipping under -race due to known mbserver upstream data race on HoldingRegisters slice")
	}

	server, modbusAddr := startMbserver(t)
	preset(server)
	server.RegisterFunctionHandler(6, func(s *mbserver.Server, frame mbserver.Framer) ([]byte, *mbserver.Exception) {
		data := frame.GetData()
		if len(data) >= 4 && binary.BigEndian.Uint16(data[0:2]) == 36 {
			return data[0:4], &mbserver.Success
		}
		return mbserver.WriteHoldingRegister(s, frame)
	})
	mqttBroker := startMosquitto(t)
	cfg := e2eConfig(t, modbusAddr, mqttBroker)

	client := newTestPahoClient(t, mqttBroker, "test-observer-"+uuid.NewString())
	states := subscribeAndCollect(t, client, "oasis_test/state/+")
	co2History := subscribeAndRecordAll(t, client, "oasis_test/state/co2_target")

	a, err := app.New(cfg, slog.New(slog.NewTextHandler(io.Discard, nil)))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	assert.Eventually(t, func() bool {
		return string(states()["oasis_test/state/co2_target"]) == "500"
	}, 30*time.Second, 50*time.Millisecond, "co2_target=500 before the write")

	counter := metrics.WriteNotAppliedTotal("co2_target")
	before := counter.Get()
	publishCmd(t, client, "co2_target", "800")
	assert.Eventually(t, func() bool { return counter.Get() == before+1 }, 5*time.Second, 20*time.Millisecond,
		"oasis_modbus_write_not_applied_total{entity=co2_target} incremented")
	t.Logf("ignored write cmd/co2_target \"800\": mbserver h36 500 -> %d, state/co2_target = %q, write_not_applied %d -> %d",
		server.HoldingRegisters[36], states()["oasis_test/state/co2_target"], before, counter.Get())

	assert.Equal(t, uint16(500), server.HoldingRegisters[36])
	assert.Equal(t, "500", string(states()["oasis_test/state/co2_target"]))
	assert.NotContains(t, co2History(), "800", "the requested value is never published")

	settingRoundTrip(t, server, client, states, "dark_brightness", "35", 23, 35, "35")

	cancel()
	select {
	case runErr := <-done:
		require.NoError(t, runErr)
	case <-time.After(15 * time.Second):
		t.Fatal("app.Run did not return within shutdown timeout")
	}
}

// TestE2E_SettingCommandBurst sends 8 different setting commands back to
// back with production poll intervals and guard interval. Regression guard
// for commands applied on the paho router goroutine: each apply publishes the
// full retained state set at QoS 1, and with the router blocked no PUBACK is
// processed, so a burst of 4+ commands froze every publish for 5 s each.
//
//nolint:misspell // mosquitto is the broker's correct name
func TestE2E_SettingCommandBurst(t *testing.T) {
	if testing.Short() {
		t.Skip("e2e test requires Docker; skipped under -short")
	}
	if raceEnabled {
		t.Skip("e2e: skipping under -race due to known mbserver upstream data race on HoldingRegisters slice")
	}

	server, modbusAddr := startMbserver(t)
	preset(server)
	mqttBroker := startMosquitto(t)
	cfg := e2eConfig(t, modbusAddr, mqttBroker)
	cfg.Modbus.GuardInterval = 100 * time.Millisecond
	cfg.Polling = config.PollingConfig{
		HotInterval:           5 * time.Second,
		MediumInterval:        15 * time.Second,
		SlowInterval:          60 * time.Second,
		AvailabilityThreshold: 30 * time.Second,
	}

	client := newTestPahoClient(t, mqttBroker, "test-observer-"+uuid.NewString())
	states := subscribeAndCollect(t, client, "oasis_test/state/+")

	logs := &lockedBuffer{}
	a, err := app.New(cfg, slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelWarn})))
	require.NoError(t, err)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- a.Run(ctx) }()

	require.Eventually(t, func() bool {
		st := states()
		return string(st["oasis_test/state/alarm_beep"]) == "ON" &&
			string(st["oasis_test/state/co2_target"]) == "500"
	}, 30*time.Second, 50*time.Millisecond, "settings published after the first slow poll")

	burst := []struct {
		objectID, payload, wantState string
	}{
		{"alarm_beep", "OFF", "OFF"},
		{"touch_click", "OFF", "OFF"},
		{"nord_start", "ON", "ON"},
		{"control_priority", "humidity", "humidity"},
		{"active_filter", "OFF", "OFF"},
		{"dark_brightness", "35", "35"},
		{"humidity_target", "60", "60"},
		{"co2_target", "900", "900"},
	}
	start := time.Now()
	for _, c := range burst {
		publishCmd(t, client, c.objectID, c.payload)
	}
	converged := func() bool {
		st := states()
		for _, c := range burst {
			if string(st["oasis_test/state/"+c.objectID]) != c.wantState {
				return false
			}
		}
		return server.HoldingRegisters[22] == 0x2786 && server.HoldingRegisters[23] == 35 &&
			server.HoldingRegisters[25] == 0 && server.HoldingRegisters[35] == 60 &&
			server.HoldingRegisters[36] == 900
	}
	ok := assert.Eventually(t, converged, 4*time.Second, 10*time.Millisecond,
		"all 8 setting commands applied in mbserver and published")
	elapsed := time.Since(start)
	t.Logf("burst of %d setting commands: converged=%v after %s (h22=0x%04X h23=%d h25=0x%04X h35=%d h36=%d)",
		len(burst), ok, elapsed.Round(time.Millisecond), server.HoldingRegisters[22], server.HoldingRegisters[23],
		server.HoldingRegisters[25], server.HoldingRegisters[35], server.HoldingRegisters[36])
	assert.Less(t, elapsed, 3500*time.Millisecond, "burst converges in about 3 s")

	out := logs.String()
	assert.NotContains(t, out, "state publish completed with errors")
	assert.NotContains(t, out, "mqtt publish failed")
	assert.NotContains(t, out, "command queue full")

	cancel()
	select {
	case runErr := <-done:
		require.NoError(t, runErr)
	case <-time.After(15 * time.Second):
		t.Fatal("app.Run did not return within shutdown timeout")
	}
}

func e2eConfig(t *testing.T, modbusAddr, mqttBroker string) *config.Config {
	t.Helper()
	modbusHost, modbusPort := splitAddr(t, modbusAddr)
	return &config.Config{
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
}
