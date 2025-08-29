package threadmapping

import (
	"sync"
	"time"
)

// ThreadMapping manages the bidirectional mapping between TIDs and PIDs.
// It is designed for high concurrency and includes mechanisms to clean up
// stale entries and remove all threads associated with a terminated process.
type ThreadMapping struct {
	tidToPid          sync.Map // key: TID (uint32), value: PID (uint32)
	pidToTids         sync.Map // key: PID (uint32), value: *sync.Map (of TIDs -> struct{})
	terminatedThreads sync.Map // key: TID (uint32), value: time.Time (when marked for deletion)
}

// NewThreadMapping creates a new ThreadMapping instance.
func NewThreadMapping() *ThreadMapping {
	return &ThreadMapping{}
}

// AddThread adds or updates a TID -> PID mapping and its reverse mapping.
func (tm *ThreadMapping) AddThread(tid, pid uint32) {
	// Store the TID -> PID mapping directly.
	tm.tidToPid.Store(tid, pid)

	// Add the TID to the set of threads for the given PID
	val, _ := tm.pidToTids.LoadOrStore(pid, &sync.Map{})
	tidSet := val.(*sync.Map)
	tidSet.Store(tid, struct{}{})
}

// removeThread is the internal, non-exported function that performs the actual deletion.
func (tm *ThreadMapping) removeThread(tid uint32) {
	// First, find the PID associated with this TID
	val, exists := tm.tidToPid.Load(tid)
	if !exists {
		return // Thread already removed
	}
	pid := val.(uint32)

	// Remove the primary mapping
	tm.tidToPid.Delete(tid)

	// Remove the TID from the process's thread set
	if val, exists := tm.pidToTids.Load(pid); exists {
		tidSet := val.(*sync.Map)
		tidSet.Delete(tid)
	}
}

// MarkThreadForDeletion marks a thread to be cleaned up after the next scrape.
func (tm *ThreadMapping) MarkThreadForDeletion(tid uint32) {
	tm.terminatedThreads.Store(tid, time.Now())
}

// MarkThreadsForDeletionForProcess marks all threads for a given process for cleanup.
func (tm *ThreadMapping) MarkThreadsForDeletionForProcess(pid uint32) {
	val, exists := tm.pidToTids.Load(pid)
	if !exists {
		return // No threads registered for this process
	}
	tidSet := val.(*sync.Map)

	// Iterate over all TIDs for this process and mark them for deletion
	now := time.Now()
	tidSet.Range(func(key, value any) bool {
		tid := key.(uint32)
		tm.terminatedThreads.Store(tid, now)
		return true
	})
}

// PostScrapeCleanup performs the actual deletion of threads that were marked.
// This is called by the sentinel collector AFTER a Prometheus scrape is complete.
func (tm *ThreadMapping) PostScrapeCleanup() {
	tm.terminatedThreads.Range(func(key, value any) bool {
		tid := key.(uint32)
		tm.removeThread(tid)
		tm.terminatedThreads.Delete(tid)
		return true
	})
}

// ForceCleanupOldEntries is a safety net to prevent memory leaks if scrapes stop.
// It removes any thread that was marked for deletion more than the maxAge ago.
func (tm *ThreadMapping) ForceCleanupOldEntries(maxAge time.Duration) int {
	cutoff := time.Now().Add(-maxAge)
	var cleanedCount int

	tm.terminatedThreads.Range(func(key, value any) bool {
		tid := key.(uint32)
		markedTime := value.(time.Time)

		if markedTime.Before(cutoff) {
			tm.removeThread(tid)
			tm.terminatedThreads.Delete(tid)
			cleanedCount++
		}
		return true
	})
	return cleanedCount
}

// GetProcessID resolves a TID to its PID.
func (tm *ThreadMapping) GetProcessID(threadID uint32) (uint32, bool) {
	val, exists := tm.tidToPid.Load(threadID)
	if !exists {
		return 0, false
	}

	return val.(uint32), true
}

// RangePIDs iterates over all process IDs currently tracked in the mapping.
// This is used by the collector to perform periodic cleanup.
func (tm *ThreadMapping) RangePIDs(f func(pid uint32) bool) {
	tm.pidToTids.Range(func(key, value any) bool {
		return f(key.(uint32))
	})
}
