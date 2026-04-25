package handler_test

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/metrics"
	"github.com/alexmorbo/oasis-modbus2mqtt/interface/http/handler"
)

// NOTE: no t.Parallel() — vm.WritePrometheus walks the global registry that
// is shared across the entire test binary, so concurrent runs would race.

func TestNewMetrics_NotNil(t *testing.T) {
	gin.SetMode(gin.TestMode)
	require.NotNil(t, handler.NewMetrics())
}

func TestPrometheus_Returns200WithText(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// Touch the metrics package so its package-level counters are registered,
	// then bump a known counter to guarantee non-empty output.
	metrics.ModbusReconnectsTotal.Inc()

	h := handler.NewMetrics()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/metrics", nil)

	h.Prometheus(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Header().Get("Content-Type"), "text/plain")
	assert.Contains(t, w.Body.String(), "oasis_modbus_reconnects_total")
}
