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
// kernel state information. As a facade, it orchestrates the ProcessManager and
// SystemState components, providing a unified API.
type KernelStateManager struct {
	processManager *ProcessManager
	systemState    *SystemState

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

// GetProcessCount implements the debug.StateProvider interface.
func (sm *KernelStateManager) GetProcessCount() int {
	return sm.processManager.GetProcessCount()
}

// GetThreadCount returns the current number of tracked threads.
// This is a helper for the debug endpoint and implements the debug.StateProvider interface.
func (sm *KernelStateManager) GetThreadCount() int {
	return sm.systemState.GetThreadCount()
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
		ss := newSystemState(log)

		// 2. Link SystemState to ProcessManager. The reverse dependency is now gone.
		ss.setProcessManager(pm)

		// 3. Create the facade and store the components.
		sm := &KernelStateManager{
			log:            log,
			processManager: pm,
			systemState:    ss,
			lastScrapeTime: time.Now(), // Initialize to startup time.
			aggregationDataPool: sync.Pool{
				New: func() any {
					return make(map[ProgramAggregationKey]*AggregatedProgramMetrics)
				},
			},
		}

		GlobalDiagnostics.Init()
		globalStateManager = sm
	})
	return globalStateManager
}

// ApplyConfig applies collector configuration to the state manager.
// This is where regex patterns for process filtering are compiled.
func (sm *KernelStateManager) ApplyConfig(cfg *config.CollectorConfig) {
	sm.processManager.ApplyConfig(cfg)
	// In the future, if systemState needs configuration, it can be applied here:
	// sm.systemState.ApplyConfig(cfg)
}

