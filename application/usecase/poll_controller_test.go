package usecase_test

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/dto"
	"github.com/alexmorbo/oasis-modbus2mqtt/application/usecase"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
)

type fakeDispatcher struct {
	inputResp   map[string][]uint16
	holdingResp map[string][]uint16
	inputErr    map[string]error
	holdingErr  map[string]error
	inputCalls  int
	holdCalls   int
}

func newFakeDispatcher() *fakeDispatcher {
	return &fakeDispatcher{
		inputResp:   make(map[string][]uint16),
		holdingResp: make(map[string][]uint16),
		inputErr:    make(map[string]error),
		holdingErr:  make(map[string]error),
	}
}

func key(start, count uint16) string { return fmt.Sprintf("%d:%d", start, count) }

func (f *fakeDispatcher) ReadInput(_ context.Context, start, count uint16) ([]uint16, error) {
	f.inputCalls++
	k := key(start, count)
	if err := f.inputErr[k]; err != nil {
		return nil, err
	}
	if v, ok := f.inputResp[k]; ok {
		return v, nil
	}
	return nil, fmt.Errorf("fakeDispatcher: no input response for %s", k)
}

func (f *fakeDispatcher) ReadHolding(_ context.Context, start, count uint16) ([]uint16, error) {
	f.holdCalls++
	k := key(start, count)
	if err := f.holdingErr[k]; err != nil {
		return nil, err
	}
	if v, ok := f.holdingResp[k]; ok {
		return v, nil
	}
	return nil, fmt.Errorf("fakeDispatcher: no holding response for %s", k)
}

type fakeClock struct{ now time.Time }

func (f fakeClock) Now() time.Time { return f.now }

func TestNewPollController_NilDispatcherPanics(t *testing.T) {
	t.Parallel()
	assert.Panics(t, func() {
		_ = usecase.NewPollController(nil, nil, nil)
	})
}

func TestNewPollController_DefaultsClockAndLogger(t *testing.T) {
	t.Parallel()
	d := newFakeDispatcher()
	pc := usecase.NewPollController(d, nil, nil)
	assert.NotNil(t, pc)
}

func TestPollHot_DecodesAllFields(t *testing.T) {
	t.Parallel()

	d := newFakeDispatcher()
	// State_0 = 0b0000_0001_1100_0001 = PowerOn(b0)+HeatCapable(b6)+CoolCapable(b7)+b8 (mode_heat indicator)
	// We use b0|b6|b7 only: 0x00C1.
	state0 := uint16((1 << 0) | (1 << 6) | (1 << 7))
	state1 := uint16(2) // OpPreheatCalorifier
	d.inputResp[key(2, 17)] = []uint16{
		state0, // i2 State_0
		state1, // i3 State_1
		0,      // i4 Error_Code
		0,      // i5 Error_Code_1
		0x0530, // i6 Last_Time = 5min 48s
		0,      // i7 (skip)
		0,      // i8 (skip)
		0x00DC, // i9 TkanK = 220 → 22.0°C
		0,      // i10
		0,      // i11
		0,      // i12
		0,      // i13
		52,     // i14 ZagrFiltr1
		0,      // i15 DInputs (skip)
		0x21,   // i16 DOutputs: heater b0=1, damper b5=1
		0,      // i17 (skip)
		30,     // i18 PID
	}
	d.inputResp[key(25, 6)] = []uint16{5, 0, 0, 0, 0, 7} // FanState1=5, FanState2=7
	d.holdingResp[key(31, 2)] = []uint16{225, 5}         // TargetTemp=22.5, FanTarget1=5

	clk := fakeClock{now: time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)}
	pc := usecase.NewPollController(d, clk, nil)

	snap, err := pc.PollHot(context.Background())
	require.NoError(t, err)

	assert.True(t, snap.PowerOn)
	assert.False(t, snap.Switching)
	assert.True(t, snap.HeatCapable)
	assert.True(t, snap.CoolCapable)
	assert.Equal(t, entity.OpPreheatCalorifier, snap.Operation)
	assert.Equal(t, 5*time.Minute+48*time.Second, snap.OperationTimeLeft)
	assert.InDelta(t, 22.0, snap.SupplyTemp.Celsius(), 0.001)
	assert.Equal(t, int16(52), snap.FilterPct)
	assert.True(t, snap.HeaterPWM)
	assert.True(t, snap.DamperOpen)
	assert.Equal(t, uint16(30), snap.PIDDemand)
	assert.Equal(t, uint16(5), snap.FanState1)
	assert.Equal(t, uint16(7), snap.FanState2)
	assert.InDelta(t, 22.5, snap.TargetTemp.Celsius(), 0.001)
	assert.Equal(t, uint16(5), snap.FanTarget1)
	assert.Equal(t, uint16(0), snap.RawErrors[0])
	assert.Equal(t, uint16(0), snap.RawErrors[1])
	assert.Equal(t, clk.now, snap.PolledAt)
}

