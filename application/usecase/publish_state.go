package usecase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/catalog"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"
)

// Topics produces MQTT topic strings for state messages keyed by HA object_id.
// The canonical implementation lives in infrastructure/mqtt; this interface
// keeps the use case independent of the topic-builder implementation.
type Topics interface {
	// State returns the state topic for the supplied HA object_id (e.g.
	// "supply_temperature" → "<prefix>/state/supply_temperature").
	State(objectID string) string
}

// stateMsg is one (topic, payload) pair produced by buildMessages, plus the
// HA object_id for log correlation. Internal to the use case.
type stateMsg struct {
	topic    string
	payload  []byte
	objectID string
}

// unknownSetting is a settings register whose value has no HA state.
type unknownSetting struct {
	objectID string
	raw      uint16
}

// PublishState transforms a domain Snapshot into per-entity MQTT state messages
// and publishes every message retained on every Apply. Idempotent publishes
// are cheap and let any reconnecting subscriber recover the current value
// from the broker instead of waiting for the next change. A single Apply call
// emits up to 35 messages: 19 always, plus the filter pair and the 14 panel
// settings once each best-effort read has succeeded. Per-topic publish errors
// are aggregated via errors.Join.
type PublishState struct {
	publisher Publisher
	topics    Topics
	logger    *slog.Logger

	mu            sync.Mutex
	unknownWarned map[string]uint16
}

