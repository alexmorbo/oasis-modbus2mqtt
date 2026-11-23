package service

import (
	"context"

	"github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"
)

// MergeSlowForTest exposes defaultMerger.MergeSlow to the external test package.
func MergeSlowForTest(prev, partial entity.Snapshot) entity.Snapshot {
	return defaultMerger{}.MergeSlow(prev, partial)
}

// PollSlowOnceForTest runs one synchronous slow-tier poll-and-merge, so tests
// can interleave it with MergeSettingRegister deterministically.
func (p *Poller) PollSlowOnceForTest(ctx context.Context) {
	p.doPoll(ctx, p.ctrl.PollSlow, p.merger.MergeSlow, "slow")
}