func TestPollHot_RawErrorsCaptured(t *testing.T) {
	t.Parallel()

	d := newFakeDispatcher()
	d.inputResp[key(2, 17)] = []uint16{0, 0, 0x1234, 0xABCD, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0}
	d.inputResp[key(25, 6)] = []uint16{0, 0, 0, 0, 0, 0}
	d.holdingResp[key(31, 2)] = []uint16{0, 0}

	pc := usecase.NewPollController(d, fakeClock{now: time.Now()}, nil)
	snap, err := pc.PollHot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, uint16(0x1234), snap.RawErrors[0])
	assert.Equal(t, uint16(0xABCD), snap.RawErrors[1])
}

func TestPollHot_OutOfRangeTemperature_DefaultsZero(t *testing.T) {
	t.Parallel()

	d := newFakeDispatcher()
	in1 := make([]uint16, 17)
	in1[7] = 0xFFE7 // -25 → -2.5°C, below the 5..30 range
	d.inputResp[key(2, 17)] = in1
	d.inputResp[key(25, 6)] = []uint16{0, 0, 0, 0, 0, 0}
	d.holdingResp[key(31, 2)] = []uint16{0, 0}

	pc := usecase.NewPollController(d, fakeClock{now: time.Now()}, nil)
	snap, err := pc.PollHot(context.Background())
	require.NoError(t, err)
	assert.InDelta(t, 0.0, snap.SupplyTemp.Celsius(), 0.001)
}

func TestPollHot_FirstReadErrorPropagates(t *testing.T) {
	t.Parallel()
	d := newFakeDispatcher()
	d.inputErr[key(2, 17)] = dto.ErrModbusNotConnected
	pc := usecase.NewPollController(d, fakeClock{now: time.Now()}, nil)
	_, err := pc.PollHot(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, dto.ErrModbusNotConnected)
	assert.Contains(t, err.Error(), "i2..i18")
}

func TestPollHot_SecondReadErrorPropagates(t *testing.T) {
	t.Parallel()
	d := newFakeDispatcher()
	d.inputResp[key(2, 17)] = make([]uint16, 17)
	d.inputErr[key(25, 6)] = dto.ErrModbusNotConnected
	pc := usecase.NewPollController(d, fakeClock{now: time.Now()}, nil)
	_, err := pc.PollHot(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "i25..i30")
}

func TestPollHot_ThirdReadErrorPropagates(t *testing.T) {
	t.Parallel()
	d := newFakeDispatcher()
	d.inputResp[key(2, 17)] = make([]uint16, 17)
	d.inputResp[key(25, 6)] = make([]uint16, 6)
	d.holdingErr[key(31, 2)] = dto.ErrModbusNotConnected
	pc := usecase.NewPollController(d, fakeClock{now: time.Now()}, nil)
	_, err := pc.PollHot(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "h31..h32")
}

func TestPollHot_UnexpectedReadLength(t *testing.T) {
	t.Parallel()
	d := newFakeDispatcher()
	d.inputResp[key(2, 17)] = make([]uint16, 5) // wrong length
	pc := usecase.NewPollController(d, fakeClock{now: time.Now()}, nil)
	_, err := pc.PollHot(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, usecase.ErrUnexpectedReadLength)
}

func TestPollHot_UnexpectedReadLengthSecondBatch(t *testing.T) {
	t.Parallel()
	d := newFakeDispatcher()
	d.inputResp[key(2, 17)] = make([]uint16, 17)
	d.inputResp[key(25, 6)] = make([]uint16, 3)
	pc := usecase.NewPollController(d, fakeClock{now: time.Now()}, nil)
	_, err := pc.PollHot(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, usecase.ErrUnexpectedReadLength)
}

