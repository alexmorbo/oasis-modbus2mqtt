package mqtt_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/dto"
	"github.com/alexmorbo/oasis-modbus2mqtt/application/port"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"
	imqtt "github.com/alexmorbo/oasis-modbus2mqtt/interface/mqtt"
)

type fakeSubscriber struct {
	mu       sync.Mutex
	handlers map[string]port.MessageHandler
	subErr   error
}

func (s *fakeSubscriber) Subscribe(_ context.Context, topic string, h port.MessageHandler) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subErr != nil {
		return s.subErr
	}
	if s.handlers == nil {
		s.handlers = make(map[string]port.MessageHandler)
	}
	s.handlers[topic] = h
	return nil
}

func (s *fakeSubscriber) handler(topic string) (port.MessageHandler, bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.handlers[topic]
	return h, ok
}

func (s *fakeSubscriber) topics() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, 0, len(s.handlers))
	for t := range s.handlers {
		out = append(out, t)
	}
	return out
}

type fakeApplier struct {
	mu       sync.Mutex
	commands []dto.Command
	err      error
	errOnce  bool
}

func (a *fakeApplier) Apply(_ context.Context, cmd dto.Command) error {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.commands = append(a.commands, cmd)
	if a.err != nil {
		err := a.err
		if a.errOnce {
			a.err = nil
		}
		return err
	}
	return nil
}

func (a *fakeApplier) snapshot() []dto.Command {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := make([]dto.Command, len(a.commands))
	copy(out, a.commands)
	return out
}

type fakeTopics struct{}

func (fakeTopics) Command(o string) string { return "test/cmd/" + o }

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

func setupSubscriber(t *testing.T) (*fakeSubscriber, *fakeApplier) {
	t.Helper()
	sub := &fakeSubscriber{}
	app := &fakeApplier{}
	cs := imqtt.NewCommandSubscriber(sub, app, fakeTopics{}, discardLogger())
	require.NoError(t, cs.Start(context.Background()))
	return sub, app
}

func dispatchMessage(t *testing.T, sub *fakeSubscriber, objectID, payload string) {
	t.Helper()
	topic := "test/cmd/" + objectID
	h, ok := sub.handler(topic)
	require.Truef(t, ok, "no handler registered for %s", topic)
	h(topic, []byte(payload))
}

func mustTemp(t *testing.T, c float64) valueobject.Temperature {
	t.Helper()
	temp, err := valueobject.NewTemperature(c)
	require.NoError(t, err)
	return temp
}

func mustFan(t *testing.T, v uint16) valueobject.FanSpeed {
	t.Helper()
	f, err := valueobject.NewFanSpeed(v)
	require.NoError(t, err)
	return f
}

func TestStart_Subscribes4Topics(t *testing.T) {
	t.Parallel()
	sub, _ := setupSubscriber(t)
	topics := sub.topics()
	assert.Len(t, topics, 4)
	assert.ElementsMatch(t, []string{
		"test/cmd/power",
		"test/cmd/hvac_mode",
		"test/cmd/target_temperature",
		"test/cmd/fan_target",
	}, topics)
}

func TestStart_SubscribeError_Propagates(t *testing.T) {
	t.Parallel()
	sub := &fakeSubscriber{subErr: errors.New("boom")}
	cs := imqtt.NewCommandSubscriber(sub, &fakeApplier{}, fakeTopics{}, discardLogger())
	err := cs.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "subscribe test/cmd/power")
	assert.Contains(t, err.Error(), "boom")
}

func TestPower_ON(t *testing.T) {
	t.Parallel()
	sub, app := setupSubscriber(t)
	dispatchMessage(t, sub, "power", "ON")
	assert.Equal(t, []dto.Command{dto.SetPowerCommand{On: true}}, app.snapshot())
}

func TestPower_OFF(t *testing.T) {
	t.Parallel()
	sub, app := setupSubscriber(t)
	dispatchMessage(t, sub, "power", "OFF")
	assert.Equal(t, []dto.Command{dto.SetPowerCommand{On: false}}, app.snapshot())
}

func TestPower_OnLowercase(t *testing.T) {
	t.Parallel()
	sub, app := setupSubscriber(t)
	dispatchMessage(t, sub, "power", "on")
	assert.Equal(t, []dto.Command{dto.SetPowerCommand{On: true}}, app.snapshot())
}

