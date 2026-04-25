package port

import "context"

// ModbusConnection extends ModbusClient with the connection-lifecycle
// operations that ConnectionSupervisor (story 007) needs to (re)establish
// the underlying TCP transport. The infrastructure client (story 006)
// implements both interfaces; application code that does not perform
// lifecycle management depends on ModbusClient only.
type ModbusConnection interface {
	ModbusClient

	// Connect opens (or re-opens) the underlying transport. It must be
	// idempotent: calling Connect on an already-connected client returns
	// nil without side effects.
	Connect(ctx context.Context) error

	// ForceClose tears down the underlying transport unconditionally. It
	// must be idempotent and safe to call from any goroutine.
	ForceClose() error
}
