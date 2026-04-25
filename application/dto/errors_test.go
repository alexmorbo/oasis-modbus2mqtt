package dto_test

import (
	"errors"
	"fmt"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/dto"
)

func TestErrors_Distinct(t *testing.T) {
	t.Parallel()

	all := []error{
		dto.ErrModbusNotConnected,
		dto.ErrMQTTNotConnected,
		dto.ErrTransitionInProgress,
		dto.ErrCommandTimeout,
		dto.ErrInvalidPayload,
	}

	for i, a := range all {
		for j, b := range all {
			if i == j {
				continue
			}
			assert.Falsef(t, errors.Is(a, b),
				"errors.Is(%v, %v) must be false (sentinels must be distinct)", a, b)
		}
	}
}

func TestErrors_WrapPreserves(t *testing.T) {
	t.Parallel()

	all := []error{
		dto.ErrModbusNotConnected,
		dto.ErrMQTTNotConnected,
		dto.ErrTransitionInProgress,
		dto.ErrCommandTimeout,
		dto.ErrInvalidPayload,
	}

	for _, original := range all {
		original := original
		t.Run(original.Error(), func(t *testing.T) {
			t.Parallel()
			wrapped := fmt.Errorf("context: %w", original)
			assert.True(t, errors.Is(wrapped, original),
				"errors.Is(wrapped, %v) must be true after fmt.Errorf wrap", original)
		})
	}
}
