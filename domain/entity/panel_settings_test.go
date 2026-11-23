package entity_test

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
)

func TestPanelSettings_RegisterRoundTrip(t *testing.T) {
	t.Parallel()

	var s entity.PanelSettings
	addrs := []uint16{22, 23, 24, 25, 35, 36}
	for i, addr := range addrs {
		next, ok := s.WithRegister(addr, uint16(100+i))
		assert.Truef(t, ok, "h%d", addr)
		s = next
	}
	want := entity.PanelSettings{
		UserKeys0: 100, DarkBrightness: 101, IRRemoteTime: 102,
		UserDev0: 103, HumidityTarget: 104, CO2Target: 105,
	}
	assert.Equal(t, want, s)
	for i, addr := range addrs {
		got, ok := s.Register(addr)
		assert.Truef(t, ok, "h%d", addr)
		assert.Equal(t, uint16(100+i), got)
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
