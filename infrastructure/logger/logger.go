package logger

import (
	"io"
	"log/slog"
	"os"
	"strings"

	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/config"
)

// ParseLevel converts a string level to slog.Level. Unknown values map to LevelInfo.
func ParseLevel(s string) slog.Level {
	switch strings.ToLower(s) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error":
		return slog.LevelError
	case "info":
		return slog.LevelInfo
	default:
		return slog.LevelInfo
	}
}

// New returns a JSON slog.Logger writing to os.Stdout at the level from cfg.
func New(cfg config.LoggerConfig) *slog.Logger {
	return NewWithWriter(cfg, os.Stdout)
}

// NewWithWriter returns a JSON slog.Logger writing to w at the level from cfg.
// Useful for tests that need to capture output.
func NewWithWriter(cfg config.LoggerConfig, w io.Writer) *slog.Logger {
	handler := slog.NewJSONHandler(w, &slog.HandlerOptions{
		Level: ParseLevel(cfg.Level),
	})
	return slog.New(handler)
}
