package usecase

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"

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

// PublishState transforms a domain Snapshot into per-entity MQTT state messages
// and publishes only the deltas relative to the last successful publish. A
// single Apply call may emit zero, one, or many publishes depending on what
// changed since the previous snapshot.
type PublishState struct {
	publisher   Publisher
	topics      Topics
	logger      *slog.Logger
	mu          sync.Mutex
	lastPayload map[string][]byte
}

// NewPublishState constructs a PublishState. A nil publisher or nil topics
// builder is a programming error and panics. A nil logger falls back to
// slog.Default. The internal delta cache is initialised empty.
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
		publisher:   pub,
		topics:      topics,
		logger:      logger,
		lastPayload: make(map[string][]byte),
	}
}

// Apply publishes every state message whose payload differs from the value
// last successfully published on the same topic. Identical payloads are
// silently skipped (DEBUG-logged). Publish errors are aggregated via
// errors.Join; failed topics are NOT inserted into the cache, so the next
// Apply will retry them. Messages are sent with retained=false because the
// AvailabilityManager (story 008) drives offline transitions out-of-band.
func (p *PublishState) Apply(ctx context.Context, snap entity.Snapshot) error {
	msgs := p.buildMessages(snap)

	p.mu.Lock()
	defer p.mu.Unlock()

	var (
		errs      []error
		published int
		skipped   int
	)
	for _, m := range msgs {
		if prev, ok := p.lastPayload[m.topic]; ok && bytes.Equal(prev, m.payload) {
			skipped++
			p.logger.Debug("state skip: payload unchanged", "object_id", m.objectID, "topic", m.topic)
			continue
		}
		if err := p.publisher.Publish(ctx, m.topic, m.payload, false); err != nil {
			errs = append(errs, fmt.Errorf("publish state %s: %w", m.objectID, err))
			continue
		}
		p.lastPayload[m.topic] = append([]byte(nil), m.payload...)
		published++
	}

	if len(errs) > 0 {
		p.logger.Warn("state publish completed with errors",
			"published", published,
			"skipped", skipped,
			"errors", len(errs),
		)
		return errors.Join(errs...)
	}
	if published > 0 {
		p.logger.Info("state published", "count", published, "skipped", skipped)
	}
	return nil
}

// buildMessages returns the full ordered list of state messages for snap. Pure
// function: no I/O, no goroutines, deterministic ordering for stable tests.
func (p *PublishState) buildMessages(snap entity.Snapshot) []stateMsg {
	return []stateMsg{
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
			payload:  []byte(fmt.Sprintf("0x%04X", snap.DeviceID)),
			objectID: "device_id",
		},
		{
			topic:    p.topics.State("operation_time_left"),
			payload:  []byte(strconv.Itoa(int(snap.OperationTimeLeft.Seconds()))),
			objectID: "operation_time_left",
		},
		{
			topic:    p.topics.State("power"),
			payload:  []byte(boolToOnOff(snap.PowerOn)),
			objectID: "power",
		},
		{
			topic:    p.topics.State("heater_active"),
			payload:  []byte(boolToOnOff(snap.HeaterPWM)),
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
	}
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
// (top wins): power off → preheat transition → fan transitions → heater PWM
// active → fan running → idle. Matches plan section 6.
func hvacAction(snap entity.Snapshot) string {
	if !snap.PowerOn {
		return "off"
	}
	switch snap.Operation {
	case entity.OpPreheatCalorifier:
		return "preheating"
	case entity.OpStartFan, entity.OpFanCoastdown:
		return "fan"
	}
	if snap.HeaterPWM {
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