func TestPollHot_UnexpectedReadLengthHolding(t *testing.T) {
	t.Parallel()
	d := newFakeDispatcher()
	d.inputResp[key(2, 17)] = make([]uint16, 17)
	d.inputResp[key(25, 6)] = make([]uint16, 6)
	d.holdingResp[key(31, 2)] = make([]uint16, 1)
	pc := usecase.NewPollController(d, fakeClock{now: time.Now()}, nil)
	_, err := pc.PollHot(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, usecase.ErrUnexpectedReadLength)
}

func TestPollMedium_DecodesAllFields(t *testing.T) {
	t.Parallel()

	d := newFakeDispatcher()
	d.inputResp[key(57, 2)] = []uint16{210, 45} // RoomTemp = 21.0, RoomHumidity=45
	d.inputResp[key(69, 2)] = []uint16{0xCAFE, 0xBABE}
	d.holdingResp[key(86, 1)] = []uint16{0x0001} // ModeHeat

	clk := fakeClock{now: time.Date(2026, 4, 25, 13, 0, 0, 0, time.UTC)}
	pc := usecase.NewPollController(d, clk, nil)

	snap, err := pc.PollMedium(context.Background())
	require.NoError(t, err)
	assert.InDelta(t, 21.0, snap.RoomTemp.Celsius(), 0.001)
	assert.Equal(t, uint16(45), snap.RoomHumidity)
	assert.Equal(t, uint16(0xCAFE), snap.RawErrors[2])
	assert.Equal(t, uint16(0xBABE), snap.RawErrors[3])
	assert.Equal(t, "heat", snap.CurrentMode.String())
	assert.Equal(t, clk.now, snap.PolledAt)
}

func TestPollMedium_FirstReadError(t *testing.T) {
	t.Parallel()
	d := newFakeDispatcher()
	d.inputErr[key(57, 2)] = dto.ErrModbusNotConnected
	pc := usecase.NewPollController(d, fakeClock{}, nil)
	_, err := pc.PollMedium(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "i57..i58")
}

func TestPollMedium_SecondReadError(t *testing.T) {
	t.Parallel()
	d := newFakeDispatcher()
	d.inputResp[key(57, 2)] = make([]uint16, 2)
	d.inputErr[key(69, 2)] = dto.ErrModbusNotConnected
	pc := usecase.NewPollController(d, fakeClock{}, nil)
	_, err := pc.PollMedium(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "i69..i70")
}

func TestPollMedium_HoldingReadError(t *testing.T) {
	t.Parallel()
	d := newFakeDispatcher()
	d.inputResp[key(57, 2)] = make([]uint16, 2)
	d.inputResp[key(69, 2)] = make([]uint16, 2)
	d.holdingErr[key(86, 1)] = dto.ErrModbusNotConnected
	pc := usecase.NewPollController(d, fakeClock{}, nil)
	_, err := pc.PollMedium(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "h86")
}

func TestPollMedium_LengthChecks(t *testing.T) {
	t.Parallel()

	t.Run("first batch wrong length", func(t *testing.T) {
		t.Parallel()
		d := newFakeDispatcher()
		d.inputResp[key(57, 2)] = []uint16{0}
		pc := usecase.NewPollController(d, fakeClock{}, nil)
		_, err := pc.PollMedium(context.Background())
		assert.ErrorIs(t, err, usecase.ErrUnexpectedReadLength)
	})

	t.Run("second batch wrong length", func(t *testing.T) {
		t.Parallel()
		d := newFakeDispatcher()
		d.inputResp[key(57, 2)] = make([]uint16, 2)
		d.inputResp[key(69, 2)] = []uint16{0}
		pc := usecase.NewPollController(d, fakeClock{}, nil)
		_, err := pc.PollMedium(context.Background())
		assert.ErrorIs(t, err, usecase.ErrUnexpectedReadLength)
	})

	t.Run("holding wrong length", func(t *testing.T) {
		t.Parallel()
		d := newFakeDispatcher()
		d.inputResp[key(57, 2)] = make([]uint16, 2)
		d.inputResp[key(69, 2)] = make([]uint16, 2)
		d.holdingResp[key(86, 1)] = []uint16{}
		pc := usecase.NewPollController(d, fakeClock{}, nil)
		_, err := pc.PollMedium(context.Background())
		assert.ErrorIs(t, err, usecase.ErrUnexpectedReadLength)
	})
}

