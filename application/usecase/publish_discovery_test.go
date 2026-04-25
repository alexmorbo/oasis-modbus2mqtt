package usecase_test

import (
	"bytes"
	"context"
	"errors"
	"log/slog"
	"sync"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/alexmorbo/oasis-modbus2mqtt/application/usecase"
	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/discovery"
)

type fakeDiscoveryBuilder struct {
	configs []discovery.Discovery
	err     error
	calls   int
	mu      sync.Mutex
}

func (b *fakeDiscoveryBuilder) Build(_ string) ([]discovery.Discovery, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.calls++
	if b.err != nil {
		return nil, b.err
	}
	out := make([]discovery.Discovery, len(b.configs))
	copy(out, b.configs)
	return out, nil
}

func (b *fakeDiscoveryBuilder) callCount() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.calls
}

type publishCall struct {
	topic    string
	payload  []byte
	retained bool
}

type fakePublisher struct {
	mu         sync.Mutex
	calls      []publishCall
	errAt      map[string]error
	defaultErr error
}

func (f *fakePublisher) Publish(_ context.Context, topic string, payload []byte, retained bool) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, publishCall{topic: topic, payload: append([]byte(nil), payload...), retained: retained})
	if err, ok := f.errAt[topic]; ok {
		return err
	}
	return f.defaultErr
}

func (f *fakePublisher) snapshot() []publishCall {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]publishCall, len(f.calls))
	copy(out, f.calls)
	return out
}

func discardLoggerDiscovery() *slog.Logger {
	return slog.New(slog.NewTextHandler(&bytes.Buffer{}, &slog.HandlerOptions{Level: slog.LevelError + 1}))
}

func sampleConfigs() []discovery.Discovery {
	return []discovery.Discovery{
		{Topic: "ha/sensor/a/config", Payload: []byte(`{"a":1}`)},
		{Topic: "ha/sensor/b/config", Payload: []byte(`{"b":2}`)},
		{Topic: "ha/sensor/c/config", Payload: []byte(`{"c":3}`)},
	}
}

func TestNewPublishDiscovery_NilBuilder_Panics(t *testing.T) {
	t.Parallel()
	require.PanicsWithValue(t, "publish discovery: builder must not be nil", func() {
		_ = usecase.NewPublishDiscovery(nil, &fakePublisher{}, nil)
	})
}

func TestNewPublishDiscovery_NilPublisher_Panics(t *testing.T) {
	t.Parallel()
	require.PanicsWithValue(t, "publish discovery: publisher must not be nil", func() {
		_ = usecase.NewPublishDiscovery(&fakeDiscoveryBuilder{}, nil, nil)
	})
}

func TestNewPublishDiscovery_NilLogger_FallsBack(t *testing.T) {
	t.Parallel()
	pd := usecase.NewPublishDiscovery(&fakeDiscoveryBuilder{configs: sampleConfigs()}, &fakePublisher{}, nil)
	require.NotNil(t, pd)
	err := pd.Publish(context.Background(), "v5.2.0")
	require.NoError(t, err)
}

func TestPublish_PublishesAll_AsRetained(t *testing.T) {
	t.Parallel()
	b := &fakeDiscoveryBuilder{configs: sampleConfigs()}
	p := &fakePublisher{}
	pd := usecase.NewPublishDiscovery(b, p, discardLoggerDiscovery())

	err := pd.Publish(context.Background(), "v5.2.0")
	require.NoError(t, err)

	calls := p.snapshot()
	require.Len(t, calls, 3)
	for _, c := range calls {
		assert.True(t, c.retained, "expected retained=true for topic %s", c.topic)
	}
	assert.Equal(t, "ha/sensor/a/config", calls[0].topic)
	assert.Equal(t, []byte(`{"a":1}`), calls[0].payload)
	assert.Equal(t, "ha/sensor/c/config", calls[2].topic)
}

func TestPublish_FirstPublishError_ContinuesAndAggregates(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("broker rejected")
	b := &fakeDiscoveryBuilder{configs: sampleConfigs()}
	p := &fakePublisher{errAt: map[string]error{"ha/sensor/b/config": sentinel}}
	pd := usecase.NewPublishDiscovery(b, p, discardLoggerDiscovery())

	err := pd.Publish(context.Background(), "v5.2.0")
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
	assert.Len(t, p.snapshot(), 3, "all configs should be attempted despite mid-loop error")
}

