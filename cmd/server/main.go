package main

import (
	"context"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/config"
	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/logger"
	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/metrics"
	"github.com/alexmorbo/oasis-modbus2mqtt/internal/app"
)

func main() {
	cfg, err := config.Load()
	if err != nil {
		slog.New(slog.NewJSONHandler(os.Stderr, nil)).Error("config load failed", "error", err)
		os.Exit(1)
	}

	log := logger.New(cfg.Logger)
	slog.SetDefault(log)
	log.Info("oasis-modbus2mqtt starting", "version", "0.1.0")

	metrics.Init()

	a, err := app.New(cfg, log)
	if err != nil {
		log.Error("app build failed", "error", err)
		os.Exit(1)
	}

	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer cancel()

	if err := a.Run(ctx); err != nil {
		log.Error("app run failed", "error", err)
		os.Exit(1)
	}
	log.Info("oasis-modbus2mqtt exited cleanly")
}