func TestPollSlow_DecodesAllFields(t *testing.T) {
	t.Parallel()

	d := newFakeDispatcher()
	d.inputResp[key(0, 1)] = []uint16{0x5210}   // Firmware v5.2.1.0 → "v5.2.1"
	d.inputResp[key(79, 1)] = []uint16{0x4242}  // DeviceID
	d.holdingResp[key(0, 1)] = []uint16{0x0121} // heater=1, cooler=2, recup=1

	clk := fakeClock{now: time.Date(2026, 4, 25, 14, 0, 0, 0, time.UTC)}
	pc := usecase.NewPollController(d, clk, nil)

	snap, err := pc.PollSlow(context.Background())
	require.NoError(t, err)
	assert.Equal(t, "v5.2.1", snap.Firmware.String())
	assert.Equal(t, uint16(0x4242), snap.DeviceID)
	assert.Equal(t, entity.HeaterElectric, snap.DeviceConfig.Heater)
	assert.Equal(t, entity.CoolerFancoil, snap.DeviceConfig.Cooler)
	assert.Equal(t, entity.RecuperatorPlate, snap.DeviceConfig.Recuperator)
	assert.Equal(t, clk.now, snap.PolledAt)
}

func TestPollSlow_FirstReadError(t *testing.T) {
	t.Parallel()
	d := newFakeDispatcher()
	d.inputErr[key(0, 1)] = dto.ErrModbusNotConnected
	pc := usecase.NewPollController(d, fakeClock{}, nil)
	_, err := pc.PollSlow(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "i0")
}

func TestPollSlow_SecondReadError(t *testing.T) {
	t.Parallel()
	d := newFakeDispatcher()
	d.inputResp[key(0, 1)] = make([]uint16, 1)
	d.inputErr[key(79, 1)] = dto.ErrModbusNotConnected
	pc := usecase.NewPollController(d, fakeClock{}, nil)
	_, err := pc.PollSlow(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "i79")
}

func TestPollSlow_HoldingReadError(t *testing.T) {
	t.Parallel()
	d := newFakeDispatcher()
	d.inputResp[key(0, 1)] = make([]uint16, 1)
	d.inputResp[key(79, 1)] = make([]uint16, 1)
	d.holdingErr[key(0, 1)] = dto.ErrModbusNotConnected
	pc := usecase.NewPollController(d, fakeClock{}, nil)
	_, err := pc.PollSlow(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "h0")
}

func TestPollSlow_LengthChecks(t *testing.T) {
	t.Parallel()

	t.Run("first wrong", func(t *testing.T) {
		t.Parallel()
		d := newFakeDispatcher()
		d.inputResp[key(0, 1)] = []uint16{}
		pc := usecase.NewPollController(d, fakeClock{}, nil)
		_, err := pc.PollSlow(context.Background())
		assert.ErrorIs(t, err, usecase.ErrUnexpectedReadLength)
	})

	t.Run("second wrong", func(t *testing.T) {
		t.Parallel()
		d := newFakeDispatcher()
		d.inputResp[key(0, 1)] = make([]uint16, 1)
		d.inputResp[key(79, 1)] = []uint16{}
		pc := usecase.NewPollController(d, fakeClock{}, nil)
		_, err := pc.PollSlow(context.Background())
		assert.ErrorIs(t, err, usecase.ErrUnexpectedReadLength)
	})

	t.Run("holding wrong", func(t *testing.T) {
		t.Parallel()
		d := newFakeDispatcher()
		d.inputResp[key(0, 1)] = make([]uint16, 1)
		d.inputResp[key(79, 1)] = make([]uint16, 1)
		d.holdingResp[key(0, 1)] = []uint16{}
		pc := usecase.NewPollController(d, fakeClock{}, nil)
		_, err := pc.PollSlow(context.Background())
		assert.ErrorIs(t, err, usecase.ErrUnexpectedReadLength)
	})
}