func TestPublish_BuilderError_PropagatesNoPublish(t *testing.T) {
	t.Parallel()
	sentinel := errors.New("build failed")
	b := &fakeDiscoveryBuilder{err: sentinel}
	p := &fakePublisher{}
	pd := usecase.NewPublishDiscovery(b, p, discardLoggerDiscovery())

	err := pd.Publish(context.Background(), "v5.2.0")
	require.Error(t, err)
	assert.ErrorIs(t, err, sentinel)
	assert.Empty(t, p.snapshot())
}

func TestPublishIfChanged_SkipsWhenSameFirmware(t *testing.T) {
	t.Parallel()
	b := &fakeDiscoveryBuilder{configs: sampleConfigs()}
	p := &fakePublisher{}
	pd := usecase.NewPublishDiscovery(b, p, discardLoggerDiscovery())

	require.NoError(t, pd.PublishIfChanged(context.Background(), "v5.2.0"))
	require.Len(t, p.snapshot(), 3)

	require.NoError(t, pd.PublishIfChanged(context.Background(), "v5.2.0"))
	assert.Len(t, p.snapshot(), 3, "second call should not publish anything new")
	assert.Equal(t, 1, b.callCount())
}

func TestPublishIfChanged_PublishesOnFirstCall(t *testing.T) {
	t.Parallel()
	b := &fakeDiscoveryBuilder{configs: sampleConfigs()}
	p := &fakePublisher{}
	pd := usecase.NewPublishDiscovery(b, p, discardLoggerDiscovery())

	err := pd.PublishIfChanged(context.Background(), "v5.2.0")
	require.NoError(t, err)
	assert.Len(t, p.snapshot(), 3)
}

func TestPublishIfChanged_PublishesOnFirmwareChange(t *testing.T) {
	t.Parallel()
	b := &fakeDiscoveryBuilder{configs: sampleConfigs()}
	p := &fakePublisher{}
	pd := usecase.NewPublishDiscovery(b, p, discardLoggerDiscovery())

	require.NoError(t, pd.PublishIfChanged(context.Background(), "v5.2.0"))
	require.NoError(t, pd.PublishIfChanged(context.Background(), "v5.2.1"))
	assert.Len(t, p.snapshot(), 6)
	assert.Equal(t, 2, b.callCount())
}

func TestPublishIfChanged_EmptyFirmwareOnFirstCall_Skips(t *testing.T) {
	t.Parallel()
	b := &fakeDiscoveryBuilder{configs: sampleConfigs()}
	p := &fakePublisher{}
	pd := usecase.NewPublishDiscovery(b, p, discardLoggerDiscovery())

	require.NoError(t, pd.PublishIfChanged(context.Background(), ""))
	assert.Empty(t, p.snapshot())
	assert.Equal(t, 0, b.callCount())
}

func TestPublish_LogsCount(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	pd := usecase.NewPublishDiscovery(&fakeDiscoveryBuilder{configs: sampleConfigs()}, &fakePublisher{}, logger)
	require.NoError(t, pd.Publish(context.Background(), "v5.2.0"))
	out := buf.String()
	assert.Contains(t, out, "discovery published")
	assert.Contains(t, out, `"count":3`)
	assert.Contains(t, out, `"firmware":"v5.2.0"`)
}

func TestPublish_LogsErrorsCount(t *testing.T) {
	t.Parallel()
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, &slog.HandlerOptions{Level: slog.LevelDebug}))

	p := &fakePublisher{errAt: map[string]error{"ha/sensor/b/config": errors.New("nope")}}
	pd := usecase.NewPublishDiscovery(&fakeDiscoveryBuilder{configs: sampleConfigs()}, p, logger)
	require.Error(t, pd.Publish(context.Background(), "v5.2.0"))
	out := buf.String()
	assert.Contains(t, out, "discovery publish completed with errors")
	assert.Contains(t, out, `"errors":1`)
}
