package usecase_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"strings"
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
		PIDDemand:         42, // heat demand > 0 → heater_active ON, hvac_action heating
		FilterPct:         15,
		FilterResource:    150,
		FilterDaysLeft:    120,
		FilterPolledAt:    time.Date(2026, 4, 25, 14, 0, 0, 0, time.UTC),
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
	assert.Len(t, calls, 21)
	for _, c := range calls {
		assert.True(t, c.retained, "state messages must be retained, got non-retained for %s", c.topic)
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

	c, ok = pub.findCall("test/state/filter_days_left")
	require.True(t, ok)
	assert.Equal(t, []byte("120"), c.payload)

	c, ok = pub.findCall("test/state/filter_resource")
	require.True(t, ok)
	assert.Equal(t, []byte("150"), c.payload)
}

func TestApply_RepublishesAllOnEveryCall(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)

	for range 3 {
		require.NoError(t, ps.Apply(context.Background(), snap))
	}

	calls := pub.snapshot()
	assert.Len(t, calls, 63, "three identical Apply calls must publish 21×3 messages")
	for _, c := range calls {
		assert.True(t, c.retained, "every state publish must be retained")
	}
}

func TestApply_AlwaysRetainsState(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())

	require.NoError(t, ps.Apply(context.Background(), sampleSnapshot(t)))
	pub.reset()
	require.NoError(t, ps.Apply(context.Background(), sampleSnapshot(t)))

	calls := pub.snapshot()
	require.Len(t, calls, 21)
	for _, c := range calls {
		assert.True(t, c.retained, "retained flag must be true on %s", c.topic)
	}
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

func TestApply_ErrorOnOneTopic_DoesNotSkipOthers(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("publish boom")
	pub := &fakeStatePublisher{errByTop: map[string]error{"test/state/fan_target": sentinel}}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())

	err := ps.Apply(context.Background(), sampleSnapshot(t))
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)

	calls := pub.snapshot()
	assert.Len(t, calls, 21, "all 21 publish attempts must be made even when one fails")
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
	snap.PIDDemand = 0
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
	snap.PIDDemand = 0

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
	snap.PIDDemand = 42

	require.NoError(t, ps.Apply(context.Background(), snap))
	c, ok := pub.findCall("test/state/hvac_action")
	require.True(t, ok)
	assert.Equal(t, []byte("fan"), c.payload, "fan transitions outrank heat demand")
}

func TestHvacAction_HeatDemand(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)
	snap.Operation = entity.OpIdle
	snap.HeaterPWM = false
	snap.PIDDemand = 30
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
	snap.PIDDemand = 0
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
	snap.PIDDemand = 0
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
	assert.Len(t, calls, 19, "filter topics are omitted until the first successful filter read")

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

	_, ok = pub.findCall("test/state/filter_days_left")
	assert.False(t, ok, "filter_days_left must not publish a bogus 0 before the first filter read")

	_, ok = pub.findCall("test/state/filter_resource")
	assert.False(t, ok, "filter_resource must not publish a bogus 0 before the first filter read")
}

func TestApply_FilterTopicsGatedOnFilterPolledAt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		mutate       func(s *entity.Snapshot)
		wantCount    int
		wantDaysLeft string
	}{
		{
			name:      "filter not yet read",
			mutate:    func(s *entity.Snapshot) { s.FilterPolledAt = time.Time{} },
			wantCount: 19,
		},
		{
			name:         "filter read",
			mutate:       func(*entity.Snapshot) {},
			wantCount:    21,
			wantDaysLeft: "120",
		},
		{
			name:         "overdue filter renders negative days",
			mutate:       func(s *entity.Snapshot) { s.FilterDaysLeft = -3 },
			wantCount:    21,
			wantDaysLeft: "-3",
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pub := &fakeStatePublisher{}
			ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
			snap := sampleSnapshot(t)
			tc.mutate(&snap)

			require.NoError(t, ps.Apply(context.Background(), snap))
			assert.Len(t, pub.snapshot(), tc.wantCount)

			days, okDays := pub.findCall("test/state/filter_days_left")
			_, okRes := pub.findCall("test/state/filter_resource")
			if tc.wantDaysLeft == "" {
				assert.False(t, okDays)
				assert.False(t, okRes)
				return
			}
			require.True(t, okDays)
			assert.True(t, okRes)
			assert.Equal(t, []byte(tc.wantDaysLeft), days.payload)
		})
	}
}

