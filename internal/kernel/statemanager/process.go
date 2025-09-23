package statemanager

import (
	"strconv"
	"sync"
	"time"
)

// ProcessData is the central "Process Control Block" for a single process instance.
// It contains all state and metrics associated with that instance, organized into
// specialized modules. It is the primary data structure for the hot path.
type ProcessData struct {
	Info ProcessInfo // Basic info: PID, Name, StartTime, etc.

	// All modules are now pre-allocated for maximum hot-path performance.
	// The entire ProcessData object is pooled to manage memory usage.
	Disk     *DiskModule
	Memory   *MemoryModule
	Network  *NetworkModule
	Registry *RegistryModule
	Threads  *ThreadModule
}

// Reset clears the ProcessData object for reuse by the sync.Pool.
// It calls Reset() on each sub-module to zero out its state.
func (pd *ProcessData) Reset() {
	// Reset all sub-modules to prepare the entire object for reuse.
	pd.Disk.Reset()
	pd.Memory.Reset()
	pd.Network.Reset()
	pd.Registry.Reset()
	pd.Threads.Reset()

	pd.Info.reset()
}

// ProcessInfo holds information about a process, including its name, start time,
// command line, and other metadata. It is designed to be thread-safe for concurrent
// updates, with a focus on minimizing lock contention and memory allocations.
type ProcessInfo struct {
	PID           uint32
	StartKey      uint64 // Uniquely identifies a process instance during a boot session. (it's not CreateTime)
	Name          string
	StartTime     time.Time // Timestamp of the ETW event (start or rundown)
	CreateTime    time.Time // Actual process creation time from the event payload.
	LastSeen      time.Time
	ParentPID     uint32
	SessionID     uint32
	ImageChecksum uint32
	mu            sync.Mutex
}

// reset clears the ProcessInfo struct for reuse.
func (pi *ProcessInfo) reset() {
	pi.PID = 0
	pi.ParentPID = 0
	pi.StartKey = 0
	pi.StartTime = time.Time{}
	pi.CreateTime = time.Time{}
	pi.LastSeen = time.Time{}
	pi.Name = ""
	pi.SessionID = 0
	pi.ImageChecksum = 0
}

// Clone creates a safe, deep copy of the ProcessInfo struct.
func (pi *ProcessInfo) Clone() ProcessInfo {
	pi.mu.Lock()
	defer pi.mu.Unlock()

	return ProcessInfo{
		PID:           pi.PID,
		StartKey:      pi.StartKey,
		Name:          pi.Name,
		StartTime:     pi.StartTime,
		CreateTime:    pi.CreateTime,
		LastSeen:      pi.LastSeen,
		ParentPID:     pi.ParentPID,
		SessionID:     pi.SessionID,
		ImageChecksum: pi.ImageChecksum,
	}
}

// TODO: instead of mark for deletion, delete it and aggregate by name+checksum+sessionID?

// --- Process Management ---

// AddProcess adds or updates process information. If the process already exists,
// it updates its details and refreshes its LastSeen timestamp. This handles both
// new processes and updates from rundown events.
func (sm *KernelStateManager) AddProcess(pid uint32, startKey uint64, imageName string, parentPID uint32, sessionID uint32, imageChecksum uint32, createTime time.Time, eventTimestamp time.Time) {
	// A valid startKey is required to uniquely identify and store a process instance.
	if startKey == 0 {
		sm.log.SampledWarn("add_process_no_startkey").Uint32("pid", pid).Str("name", imageName).Msg("Cannot add process without a valid start key.")
		return
	}

	// If process filtering is enabled, check if this process should be tracked.
	// If it doesn't match any include pattern, we ignore it completely.
	if sm.processFilterEnabled {
		matches := false
		for _, re := range sm.processNameFilters {
			if re.MatchString(imageName) {
				matches = true
				break
			}
		}
		if !matches {
			return // This process is not in the include list, so we don't track it.
		}
	}

	// Set the current active start key for this PID. This simply overwrites any
	// previous entry, as this is, by definition, the newest process instance.
	sm.pidToCurrentStartKey.Store(pid, startKey)

	// Associate Start Key with its PID. This reverse mapping is always safe.
	sm.startKeyToPid.Store(startKey, pid)

	// If a process with this start key already exists, just update its LastSeen timestamp.
	// This is crucial for the stale process cleanup logic to work correctly with rundowns.
	if p, exists := sm.instanceData.Load(startKey); exists {
		p.Info.mu.Lock()
		p.Info.LastSeen = eventTimestamp
		p.Info.mu.Unlock()
		return
	}

	// The process is new. Get a fully-formed ProcessData object from the pool.
	pData := sm.processDataPool.Get().(*ProcessData)
	pData.Reset() // Ensure the object and all its modules are clean before use.

	pData.Info.PID = pid
	pData.Info.StartKey = startKey
	pData.Info.Name = imageName
	pData.Info.StartTime = eventTimestamp
	pData.Info.CreateTime = createTime
	pData.Info.LastSeen = eventTimestamp
	pData.Info.ParentPID = parentPID
	pData.Info.SessionID = sessionID
	pData.Info.ImageChecksum = imageChecksum

	// Atomically store the new process data.
	sm.instanceData.Store(startKey, pData)

	sm.log.Trace().
		Uint32("pid", pid).
		Uint64("start_key", startKey).
		Str("name", imageName).
		Uint32("parent_pid", parentPID).
		Uint32("session_id", sessionID).
		Uint32("checksum", imageChecksum).
		Msg("Process added to state manager")
}

