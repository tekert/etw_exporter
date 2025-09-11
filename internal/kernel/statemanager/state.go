package statemanager

import (
	"regexp"
	"sync"
	"time"

	"etw_exporter/internal/config"
	"etw_exporter/internal/logger"
	"etw_exporter/internal/maps"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// KernelStateManager is the single source of truth for kernel entity state.
// It manages the lifecycle of processes and threads, ensuring data consistency
// and handling cleanup logic in a centralized, thread-safe manner. It is designed
// as a singleton to be accessible by all collectors and handlers that require
// kernel state information.
type KernelStateManager struct {
	// Process state
	processes           maps.ConcurrentMap[uint32, *ProcessInfo] // key: uint32 (PID), value: *ProcessInfo
	terminatedProcesses maps.ConcurrentMap[uint32, time.Time]    // key: uint32 (PID), value: time.Time

	// Process Start Key state for robust tracking
	startKeyToPid maps.ConcurrentMap[uint64, uint32] // key: uint64 (startKey), value: uint32 (PID)
	pidToStartKey maps.ConcurrentMap[uint32, uint64] // key: uint32 (PID), value: uint64 (startKey)

	// Thread state
	tidToPid          maps.ConcurrentMap[uint32, uint32]                               // key: TID (uint32), value: PID (uint32)
	pidToTids         maps.ConcurrentMap[uint32, maps.ConcurrentMap[uint32, struct{}]] // key: PID (uint32), value: map of TIDs
	terminatedThreads maps.ConcurrentMap[uint32, time.Time]                            // key: TID (uint32), value: time.Time

	// Image state
	images           maps.ConcurrentMap[uint64, *ImageInfo]                           // key: uint64 (ImageBase), value: *ImageInfo
	pidToImages      maps.ConcurrentMap[uint32, maps.ConcurrentMap[uint64, struct{}]] // key: uint32 (PID), value: map of ImageBases
	imageToPid       maps.ConcurrentMap[uint64, uint32]                               // key: uint64 (ImageBase), value: uint32 (PID)
	terminatedImages maps.ConcurrentMap[uint64, time.Time]                            // key: uint64 (ImageBase), value: time.Time
	imageInfoPool    sync.Pool                                                        // Pool for recycling ImageInfo objects.

	// Process filtering state
	processFilterEnabled bool
	processNameFilters   []*regexp.Regexp
	trackedStartKeys     maps.ConcurrentMap[uint64, struct{}] // key: uint64 (startKey), value: struct{}

	cleaners []PostScrapeCleaner // Collectors that need post-scrape cleanup.
	log      *phusluadapter.SampledLogger
	mu       sync.Mutex // Protects cleanup logic and cleaners slice
}

var (
	globalStateManager *KernelStateManager
	initOnce           sync.Once
)

// PostScrapeCleaner is an interface for collectors that need to perform cleanup
// of their internal state after a Prometheus scrape is complete.
type PostScrapeCleaner interface {
	// CleanupTerminatedProcesses is called with a map of terminated processes,
	// where the key is the PID and the value is the unique Process Start Key.
	CleanupTerminatedProcesses(terminatedProcs map[uint32]uint64)
}

// GetGlobalStateManager returns the singleton KernelStateManager instance, ensuring
// that all parts of the application share the same state information.
func GetGlobalStateManager() *KernelStateManager {
	initOnce.Do(func() {
		globalStateManager = &KernelStateManager{
			log:                 logger.NewSampledLoggerCtx("state_manager"),
			cleaners:            make([]PostScrapeCleaner, 0),
			processes:           maps.NewConcurrentMap[uint32, *ProcessInfo](),
			terminatedProcesses: maps.NewConcurrentMap[uint32, time.Time](),
			pidToStartKey:       maps.NewConcurrentMap[uint32, uint64](),
			startKeyToPid:       maps.NewConcurrentMap[uint64, uint32](),
			trackedStartKeys:    maps.NewConcurrentMap[uint64, struct{}](),
			tidToPid:            maps.NewConcurrentMap[uint32, uint32](),
			pidToTids:           maps.NewConcurrentMap[uint32, maps.ConcurrentMap[uint32, struct{}]](),
			terminatedThreads:   maps.NewConcurrentMap[uint32, time.Time](),
			images:              maps.NewConcurrentMap[uint64, *ImageInfo](),
			pidToImages:         maps.NewConcurrentMap[uint32, maps.ConcurrentMap[uint64, struct{}]](),
			imageToPid:          maps.NewConcurrentMap[uint64, uint32](),
			terminatedImages:    maps.NewConcurrentMap[uint64, time.Time](),
			imageInfoPool: sync.Pool{
				New: func() any {
					return new(ImageInfo)
				},
			},
		}
	})
	return globalStateManager
}

// RegisterCleaner allows a collector to register for post-scrape cleanup notifications.
// This should be called once during collector initialization.
func (sm *KernelStateManager) RegisterCleaner(cleaner PostScrapeCleaner) {
	sm.mu.Lock()
	defer sm.mu.Unlock()
	sm.cleaners = append(sm.cleaners, cleaner)
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

// --- Sentinel Collector ---

// StateCleanupCollector is a sentinel collector that triggers cleanup after a scrape.
// Its sole purpose is to be registered last in the Prometheus registry. Its Collect
// method is called after all other collectors have finished, making it the perfect
// hook to perform cleanup of terminated entities without creating race conditions.
type StateCleanupCollector struct {
	sm *KernelStateManager
}

// NewStateCleanupCollector creates a new sentinel collector.
func NewStateCleanupCollector() *StateCleanupCollector {
	return &StateCleanupCollector{
		sm: GetGlobalStateManager(),
	}
}

// Describe does nothing. It's a sentinel collector.
func (c *StateCleanupCollector) Describe(ch chan<- *prometheus.Desc) {}

// Collect triggers the post-scrape cleanup in the KernelStateManager.
// This method is called by Prometheus during a scrape. By registering this
// collector last, we ensure cleanup happens after all other collectors are done.
func (c *StateCleanupCollector) Collect(ch chan<- prometheus.Metric) {
	c.sm.PostScrapeCleanup()
}

// --- Coordinated Lifecycle Management ---

// PostScrapeCleanup performs the actual deletion of processes and threads that were marked.
// This is called by the sentinel collector AFTER a Prometheus scrape is complete.
// The logic is carefully ordered to handle process termination cascades.
func (sm *KernelStateManager) PostScrapeCleanup() {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	// 1. Identify all processes marked for deletion and their start keys.
	procsToClean := make(map[uint32]uint64)
	sm.terminatedProcesses.Range(func(pid uint32, _ time.Time) bool {
		// Get the start key for the terminated process. It will be 0 if not found.
		startKey, _ := sm.GetProcessStartKey(pid)
		procsToClean[pid] = startKey
		return true
	})

	// 2. Execute the shared cleanup logic for the identified processes.
	cleanedProcs := sm.cleanupProcesses(procsToClean)

	// 3. Clean up individually terminated threads (that weren't part of a terminated process).
	var cleanedThreads int
	sm.terminatedThreads.Range(func(tid uint32, _ time.Time) bool {
		sm.removeThread(tid)
		sm.terminatedThreads.Delete(tid)
		cleanedThreads++
		return true
	})

	// 4. Clean up individually unloaded images.
	var cleanedImages int
	sm.terminatedImages.Range(func(imageBase uint64, _ time.Time) bool {
		sm.RemoveImage(imageBase)
		cleanedImages++
		return true
	})

	if cleanedProcs > 0 || cleanedThreads > 0 || cleanedImages > 0 {
		sm.log.Debug().Int("processes", cleanedProcs).Int("threads", cleanedThreads).Int("images", cleanedImages).Msg("Post-scrape cleanup complete")
	}
}

// ForceCleanupOldEntries is a safety net to prevent memory leaks if scrapes stop.
// It removes any process or thread that was marked for deletion more than the maxAge ago.
// This is called periodically by a background goroutine in the SessionManager.
// It follows the same cleanup protocol as PostScrapeCleanup, ensuring collectors are notified.
func (sm *KernelStateManager) ForceCleanupOldEntries(maxAge time.Duration) {
	sm.mu.Lock()
	defer sm.mu.Unlock()

	cutoff := time.Now().Add(-maxAge)
	procsToClean := make(map[uint32]uint64)

	// 1. Identify old terminated processes to be forcibly cleaned.
	sm.terminatedProcesses.Range(func(pid uint32, termTime time.Time) bool {
		if termTime.Before(cutoff) {
			startKey, _ := sm.GetProcessStartKey(pid)
			procsToClean[pid] = startKey
		}
		return true
	})

	// 2. Cleanup the identified processes.
	cleanedProcs := sm.cleanupProcesses(procsToClean)

	// 3. Force clean old individually terminated threads (that weren't part of a process cleanup).
	var cleanedThreads int
	sm.terminatedThreads.Range(func(tid uint32, termTime time.Time) bool {
		if termTime.Before(cutoff) {
			sm.removeThread(tid)
			sm.terminatedThreads.Delete(tid)
			cleanedThreads++
		}
		return true
	})

	// 4. Force clean old individually unloaded images.
	var cleanedImages int
	sm.terminatedImages.Range(func(imageBase uint64, termTime time.Time) bool {
		if termTime.Before(cutoff) {
			sm.RemoveImage(imageBase)
			cleanedImages++
		}
		return true
	})

	if cleanedProcs > 0 || cleanedThreads > 0 || cleanedImages > 0 {
		sm.log.Warn().
			Int("processes", cleanedProcs).
			Int("threads", cleanedThreads).
			Int("images", cleanedImages).
			Dur("max_age", maxAge).
			Msg("Forcibly cleaned up stale entries (scrape likely stopped)")
	}
}

// cleanupProcesses is the centralized private helper for process deletion.
// It notifies collectors and then removes processes and their associated threads.
// This method MUST be called with the state manager's mutex held.
// It returns the number of processes that were cleaned.
func (sm *KernelStateManager) cleanupProcesses(procsToClean map[uint32]uint64) int {
	if len(procsToClean) == 0 {
		return 0
	}

	// Notify registered collectors to clean up their state for these PIDs.
	// This happens BEFORE the state manager removes the process info, allowing
	// collectors to perform any final lookups if needed.
	for _, cleaner := range sm.cleaners {
		cleaner.CleanupTerminatedProcesses(procsToClean)
	}

	// Perform the state manager's own cleanup for the identified processes.
	for pid := range procsToClean {
		sm.processes.Delete(pid)
		sm.terminatedProcesses.Delete(pid)
		sm.removeProcessStartKeyMapping(pid) // Clean up start key mapping

		// Cascade delete to threads
		if tids, exists := sm.pidToTids.Load(pid); exists {
			tids.Range(func(tid uint32, _ struct{}) bool {
				sm.tidToPid.Delete(tid)
				sm.terminatedThreads.Delete(tid) // Also remove from other terminated list
				return true
			})
			sm.pidToTids.Delete(pid)
		}

		// Cascade delete to images loaded by this process
		if images, exists := sm.pidToImages.LoadAndDelete(pid); exists {
			images.Range(func(imageBase uint64, _ struct{}) bool {
				if imgInfo, loaded := sm.images.LoadAndDelete(imageBase); loaded {
					sm.imageInfoPool.Put(imgInfo)
				}
				sm.imageToPid.Delete(imageBase)
				return true
			})
		}

		// Cascade delete to FileObject mappings owned by this process
		// This is no longer needed with IRP correlation as IRPs are transient.
	}
	return len(procsToClean)
}

// CleanupStaleProcesses marks processes that have not been seen for a specified duration for deletion.
// This is used in conjunction with ETW rundown events to detect processes that terminated
// while the exporter was not running or if a ProcessEnd event was missed.
func (sm *KernelStateManager) CleanupStaleProcesses(lastSeenIn time.Duration) {
	cutoff := time.Now().Add(-lastSeenIn)
	var pidsToMark []uint32
	sm.processes.Range(func(pid uint32, info *ProcessInfo) bool {
		info.mu.Lock()
		lastSeen := info.LastSeen
		info.mu.Unlock()
		if lastSeen.Before(cutoff) {
			pidsToMark = append(pidsToMark, pid)
		}
		return true
	})

	if len(pidsToMark) > 0 {
		for _, pid := range pidsToMark {
			sm.MarkProcessForDeletion(pid)
		}
		sm.log.Debug().
			Int("stale_count", len(pidsToMark)).
			Dur("max_age", lastSeenIn).
			Msg("Marked stale processes for cleanup post-scrape")
	}
}

// removeThread is a private helper for thread cleanup that removes a thread's
// entries from both the forward (TID->PID) and reverse (PID->TIDs) maps.
func (sm *KernelStateManager) removeThread(tid uint32) {
	if pid, exists := sm.tidToPid.Load(tid); exists {
		if pVal, pExists := sm.pidToTids.Load(pid); pExists {
			pVal.Delete(tid)
		}
	}
	sm.tidToPid.Delete(tid)
}

// removeProcessStartKeyMapping cleans up the PID <-> StartKey mappings.
// If a StartKey no longer has any associated PIDs, the key itself is removed.
func (sm *KernelStateManager) removeProcessStartKeyMapping(pid uint32) {
	if startKey, skExists := sm.pidToStartKey.Load(pid); skExists {
		sm.pidToStartKey.Delete(pid)
		sm.startKeyToPid.Delete(startKey)    // Clean up the reverse mapping
		sm.trackedStartKeys.Delete(startKey) // Also remove from tracked keys
	}
}
