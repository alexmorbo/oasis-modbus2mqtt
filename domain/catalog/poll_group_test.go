package catalog_test

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/catalog"
)

func TestPollGroup_String(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		group catalog.PollGroup
		want  string
	}{
		{name: "none", group: catalog.PollGroupNone, want: "none"},
		{name: "hot", group: catalog.PollGroupHot, want: "hot"},
		{name: "medium", group: catalog.PollGroupMedium, want: "medium"},
		{name: "slow", group: catalog.PollGroupSlow, want: "slow"},
		{name: "unknown high", group: catalog.PollGroup(99), want: "unknown"},
		{name: "unknown negative", group: catalog.PollGroup(-1), want: "unknown"},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.group.String())
		})
	}
}

func TestPollGroup_Period(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		group catalog.PollGroup
		want  time.Duration
	}{
		{name: "none has zero period", group: catalog.PollGroupNone, want: 0},
		{name: "hot is 5 seconds", group: catalog.PollGroupHot, want: 5 * time.Second},
		{name: "medium is 15 seconds", group: catalog.PollGroupMedium, want: 15 * time.Second},
		{name: "slow is 60 seconds", group: catalog.PollGroupSlow, want: 60 * time.Second},
		{name: "unknown has zero period", group: catalog.PollGroup(99), want: 0},
	}

	for _, tc := range tests {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			assert.Equal(t, tc.want, tc.group.Period())
		})
	}
}
