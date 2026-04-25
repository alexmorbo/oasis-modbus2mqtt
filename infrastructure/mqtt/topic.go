// Package mqtt provides the paho-based MQTTPublisher implementation and the
// TopicBuilder that maps Home Assistant discovery / state / command /
// availability conventions to concrete topic strings.
package mqtt

import (
	"fmt"

	"github.com/alexmorbo/oasis-modbus2mqtt/infrastructure/config"
)

// TopicBuilder produces MQTT topics for Home Assistant discovery and the
// device's state, command, and availability channels. It is immutable once
// constructed and safe for concurrent use.
type TopicBuilder struct {
	discoveryPrefix string
	devicePrefix    string
}

// NewTopicBuilder returns a TopicBuilder using the prefixes from cfg. It
// panics if either DiscoveryPrefix or DevicePrefix is empty — those are
// mandatory for HA discovery to function and an empty value would silently
// produce malformed topics.
func NewTopicBuilder(cfg config.HAConfig) *TopicBuilder {
	if cfg.DiscoveryPrefix == "" {
		panic("mqtt: HAConfig.DiscoveryPrefix is empty")
	}
	if cfg.DevicePrefix == "" {
		panic("mqtt: HAConfig.DevicePrefix is empty")
	}
	return &TopicBuilder{
		discoveryPrefix: cfg.DiscoveryPrefix,
		devicePrefix:    cfg.DevicePrefix,
	}
}

// Discovery returns the HA discovery config topic for the given component
// (e.g. "sensor", "switch") and object id.
func (b *TopicBuilder) Discovery(component, objectID string) string {
	return fmt.Sprintf("%s/%s/%s/%s/config", b.discoveryPrefix, component, b.devicePrefix, objectID)
}

// State returns the state topic for the given object id.
func (b *TopicBuilder) State(objectID string) string {
	return fmt.Sprintf("%s/state/%s", b.devicePrefix, objectID)
}

// Command returns the command topic for the given object id.
func (b *TopicBuilder) Command(objectID string) string {
	return fmt.Sprintf("%s/cmd/%s", b.devicePrefix, objectID)
}

// CommandWildcard returns the wildcard command topic used for a single
// subscription that covers every command object id.
func (b *TopicBuilder) CommandWildcard() string {
	return fmt.Sprintf("%s/cmd/+", b.devicePrefix)
}

// Availability returns the device's availability topic, used both for the
// LWT and for explicit online/offline announcements.
func (b *TopicBuilder) Availability() string {
	return fmt.Sprintf("%s/availability", b.devicePrefix)
}
