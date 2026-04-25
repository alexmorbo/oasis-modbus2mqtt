package http_test

import (
	"context"
	"net"
	stdhttp "net/http"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	httpinternal "github.com/alexmorbo/oasis-modbus2mqtt/interface/http"
)

func freeAddr(t *testing.T) string {
	t.Helper()
	var lc net.ListenConfig
	ln, err := lc.Listen(context.Background(), "tcp", "127.0.0.1:0")
	require.NoError(t, err)
	addr := ln.Addr().String()
	require.NoError(t, ln.Close())
	return addr
}

func TestNewServer_EmptyAddr_Panic(t *testing.T) {
	t.Parallel()

	assert.PanicsWithValue(t,
		"http: NewServer requires non-empty addr",
		func() {
			httpinternal.NewServer("", gin.New(), nil)
		},
	)
}

func TestNewServer_NilRouter_Panic(t *testing.T) {
	t.Parallel()

	assert.PanicsWithValue(t,
		"http: NewServer requires non-nil router",
		func() {
			httpinternal.NewServer("127.0.0.1:0", nil, nil)
		},
	)
}

func TestNewServer_NilLogger_FallsBack(t *testing.T) {
	t.Parallel()

	srv := httpinternal.NewServer("127.0.0.1:0", gin.New(), nil)
	require.NotNil(t, srv)
}

func TestServer_StartAndShutdown(t *testing.T) {
	t.Parallel()

	addr := freeAddr(t)

	gin.SetMode(gin.ReleaseMode)
	router := gin.New()
	router.GET("/ping", func(c *gin.Context) {
		c.String(stdhttp.StatusOK, "pong")
	})

	srv := httpinternal.NewServer(addr, router, nil)

	errCh := make(chan error, 1)
	go func() { errCh <- srv.Start() }()

	// Wait for the listener to come up by polling /ping.
	deadline := time.Now().Add(2 * time.Second)
	gotPong := false
	for time.Now().Before(deadline) {
		req, reqErr := stdhttp.NewRequestWithContext(context.Background(), stdhttp.MethodGet, "http://"+addr+"/ping", nil)
		if reqErr == nil {
			resp, doErr := stdhttp.DefaultClient.Do(req)
			if doErr == nil {
				_ = resp.Body.Close()
				if resp.StatusCode == stdhttp.StatusOK {
					gotPong = true
					break
				}
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	require.True(t, gotPong, "server did not become ready within 2s")

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	require.NoError(t, srv.Shutdown(ctx))

	select {
	case err := <-errCh:
		assert.NoError(t, err)
	case <-time.After(2 * time.Second):
		t.Fatal("server did not stop within 2s of Shutdown")
	}
}

func TestServer_Shutdown_AlreadyStopped(t *testing.T) {
	t.Parallel()

	srv := httpinternal.NewServer("127.0.0.1:0", gin.New(), nil)
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	// Shutdown without Start is a no-op for net/http; it should not error.
	require.NoError(t, srv.Shutdown(ctx))
}