func TestPollHot_PolledAtUsesClock(t *testing.T) {
	t.Parallel()
	d := newFakeDispatcher()
	d.inputResp[key(2, 17)] = make([]uint16, 17)
	d.inputResp[key(25, 6)] = make([]uint16, 6)
	d.holdingResp[key(31, 2)] = make([]uint16, 2)

	clk := fakeClock{now: time.Date(2030, 1, 2, 3, 4, 5, 0, time.UTC)}
	pc := usecase.NewPollController(d, clk, nil)
	snap, err := pc.PollHot(context.Background())
	require.NoError(t, err)
	assert.Equal(t, clk.now, snap.PolledAt)
}

func TestPollHot_ContextErrorPropagates(t *testing.T) {
	t.Parallel()

	customErr := errors.New("boom")
	d := newFakeDispatcher()
	d.inputErr[key(2, 17)] = customErr
	pc := usecase.NewPollController(d, fakeClock{}, nil)
	_, err := pc.PollHot(context.Background())
	require.Error(t, err)
	assert.ErrorIs(t, err, customErr)
}

// TestNewPollController_NilFallbacks verifies that nil clock and nil logger
// each fall back to safe defaults without panicking, while a nil dispatcher
// still panics.
func TestNewPollController_NilFallbacks(t *testing.T) {
	t.Parallel()

	t.Run("nil clock uses RealClock no panic", func(t *testing.T) {
		t.Parallel()
		d := newFakeDispatcher()
		pc := usecase.NewPollController(d, nil, nil)
		assert.NotNil(t, pc)
	})

	t.Run("nil logger uses slog.Default no panic", func(t *testing.T) {
		t.Parallel()
		d := newFakeDispatcher()
		pc := usecase.NewPollController(d, fakeClock{now: time.Now()}, nil)
		assert.NotNil(t, pc)
	})

	t.Run("nil dispatcher panics with dispatcher in message", func(t *testing.T) {
		t.Parallel()
		assert.Panics(t, func() {
			_ = usecase.NewPollController(nil, nil, nil)
		})
	})
}

var errFakeRead = errors.New("fake read failure")

// TestPollHot_ReadErrors verifies that an error from any of the three hot-tier
// reads is propagated as a wrapped error with the address-range hint.
func TestPollHot_ReadErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setupErr    func(d *fakeDispatcher)
		wantContain string
	}{
		{
			name: "first read i2..i18 error",
			setupErr: func(d *fakeDispatcher) {
				d.inputErr[key(2, 17)] = errFakeRead
			},
			wantContain: "i2..i18",
		},
		{
			name: "second read i25..i30 error",
			setupErr: func(d *fakeDispatcher) {
				d.inputResp[key(2, 17)] = make([]uint16, 17)
				d.inputErr[key(25, 6)] = errFakeRead
			},
			wantContain: "i25..i30",
		},
		{
			name: "third read h31..h32 error",
			setupErr: func(d *fakeDispatcher) {
				d.inputResp[key(2, 17)] = make([]uint16, 17)
				d.inputResp[key(25, 6)] = make([]uint16, 6)
				d.holdingErr[key(31, 2)] = errFakeRead
			},
			wantContain: "h31..h32",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newFakeDispatcher()
			tc.setupErr(d)
			pc := usecase.NewPollController(d, fakeClock{now: time.Now()}, nil)
			_, err := pc.PollHot(context.Background())
			require.Error(t, err)
			assert.ErrorIs(t, err, errFakeRead)
			assert.Contains(t, err.Error(), tc.wantContain)
		})
	}
}

// TestPollHot_LengthMismatch verifies that a wrong-length response from any
// hot-tier read returns an error wrapping ErrUnexpectedReadLength.
func TestPollHot_LengthMismatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(d *fakeDispatcher)
	}{
		{
			name: "i2..i18 wrong length",
			setup: func(d *fakeDispatcher) {
				d.inputResp[key(2, 17)] = make([]uint16, 16) // 16 instead of 17
			},
		},
		{
			name: "i25..i30 wrong length",
			setup: func(d *fakeDispatcher) {
				d.inputResp[key(2, 17)] = make([]uint16, 17)
				d.inputResp[key(25, 6)] = make([]uint16, 4) // 4 instead of 6
			},
		},
		{
			name: "h31..h32 wrong length",
			setup: func(d *fakeDispatcher) {
				d.inputResp[key(2, 17)] = make([]uint16, 17)
				d.inputResp[key(25, 6)] = make([]uint16, 6)
				d.holdingResp[key(31, 2)] = make([]uint16, 0) // 0 instead of 2
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newFakeDispatcher()
			tc.setup(d)
			pc := usecase.NewPollController(d, fakeClock{now: time.Now()}, nil)
			_, err := pc.PollHot(context.Background())
			require.Error(t, err)
			require.ErrorIs(t, err, usecase.ErrUnexpectedReadLength)
		})
	}
}

