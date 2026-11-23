package usecase_test

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
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

// fakeCommandDispatcher holds a register map: ModifyHolding applies modify to
// the stored value under its mutex and, like the real client, skips the write
// when the value is unchanged; WriteHolding stores; ReadHolding returns the
// stored value. ignoreWrites simulates a controller that ACKs and ignores.
// ops records the Modbus-level sequence ("write h22", "skip h22", "read h22").
type fakeCommandDispatcher struct {
	mu                sync.Mutex
	writes            []writeCall
	modifies          []modifyCall
	modifyWrites      []writeCall
	ops               []string
	reads             []uint16
	regs              map[uint16]uint16
	ignoreWrites      map[uint16]bool
	writeErr          error
	modifyErr         error
	readErr           error
	readLen           int
	writeErrAtCall    int
	writeErrAtCallErr error
	// afterWrite runs under mu after a stored write, before any read-back.
	afterWrite func(regs map[uint16]uint16)
	// pending counts writes not yet followed by a read-back; maxPending > 1
	// means two settings commands interleaved.
	pending    int
	maxPending int
}

func (d *fakeCommandDispatcher) store(addr, value uint16) {
	if d.ignoreWrites[addr] {
		return
	}
	if d.regs == nil {
		d.regs = map[uint16]uint16{}
	}
	d.regs[addr] = value
	if d.afterWrite != nil {
		d.afterWrite(d.regs)
	}
}

func (d *fakeCommandDispatcher) markPending() {
	d.pending++
	if d.pending > d.maxPending {
		d.maxPending = d.pending
	}
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
	d.store(addr, value)
	d.markPending()
	return nil
}

func (d *fakeCommandDispatcher) ModifyHolding(_ context.Context, addr uint16, modify func(uint16) uint16) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.modifies = append(d.modifies, modifyCall{addr: addr, modify: modify})
	if d.modifyErr != nil {
		return d.modifyErr
	}
	newVal := modify(d.regs[addr])
	d.markPending()
	if newVal == d.regs[addr] {
		d.ops = append(d.ops, fmt.Sprintf("skip h%d", addr))
		return nil
	}
	d.ops = append(d.ops, fmt.Sprintf("write h%d", addr))
	d.modifyWrites = append(d.modifyWrites, writeCall{addr: addr, value: newVal})
	d.store(addr, newVal)
	return nil
}

func (d *fakeCommandDispatcher) ReadHolding(_ context.Context, start, count uint16) ([]uint16, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.reads = append(d.reads, start)
	d.ops = append(d.ops, fmt.Sprintf("read h%d", start))
	d.pending--
	if d.readErr != nil {
		return nil, d.readErr
	}
	n := int(count)
	if d.readLen != 0 {
		n = d.readLen
	}
	out := make([]uint16, n)
	for i := range out {
		out[i] = d.regs[start+uint16(i)] //nolint:gosec // test fixture, i < count
	}
	return out, nil
}

func (d *fakeCommandDispatcher) reg(addr uint16) uint16 {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.regs[addr]
}

func (d *fakeCommandDispatcher) calls() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return len(d.writes) + len(d.modifies) + len(d.reads)
}

func (d *fakeCommandDispatcher) snapshotWrites() []writeCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]writeCall, len(d.writes))
	copy(out, d.writes)
	return out
}

func (d *fakeCommandDispatcher) snapshotModifyWrites() []writeCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]writeCall(nil), d.modifyWrites...)
}

func (d *fakeCommandDispatcher) snapshotOps() []string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return append([]string(nil), d.ops...)
}

func (d *fakeCommandDispatcher) snapshotModifies() []modifyCall {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]modifyCall, len(d.modifies))
	copy(out, d.modifies)
	return out
}

type fakeSnapshots struct {
	mu        sync.Mutex
	snap      entity.Snapshot
	merges    []writeCall
	notPolled bool
}

func (f *fakeSnapshots) Snapshot() entity.Snapshot {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.snap
}

