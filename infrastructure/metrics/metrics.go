// Package metrics exposes VictoriaMetrics counters, histograms, and gauges for the service.
package metrics

import (
	"fmt"
	"sync/atomic"
	"time"

	vm "github.com/VictoriaMetrics/metrics"
)

// Static counters registered eagerly at package load.
var (
	// ModbusReconnectsTotal counts Modbus client reconnect events.
	ModbusReconnectsTotal = vm.NewCounter("oasis_modbus_reconnects_total")
	// MQTTReconnectsTotal counts MQTT client reconnect events.
	MQTTReconnectsTotal = vm.NewCounter("oasis_mqtt_reconnects_total")
	// JobsDroppedTotal counts jobs dropped across all tiers.
	JobsDroppedTotal = vm.NewCounter("oasis_jobs_dropped_total")
	// MQTTPublishesTotal counts MQTT publishes across all topics.
	MQTTPublishesTotal = vm.NewCounter("oasis_mqtt_publishes_total")
	// DiscoveryRepublishesTotal counts HA discovery config republish events.
	DiscoveryRepublishesTotal = vm.NewCounter("oasis_discovery_republishes_total")
)

// Atomic state backing gauge callbacks.
var (
	modbusConnectedFlag    atomic.Bool
	mqttConnectedFlag      atomic.Bool
	lastSuccessfulPollNano atomic.Int64
)

// Init registers gauge callbacks. Safe to call multiple times (idempotent).
// Must be called once after config load and before metrics endpoint starts.
func Init() {
	vm.GetOrCreateGauge("oasis_modbus_connected", func() float64 {
		if modbusConnectedFlag.Load() {
			return 1
		}
		return 0
	})
	vm.GetOrCreateGauge("oasis_mqtt_connected", func() float64 {
		if mqttConnectedFlag.Load() {
			return 1
		}
		return 0
	})
	vm.GetOrCreateGauge("oasis_last_successful_poll_unix_nanos", func() float64 {
		return float64(lastSuccessfulPollNano.Load())
	})
}

// SetModbusConnected updates the modbus connection gauge state.
func SetModbusConnected(connected bool) {
	modbusConnectedFlag.Store(connected)
}

// SetMQTTConnected updates the MQTT connection gauge state.
func SetMQTTConnected(connected bool) {
	mqttConnectedFlag.Store(connected)
}

// SetLastSuccessfulPoll records the timestamp of the latest successful poll.
func SetLastSuccessfulPoll(t time.Time) {
	lastSuccessfulPollNano.Store(t.UnixNano())
}

// ModbusOpsTotal returns the labeled counter for Modbus operations.
func ModbusOpsTotal(op, status string) *vm.Counter {
	return vm.GetOrCreateCounter(
		fmt.Sprintf(`oasis_modbus_ops_total{op=%q,status=%q}`, op, status),
	)
}

// ModbusOpDurationSeconds returns the labeled histogram for Modbus op latency.
func ModbusOpDurationSeconds(op string) *vm.Histogram {
	return vm.GetOrCreateHistogram(
		fmt.Sprintf(`oasis_modbus_op_duration_seconds{op=%q}`, op),
	)
}

// ModbusErrorsTotal returns the labeled counter for Modbus errors by type.
func ModbusErrorsTotal(errorType string) *vm.Counter {
	return vm.GetOrCreateCounter(
		fmt.Sprintf(`oasis_modbus_errors_total{error_type=%q}`, errorType),
	)
}

// JobsByTierDroppedTotal returns the labeled counter for dropped jobs per tier.
func JobsByTierDroppedTotal(tier string) *vm.Counter {
	return vm.GetOrCreateCounter(
		fmt.Sprintf(`oasis_jobs_by_tier_dropped_total{tier=%q}`, tier),
	)
}

// PollDurationSeconds returns the labeled histogram for poll duration per tier.
func PollDurationSeconds(tier string) *vm.Histogram {
	return vm.GetOrCreateHistogram(
		fmt.Sprintf(`oasis_poll_duration_seconds{tier=%q}`, tier),
	)
}

// MQTTPublishesByEntityTotal returns the labeled counter for publishes per entity.
func MQTTPublishesByEntityTotal(entity string) *vm.Counter {
	return vm.GetOrCreateCounter(
		fmt.Sprintf(`oasis_mqtt_publishes_by_entity_total{entity=%q}`, entity),
	)
}