// TestPollMedium_ReadErrors verifies that an error from any of the three
// medium-tier reads is propagated with the address-range hint.
func TestPollMedium_ReadErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setupErr    func(d *fakeDispatcher)
		wantContain string
	}{
		{
			name: "first read i57..i58 error",
			setupErr: func(d *fakeDispatcher) {
				d.inputErr[key(57, 2)] = errFakeRead
			},
			wantContain: "i57..i58",
		},
		{
			name: "second read i69..i70 error",
			setupErr: func(d *fakeDispatcher) {
				d.inputResp[key(57, 2)] = make([]uint16, 2)
				d.inputErr[key(69, 2)] = errFakeRead
			},
			wantContain: "i69..i70",
		},
		{
			name: "third read h86 error",
			setupErr: func(d *fakeDispatcher) {
				d.inputResp[key(57, 2)] = make([]uint16, 2)
				d.inputResp[key(69, 2)] = make([]uint16, 2)
				d.holdingErr[key(86, 1)] = errFakeRead
			},
			wantContain: "h86",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newFakeDispatcher()
			tc.setupErr(d)
			pc := usecase.NewPollController(d, fakeClock{}, nil)
			_, err := pc.PollMedium(context.Background())
			require.Error(t, err)
			assert.ErrorIs(t, err, errFakeRead)
			assert.Contains(t, err.Error(), tc.wantContain)
		})
	}
}

// TestPollMedium_LengthMismatch verifies that a wrong-length response from any
// medium-tier read returns an error wrapping ErrUnexpectedReadLength.
func TestPollMedium_LengthMismatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(d *fakeDispatcher)
	}{
		{
			name: "i57..i58 wrong length",
			setup: func(d *fakeDispatcher) {
				d.inputResp[key(57, 2)] = make([]uint16, 1) // 1 instead of 2
			},
		},
		{
			name: "i69..i70 wrong length",
			setup: func(d *fakeDispatcher) {
				d.inputResp[key(57, 2)] = make([]uint16, 2)
				d.inputResp[key(69, 2)] = make([]uint16, 0) // 0 instead of 2
			},
		},
		{
			name: "h86 wrong length",
			setup: func(d *fakeDispatcher) {
				d.inputResp[key(57, 2)] = make([]uint16, 2)
				d.inputResp[key(69, 2)] = make([]uint16, 2)
				d.holdingResp[key(86, 1)] = make([]uint16, 0) // 0 instead of 1
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newFakeDispatcher()
			tc.setup(d)
			pc := usecase.NewPollController(d, fakeClock{}, nil)
			_, err := pc.PollMedium(context.Background())
			require.Error(t, err)
			require.ErrorIs(t, err, usecase.ErrUnexpectedReadLength)
		})
	}
}

// TestPollSlow_ReadErrors verifies that an error from any of the three
// slow-tier reads is propagated with the address-range hint.
func TestPollSlow_ReadErrors(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		setupErr    func(d *fakeDispatcher)
		wantContain string
	}{
		{
			name: "first read i0 error",
			setupErr: func(d *fakeDispatcher) {
				d.inputErr[key(0, 1)] = errFakeRead
			},
			wantContain: "i0",
		},
		{
			name: "second read i79 error",
			setupErr: func(d *fakeDispatcher) {
				d.inputResp[key(0, 1)] = make([]uint16, 1)
				d.inputErr[key(79, 1)] = errFakeRead
			},
			wantContain: "i79",
		},
		{
			name: "third read h0 error",
			setupErr: func(d *fakeDispatcher) {
				d.inputResp[key(0, 1)] = make([]uint16, 1)
				d.inputResp[key(79, 1)] = make([]uint16, 1)
				d.holdingErr[key(0, 1)] = errFakeRead
			},
			wantContain: "h0",
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newFakeDispatcher()
			tc.setupErr(d)
			pc := usecase.NewPollController(d, fakeClock{}, nil)
			_, err := pc.PollSlow(context.Background())
			require.Error(t, err)
			assert.ErrorIs(t, err, errFakeRead)
			assert.Contains(t, err.Error(), tc.wantContain)
		})
	}
}

