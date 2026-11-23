package usecase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/dto"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/catalog"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"
)

// CommandDispatcher is the subset of the Modbus dispatcher API used by
// ApplyCommand for write-side operations. Defining the interface here (rather
// than importing application/service) keeps the use case independent of the
// service implementation and breaks a would-be cyclic dependency.
type CommandDispatcher interface {
	// WriteHolding writes a single holding register at addr.
	WriteHolding(ctx context.Context, addr, value uint16) error
	// ModifyHolding atomically reads addr, applies modify, and writes the
	// result back. Implementations are expected to serialize the read+write.
	ModifyHolding(ctx context.Context, addr uint16, modify func(uint16) uint16) error
	// ReadHolding reads count holding registers starting at start.
	ReadHolding(ctx context.Context, start, count uint16) ([]uint16, error)
}

// SnapshotStore is the latest-snapshot view ApplyCommand needs: Snapshot for
// the transition guard, and MergeSettingRegister to publish a read-back
// settings register at once. *service.Poller satisfies it via duck typing.
type SnapshotStore interface {
	// Snapshot returns the most recent merged snapshot. The provider must be
	// safe for concurrent calls; ApplyCommand calls Snapshot once per command.
	Snapshot() entity.Snapshot
	// MergeSettingRegister merges a read-back settings register and fans the
	// snapshot out to subscribers. It returns false before the first settings read.
	MergeSettingRegister(addr, value uint16) bool
}

// WriteNotAppliedRecorder counts writes the controller ACKed but did not apply.
type WriteNotAppliedRecorder interface{ WriteNotApplied(objectID string) }

type noopWriteNotAppliedRecorder struct{}

func (noopWriteNotAppliedRecorder) WriteNotApplied(string) {}

// Modbus holding-register addresses written by ApplyCommand.
const (
	// AddrPowerDev is holding register Power_Dev (h2): edge-trigger power.
	AddrPowerDev uint16 = 2
	// AddrTempTarget is holding register Temp_Target (h31): setpoint ×10 °C.
	AddrTempTarget uint16 = 31
	// AddrFanTarget1 is holding register Fan_Target_1 (h32): fan-speed [1,10].
	AddrFanTarget1 uint16 = 32
	// AddrDevKeys2 is holding register Dev_Keys_2 (h86): mode in bits 0..1.
	AddrDevKeys2 uint16 = 86
)

// DefaultPowerOffPause is the dwell between the rising and falling edges of
// the Power_Dev OFF sequence. 150 ms exceeds the controller's documented
// minimum guard interval (100 ms, quirk #3) without noticeably delaying UX.
const DefaultPowerOffPause = 150 * time.Millisecond

// ApplyCommand is the single write-side use case of the bridge: it accepts
// validated dto.Command values and translates them into the correct sequence
// of Modbus writes for the Oasis Syberia controller, applying the
// transition-state guard and Power_Dev edge-trigger quirks.
type ApplyCommand struct {
	dispatcher    CommandDispatcher
	store         SnapshotStore
	recorder      WriteNotAppliedRecorder
	powerOffPause time.Duration
	logger        *slog.Logger

	// settingsMu serializes settings commands end to end (write, read-back,
	// merge), so two toggles of one bit cannot interleave into a false mismatch.
	settingsMu sync.Mutex
}

