// This file defines the KernelStateManager, the public-facing facade for all
// kernel state operations. It orchestrates the ProcessManager and SystemState components
// to provide a unified, thread-safe API for the rest of the application.
package statemanager

import (
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"etw_exporter/internal/config"
	"etw_exporter/internal/logger"

	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

const (
	// UnknownProcessStartKey is a reserved key for the singleton "unknown process" instance.
	// Threads whose parent process is not found within the timeout are attributed to this key.
	UnknownProcessStartKey uint64 = 1

	// SystemProcessID is the well-known process ID for the Windows System process.
	SystemProcessID uint32 = 4
)

// KernelStateManager is the single source of truth for kernel entity state.
// It manages the lifecycle of processes and threads, ensuring data consistency
// and handling cleanup logic in a centralized, thread-safe manner. It is designed
// as a singleton to be accessible by all collectors and handlers that require
// kernel state information. It holds and orchestrates the specialized managers
// for processes, threads, images, and services.
type KernelStateManager struct {
	Processes *ProcessManager
	Threads   *ThreadManager
	Images    *ImageManager
	Services  *ServiceManager

	// WARM PATH: Aggregation state remains at the top level as it needs
	// access to both managers.
	aggregatedData      map[ProgramAggregationKey]*AggregatedProgramMetrics
	aggregationMu       sync.Mutex
	aggregationDataPool sync.Pool
	scrapeCounter       atomic.Uint64

	lastScrapeTime      time.Time
	lastScrapeTimeMutex sync.Mutex

	log *phusluadapter.SampledLogger
}

// GetProcessCount returns the current number of tracked processes.
func (sm *KernelStateManager) GetProcessCount() int {
	return sm.Processes.GetProcessCount()
}

// GetThreadCount returns the current number of tracked threads.
func (sm *KernelStateManager) GetThreadCount() int {
	return sm.Threads.GetThreadCount()
}

var (
	globalStateManager *KernelStateManager
	initOnce           sync.Once
)

// GetGlobalStateManager returns the singleton KernelStateManager instance, ensuring
// that all parts of the application share the same state information.
func GetGlobalStateManager() *KernelStateManager {
	initOnce.Do(func() {
		log := logger.NewSampledLoggerCtx("state_manager")

		// 1. Create the individual components.
		pm := newProcessManager(log)
		tm := newThreadManager(log)
		im := newImageManager(log)
		sm := newServiceManager(log)

		// 2. Create the orchestrator.
		ksm := &KernelStateManager{
			log:            log,
			Processes:      pm,
			Threads:        tm,
			Images:         im,
			Services:       sm,
			lastScrapeTime: time.Now(), // Initialize to startup time.
			aggregationDataPool: sync.Pool{
				New: func() any {
					return make(map[ProgramAggregationKey]*AggregatedProgramMetrics)
				},
			},
		}

		// 3. Wire up dependencies between managers, including the back-reference.
		pm.setStateManager(ksm)
		tm.setStateManager(ksm)
		im.setStateManager(ksm)
		// sm does not currently need a back-reference.

		tm.setProcessManager(pm)
		tm.setServiceManager(sm)
		im.setProcessManager(pm)

		GlobalDiagnostics.Init()
		globalStateManager = ksm
	})
	return globalStateManager
}

// ApplyConfig applies collector configuration to the state manager.
// This is where regex patterns for process filtering are compiled.
func (sm *KernelStateManager) ApplyConfig(cfg *config.CollectorConfig) {
	sm.Processes.ApplyConfig(cfg)
	// In the future, if other managers need configuration, it can be applied here.
}

func (sm *KernelStateManager) EnableProcessManager(enable bool) {
	sm.Processes.Enabled = enable
}

// --- Coordinated Lifecycle Management ---

// PostScrapeCleanup performs the actual deletion of entities that were marked for termination.
// This is called by the metrics HTTP handler AFTER a Prometheus scrape is complete.
func (sm *KernelStateManager) PostScrapeCleanup() {
	// "Pet" the watchdog by updating the last scrape time.
	sm.updateLastScrapeTime()

	// Report diagnostic statistics for the completed scrape interval.
	GlobalDiagnostics.ReportTrackedProcToConsole(sm.GetProcessCount())

	// Increment the scrape counter to mark the start of a new generation.
	// Any entity terminated during the previous scrape cycle will have a value less than this.
	currentScrape := sm.scrapeCounter.Add(1)

	sm.log.Debug().Uint64("generation", currentScrape).Msg("Starting post-scrape cleanup of terminated entities")

	// An entity is only cleaned up if it was terminated in a *previous* scrape generation.
	procsToClean := make(map[uint64]struct{}) // Use a simple set of StartKeys.
	sm.Processes.terminatedProcesses.Range(func(startKey uint64, terminatedAtGeneration uint64) bool {
		if terminatedAtGeneration < currentScrape {
			procsToClean[startKey] = struct{}{}
			// It's safe to delete while ranging over this specific concurrent map implementation.
			sm.Processes.terminatedProcesses.Delete(startKey)
		}
		return true
	})

	var cleanedProcs int
	if len(procsToClean) > 0 {
		// New single-line diagnostic log for cleaned processes.
		cleanedProcsDiag := make([]string, 0, len(procsToClean))

		// Log detailed trace info for each process being cleaned.
		for sk := range procsToClean {
			if pData, exists := sm.Processes.GetProcessDataBySK(sk); exists {
				// Detailed, per-process log is now at Trace level.
				sm.log.Trace().
					Uint64("start_key", pData.Info.StartKey).
					Uint32("pid", pData.Info.PID).
					Str("name", pData.Info.Name).
					Msg("Process selected for cleanup")

				// Append to the list for the summary diagnostic.
				cleanedProcsDiag = append(cleanedProcsDiag,
					fmt.Sprintf("%d-%d-%s", pData.Info.StartKey, pData.Info.PID, filepath.Base(pData.Info.Name)))
			}
		}

		// Log the new summary diagnostic at Debug level.
		sort.Strings(cleanedProcsDiag)
		sm.log.Debug().
			Strs("cleaned_processes_in_interval", cleanedProcsDiag).
			Msg("Processes cleaned up in this interval")

		// Clean up all threads that belonged to the terminated processes.
		// This must happen BEFORE the process data is deleted.
		sm.Threads.cleanupThreadsForTerminatedProcesses(procsToClean)

		// Now that dependents are gone, clean up the process objects themselves.
		cleanedProcs = sm.Processes.cleanupProcesses(procsToClean)
	}

	// Cleanup for individually terminated threads and images.
	cleanedThreads := sm.Threads.cleanupTerminated(currentScrape)
	cleanedImages := sm.Images.cleanupTerminated(currentScrape)

	if cleanedProcs > 0 || cleanedThreads > 0 || cleanedImages > 0 {
		sm.log.Debug().Int("processes", cleanedProcs).
			Int("threads", cleanedThreads).
			Int("images", cleanedImages).
			Msg("Post-scrape cleanup complete")
	}
}

// updateLastScrapeTime records the current time as the last successful scrape.
// This is called by PostScrapeCleanup.
func (sm *KernelStateManager) updateLastScrapeTime() {
	sm.lastScrapeTimeMutex.Lock()
	defer sm.lastScrapeTimeMutex.Unlock()
	sm.lastScrapeTime = time.Now()
}

// GetTimeSinceLastScrape returns the duration since the last scrape completed.
// This is used by the watchdog to detect a stale state.
func (sm *KernelStateManager) GetTimeSinceLastScrape() time.Duration {
	sm.lastScrapeTimeMutex.Lock()
	defer sm.lastScrapeTimeMutex.Unlock()
	return time.Since(sm.lastScrapeTime)
}

// --- Public API (All direct calls should go to sub-managers) ---
// The facade methods have been removed. Use GetGlobalStateManager().Processes.*,
// GetGlobalStateManager().Threads.*, etc. to access state management functions.

// --- Event Attribution Methods ---
// This method remains here as it coordinates between managers.
func (sm *KernelStateManager) RecordHardPageFaultForThread(tid, pid uint32) {
	// This logic is now self-contained within the ProcessData object and its modules.
	// The call finds the process and then calls a method on it.
	if pData, ok := sm.Threads.GetCurrentProcessDataByThread(tid); ok {
		pData.RecordHardPageFault()
		return
	}

	// Fallback for all error cases.
	if unknownProc, unknownOk := sm.Processes.GetProcessDataBySK(UnknownProcessStartKey); unknownOk {
		unknownProc.RecordHardPageFault()
	}
}

// --- Debug Provider Facade Methods ---
// These methods remain to provide a unified interface for the debug endpoint.

func (sm *KernelStateManager) GetTerminatedCounts() (procs, threads, images int) {
	procs = sm.Processes.GetTerminatedProcessCount()
	threads = sm.Threads.GetTerminatedCount()
	images = sm.Images.GetTerminatedCount()
	return
}

func (sm *KernelStateManager) GetImageCount() int {
	return sm.Images.GetImageCount()
}

func (sm *KernelStateManager) GetServiceCount() int {
	return sm.Services.GetServiceCount()
}

func (sm *KernelStateManager) RangeServices(f func(tag uint32, name string)) {
	sm.Services.RangeServices(f)
}

func (sm *KernelStateManager) RangeImages(f func(info *ImageInfo)) {
	sm.Images.RangeImages(f)
}

func (sm *KernelStateManager) RangeThreads(f func(tid uint32, startKey uint64)) {
	sm.Threads.RangeThreads(f)
}
