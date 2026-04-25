// Package catalog declares the static register catalog and the small types
// (poll groups, entity hints) that decorate each register definition.
package catalog

import "time"

// PollGroup classifies how often a register is read by the poller.
type PollGroup int

const (
	// PollGroupNone marks command-only registers that are never polled.
	PollGroupNone PollGroup = 0
	// PollGroupHot is the 5-second read tier (fast-changing state).
	PollGroupHot PollGroup = 1
	// PollGroupMedium is the 15-second read tier (slowly-changing state).
	PollGroupMedium PollGroup = 2
	// PollGroupSlow is the 60-second read tier (effectively static state).
	PollGroupSlow PollGroup = 3
)

// String returns the canonical lowercase label.
func (g PollGroup) String() string {
	switch g {
	case PollGroupNone:
		return "none"
	case PollGroupHot:
		return "hot"
	case PollGroupMedium:
		return "medium"
	case PollGroupSlow:
		return "slow"
	default:
		return "unknown"
	}
}

// Period returns the polling interval for the group, or 0 for None / Unknown.
func (g PollGroup) Period() time.Duration {
	switch g {
	case PollGroupHot:
		return 5 * time.Second
	case PollGroupMedium:
		return 15 * time.Second
	case PollGroupSlow:
		return 60 * time.Second
	default:
		return 0
	}
}
