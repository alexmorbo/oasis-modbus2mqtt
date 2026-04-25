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

	"github.com/alexmorbo/oasis-modbus2mqtt/application/dto"
	"github.com/alexmorbo/oasis-modbus2mqtt/application/usecase"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"
)

type writeCall struct {
	addr  uint16
	value uint16
}

type modifyCall struct {
	addr   uint16
	modify func(uint16) uint16
}

type fakeCommandDispatcher struct {
	mu                sync.Mutex
	writes            []writeCall
	modifies          []modifyCall
	writeErr          error
	modifyErr         error
	writeErrAtCall    int
	writeErrAtCallErr error
}

func (d *fakeCommandDispatcher) WriteHolding(_ context.Context, addr, value uint16) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.writes = append(d.writes, writeCall{addr: addr, value: value})
	if d.writeErrAtCall != 0 && len(d.writes) == d.writeErrAtCall {
		return d.writeErrAtCallErr
	}
	if d.writeErr != nil {
		return d.writeErr
	}
	return nil
}

func (d *fakeCommandDispatcher) ModifyHolding(_ context.Context, addr uint16, modify func(uint16) uint16) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.modifies = append(d.modifies, modifyCall{addr: addr, modify: modify})
	if d.modifyErr != nil {
		return d.modifyErr
	}
	return nil
}

func (d *fakeCommandDispatcher) snapshotWrites() []writeCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]writeCall, len(d.writes))
	copy(out, d.writes)
	return out
}

func (d *fakeCommandDispatcher) snapshotModifies() []modifyCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]modifyCall, len(d.modifies))
	copy(out, d.modifies)
	return out
}

type fakeSnapshots struct {
	mu   sync.Mutex
	snap entity.Snapshot
}

func (f *fakeSnapshots) Snapshot() entity.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap
}

func newSnaps(op entity.OperatingState) *fakeSnapshots {
	return &fakeSnapshots{snap: entity.Snapshot{Operation: op}}
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

func TestNewApplyCommand_NilDispatcher_Panics(t *testing.T) {
	t.Parallel()
	require.PanicsWithValue(t, "apply command: dispatcher must not be nil", func() {
		_ = usecase.NewApplyCommand(nil, newSnaps(entity.OpIdle), nil)
	})
}

func TestNewApplyCommand_NilSnapshots_Panics(t *testing.T) {
	t.Parallel()
	d := &fakeCommandDispatcher{}
	require.PanicsWithValue(t, "apply command: snapshots must not be nil", func() {
		_ = usecase.NewApplyCommand(d, nil, nil)
	})
}

func TestNewApplyCommand_NilLogger_FallsBack(t *testing.T) {
	t.Parallel()
	d := &fakeCommandDispatcher{}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpIdle), nil)
	require.NotNil(t, apply)
	err := apply.Apply(context.Background(), dto.SetPowerCommand{On: true})
	require.NoError(t, err)
	assert.Equal(t, []writeCall{{addr: 2, value: 1}}, d.snapshotWrites())
}

func TestApply_UnknownCommandType_ReturnsErrInvalidPayload(t *testing.T) {
	t.Parallel()
	d := &fakeCommandDispatcher{}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpIdle), discardLogger())

	var cmd dto.Command
	err := apply.Apply(context.Background(), cmd)
	require.Error(t, err)
	assert.ErrorIs(t, err, dto.ErrInvalidPayload)
	assert.Empty(t, d.snapshotWrites())
	assert.Empty(t, d.snapshotModifies())
}

func TestApply_SetPower_On_SingleWrite(t *testing.T) {
	t.Parallel()
	d := &fakeCommandDispatcher{}
	snaps := newSnaps(entity.OpIdle)
	apply := usecase.NewApplyCommand(d, snaps, discardLogger())

	err := apply.Apply(context.Background(), dto.SetPowerCommand{On: true})
	require.NoError(t, err)
	assert.Equal(t, []writeCall{{addr: 2, value: 1}}, d.snapshotWrites())
}

func TestApply_SetPower_Off_EdgeSequence(t *testing.T) {
	t.Parallel()
	d := &fakeCommandDispatcher{}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpIdle), discardLogger()).
		WithPowerOffPause(1 * time.Millisecond)

	start := time.Now()
	err := apply.Apply(context.Background(), dto.SetPowerCommand{On: false})
	elapsed := time.Since(start)
	require.NoError(t, err)
	assert.Equal(t, []writeCall{{addr: 2, value: 1}, {addr: 2, value: 0}}, d.snapshotWrites())
	assert.GreaterOrEqual(t, elapsed, 1*time.Millisecond)
}

func TestApply_SetPower_Off_TransitionGuard_Refuses(t *testing.T) {
	t.Parallel()
	d := &fakeCommandDispatcher{}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpPreheatCalorifier), discardLogger())

	err := apply.Apply(context.Background(), dto.SetPowerCommand{On: false})
	require.Error(t, err)
	assert.ErrorIs(t, err, dto.ErrTransitionInProgress)
	assert.Empty(t, d.snapshotWrites())
}

func TestApply_SetPower_On_AllowedDuringTransition(t *testing.T) {
	t.Parallel()
	d := &fakeCommandDispatcher{}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpPreheatCalorifier), discardLogger())

	err := apply.Apply(context.Background(), dto.SetPowerCommand{On: true})
	require.NoError(t, err)
	assert.Equal(t, []writeCall{{addr: 2, value: 1}}, d.snapshotWrites())
}