func TestPower_OffMixedCase(t *testing.T) {
	t.Parallel()
	sub, app := setupSubscriber(t)
	dispatchMessage(t, sub, "power", "Off")
	assert.Equal(t, []dto.Command{dto.SetPowerCommand{On: false}}, app.snapshot())
}

func TestPower_Invalid_NoCommand(t *testing.T) {
	t.Parallel()
	sub, app := setupSubscriber(t)
	dispatchMessage(t, sub, "power", "garbage")
	assert.Empty(t, app.snapshot())
}

func TestHVACMode_Off(t *testing.T) {
	t.Parallel()
	sub, app := setupSubscriber(t)
	dispatchMessage(t, sub, "hvac_mode", "off")
	assert.Equal(t, []dto.Command{dto.SetPowerCommand{On: false}}, app.snapshot())
}

func TestHVACMode_Heat(t *testing.T) {
	t.Parallel()
	sub, app := setupSubscriber(t)
	dispatchMessage(t, sub, "hvac_mode", "heat")
	assert.Equal(t, []dto.Command{
		dto.SetPowerCommand{On: true},
		dto.SetModeCommand{Mode: valueobject.ModeHeat},
	}, app.snapshot())
}

func TestHVACMode_FanOnly(t *testing.T) {
	t.Parallel()
	sub, app := setupSubscriber(t)
	dispatchMessage(t, sub, "hvac_mode", "fan_only")
	assert.Equal(t, []dto.Command{
		dto.SetPowerCommand{On: true},
		dto.SetModeCommand{Mode: valueobject.ModeOff},
	}, app.snapshot())
}

func TestHVACMode_HeatUppercase(t *testing.T) {
	t.Parallel()
	sub, app := setupSubscriber(t)
	dispatchMessage(t, sub, "hvac_mode", "HEAT")
	assert.Equal(t, []dto.Command{
		dto.SetPowerCommand{On: true},
		dto.SetModeCommand{Mode: valueobject.ModeHeat},
	}, app.snapshot())
}

func TestHVACMode_Heat_FirstApplyFails_StopsSequence(t *testing.T) {
	t.Parallel()
	sub := &fakeSubscriber{}
	app := &fakeApplier{err: errors.New("dispatcher down"), errOnce: true}
	cs := imqtt.NewCommandSubscriber(sub, app, fakeTopics{}, discardLogger())
	require.NoError(t, cs.Start(context.Background()))
	dispatchMessage(t, sub, "hvac_mode", "heat")
	assert.Equal(t, []dto.Command{dto.SetPowerCommand{On: true}}, app.snapshot(),
		"second command must be skipped when first fails")
}

func TestHVACMode_FanOnly_FirstApplyFails_StopsSequence(t *testing.T) {
	t.Parallel()
	sub := &fakeSubscriber{}
	app := &fakeApplier{err: errors.New("dispatcher down"), errOnce: true}
	cs := imqtt.NewCommandSubscriber(sub, app, fakeTopics{}, discardLogger())
	require.NoError(t, cs.Start(context.Background()))
	dispatchMessage(t, sub, "hvac_mode", "fan_only")
	assert.Equal(t, []dto.Command{dto.SetPowerCommand{On: true}}, app.snapshot())
}

func TestHVACMode_Invalid(t *testing.T) {
	t.Parallel()
	sub, app := setupSubscriber(t)
	dispatchMessage(t, sub, "hvac_mode", "garbage")
	assert.Empty(t, app.snapshot())
}

func TestTargetTemperature_Valid(t *testing.T) {
	t.Parallel()
	sub, app := setupSubscriber(t)
	dispatchMessage(t, sub, "target_temperature", "22.5")
	assert.Equal(t, []dto.Command{
		dto.SetTemperatureCommand{Temperature: mustTemp(t, 22.5)},
	}, app.snapshot())
}

func TestTargetTemperature_Integer(t *testing.T) {
	t.Parallel()
	sub, app := setupSubscriber(t)
	dispatchMessage(t, sub, "target_temperature", "20")
	assert.Equal(t, []dto.Command{
		dto.SetTemperatureCommand{Temperature: mustTemp(t, 20.0)},
	}, app.snapshot())
}

