package handler

import (
	"net/http"

	vm "github.com/VictoriaMetrics/metrics"
	"github.com/gin-gonic/gin"
)

// Metrics serves the Prometheus scrape endpoint backed by the global
// VictoriaMetrics registry.
type Metrics struct{}

// NewMetrics returns a stateless Metrics handler.
func NewMetrics() *Metrics {
	return &Metrics{}
}

// Prometheus writes the current registry contents in Prometheus text exposition
// format. Process metrics are intentionally excluded (second arg = false) — the
// service exposes only its application counters/gauges/histograms.
func (m *Metrics) Prometheus(c *gin.Context) {
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.Status(http.StatusOK)
	vm.WritePrometheus(c.Writer, false)
}
