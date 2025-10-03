package statemanager

import (
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"time"

	"etw_exporter/internal/config"
	"etw_exporter/internal/logger"

	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

const (
	// entityGracePeriod defines how long a terminated entity's metadata (e.g., TID -> StartKey mapping,
	// or a ProcessData object) is kept before being cleaned up. This grace period is crucial for
	// correctly attributing delayed events, such as cached disk I/O, memory faults, that may
	// arrive after the originating thread or process has already terminated.
	entityGracePeriod = 10 * time.Second

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

		// 2. Link them to resolve circular dependencies.
		pm.setSystemState(ss)
		ss.setProcessManager(pm)

		// 3. Create the facade and store the components.
		sm := &KernelStateManager{
			log:            log,
			processManager: pm,
			systemState:    ss,
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

// --- Coordinated Lifecycle Management ---

// PostScrapeCleanup performs the actual deletion of entities that were marked for termination.
// This is called by the metrics HTTP handler AFTER a Prometheus scrape is complete.
func (sm *KernelStateManager) PostScrapeCleanup() {
	// Report diagnostic statistics for the completed scrape interval.
	GlobalDiagnostics.ReportTrackedProcToConsole(sm.GetProcessCount())

	sm.log.Debug().Msg("Starting post-scrape cleanup of terminated entities")

	// Phase 1: Cleanup for stale pending metrics buckets is no longer needed,
	// as pending threads are cleaned up by their own timeout mechanism.

	// Phase 2: Cleanup for terminated processes that are past their grace period.
	cutoff := time.Now().Add(-entityGracePeriod)
	procsToClean := make(map[uint64]struct{}) // Use a simple set of StartKeys.
	sm.processManager.terminatedProcesses.Range(func(startKey uint64, termTime time.Time) bool {
		if termTime.Before(cutoff) {
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
		sm.log.Debug().Strs("cleaned_processes_in_interval", cleanedProcsDiag).Msg("Processes cleaned up in this interval")

		cleanedProcs = sm.processManager.cleanupProcesses(procsToClean)
	}

	// Phase 3: Cleanup for individually terminated threads and images (fast path).
	cleanedThreads, cleanedImages := sm.systemState.cleanupIndividualEntities()

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
func (sm *KernelStateManager) MarkProcessForDeletion(pid uint32, parentProcessID uint32, uniqueProcessKey uint64, terminationTime time.Time) {
	sm.processManager.MarkProcessForDeletion(pid, parentProcessID, uniqueProcessKey, terminationTime)
}
func (sm *KernelStateManager) GetProcessDataBySK(startKey uint64) (*ProcessData, bool) {
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
	sm.systemState.MarkThreadForDeletion(tid)
}
func (sm *KernelStateManager) GetStartKeyForThread(tid uint32) (uint64, bool) {
	return sm.systemState.GetStartKeyForThread(tid)
}

// --- Service Tag Methods ---

// RegisterServiceTag stores a mapping between a SubProcessTag and a service name.
func (sm *KernelStateManager) RegisterServiceTag(tag uint32, name string) {
	sm.systemState.RegisterServiceTag(tag, name)
}

// --- Image Methods ---
func (sm *KernelStateManager) AddImage(pid uint32, imageBase, imageSize uint64, fileName string, loadTimestamp uint32, imageChecksum uint32) {
	sm.systemState.AddImage(pid, imageBase, imageSize, fileName, loadTimestamp, imageChecksum)
}
func (sm *KernelStateManager) UnloadImage(imageBase uint64) {
	sm.systemState.UnloadImage(imageBase)
}
func (sm *KernelStateManager) MarkImageForDeletion(imageBase uint64) {
	sm.systemState.MarkImageForDeletion(imageBase)
}
func (sm *KernelStateManager) GetImageForAddress(address uint64) (*ImageInfo, bool) {
	return sm.systemState.GetImageForAddress(address)
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
