package statemanager

import (
	"time"
)

// threadTerminationRecord holds the necessary context to perform a safe,
// conditional cleanup of a terminated thread, preventing race conditions with TID reuse.
type threadTerminationRecord struct {
	TermTime time.Time // The wall-clock time the thread was marked for deletion.
	StartKey uint64    // The StartKey of the parent process at the time of termination.
}

// --- Thread Management ---

// AddThread adds or updates a TID -> StartKey mapping.
// This is the primary method for tracking thread creation and associating
// threads with their parent process instances.
// If the parent process is not yet known, the thread is added to a temporary
// pending list to be resolved shortly.
func (ss *SystemState) AddThread(tid, pid uint32, eventTimestamp time.Time, subProcessTag uint32) {

	// We assume the parent Process/Start event has already been processed.
	// We look up the current StartKey for the given PID to create the mapping.
	if startKey, ok := ss.processManager.GetProcessCurrentStartKey(pid); ok {
		if pData, pOk := ss.processManager.GetProcessDataBySK(startKey); pOk {
			pData.Info.mu.Lock()
			createTime := pData.Info.CreateTime
			pData.Info.mu.Unlock()

			// This check remains as a critical guard against PID reuse. If a thread's timestamp
			// is older than its parent process, something is fundamentally wrong.
			if eventTimestamp.After(createTime.Add(-time.Millisecond)) {
				// Store the TID -> StartKey mapping directly. This is the successful hot path.
				ss.tidToStartKey.Store(tid, startKey)
				GlobalDiagnostics.RecordThreadAdded()

				// --- Service Name Attribution ---
				ss.resolveAndApplyServiceName(pData, subProcessTag)

				ss.log.Trace().Uint32("tid", tid).
					Uint32("pid", pid).
					Uint64("start_key", startKey).
					Msg("Thread added and associated with process")

				return // Success.
			}

			// ERROR CASE: The thread's timestamp is BEFORE the current process's create time.
			// This indicates a PID was reused faster than we could process the events.
			// This should be rare and is now treated as an error.
			ss.log.Error().
				Uint32("tid", tid).
				Uint32("pid", pid).
				Time("thread_time", eventTimestamp).
				Time("process_create_time", createTime).
				Msg("ERROR: Thread event arrived for a reused PID before the old process was terminated. Attributing to unknown.")
		}
	}

	// ERROR CASE: If we reach here, the parent process (by PID) was not found in the state manager.
	ss.log.Error().
		Uint32("tid", tid).
		Uint32("pid", pid).
		Msg("ERROR: Parent process not found for thread. Attributing to unknown.")

	// As a fallback, associate the thread with the "unknown_process" singleton
	// to ensure its metrics are not lost entirely.
	ss.tidToStartKey.Store(tid, UnknownProcessStartKey)
}

// MarkThreadForDeletion marks a thread to be cleaned up after the next scrape.
// This is used for individual thread termination events.
func (ss *SystemState) MarkThreadForDeletion(tid uint32) {
	// To handle TID reuse correctly, we must capture the StartKey that this
	// thread instance was associated with at the moment of termination.
	if startKey, ok := ss.tidToStartKey.Load(tid); ok {
		ss.terminatedThreads.Store(tid, &threadTerminationRecord{
			TermTime: time.Now(),
			StartKey: startKey,
		})
		ss.log.Trace().Uint32("tid", tid).Uint64("start_key", startKey).Msg("Thread marked for deletion")
	} else {
		// This can happen if a thread terminates that we were not tracking (e.g. from rundown).
		ss.log.Trace().Uint32("tid", tid).Msg("Ignoring termination for untracked thread")
	}
}

// GetStartKeyForThread resolves a TID to its parent process's unique StartKey.
// This is a critical function used on the hot path by collectors (e.g., for disk I/O,
// memory faults) to attribute events to a specific process instance.
// It is a single, fast, lock-free map lookup.
func (ss *SystemState) GetStartKeyForThread(tid uint32) (uint64, bool) {
	return ss.tidToStartKey.Load(tid)
}

// GetThreadCount returns the current number of tracked threads.
// This is a helper for the debug endpoint and implements the debug.StateProvider interface.
func (ss *SystemState) GetThreadCount() int {
	var count int
	ss.tidToStartKey.Range(func(u uint32, u2 uint64) bool {
		count++
		return true
	})
	return count
}

// RangeThreads iterates over threads for the debug http handler.
func (ss *SystemState) RangeThreads(f func(tid uint32, startKey uint64)) {
	ss.tidToStartKey.Range(func(tid uint32, startKey uint64) bool {
		f(tid, startKey)
		return true
	})
}
