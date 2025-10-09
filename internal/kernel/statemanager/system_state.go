// This file defines the SystemState manager, a specialized data store for all state
// that is not specific to a single process instance. This includes system-wide resources
package statemanager

import (
	"sync"

	"etw_exporter/internal/maps"

	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// SystemState manages all state that is not specific to a single process instance.
// This includes the master thread map, all pending logic, the global image store,
// and other global OS-level resources.
type SystemState struct {
	// Thread state
	tidToCurrentProcessData maps.ConcurrentMap[uint32, *ProcessData]
	terminatedThreads       maps.ConcurrentMap[uint32, *threadTerminationRecord] // key: TID (uint32)

	// Service Tag state
	serviceTags maps.ConcurrentMap[uint32, string] // Key: SubProcessTag, Value: ServiceName

	// Unified image state store
	images             map[uint64]*ImageInfo
	addressCache       map[uint64]*ImageInfo // Point-address cache for hot-path lookups
	imagesMutex        sync.RWMutex
	terminatedImages   maps.ConcurrentMap[uint64, uint64] // key: ImageBase, value: scrape generation
	imageInfoPool      sync.Pool
	internedImageNames map[string]string
	internMutex        sync.Mutex

	// Collaborators

	processManager *ProcessManager // Reference to look up process data

	log *phusluadapter.SampledLogger
}

// newSystemState creates and initializes a new SystemState manager.
func newSystemState(log *phusluadapter.SampledLogger) *SystemState {
	ss := &SystemState{
		log:                     log,
		tidToCurrentProcessData: maps.NewConcurrentMap[uint32, *ProcessData](),
		terminatedThreads:       maps.NewConcurrentMap[uint32, *threadTerminationRecord](),
		serviceTags:             maps.NewConcurrentMap[uint32, string](),
		images:                  make(map[uint64]*ImageInfo),
		addressCache:            make(map[uint64]*ImageInfo, 4096),
		terminatedImages:        maps.NewConcurrentMap[uint64, uint64](),
		imageInfoPool: sync.Pool{
			New: func() any { return new(ImageInfo) },
		},
		internedImageNames: make(map[string]string),
	}

	return ss
}

// setProcessManager sets the reference to the ProcessManager component to resolve circular dependencies.
func (ss *SystemState) setProcessManager(pm *ProcessManager) {
	ss.processManager = pm
}

// --- Event Attribution ---

// RecordHardPageFaultForThread ensures that every hard page fault is attributed to the correct process.
// If the thread is not found, the metric is attributed to "unknown_process".
func (ss *SystemState) RecordHardPageFaultForThread(tid, pid uint32) {
	// --- Path 1: Attempt to resolve by Thread ID (The only path) ---
	if pData, ok := ss.GetCurrentProcessDataByThread(tid); ok {
		// Happy path: TID and process data resolved instantly.
		pData.RecordHardPageFault()
		return
	}

	// This is an error: a metric event arrived for a thread we don't know about.
	ss.log.Error().
		Uint32("tid", tid).
		Uint32("pid_from_header", pid).
		Msg("ERROR: Hard fault event for unknown thread. Attributing to unknown.")

	// Fallback for all error cases.
	if unknownProc, unknownOk := ss.processManager.GetProcessDataBySK(UnknownProcessStartKey); unknownOk {
		unknownProc.RecordHardPageFault()
	}
}

// --- Cleanup ---

// cleanupCachedEntities handles the fast-path cleanup for threads and images
// that were terminated independently of a process.
func (ss *SystemState) cleanupCachedEntities(currentGeneration uint64) (cleanedThreads, cleanedImages int) {
	// Clean up individually terminated threads that are past their grace period.
	threadsToClean := make(map[uint32]uint64) // map[TID]StartKey
	ss.terminatedThreads.Range(func(tid uint32, record *threadTerminationRecord) bool {
		if record.TerminatedAtGeneration < currentGeneration {
			threadsToClean[tid] = record.StartKey
			// Safe to delete from the concurrent map while iterating.
			ss.terminatedThreads.Delete(tid)
		}
		return true
	})

	for tid, startKeyToClean := range threadsToClean {
		// Atomically remove the tid -> pData mapping, but ONLY if it still
		// points to a process with the key we are cleaning up. If the TID has been reused,
		// this condition will be false, and we will not touch the new mapping.
		ss.tidToCurrentProcessData.Update(tid, func(currentPData *ProcessData, exists bool) (*ProcessData, bool) {
			if exists && currentPData.Info.StartKey == startKeyToClean {
				return nil, false // Value matches, so delete the entry.
			}
			return currentPData, true // Value does not match (TID reused), so keep the new entry.
		})
		cleanedThreads++
	}

	// Clean up individually unloaded images.
	imagesToClean := make([]uint64, 0)
	ss.terminatedImages.Range(func(imageBase uint64, terminatedAtGeneration uint64) bool {
		if terminatedAtGeneration < currentGeneration {
			imagesToClean = append(imagesToClean, imageBase)
		}
		return true
	})
	for _, imageBase := range imagesToClean {
		ss.RemoveImage(imageBase) // RemoveImage also deletes from terminatedImages
		cleanedImages++
	}
	return
}

// cleanupCachedThreads removes all threads associated with the given set past terminated processes.
func (ss *SystemState) cleanupCachedThreads(procsToClean map[uint64]struct{}) {
	tidsForDeletion := make([]uint32, 0, 128)
	ss.tidToCurrentProcessData.Range(func(tid uint32, pData *ProcessData) bool {
		if _, isTerminated := procsToClean[pData.Info.StartKey]; isTerminated {
			tidsForDeletion = append(tidsForDeletion, tid)
		}
		return true
	})
	if len(tidsForDeletion) > 0 {
		for _, tid := range tidsForDeletion {
			ss.tidToCurrentProcessData.Delete(tid)
			ss.terminatedThreads.Delete(tid) // Also remove from other terminated list
		}
	}
}

// --- Debug Provider Methods ---

// GetTerminatedThreadAndImageCounts returns the number of threads and images marked for termination.
func (ss *SystemState) GetTerminatedThreadAndImageCounts() (threads, images int) {
	ss.terminatedThreads.Range(func(u uint32, r *threadTerminationRecord) bool {
		threads++
		return true
	})
	ss.terminatedImages.Range(func(u uint64, u2 uint64) bool {
		images++
		return true
	})
	return
}
