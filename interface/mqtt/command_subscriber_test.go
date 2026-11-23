package mqtt_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/dto"
	"github.com/alexmorbo/oasis-modbus2mqtt/application/port"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/catalog"
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

func setupSubscriber(t *testing.T) (*fakeSubscriber, *fakeApplier, *imqtt.CommandSubscriber) {
	t.Helper()
	sub := &fakeSubscriber{}
	app := &fakeApplier{}
	cs := imqtt.NewCommandSubscriber(sub, app, fakeTopics{}, discardLogger())
	require.NoError(t, cs.Start(t.Context()))
	return sub, app, cs
}

// deliver invokes the registered handler the way the MQTT router would,
// without waiting for the worker.
func deliver(t *testing.T, sub *fakeSubscriber, objectID, payload string) {
	t.Helper()
	topic := "test/cmd/" + objectID
	h, ok := sub.handler(topic)
	require.Truef(t, ok, "no handler registered for %s", topic)
	h(topic, []byte(payload))
}

// dispatchMessage delivers one message and waits until the worker applied it.
func dispatchMessage(t *testing.T, cs *imqtt.CommandSubscriber, sub *fakeSubscriber, objectID, payload string) {
	t.Helper()
	deliver(t, sub, objectID, payload)
	cs.FlushForTest()
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

func TestStart_Subscribes18Topics(t *testing.T) {
	t.Parallel()
	sub, _, _ := setupSubscriber(t)
	want := []string{
		"test/cmd/power",
		"test/cmd/hvac_mode",
		"test/cmd/target_temperature",
		"test/cmd/fan_target",
	}
	for _, def := range catalog.Settings {
		want = append(want, "test/cmd/"+def.ObjectID)
	}
	topics := sub.topics()
	assert.Len(t, topics, 18)
	assert.ElementsMatch(t, want, topics)
}

func TestSettingHandler_ForwardsRawPayload(t *testing.T) {
	t.Parallel()
	sub, app, cs := setupSubscriber(t)

	dispatchMessage(t, cs, sub, "humidity_target", "50.0")
	dispatchMessage(t, cs, sub, "co2_target", "garbage")
	dispatchMessage(t, cs, sub, "alarm_beep", " off ")

	assert.Equal(t, []dto.Command{
		dto.SetSettingCommand{ObjectID: "humidity_target", Payload: "50.0"},
		dto.SetSettingCommand{ObjectID: "co2_target", Payload: "garbage"},
		dto.SetSettingCommand{ObjectID: "alarm_beep", Payload: " off "},
	}, app.snapshot(), "the subscriber forwards verbatim and never validates")
}

func TestSettingHandler_ApplyErrorIsLogged(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	sub := &fakeSubscriber{}
	app := &fakeApplier{err: dto.ErrWriteNotApplied}
	cs := imqtt.NewCommandSubscriber(sub, app, fakeTopics{}, logger)
	require.NoError(t, cs.Start(t.Context()))

	dispatchMessage(t, cs, sub, "co2_target", "800")
	out := buf.String()
	assert.Contains(t, out, `level=DEBUG msg="apply command failed" command="SetSetting(co2_target=800)"`,
		"ApplyCommand owns the WARN; the subscriber logs the command string at Debug")
	assert.Contains(t, out, "did not apply")
	assert.NotContains(t, out, "level=WARN")
}

func TestStart_SubscribeError_Propagates(t *testing.T) {
	t.Parallel()
	sub := &fakeSubscriber{subErr: errors.New("boom")}
	cs := imqtt.NewCommandSubscriber(sub, &fakeApplier{}, fakeTopics{}, discardLogger())
	err := cs.Start(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "subscribe test/cmd/power")
	assert.Contains(t, err.Error(), "boom")
}

func TestPower_ON(t *testing.T) {
	t.Parallel()
	sub, app, cs := setupSubscriber(t)
	dispatchMessage(t, cs, sub, "power", "ON")
	assert.Equal(t, []dto.Command{dto.SetPowerCommand{On: true}}, app.snapshot())
}

func TestPower_OFF(t *testing.T) {
	t.Parallel()
	sub, app, cs := setupSubscriber(t)
	dispatchMessage(t, cs, sub, "power", "OFF")
	assert.Equal(t, []dto.Command{dto.SetPowerCommand{On: false}}, app.snapshot())
}

func TestPower_OnLowercase(t *testing.T) {
	t.Parallel()
	sub, app, cs := setupSubscriber(t)
	dispatchMessage(t, cs, sub, "power", "on")
	assert.Equal(t, []dto.Command{dto.SetPowerCommand{On: true}}, app.snapshot())
}

func TestPower_OffMixedCase(t *testing.T) {
	t.Parallel()
	sub, app, cs := setupSubscriber(t)
	dispatchMessage(t, cs, sub, "power", "Off")
	assert.Equal(t, []dto.Command{dto.SetPowerCommand{On: false}}, app.snapshot())
}

func TestPower_Invalid_NoCommand(t *testing.T) {
	t.Parallel()
	sub, app, cs := setupSubscriber(t)
	dispatchMessage(t, cs, sub, "power", "garbage")
	assert.Empty(t, app.snapshot())
}

func TestHVACMode_Off(t *testing.T) {
	t.Parallel()
	sub, app, cs := setupSubscriber(t)
	dispatchMessage(t, cs, sub, "hvac_mode", "off")
	assert.Equal(t, []dto.Command{dto.SetPowerCommand{On: false}}, app.snapshot())
}

func TestHVACMode_Heat(t *testing.T) {
	t.Parallel()
	sub, app, cs := setupSubscriber(t)
	dispatchMessage(t, cs, sub, "hvac_mode", "heat")
	assert.Equal(t, []dto.Command{
		dto.SetPowerCommand{On: true},
		dto.SetModeCommand{Mode: valueobject.ModeHeat},
	}, app.snapshot())
}

func TestHVACMode_FanOnly(t *testing.T) {
	t.Parallel()
	sub, app, cs := setupSubscriber(t)
	dispatchMessage(t, cs, sub, "hvac_mode", "fan_only")
	assert.Equal(t, []dto.Command{
		dto.SetPowerCommand{On: true},
		dto.SetModeCommand{Mode: valueobject.ModeOff},
	}, app.snapshot())
}

func TestHVACMode_HeatUppercase(t *testing.T) {
	t.Parallel()
	sub, app, cs := setupSubscriber(t)
	dispatchMessage(t, cs, sub, "hvac_mode", "HEAT")
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
	require.NoError(t, cs.Start(t.Context()))
	dispatchMessage(t, cs, sub, "hvac_mode", "heat")
	assert.Equal(t, []dto.Command{dto.SetPowerCommand{On: true}}, app.snapshot(),
		"second command must be skipped when first fails")
}

func TestHVACMode_FanOnly_FirstApplyFails_StopsSequence(t *testing.T) {
	t.Parallel()
	sub := &fakeSubscriber{}
	app := &fakeApplier{err: errors.New("dispatcher down"), errOnce: true}
	cs := imqtt.NewCommandSubscriber(sub, app, fakeTopics{}, discardLogger())
	require.NoError(t, cs.Start(t.Context()))
	dispatchMessage(t, cs, sub, "hvac_mode", "fan_only")
	assert.Equal(t, []dto.Command{dto.SetPowerCommand{On: true}}, app.snapshot())
}

func TestHVACMode_Invalid(t *testing.T) {
	t.Parallel()
	sub, app, cs := setupSubscriber(t)
	dispatchMessage(t, cs, sub, "hvac_mode", "garbage")
	assert.Empty(t, app.snapshot())
}

func TestTargetTemperature_Valid(t *testing.T) {
	t.Parallel()
	sub, app, cs := setupSubscriber(t)
	dispatchMessage(t, cs, sub, "target_temperature", "22.5")
	assert.Equal(t, []dto.Command{
		dto.SetTemperatureCommand{Temperature: mustTemp(t, 22.5)},
	}, app.snapshot())
}

func TestTargetTemperature_Integer(t *testing.T) {
	t.Parallel()
	sub, app, cs := setupSubscriber(t)
	dispatchMessage(t, cs, sub, "target_temperature", "20")
	assert.Equal(t, []dto.Command{
		dto.SetTemperatureCommand{Temperature: mustTemp(t, 20.0)},
	}, app.snapshot())
}

func TestTargetTemperature_Invalid_NotFloat(t *testing.T) {
	t.Parallel()
	sub, app, cs := setupSubscriber(t)
	dispatchMessage(t, cs, sub, "target_temperature", "abc")
	assert.Empty(t, app.snapshot())
}

func TestTargetTemperature_OutOfRange_High(t *testing.T) {
	t.Parallel()
	sub, app, cs := setupSubscriber(t)
	dispatchMessage(t, cs, sub, "target_temperature", "100")
	assert.Empty(t, app.snapshot())
}

func TestTargetTemperature_OutOfRange_Low(t *testing.T) {
	t.Parallel()
	sub, app, cs := setupSubscriber(t)
	dispatchMessage(t, cs, sub, "target_temperature", "1")
	assert.Empty(t, app.snapshot())
}

func TestFanTarget_Valid(t *testing.T) {
	t.Parallel()
	sub, app, cs := setupSubscriber(t)
	dispatchMessage(t, cs, sub, "fan_target", "5")
	assert.Equal(t, []dto.Command{
		dto.SetFanCommand{FanSpeed: mustFan(t, 5)},
	}, app.snapshot())
}

func TestFanTarget_Boundary_Max(t *testing.T) {
	t.Parallel()
	sub, app, cs := setupSubscriber(t)
	dispatchMessage(t, cs, sub, "fan_target", "10")
	assert.Equal(t, []dto.Command{
		dto.SetFanCommand{FanSpeed: mustFan(t, 10)},
	}, app.snapshot())
}

func TestFanTarget_Invalid_NotInt(t *testing.T) {
	t.Parallel()
	sub, app, cs := setupSubscriber(t)
	dispatchMessage(t, cs, sub, "fan_target", "abc")
	assert.Empty(t, app.snapshot())
}

func TestFanTarget_Negative(t *testing.T) {
	t.Parallel()
	sub, app, cs := setupSubscriber(t)
	dispatchMessage(t, cs, sub, "fan_target", "-1")
	assert.Empty(t, app.snapshot())
}

func TestFanTarget_OutOfRange_Zero(t *testing.T) {
	t.Parallel()
	sub, app, cs := setupSubscriber(t)
	dispatchMessage(t, cs, sub, "fan_target", "0")
	assert.Empty(t, app.snapshot())
}

func TestFanTarget_OutOfRange_High(t *testing.T) {
	t.Parallel()
	sub, app, cs := setupSubscriber(t)
	dispatchMessage(t, cs, sub, "fan_target", "11")
	assert.Empty(t, app.snapshot())
}

func TestFanTarget_OverflowUint16(t *testing.T) {
	t.Parallel()
	sub, app, cs := setupSubscriber(t)
	dispatchMessage(t, cs, sub, "fan_target", "70000")
	assert.Empty(t, app.snapshot())
}

func TestApply_ErrorIsLogged_NotPropagated(t *testing.T) {
	t.Parallel()
	sub := &fakeSubscriber{}
	app := &fakeApplier{err: errors.New("apply failed")}
	cs := imqtt.NewCommandSubscriber(sub, app, fakeTopics{}, discardLogger())
	require.NoError(t, cs.Start(t.Context()))
	require.NotPanics(t, func() {
		dispatchMessage(t, cs, sub, "power", "ON")
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

// lockedBuffer is a goroutine-safe log sink.
type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

// blockingApplier records commands and blocks every Apply until release is
// closed. entered receives one value per Apply call as it starts.
type blockingApplier struct {
	fakeApplier
	entered chan struct{}
	release chan struct{}
	ctxs    chan context.Context
}

func newBlockingApplier() *blockingApplier {
	return &blockingApplier{
		entered: make(chan struct{}, 128),
		release: make(chan struct{}),
		ctxs:    make(chan context.Context, 128),
	}
}

func (a *blockingApplier) Apply(ctx context.Context, cmd dto.Command) error {
	a.entered <- struct{}{}
	a.ctxs <- ctx
	<-a.release
	return a.fakeApplier.Apply(ctx, cmd)
}

func waitEntered(t *testing.T, a *blockingApplier) {
	t.Helper()
	select {
	case <-a.entered:
	case <-time.After(2 * time.Second):
		t.Fatal("worker never started applying")
	}
}

func TestHandlers_ReturnImmediatelyWhileApplierBlocks(t *testing.T) {
	t.Parallel()
	sub := &fakeSubscriber{}
	app := newBlockingApplier()
	cs := imqtt.NewCommandSubscriber(sub, app, fakeTopics{}, discardLogger())
	require.NoError(t, cs.Start(t.Context()))

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		deliver(t, sub, "alarm_beep", "OFF")
		deliver(t, sub, "hvac_mode", "heat")
		deliver(t, sub, "humidity_target", "55")
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("handlers blocked on the applier: they must only parse and enqueue")
	}
	waitEntered(t, app)
	assert.Empty(t, app.snapshot(), "nothing applied while the applier is blocked")

	close(app.release)
	cs.FlushForTest()
	assert.Equal(t, []dto.Command{
		dto.SetSettingCommand{ObjectID: "alarm_beep", Payload: "OFF"},
		dto.SetPowerCommand{On: true},
		dto.SetModeCommand{Mode: valueobject.ModeHeat},
		dto.SetSettingCommand{ObjectID: "humidity_target", Payload: "55"},
	}, app.snapshot())
}

func TestWorker_AppliesInArrivalOrder(t *testing.T) {
	t.Parallel()
	sub, app, cs := setupSubscriber(t)

	var want []dto.Command
	for i := range 20 {
		payload := "ON"
		if i%2 == 1 {
			payload = "OFF"
		}
		deliver(t, sub, "touch_click", payload)
		want = append(want, dto.SetSettingCommand{ObjectID: "touch_click", Payload: payload})
		if i%5 == 0 {
			deliver(t, sub, "power", payload)
			want = append(want, dto.SetPowerCommand{On: payload == "ON"})
		}
	}
	cs.FlushForTest()
	assert.Equal(t, want, app.snapshot(), "two toggles of the same bit apply in arrival order")
}

func TestQueueFull_DropsWithWarn(t *testing.T) {
	t.Parallel()
	logs := &lockedBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	sub := &fakeSubscriber{}
	app := newBlockingApplier()
	cs := imqtt.NewCommandSubscriber(sub, app, fakeTopics{}, logger)
	require.NoError(t, cs.Start(t.Context()))

	deliver(t, sub, "touch_click", "OFF")
	waitEntered(t, app) // the worker holds job 1; the queue is empty again
	for range imqtt.CommandQueueSize {
		deliver(t, sub, "alarm_beep", "OFF")
	}
	assert.NotContains(t, logs.String(), "command queue full", "exactly capacity jobs fit")

	deliver(t, sub, "co2_target", "800")
	out := logs.String()
	assert.Contains(t, out, `level=WARN msg="command queue full, command dropped" topic=test/cmd/co2_target`)

	close(app.release)
	cs.FlushForTest()
	cmds := app.snapshot()
	require.Len(t, cmds, 1+imqtt.CommandQueueSize)
	assert.NotContains(t, cmds, dto.SetSettingCommand{ObjectID: "co2_target", Payload: "800"}, "the dropped command never applies")
}

func TestWorker_JobContextHasDeadlineAndIsCancelledByStartCtx(t *testing.T) {
	t.Parallel()
	sub := &fakeSubscriber{}
	app := newBlockingApplier()
	cs := imqtt.NewCommandSubscriber(sub, app, fakeTopics{}, discardLogger())
	ctx, cancel := context.WithCancel(t.Context())
	require.NoError(t, cs.Start(ctx))

	deliver(t, sub, "power", "ON")
	waitEntered(t, app)
	jobCtx := <-app.ctxs
	_, hasDeadline := jobCtx.Deadline()
	assert.True(t, hasDeadline, "each job gets its own handlerTimeout")

	cancel()
	select {
	case <-jobCtx.Done():
	case <-time.After(time.Second):
		t.Fatal("cancelling the Start ctx must cancel the in-flight job ctx")
	}
	close(app.release)
	select {
	case <-cs.Done():
	case <-time.After(2 * time.Second):
		t.Fatal("worker did not exit after ctx cancellation")
	}
}

func TestEnqueue_AfterStop_Drops(t *testing.T) {
	t.Parallel()
	logs := &lockedBuffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	sub := &fakeSubscriber{}
	app := &fakeApplier{}
	cs := imqtt.NewCommandSubscriber(sub, app, fakeTopics{}, logger)
	ctx, cancel := context.WithCancel(t.Context())
	require.NoError(t, cs.Start(ctx))
	cancel()
	<-cs.Done()

	deliver(t, sub, "power", "ON")
	assert.Empty(t, app.snapshot())
	assert.Contains(t, logs.String(), "command dropped: subscriber stopped")
}

func TestStart_Twice_ReturnsError(t *testing.T) {
	t.Parallel()
	_, _, cs := setupSubscriber(t)
	err := cs.Start(t.Context())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "already started")
}