// NewApplyCommand constructs an ApplyCommand. A nil dispatcher or nil
// snapshot store is a programming error and panics. A nil logger falls
// back to slog.Default. powerOffPause defaults to DefaultPowerOffPause and
// the write-not-applied recorder to a no-op.
func NewApplyCommand(d CommandDispatcher, snaps SnapshotStore, logger *slog.Logger) *ApplyCommand {
	if d == nil {
		panic("apply command: dispatcher must not be nil")
	}
	if snaps == nil {
		panic("apply command: snapshots must not be nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &ApplyCommand{
		dispatcher:    d,
		store:         snaps,
		recorder:      noopWriteNotAppliedRecorder{},
		powerOffPause: DefaultPowerOffPause,
		logger:        logger,
	}
}

// WithWriteNotAppliedRecorder sets the recorder notified when a settings
// write is ACKed but not applied. A nil recorder restores the no-op default.
func (a *ApplyCommand) WithWriteNotAppliedRecorder(r WriteNotAppliedRecorder) *ApplyCommand {
	if r == nil {
		r = noopWriteNotAppliedRecorder{}
	}
	a.recorder = r
	return a
}

// WithPowerOffPause overrides the dwell between the two Power_Dev writes in
// the OFF edge sequence. The setter is fluent and exists primarily for tests;
// callers are responsible for picking a value at least as long as the
// controller's guard interval.
func (a *ApplyCommand) WithPowerOffPause(d time.Duration) *ApplyCommand {
	a.powerOffPause = d
	return a
}

// Apply dispatches cmd to the appropriate write sequence. It returns an
// error wrapping dto.ErrInvalidPayload for unknown or nil commands,
// dto.ErrTransitionInProgress when the controller is mid-transition (except
// for SetPower ON, which bypasses the guard), or whatever error the
// dispatcher returns verbatim-wrapped with context.
func (a *ApplyCommand) Apply(ctx context.Context, cmd dto.Command) error {
	if cmd == nil {
		a.logger.Warn("apply: nil command")
		return fmt.Errorf("nil command: %w", dto.ErrInvalidPayload)
	}

	cmdStr := "<unknown>"
	if stringer, ok := cmd.(fmt.Stringer); ok {
		cmdStr = stringer.String()
	}
	a.logger.Info("applying command", "command", cmdStr)

	var err error
	switch c := cmd.(type) {
	case dto.SetPowerCommand:
		err = a.setPower(ctx, c)
	case dto.SetModeCommand:
		err = a.setMode(ctx, c)
	case dto.SetTemperatureCommand:
		err = a.setTemperature(ctx, c)
	case dto.SetFanCommand:
		err = a.setFan(ctx, c)
	case dto.SetSettingCommand:
		err = a.setSetting(ctx, c)
	default:
		return fmt.Errorf("unknown command type %T: %w", cmd, dto.ErrInvalidPayload)
	}

	switch {
	case err == nil:
	case errors.Is(err, dto.ErrWriteNotApplied):
		// setSetting already logged one WARN with object_id, requested and actual.
		a.logger.Debug("command failed", "command", cmdStr, "error", err)
	default:
		a.logger.Warn("command failed", "command", cmdStr, "error", err)
	}
	return err
}

// setPower writes the Power_Dev (h2) edge sequence. ON is a single rising
// write; OFF is a rising-then-falling sequence with a context-aware dwell
// in between. Both paths respect the transition guard — the controller's
// shutdown phases (close_damper, electric_calorifier_purge) take 30-60s
// and ignore writes during that window, so we reject early instead of
// silently dropping the user's command.
func (a *ApplyCommand) setPower(ctx context.Context, cmd dto.SetPowerCommand) error {
	if err := a.transitionGuard(); err != nil {
		return err
	}

	if cmd.On {
		if err := a.dispatcher.WriteHolding(ctx, AddrPowerDev, 1); err != nil {
			return fmt.Errorf("power on write h%d: %w", AddrPowerDev, err)
		}
		return nil
	}

	if err := a.dispatcher.WriteHolding(ctx, AddrPowerDev, 1); err != nil {
		return fmt.Errorf("power off rising-edge write h%d: %w", AddrPowerDev, err)
	}

	select {
	case <-time.After(a.powerOffPause):
	case <-ctx.Done():
		return ctx.Err()
	}

	if err := a.dispatcher.WriteHolding(ctx, AddrPowerDev, 0); err != nil {
		return fmt.Errorf("power off falling-edge write h%d: %w", AddrPowerDev, err)
	}

	a.logger.Info("power off sequence completed")
	return nil
}

// setMode performs an atomic read-modify-write on Dev_Keys_2 (h86), replacing
// only bits 0..1 with cmd.Mode and preserving every other bit (timer flags,
// humidifier-type, etc.). Respects the transition guard.
func (a *ApplyCommand) setMode(ctx context.Context, cmd dto.SetModeCommand) error {
	if err := a.transitionGuard(); err != nil {
		return err
	}
	mode := cmd.Mode
	if err := a.dispatcher.ModifyHolding(ctx, AddrDevKeys2, func(cur uint16) uint16 {
		return valueobject.ApplyToRegister(cur, mode)
	}); err != nil {
		return fmt.Errorf("set mode modify h%d: %w", AddrDevKeys2, err)
	}
	return nil
}

// setTemperature writes the validated setpoint to Temp_Target (h31). Respects
// the transition guard. Range validation lives in valueobject.Temperature.
func (a *ApplyCommand) setTemperature(ctx context.Context, cmd dto.SetTemperatureCommand) error {
	if err := a.transitionGuard(); err != nil {
		return err
	}
	if err := a.dispatcher.WriteHolding(ctx, AddrTempTarget, cmd.Temperature.Raw()); err != nil {
		return fmt.Errorf("set temperature write h%d: %w", AddrTempTarget, err)
	}
	return nil
}

// setFan writes the validated fan speed to Fan_Target_1 (h32). Respects the
// transition guard. Range validation lives in valueobject.FanSpeed.
func (a *ApplyCommand) setFan(ctx context.Context, cmd dto.SetFanCommand) error {
	if err := a.transitionGuard(); err != nil {
		return err
	}
	if err := a.dispatcher.WriteHolding(ctx, AddrFanTarget1, cmd.FanSpeed.Value()); err != nil {
		return fmt.Errorf("set fan write h%d: %w", AddrFanTarget1, err)
	}
	return nil
}

// setSetting validates and writes one panel setting, then reads the register
// back and publishes the actual value. Bitfields are RMW on a fresh controller
// read, never on the cached snapshot. A read-back that does not carry the
// requested field returns dto.ErrWriteNotApplied: the controller can ACK a
// write and silently ignore it (quirk §13).
func (a *ApplyCommand) setSetting(ctx context.Context, cmd dto.SetSettingCommand) error {
	def, ok := catalog.SettingByObjectID(cmd.ObjectID)
	if !ok {
		return fmt.Errorf("unknown setting %q: %w", cmd.ObjectID, dto.ErrInvalidPayload)
	}
	field, err := def.ParseCommand(cmd.Payload)
	if err != nil {
		return fmt.Errorf("%w: %w", dto.ErrInvalidPayload, err)
	}
	if err := a.transitionGuard(); err != nil {
		return err
	}

	a.settingsMu.Lock()
	defer a.settingsMu.Unlock()

	// Both kinds go through ModifyHolding: a fresh read under the client
	// mutex, and no write when the register already holds the target
	// (h22..h36 are EEPROM-backed). The read-back below still runs so HA
	// converges on the actual value.
	addr := def.Addr.Uint16()
	modify := func(cur uint16) uint16 { return def.Apply(cur, field) }
	if !def.IsBitfield() {
		modify = func(uint16) uint16 { return field }
	}
	if err := a.dispatcher.ModifyHolding(ctx, addr, modify); err != nil {
		return fmt.Errorf("set setting %s write h%d: %w", def.ObjectID, addr, err)
	}

	regs, err := a.dispatcher.ReadHolding(ctx, addr, 1)
	if err == nil && len(regs) != 1 {
		err = fmt.Errorf("got %d regs: %w", len(regs), ErrUnexpectedReadLength)
	}
	if err != nil {
		return fmt.Errorf("set setting %s read-back h%d: %w", def.ObjectID, addr, err)
	}
	actual := regs[0]

	if !a.store.MergeSettingRegister(addr, actual) {
		a.logger.Debug("setting read-back not merged: settings not yet polled",
			"object_id", def.ObjectID, "register", fmt.Sprintf("h%d", addr))
	}

	if !def.Matches(actual, field) {
		a.logger.Warn("setting write not applied",
			"object_id", def.ObjectID,
			"register", fmt.Sprintf("h%d", addr),
			"requested", field,
			"actual", def.Field(actual),
			"raw", fmt.Sprintf("0x%04X", actual),
		)
		a.recorder.WriteNotApplied(def.ObjectID)
		return fmt.Errorf("setting %s h%d: requested %d, read back %d: %w",
			def.ObjectID, addr, field, def.Field(actual), dto.ErrWriteNotApplied)
	}

	a.logger.Info("setting applied", "object_id", def.ObjectID, "value", field)
	return nil
}

// transitionGuard returns a wrapped dto.ErrTransitionInProgress when the
// latest snapshot reports a non-idle, non-unknown operating state. Used by
// every write path except SetPower ON (which is the recovery escape hatch).
func (a *ApplyCommand) transitionGuard() error {
	snap := a.store.Snapshot()
	if snap.Operation.IsTransitional() {
		return fmt.Errorf("operation %s in progress: %w", snap.Operation, dto.ErrTransitionInProgress)
	}
	return nil
}