// MarkProcessForDeletion marks a process for deletion. This is the single, consolidated
// function for handling process termination.
func (sm *KernelStateManager) MarkProcessForDeletion(pid uint32, startKey uint64) {
	// A precise startKey is required for reliable termination.
	if startKey == 0 {
		return
	}
	
	// Mark in the global terminated list for post-scrape cleanup by collectors
	sm.terminatedProcesses.Store(startKey, time.Now())

	// Atomically check if the current mapping for the PID still points to the
	// startKey we are terminating. If it does, we can safely delete the mapping.
	// If it doesn't, it means the PID has already been reused, and we should
	// not touch the new mapping. This correctly handles the race condition.
	sm.pidToCurrentStartKey.Update(pid, func(currentSK uint64, exists bool) (uint64, bool) {
		if exists && currentSK == startKey {
			return 0, false // Value matches, so delete the entry.
		}
		return currentSK, true // Value does not match, keep it.
	})

	sm.log.Trace().Uint64("start_key", startKey).Msg("Process marked for deletion by StartKey")
}

// --- Public Getters (StartKey-based) ---

// GetProcessNameBySK returns the process name for a given start key.
// It can resolve names for active processes.
// If the process is not found, it returns a formatted "unknown" string and false.
func (sm *KernelStateManager) GetProcessNameBySK(startKey uint64) (string, bool) {
	if pData, exists := sm.instanceData.Load(startKey); exists {
		pData.Info.mu.Lock()
		name := pData.Info.Name
		pData.Info.mu.Unlock()
		return name, true
	}
	return "unknown_sk_" + strconv.FormatUint(startKey, 10), false
}

// GetProcessInfoBySK returns a clone of the ProcessInfo struct for a given start key.
// This provides a consistent, thread-safe snapshot of the process's state.
func (sm *KernelStateManager) GetProcessInfoBySK(startKey uint64) (ProcessInfo, bool) {
	if pData, exists := sm.instanceData.Load(startKey); exists {
		return pData.Info.Clone(), true
	}
	return ProcessInfo{}, false
}

// GetProcessDataBySK is the new primary entry point for handlers to get the central
// process data object. It is highly optimized for the hot path.
func (sm *KernelStateManager) GetProcessDataBySK(startKey uint64) (*ProcessData, bool) {
	return sm.instanceData.Load(startKey)
}

// GetKnownProcessNameBySK retrieves the name for a known/tracked process.
// With the new filtering logic, if a process exists in the map, it is considered known.
func (sm *KernelStateManager) GetKnownProcessNameBySK(startKey uint64) (string, bool) {
	if pData, exists := sm.instanceData.Load(startKey); exists {
		pData.Info.mu.Lock()
		name := pData.Info.Name
		pData.Info.mu.Unlock()
		return name, true
	}
	return "", false
}

// --- Public Getters (PID-based, for convenience) ---

// IsKnownProcessID checks if a process with the given PID is being tracked.
// This is a convenience wrapper around the StartKey-based check.
func (sm *KernelStateManager) IsKnownProcessID(pid uint32) bool {
	startKey, ok := sm.GetProcessStartKey(pid)
	if !ok {
		return false // Cannot be known if we don't have a start key for it.
	}
	return sm.IsTrackedStartKey(startKey)
}

// GetProcessNameByID returns the process name for a given PID.
// This is a convenience wrapper around the StartKey-based getter.
func (sm *KernelStateManager) GetProcessNameByID(pid uint32) (string, bool) {
	if startKey, ok := sm.GetProcessStartKey(pid); ok {
		return sm.GetProcessNameBySK(startKey)
	}
	return "unknown_pid_" + strconv.FormatUint(uint64(pid), 10), false
}

// GetKnownProcessNameByID is a convenience helper that combines checking if a process
// is known/tracked with retrieving its name.
// This is a convenience wrapper around the StartKey-based getter.
func (sm *KernelStateManager) GetKnownProcessNameByID(pid uint32) (string, bool) {
	startKey, ok := sm.GetProcessStartKey(pid)
	if !ok {
		return "", false
	}
	return sm.GetKnownProcessNameBySK(startKey)
}

// GetProcessStartKey returns the unique process start key for a given PID.
func (sm *KernelStateManager) GetProcessStartKey(pid uint32) (uint64, bool) {
	return sm.pidToCurrentStartKey.Load(pid)
}

// GetPIDFromStartKey returns the process ID for a given unique process start key.
func (sm *KernelStateManager) GetPIDFromStartKey(startKey uint64) (uint32, bool) {
	return sm.startKeyToPid.Load(startKey)
}

// IsTrackedStartKey checks if a process with the given start key is being tracked.
// With the new filtering logic, this is equivalent to checking for the process's existence
// in the main process map.
func (sm *KernelStateManager) IsTrackedStartKey(startKey uint64) bool {
	_, exists := sm.instanceData.Load(startKey)
	return exists
}

// RangeProcesses iterates over all active processes in the state manager.
// The provided function is called for each process. If the function returns
// false, the iteration stops.
func (sm *KernelStateManager) RangeProcesses(f func(p *ProcessData) bool) {
	sm.instanceData.Range(func(startKey uint64, p *ProcessData) bool {
		// The ProcessData contains the correct, immutable PID and other metadata
		// associated with its StartKey.
		return f(p)
	})
}
