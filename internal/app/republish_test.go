package app

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
	"github.com/alexmorbo/oasis-modbus2mqtt/domain/valueobject"
)

type fakeSnapshots struct{ snap entity.Snapshot }

func (f fakeSnapshots) Snapshot() entity.Snapshot { return f.snap }

type fakeDiscovery struct {
	mu        sync.Mutex
	firmwares []string
	deadlines []bool
	err       error
}

func (f *fakeDiscovery) Publish(ctx context.Context, firmware string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	_, ok := ctx.Deadline()
	f.firmwares = append(f.firmwares, firmware)
	f.deadlines = append(f.deadlines, ok)
	return f.err
}

type fakeState struct {
	mu    sync.Mutex
	snaps []entity.Snapshot
	err   error
}

func (f *fakeState) Apply(_ context.Context, snap entity.Snapshot) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.snaps = append(f.snaps, snap)
	return f.err
}

func TestRepublisher(t *testing.T) {
	t.Parallel()

	polled := entity.Snapshot{
		Firmware:       valueobject.FirmwareFromRaw(0x5200),
		FilterDaysLeft: 120,
		PolledAt:       time.Date(2026, 11, 23, 10, 0, 0, 0, time.UTC),
	}

	tests := []struct {
		name         string
		snap         entity.Snapshot
		discErr      error
		stateErr     error
		wantFirmware string
		wantStates   int
	}{
		{name: "no snapshot publishes discovery only", snap: entity.Snapshot{}, wantFirmware: "unknown", wantStates: 0},
		{name: "snapshot publishes discovery and state", snap: polled, wantFirmware: "v5.2.0", wantStates: 1},
		{name: "discovery error still publishes state", snap: polled, discErr: errors.New("boom"), wantFirmware: "v5.2.0", wantStates: 1},
		{name: "state error is swallowed", snap: polled, stateErr: errors.New("boom"), wantFirmware: "v5.2.0", wantStates: 1},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			disc := &fakeDiscovery{err: tc.discErr}
			state := &fakeState{err: tc.stateErr}
			republish := newRepublisher(fakeSnapshots{snap: tc.snap}, disc, state, newDiscardLogger())

			republish(context.Background(), "test")

			require.Equal(t, []string{tc.wantFirmware}, disc.firmwares)
			assert.Equal(t, []bool{true}, disc.deadlines, "republish must bound its context")
			require.Len(t, state.snaps, tc.wantStates)
			if tc.wantStates > 0 {
				assert.Equal(t, int16(120), state.snaps[0].FilterDaysLeft)
			}
		})
	}
}
