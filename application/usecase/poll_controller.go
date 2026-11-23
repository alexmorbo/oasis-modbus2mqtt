// Package usecase contains application use cases that orchestrate domain
// entities and ports. Files in this package depend on domain/* and
// application/port; they never import infrastructure adapters or
// application/service implementations directly.
package usecase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/port"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/catalog"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"
)

// ErrUnexpectedReadLength is returned when a Modbus read returns fewer
// registers than the call requested. PollController wraps it with the batch
// label so callers can identify the offending tier read.
var ErrUnexpectedReadLength = errors.New("unexpected modbus read length")

// Dispatcher is the local read-only view of the Modbus dispatcher used by
// PollController. service.CommandDispatcher (story 007) satisfies it via
// duck typing — defining the interface here breaks the would-be cyclic
// dependency between application/usecase and application/service.
type Dispatcher interface {
	// ReadInput reads count input registers starting at start.
	ReadInput(ctx context.Context, start, count uint16) ([]uint16, error)
	// ReadHolding reads count holding registers starting at start.
	ReadHolding(ctx context.Context, start, count uint16) ([]uint16, error)
}

// PollController performs tiered Modbus reads and decodes raw register
// values into a partially populated entity.Snapshot. Each Poll* method
// returns only the fields owned by its tier; the caller (Poller) merges
// the partial snapshots into a single shared latest snapshot.
type PollController struct {
	dispatcher Dispatcher
	clock      port.Clock
	logger     *slog.Logger
}

