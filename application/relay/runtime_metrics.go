package relay

import "sync/atomic"

type RuntimeMetrics struct {
	snapshotUploadSuccess atomic.Uint64
	snapshotUploadFailure atomic.Uint64
}

func NewRuntimeMetrics() *RuntimeMetrics {
	return &RuntimeMetrics{}
}

func (m *RuntimeMetrics) IncSnapshotSuccess() {
	if m == nil {
		return
	}
	m.snapshotUploadSuccess.Add(1)
}

func (m *RuntimeMetrics) IncSnapshotFailure() {
	if m == nil {
		return
	}
	m.snapshotUploadFailure.Add(1)
}

func (m *RuntimeMetrics) SnapshotStats() map[string]uint64 {
	if m == nil {
		return map[string]uint64{"success": 0, "failure": 0}
	}
	return map[string]uint64{
		"success": m.snapshotUploadSuccess.Load(),
		"failure": m.snapshotUploadFailure.Load(),
	}
}