func (sm *KernelStateManager) EnableProcessManager(enable bool) {
	sm.processManager.Enabled = enable
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
	sm.processManager.terminatedProcesses.Range(func(startKey uint64, terminatedAtGeneration uint64) bool {
		if terminatedAtGeneration < currentScrape {
			procsToClean[startKey] = struct{}{}
			// It's safe to delete while ranging over this specific concurrent map implementation.
			sm.processManager.terminatedProcesses.Delete(startKey)
		}
		return true
	})

	var cleanedProcs int
	if len(procsToClean) > 0 {
		// New single-line diagnostic log for cleaned processes.
		cleanedProcsDiag := make([]string, 0, len(procsToClean))

		// Log detailed trace info for each process being cleaned.
		for sk := range procsToClean {
			if pData, exists := sm.processManager.GetProcessDataBySK(sk); exists {
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
		sm.systemState.cleanupCachedThreads(procsToClean)

		// Now that dependents are gone, clean up the process objects themselves.
		cleanedProcs = sm.processManager.cleanupProcesses(procsToClean)
	}

	//Cleanup for individually terminated threads and images (fast path).
	cleanedThreads, cleanedImages := sm.systemState.cleanupCachedEntities(currentScrape)

	if cleanedProcs > 0 || cleanedThreads > 0 || cleanedImages > 0 {
		sm.log.Debug().Int("processes", cleanedProcs).
			Int("threads", cleanedThreads).
			Int("images", cleanedImages).
			Msg("Post-scrape cleanup complete")
	}
}

// TODO: maybe refactor these to be direct calls?

// --- Public API (Delegation) ---

// --- Process Methods ---
func (sm *KernelStateManager) AddProcess(pid uint32, uniqueProcessKey uint64, imageName string, parentPID uint32,
	sessionID uint32, eventTimestamp time.Time) {
	sm.processManager.AddProcess(pid, uniqueProcessKey, imageName, parentPID, sessionID, eventTimestamp)
}
func (sm *KernelStateManager) MarkProcessForDeletion(pid uint32, startKey uint64, terminationTime time.Time) {
	currentScrape := sm.scrapeCounter.Load()
	sm.processManager.MarkProcessForDeletion(pid, startKey, terminationTime, currentScrape)
}
func (sm *KernelStateManager) GetCurrentProcessDataByPID(pid uint32) (*ProcessData, bool) {
	return sm.processManager.GetCurrentProcessDataByPID(pid)
}
func (sm *KernelStateManager) GetProcessDataBySK(startKey uint64) (*ProcessData, bool) { // not used (requieres two lookups)
	return sm.processManager.GetProcessDataBySK(startKey)
}
func (sm *KernelStateManager) GetProcessCurrentStartKey(pid uint32) (uint64, bool) {
	return sm.processManager.GetProcessCurrentStartKey(pid)
}
func (sm *KernelStateManager) RangeProcesses(f func(pData *ProcessData) bool) {
	sm.processManager.RangeProcesses(f)
}

// --- Thread Methods ---
func (sm *KernelStateManager) AddThread(tid, pid uint32, eventTimestamp time.Time, subProcessTag uint32) {
	sm.systemState.AddThread(tid, pid, eventTimestamp, subProcessTag)
}

func (sm *KernelStateManager) MarkThreadForDeletion(tid uint32) {
	currentScrape := sm.scrapeCounter.Load()
	sm.systemState.MarkThreadForDeletion(tid, currentScrape)
}
func (sm *KernelStateManager) GetStartKeyByThread(tid uint32) (uint64, bool) { // not used (requieres two lookups)
	return sm.systemState.GetStartKeyForThread(tid)
}

// GetCurrentProcessDataByThread resolves a TID to its parent's *ProcessData object.
// This is the new, optimized hot-path function that avoids a second map lookup.
func (sm *KernelStateManager) GetCurrentProcessDataByThread(tid uint32) (*ProcessData, bool) {
	return sm.systemState.GetCurrentProcessDataByThread(tid)
}

// --- Service Tag Methods ---

// RegisterServiceTag stores a mapping between a SubProcessTag and a service name.
func (sm *KernelStateManager) RegisterServiceTag(tag uint32, name string) {
	sm.systemState.RegisterServiceTag(tag, name)
}

// --- Image Methods ---
func (sm *KernelStateManager) AddImage(pid uint32, imageBase, imageSize uint64, fileName string, peTimestamp uint32, peChecksum uint32) {
	sm.systemState.AddImage(pid, imageBase, imageSize, fileName, peTimestamp, peChecksum)
}
func (sm *KernelStateManager) UnloadImage(imageBase uint64) {
	currentScrape := sm.scrapeCounter.Load()
	sm.systemState.UnloadImage(imageBase, currentScrape)
}
func (sm *KernelStateManager) markImageForDeletion(imageBase uint64) { // TODO: not used anymore for now. delete.
	currentScrape := sm.scrapeCounter.Load()
	sm.systemState.markImageForDeletion(imageBase, currentScrape)
}
func (sm *KernelStateManager) GetImageForAddress(address uint64) (*ImageInfo, bool) {
	return sm.systemState.GetImageForAddress(address)
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

// GetCleanedImages returns the names of images that have been terminated in the
// last scrape interval. This is used by collectors to prune their own state.
func (sm *KernelStateManager) GetCleanedImages() []string {
	return sm.systemState.GetCleanedImages()
}

// --- Event Attribution Methods ---
func (sm *KernelStateManager) RecordHardPageFaultForThread(tid, pid uint32) {
	sm.systemState.RecordHardPageFaultForThread(tid, pid)
}

// --- Debug Provider Facade Methods ---

func (sm *KernelStateManager) GetTerminatedCounts() (procs, threads, images int) {
	procs = sm.processManager.GetTerminatedProcessCount()
	threads, images = sm.systemState.GetTerminatedThreadAndImageCounts()
	return
}

func (sm *KernelStateManager) GetImageCount() int {
	return sm.systemState.GetImageCount()
}

func (sm *KernelStateManager) GetServiceCount() int {
	return sm.systemState.GetServiceCount()
}

func (sm *KernelStateManager) RangeServices(f func(tag uint32, name string)) {
	sm.systemState.RangeServices(f)
}

func (sm *KernelStateManager) RangeImages(f func(info *ImageInfo)) {
	sm.systemState.RangeImages(f)
}

func (sm *KernelStateManager) RangeThreads(f func(tid uint32, startKey uint64)) {
	sm.systemState.RangeThreads(f)
}
