package kernelthread

import "sync"

// ThreadMapping manages the mapping of thread IDs (TID) to process IDs (PID).
// This mapping is responsible for maintaining the crucial thread-to-process mapping,
// which is required by multiple collectors.
type ThreadMapping struct {
	mapping sync.Map // key: TID (uint32), value: PID (uint32)
}

// NewThreadMapping creates a new ThreadMapping instance.
func NewThreadMapping() *ThreadMapping {
	return &ThreadMapping{}
}

// AddThread adds or updates a TID -> PID mapping.
func (tm *ThreadMapping) AddThread(tid, pid uint32) {
	tm.mapping.Store(tid, pid)
}

// RemoveThread removes a TID -> PID mapping.
func (tm *ThreadMapping) RemoveThread(tid uint32) {
	tm.mapping.Delete(tid)
}

// GetProcessID resolves a thread ID to its parent process ID.
// It returns the process ID and a boolean indicating if the mapping was found.
func (tm *ThreadMapping) GetProcessID(threadID uint32) (uint32, bool) {
	pid, exists := tm.mapping.Load(threadID)
	if !exists {
		return 0, false
	}
	return pid.(uint32), true
}
