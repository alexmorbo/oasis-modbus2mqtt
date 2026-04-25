package http_test

import (
	"context"
	"io"
	stdhttp "net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
	httpinternal "github.com/alexmorbo/oasis-modbus2mqtt/interface/http"
	"github.com/alexmorbo/oasis-modbus2mqtt/interface/http/handler"
)

type stubConn struct{ connected bool }

func (s *stubConn) Connected() bool { return s.connected }

type stubSnap struct{ snap entity.Snapshot }

func (s *stubSnap) Snapshot() entity.Snapshot { return s.snap }

type stubClock struct{ now time.Time }

func (s *stubClock) Now() time.Time { return s.now }

func buildRouter(connected bool, polled time.Time, now time.Time) *httptest.Server {
	h := handler.NewHealth(
		&stubConn{connected: connected},
		&stubSnap{snap: entity.Snapshot{PolledAt: polled}},
		120*time.Second,
		&stubClock{now: now},
		nil,
	)
	m := handler.NewMetrics()
	r := httpinternal.NewRouter(h, m, nil)
	return httptest.NewServer(r)
}

func get(t *testing.T, url string) (int, string) {
	t.Helper()
	req, err := stdhttp.NewRequestWithContext(context.Background(), stdhttp.MethodGet, url, nil)
	require.NoError(t, err)
	resp, err := stdhttp.DefaultClient.Do(req)
	require.NoError(t, err)
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	require.NoError(t, err)
	return resp.StatusCode, string(body)
}

func TestRouter_Routes_Health_Metrics(t *testing.T) {
	t.Parallel()

	now := time.Now()
	srv := buildRouter(true, now, now)
	defer srv.Close()

	t.Run("live", func(t *testing.T) {
		code, body := get(t, srv.URL+"/health/live")
		assert.Equal(t, stdhttp.StatusOK, code)
		assert.Contains(t, body, `"status":"ok"`)
	})

	t.Run("ready", func(t *testing.T) {
		code, body := get(t, srv.URL+"/health/ready")
		assert.Equal(t, stdhttp.StatusOK, code)
		assert.Contains(t, body, `"status":"ok"`)
	})

	t.Run("metrics", func(t *testing.T) {
		code, _ := get(t, srv.URL+"/metrics")
		assert.Equal(t, stdhttp.StatusOK, code)
	})
}

func TestRouter_NotFound_Returns404(t *testing.T) {
	t.Parallel()

	now := time.Now()
	srv := buildRouter(true, now, now)
	defer srv.Close()

	code, _ := get(t, srv.URL+"/unknown")
	assert.Equal(t, stdhttp.StatusNotFound, code)
}

func TestRouter_ReadyDisconnected_Returns503(t *testing.T) {
	t.Parallel()

	now := time.Now()
	srv := buildRouter(false, now, now)
	defer srv.Close()

	code, body := get(t, srv.URL+"/health/ready")
	assert.Equal(t, stdhttp.StatusServiceUnavailable, code)
	assert.Contains(t, body, "mqtt_not_connected")
}

func TestNewRouter_NilHealth_Panic(t *testing.T) {
	t.Parallel()

	assert.PanicsWithValue(t,
		"http: NewRouter requires non-nil Health handler",
		func() {
			httpinternal.NewRouter(nil, handler.NewMetrics(), nil)
		},
	)
}

func TestNewRouter_NilMetrics_Panic(t *testing.T) {
	t.Parallel()

	now := time.Now()
	h := handler.NewHealth(
		&stubConn{connected: true},
		&stubSnap{snap: entity.Snapshot{PolledAt: now}},
		120*time.Second,
		&stubClock{now: now},
		nil,
	)
	assert.PanicsWithValue(t,
		"http: NewRouter requires non-nil Metrics handler",
		func() {
			httpinternal.NewRouter(h, nil, nil)
		},
	)
}

func TestNewRouter_NilLogger_FallsBack(t *testing.T) {
	t.Parallel()

	now := time.Now()
	h := handler.NewHealth(
		&stubConn{connected: true},
		&stubSnap{snap: entity.Snapshot{PolledAt: now}},
		120*time.Second,
		&stubClock{now: now},
		nil,
	)
	r := httpinternal.NewRouter(h, handler.NewMetrics(), nil)
	require.NotNil(t, r)
}
