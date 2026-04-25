package port_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/port"
)

func TestRealClock_Now(t *testing.T) {
	t.Parallel()

	before := time.Now()
	now := port.RealClock{}.Now()
	after := time.Now()

	assert.True(t, !now.Before(before) && !now.After(after),
		"RealClock.Now() = %v, want in [%v, %v]", now, before, after)
}
