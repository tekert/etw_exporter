// This file defines the ProcessManager, a specialized data store responsible exclusively
// for the lifecycle of ProcessData objects.
package statemanager

import (
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"etw_exporter/internal/config"
	"etw_exporter/internal/maps"

	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// ProcessManager is exclusively responsible for the lifecycle of user processes.
// It creates, tracks, updates, and cleans up ProcessData objects, manages PID-to-StartKey
// mapping to handle PID reuse, and owns the processDataPool.
type ProcessManager struct {
	// Process state
	instanceData        maps.ConcurrentMap[uint64, *ProcessData] // key: uint64 (StartKey), value: *ProcessData
	terminatedProcesses maps.ConcurrentMap[uint64, uint64]       // key: uint64 (StartKey), value: scrape generation
	processDataPool     sync.Pool

	// Process Start Key state for robust tracking
	pidToCurrentStartKey    maps.ConcurrentMap[uint32, uint64]       // key: uint32 (PID), value: uint64 (current StartKey)  // not used
	pidToCurrentProcessData maps.ConcurrentMap[uint32, *ProcessData] // key: uint32 (PID), value: *ProcessData (direct access)

	// Back-reference to the orchestrator to access shared state like the scrape counter.
	stateManager *KernelStateManager

	// Process filtering state
	processFilterEnabled bool
	processNameFilters   []*regexp.Regexp

	Enabled bool // Perfinfo doesnt requiere processes manager for example.

	log *phusluadapter.SampledLogger
}

// newProcessManager creates and initializes a new ProcessManager.
func newProcessManager(log *phusluadapter.SampledLogger) *ProcessManager {
	pm := &ProcessManager{
		log:                     log,
		instanceData:            maps.NewConcurrentMap[uint64, *ProcessData](),
		terminatedProcesses:     maps.NewConcurrentMap[uint64, uint64](),
		pidToCurrentStartKey:    maps.NewConcurrentMap[uint32, uint64](), // not used
		pidToCurrentProcessData: maps.NewConcurrentMap[uint32, *ProcessData](),
		processDataPool: sync.Pool{
			New: func() any {
				return &ProcessData{
					Disk:     newDiskModule(),
					Memory:   newMemoryModule(),
					Network:  newNetworkModule(),
					Registry: new(RegistryModule),
					Threads:  newThreadModule(),
					// resolutionDurations is initialized on first use in AddProcess
				}
			},
		},
		Enabled: false,
	}

	// Initialize the singleton "unknown process"
	// This acts as a bucket for metrics from threads whose parent process
	// could not be identified in time.
	unknownProc := pm.processDataPool.Get().(*ProcessData)
	unknownProc.Reset()
	unknownProc.Info.StartKey = UnknownProcessStartKey
	unknownProc.Info.PID = 1<<32 - 1 // Invalid PID to indicate "unknown"
	unknownProc.Info.Name = "unknown_process"
	unknownProc.Info.LastSeen = time.Now().Add(100 * 365 * 24 * time.Hour) // Never gets cleaned up.
	pm.instanceData.Store(UnknownProcessStartKey, unknownProc)

	return pm
}

// setStateManager sets the reference to the KernelStateManager to resolve circular dependencies
// and allow access to shared state.
func (pm *ProcessManager) setStateManager(sm *KernelStateManager) {
	pm.stateManager = sm
}

// EnrichProcessWithImageData handles enrichment data from an Image/Load event.
// If the process is already known, it enriches it directly. If the Image/Load
// event arrives before the Process/Start event, it stores the data in a
// pending map to be applied when the process is created.
func (pm *ProcessManager) EnrichProcessWithImageData(pid uint32, checksum uint64, imageName string) {
	// Attempt to find the live process. This is now the ONLY path.
	if startKey, ok := pm.pidToCurrentStartKey.Load(pid); ok {
		if pData, pOk := pm.instanceData.Load(startKey); pOk {
			// Found the live process. Lock and enrich it.
			pData.Info.mu.Lock()
			if pData.Info.UniqueHash == 0 {
				pData.Info.UniqueHash = checksum
			}

			// Smart name update: Only replace the name if the new one is a full path
			// and the old one is not.
			isNewNameFullPath := strings.Contains(imageName, "\\")
			isOldNameFullPath := strings.Contains(pData.Info.Name, "\\")

			if isNewNameFullPath && !isOldNameFullPath {
				pm.log.SampledDebug("enrich_name_debug").
					Uint32("pid", pid).
					Str("old_name", pData.Info.Name).
					Str("new_name", imageName).
					Msg("Upgrading process name to full path from Image/Load.")
				pData.Info.Name = imageName
			} else {
				// Log why we are NOT updating, but only at trace level to avoid noise.
				pm.log.Trace().
					Uint32("pid", pid).
					Str("current_name", pData.Info.Name).
					Str("candidate_name", imageName).
					Bool("is_new_path", isNewNameFullPath).
					Bool("is_old_path", isOldNameFullPath).
					Msg("Skipping process name update during enrichment.")
			}

			pData.Info.mu.Unlock()
			return
		}
	}

	// ERROR CASE: The Image/Load event arrived for a process we are not tracking.
	pm.log.Error().
		Uint32("pid", pid).
		Str("image_name", imageName).
		Msg("ERROR: Image/Load event for unknown process.")
}

// AddProcess adds or updates process information.
func (pm *ProcessManager) AddProcess(
	pid uint32,
	startKey uint64, // The UniqueProcessKey from the MOF event, used as the immutable primary key.
	eventImageName string,
	parentPID uint32,
	sessionID uint32,
	eventTimestamp time.Time) {

	if pData, exists := pm.instanceData.Load(startKey); exists {
		// We are already tracking this exact process instance. This is likely a duplicate
		// event (e.g., from a rundown). Just update its last seen time.
		pData.Info.mu.Lock()
		pData.Info.LastSeen = eventTimestamp
		pData.Info.mu.Unlock()
		return
	}

	// --- CREATION PATH ---
	// The System process (PID 4) is a special singleton.
	if sessionID == 0xFFFFFFFF || pid == 4 {
		sessionID = 0 // Normalize invalid session ID.
	}

	// Get a new ProcessData object from the pool and populate with the ETW event data.
	pData := pm.processDataPool.Get().(*ProcessData)
	pData.Reset()
	pData.Info.PID = pid
	pData.Info.StartKey = startKey
	pData.Info.Name = eventImageName
	pData.Info.CreateTime = eventTimestamp // Best-effort create time.
	pData.Info.LastSeen = eventTimestamp
	pData.Info.ParentPID = parentPID
	pData.Info.SessionID = sessionID

	// Store the new process instance immediately.
	pm.instanceData.Store(startKey, pData)
	pm.pidToCurrentStartKey.Store(pid, startKey)
	pm.pidToCurrentProcessData.Store(pid, pData)
	GlobalDiagnostics.RecordProcessAdded(pid, pData.Info.Name)

	pm.log.Trace().
		Uint32("pid", pid).
		Uint64("start_key", startKey).
		Str("name", pData.Info.Name).
		Bool("enriched", pData.Info.UniqueHash > 0).
		Msg("Process added to process manager")
}

// TODO: instead of mark for deletion, delete it and aggregate by name+checksum+sessionID?
// MarkProcessForDeletion marks a process for deletion. This is the single, consolidated
// function for handling process termination.
func (pm *ProcessManager) MarkProcessForDeletion(
	pid uint32,
	startKey uint64,
	terminationTime time.Time) {

	// Get the current scrape generation internally, keeping the public API clean.
	currentScrape := pm.stateManager.scrapeCounter.Load()

	//  Update the ProcessData object if it exists to set the termination time.
	if pData, exists := pm.instanceData.Load(startKey); exists {
		pData.Info.mu.Lock()
		if pData.Info.TerminationTime.IsZero() {
			pData.Info.TerminationTime = terminationTime
		}
		pData.Info.mu.Unlock()
	} else {
		pm.log.Trace().
			Uint64("start_key", startKey).
			Msg("Process to be marked for deletion not found, likely already cleaned up.")
		return
	}

	// Mark in the global terminated list with the scrape generation when it was terminated.
	pm.terminatedProcesses.Store(startKey, currentScrape)

	// The pidToCurrentStartKey mapping is intentionally NOT deleted here.
	// The mapping must persist for the entire grace period to allow late-arriving
	// events to be attributed to this terminated process.
	//
	// PID Reuse Handling:
	// - If the PID is reused, the AddProcess function for the NEW process will
	//   atomically replace this mapping with the new StartKey.
	// - The cleanupProcesses function will later try to clean up this old mapping,
	//   but it will see that it no longer points to this StartKey and will correctly
	//   leave the new mapping untouched.
	// This ensures the "current" mapping is always accurate.

	pm.log.Trace().Uint64("start_key", startKey).Msg("Process marked for deletion by StartKey")
}

// cleanupProcesses is the centralized private helper for process deletion.
// It removes processes and their associated threads.
// It returns the number of processes that were cleaned.
func (pm *ProcessManager) cleanupProcesses(procsToClean map[uint64]struct{}) int {
	pm.log.Debug().Int("count", len(procsToClean)).Msg("Starting process cleanup...")
	if len(procsToClean) == 0 {
		return 0
	}

	for startKey := range procsToClean {
		// Always remove the process info and its primary key mappings.
		if pData, exists := pm.instanceData.LoadAndDelete(startKey); exists {
			// Get the PID from the ProcessData object we are about to recycle.
			pid := pData.Info.PID

			// Atomically remove the pid -> current mapping, but ONLY if it still
			// points to the key we are cleaning up. If the PID has been reused,
			// this condition will be false, and we will not touch the new mapping.
			pm.pidToCurrentStartKey.Update(pid, func(currentSK uint64, exists bool) (uint64, bool) {
				if exists && currentSK == startKey {
					return 0, false // Value matches, so delete the entry.
				}
				return currentSK, true // Value does not match (PID reused), so keep the new entry.
			})

			// Atomically remove the direct pid -> pData mapping, using the same
			// PID reuse-aware logic.
			pm.pidToCurrentProcessData.Update(pid, func(currentPData *ProcessData, exists bool) (*ProcessData, bool) {
				if exists && currentPData.Info.StartKey == startKey {
					return nil, false // StartKey matches, so delete the entry.
				}
				return currentPData, true // StartKey does not match (PID reused), so keep the new entry.
			})

			// --- Recycle the entire ProcessData object ---
			// The object is reset after it's retrieved from the pool in AddProcess
			pm.processDataPool.Put(pData)
		}
	}
	return len(procsToClean)
}

// ApplyConfig applies collector configuration to the process manager.
func (pm *ProcessManager) ApplyConfig(cfg *config.CollectorConfig) {
	pm.Enabled = cfg.RequiresProcessManager

	pm.processFilterEnabled = cfg.ProcessFilter.Enabled
	if !pm.processFilterEnabled {
		pm.log.Debug().Msg("Process filtering is disabled. All processes will be tracked.")
		return
	}

	pm.processNameFilters = make([]*regexp.Regexp, 0, len(cfg.ProcessFilter.IncludeNames))
	for _, pattern := range cfg.ProcessFilter.IncludeNames {
		re, err := regexp.Compile(pattern)
		if err != nil {
			pm.log.Error().Err(err).Str("pattern", pattern).Msg("Failed to compile process name filter regex, pattern will be ignored.")
			continue
		}
		pm.processNameFilters = append(pm.processNameFilters, re)
	}
	pm.log.Info().Int("patterns", len(pm.processNameFilters)).Msg("Process filtering enabled with patterns.")
}

// --- Public Getters ---

// GetCurrentProcessDataByPID provides a direct, single-lookup way to get the current
// ProcessData for a given PID. It is highly optimized for the hot path.
func (pm *ProcessManager) GetCurrentProcessDataByPID(pid uint32) (*ProcessData, bool) {
	return pm.pidToCurrentProcessData.Load(pid)
}

// GetProcessDataBySK is the primary entry point for handlers to get the central
// process data object. It is highly optimized for the hot path.
func (pm *ProcessManager) GetProcessDataBySK(startKey uint64) (*ProcessData, bool) {
	return pm.instanceData.Load(startKey)
}

// GetProcessCurrentStartKey returns the unique process start key for a given PID.
func (pm *ProcessManager) GetProcessCurrentStartKey(pid uint32) (uint64, bool) {
	return pm.pidToCurrentStartKey.Load(pid)
}

// RangeProcesses iterates over all active processes in the state manager.
// The provided function is called for each process. If the function returns
// false, the iteration stops.
func (pm *ProcessManager) RangeProcesses(f func(p *ProcessData) bool) {
	pm.instanceData.Range(func(startKey uint64, p *ProcessData) bool {
		return f(p)
	})
}

// GetProcessCount returns the total number of active processes being tracked.
func (pm *ProcessManager) GetProcessCount() int {
	var count int
	pm.instanceData.Range(func(_ uint64, _ *ProcessData) bool {
		count++
		return true
	})
	return count
}

// GetProcessNameBySK returns the process name for a given start key.
// It can resolve names for active processes.
// If the process is not found, it returns a formatted "unknown" string and false.
func (pm *ProcessManager) GetProcessNameBySK(startKey uint64) (string, bool) {
	if pData, exists := pm.GetProcessDataBySK(startKey); exists {
		pData.Info.mu.Lock()
		name := pData.Info.Name
		pData.Info.mu.Unlock()
		return name, true
	}
	return "unknown_sk_" + strconv.FormatUint(startKey, 10), false
}

// GetProcessInfoBySK returns a clone of the ProcessInfo struct for a given start key.
// This provides a consistent, thread-safe snapshot of the process's state.
func (pm *ProcessManager) GetProcessInfoBySK(startKey uint64) (ProcessInfo, bool) {
	if pData, exists := pm.GetProcessDataBySK(startKey); exists {
		return pData.Info.Clone(), true
	}
	return ProcessInfo{}, false
}

// GetKnownProcessNameBySK retrieves the name for a known/tracked process.
func (pm *ProcessManager) GetKnownProcessNameBySK(startKey uint64) (string, bool) {
	if pData, exists := pm.GetProcessDataBySK(startKey); exists {
		pData.Info.mu.Lock()
		name := pData.Info.Name
		pData.Info.mu.Unlock()
		return name, true
	}
	return "", false
}

// IsKnownProcessID checks if a process with the given PID is being tracked.
func (pm *ProcessManager) IsKnownProcessID(pid uint32) bool {
	startKey, ok := pm.GetProcessCurrentStartKey(pid)
	if !ok {
		return false
	}
	return pm.IsTrackedStartKey(startKey)
}

// GetProcessNameByID returns the process name for a given PID.
func (pm *ProcessManager) GetProcessNameByID(pid uint32) (string, bool) {
	if startKey, ok := pm.GetProcessCurrentStartKey(pid); ok {
		return pm.GetProcessNameBySK(startKey)
	}
	return "unknown_pid_" + strconv.FormatUint(uint64(pid), 10), false
}

// GetKnownProcessNameByID is a convenience helper that combines checking if a process
// is known/tracked with retrieving its name.
func (pm *ProcessManager) GetKnownProcessNameByID(pid uint32) (string, bool) {
	startKey, ok := pm.GetProcessCurrentStartKey(pid)
	if !ok {
		return "", false
	}
	return pm.GetKnownProcessNameBySK(startKey)
}

// IsTrackedStartKey checks if a process with the given start key is being tracked.
func (pm *ProcessManager) IsTrackedStartKey(startKey uint64) bool {
	_, exists := pm.GetProcessDataBySK(startKey)
	return exists
}

// --- Debug Provider Methods ---

// GetTerminatedProcessCount returns the number of processes marked for termination.
func (pm *ProcessManager) GetTerminatedProcessCount() int {
	var count int
	pm.terminatedProcesses.Range(func(u uint64, u2 uint64) bool {
		count++
		return true
	})
	return count
}
