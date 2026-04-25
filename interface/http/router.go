// Package http wires the bridge's HTTP surface (router, handlers, server).
package http

import (
	"log/slog"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/alexmorbo/oasis-modbus2mqtt/interface/http/handler"
)

// NewRouter builds the Gin engine with health and metrics routes plus a slog
// access-log middleware and a panic-recovery middleware. Gin runs in
// ReleaseMode so its debug logger does not write to stdout.
func NewRouter(
	health *handler.Health,
	metricsHandler *handler.Metrics,
	logger *slog.Logger,
) *gin.Engine {
	if health == nil {
		panic("http: NewRouter requires non-nil Health handler")
	}
	if metricsHandler == nil {
		panic("http: NewRouter requires non-nil Metrics handler")
	}
	if logger == nil {
		logger = slog.Default()
	}

	gin.SetMode(gin.ReleaseMode)
	r := gin.New()
	r.Use(gin.Recovery())
	r.Use(slogMiddleware(logger))

	r.GET("/health/live", health.Live)
	r.GET("/health/ready", health.Ready)
	r.GET("/metrics", metricsHandler.Prometheus)

	return r
}

// slogMiddleware logs each completed request at INFO with method, path,
// status, and duration_ms.
func slogMiddleware(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		logger.Info("http request",
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"status", c.Writer.Status(),
			"duration_ms", time.Since(start).Milliseconds(),
		)
	}
}