func TestApply_LogsPublishedCount(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	ps := usecase.NewPublishState(&fakeStatePublisher{}, fakeTopics{}, logger)
	require.NoError(t, ps.Apply(context.Background(), sampleSnapshot(t)))
	out := buf.String()
	assert.Contains(t, out, "state published")
	assert.Contains(t, out, `"count":21`)
}

func withSettings(snap entity.Snapshot) entity.Snapshot {
	snap.Settings = entity.PanelSettings{
		UserKeys0: 0x07E4, DarkBrightness: 20, IRRemoteTime: 3, UserDev0: 0x0004,
		HumidityTarget: 50, CO2Target: 500,
	}
	snap.SettingsPolledAt = time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC)
	return snap
}

func payloadOf(t *testing.T, pub *fakeStatePublisher, objectID string) string {
	t.Helper()
	c, ok := pub.findCall("test/state/" + objectID)
	require.Truef(t, ok, "no state published for %s", objectID)
	return string(c.payload)
}

func TestApply_SettingsTopicsGatedOnSettingsPolledAt(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		mutate    func(s entity.Snapshot) entity.Snapshot
		wantCount int
		wantSet   bool
	}{
		{
			name: "nothing best-effort polled",
			mutate: func(s entity.Snapshot) entity.Snapshot {
				s.FilterPolledAt = time.Time{}
				return s
			},
			wantCount: 19,
		},
		{name: "filter only", mutate: func(s entity.Snapshot) entity.Snapshot { return s }, wantCount: 21},
		{
			name: "settings only",
			mutate: func(s entity.Snapshot) entity.Snapshot {
				s.FilterPolledAt = time.Time{}
				return withSettings(s)
			},
			wantCount: 33,
			wantSet:   true,
		},
		{name: "both", mutate: withSettings, wantCount: 35, wantSet: true},
		{
			name: "settings values without SettingsPolledAt stay unpublished",
			mutate: func(s entity.Snapshot) entity.Snapshot {
				s = withSettings(s)
				s.SettingsPolledAt = time.Time{}
				return s
			},
			wantCount: 21,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pub := &fakeStatePublisher{}
			ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
			require.NoError(t, ps.Apply(context.Background(), tc.mutate(sampleSnapshot(t))))
			calls := pub.snapshot()
			assert.Len(t, calls, tc.wantCount)
			for _, c := range calls {
				assert.True(t, c.retained)
			}
			_, has := pub.findCall("test/state/alarm_beep")
			assert.Equal(t, tc.wantSet, has)
		})
	}
}

func TestApply_SettingsPayloads(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	require.NoError(t, ps.Apply(context.Background(), withSettings(sampleSnapshot(t))))

	want := map[string]string{
		"nord_start":                "OFF",
		"touch_click":               "ON",
		"alarm_beep":                "ON",
		"stop_on_filter_full":       "ON",
		"auto_restart":              "ON",
		"performance_control":       "ON",
		"dark_screen":               "ON",
		"humidifier":                "OFF",
		"stop_on_conditioner_alarm": "OFF",
		"control_priority":          "temperature",
		"active_filter":             "ON",
		"dark_brightness":           "20",
		"humidity_target":           "50",
		"co2_target":                "500",
	}
	for id, payload := range want {
		assert.Equalf(t, payload, payloadOf(t, pub, id), "%s", id)
	}

	var order []string
	for _, c := range pub.snapshot() {
		order = append(order, c.topic)
	}
	assert.Less(t, indexOf(order, "test/state/filter_resource"), indexOf(order, "test/state/nord_start"),
		"settings follow the filter pair")
	assert.Less(t, indexOf(order, "test/state/nord_start"), indexOf(order, "test/state/co2_target"),
		"settings keep catalog table order")
}

func indexOf(xs []string, x string) int {
	for i, v := range xs {
		if v == x {
			return i
		}
	}
	return -1
}

