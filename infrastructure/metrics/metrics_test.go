package metrics_test

import (
	"bytes"
	"strconv"
	"strings"
	"testing"
	"time"

	vm "github.com/VictoriaMetrics/metrics"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/metrics"
)

func dump(t *testing.T) string {
	t.Helper()
	var buf bytes.Buffer
	vm.WritePrometheus(&buf, false)
	return buf.String()
}

func TestInit_Idempotent(t *testing.T) {
	metrics.Init()
	metrics.Init()
	out := dump(t)
	assert.Contains(t, out, "oasis_modbus_connected")
	assert.Contains(t, out, "oasis_mqtt_connected")
	assert.Contains(t, out, "oasis_last_successful_poll_unix_nanos")
}

func TestStaticCounters_Increment(t *testing.T) {
	metrics.Init()
	before := metrics.ModbusReconnectsTotal.Get()
	metrics.ModbusReconnectsTotal.Inc()
	metrics.ModbusReconnectsTotal.Inc()
	assert.Equal(t, before+2, metrics.ModbusReconnectsTotal.Get())

	out := dump(t)
	assert.Contains(t, out, "oasis_modbus_reconnects_total")
}

func TestStaticCounters_AllNamesPresent(t *testing.T) {
	metrics.Init()
	metrics.MQTTReconnectsTotal.Inc()
	metrics.JobsDroppedTotal.Inc()
	metrics.MQTTPublishesTotal.Inc()
	metrics.DiscoveryRepublishesTotal.Inc()

	out := dump(t)
	for _, name := range []string{
		"oasis_mqtt_reconnects_total",
		"oasis_jobs_dropped_total",
		"oasis_mqtt_publishes_total",
		"oasis_discovery_republishes_total",
	} {
		assert.Contains(t, out, name, "missing %s in metrics dump", name)
	}
}

func TestModbusOpsTotal_Labeled(t *testing.T) {
	metrics.Init()
	c := metrics.ModbusOpsTotal("read_input", "ok")
	require.NotNil(t, c)
	c.Inc()

	c2 := metrics.ModbusOpsTotal("read_input", "ok")
	c2.Inc()

	out := dump(t)
	assert.Contains(t, out, `oasis_modbus_ops_total{op="read_input",status="ok"}`)
}

func TestModbusOpDurationSeconds_Labeled(t *testing.T) {
	metrics.Init()
	h := metrics.ModbusOpDurationSeconds("read_holding")
	require.NotNil(t, h)
	h.Update(0.5)

	out := dump(t)
	assert.Contains(t, out, "oasis_modbus_op_duration_seconds")
	assert.Contains(t, out, `op="read_holding"`)
}

func TestModbusErrorsTotal_Labeled(t *testing.T) {
	metrics.Init()
	metrics.ModbusErrorsTotal("timeout").Inc()
	out := dump(t)
	assert.Contains(t, out, `oasis_modbus_errors_total{error_type="timeout"}`)
}

func TestJobsByTierDroppedTotal_Labeled(t *testing.T) {
	metrics.Init()
	metrics.JobsByTierDroppedTotal("hot").Inc()
	out := dump(t)
	assert.Contains(t, out, `oasis_jobs_by_tier_dropped_total{tier="hot"}`)
}

func TestPollDurationSeconds_Labeled(t *testing.T) {
	metrics.Init()
	metrics.PollDurationSeconds("medium").Update(0.123)
	out := dump(t)
	assert.Contains(t, out, "oasis_poll_duration_seconds")
	assert.Contains(t, out, `tier="medium"`)
}

func TestMQTTPublishesByEntityTotal_Labeled(t *testing.T) {
	metrics.Init()
	metrics.MQTTPublishesByEntityTotal("oasis_syberia_temperature").Inc()
	out := dump(t)
	assert.Contains(t, out, `oasis_mqtt_publishes_by_entity_total{entity="oasis_syberia_temperature"}`)
}

func TestSetModbusConnected_Gauge(t *testing.T) {
	metrics.Init()

	metrics.SetModbusConnected(true)
	out := dump(t)
	assert.True(t, hasExactGaugeLine(out, "oasis_modbus_connected", 1),
		"expected oasis_modbus_connected 1 in:\n%s", out)

	metrics.SetModbusConnected(false)
	out = dump(t)
	assert.True(t, hasExactGaugeLine(out, "oasis_modbus_connected", 0),
		"expected oasis_modbus_connected 0 in:\n%s", out)
}

func TestSetMQTTConnected_Gauge(t *testing.T) {
	metrics.Init()

	metrics.SetMQTTConnected(true)
	out := dump(t)
	assert.True(t, hasExactGaugeLine(out, "oasis_mqtt_connected", 1))

	metrics.SetMQTTConnected(false)
	out = dump(t)
	assert.True(t, hasExactGaugeLine(out, "oasis_mqtt_connected", 0))
}

func TestSetLastSuccessfulPoll_Gauge(t *testing.T) {
	metrics.Init()

	known := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	metrics.SetLastSuccessfulPoll(known)

	out := dump(t)
	assert.Contains(t, out, "oasis_last_successful_poll_unix_nanos")
	expected := float64(known.UnixNano())
	require.True(t, gaugeValueEquals(out, "oasis_last_successful_poll_unix_nanos", expected),
		"expected gauge == %v, output:\n%s", expected, out)
}

// hasExactGaugeLine checks for a line exactly matching "<name> <numericValue>".
func hasExactGaugeLine(out, name string, value float64) bool {
	prefix := name + " "
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, prefix) {
			continue
		}
		got, err := strconv.ParseFloat(strings.TrimPrefix(line, prefix), 64)
		if err != nil {
			continue
		}
		if got == value {
			return true
		}
	}
	return false
}

// gaugeValueEquals checks for a "<name> <num>" line where num equals want.
func gaugeValueEquals(out, name string, want float64) bool {
	return hasExactGaugeLine(out, name, want)
}