func TestTargetTemperature_Invalid_NotFloat(t *testing.T) {
	t.Parallel()
	sub, app := setupSubscriber(t)
	dispatchMessage(t, sub, "target_temperature", "abc")
	assert.Empty(t, app.snapshot())
}

func TestTargetTemperature_OutOfRange_High(t *testing.T) {
	t.Parallel()
	sub, app := setupSubscriber(t)
	dispatchMessage(t, sub, "target_temperature", "100")
	assert.Empty(t, app.snapshot())
}

func TestTargetTemperature_OutOfRange_Low(t *testing.T) {
	t.Parallel()
	sub, app := setupSubscriber(t)
	dispatchMessage(t, sub, "target_temperature", "1")
	assert.Empty(t, app.snapshot())
}

func TestFanTarget_Valid(t *testing.T) {
	t.Parallel()
	sub, app := setupSubscriber(t)
	dispatchMessage(t, sub, "fan_target", "5")
	assert.Equal(t, []dto.Command{
		dto.SetFanCommand{FanSpeed: mustFan(t, 5)},
	}, app.snapshot())
}

func TestFanTarget_Boundary_Max(t *testing.T) {
	t.Parallel()
	sub, app := setupSubscriber(t)
	dispatchMessage(t, sub, "fan_target", "10")
	assert.Equal(t, []dto.Command{
		dto.SetFanCommand{FanSpeed: mustFan(t, 10)},
	}, app.snapshot())
}

func TestFanTarget_Invalid_NotInt(t *testing.T) {
	t.Parallel()
	sub, app := setupSubscriber(t)
	dispatchMessage(t, sub, "fan_target", "abc")
	assert.Empty(t, app.snapshot())
}

func TestFanTarget_Negative(t *testing.T) {
	t.Parallel()
	sub, app := setupSubscriber(t)
	dispatchMessage(t, sub, "fan_target", "-1")
	assert.Empty(t, app.snapshot())
}

func TestFanTarget_OutOfRange_Zero(t *testing.T) {
	t.Parallel()
	sub, app := setupSubscriber(t)
	dispatchMessage(t, sub, "fan_target", "0")
	assert.Empty(t, app.snapshot())
}

func TestFanTarget_OutOfRange_High(t *testing.T) {
	t.Parallel()
	sub, app := setupSubscriber(t)
	dispatchMessage(t, sub, "fan_target", "11")
	assert.Empty(t, app.snapshot())
}

func TestFanTarget_OverflowUint16(t *testing.T) {
	t.Parallel()
	sub, app := setupSubscriber(t)
	dispatchMessage(t, sub, "fan_target", "70000")
	assert.Empty(t, app.snapshot())
}

func TestApply_ErrorIsLogged_NotPropagated(t *testing.T) {
	t.Parallel()
	sub := &fakeSubscriber{}
	app := &fakeApplier{err: errors.New("apply failed")}
	cs := imqtt.NewCommandSubscriber(sub, app, fakeTopics{}, discardLogger())
	require.NoError(t, cs.Start(context.Background()))
	require.NotPanics(t, func() {
		dispatchMessage(t, sub, "power", "ON")
	})
	assert.Equal(t, []dto.Command{dto.SetPowerCommand{On: true}}, app.snapshot())
}

func TestNew_NilSubscriber_Panics(t *testing.T) {
	t.Parallel()
	assert.PanicsWithValue(t, "command subscriber: subscriber must not be nil", func() {
		imqtt.NewCommandSubscriber(nil, &fakeApplier{}, fakeTopics{}, discardLogger())
	})
}

func TestNew_NilApplier_Panics(t *testing.T) {
	t.Parallel()
	assert.PanicsWithValue(t, "command subscriber: applier must not be nil", func() {
		imqtt.NewCommandSubscriber(&fakeSubscriber{}, nil, fakeTopics{}, discardLogger())
	})
}

func TestNew_NilTopics_Panics(t *testing.T) {
	t.Parallel()
	assert.PanicsWithValue(t, "command subscriber: topics must not be nil", func() {
		imqtt.NewCommandSubscriber(&fakeSubscriber{}, &fakeApplier{}, nil, discardLogger())
	})
}

func TestNew_NilLogger_FallsBackToDefault(t *testing.T) {
	t.Parallel()
	cs := imqtt.NewCommandSubscriber(&fakeSubscriber{}, &fakeApplier{}, fakeTopics{}, nil)
	require.NotNil(t, cs)
}
