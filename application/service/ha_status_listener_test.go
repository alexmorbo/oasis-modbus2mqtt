package service_test

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/port"
	"github.com/alexmorbo/oasis-modbus2mqtt/application/service"
)

type fakeHASubscriber struct {
	mu       sync.Mutex
	handlers map[string]port.MessageHandler
	subErr   error
}

func (s *fakeHASubscriber) Subscribe(_ context.Context, topic string, h port.MessageHandler) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.subErr != nil {
		return s.subErr
	}
	if s.handlers == nil {
		s.handlers = make(map[string]port.MessageHandler)
	}
	s.handlers[topic] = h
	return nil
}

func (s *fakeHASubscriber) handler(topic string) (port.MessageHandler, bool) { //nolint:unparam
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.handlers[topic]
	return h, ok
}

func discardLoggerHA() *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, nil))
}

func TestNewHAStatusListener_NilSubscriber_Panics(t *testing.T) {
	t.Parallel()
	require.PanicsWithValue(t, "ha status listener: subscriber must not be nil", func() {
		_ = service.NewHAStatusListener(nil, "homeassistant/status", nil, discardLoggerHA())
	})
}

func TestNewHAStatusListener_EmptyTopic_Panics(t *testing.T) {
	t.Parallel()
	require.PanicsWithValue(t, "ha status listener: topic must not be empty", func() {
		_ = service.NewHAStatusListener(&fakeHASubscriber{}, "", nil, discardLoggerHA())
	})
}

func TestNewHAStatusListener_NilLogger_FallsBack(t *testing.T) {
	t.Parallel()
	l := service.NewHAStatusListener(&fakeHASubscriber{}, "homeassistant/status", nil, nil)
	require.NotNil(t, l)
}

func TestStart_RegistersHandler(t *testing.T) {
	t.Parallel()
	sub := &fakeHASubscriber{}
	l := service.NewHAStatusListener(sub, "homeassistant/status", nil, discardLoggerHA())
	require.NoError(t, l.Start(context.Background()))
	_, ok := sub.handler("homeassistant/status")
	assert.True(t, ok, "handler must be registered for the configured topic")
}

func TestStart_SubscribeError_Propagates(t *testing.T) {
	t.Parallel()
	sub := &fakeHASubscriber{subErr: errors.New("subscribe boom")}
	l := service.NewHAStatusListener(sub, "homeassistant/status", nil, discardLoggerHA())
	err := l.Start(context.Background())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "subscribe boom")
}

func TestHandle_OnlinePayload_InvokesCallback(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		payload string
	}{
		{"lowercase", "online"},
		{"uppercase", "ONLINE"},
		{"mixed_case", "Online"},
		{"with_whitespace", "  online  "},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			sub := &fakeHASubscriber{}
			l := service.NewHAStatusListener(sub, "homeassistant/status",
				func(_ context.Context) { calls.Add(1) },
				discardLoggerHA())
			require.NoError(t, l.Start(context.Background()))
			h, ok := sub.handler("homeassistant/status")
			require.True(t, ok)
			h("homeassistant/status", []byte(tc.payload))
			<-l.Idle()
			assert.Equal(t, int32(1), calls.Load())
		})
	}
}

func TestHandle_OfflinePayload_DoesNotInvokeCallback(t *testing.T) {
	t.Parallel()
	var calls atomic.Int32
	sub := &fakeHASubscriber{}
	l := service.NewHAStatusListener(sub, "homeassistant/status",
		func(_ context.Context) { calls.Add(1) },
		discardLoggerHA())
	require.NoError(t, l.Start(context.Background()))
	h, ok := sub.handler("homeassistant/status")
	require.True(t, ok)
	h("homeassistant/status", []byte("offline"))
	<-l.Idle()
	assert.Equal(t, int32(0), calls.Load())
}

