// Package port defines the interfaces (ports) that application use cases
// depend on. Infrastructure adapters in infrastructure/* implement these.
//
// The package intentionally has no dependency on domain/* — ports are pure
// abstractions over outside concerns (time, Modbus, MQTT) and exchange only
// stdlib types so adapters stay decoupled from domain models.
package port

import "time"

// Clock is an injection point for the current wall-clock time.
//
// Production code uses RealClock; tests inject a fake clock to exercise
// time-dependent behavior (e.g. AvailabilityManager staleness windows in
// story 008) without sleeping.
type Clock interface {
	// Now returns the current time. Implementations must be safe for
	// concurrent use.
	Now() time.Time
}

// RealClock is the production Clock implementation backed by time.Now.
// The zero value is ready for use; the type carries no state.
type RealClock struct{}

// Now returns time.Now().
func (RealClock) Now() time.Time {
	return time.Now()
}

// Compile-time assertion that RealClock satisfies Clock.
var _ Clock = RealClock{}
