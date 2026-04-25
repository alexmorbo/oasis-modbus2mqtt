package usecase_test

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

	"github.com/alexmorbo/oasis-modbus2mqtt/application/usecase"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"
)

type fakeTopics struct{}

func (fakeTopics) State(objectID string) string { return "test/state/" + objectID }

type statePublishCall struct {
	topic    string
	payload  []byte
	retained bool
}

type fakeStatePublisher struct {
	mu       sync.Mutex
	calls    []statePublishCall
	errByTop map[string]error
}

func (f *fakeStatePublisher) Publish(_ context.Context, topic string, payload []byte, retained bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, statePublishCall{
		topic:    topic,
		payload:  append([]byte(nil), payload...),
		retained: retained,
	})
	if err, ok := f.errByTop[topic]; ok {
		return err
	}
	return nil
}

func (f *fakeStatePublisher) snapshot() []statePublishCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]statePublishCall, len(f.calls))
	copy(out, f.calls)
	return out
}

func (f *fakeStatePublisher) reset() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = nil
}

func (f *fakeStatePublisher) findCall(topic string) (statePublishCall, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, c := range f.calls {
		if c.topic == topic {
			return c, true
		}
	}
	return statePublishCall{}, false
}

func discardLoggerState() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

func mustTempState(t *testing.T, c float64) valueobject.Temperature {
	t.Helper()
	temp, err := valueobject.NewTemperature(c)
	require.NoError(t, err)
	return temp
}

func sampleSnapshot(t *testing.T) entity.Snapshot {
	t.Helper()
	return entity.Snapshot{
		Firmware:          valueobject.FirmwareFromRaw(0x5200),
		DeviceID:          0x1234,
		PowerOn:           true,
		CurrentMode:       valueobject.ModeHeat,
		Operation:         entity.OpIdle,
		OperationTimeLeft: 90 * time.Second,
		TargetTemp:        mustTempState(t, 22.0),
		RoomTemp:          mustTempState(t, 21.5),
		SupplyTemp:        mustTempState(t, 23.0),
		FanTarget1:        5,
		FanState1:         5,
		FanState2:         3,
		HeaterPWM:         true,
		DamperOpen:        true,
		PIDDemand:         42,
		FilterPct:         15,
		RoomHumidity:      55,
	}
}

func TestNewPublishState_NilPublisher_Panics(t *testing.T) {
	t.Parallel()
	require.PanicsWithValue(t, "publish state: publisher must not be nil", func() {
		_ = usecase.NewPublishState(nil, fakeTopics{}, nil)
	})
}

func TestNewPublishState_NilTopics_Panics(t *testing.T) {
	t.Parallel()
	require.PanicsWithValue(t, "publish state: topics must not be nil", func() {
		_ = usecase.NewPublishState(&fakeStatePublisher{}, nil, nil)
	})
}

func TestNewPublishState_NilLogger_FallsBack(t *testing.T) {
	t.Parallel()
	ps := usecase.NewPublishState(&fakeStatePublisher{}, fakeTopics{}, nil)
	require.NotNil(t, ps)
	err := ps.Apply(context.Background(), sampleSnapshot(t))
	require.NoError(t, err)
}

func TestApply_PublishesAllOnFirstCall(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())

	err := ps.Apply(context.Background(), sampleSnapshot(t))
	require.NoError(t, err)

	calls := pub.snapshot()
	assert.Len(t, calls, 19)
	for _, c := range calls {
		assert.False(t, c.retained, "state messages must be non-retained, got %s", c.topic)
	}

	c, ok := pub.findCall("test/state/supply_temperature")
	require.True(t, ok)
	assert.Equal(t, []byte("23.0"), c.payload)

	c, ok = pub.findCall("test/state/device_id")
	require.True(t, ok)
	assert.Equal(t, []byte("0x1234"), c.payload)

	c, ok = pub.findCall("test/state/operation_time_left")
	require.True(t, ok)
	assert.Equal(t, []byte("90"), c.payload)

	c, ok = pub.findCall("test/state/power")
	require.True(t, ok)
	assert.Equal(t, []byte("ON"), c.payload)

	c, ok = pub.findCall("test/state/damper_open")
	require.True(t, ok)
	assert.Equal(t, []byte("OPEN"), c.payload)

	c, ok = pub.findCall("test/state/firmware")
	require.True(t, ok)
	assert.Equal(t, []byte("v5.2.0"), c.payload)

	c, ok = pub.findCall("test/state/hvac_mode")
	require.True(t, ok)
	assert.Equal(t, []byte("heat"), c.payload)

	c, ok = pub.findCall("test/state/hvac_action")
	require.True(t, ok)
	assert.Equal(t, []byte("heating"), c.payload)

	c, ok = pub.findCall("test/state/problem")
	require.True(t, ok)
	assert.Equal(t, []byte("OFF"), c.payload)
}

