package port

import "context"

// ModbusClient is the application's view of a Modbus TCP client.
//
// It exposes only the operations use cases need: bulk reads, single-register
// writes, and an atomic read-modify-write helper for packed registers
// (e.g. Dev_Keys_2). Connection lifecycle (Connect/Disconnect/reconnect) is
// an infrastructure concern and intentionally absent from this interface.
type ModbusClient interface {
	// ReadInput reads count input registers (function code 0x04) starting
	// at start. The returned slice has exactly count elements on success.
	ReadInput(ctx context.Context, start uint16, count uint16) ([]uint16, error)

	// ReadHolding reads count holding registers (function code 0x03)
	// starting at start. The returned slice has exactly count elements on
	// success.
	ReadHolding(ctx context.Context, start uint16, count uint16) ([]uint16, error)

	// WriteHolding writes value to a single holding register at addr
	// (function code 0x06).
	WriteHolding(ctx context.Context, addr uint16, value uint16) error

	// ModifyHolding atomically applies a read-modify-write to the holding
	// register at addr: it reads the current value, calls modify, then
	// writes the result.
	//
	// Implementations MUST hold a lock across read+modify+write to
	// guarantee atomicity from this client's perspective. The Modbus
	// protocol itself offers no atomicity primitive, so concurrent writers
	// outside this process can still race; the contract only covers
	// callers that share this ModbusClient instance.
	ModifyHolding(ctx context.Context, addr uint16, modify func(current uint16) uint16) error

	// Connected reports whether the underlying transport is currently
	// usable. It must be non-blocking and safe to call concurrently —
	// ConnectionSupervisor (story 007) polls it on its own cadence.
	Connected() bool
}