// NewPublishState constructs a PublishState. A nil publisher or nil topics
// builder is a programming error and panics. A nil logger falls back to
// slog.Default.
func NewPublishState(pub Publisher, topics Topics, logger *slog.Logger) *PublishState {
	if pub == nil {
		panic("publish state: publisher must not be nil")
	}
	if topics == nil {
		panic("publish state: topics must not be nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &PublishState{
		publisher:     pub,
		topics:        topics,
		logger:        logger,
		unknownWarned: make(map[string]uint16),
	}
}

// Apply publishes every state message for snap with retained=true. Up to 35
// messages are sent on every call — there is no payload-level dedup. The
// filter_days_left and filter_resource messages are omitted while
// snap.FilterPolledAt is zero, and the panel settings while
// snap.SettingsPolledAt is zero, so HA keeps them "unknown" instead of
// recording a bogus 0 before the slow tier has read them. A setting whose
// value has no HA state publishes "None" and warns once per distinct value. The
// retained flag means a late or reconnecting subscriber (HA restart, MQTT
// integration reload, broker blip) recovers the current value from the
// broker instead of staying at "unknown" until the underlying register
// changes. Per-topic publish errors are aggregated via errors.Join; a single
// failed publish does not skip the remaining topics.
func (p *PublishState) Apply(ctx context.Context, snap entity.Snapshot) error {
	msgs, unknown := p.buildMessages(snap)
	if !snap.SettingsPolledAt.IsZero() {
		p.warnUnknownSettings(unknown)
	}

	var (
		errs      []error
		published int
	)
	for _, m := range msgs {
		if err := p.publisher.Publish(ctx, m.topic, m.payload, true); err != nil {
			errs = append(errs, fmt.Errorf("publish state %s: %w", m.objectID, err))
			continue
		}
		published++
	}

	if len(errs) > 0 {
		p.logger.Warn("state publish completed with errors",
			"published", published,
			"errors", len(errs),
		)
		return errors.Join(errs...)
	}
	p.logger.Info("state published", "count", published)
	return nil
}

// warnUnknownSettings logs each unknown setting once per distinct raw value
// and forgets settings that have become known again.
func (p *PublishState) warnUnknownSettings(unknown []unknownSetting) {
	p.mu.Lock()
	defer p.mu.Unlock()
	current := make(map[string]struct{}, len(unknown))
	for _, u := range unknown {
		current[u.objectID] = struct{}{}
		if prev, warned := p.unknownWarned[u.objectID]; warned && prev == u.raw {
			continue
		}
		p.unknownWarned[u.objectID] = u.raw
		p.logger.Warn("setting value has no label", "object_id", u.objectID, "raw", u.raw)
	}
	for id := range p.unknownWarned {
		if _, still := current[id]; !still {
			delete(p.unknownWarned, id)
		}
	}
}

// buildMessages returns the ordered list of state messages for snap (19, plus
// 2 for the filter pair and 14 for the panel settings once each is polled)
// and the settings whose value has no HA state. Pure function: no I/O, no
// goroutines, deterministic ordering for stable tests.
func (p *PublishState) buildMessages(snap entity.Snapshot) ([]stateMsg, []unknownSetting) {
	msgs := []stateMsg{
		{
			topic:    p.topics.State("supply_temperature"),
			payload:  []byte(formatTemperature(snap.SupplyTemp)),
			objectID: "supply_temperature",
		},
		{
			topic:    p.topics.State("filter_clog"),
			payload:  []byte(strconv.FormatInt(int64(snap.FilterPct), 10)),
			objectID: "filter_clog",
		},
		{
			topic:    p.topics.State("heat_demand"),
			payload:  []byte(strconv.FormatUint(uint64(snap.PIDDemand), 10)),
			objectID: "heat_demand",
		},
		{
			topic:    p.topics.State("supply_fan_speed"),
			payload:  []byte(strconv.FormatUint(uint64(snap.FanState1), 10)),
			objectID: "supply_fan_speed",
		},
		{
			topic:    p.topics.State("exhaust_fan_speed"),
			payload:  []byte(strconv.FormatUint(uint64(snap.FanState2), 10)),
			objectID: "exhaust_fan_speed",
		},
		{
			topic:    p.topics.State("room_temperature"),
			payload:  []byte(formatTemperature(snap.RoomTemp)),
			objectID: "room_temperature",
		},
		{
			topic:    p.topics.State("room_humidity"),
			payload:  []byte(strconv.FormatUint(uint64(snap.RoomHumidity), 10)),
			objectID: "room_humidity",
		},
		{
			topic:    p.topics.State("firmware"),
			payload:  []byte(snap.Firmware.String()),
			objectID: "firmware",
		},
		{
			topic:    p.topics.State("device_id"),
			payload:  fmt.Appendf(nil, "0x%04X", snap.DeviceID),
			objectID: "device_id",
		},
	}
	if !snap.FilterPolledAt.IsZero() {
		msgs = append(msgs,
			stateMsg{
				topic:    p.topics.State("filter_days_left"),
				payload:  []byte(strconv.FormatInt(int64(snap.FilterDaysLeft), 10)),
				objectID: "filter_days_left",
			},
			stateMsg{
				topic:    p.topics.State("filter_resource"),
				payload:  []byte(strconv.FormatUint(uint64(snap.FilterResource), 10)),
				objectID: "filter_resource",
			},
		)
	}
	var unknown []unknownSetting
	if !snap.SettingsPolledAt.IsZero() {
		for _, def := range catalog.Settings {
			raw, _ := snap.Settings.Register(def.Addr.Uint16())
			payload, known := def.StatePayload(raw)
			if !known {
				unknown = append(unknown, unknownSetting{objectID: def.ObjectID, raw: def.Field(raw)})
			}
			msgs = append(msgs, stateMsg{
				topic:    p.topics.State(def.ObjectID),
				payload:  []byte(payload),
				objectID: def.ObjectID,
			})
		}
	}
	return append(msgs, []stateMsg{
		{
			topic:    p.topics.State("operation_time_left"),
			payload:  []byte(strconv.Itoa(int(snap.OperationTimeLeft.Seconds()))),
			objectID: "operation_time_left",
		},
		{
			topic:    p.topics.State("current_operation"),
			payload:  []byte(snap.Operation.String()),
			objectID: "current_operation",
		},
		{
			topic:    p.topics.State("power"),
			payload:  []byte(boolToOnOff(snap.PowerOn)),
			objectID: "power",
		},
		{
			topic:    p.topics.State("heater_active"),
			payload:  []byte(boolToOnOff(snap.HeaterActive())),
			objectID: "heater_active",
		},
		{
			topic:    p.topics.State("damper_open"),
			payload:  []byte(boolToOpenClosed(snap.DamperOpen)),
			objectID: "damper_open",
		},
		{
			topic:    p.topics.State("problem"),
			payload:  []byte(problemValue(snap)),
			objectID: "problem",
		},
		{
			topic:    p.topics.State("hvac_mode"),
			payload:  []byte(hvacMode(snap)),
			objectID: "hvac_mode",
		},
		{
			topic:    p.topics.State("target_temperature"),
			payload:  []byte(formatTemperature(snap.TargetTemp)),
			objectID: "target_temperature",
		},
		{
			topic:    p.topics.State("fan_target"),
			payload:  []byte(strconv.FormatUint(uint64(snap.FanTarget1), 10)),
			objectID: "fan_target",
		},
		{
			topic:    p.topics.State("hvac_action"),
			payload:  []byte(hvacAction(snap)),
			objectID: "hvac_action",
		},
	}...), unknown
}

// formatTemperature renders t with exactly one decimal place. A zero-value
// Temperature yields "0.0"; HA accepts that as a valid sensor reading and the
// bridge publishes whatever the controller reports.
func formatTemperature(t valueobject.Temperature) string {
	return fmt.Sprintf("%.1f", t.Celsius())
}

// boolToOnOff renders b as the canonical HA payload pair "ON"/"OFF" used by
// switches and most binary sensors.
func boolToOnOff(b bool) string {
	if b {
		return "ON"
	}
	return "OFF"
}

// boolToOpenClosed renders b as "OPEN"/"CLOSED" — the payload pair used by HA
// binary sensors with device_class=opening for the air damper.
func boolToOpenClosed(b bool) string {
	if b {
		return "OPEN"
	}
	return "CLOSED"
}

// hvacMode collapses the controller's mode + power state into HA's hvac_mode
// vocabulary. The Oasis Syberia ventilation hardware is heat-only; cool/auto
// modes report as fan_only because no cooling element exists.
func hvacMode(snap entity.Snapshot) string {
	if !snap.PowerOn {
		return "off"
	}
	if snap.CurrentMode == valueobject.ModeHeat {
		return "heat"
	}
	return "fan_only"
}

// hvacAction maps the snapshot to HA's hvac_action vocabulary. Priority order
// (top wins): power off → preheat transition → fan transitions → idle
// transitions → heat demand → fan running → idle. Heat demand is
// Snapshot.HeaterActive, not the ~1 Hz heater PWM, so "heating" is stable.
func hvacAction(snap entity.Snapshot) string {
	if !snap.PowerOn {
		return "off"
	}
	switch snap.Operation {
	case entity.OpPreheatCalorifier:
		return "preheating"
	case entity.OpStartFan, entity.OpFanCoastdown, entity.OpRotorSpinup:
		return "fan"
	case entity.OpCloseDamper, entity.OpElectricCalorifierPurge,
		entity.OpOpenDamper, entity.OpNorthStart,
		entity.OpOpenHotWaterValve, entity.OpCloseHotWaterValve,
		entity.OpOpenColdValve, entity.OpCloseColdValve:
		return "idle"
	}
	if snap.HeaterActive() {
		return "heating"
	}
	if snap.FanState1 > 0 {
		return "fan"
	}
	return "idle"
}

// problemValue is "ON" when the snapshot decodes any active error flag,
// otherwise "OFF". HA renders it via a binary_sensor with device_class=problem.
func problemValue(snap entity.Snapshot) string {
	if snap.DecodeErrorSet().IsEmpty() {
		return "OFF"
	}
	return "ON"
}
