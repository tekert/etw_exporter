package statemanager

import (
	"regexp"
	"sync"
	"time"

	"etw_exporter/internal/config"
	"etw_exporter/internal/logger"
	"etw_exporter/internal/maps"

	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// KernelStateManager is the single source of truth for kernel entity state.
// It manages the lifecycle of processes and threads, ensuring data consistency
// and handling cleanup logic in a centralized, thread-safe manner. It is designed
// as a singleton to be accessible by all collectors and handlers that require
// kernel state information.
type KernelStateManager struct {
	// Process state
	instanceData        maps.ConcurrentMap[uint64, *ProcessData] // key: uint64 (StartKey), value: *ProcessData
	terminatedProcesses maps.ConcurrentMap[uint64, time.Time]    // key: uint64 (StartKey), value: time.Time

	// WARM PATH: A store for aggregated metrics, prepared on-demand for Prometheus.
	// This is populated by the AggregateMetrics method just before a scrape.
	// Collectors read from this map.
	aggregatedData      map[ProgramAggregationKey]*AggregatedProgramMetrics
	aggregationMu       sync.Mutex // Protects the aggregatedData map during swaps.
	aggregationDataPool sync.Pool

	// Process Start Key state for robust tracking
	startKeyToPid        maps.ConcurrentMap[uint64, uint32] // key: uint64 (startKey), value: uint32 (PID)
	pidToCurrentStartKey maps.ConcurrentMap[uint32, uint64] // key: uint32 (PID), value: uint64 (current StartKey)

	// Thread state
	tidToPid maps.ConcurrentMap[uint32, uint32]
	// The reverse mapping (pid -> []tids) has been removed to eliminate allocations on the hot path.
	// Cleanup is now handled by iterating tidToPid on the cold path (post-scrape).
	terminatedThreads maps.ConcurrentMap[uint32, time.Time] // key: TID (uint32), value: time.Time

	processDataPool sync.Pool // Pool for reusing fully-formed ProcessData objects.

	// Unified image state store.
	// This uses a simple map protected by an RWMux. This provides a balance
	// of performance for both writes (image loads) and reads (address lookups)
	// while being simple and avoiding heap allocations on the read path.
	images             map[uint64]*ImageInfo
	addressCache       map[uint64]*ImageInfo // Point-address cache for hot-path lookups
	imagesMutex        sync.RWMutex
	terminatedImages   maps.ConcurrentMap[uint64, time.Time]
	imageInfoPool      sync.Pool
	internedImageNames map[string]string
	internMutex        sync.Mutex

	// Process filtering state
	processFilterEnabled bool
	processNameFilters   []*regexp.Regexp

	log *phusluadapter.SampledLogger
	// The global mutex has been removed in favor of a simpler, more robust
	// rundown-driven cleanup model.
}

var (
	globalStateManager *KernelStateManager
	initOnce           sync.Once
)

// GetGlobalStateManager returns the singleton KernelStateManager instance, ensuring
// that all parts of the application share the same state information.
func GetGlobalStateManager() *KernelStateManager {
	initOnce.Do(func() {
		globalStateManager = &KernelStateManager{
			log:                 logger.NewSampledLoggerCtx("state_manager"),
			instanceData:        maps.NewConcurrentMap[uint64, *ProcessData](),
			terminatedProcesses: maps.NewConcurrentMap[uint64, time.Time](),
			aggregationDataPool: sync.Pool{
				New: func() any {
					return make(map[ProgramAggregationKey]*AggregatedProgramMetrics)
				},
			},
			pidToCurrentStartKey: maps.NewConcurrentMap[uint32, uint64](),
			startKeyToPid:        maps.NewConcurrentMap[uint64, uint32](),
			tidToPid:             maps.NewConcurrentMap[uint32, uint32](),
			terminatedThreads:    maps.NewConcurrentMap[uint32, time.Time](),
			images:               make(map[uint64]*ImageInfo),
			addressCache:         make(map[uint64]*ImageInfo, 4096), // Pre-size the cache
			terminatedImages:     maps.NewConcurrentMap[uint64, time.Time](),
			imageInfoPool: sync.Pool{
				New: func() any {
					return new(ImageInfo)
				},
			},
			processDataPool: sync.Pool{
				New: func() any {
					// This makes the hot-path lock-free
					// and allocation-free, and stabilizes memory usage.
					return &ProcessData{
						Disk:     newDiskModule(),
						Memory:   newMemoryModule(),
						Network:  newNetworkModule(),
						Registry: new(RegistryModule),
						Threads:  newThreadModule(),
					}
				},
			},
			internedImageNames: make(map[string]string),
		}
	})
	return globalStateManager
}

// ApplyConfig applies collector configuration to the state manager.
// This is where regex patterns for process filtering are compiled.
func (sm *KernelStateManager) ApplyConfig(cfg *config.CollectorConfig) {
	sm.processFilterEnabled = cfg.ProcessFilter.Enabled
	if !sm.processFilterEnabled {
		sm.log.Debug().Msg("Process filtering is disabled. All processes will be tracked.")
		return
	}

	sm.processNameFilters = make([]*regexp.Regexp, 0, len(cfg.ProcessFilter.IncludeNames))
	for _, pattern := range cfg.ProcessFilter.IncludeNames {
		re, err := regexp.Compile(pattern)
		if err != nil {
			sm.log.Error().Err(err).Str("pattern", pattern).Msg("Failed to compile process name filter regex, pattern will be ignored.")
			continue
		}
		sm.processNameFilters = append(sm.processNameFilters, re)
	}
	sm.log.Info().Int("patterns", len(sm.processNameFilters)).Msg("Process filtering enabled with patterns.")
}

// --- Coordinated Lifecycle Management ---

// PostScrapeCleanup performs the actual deletion of entities that were marked for termination.
// This is called by the metrics HTTP handler AFTER a Prometheus scrape is complete.
func (sm *KernelStateManager) PostScrapeCleanup() {
	sm.log.Debug().Msg("Starting post-scrape cleanup of terminated entities")

	// Phase 1: Fast-path cleanup for explicitly terminated processes.
	procsToClean := make(map[uint64]uint32)
	sm.terminatedProcesses.Range(func(startKey uint64, termTime time.Time) bool {
		if pid, ok := sm.GetPIDFromStartKey(startKey); ok {
			procsToClean[startKey] = pid
		}
		// Always remove from the terminated map after processing.
		sm.terminatedProcesses.Delete(startKey)
		return true
	})

	var cleanedProcs, cleanedThreads, cleanedImages int
	if len(procsToClean) > 0 {
		cleanedProcs = sm.cleanupProcesses(procsToClean)
	}

	// Phase 2: Cleanup for individually terminated threads and images (fast path).
	cleanedThreads, cleanedImages = sm.cleanupIndividualEntities()

	if cleanedProcs > 0 || cleanedThreads > 0 || cleanedImages > 0 {
		sm.log.Debug().Int("processes", cleanedProcs).
			Int("threads", cleanedThreads).
			Int("images", cleanedImages).
			Msg("Post-scrape cleanup complete")
	}
}

// ForceCleanupOldEntries is a safety net to prevent memory leaks if scrapes stop.
// It removes any entity that was marked for deletion more than the maxAge ago.
// This is called periodically by a background goroutine in the SessionManager.
func (sm *KernelStateManager) ForceCleanupOldEntries(maxAge time.Duration) {
	sm.log.Debug().Dur("max_age", maxAge).Msg("Forcibly cleaning up old entries (scrape safety net)")
	// This function now serves as the ultimate safety net by sweeping for any
	// process that hasn't been seen in a very long time.
	sm.CleanupStaleProcesses(maxAge)
}

// cleanupIndividualEntities handles the fast-path cleanup for threads and images
// that were terminated independently of a process.
func (sm *KernelStateManager) cleanupIndividualEntities() (cleanedThreads, cleanedImages int) {
	// Clean up individually terminated threads.
	threadsToClean := make([]uint32, 0)
	sm.terminatedThreads.Range(func(tid uint32, _ time.Time) bool {
		threadsToClean = append(threadsToClean, tid)
		return true
	})
	for _, tid := range threadsToClean {
		sm.tidToPid.Delete(tid)
		sm.terminatedThreads.Delete(tid)
		cleanedThreads++
	}

	// Clean up individually unloaded images.
	imagesToClean := make([]uint64, 0)
	sm.terminatedImages.Range(func(imageBase uint64, _ time.Time) bool {
		imagesToClean = append(imagesToClean, imageBase)
		return true
	})
	for _, imageBase := range imagesToClean {
		sm.RemoveImage(imageBase) // RemoveImage also deletes from terminatedImages
		cleanedImages++
	}
	return
}

// cleanupProcesses is the centralized private helper for process deletion.
// It removes processes and their associated threads.
// It returns the number of processes that were cleaned.
func (sm *KernelStateManager) cleanupProcesses(procsToClean map[uint64]uint32) int {
	sm.log.Debug().Int("count", len(procsToClean)).Msg("Starting process cleanup...")
	if len(procsToClean) == 0 {
		return 0
	}

	// --- Cascade Deletion to Threads ---
	// Create a set of terminated PIDs for efficient lookup while scanning all threads.
	terminatedPids := make(map[uint32]struct{}, len(procsToClean))
	for _, pid := range procsToClean {
		terminatedPids[pid] = struct{}{}
	}

	// Iterate through all tracked threads and remove those belonging to terminated processes.
	// This is an O(T) operation (where T is total threads) performed on the cold path,
	// which is far better than allocating on the hot path for every thread creation.
	tidsForDeletion := make([]uint32, 0, 128)
	sm.tidToPid.Range(func(tid uint32, pid uint32) bool {
		if _, isTerminated := terminatedPids[pid]; isTerminated {
			tidsForDeletion = append(tidsForDeletion, tid)
		}
		return true
	})

	if len(tidsForDeletion) > 0 {
		for _, tid := range tidsForDeletion {
			sm.tidToPid.Delete(tid)
			sm.terminatedThreads.Delete(tid) // Also remove from other terminated list
		}
	}

	// --- Perform the state manager's own cleanup for the identified processes. ---
	for startKey, pid := range procsToClean {
		// Always remove the process info and its primary key mappings.
		if pData, exists := sm.instanceData.LoadAndDelete(startKey); exists {
			// --- Recycle the entire ProcessData object ---
			// The object is reset after it's retrieved from the pool in AddProcess
			sm.processDataPool.Put(pData)
		}
		sm.startKeyToPid.Delete(startKey)

		// Atomically remove the pid -> current mapping if it still points to the
		// key we are cleaning up. This handles cases where the process was terminated
		// implicitly by the stale cleanup mechanism.
		sm.pidToCurrentStartKey.Update(pid, func(currentSK uint64, exists bool) (uint64, bool) {
			if exists && currentSK == startKey {
				return 0, false // Value matches, so delete the entry.
			}
			return currentSK, true // Value does not match, keep it.
		})
	}

	// NOTE: Image cleanup is no longer cascaded from process termination.
	// Images are global entities and are only cleaned up on explicit unload events.
	return len(procsToClean)
}

// CleanupStaleProcesses is the safety net that sweeps for and removes any process
// that has not been seen for a specified duration. This is the authoritative
// cleanup mechanism that makes the system self-healing.
func (sm *KernelStateManager) CleanupStaleProcesses(maxAge time.Duration) {
	sm.log.Debug().Dur("max_age", maxAge).Msg("Checking for stale processes to clean up")

	cutoff := time.Now().Add(-maxAge)
	procsToClean := make(map[uint64]uint32)

	sm.instanceData.Range(func(sk uint64, pData *ProcessData) bool {
		pData.Info.mu.Lock()
		lastSeen := pData.Info.LastSeen
		pid := pData.Info.PID
		pData.Info.mu.Unlock()

		if lastSeen.Before(cutoff) {
			procsToClean[sk] = pid
		}
		return true
	})

	if len(procsToClean) > 0 {
		cleanedCount := sm.cleanupProcesses(procsToClean)
		sm.log.Debug().
			Int("stale_count", cleanedCount).
			Dur("max_age", maxAge).
			Msg("Cleaned up stale processes")
	}
}