// NewPollController constructs a PollController. A nil dispatcher is a
// programming error and panics. A nil clock falls back to port.RealClock; a
// nil logger to slog.Default.
func NewPollController(d Dispatcher, clock port.Clock, logger *slog.Logger) *PollController {
	if d == nil {
		panic("poll controller: dispatcher must not be nil")
	}
	if clock == nil {
		clock = port.RealClock{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &PollController{dispatcher: d, clock: clock, logger: logger}
}

// PollHot reads the hot-tier register set (i2..i18, i25..i30, h31..h32) and
// returns a Snapshot populated with the hot-tier fields. PolledAt is set
// from the injected clock once after all reads succeed.
func (p *PollController) PollHot(ctx context.Context) (entity.Snapshot, error) {
	in1, err := p.dispatcher.ReadInput(ctx, 2, 17)
	if err != nil {
		return entity.Snapshot{}, fmt.Errorf("poll_hot read i2..i18: %w", err)
	}
	if len(in1) != 17 {
		return entity.Snapshot{}, fmt.Errorf("poll_hot read i2..i18: got %d regs: %w", len(in1), ErrUnexpectedReadLength)
	}

	in2, err := p.dispatcher.ReadInput(ctx, 25, 6)
	if err != nil {
		return entity.Snapshot{}, fmt.Errorf("poll_hot read i25..i30: %w", err)
	}
	if len(in2) != 6 {
		return entity.Snapshot{}, fmt.Errorf("poll_hot read i25..i30: got %d regs: %w", len(in2), ErrUnexpectedReadLength)
	}

	hold, err := p.dispatcher.ReadHolding(ctx, 31, 2)
	if err != nil {
		return entity.Snapshot{}, fmt.Errorf("poll_hot read h31..h32: %w", err)
	}
	if len(hold) != 2 {
		return entity.Snapshot{}, fmt.Errorf("poll_hot read h31..h32: got %d regs: %w", len(hold), ErrUnexpectedReadLength)
	}

	state0 := in1[0]
	state1 := in1[1]
	dout := in1[14]

	snap := entity.Snapshot{
		PowerOn:           state0&(1<<0) != 0,
		Switching:         state0&(1<<1) != 0,
		HeatCapable:       state0&(1<<6) != 0,
		CoolCapable:       state0&(1<<7) != 0,
		Operation:         entity.OperatingStateFromRegister(state1),
		OperationTimeLeft: entity.ParseLastTime(in1[4]),
		SupplyTemp:        signedTempFromRaw(in1[7]),
		FilterPct:         int16(in1[12]), //nolint:gosec // same-size reinterpret; controller documented to return int16 percent
		HeaterPWM:         (dout & 0x3) > 0,
		DamperOpen:        (dout & 0x20) != 0,
		PIDDemand:         in1[16],
		FanState1:         in2[0],
		FanState2:         in2[5],
		TargetTemp:        valueobject.TemperatureFromRaw(hold[0]),
		FanTarget1:        hold[1],
		PolledAt:          p.clock.Now(),
	}
	snap.RawErrors[0] = in1[2]
	snap.RawErrors[1] = in1[3]
	return snap, nil
}

// PollMedium reads the medium-tier register set (i57..i58, i69..i70, h86)
// and returns a Snapshot populated with the medium-tier fields.
func (p *PollController) PollMedium(ctx context.Context) (entity.Snapshot, error) {
	in1, err := p.dispatcher.ReadInput(ctx, 57, 2)
	if err != nil {
		return entity.Snapshot{}, fmt.Errorf("poll_medium read i57..i58: %w", err)
	}
	if len(in1) != 2 {
		return entity.Snapshot{}, fmt.Errorf("poll_medium read i57..i58: got %d regs: %w", len(in1), ErrUnexpectedReadLength)
	}

	in2, err := p.dispatcher.ReadInput(ctx, 69, 2)
	if err != nil {
		return entity.Snapshot{}, fmt.Errorf("poll_medium read i69..i70: %w", err)
	}
	if len(in2) != 2 {
		return entity.Snapshot{}, fmt.Errorf("poll_medium read i69..i70: got %d regs: %w", len(in2), ErrUnexpectedReadLength)
	}

	hold, err := p.dispatcher.ReadHolding(ctx, 86, 1)
	if err != nil {
		return entity.Snapshot{}, fmt.Errorf("poll_medium read h86: %w", err)
	}
	if len(hold) != 1 {
		return entity.Snapshot{}, fmt.Errorf("poll_medium read h86: got %d regs: %w", len(hold), ErrUnexpectedReadLength)
	}

	snap := entity.Snapshot{
		RoomTemp:     signedTempFromRaw(in1[0]),
		RoomHumidity: in1[1],
		CurrentMode:  valueobject.ModeFromRegister(hold[0]),
		PolledAt:     p.clock.Now(),
	}
	snap.RawErrors[2] = in2[0]
	snap.RawErrors[3] = in2[1]
	return snap, nil
}

// PollSlow reads the slow-tier register set (i0, i79, h0, h22..h25, h35..h36,
// h344, h346) and returns a Snapshot populated with the slow-tier fields. i0,
// i79 and h0 are mandatory. The panel settings and the filter registers are
// best-effort and only populated (with SettingsPolledAt / FilterPolledAt set)
// when all their reads succeed.
func (p *PollController) PollSlow(ctx context.Context) (entity.Snapshot, error) {
	in1, err := p.dispatcher.ReadInput(ctx, 0, 1)
	if err != nil {
		return entity.Snapshot{}, fmt.Errorf("poll_slow read i0: %w", err)
	}
	if len(in1) != 1 {
		return entity.Snapshot{}, fmt.Errorf("poll_slow read i0: got %d regs: %w", len(in1), ErrUnexpectedReadLength)
	}

	in2, err := p.dispatcher.ReadInput(ctx, 79, 1)
	if err != nil {
		return entity.Snapshot{}, fmt.Errorf("poll_slow read i79: %w", err)
	}
	if len(in2) != 1 {
		return entity.Snapshot{}, fmt.Errorf("poll_slow read i79: got %d regs: %w", len(in2), ErrUnexpectedReadLength)
	}

	hold, err := p.dispatcher.ReadHolding(ctx, 0, 1)
	if err != nil {
		return entity.Snapshot{}, fmt.Errorf("poll_slow read h0: %w", err)
	}
	if len(hold) != 1 {
		return entity.Snapshot{}, fmt.Errorf("poll_slow read h0: got %d regs: %w", len(hold), ErrUnexpectedReadLength)
	}

	snap := entity.Snapshot{
		Firmware:     valueobject.FirmwareFromRaw(in1[0]),
		DeviceID:     in2[0],
		DeviceConfig: entity.DeviceConfigFromRegister(hold[0]),
		PolledAt:     p.clock.Now(),
	}

	// Settings go before the filter pair, whose branch returns early on failure.
	if settings, ok := p.readSettings(ctx); ok {
		snap.Settings = settings
		snap.SettingsPolledAt = p.clock.Now()
	}

	// h344 and h346 are read separately (h345 is undocumented) and
	// best-effort: controller variants without them must not lose the
	// firmware/device_id reads above.
	resource, ok := p.readFilterRegister(ctx, 344)
	if !ok {
		return snap, nil
	}
	daysLeft, ok := p.readFilterRegister(ctx, 346)
	if !ok {
		return snap, nil
	}

	snap.FilterResource = resource
	snap.FilterDaysLeft = int16(daysLeft) //nolint:gosec // same-size reinterpret; overdue filter reads negative days
	snap.FilterPolledAt = p.clock.Now()
	return snap, nil
}

// readSettings reads the panel-menu settings as two spans over documented
// registers only (h26..h34 are skipped). A failed or short read is logged at
// Warn and reported as ok=false; the second span is not read if the first fails.
func (p *PollController) readSettings(ctx context.Context) (entity.PanelSettings, bool) {
	keys, ok := p.readSettingsSpan(ctx, catalog.AddrUserKeys0.Uint16(), 4, "h22..h25")
	if !ok {
		return entity.PanelSettings{}, false
	}
	targets, ok := p.readSettingsSpan(ctx, catalog.AddrHumidityTarget.Uint16(), 2, "h35..h36")
	if !ok {
		return entity.PanelSettings{}, false
	}
	return entity.PanelSettings{
		UserKeys0:      keys[0],
		DarkBrightness: keys[1],
		IRRemoteTime:   keys[2],
		UserDev0:       keys[3],
		HumidityTarget: targets[0],
		CO2Target:      targets[1],
	}, true
}

func (p *PollController) readSettingsSpan(ctx context.Context, start, count uint16, label string) ([]uint16, bool) {
	regs, err := p.dispatcher.ReadHolding(ctx, start, count)
	if err == nil && len(regs) != int(count) {
		err = fmt.Errorf("got %d regs: %w", len(regs), ErrUnexpectedReadLength)
	}
	if err != nil {
		p.logger.Warn("poll_slow settings read failed", "register", label, "error", err)
		return nil, false
	}
	return regs, true
}

// readFilterRegister reads one best-effort filter holding register. A failed
// or short read is logged at Warn and reported as ok=false.
func (p *PollController) readFilterRegister(ctx context.Context, addr uint16) (uint16, bool) {
	regs, err := p.dispatcher.ReadHolding(ctx, addr, 1)
	if err != nil {
		p.logger.Warn("poll_slow filter register read failed", "register", fmt.Sprintf("h%d", addr), "error", err)
		return 0, false
	}
	if len(regs) != 1 {
		p.logger.Warn("poll_slow filter register read failed", "register", fmt.Sprintf("h%d", addr),
			"error", fmt.Errorf("got %d regs: %w", len(regs), ErrUnexpectedReadLength))
		return 0, false
	}
	return regs[0], true
}

// signedTempFromRaw decodes a signed int16 ×10 °C sensor reading. Out-of-
// range values (NaN/Inf or outside the validated [5,30] °C range used by
// valueobject.NewTemperature) collapse to a zero Temperature instead of
// surfacing as an error: sensors briefly produce stale or unplugged values
// during boot or fault states, and the poll path must remain total.
func signedTempFromRaw(raw uint16) valueobject.Temperature {
	//nolint:gosec // same-size reinterpret of register value as signed int16, controller documented to return int16-encoded temperatures
	c := float64(int16(raw)) / 10.0
	if math.IsNaN(c) || math.IsInf(c, 0) {
		return valueobject.Temperature{}
	}
	t, err := valueobject.NewTemperature(c)
	if err != nil {
		return valueobject.Temperature{}
	}
	return t
}
