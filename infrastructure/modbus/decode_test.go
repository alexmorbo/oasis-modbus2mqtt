package modbus_test

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"

	gridx "github.com/grid-x/modbus"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/modbus"
)

func TestBytesToUint16(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		in      []byte
		want    []uint16
		wantErr error
	}{
		{name: "single", in: []byte{0x12, 0x34}, want: []uint16{0x1234}},
		{name: "two", in: []byte{0xFF, 0xFF, 0x00, 0x01}, want: []uint16{0xFFFF, 0x0001}},
		{name: "empty", in: nil, want: []uint16{}},
		{name: "odd", in: []byte{0x01}, wantErr: modbus.ErrInvalidByteCount},
		{name: "odd_three", in: []byte{0x01, 0x02, 0x03}, wantErr: modbus.ErrInvalidByteCount},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got, err := modbus.BytesToUint16(tc.in)
			if tc.wantErr != nil {
				require.Error(t, err)
				assert.ErrorIs(t, err, tc.wantErr)
				assert.Nil(t, got)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestUint16ToBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   []uint16
		want []byte
	}{
		{name: "single", in: []uint16{0x1234}, want: []byte{0x12, 0x34}},
		{name: "two", in: []uint16{0xFFFF, 0x0001}, want: []byte{0xFF, 0xFF, 0x00, 0x01}},
		{name: "empty", in: nil, want: []byte{}},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := modbus.Uint16ToBytes(tc.in)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestUint16Roundtrip(t *testing.T) {
	t.Parallel()

	in := []uint16{0x0000, 0x1234, 0xABCD, 0xFFFF, 0x0001}
	encoded := modbus.Uint16ToBytes(in)
	decoded, err := modbus.BytesToUint16(encoded)
	require.NoError(t, err)
	assert.Equal(t, in, decoded)
}

// fakeTimeoutErr satisfies net.Error with Timeout()==true.
type fakeTimeoutErr struct{}

func (fakeTimeoutErr) Error() string   { return "fake timeout" }
func (fakeTimeoutErr) Timeout() bool   { return true }
func (fakeTimeoutErr) Temporary() bool { return true }

func TestWrapError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		in           error
		wantSentinel error
		wantNil      bool
	}{
		{name: "nil_in_nil_out", in: nil, wantNil: true},
		{name: "io_eof", in: io.EOF, wantSentinel: modbus.ErrConnectionLost},
		{name: "io_unexpected_eof", in: io.ErrUnexpectedEOF, wantSentinel: modbus.ErrConnectionLost},
		{name: "net_timeout", in: fakeTimeoutErr{}, wantSentinel: modbus.ErrTimeout},
		{
			name: "net_op_error",
			in: &net.OpError{
				Op:  "read",
				Net: "tcp",
				Err: errors.New("connection reset by peer"),
			},
			wantSentinel: modbus.ErrConnectionLost,
		},
		{
			name:         "mb_illegal_function",
			in:           &gridx.Error{FunctionCode: 0x83, ExceptionCode: gridx.ExceptionCodeIllegalFunction},
			wantSentinel: modbus.ErrIllegalFunction,
		},
		{
			name:         "mb_illegal_address",
			in:           &gridx.Error{FunctionCode: 0x83, ExceptionCode: gridx.ExceptionCodeIllegalDataAddress},
			wantSentinel: modbus.ErrIllegalAddress,
		},
		{
			name:         "mb_illegal_data_value",
			in:           &gridx.Error{FunctionCode: 0x83, ExceptionCode: gridx.ExceptionCodeIllegalDataValue},
			wantSentinel: modbus.ErrIllegalDataValue,
		},
		{
			name:         "mb_server_failure",
			in:           &gridx.Error{FunctionCode: 0x83, ExceptionCode: gridx.ExceptionCodeServerDeviceFailure},
			wantSentinel: modbus.ErrServerFailure,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := modbus.WrapError("test_op", tc.in)
			if tc.wantNil {
				assert.NoError(t, got)
				return
			}
			require.Error(t, got)
			assert.ErrorIs(t, got, tc.wantSentinel)
			assert.Contains(t, got.Error(), "test_op")
		})
	}
}

func TestWrapError_UnknownExceptionCode_NoSentinelMatch(t *testing.T) {
	t.Parallel()

	in := &gridx.Error{FunctionCode: 0x83, ExceptionCode: 0x06}
	got := modbus.WrapError("test_op", in)
	require.Error(t, got)
	assert.NotErrorIs(t, got, modbus.ErrIllegalAddress)
	assert.NotErrorIs(t, got, modbus.ErrIllegalFunction)
	assert.NotErrorIs(t, got, modbus.ErrIllegalDataValue)
	assert.NotErrorIs(t, got, modbus.ErrServerFailure)
	assert.Contains(t, got.Error(), "test_op")
}

func TestWrapError_PlainError_NoSentinelMatch(t *testing.T) {
	t.Parallel()

	in := errors.New("some random error")
	got := modbus.WrapError("test_op", in)
	require.Error(t, got)
	assert.NotErrorIs(t, got, modbus.ErrConnectionLost)
	assert.NotErrorIs(t, got, modbus.ErrTimeout)
	assert.NotErrorIs(t, got, modbus.ErrIllegalAddress)
	assert.Contains(t, got.Error(), "test_op")
	assert.Contains(t, got.Error(), "some random error")
}

var _ = time.Second
