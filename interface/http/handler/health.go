// Package handler hosts the Gin HTTP handlers for the bridge's operational
// endpoints (liveness, readiness, Prometheus metrics).
package handler

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/port"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
)

// ConnectionChecker reports whether an outgoing dependency (typically MQTT)
// is currently connected. Implemented by infrastructure/mqtt.Client.
type ConnectionChecker interface {
	Connected() bool
}

// SnapshotProvider returns the most recent controller snapshot. Implemented
// by application/service.Poller (Snapshot returns a shallow copy under lock).
type SnapshotProvider interface {
	Snapshot() entity.Snapshot
}

// Health serves Kubernetes liveness and readiness probes. Liveness is
// unconditional 200; readiness depends on MQTT connectivity and the freshness
// of the last successful Modbus poll.
type Health struct {
	mqtt               ConnectionChecker
	snapshots          SnapshotProvider
	readinessThreshold time.Duration
	clock              port.Clock
	logger             *slog.Logger
}

// NewHealth constructs a Health handler. mqtt and snapshots are required
// (panic on nil); a nil clock falls back to port.RealClock{}; a nil logger
// falls back to slog.Default(); a non-positive threshold panics.
func NewHealth(
	mqtt ConnectionChecker,
	snapshots SnapshotProvider,
	threshold time.Duration,
	clock port.Clock,
	logger *slog.Logger,
) *Health {
	if mqtt == nil {
		panic("handler: NewHealth requires non-nil ConnectionChecker")
	}
	if snapshots == nil {
		panic("handler: NewHealth requires non-nil SnapshotProvider")
	}
	if threshold <= 0 {
		panic("handler: NewHealth requires positive readiness threshold")
	}
	if clock == nil {
		clock = port.RealClock{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Health{
		mqtt:               mqtt,
		snapshots:          snapshots,
		readinessThreshold: threshold,
		clock:              clock,
		logger:             logger,
	}
}

// Live is the Kubernetes liveness probe handler. It always returns 200 OK
// with body {"status":"ok"} as long as the HTTP server can serve requests.
func (h *Health) Live(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}

// Ready is the Kubernetes readiness probe handler. It returns 200 OK only
// when MQTT is connected and the latest snapshot was polled within the
// configured threshold; otherwise it returns 503 with a machine-readable
// reason field.
func (h *Health) Ready(c *gin.Context) {
	if !h.mqtt.Connected() {
		h.logger.Debug("readiness check failed", "reason", "mqtt_not_connected")
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status": "not_ready",
			"reason": "mqtt_not_connected",
		})
		return
	}

	snap := h.snapshots.Snapshot()
	now := h.clock.Now()
	if snap.PolledAt.IsZero() || now.Sub(snap.PolledAt) > h.readinessThreshold {
		h.logger.Debug("readiness check failed",
			"reason", "stale_snapshot",
			"polled_at", snap.PolledAt,
			"threshold", h.readinessThreshold,
		)
		c.JSON(http.StatusServiceUnavailable, gin.H{
			"status": "not_ready",
			"reason": "stale_snapshot",
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{"status": "ok"})
}
