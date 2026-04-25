package config

import (
	"fmt"
	"os"
	"strconv"
	"time"
)

// getEnvOrDefault returns env[key] if non-empty, otherwise def.
func getEnvOrDefault(key, def string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return def
}

// getEnvOrDefaultInt parses env[key] as int. Returns def if unset; error if set but invalid.
func getEnvOrDefaultInt(key string, def int) (int, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid %s=%q: %w", key, v, err)
	}
	return n, nil
}

// getEnvOrDefaultBool parses env[key] as bool. Returns def if unset; error if set but invalid.
func getEnvOrDefaultBool(key string, def bool) (bool, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	b, err := strconv.ParseBool(v)
	if err != nil {
		return false, fmt.Errorf("invalid %s=%q: %w", key, v, err)
	}
	return b, nil
}

// getEnvOrDefaultDuration parses env[key] as time.Duration. Returns def if unset; error if invalid.
func getEnvOrDefaultDuration(key string, def time.Duration) (time.Duration, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	d, err := time.ParseDuration(v)
	if err != nil {
		return 0, fmt.Errorf("invalid %s=%q: %w", key, v, err)
	}
	return d, nil
}

// getEnvOrDefaultByte parses env[key] as int and narrows to byte. Bounds check [0, 255].
func getEnvOrDefaultByte(key string, def byte) (byte, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("invalid %s=%q: %w", key, v, err)
	}
	if n < 0 || n > 255 {
		return 0, fmt.Errorf("invalid %s=%d: out of byte range [0,255]", key, n)
	}
	//nolint:gosec // bounded by check above
	return byte(n), nil
}

// getEnvOrDefaultFloat parses env[key] as float64. Returns def if unset; error if invalid.
func getEnvOrDefaultFloat(key string, def float64) (float64, error) {
	v := os.Getenv(key)
	if v == "" {
		return def, nil
	}
	f, err := strconv.ParseFloat(v, 64)
	if err != nil {
		return 0, fmt.Errorf("invalid %s=%q: %w", key, v, err)
	}
	return f, nil
}