func (f *fakeSnapshots) MergeSettingRegister(addr, value uint16) bool {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.merges = append(f.merges, writeCall{addr: addr, value: value})
	return !f.notPolled
}

func (f *fakeSnapshots) snapshotMerges() []writeCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]writeCall(nil), f.merges...)
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

func TestApply_SetPower_On_BlockedDuringTransition(t *testing.T) {
	t.Parallel()
	d := &fakeCommandDispatcher{}
	apply := usecase.NewApplyCommand(d, newSnaps(entity.OpPreheatCalorifier), discardLogger())

	err := apply.Apply(context.Background(), dto.SetPowerCommand{On: true})
	require.Error(t, err)
	assert.ErrorIs(t, err, dto.ErrTransitionInProgress)
	assert.Empty(t, d.snapshotWrites(),
		"SetPower(on=true) must respect transition guard — controller ignores writes during shutdown phases")
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

type fakeRecorder struct {
	mu  sync.Mutex
	ids []string
}

func (r *fakeRecorder) WriteNotApplied(objectID string) {
	r.mu.Lock()
	r.ids = append(r.ids, objectID)
	r.mu.Unlock()
}

func (r *fakeRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.ids...)
}

// settingsRig wires an ApplyCommand against a fake controller preset to the
// 2026-11-23 live dump, with a snapshot whose cached settings are all zero.
type settingsRig struct {
	d     *fakeCommandDispatcher
	store *fakeSnapshots
	rec   *fakeRecorder
	apply *usecase.ApplyCommand
	logs  *bytes.Buffer
}

func newSettingsRig(t *testing.T) *settingsRig {
	t.Helper()
	d := &fakeCommandDispatcher{regs: map[uint16]uint16{
		22: 0x07E4, 23: 20, 24: 3, 25: 0x0004, 35: 50, 36: 500,
	}}
	store := &fakeSnapshots{snap: entity.Snapshot{
		Operation:        entity.OpIdle,
		SettingsPolledAt: time.Unix(100, 0),
	}}
	rec := &fakeRecorder{}
	logs := &bytes.Buffer{}
	logger := slog.New(slog.NewTextHandler(logs, &slog.HandlerOptions{Level: slog.LevelDebug}))
	apply := usecase.NewApplyCommand(d, store, logger).WithWriteNotAppliedRecorder(rec)
	return &settingsRig{d: d, store: store, rec: rec, apply: apply, logs: logs}
}

func (r *settingsRig) set(id, payload string) error {
	return r.apply.Apply(context.Background(), dto.SetSettingCommand{ObjectID: id, Payload: payload})
}

func TestSetSetting_RMWUsesFreshRegisterNotSnapshot(t *testing.T) {
	t.Parallel()
	r := newSettingsRig(t)
	require.Zero(t, r.store.Snapshot().Settings.UserKeys0, "cached snapshot says h22=0x0000")

	require.NoError(t, r.set("alarm_beep", "OFF"))
	assert.Equal(t, uint16(0x07A4), r.d.reg(22), "RMW base is the controller's 0x07E4, not the snapshot's 0x0000")
	assert.Empty(t, r.d.snapshotWrites(), "bitfields never use a plain WriteHolding")
	require.Len(t, r.d.snapshotModifies(), 1)
	assert.Equal(t, []string{"write h22", "read h22"}, r.d.snapshotOps(), "a changing toggle writes, then reads back")
	assert.Equal(t, []writeCall{{addr: 22, value: 0x07A4}}, r.store.snapshotMerges())
	assert.Empty(t, r.rec.snapshot())
	assert.Contains(t, r.logs.String(), "setting applied")
	assert.Contains(t, r.logs.String(), "object_id=alarm_beep value=0")
}