func TestApply_DeltaSkipsUnchanged(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)

	require.NoError(t, ps.Apply(context.Background(), snap))
	first := len(pub.snapshot())
	assert.Equal(t, 19, first)

	pub.reset()
	require.NoError(t, ps.Apply(context.Background(), snap))
	assert.Empty(t, pub.snapshot(), "second Apply with identical snapshot must publish nothing")
}

func TestApply_DeltaPublishesChanged(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)

	require.NoError(t, ps.Apply(context.Background(), snap))
	pub.reset()

	snap.FanTarget1 = 7
	require.NoError(t, ps.Apply(context.Background(), snap))

	calls := pub.snapshot()
	require.Len(t, calls, 1)
	assert.Equal(t, "test/state/fan_target", calls[0].topic)
	assert.Equal(t, []byte("7"), calls[0].payload)
}

func TestApply_PublishError_DoesNotUpdateLastPayload(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("publish boom")
	pub := &fakeStatePublisher{errByTop: map[string]error{"test/state/fan_target": sentinel}}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)

	err := ps.Apply(context.Background(), snap)
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)

	pub.reset()
	pub.mu.Lock()
	pub.errByTop = nil
	pub.mu.Unlock()

	err = ps.Apply(context.Background(), snap)
	require.NoError(t, err)
	c, ok := pub.findCall("test/state/fan_target")
	require.True(t, ok, "second Apply should retry the previously failed topic")
	assert.Equal(t, []byte("5"), c.payload)
	assert.Len(t, pub.snapshot(), 1, "only the previously failed topic should be retried")
}

func TestApply_AggregatesErrors(t *testing.T) {
	t.Parallel()
	errA := errors.New("err a")
	errB := errors.New("err b")
	pub := &fakeStatePublisher{errByTop: map[string]error{
		"test/state/fan_target": errA,
		"test/state/hvac_mode":  errB,
	}}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())

	err := ps.Apply(context.Background(), sampleSnapshot(t))
	require.Error(t, err)
	assert.ErrorIs(t, err, errA)
	assert.ErrorIs(t, err, errB)
}

func TestHvacMode_PowerOff(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)
	snap.PowerOn = false

	require.NoError(t, ps.Apply(context.Background(), snap))
	c, ok := pub.findCall("test/state/hvac_mode")
	require.True(t, ok)
	assert.Equal(t, []byte("off"), c.payload)
}

func TestHvacMode_PowerOn_Heat(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)
	snap.PowerOn = true
	snap.CurrentMode = valueobject.ModeHeat

	require.NoError(t, ps.Apply(context.Background(), snap))
	c, ok := pub.findCall("test/state/hvac_mode")
	require.True(t, ok)
	assert.Equal(t, []byte("heat"), c.payload)
}

func TestHvacMode_PowerOn_OffMode(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)
	snap.PowerOn = true
	snap.CurrentMode = valueobject.ModeOff

	require.NoError(t, ps.Apply(context.Background(), snap))
	c, ok := pub.findCall("test/state/hvac_mode")
	require.True(t, ok)
	assert.Equal(t, []byte("fan_only"), c.payload)
}

func TestHvacMode_PowerOn_Cool(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)
	snap.PowerOn = true
	snap.CurrentMode = valueobject.ModeCool

	require.NoError(t, ps.Apply(context.Background(), snap))
	c, ok := pub.findCall("test/state/hvac_mode")
	require.True(t, ok)
	assert.Equal(t, []byte("fan_only"), c.payload)
}

func TestHvacMode_PowerOn_Auto(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)
	snap.PowerOn = true
	snap.CurrentMode = valueobject.ModeAuto

	require.NoError(t, ps.Apply(context.Background(), snap))
	c, ok := pub.findCall("test/state/hvac_mode")
	require.True(t, ok)
	assert.Equal(t, []byte("fan_only"), c.payload)
}

func TestHvacAction_PowerOff(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)
	snap.PowerOn = false

	require.NoError(t, ps.Apply(context.Background(), snap))
	c, ok := pub.findCall("test/state/hvac_action")
	require.True(t, ok)
	assert.Equal(t, []byte("off"), c.payload)
}

