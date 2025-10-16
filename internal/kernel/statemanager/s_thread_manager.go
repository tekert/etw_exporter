package statemanager

import (
	"time"

	"etw_exporter/internal/maps"

	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// threadTerminationRecord holds the necessary context to perform a safe,
// conditional cleanup of a terminated thread, preventing race conditions with TID reuse.
type threadTerminationRecord struct {
	TerminatedAtGeneration uint64 // The scrape generation when the thread was marked for deletion.
	StartKey               uint64 // The StartKey of the parent process at the time of termination.
}

// ThreadManager is responsible for the lifecycle of threads, primarily maintaining
// the critical, high-performance TID -> ProcessData mapping.
type ThreadManager struct {
	tidToCurrentProcessData maps.ConcurrentMap[uint32, *ProcessData]
	terminatedThreads       maps.ConcurrentMap[uint32, *threadTerminationRecord] // key: TID (uint32)

	// Collaborators
	processManager *ProcessManager
	serviceManager *ServiceManager

	// Back-reference to the orchestrator to access shared state like the scrape counter.
	stateManager *KernelStateManager

	log *phusluadapter.SampledLogger
}

// newThreadManager creates and initializes a new ThreadManager.
func newThreadManager(log *phusluadapter.SampledLogger) *ThreadManager {
	return &ThreadManager{
		log:                     log,
		tidToCurrentProcessData: maps.NewConcurrentMap[uint32, *ProcessData](),
		terminatedThreads:       maps.NewConcurrentMap[uint32, *threadTerminationRecord](),
	}
}

// setStateManager sets the reference to the KernelStateManager to resolve circular dependencies
// and allow access to shared state.
func (tm *ThreadManager) setStateManager(sm *KernelStateManager) {
	tm.stateManager = sm
}

// setProcessManager sets the reference to the ProcessManager to resolve circular dependencies.
func (tm *ThreadManager) setProcessManager(pm *ProcessManager) {
	tm.processManager = pm
}

// setServiceManager sets the reference to the ServiceManager.
func (tm *ThreadManager) setServiceManager(sm *ServiceManager) {
	tm.serviceManager = sm
}

// AddThread adds or updates a TID -> ProcessData mapping.
func (tm *ThreadManager) AddThread(tid, pid uint32, eventTimestamp time.Time, subProcessTag uint32) {
	if pData, ok := tm.processManager.GetCurrentProcessDataByPID(pid); ok {
		pData.Info.mu.Lock()
		createTime := pData.Info.CreateTime
		pData.Info.mu.Unlock()

		// This check remains as a critical guard against PID reuse. If a thread's timestamp
		// is older than its parent process, something is fundamentally wrong.
		if eventTimestamp.After(createTime.Add(-time.Millisecond)) {
			// Store the TID -> *ProcessData mapping directly. This is the successful hot path.
			tm.tidToCurrentProcessData.Store(tid, pData)
			GlobalDiagnostics.RecordThreadAdded()

			// --- Service Name Attribution ---
			// so we can't resolve service name until the thread becomes active.
			tm.serviceManager.resolveAndApplyServiceName(pData, subProcessTag)

			tm.log.Trace().Uint32("tid", tid).
				Uint32("pid", pid).
				Uint64("start_key", pData.Info.StartKey).
				Msg("Thread added and associated with process")
			return // Success.
		}

		// ERROR CASE: The thread's timestamp is BEFORE the current process's create time.
		// This indicates a PID was reused faster than we could process the events.
		// This should be rare and is now treated as an error.
		tm.log.Error().
			Uint32("tid", tid).
			Uint32("pid", pid).
			Time("thread_time", eventTimestamp).
			Time("process_create_time", createTime).
			Msg("ERROR: Thread event arrived for a reused PID before the old process was terminated. Attributing to unknown.")
	} else {
		// ERROR CASE: If we reach here, the parent process (by PID) was not found in the state manager.
		tm.log.Error().
			Uint32("tid", tid).
			Uint32("pid", pid).
			Msg("ERROR: Parent process not found for thread. Attributing to unknown.")
	}

	// As a fallback, associate the thread with the "unknown_process" singleton
	// to ensure its metrics are not lost entirely.
	if unknownPData, ok := tm.processManager.GetProcessDataBySK(UnknownProcessStartKey); ok {
		tm.tidToCurrentProcessData.Store(tid, unknownPData)
	}
}

// MarkThreadForDeletion marks a thread to be cleaned up after the next scrape.
func (tm *ThreadManager) MarkThreadForDeletion(tid uint32) {
	currentScrape := tm.stateManager.scrapeCounter.Load()
	// To handle TID reuse correctly, we must capture the StartKey that this
	// thread instance was associated with at the moment of termination.
	if pData, ok := tm.tidToCurrentProcessData.Load(tid); ok {
		tm.terminatedThreads.Store(tid, &threadTerminationRecord{
			TerminatedAtGeneration: currentScrape,
			StartKey:               pData.Info.StartKey,
		})
		tm.log.Trace().Uint32("tid", tid).Uint64("start_key", pData.Info.StartKey).Msg("Thread marked for deletion")
	} else {
		// This can happen if a thread terminates that we were not tracking (e.g. from rundown).
		tm.log.Trace().Uint32("tid", tid).Msg("Ignoring termination for untracked thread")
	}
}

// GetCurrentProcessDataByThread resolves a TID to its parent's *ProcessData object.
func (tm *ThreadManager) GetCurrentProcessDataByThread(tid uint32) (*ProcessData, bool) {
	return tm.tidToCurrentProcessData.Load(tid)
}

// cleanupTerminated handles the cleanup for threads that were terminated independently of a process.
func (tm *ThreadManager) cleanupTerminated(cleanupThreshold uint64) (cleanedThreads int) {
    threadsToClean := make(map[uint32]uint64) // map[TID]StartKey
    tm.terminatedThreads.Range(func(tid uint32, record *threadTerminationRecord) bool {
        if record.TerminatedAtGeneration < cleanupThreshold {
            threadsToClean[tid] = record.StartKey
            tm.terminatedThreads.Delete(tid)
        }
        return true
    })

    for tid, startKeyToClean := range threadsToClean {
		// Atomically remove the tid -> pData mapping, but ONLY if it still
		// points to a process with the key we are cleaning up. If the TID has been reused,
		// this condition will be false, and we will not touch the new mapping.
		tm.tidToCurrentProcessData.Update(tid, func(currentPData *ProcessData, exists bool) (*ProcessData, bool) {
			if exists && currentPData.Info.StartKey == startKeyToClean {
				return nil, false // Value matches, so delete the entry.
			}
			return currentPData, true // Value does not match (TID reused), so keep the new entry.
		})
		cleanedThreads++
	}
	return
}

// cleanupThreadsForTerminatedProcesses removes all threads associated with a given set of terminated processes.
func (tm *ThreadManager) cleanupThreadsForTerminatedProcesses(procsToClean map[uint64]struct{}) {
	tidsForDeletion := make([]uint32, 0, 128)
	tm.tidToCurrentProcessData.Range(func(tid uint32, pData *ProcessData) bool {
		if _, isTerminated := procsToClean[pData.Info.StartKey]; isTerminated {
			tidsForDeletion = append(tidsForDeletion, tid)
		}
		return true
	})
	if len(tidsForDeletion) > 0 {
		for _, tid := range tidsForDeletion {
			tm.tidToCurrentProcessData.Delete(tid)
			tm.terminatedThreads.Delete(tid) // Also remove from other terminated list
		}
	}
}

// --- Debug Provider Methods ---

// GetThreadCount returns the current number of tracked threads.
func (tm *ThreadManager) GetThreadCount() int {
	var count int
	tm.tidToCurrentProcessData.Range(func(u uint32, u2 *ProcessData) bool {
		count++
		return true
	})
	return count
}

// GetTerminatedCount returns the number of threads marked for termination.
func (tm *ThreadManager) GetTerminatedCount() (threads int) {
	tm.terminatedThreads.Range(func(u uint32, r *threadTerminationRecord) bool {
		threads++
		return true
	})
	return
}

// RangeThreads iterates over threads for the debug http handler.
func (tm *ThreadManager) RangeThreads(f func(tid uint32, startKey uint64)) {
	tm.tidToCurrentProcessData.Range(func(tid uint32, pData *ProcessData) bool {
		f(tid, pData.Info.StartKey)
		return true
	})
}