func TestSetSetting_BitfieldPreservesOtherBits(t *testing.T) {
	t.Parallel()
	tests := []struct {
		id      string
		payload string
		addr    uint16
		before  uint16
		want    uint16
	}{
		{id: "alarm_beep", payload: "OFF", addr: 22, before: 0x07E4, want: 0x07A4},
		{id: "control_priority", payload: "co2", addr: 22, before: 0x07E4, want: 0x47E4},
		{id: "nord_start", payload: "ON", addr: 22, before: 0x07E4, want: 0x07E6},
		{id: "active_filter", payload: "OFF", addr: 25, before: 0x00A5, want: 0x00A1},
	}
	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()
			r := newSettingsRig(t)
			r.d.regs[tc.addr] = tc.before
			require.NoError(t, r.set(tc.id, tc.payload))
			assert.Equal(t, tc.want, r.d.reg(tc.addr), "got 0x%04X", r.d.reg(tc.addr))
			assert.Equal(t, []writeCall{{addr: tc.addr, value: tc.want}}, r.store.snapshotMerges())
		})
	}
}

func TestSetSetting_NumberUsesModifyHoldingAndReadsBack(t *testing.T) {
	t.Parallel()
	r := newSettingsRig(t)

	require.NoError(t, r.set("humidity_target", "55.0"))
	assert.Empty(t, r.d.snapshotWrites(), "numbers go through ModifyHolding for the fresh read and no-op skip")
	mods := r.d.snapshotModifies()
	require.Len(t, mods, 1)
	assert.Equal(t, uint16(55), mods[0].modify(0xFFFF), "the number closure ignores the current value")
	assert.Equal(t, []writeCall{{addr: 35, value: 55}}, r.d.snapshotModifyWrites())
	assert.Equal(t, []string{"write h35", "read h35"}, r.d.snapshotOps(), "exactly one read-back of h35")
	assert.Equal(t, []writeCall{{addr: 35, value: 55}}, r.store.snapshotMerges())
}

func TestSetSetting_NoOpSkipsWriteButReadsBackAndMerges(t *testing.T) {
	t.Parallel()
	tests := []struct {
		id      string
		payload string
		addr    uint16
		raw     uint16
	}{
		{id: "alarm_beep", payload: "ON", addr: 22, raw: 0x07E4},
		{id: "control_priority", payload: "temperature", addr: 22, raw: 0x07E4},
		{id: "active_filter", payload: "ON", addr: 25, raw: 0x0004},
		{id: "humidity_target", payload: "50", addr: 35, raw: 50},
		{id: "co2_target", payload: "500", addr: 36, raw: 500},
	}
	for _, tc := range tests {
		t.Run(tc.id, func(t *testing.T) {
			t.Parallel()
			r := newSettingsRig(t)
			require.NoError(t, r.set(tc.id, tc.payload))
			hreg := fmt.Sprintf("h%d", tc.addr)
			assert.Equal(t, []string{"skip " + hreg, "read " + hreg}, r.d.snapshotOps(),
				"zero writes, then the read-back")
			assert.Empty(t, r.d.snapshotModifyWrites())
			assert.Empty(t, r.d.snapshotWrites())
			assert.Equal(t, []writeCall{{addr: tc.addr, value: tc.raw}}, r.store.snapshotMerges(),
				"the read-back is still merged so HA converges")
			assert.Equal(t, tc.raw, r.d.reg(tc.addr))
			assert.Empty(t, r.rec.snapshot())
			assert.Contains(t, r.logs.String(), "setting applied")
		})
	}
}

