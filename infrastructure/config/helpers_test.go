package config

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGetEnvOrDefault(t *testing.T) {
	t.Run("unset returns default", func(t *testing.T) {
		t.Setenv("TEST_STRING_VAR_UNSET", "")
		assert.Equal(t, "def", getEnvOrDefault("TEST_STRING_VAR_UNSET", "def"))
	})

	t.Run("set returns value", func(t *testing.T) {
		t.Setenv("TEST_STRING_VAR_SET", "hello")
		assert.Equal(t, "hello", getEnvOrDefault("TEST_STRING_VAR_SET", "def"))
	})
}

func TestGetEnvOrDefaultInt(t *testing.T) {
	t.Run("unset returns default", func(t *testing.T) {
		t.Setenv("TEST_INT_UNSET", "")
		v, err := getEnvOrDefaultInt("TEST_INT_UNSET", 42)
		require.NoError(t, err)
		assert.Equal(t, 42, v)
	})

	t.Run("set valid", func(t *testing.T) {
		t.Setenv("TEST_INT_VALID", "123")
		v, err := getEnvOrDefaultInt("TEST_INT_VALID", 0)
		require.NoError(t, err)
		assert.Equal(t, 123, v)
	})

	t.Run("set invalid returns error", func(t *testing.T) {
		t.Setenv("TEST_INT_INVALID", "abc")
		_, err := getEnvOrDefaultInt("TEST_INT_INVALID", 0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "TEST_INT_INVALID")
	})
}

func TestGetEnvOrDefaultBool(t *testing.T) {
	t.Run("unset returns default", func(t *testing.T) {
		t.Setenv("TEST_BOOL_UNSET", "")
		v, err := getEnvOrDefaultBool("TEST_BOOL_UNSET", true)
		require.NoError(t, err)
		assert.True(t, v)
	})

	t.Run("set valid true", func(t *testing.T) {
		t.Setenv("TEST_BOOL_TRUE", "true")
		v, err := getEnvOrDefaultBool("TEST_BOOL_TRUE", false)
		require.NoError(t, err)
		assert.True(t, v)
	})

	t.Run("set invalid", func(t *testing.T) {
		t.Setenv("TEST_BOOL_INVALID", "notabool")
		_, err := getEnvOrDefaultBool("TEST_BOOL_INVALID", false)
		require.Error(t, err)
	})
}

func TestGetEnvOrDefaultDuration(t *testing.T) {
	t.Run("unset returns default", func(t *testing.T) {
		t.Setenv("TEST_DUR_UNSET", "")
		v, err := getEnvOrDefaultDuration("TEST_DUR_UNSET", 5*time.Second)
		require.NoError(t, err)
		assert.Equal(t, 5*time.Second, v)
	})

	t.Run("set valid", func(t *testing.T) {
		t.Setenv("TEST_DUR_VALID", "1m30s")
		v, err := getEnvOrDefaultDuration("TEST_DUR_VALID", 0)
		require.NoError(t, err)
		assert.Equal(t, 90*time.Second, v)
	})

	t.Run("set invalid", func(t *testing.T) {
		t.Setenv("TEST_DUR_INVALID", "abc")
		_, err := getEnvOrDefaultDuration("TEST_DUR_INVALID", 0)
		require.Error(t, err)
	})
}

func TestGetEnvOrDefaultByte(t *testing.T) {
	t.Run("unset returns default", func(t *testing.T) {
		t.Setenv("TEST_BYTE_UNSET", "")
		v, err := getEnvOrDefaultByte("TEST_BYTE_UNSET", 7)
		require.NoError(t, err)
		assert.Equal(t, byte(7), v)
	})

	t.Run("set valid", func(t *testing.T) {
		t.Setenv("TEST_BYTE_VALID", "200")
		v, err := getEnvOrDefaultByte("TEST_BYTE_VALID", 0)
		require.NoError(t, err)
		assert.Equal(t, byte(200), v)
	})

	t.Run("set invalid string", func(t *testing.T) {
		t.Setenv("TEST_BYTE_BAD", "abc")
		_, err := getEnvOrDefaultByte("TEST_BYTE_BAD", 0)
		require.Error(t, err)
	})

	t.Run("out of range high", func(t *testing.T) {
		t.Setenv("TEST_BYTE_HIGH", "256")
		_, err := getEnvOrDefaultByte("TEST_BYTE_HIGH", 0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "out of byte range")
	})

	t.Run("out of range low", func(t *testing.T) {
		t.Setenv("TEST_BYTE_LOW", "-1")
		_, err := getEnvOrDefaultByte("TEST_BYTE_LOW", 0)
		require.Error(t, err)
		assert.Contains(t, err.Error(), "out of byte range")
	})
}

func TestGetEnvOrDefaultFloat(t *testing.T) {
	t.Run("unset returns default", func(t *testing.T) {
		t.Setenv("TEST_FLOAT_UNSET", "")
		v, err := getEnvOrDefaultFloat("TEST_FLOAT_UNSET", 1.5)
		require.NoError(t, err)
		assert.InDelta(t, 1.5, v, 0.0001)
	})

	t.Run("set valid", func(t *testing.T) {
		t.Setenv("TEST_FLOAT_VALID", "3.14")
		v, err := getEnvOrDefaultFloat("TEST_FLOAT_VALID", 0)
		require.NoError(t, err)
		assert.InDelta(t, 3.14, v, 0.0001)
	})

	t.Run("set invalid", func(t *testing.T) {
		t.Setenv("TEST_FLOAT_BAD", "abc")
		_, err := getEnvOrDefaultFloat("TEST_FLOAT_BAD", 0)
		require.Error(t, err)
	})
}
