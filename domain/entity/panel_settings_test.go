package entity_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
)

func TestPanelSettings_RegisterRoundTrip(t *testing.T) {
	t.Parallel()

	var s entity.PanelSettings
	regs := []struct{ addr, val uint16 }{
		{22, 100}, {23, 101}, {24, 102}, {25, 103}, {35, 104}, {36, 105},
	}
	for _, r := range regs {
		next, ok := s.WithRegister(r.addr, r.val)
		assert.Truef(t, ok, "h%d", r.addr)
		s = next
	}
	want := entity.PanelSettings{
		UserKeys0: 100, DarkBrightness: 101, IRRemoteTime: 102,
		UserDev0: 103, HumidityTarget: 104, CO2Target: 105,
	}
	assert.Equal(t, want, s)
	for _, r := range regs {
		got, ok := s.Register(r.addr)
		assert.Truef(t, ok, "h%d", r.addr)
		assert.Equal(t, r.val, got)
	}

	for _, addr := range []uint16{0, 21, 26, 34, 37, 86} {
		_, ok := s.Register(addr)
		assert.Falsef(t, ok, "h%d is not a settings register", addr)
		same, ok := s.WithRegister(addr, 1)
		assert.False(t, ok)
		assert.Equal(t, s, same)
	}
}

func TestPanelSettings_WithRegisterDoesNotMutateReceiver(t *testing.T) {
	t.Parallel()
	orig := entity.PanelSettings{UserKeys0: 0x07E4}
	next, ok := orig.WithRegister(22, 0x07A4)
	assert.True(t, ok)
	assert.Equal(t, uint16(0x07E4), orig.UserKeys0)
	assert.Equal(t, uint16(0x07A4), next.UserKeys0)
}
