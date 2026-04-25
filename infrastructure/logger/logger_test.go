package logger_test

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/config"
	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/logger"
)

func TestParseLevel(t *testing.T) {
	t.Parallel()

	tests := []struct {
		input string
		want  slog.Level
	}{
		{"debug", slog.LevelDebug},
		{"DEBUG", slog.LevelDebug},
		{"info", slog.LevelInfo},
		{"INFO", slog.LevelInfo},
		{"warn", slog.LevelWarn},
		{"warning", slog.LevelWarn},
		{"error", slog.LevelError},
		{"ERROR", slog.LevelError},
		{"", slog.LevelInfo},
		{"unknown", slog.LevelInfo},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, logger.ParseLevel(tc.input))
		})
	}
}

func TestNew_DefaultsToStdout(t *testing.T) {
	t.Parallel()
	lg := logger.New(config.LoggerConfig{Level: "info"})
	require.NotNil(t, lg)
}

func TestNewWithWriter_Levels(t *testing.T) {
	t.Parallel()

	cases := []struct {
		level    string
		wantMsgs map[string]bool // msg → expected presence in output
	}{
		{
			level: "debug",
			wantMsgs: map[string]bool{
				"dbg-msg":  true,
				"info-msg": true,
				"warn-msg": true,
				"err-msg":  true,
			},
		},
		{
			level: "info",
			wantMsgs: map[string]bool{
				"dbg-msg":  false,
				"info-msg": true,
				"warn-msg": true,
				"err-msg":  true,
			},
		},
		{
			level: "warn",
			wantMsgs: map[string]bool{
				"dbg-msg":  false,
				"info-msg": false,
				"warn-msg": true,
				"err-msg":  true,
			},
		},
		{
			level: "error",
			wantMsgs: map[string]bool{
				"dbg-msg":  false,
				"info-msg": false,
				"warn-msg": false,
				"err-msg":  true,
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.level, func(t *testing.T) {
			t.Parallel()
			var buf bytes.Buffer
			lg := logger.NewWithWriter(config.LoggerConfig{Level: tc.level}, &buf)

			lg.Debug("dbg-msg")
			lg.Info("info-msg")
			lg.Warn("warn-msg")
			lg.Error("err-msg")

			out := buf.String()
			for msg, want := range tc.wantMsgs {
				if want {
					assert.Contains(t, out, msg, "level=%s expected msg=%s in output", tc.level, msg)
				} else {
					assert.NotContains(t, out, msg, "level=%s expected msg=%s NOT in output", tc.level, msg)
				}
			}
		})
	}
}

func TestNewWithWriter_JSONFormat(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	lg := logger.NewWithWriter(config.LoggerConfig{Level: "info"}, &buf)

	lg.Info("hello", "key", "value")

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 1)

	var entry map[string]any
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &entry))
	assert.Equal(t, "hello", entry["msg"])
	assert.Equal(t, "value", entry["key"])
	assert.Equal(t, "INFO", entry["level"])
}
