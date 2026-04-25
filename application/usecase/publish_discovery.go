package usecase

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"sync"

	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/discovery"
)

// DiscoveryBuilder produces the slice of HA discovery configs to publish for a
// given firmware label. Implementations are pure (no I/O) and safe for repeated
// calls; the canonical implementation lives in infrastructure/discovery.
type DiscoveryBuilder interface {
	// Build returns the full set of (topic, payload) pairs that describe every
	// entity the bridge exposes. The firmware string is stamped into the
	// device.sw_version field of each payload.
	Build(firmware string) ([]discovery.Discovery, error)
}

// Publisher is the minimal MQTT publish surface used by both publish use cases.
// Defining the interface in this file (and re-using it from publish_state.go
// via package-level visibility) keeps the use cases independent of the MQTT
// client implementation in infrastructure/mqtt.
type Publisher interface {
	// Publish sends payload to topic. retained=true asks the broker to store
	// the last value for late subscribers; retained=false sends a normal one-
	// shot message. Errors propagate verbatim to the caller.
	Publish(ctx context.Context, topic string, payload []byte, retained bool) error
}

// PublishDiscovery is the use case that publishes Home Assistant MQTT
// discovery configs. It re-publishes the full set on a firmware change so HA
// always reflects the live controller version.
type PublishDiscovery struct {
	builder      DiscoveryBuilder
	publisher    Publisher
	logger       *slog.Logger
	mu           sync.Mutex
	lastFirmware string
}

// NewPublishDiscovery constructs a PublishDiscovery. A nil builder or nil
// publisher is a programming error and panics. A nil logger falls back to
// slog.Default.
func NewPublishDiscovery(b DiscoveryBuilder, p Publisher, logger *slog.Logger) *PublishDiscovery {
	if b == nil {
		panic("publish discovery: builder must not be nil")
	}
	if p == nil {
		panic("publish discovery: publisher must not be nil")
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &PublishDiscovery{
		builder:   b,
		publisher: p,
		logger:    logger,
	}
}

// Publish forces a full discovery republish for the supplied firmware label.
// Every config is sent with retained=true so HA can rebuild entities after a
// broker restart. On any publish error the use case continues with the
// remaining configs and aggregates failures via errors.Join. lastFirmware is
// updated regardless of partial errors so subsequent PublishIfChanged calls
// honour the most recent attempted version.
func (p *PublishDiscovery) Publish(ctx context.Context, firmware string) error {
	configs, err := p.builder.Build(firmware)
	if err != nil {
		return fmt.Errorf("build discovery: %w", err)
	}

	var errs []error
	for _, cfg := range configs {
		if pubErr := p.publisher.Publish(ctx, cfg.Topic, cfg.Payload, true); pubErr != nil {
			errs = append(errs, fmt.Errorf("publish discovery topic %s: %w", cfg.Topic, pubErr))
		}
	}

	p.mu.Lock()
	p.lastFirmware = firmware
	p.mu.Unlock()

	if len(errs) > 0 {
		p.logger.Warn("discovery publish completed with errors",
			"count", len(configs),
			"errors", len(errs),
			"firmware", firmware,
		)
		return errors.Join(errs...)
	}

	p.logger.Info("discovery published",
		"count", len(configs),
		"firmware", firmware,
	)
	return nil
}

// PublishIfChanged publishes the full discovery set only when firmware differs
// from the last successful (or attempted) firmware label. The first call after
// construction always publishes because lastFirmware starts empty. An empty
// firmware argument with an empty cached value is treated as "no change" and
// returns nil without contacting the builder.
func (p *PublishDiscovery) PublishIfChanged(ctx context.Context, firmware string) error {
	p.mu.Lock()
	last := p.lastFirmware
	p.mu.Unlock()

	if firmware == last && last != "" {
		p.logger.Debug("discovery skip: firmware unchanged", "firmware", firmware)
		return nil
	}
	if firmware == "" && last == "" {
		p.logger.Debug("discovery skip: empty firmware on first call")
		return nil
	}
	return p.Publish(ctx, firmware)
}