func TestHvacAction_PreheatTransition(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)
	snap.Operation = entity.OpPreheatCalorifier
	snap.HeaterPWM = false
	snap.FanState1 = 0

	require.NoError(t, ps.Apply(context.Background(), snap))
	c, ok := pub.findCall("test/state/hvac_action")
	require.True(t, ok)
	assert.Equal(t, []byte("preheating"), c.payload)
}

func TestHvacAction_StartFanTransition(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)
	snap.Operation = entity.OpStartFan
	snap.HeaterPWM = false

	require.NoError(t, ps.Apply(context.Background(), snap))
	c, ok := pub.findCall("test/state/hvac_action")
	require.True(t, ok)
	assert.Equal(t, []byte("fan"), c.payload)
}

func TestHvacAction_FanCoastdown(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)
	snap.Operation = entity.OpFanCoastdown
	snap.HeaterPWM = true

	require.NoError(t, ps.Apply(context.Background(), snap))
	c, ok := pub.findCall("test/state/hvac_action")
	require.True(t, ok)
	assert.Equal(t, []byte("fan"), c.payload, "fan transitions outrank heater PWM")
}

func TestHvacAction_HeaterPWM(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)
	snap.Operation = entity.OpIdle
	snap.HeaterPWM = true
	snap.FanState1 = 5

	require.NoError(t, ps.Apply(context.Background(), snap))
	c, ok := pub.findCall("test/state/hvac_action")
	require.True(t, ok)
	assert.Equal(t, []byte("heating"), c.payload)
}

func TestHvacAction_FanRunning(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)
	snap.Operation = entity.OpIdle
	snap.HeaterPWM = false
	snap.FanState1 = 4

	require.NoError(t, ps.Apply(context.Background(), snap))
	c, ok := pub.findCall("test/state/hvac_action")
	require.True(t, ok)
	assert.Equal(t, []byte("fan"), c.payload)
}

func TestHvacAction_Idle(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)
	snap.Operation = entity.OpIdle
	snap.HeaterPWM = false
	snap.FanState1 = 0

	require.NoError(t, ps.Apply(context.Background(), snap))
	c, ok := pub.findCall("test/state/hvac_action")
	require.True(t, ok)
	assert.Equal(t, []byte("idle"), c.payload)
}

func TestProblem_NoErrors(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)
	snap.RawErrors = [4]uint16{0, 0, 0, 0}

	require.NoError(t, ps.Apply(context.Background(), snap))
	c, ok := pub.findCall("test/state/problem")
	require.True(t, ok)
	assert.Equal(t, []byte("OFF"), c.payload)
}

func TestProblem_WithErrors(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)
	snap.RawErrors[0] = 0xFFFF

	require.NoError(t, ps.Apply(context.Background(), snap))
	c, ok := pub.findCall("test/state/problem")
	require.True(t, ok)
	assert.Equal(t, []byte("ON"), c.payload)
}

func TestApply_ZeroSnapshot_PublishesDefaults(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())

	require.NoError(t, ps.Apply(context.Background(), entity.Snapshot{}))
	calls := pub.snapshot()
	assert.Len(t, calls, 19)

	c, ok := pub.findCall("test/state/firmware")
	require.True(t, ok)
	assert.Equal(t, []byte("unknown"), c.payload)

	c, ok = pub.findCall("test/state/device_id")
	require.True(t, ok)
	assert.Equal(t, []byte("0x0000"), c.payload)

	c, ok = pub.findCall("test/state/power")
	require.True(t, ok)
	assert.Equal(t, []byte("OFF"), c.payload)

	c, ok = pub.findCall("test/state/damper_open")
	require.True(t, ok)
	assert.Equal(t, []byte("CLOSED"), c.payload)

	c, ok = pub.findCall("test/state/hvac_mode")
	require.True(t, ok)
	assert.Equal(t, []byte("off"), c.payload)

	c, ok = pub.findCall("test/state/hvac_action")
	require.True(t, ok)
	assert.Equal(t, []byte("off"), c.payload)

	c, ok = pub.findCall("test/state/supply_temperature")
	require.True(t, ok)
	assert.Equal(t, []byte("0.0"), c.payload)
}

func TestApply_LogsPublishedCount(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	ps := usecase.NewPublishState(&fakeStatePublisher{}, fakeTopics{}, logger)
	require.NoError(t, ps.Apply(context.Background(), sampleSnapshot(t)))
	out := buf.String()
	assert.Contains(t, out, "state published")
	assert.Contains(t, out, `"count":19`)
}
