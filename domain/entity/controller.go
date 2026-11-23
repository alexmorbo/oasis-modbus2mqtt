package entity

import (
	"time"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"
)

// Snapshot is an immutable single-poll-cycle view of the controller. Fields
// are exported so the poller can populate them with struct literals; the type
// has no constructor because all invariants live in the underlying value
// objects (Mode, Temperature, FirmwareVersion).
//
// FanTarget1 is the raw Fan_Target_1 register value (h32). The poll path does
// not validate it through valueobject.FanSpeed because the controller may
// transiently return 0; the write path constructs a strict
// valueobject.FanSpeed via NewFanSpeed instead.
//
// FilterResource (h344) and FilterDaysLeft (h346) are undocumented holding
// registers in days; FilterPct (i14) is derived by the controller from them.
// FilterDaysLeft is signed so an overdue filter reads negative. Both are
// best-effort reads: FilterPolledAt is zero until they have been read
// successfully at least once, and consumers must not publish them before.
//
// Settings holds the raw panel-menu holding registers (h22..h25, h35..h36).
// They are a best-effort slow-tier read: SettingsPolledAt is zero until the
// first successful read, and consumers must not publish them before.
//
// HeaterPWM is DOutputs bits 0..1. It pulses ~1 Hz while heating (quirk §7),
// so it stays in the snapshot for diagnostics only; HeaterActive is the
// user-facing heat signal.
//
// Errors is retained for backwards compatibility but is NOT populated by the
// merger — RawErrors holds the raw register reads and DecodeErrorSet derives
// a fresh ErrorSet on demand. Callers that need current error flags should
// prefer Snapshot.DecodeErrorSet().
type Snapshot struct {
	Firmware          valueobject.FirmwareVersion
	DeviceID          uint16
	DeviceConfig      DeviceConfig
	PowerOn           bool
	Switching         bool
	HeatCapable       bool
	CoolCapable       bool
	CurrentMode       valueobject.Mode
	Operation         OperatingState
	OperationTimeLeft time.Duration
	TargetTemp        valueobject.Temperature
	RoomTemp          valueobject.Temperature
	SupplyTemp        valueobject.Temperature
	FanTarget1        uint16
	FanState1         uint16
	FanState2         uint16
	HeaterPWM         bool
	DamperOpen        bool
	PIDDemand         uint16
	FilterPct         int16
	FilterResource    uint16
	FilterDaysLeft    int16
	FilterPolledAt    time.Time
	Settings          PanelSettings
	SettingsPolledAt  time.Time
	Errors            ErrorSet
	RawErrors         [4]uint16
	RoomHumidity      uint16
	PolledAt          time.Time
}

// IsHealthy reports whether the snapshot was polled within the supplied freshness
// window (now - PolledAt <= threshold). Used by the availability manager.
func (s Snapshot) IsHealthy(now time.Time, threshold time.Duration) bool {
	return now.Sub(s.PolledAt) <= threshold
}

// HeaterActive reports PID heat demand while powered. HeaterPWM pulses ~1 Hz (quirk §7)
// and stays in the snapshot for diagnostics only; nothing user-facing may toggle on it.
func (s Snapshot) HeaterActive() bool { return s.PowerOn && s.PIDDemand > 0 }

// DecodeErrorSet returns a freshly decoded ErrorSet from RawErrors. Prefer this
// over the static Errors field, which is left untouched by the poller.
func (s Snapshot) DecodeErrorSet() ErrorSet {
	return DecodeErrors(s.RawErrors[0], s.RawErrors[1], s.RawErrors[2], s.RawErrors[3])
}

// ParseLastTime decodes the Last_Time register (i6): high byte = minutes,
// low byte = seconds remaining for the current operation.
func ParseLastTime(raw uint16) time.Duration {
	mins := (raw >> 8) & 0xFF
	secs := raw & 0xFF
	return time.Duration(mins)*time.Minute + time.Duration(secs)*time.Second
}
