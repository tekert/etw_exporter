// This file defines the KernelStateManager, the public-facing facade for all
// kernel state operations. It orchestrates the ProcessManager and SystemState components
// to provide a unified, thread-safe API for the rest of the application.
package statemanager

import (
	"fmt"
	"math"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"etw_exporter/internal/config"
	"etw_exporter/internal/logger"
	"etw_exporter/internal/windowsapi"

	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

const (
	// UnknownProcessStartKey is a reserved key for the singleton "unknown process" instance.
	// Threads whose parent process is not found within the timeout are attributed to this key.
	UnknownProcessStartKey uint64 = 1

	// SystemProcessID is the well-known process ID for the Windows System process.
	SystemProcessID uint32 = 4

	// The number of recent scrape intervals to average for calculating the dynamic grace period.
	scrapeDurationSamples = 4
	// The minimum grace period an entity should exist after termination before being cleaned up.
	// This handles delayed ETW events for terminated entities.
	minTerminationGracePeriod = 30 * time.Second
)

// KernelStateManager is the single source for kernel entity state.
// It manages the lifecycle of processes and threads, ensuring data consistency
// and handling cleanup logic in a centralized, thread-safe manner. It is designed
// as a singleton to be accessible by all collectors and handlers that require
// kernel state information.
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

	// State for calculating adaptive grace period and watchdog
	lastScrapeTime      time.Time
	lastScrapeTimeMu    sync.RWMutex
	scrapeDurations     [scrapeDurationSamples]time.Duration
	lastScrapeIndex     int
	scrapeDurationsFull bool // Flag to indicate if the buffer is full for averaging.

	cleanupMu       sync.Mutex // Ensures PostScrapeCleanup is not run concurrently.
	serviceListOnce sync.Once  // Ensures proactive service enrichment runs only once.
	log             *phusluadapter.SampledLogger
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

		// Start the failsafe watchdog to prevent memory leaks if scrapes stop.
		go globalStateManager.startScrapeWatchdog()
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
	// Prevent concurrent cleanup runs from a real scrape and the watchdog.
	sm.cleanupMu.Lock()
	defer sm.cleanupMu.Unlock()

	// Calculate the adaptive cleanup threshold based on recent scrape durations.
	currentScrape := sm.scrapeCounter.Add(1)
	cleanupThreshold := sm.calculateAdaptiveCleanupThreshold(currentScrape)

	GlobalDiagnostics.ReportTrackedProcToConsole(sm.GetProcessCount())

	// Identify terminated processes that have passed the grace period.
	procsToClean := make(map[uint64]struct{}) // Use a simple set of StartKeys.
	sm.Processes.terminatedProcesses.Range(func(startKey uint64, terminatedAtGeneration uint64) bool {
		if terminatedAtGeneration < cleanupThreshold {
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

				// Append to the list for the summary diagnostic. // TODO dont do this if diagnostic not activated.
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
	cleanedThreads := sm.Threads.cleanupTerminated(cleanupThreshold)
	cleanedImages := sm.Images.cleanupTerminated(cleanupThreshold)

	if cleanedProcs > 0 || cleanedThreads > 0 || cleanedImages > 0 {
		sm.log.Debug().Int("processes", cleanedProcs).
			Int("threads", cleanedThreads).
			Int("images", cleanedImages).
			Msg("Post-scrape cleanup complete")
	}
}

// TODO: redo this simplier?
// calculateAdaptiveCleanupThreshold is a private helper that encapsulates the logic
// for determining the cleanup generation based on a running average of scrape times.
func (sm *KernelStateManager) calculateAdaptiveCleanupThreshold(currentScrape uint64) (cleanupThreshold uint64) {
	// 1. Update timing metrics.
	now := time.Now()
	sm.lastScrapeTimeMu.Lock()
	duration := now.Sub(sm.lastScrapeTime)
	sm.lastScrapeTime = now
	sm.lastScrapeTimeMu.Unlock()

	sm.scrapeDurations[sm.lastScrapeIndex] = duration
	sm.lastScrapeIndex++
	if sm.lastScrapeIndex >= scrapeDurationSamples {
		sm.lastScrapeIndex = 0
		sm.scrapeDurationsFull = true
	}

	// 2. Calculate average scrape duration.
	var avgScrapeDuration time.Duration
	var totalDuration time.Duration
	sampleCount := scrapeDurationSamples
	if !sm.scrapeDurationsFull {
		sampleCount = sm.lastScrapeIndex
	}
	if sampleCount > 0 {
		for i := 0; i < sampleCount; i++ {
			totalDuration += sm.scrapeDurations[i]
		}
		avgScrapeDuration = totalDuration / time.Duration(sampleCount)
	}

	// 3. Calculate grace period in generations.
	var gracePeriodInGenerations uint64 = 1 // Default to 1 scrape if no average is available yet.
	if avgScrapeDuration > 0 {
		requiredGenerations := math.Ceil(minTerminationGracePeriod.Seconds() / avgScrapeDuration.Seconds())
		gracePeriodInGenerations = uint64(requiredGenerations)
		if gracePeriodInGenerations < 1 {
			gracePeriodInGenerations = 1 // Always wait at least one full scrape.
		}
	}

	// 4. Determine the cleanup threshold, prioritizing wall-clock time for watchdog scenarios.
	// If the time since the last cleanup is already much longer than our grace period,
	// it's a watchdog/stalled-scrape scenario. In this case, we override the generation
	// logic and clean up everything terminated before the current cycle.
	if duration > (minTerminationGracePeriod * 2) {
		cleanupThreshold = currentScrape
	} else if currentScrape > gracePeriodInGenerations {
		cleanupThreshold = currentScrape - gracePeriodInGenerations
	}
	// If neither condition is met (e.g., on initial startup scrapes), threshold remains 0, which is correct.

	sm.log.Debug().
		Uint64("generation", currentScrape).
		Uint64("cleanup_threshold", cleanupThreshold).
		Float64("avg_scrape_duration_s", avgScrapeDuration.Seconds()).
		Uint64("grace_period_in_generations", gracePeriodInGenerations).
		Msg("Starting post-scrape cleanup of terminated entities")

	return
}

// startScrapeWatchdog runs a periodic task that checks if Prometheus has scraped
// the exporter recently. If not, it manually triggers a cleanup cycle to prevent
// unbounded memory growth from terminated entities. This is a failsafe mechanism.
func (sm *KernelStateManager) startScrapeWatchdog() {
	const interval = 1 * time.Minute
	const timeout = 3 * time.Hour

	sm.log.Info().
		Str("check_interval", interval.String()).
		Str("timeout", timeout.String()).
		Msg("Starting scrape watchdog.")

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		// This goroutine runs for the lifetime of the application.
		// A context could be added here for graceful shutdown if needed in the future.
		<-ticker.C

		sm.lastScrapeTimeMu.RLock()
		last := sm.lastScrapeTime
		sm.lastScrapeTimeMu.RUnlock()

		timeSinceLastScrape := time.Since(last)

		if timeSinceLastScrape > timeout {
			sm.log.Warn().
				Dur("duration_since_last_scrape", timeSinceLastScrape).
				Msg("No scrapes detected in the configured timeout. Forcing cleanup to prevent memory leak.")

			// Manually trigger the same cleanup function that a normal scrape would.
			sm.PostScrapeCleanup()
		}
	}
}

// ApplyServices performs a one-time API call to get all running services
// and enriches the corresponding process data with the correct service tag. This
// solves the "dormant service" problem where initial rundown events lack tags.
func (sm *KernelStateManager) ApplyServices() {
	sm.log.Info().Msg("Performing one-time proactive service enrichment...")
	serviceMap, err := windowsapi.GetRunningServicesSnapshot()
	if err != nil {
		sm.log.Error().Err(err).Msg("Failed to get running services for proactive enrichment.")
		return
	}

	enrichedCount := 0
	for pid, serviceName := range serviceMap {
		// Find the tag for this service from the SystemConfig cache.
		if tag, ok := sm.Services.GetTagForService(serviceName); ok {
			// Find the process instance for this PID.
			if pData, pOk := sm.Processes.GetCurrentProcessDataByPID(pid); pOk {
				// Use the existing, centralized function to apply the tag.
				// This correctly handles locking and all subsequent logic.
				sm.Services.resolveAndApplyServiceName(pData, tag)
				enrichedCount++
			}
		}
	}
	sm.log.Info().Int("count", enrichedCount).Msg("Completed proactive service enrichment.")
}

// --- Public API (All direct calls should go to sub-managers) ---

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
