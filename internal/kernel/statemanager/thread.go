package statemanager

import (
	"time"
)

// --- Thread Management ---

// TODO: get starkey from pid?

// AddThread adds or updates a TID -> PID mapping and its reverse mapping.
// This is the primary method for tracking thread creation and associating
// threads with their parent processes.
// It has been optimized to perform only a single map insertion, avoiding extra allocations.
func (sm *KernelStateManager) AddThread(tid, pid uint32) {
	// Store the TID -> PID mapping directly. This is the only operation required on the hot path.
	sm.tidToPid.Store(tid, pid)
}

// MarkThreadForDeletion marks a thread to be cleaned up after the next scrape.
// This is used for individual thread termination events.
func (sm *KernelStateManager) MarkThreadForDeletion(tid uint32) {
	sm.terminatedThreads.Store(tid, time.Now())
}

// GetProcessIDForThread resolves a TID to its parent PID.
// This is a critical function used on the hot path by collectors (e.g., for context
// switches) to enrich thread-level events with process information.
func (sm *KernelStateManager) GetProcessIDForThread(tid uint32) (uint32, bool) {
	return sm.tidToPid.Load(tid)
}
