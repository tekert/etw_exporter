package statemanager

import (
	"regexp"
	"sync"
	"time"

	"etw_exporter/internal/config"
	"etw_exporter/internal/logger"

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
	processes           sync.Map // key: uint32 (PID), value: *ProcessInfo
	terminatedProcesses sync.Map // key: uint32 (PID), value: time.Time

	// Process Start Key state for robust tracking
	startKeyToPids sync.Map // key: uint64 (startKey), value: *sync.Map (of PIDs -> struct{})
	pidToStartKey  sync.Map // key: uint32 (PID), value: uint64 (startKey)

	// Thread state
	tidToPid          sync.Map // key: TID (uint32), value: PID (uint32)
	pidToTids         sync.Map // key: PID (uint32), value: *sync.Map (of TIDs -> struct{})
	terminatedThreads sync.Map // key: TID (uint32), value: time.Time

	// Process filtering state
	processFilterEnabled bool
	processNameFilters   []*regexp.Regexp
	trackedStartKeys     sync.Map // key: uint64 (startKey), value: struct{}

	log *phusluadapter.SampledLogger
	mu  sync.Mutex // Protects cleanup logic
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
			log: logger.NewSampledLoggerCtx("state_manager"),
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

	var cleanedCount int

	// 1. Clean up processes that were marked for deletion.
	sm.terminatedProcesses.Range(func(key, value any) bool {
		pid := key.(uint32)
		sm.processes.Delete(pid)
		sm.terminatedProcesses.Delete(pid)
		sm.removeProcessStartKeyMapping(pid) // Clean up start key mapping
		cleanedCount++

		// 2. Atomically clean up all threads associated with the terminated process.
		if val, exists := sm.pidToTids.Load(pid); exists {
			tidSet := val.(*sync.Map)
			tidSet.Range(func(key, value any) bool {
				tid := key.(uint32)
				sm.tidToPid.Delete(tid)
				sm.terminatedThreads.Delete(tid) // Also remove from terminated list if present
				return true
			})
			sm.pidToTids.Delete(pid)
		}
		return true
	})

	// 3. Clean up individually terminated threads.
	sm.terminatedThreads.Range(func(key, value any) bool {
		tid := key.(uint32)
		if val, exists := sm.tidToPid.Load(tid); exists {
			pid := val.(uint32)
			if pVal, pExists := sm.pidToTids.Load(pid); pExists {
				pVal.(*sync.Map).Delete(tid)
			}
		}
		sm.tidToPid.Delete(tid)
		sm.terminatedThreads.Delete(tid)
		cleanedCount++
		return true
	})

	if cleanedCount > 0 {
		sm.log.Debug().Int("count", cleanedCount).Msg("Post-scrape cleanup of terminated processes complete")
	}
}

// ForceCleanupOldEntries is a safety net to prevent memory leaks if scrapes stop.
// It removes any process or thread that was marked for deletion more than the maxAge ago.
// This is called periodically by a background goroutine in the SessionManager.
func (sm *KernelStateManager) ForceCleanupOldEntries(maxAge time.Duration) {
	cutoff := time.Now().Add(-maxAge)
	var cleanedProcs, cleanedThreads int

	// Force clean old terminated processes (and their threads)
	sm.terminatedProcesses.Range(func(key, value any) bool {
		if value.(time.Time).Before(cutoff) {
			pid := key.(uint32)
			sm.processes.Delete(pid)
			sm.terminatedProcesses.Delete(pid)
			sm.removeProcessStartKeyMapping(pid) // Clean up start key mapping
			cleanedProcs++

			// Cascade delete to threads
			if val, exists := sm.pidToTids.Load(pid); exists {
				val.(*sync.Map).Range(func(k, v any) bool {
					sm.tidToPid.Delete(k.(uint32))
					return true
				})
				sm.pidToTids.Delete(pid)
			}
		}
		return true
	})

	// Force clean old individually terminated threads
	sm.terminatedThreads.Range(func(key, value any) bool {
		if value.(time.Time).Before(cutoff) {
			sm.removeThread(key.(uint32))
			sm.terminatedThreads.Delete(key)
			cleanedThreads++
		}
		return true
	})

	if cleanedProcs > 0 || cleanedThreads > 0 {
		sm.log.Warn().
			Int("processes", cleanedProcs).
			Int("threads", cleanedThreads).
			Msg("Forcibly cleaned up stale entries older than max age (scrape likely stopped)")
	}
}

// CleanupStaleProcesses marks processes that have not been seen for a specified duration for deletion.
// This is used in conjunction with ETW rundown events to detect processes that terminated
// while the exporter was not running or if a ProcessEnd event was missed.
func (sm *KernelStateManager) CleanupStaleProcesses(lastSeenIn time.Duration) {
	cutoff := time.Now().Add(-lastSeenIn)
	var pidsToMark []uint32
	sm.processes.Range(func(key, value any) bool {
		info := value.(*ProcessInfo)
		info.mu.Lock()
		lastSeen := info.LastSeen
		info.mu.Unlock()
		if lastSeen.Before(cutoff) {
			pidsToMark = append(pidsToMark, key.(uint32))
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
	if val, exists := sm.tidToPid.Load(tid); exists {
		pid := val.(uint32)
		if pVal, pExists := sm.pidToTids.Load(pid); pExists {
			pVal.(*sync.Map).Delete(tid)
		}
	}
	sm.tidToPid.Delete(tid)
}

// removeProcessStartKeyMapping cleans up the PID <-> StartKey mappings.
// If a StartKey no longer has any associated PIDs, the key itself is removed.
func (sm *KernelStateManager) removeProcessStartKeyMapping(pid uint32) {
	if skVal, skExists := sm.pidToStartKey.Load(pid); skExists {
		startKey := skVal.(uint64)
		sm.pidToStartKey.Delete(pid)
		sm.trackedStartKeys.Delete(startKey) // Also remove from tracked keys

		if pidsVal, pidsExist := sm.startKeyToPids.Load(startKey); pidsExist {
			pidsMap := pidsVal.(*sync.Map)
			pidsMap.Delete(pid)

			// Check if the map of PIDs for this start key is now empty.
			isEmpty := true
			pidsMap.Range(func(k, v any) bool {
				isEmpty = false
				return false // Stop iteration
			})
			if isEmpty {
				sm.startKeyToPids.Delete(startKey)
			}
		}
	}
}
