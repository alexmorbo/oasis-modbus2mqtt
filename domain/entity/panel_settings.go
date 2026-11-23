package entity

import "github.com/alexmorbo/oasis-modbus2mqtt/domain/catalog"

// PanelSettings holds the raw panel-menu holding registers read in the slow tier.
type PanelSettings struct {
	UserKeys0      uint16 // h22
	DarkBrightness uint16 // h23
	IRRemoteTime   uint16 // h24
	UserDev0       uint16 // h25
	HumidityTarget uint16 // h35
	CO2Target      uint16 // h36
}

// Register returns the raw value of the settings register at addr. ok is
// false for an address that is not a settings register.
func (s PanelSettings) Register(addr uint16) (uint16, bool) {
	p := s.field(addr)
	if p == nil {
		return 0, false
	}
	return *p, true
}

// WithRegister returns a copy of s with the register at addr set to value. ok
// is false (and s is returned unchanged) for an unknown address.
func (s PanelSettings) WithRegister(addr, value uint16) (PanelSettings, bool) {
	p := s.field(addr)
	if p == nil {
		return s, false
	}
	*p = value
	return s, true
}

// field returns a pointer to the register's slot in s; Register and
// WithRegister call it on their value-receiver copy.
func (s *PanelSettings) field(addr uint16) *uint16 {
	switch addr {
	case catalog.AddrUserKeys0.Uint16():
		return &s.UserKeys0
	case catalog.AddrDarkBrightness.Uint16():
		return &s.DarkBrightness
	case catalog.AddrIRRemoteTime.Uint16():
		return &s.IRRemoteTime
	case catalog.AddrUserDev0.Uint16():
		return &s.UserDev0
	case catalog.AddrHumidityTarget.Uint16():
		return &s.HumidityTarget
	case catalog.AddrCO2Target.Uint16():
		return &s.CO2Target
	default:
		return nil
	}
}
