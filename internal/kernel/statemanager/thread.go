package statemanager

import (
	"sync"
	"time"
)

// --- Thread Management ---

// AddThread adds or updates a TID -> PID mapping and its reverse mapping.
// This is the primary method for tracking thread creation and associating
// threads with their parent processes.
func (sm *KernelStateManager) AddThread(tid, pid uint32) {
	// Store the TID -> PID mapping directly.
	sm.tidToPid.Store(tid, pid)

	// Add the TID to the set of threads for the given PID
	val, _ := sm.pidToTids.LoadOrStore(pid, &sync.Map{})
	tidSet := val.(*sync.Map)
	tidSet.Store(tid, struct{}{})
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
	pid, exists := sm.tidToPid.Load(tid)
	if !exists {
		return 0, false
	}
	return pid, true
}