func TestSetSetting_ReadBackMismatch(t *testing.T) {
	t.Parallel()
	r := newSettingsRig(t)
	r.d.ignoreWrites = map[uint16]bool{36: true}

	err := r.set("co2_target", "800")
	require.Error(t, err)
	assert.ErrorIs(t, err, dto.ErrWriteNotApplied)
	assert.Equal(t, []writeCall{{addr: 36, value: 800}}, r.d.snapshotModifyWrites(), "the write was ACKed")
	assert.Equal(t, []writeCall{{addr: 36, value: 500}}, r.store.snapshotMerges(),
		"the ACTUAL read-back value is merged, never the requested 800")
	assert.Equal(t, []string{"co2_target"}, r.rec.snapshot())

	out := r.logs.String()
	assert.Contains(t, out, "level=WARN msg=\"setting write not applied\"")
	assert.Contains(t, out, "object_id=co2_target")
	assert.Contains(t, out, "register=h36 requested=800 actual=500 raw=0x01F4")
	assert.NotContains(t, out, "setting applied")
	assert.Equal(t, 1, strings.Count(out, "level=WARN"), "exactly one WARN per not-applied write:\n%s", out)
	assert.Contains(t, out, "level=DEBUG msg=\"command failed\"")
}

func TestSetSetting_BitfieldMismatchComparesOnlyTargetBits(t *testing.T) {
	t.Parallel()
	r := newSettingsRig(t)
	r.d.afterWrite = func(regs map[uint16]uint16) { regs[22] ^= 1 << 5 }

	require.NoError(t, r.set("alarm_beep", "OFF"), "a panel change to another bit is not a mismatch")
	assert.Equal(t, []writeCall{{addr: 22, value: 0x07A4 ^ 0x20}}, r.store.snapshotMerges(),
		"the actual raw register, including the foreign bit change, is merged")
	assert.Empty(t, r.rec.snapshot())
}

func TestSetSetting_ReadBackError_NoMergeNoRecorder(t *testing.T) {
	t.Parallel()

	t.Run("read error", func(t *testing.T) {
		t.Parallel()
		r := newSettingsRig(t)
		sentinel := errors.New("read-back boom")
		r.d.readErr = sentinel
		err := r.set("humidity_target", "60")
		require.Error(t, err)
		assert.ErrorIs(t, err, sentinel)
		assert.NotErrorIs(t, err, dto.ErrWriteNotApplied)
		assert.Contains(t, err.Error(), "read-back h35")
		assert.Empty(t, r.store.snapshotMerges())
		assert.Empty(t, r.rec.snapshot())
	})

	t.Run("wrong length", func(t *testing.T) {
		t.Parallel()
		r := newSettingsRig(t)
		r.d.readLen = 2
		err := r.set("alarm_beep", "OFF")
		require.Error(t, err)
		assert.ErrorIs(t, err, usecase.ErrUnexpectedReadLength)
		assert.Empty(t, r.store.snapshotMerges())
		assert.Empty(t, r.rec.snapshot())
	})

	t.Run("write error", func(t *testing.T) {
		t.Parallel()
		r := newSettingsRig(t)
		sentinel := errors.New("modify boom")
		r.d.modifyErr = sentinel
		err := r.set("alarm_beep", "OFF")
		require.ErrorIs(t, err, sentinel)
		assert.Contains(t, err.Error(), "set setting alarm_beep write h22")
		assert.Empty(t, r.d.reads, "no read-back after a failed write")
		assert.Empty(t, r.store.snapshotMerges())
	})
}

func TestSetSetting_NotYetPolledStillSucceeds(t *testing.T) {
	t.Parallel()
	r := newSettingsRig(t)
	r.store.notPolled = true

	require.NoError(t, r.set("touch_click", "OFF"))
	assert.Equal(t, uint16(0x07C4), r.d.reg(22))
	assert.Contains(t, r.logs.String(), "settings not yet polled")
}