func TestApply_SetPower_Off_FirstWriteFails_NoSecondWrite(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("modbus boom")
	d := &fakeCommandDispatcher{
		writeErrAtCall:    1,
		writeErrAtCallErr: sentinel,
	}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpIdle), discardLogger()).
		WithPowerOffPause(1 * time.Millisecond)

	err := apply.Apply(context.Background(), dto.SetPowerCommand{On: false})
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
	assert.Equal(t, 1, len(d.snapshotWrites()))
}

func TestApply_SetPower_Off_ContextCancelledDuringPause(t *testing.T) {
	t.Parallel()
	d := &fakeCommandDispatcher{}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpIdle), discardLogger()).
		WithPowerOffPause(100 * time.Millisecond)

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(10 * time.Millisecond)
		cancel()
	}()

	err := apply.Apply(ctx, dto.SetPowerCommand{On: false})
	require.Error(t, err)
	assert.ErrorIs(t, err, context.Canceled)
	assert.Equal(t, 1, len(d.snapshotWrites()))
}

func TestApply_SetMode_RMWModifyFn(t *testing.T) {
	t.Parallel()
	d := &fakeCommandDispatcher{}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpIdle), discardLogger())

	err := apply.Apply(context.Background(), dto.SetModeCommand{Mode: valueobject.ModeHeat})
	require.NoError(t, err)
	mods := d.snapshotModifies()
	require.Len(t, mods, 1)
	assert.Equal(t, uint16(86), mods[0].addr)

	fn := mods[0].modify
	assert.Equal(t, uint16(0xC001), fn(uint16(0xC003)))
	assert.Equal(t, uint16(0x0001), fn(uint16(0x0000)))
	assert.Equal(t, uint16(0xFFFD), fn(uint16(0xFFFF)))
	assert.Empty(t, d.snapshotWrites())
}

func TestApply_SetMode_TransitionGuard(t *testing.T) {
	t.Parallel()
	d := &fakeCommandDispatcher{}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpStartFan), discardLogger())

	err := apply.Apply(context.Background(), dto.SetModeCommand{Mode: valueobject.ModeAuto})
	require.Error(t, err)
	assert.ErrorIs(t, err, dto.ErrTransitionInProgress)
	assert.Empty(t, d.snapshotModifies())
}

func TestApply_SetMode_DispatcherError_Propagates(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("modify boom")
	d := &fakeCommandDispatcher{modifyErr: sentinel}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpIdle), discardLogger())

	err := apply.Apply(context.Background(), dto.SetModeCommand{Mode: valueobject.ModeCool})
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
}

func TestApply_SetTemperature_WritesH31_RawValue(t *testing.T) {
	t.Parallel()
	d := &fakeCommandDispatcher{}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpIdle), discardLogger())

	err := apply.Apply(context.Background(), dto.SetTemperatureCommand{Temperature: mustTemp(t, 22.5)})
	require.NoError(t, err)
	assert.Equal(t, []writeCall{{addr: 31, value: 225}}, d.snapshotWrites())
}

func TestApply_SetTemperature_TransitionGuard(t *testing.T) {
	t.Parallel()
	d := &fakeCommandDispatcher{}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpOpenDamper), discardLogger())

	err := apply.Apply(context.Background(), dto.SetTemperatureCommand{Temperature: mustTemp(t, 21.0)})
	require.Error(t, err)
	assert.ErrorIs(t, err, dto.ErrTransitionInProgress)
	assert.Empty(t, d.snapshotWrites())
}

func TestApply_SetFan_WritesH32_Value(t *testing.T) {
	t.Parallel()
	d := &fakeCommandDispatcher{}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpIdle), discardLogger())

	err := apply.Apply(context.Background(), dto.SetFanCommand{FanSpeed: mustFan(t, 5)})
	require.NoError(t, err)
	assert.Equal(t, []writeCall{{addr: 32, value: 5}}, d.snapshotWrites())
}

func TestApply_SetFan_TransitionGuard(t *testing.T) {
	t.Parallel()
	d := &fakeCommandDispatcher{}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpRotorSpinup), discardLogger())

	err := apply.Apply(context.Background(), dto.SetFanCommand{FanSpeed: mustFan(t, 7)})
	require.Error(t, err)
	assert.ErrorIs(t, err, dto.ErrTransitionInProgress)
	assert.Empty(t, d.snapshotWrites())
}

func TestApply_DispatcherError_Propagates(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("write boom")
	d := &fakeCommandDispatcher{writeErr: sentinel}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpIdle), discardLogger())

	err := apply.Apply(context.Background(), dto.SetTemperatureCommand{Temperature: mustTemp(t, 20.0)})
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
}

func TestApply_LogsCommand(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	d := &fakeCommandDispatcher{}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpIdle), logger)

	err := apply.Apply(context.Background(), dto.SetTemperatureCommand{Temperature: mustTemp(t, 22.5)})
	require.NoError(t, err)
	out := buf.String()
	assert.Contains(t, out, "applying command")
	assert.Contains(t, out, "SetTemperature(22.5°C)")
}

func TestApply_LogsCommandFailure(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	d := &fakeCommandDispatcher{writeErr: errors.New("nope")}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpIdle), logger)

	err := apply.Apply(context.Background(), dto.SetFanCommand{FanSpeed: mustFan(t, 4)})
	require.Error(t, err)
	out := buf.String()
	assert.Contains(t, out, "command failed")
	assert.Contains(t, out, "SetFan(speed=4)")
}

func discardLogger() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}
