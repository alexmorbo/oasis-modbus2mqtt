// Package modbus implements the infrastructure Modbus TCP client built on
// top of github.com/grid-x/modbus. It owns the single TCP connection to the
// controller, serializes all operations, enforces the guard interval, and
// classifies low-level errors into typed sentinels for the rest of the
// service.
package modbus

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"net"

	gridx "github.com/grid-x/modbus"
)

// Sentinel errors returned (wrapped) by the Modbus client. Callers compare
// using errors.Is so the concrete error chain may add op context on top.
var (
	// ErrConnectionLost indicates the TCP transport is no longer usable;
	// the client must be reconnected before further operations.
	ErrConnectionLost = errors.New("modbus connection lost")

	// ErrTimeout indicates a Modbus operation exceeded its read/write
	// deadline at the network layer.
	ErrTimeout = errors.New("modbus operation timed out")

	// ErrIllegalAddress maps to Modbus exception code 0x02.
	ErrIllegalAddress = errors.New("modbus illegal data address")

	// ErrIllegalFunction maps to Modbus exception code 0x01.
	ErrIllegalFunction = errors.New("modbus illegal function")

	// ErrIllegalDataValue maps to Modbus exception code 0x03 and is also
	// returned for client-side request validation failures (count out of
	// range, etc.).
	ErrIllegalDataValue = errors.New("modbus illegal data value")

	// ErrServerFailure maps to Modbus exception code 0x04.
	ErrServerFailure = errors.New("modbus server device failure")

	// ErrInvalidByteCount indicates the controller returned an odd number
	// of bytes for a register read, which violates the Modbus spec.
	ErrInvalidByteCount = errors.New("modbus response byte count is odd")
)

// BytesToUint16 decodes a big-endian byte slice into a slice of uint16
// register values. Length of b must be even.
func BytesToUint16(b []byte) ([]uint16, error) {
	if len(b)%2 != 0 {
		return nil, fmt.Errorf("decode: %w: got %d bytes", ErrInvalidByteCount, len(b))
	}
	out := make([]uint16, len(b)/2)
	for i := 0; i < len(out); i++ {
		out[i] = binary.BigEndian.Uint16(b[i*2 : i*2+2])
	}
	return out, nil
}

// Uint16ToBytes encodes a slice of register values into big-endian bytes.
func Uint16ToBytes(v []uint16) []byte {
	out := make([]byte, len(v)*2)
	for i, r := range v {
		binary.BigEndian.PutUint16(out[i*2:i*2+2], r)
	}
	return out
}

// WrapError annotates err with the operation name and, when possible,
// classifies it under one of the package-level sentinels so callers can use
// errors.Is for typed handling. nil in, nil out.
func WrapError(op string, err error) error {
	if err == nil {
		return nil
	}

	// Network timeout (deadline exceeded). Check before EOF because some
	// transports surface deadline errors as net.OpError.
	var netErr net.Error
	if errors.As(err, &netErr) && netErr.Timeout() {
		return fmt.Errorf("%s: %w: %w", op, ErrTimeout, err)
	}

	// Connection torn down by peer or transport.
	if errors.Is(err, io.EOF) || errors.Is(err, io.ErrUnexpectedEOF) {
		return fmt.Errorf("%s: %w: %w", op, ErrConnectionLost, err)
	}

	// net.OpError without Timeout() typically means broken pipe / reset.
	var opErr *net.OpError
	if errors.As(err, &opErr) {
		return fmt.Errorf("%s: %w: %w", op, ErrConnectionLost, err)
	}

	// Modbus protocol exception codes (server replied with an error PDU).
	var mbErr *gridx.Error
	if errors.As(err, &mbErr) {
		switch mbErr.ExceptionCode {
		case gridx.ExceptionCodeIllegalFunction:
			return fmt.Errorf("%s: %w: %w", op, ErrIllegalFunction, err)
		case gridx.ExceptionCodeIllegalDataAddress:
			return fmt.Errorf("%s: %w: %w", op, ErrIllegalAddress, err)
		case gridx.ExceptionCodeIllegalDataValue:
			return fmt.Errorf("%s: %w: %w", op, ErrIllegalDataValue, err)
		case gridx.ExceptionCodeServerDeviceFailure:
			return fmt.Errorf("%s: %w: %w", op, ErrServerFailure, err)
		default:
			return fmt.Errorf("%s: %w", op, err)
		}
	}

	// Unclassified — preserve op context only.
	return fmt.Errorf("%s: %w", op, err)
}
