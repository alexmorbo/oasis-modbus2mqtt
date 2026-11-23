package service

import "github.com/alexmorbo/oasis-modbus2mqtt/domain/entity"

// MergeSlowForTest exposes defaultMerger.MergeSlow to the external test package.
func MergeSlowForTest(prev, partial entity.Snapshot) entity.Snapshot {
	return defaultMerger{}.MergeSlow(prev, partial)
}