func TestApply_SelectUnknownPublishesNoneAndWarnsOnce(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, logger)

	snap := withSettings(sampleSnapshot(t))
	raw3 := snap
	raw3.Settings.UserKeys0 = 0x67E4 // bits 13..14 = 3

	warnings := func() int { return strings.Count(buf.String(), "setting value has no label") }

	for range 2 {
		pub.reset()
		require.NoError(t, ps.Apply(context.Background(), raw3))
		assert.Equal(t, "None", payloadOf(t, pub, "control_priority"))
		assert.Equal(t, "ON", payloadOf(t, pub, "alarm_beep"), "other h22 settings still decode")
	}
	assert.Equal(t, 1, warnings(), "the same unknown value warns once")
	assert.Contains(t, buf.String(), "object_id=control_priority raw=3")

	require.NoError(t, ps.Apply(context.Background(), snap))
	assert.Equal(t, 1, warnings(), "a known value does not warn")

	require.NoError(t, ps.Apply(context.Background(), raw3))
	assert.Equal(t, 2, warnings(), "becoming unknown again warns again")
}

func TestApply_NumberOutOfRangePublishesNone(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, logger)

	snap := withSettings(sampleSnapshot(t))
	snap.Settings.HumidityTarget = 96
	snap.Settings.CO2Target = 0
	require.NoError(t, ps.Apply(context.Background(), snap))
	assert.Equal(t, "None", payloadOf(t, pub, "humidity_target"))
	assert.Equal(t, "None", payloadOf(t, pub, "co2_target"))
	assert.Equal(t, "20", payloadOf(t, pub, "dark_brightness"))
	assert.Equal(t, 2, strings.Count(buf.String(), "setting value has no label"))

	snap.Settings.HumidityTarget = 97
	require.NoError(t, ps.Apply(context.Background(), snap))
	assert.Equal(t, 3, strings.Count(buf.String(), "setting value has no label"),
		"a different unknown raw value warns again; co2 stays silent")
}

func heaterStates(t *testing.T, pub *fakeStatePublisher) (heater, action string) {
	t.Helper()
	return payloadOf(t, pub, "heater_active"), payloadOf(t, pub, "hvac_action")
}

func TestHeaterActive_StableAcrossPWMToggle(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)
	snap.Operation = entity.OpIdle
	snap.PIDDemand = 42

	for i, pwm := range []bool{true, false, true} {
		pub.reset()
		snap.HeaterPWM = pwm
		require.NoError(t, ps.Apply(context.Background(), snap))
		heater, action := heaterStates(t, pub)
		assert.Equalf(t, "ON", heater, "poll %d (PWM=%t)", i, pwm)
		assert.Equalf(t, "heating", action, "poll %d (PWM=%t)", i, pwm)
	}
}

func TestHeaterActive_RegZeroIsOff(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name       string
		fanState1  uint16
		wantAction string
	}{
		{name: "fan running", fanState1: 4, wantAction: "fan"},
		{name: "fan stopped", fanState1: 0, wantAction: "idle"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pub := &fakeStatePublisher{}
			ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
			snap := sampleSnapshot(t)
			snap.Operation = entity.OpIdle
			snap.PIDDemand = 0
			snap.HeaterPWM = true
			snap.FanState1 = tc.fanState1
			require.NoError(t, ps.Apply(context.Background(), snap))
			heater, action := heaterStates(t, pub)
			assert.Equal(t, "OFF", heater)
			assert.Equal(t, tc.wantAction, action)
		})
	}
}

func TestHeaterActive_PowerOff(t *testing.T) {
	t.Parallel()
	pub := &fakeStatePublisher{}
	ps := usecase.NewPublishState(pub, fakeTopics{}, discardLoggerState())
	snap := sampleSnapshot(t)
	snap.PowerOn = false
	snap.PIDDemand = 42
	snap.HeaterPWM = true
	require.NoError(t, ps.Apply(context.Background(), snap))
	heater, action := heaterStates(t, pub)
	assert.Equal(t, "OFF", heater)
	assert.Equal(t, "off", action)
}

func TestApply_ConcurrentSettingsWarnState(t *testing.T) {
	t.Parallel()
	ps := usecase.NewPublishState(&fakeStatePublisher{}, fakeTopics{}, discardLoggerState())
	snap := withSettings(sampleSnapshot(t))
	snap.Settings.UserKeys0 = 0x67E4
	var wg sync.WaitGroup
	for range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_ = ps.Apply(context.Background(), snap)
		}()
	}
	wg.Wait()
}