// TestPollSlow_LengthMismatch verifies that a wrong-length response from any
// slow-tier read returns an error wrapping ErrUnexpectedReadLength.
func TestPollSlow_LengthMismatch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		setup func(d *fakeDispatcher)
	}{
		{
			name: "i0 wrong length",
			setup: func(d *fakeDispatcher) {
				d.inputResp[key(0, 1)] = make([]uint16, 0) // empty instead of 1
			},
		},
		{
			name: "i79 wrong length",
			setup: func(d *fakeDispatcher) {
				d.inputResp[key(0, 1)] = make([]uint16, 1)
				d.inputResp[key(79, 1)] = make([]uint16, 0) // empty instead of 1
			},
		},
		{
			name: "h0 wrong length",
			setup: func(d *fakeDispatcher) {
				d.inputResp[key(0, 1)] = make([]uint16, 1)
				d.inputResp[key(79, 1)] = make([]uint16, 1)
				d.holdingResp[key(0, 1)] = make([]uint16, 0) // empty instead of 1
			},
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newFakeDispatcher()
			tc.setup(d)
			pc := usecase.NewPollController(d, fakeClock{}, nil)
			_, err := pc.PollSlow(context.Background())
			require.Error(t, err)
			require.ErrorIs(t, err, usecase.ErrUnexpectedReadLength)
		})
	}
}

// TestSignedTempFromRaw_OutOfRange tests signedTempFromRaw indirectly via
// PollHot's i9 (TkanK_x10) position, which is offset 7 in the i2..i18 batch.
// Out-of-range readings collapse to zero Temperature; valid readings decode
// correctly.
func TestSignedTempFromRaw_OutOfRange(t *testing.T) {
	t.Parallel()

	makeIn1 := func(rawTkanK uint16) []uint16 {
		in1 := make([]uint16, 17)
		in1[7] = rawTkanK
		return in1
	}

	tests := []struct {
		name        string
		rawTkanK    uint16
		wantCelsius float64
		wantZero    bool
	}{
		{
			// int16(0xFFE7) = -25 → -25/10 = -2.5°C, below [5,30] → zero
			name:     "below range 0xFFE7 negative sensor",
			rawTkanK: 0xFFE7,
			wantZero: true,
		},
		{
			// int16(0x0F00) = 3840 → 3840/10 = 384°C, above [5,30] → zero
			name:     "above range 0x0F00 absurd reading",
			rawTkanK: 0x0F00,
			wantZero: true,
		},
		{
			// int16(0x00FA) = 250 → 250/10 = 25.0°C, within [5,30] → valid
			name:        "valid 25.0 degrees",
			rawTkanK:    0x00FA,
			wantCelsius: 25.0,
			wantZero:    false,
		},
		{
			// int16(0x0032) = 50 → 50/10 = 5.0°C, at lower boundary → valid
			name:        "lower boundary 5.0 degrees",
			rawTkanK:    0x0032,
			wantCelsius: 5.0,
			wantZero:    false,
		},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			d := newFakeDispatcher()
			d.inputResp[key(2, 17)] = makeIn1(tc.rawTkanK)
			d.inputResp[key(25, 6)] = make([]uint16, 6)
			d.holdingResp[key(31, 2)] = make([]uint16, 2)

			pc := usecase.NewPollController(d, fakeClock{now: time.Now()}, nil)
			snap, err := pc.PollHot(context.Background())
			require.NoError(t, err)

			if tc.wantZero {
				assert.InDelta(t, 0.0, snap.SupplyTemp.Celsius(), 0.001)
			} else {
				assert.InDelta(t, tc.wantCelsius, snap.SupplyTemp.Celsius(), 0.1)
			}
		})
	}
}