func TestSetSetting_ValidationRejectsBeforeModbus(t *testing.T) {
	t.Parallel()
	tests := []struct {
		name    string
		id      string
		payload string
	}{
		{name: "unknown object id", id: "nonexistent", payload: "ON"},
		{name: "power is not a setting", id: "power", payload: "ON"},
		{name: "switch garbage", id: "alarm_beep", payload: "maybe"},
		{name: "switch empty", id: "alarm_beep", payload: ""},
		{name: "select unknown option", id: "control_priority", payload: "pressure"},
		{name: "select raw digit", id: "control_priority", payload: "2"},
		{name: "select raw 3 never written", id: "control_priority", payload: "3"},
		{name: "select None", id: "control_priority", payload: "None"},
		{name: "number non-numeric", id: "humidity_target", payload: "abc"},
		{name: "number NaN", id: "humidity_target", payload: "NaN"},
		{name: "number Inf", id: "humidity_target", payload: "Inf"},
		{name: "number below range", id: "humidity_target", payload: "19"},
		{name: "number above range", id: "humidity_target", payload: "200"},
		{name: "number non-integral", id: "humidity_target", payload: "57.5"},
		{name: "number off step", id: "co2_target", payload: "505"},
		{name: "number negative", id: "dark_brightness", payload: "-1"},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			r := newSettingsRig(t)
			err := r.set(tc.id, tc.payload)
			require.Error(t, err)
			assert.ErrorIs(t, err, dto.ErrInvalidPayload)
			assert.Zero(t, r.d.calls(), "no Modbus traffic on a rejected payload")
			assert.Empty(t, r.store.snapshotMerges())
		})
	}
}

func TestSetSetting_TransitionGuard(t *testing.T) {
	t.Parallel()
	r := newSettingsRig(t)
	r.store.snap.Operation = entity.OpPreheatCalorifier

	err := r.set("alarm_beep", "OFF")
	require.Error(t, err)
	assert.ErrorIs(t, err, dto.ErrTransitionInProgress)
	assert.Zero(t, r.d.calls())
}

// bitsForced decodes which bits a recorded RMW closure forces and to what.
func bitsForced(modify func(uint16) uint16) (mask, value uint16) {
	lo, hi := modify(0x0000), modify(0xFFFF)
	mask = ^(lo ^ hi)
	return mask, lo & mask
}

func TestSetSetting_ConcurrentTogglesSerialized(t *testing.T) {
	t.Parallel()
	r := newSettingsRig(t)
	const before = uint16(0x07E4)

	var (
		wg   sync.WaitGroup
		mu   sync.Mutex
		errs []error
	)
	for i := range 20 {
		for _, id := range []string{"touch_click", "alarm_beep"} {
			wg.Add(1)
			go func() {
				defer wg.Done()
				payload := "ON"
				if i%2 == 1 {
					payload = "OFF"
				}
				if err := r.set(id, payload); err != nil {
					mu.Lock()
					errs = append(errs, err)
					mu.Unlock()
				}
			}()
		}
	}
	wg.Wait()
	require.Empty(t, errs, "no ErrWriteNotApplied (or any error) from interleaving")
	assert.Equal(t, 1, r.d.maxPending, "each write is read back before the next settings write")

	final := r.d.reg(22)
	mods := r.d.snapshotModifies()
	require.Len(t, mods, 40)
	for _, bit := range []uint16{5, 6} {
		var last uint16
		for _, m := range mods {
			if mask, v := bitsForced(m.modify); mask == 1<<bit {
				last = v
			}
		}
		assert.Equalf(t, last, final&(1<<bit), "bit %d equals the last value applied", bit)
	}
	assert.Equal(t, before&^0x60, final&^0x60, "other bits untouched")
	assert.Len(t, r.store.snapshotMerges(), 40)
}

func TestWithWriteNotAppliedRecorder_NilRestoresNoop(t *testing.T) {
	t.Parallel()
	d := &fakeCommandDispatcher{regs: map[uint16]uint16{36: 500}, ignoreWrites: map[uint16]bool{36: true}}
	store := &fakeSnapshots{snap: entity.Snapshot{Operation: entity.OpIdle}}
	apply := usecase.NewApplyCommand(d, store, discardLogger()).WithWriteNotAppliedRecorder(nil)
	err := apply.Apply(context.Background(), dto.SetSettingCommand{ObjectID: "co2_target", Payload: "800"})
	assert.ErrorIs(t, err, dto.ErrWriteNotApplied)
}
