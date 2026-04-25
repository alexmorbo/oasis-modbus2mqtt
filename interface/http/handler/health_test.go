package handler_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
	"github.com/alexmorbo/oasis-modbus2mqtt/interface/http/handler"
)

type fakeConn struct{ connected bool }

func (f *fakeConn) Connected() bool { return f.connected }

type fakeSnap struct{ snap entity.Snapshot }

func (f *fakeSnap) Snapshot() entity.Snapshot { return f.snap }

type fakeClock struct{ now time.Time }

func (f *fakeClock) Now() time.Time { return f.now }

func init() {
	gin.SetMode(gin.TestMode)
}

func newReq(t *testing.T, path string) (*httptest.ResponseRecorder, *gin.Context) {
	t.Helper()
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, path, nil)
	return w, c
}

func TestLive_Returns200(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	h := handler.NewHealth(
		&fakeConn{connected: true},
		&fakeSnap{snap: entity.Snapshot{PolledAt: now}},
		120*time.Second,
		&fakeClock{now: now},
		nil,
	)

	w, c := newReq(t, "/health/live")
	h.Live(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"status":"ok"`)
}

func TestReady_AllHealthy_Returns200(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	h := handler.NewHealth(
		&fakeConn{connected: true},
		&fakeSnap{snap: entity.Snapshot{PolledAt: now.Add(-30 * time.Second)}},
		120*time.Second,
		&fakeClock{now: now},
		nil,
	)

	w, c := newReq(t, "/health/ready")
	h.Ready(c)

	assert.Equal(t, http.StatusOK, w.Code)
	assert.Contains(t, w.Body.String(), `"status":"ok"`)
}

func TestReady_MQTTDisconnected_Returns503(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	h := handler.NewHealth(
		&fakeConn{connected: false},
		&fakeSnap{snap: entity.Snapshot{PolledAt: now}},
		120*time.Second,
		&fakeClock{now: now},
		nil,
	)

	w, c := newReq(t, "/health/ready")
	h.Ready(c)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), `"reason":"mqtt_not_connected"`)
	assert.Contains(t, w.Body.String(), `"status":"not_ready"`)
}

func TestReady_StaleSnapshot_Returns503(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	h := handler.NewHealth(
		&fakeConn{connected: true},
		&fakeSnap{snap: entity.Snapshot{PolledAt: now.Add(-200 * time.Second)}},
		120*time.Second,
		&fakeClock{now: now},
		nil,
	)

	w, c := newReq(t, "/health/ready")
	h.Ready(c)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), `"reason":"stale_snapshot"`)
}

func TestReady_ZeroPolledAt_Returns503(t *testing.T) {
	t.Parallel()

	now := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)
	h := handler.NewHealth(
		&fakeConn{connected: true},
		&fakeSnap{snap: entity.Snapshot{}},
		120*time.Second,
		&fakeClock{now: now},
		nil,
	)

	w, c := newReq(t, "/health/ready")
	h.Ready(c)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
	assert.Contains(t, w.Body.String(), `"reason":"stale_snapshot"`)
}

func TestNewHealth_NilMQTT_Panic(t *testing.T) {
	t.Parallel()

	assert.PanicsWithValue(t,
		"handler: NewHealth requires non-nil ConnectionChecker",
		func() {
			handler.NewHealth(nil, &fakeSnap{}, time.Second, nil, nil)
		},
	)
}

func TestNewHealth_NilSnapshots_Panic(t *testing.T) {
	t.Parallel()

	assert.PanicsWithValue(t,
		"handler: NewHealth requires non-nil SnapshotProvider",
		func() {
			handler.NewHealth(&fakeConn{}, nil, time.Second, nil, nil)
		},
	)
}

func TestNewHealth_ZeroThreshold_Panic(t *testing.T) {
	t.Parallel()

	assert.PanicsWithValue(t,
		"handler: NewHealth requires positive readiness threshold",
		func() {
			handler.NewHealth(&fakeConn{}, &fakeSnap{}, 0, nil, nil)
		},
	)
}

func TestNewHealth_NegativeThreshold_Panic(t *testing.T) {
	t.Parallel()

	assert.PanicsWithValue(t,
		"handler: NewHealth requires positive readiness threshold",
		func() {
			handler.NewHealth(&fakeConn{}, &fakeSnap{}, -time.Second, nil, nil)
		},
	)
}

func TestNewHealth_NilClock_FallsBack(t *testing.T) {
	t.Parallel()

	h := handler.NewHealth(
		&fakeConn{connected: true},
		&fakeSnap{snap: entity.Snapshot{PolledAt: time.Now()}},
		120*time.Second,
		nil,
		nil,
	)
	require.NotNil(t, h)

	w, c := newReq(t, "/health/ready")
	h.Ready(c)

	assert.Equal(t, http.StatusOK, w.Code)
}

func TestNewHealth_NilLogger_FallsBack(t *testing.T) {
	t.Parallel()

	h := handler.NewHealth(
		&fakeConn{connected: true},
		&fakeSnap{snap: entity.Snapshot{}},
		120*time.Second,
		&fakeClock{now: time.Now()},
		nil,
	)
	require.NotNil(t, h)

	w, c := newReq(t, "/health/ready")
	h.Ready(c)

	assert.Equal(t, http.StatusServiceUnavailable, w.Code)
}
