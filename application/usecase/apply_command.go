package usecase

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/dto"
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
}

// SnapshotProvider returns the latest known controller snapshot. ApplyCommand
// uses it to consult Operation for the transition-state guard before any
// command write. *service.Poller (story 008) satisfies this interface via duck
// typing.
type SnapshotProvider interface {
	// Snapshot returns the most recent merged snapshot. The provider must be
	// safe for concurrent calls; ApplyCommand calls Snapshot once per command.
	Snapshot() entity.Snapshot
}

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
	snapshots     SnapshotProvider
	powerOffPause time.Duration
	logger        *slog.Logger
}

// NewApplyCommand constructs an ApplyCommand. A nil dispatcher or nil
// snapshots provider is a programming error and panics. A nil logger falls
// back to slog.Default. powerOffPause defaults to DefaultPowerOffPause.
func NewApplyCommand(d CommandDispatcher, snaps SnapshotProvider, logger *slog.Logger) *ApplyCommand {
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
		snapshots:     snaps,
		powerOffPause: DefaultPowerOffPause,
		logger:        logger,
	}
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
	default:
		return fmt.Errorf("unknown command type %T: %w", cmd, dto.ErrInvalidPayload)
	}

	if err != nil {
		a.logger.Warn("command failed", "command", cmdStr, "error", err)
	}
	return err
}

// setPower writes the Power_Dev (h2) edge sequence. ON is a single rising
// write and bypasses the transition guard so a stuck controller can be
// recovered. OFF is a rising-then-falling sequence with a context-aware dwell
// in between and respects the transition guard.
func (a *ApplyCommand) setPower(ctx context.Context, cmd dto.SetPowerCommand) error {
	if cmd.On {
		if err := a.dispatcher.WriteHolding(ctx, AddrPowerDev, 1); err != nil {
			return fmt.Errorf("power on write h%d: %w", AddrPowerDev, err)
		}
		return nil
	}

	if err := a.transitionGuard(); err != nil {
		return err
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

// transitionGuard returns a wrapped dto.ErrTransitionInProgress when the
// latest snapshot reports a non-idle, non-unknown operating state. Used by
// every write path except SetPower ON (which is the recovery escape hatch).
func (a *ApplyCommand) transitionGuard() error {
	snap := a.snapshots.Snapshot()
	if snap.Operation.IsTransitional() {
		return fmt.Errorf("operation %s in progress: %w", snap.Operation, dto.ErrTransitionInProgress)
	}
	return nil
}