func TestHandle_GarbagePayload_DoesNotInvokeCallback(t *testing.T) {
	t.Parallel()
	cases := []string{"", "garbage", "true", "ok", "{\"state\":\"online\"}"}
	for _, payload := range cases {
		t.Run(payload, func(t *testing.T) {
			t.Parallel()
			var calls atomic.Int32
			sub := &fakeHASubscriber{}
			l := service.NewHAStatusListener(sub, "homeassistant/status",
				func(_ context.Context) { calls.Add(1) },
				discardLoggerHA())
			require.NoError(t, l.Start(context.Background()))
			h, ok := sub.handler("homeassistant/status")
			require.True(t, ok)
			h("homeassistant/status", []byte(payload))
			<-l.Idle()
			assert.Equal(t, int32(0), calls.Load())
		})
	}
}

func TestHandle_OnlineWithNilCallback_NoPanic(t *testing.T) {
	t.Parallel()
	sub := &fakeHASubscriber{}
	l := service.NewHAStatusListener(sub, "homeassistant/status", nil, discardLoggerHA())
	require.NoError(t, l.Start(context.Background()))
	h, ok := sub.handler("homeassistant/status")
	require.True(t, ok)
	require.NotPanics(t, func() {
		h("homeassistant/status", []byte("online"))
	})
}

func TestHandle_CallbackReceivesBoundedContext(t *testing.T) {
	t.Parallel()
	sub := &fakeHASubscriber{}
	var deadlineSet atomic.Bool
	l := service.NewHAStatusListener(sub, "homeassistant/status",
		func(ctx context.Context) {
			if _, ok := ctx.Deadline(); ok {
				deadlineSet.Store(true)
			}
		},
		discardLoggerHA())
	require.NoError(t, l.Start(context.Background()))
	h, ok := sub.handler("homeassistant/status")
	require.True(t, ok)
	h("homeassistant/status", []byte("online"))
	<-l.Idle()
	assert.True(t, deadlineSet.Load(), "callback context must carry a deadline")
}

func TestHandle_DoesNotBlockOnCallback_AndCoalescesBirths(t *testing.T) {
	t.Parallel()
	sub := &fakeHASubscriber{}
	entered := make(chan struct{}, 16)
	release := make(chan struct{})
	var calls atomic.Int32
	l := service.NewHAStatusListener(sub, "homeassistant/status",
		func(_ context.Context) {
			calls.Add(1)
			entered <- struct{}{}
			<-release
		},
		discardLoggerHA())
	require.NoError(t, l.Start(t.Context()))
	h, ok := sub.handler("homeassistant/status")
	require.True(t, ok)

	returned := make(chan struct{})
	go func() {
		defer close(returned)
		h("homeassistant/status", []byte("online"))
	}()
	select {
	case <-returned:
	case <-time.After(time.Second):
		t.Fatal("handle blocked on the callback: it runs on the MQTT router goroutine")
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("callback never ran")
	}

	// Three births during the run coalesce into exactly one more run.
	for range 3 {
		h("homeassistant/status", []byte("online"))
	}
	select {
	case <-l.Idle():
		t.Fatal("listener reported idle while a run is in flight")
	default:
	}
	close(release)
	<-l.Idle()
	assert.Equal(t, int32(2), calls.Load(), "one run plus one coalesced re-run")

	h("homeassistant/status", []byte("online"))
	<-l.Idle()
	assert.Equal(t, int32(3), calls.Load(), "a birth after the runs finished starts a new run")
}

func TestHandle_CallbackCtxCancelledByStartCtx(t *testing.T) {
	t.Parallel()
	sub := &fakeHASubscriber{}
	ctx, cancel := context.WithCancel(t.Context())
	l := service.NewHAStatusListener(sub, "homeassistant/status",
		func(cbCtx context.Context) { <-cbCtx.Done() },
		discardLoggerHA())
	require.NoError(t, l.Start(ctx))
	h, ok := sub.handler("homeassistant/status")
	require.True(t, ok)
	h("homeassistant/status", []byte("online"))
	cancel()
	select {
	case <-l.Idle():
	case <-time.After(2 * time.Second):
		t.Fatal("callback ctx must derive from the Start ctx")
	}
}
