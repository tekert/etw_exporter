package statemanager

import (
	"strconv"
	"sync"
	"time"
)

// ProcessInfo holds information about a process, including its name, start time,
// and parent PID. It is protected by a mutex to allow for safe concurrent updates.
type ProcessInfo struct {
	PID       uint32
	StartKey  uint64 // Uniquely identifies a process instance during a boot session.
	Name      string
	StartTime time.Time
	LastSeen  time.Time
	ParentPID uint32
	mu        sync.Mutex
}

// Clone creates a safe, deep copy of the ProcessInfo struct.
func (pi *ProcessInfo) Clone() ProcessInfo {
	pi.mu.Lock()
	defer pi.mu.Unlock()

	return ProcessInfo{
		PID:       pi.PID,
		StartKey:  pi.StartKey,
		Name:      pi.Name,
		StartTime: pi.StartTime,
		LastSeen:  pi.LastSeen,
		ParentPID: pi.ParentPID,
	}
}

// --- Process Management ---

// AddProcess adds or updates process information. If the process already exists,
// it updates its details and refreshes its LastSeen timestamp. This handles both
// new processes and updates from rundown events.
func (sm *KernelStateManager) AddProcess(pid uint32, startKey uint64, imageName string, parentPID uint32, eventTimestamp time.Time) {
	// If process filtering is enabled, check if this process should be tracked.
	if sm.processFilterEnabled && startKey != 0 {
		// Check if the start key is already tracked to avoid re-evaluating regex.
		if _, isTracked := sm.trackedStartKeys.Load(startKey); !isTracked {
			for _, re := range sm.processNameFilters {
				if re.MatchString(imageName) {
					sm.trackedStartKeys.Store(startKey, struct{}{})
					sm.log.Trace().Str("process_name", imageName).Uint64("start_key", startKey).Msg("Process start key is now tracked due to name match.")
					break // Found a match, no need to check other patterns.
				}
			}
		}
	}

	// If the process was recently terminated, remove it from the terminated list
	// as it might be a PID reuse scenario.
	sm.terminatedProcesses.Delete(pid)

	// Associate PID with its unique Start Key
	if startKey != 0 {
		sm.pidToStartKey.Store(pid, startKey)
		pidsMap, _ := sm.startKeyToPids.LoadOrStore(startKey, &sync.Map{})
		pidsMap.(*sync.Map).Store(pid, struct{}{})
	}

	if val, existed := sm.processes.Load(pid); existed {
		// Update existing process information
		info := val.(*ProcessInfo)
		info.mu.Lock()
		info.Name = imageName
		info.ParentPID = parentPID
		info.LastSeen = eventTimestamp
		if startKey != 0 {
			info.StartKey = startKey
		}
		info.mu.Unlock()
		return
	}

	sm.processes.Store(pid, &ProcessInfo{
		PID:       pid,
		StartKey:  startKey,
		Name:      imageName,
		StartTime: eventTimestamp,
		LastSeen:  eventTimestamp,
		ParentPID: parentPID,
	})

	sm.log.Trace().
		Uint32("pid", pid).
		Str("name", imageName).
		Uint32("parent_pid", parentPID).
		Msg("Process added to collector")
}

// MarkProcessForDeletion moves a process to the terminated list for delayed cleanup.
// It does NOT immediately delete the process info, allowing in-flight scrapes to
// complete successfully.
func (sm *KernelStateManager) MarkProcessForDeletion(pid uint32) {
	if _, exists := sm.processes.Load(pid); exists {
		sm.terminatedProcesses.Store(pid, time.Now())
		sm.log.Trace().Uint32("pid", pid).Msg("Process marked for deletion post-scrape")
	}
}

// IsKnownProcess checks if a process with the given PID is being tracked.
// This is a fast, allocation-free check used by collectors to decide whether
// to create metrics for a given process.
func (sm *KernelStateManager) IsKnownProcess(pid uint32) bool {
	// If filtering is not enabled, track all known processes.
	if !sm.processFilterEnabled {
		_, exists := sm.processes.Load(pid)
		return exists
	}

	// If filtering is enabled, only track processes with a matching start key.
	startKey, ok := sm.GetProcessStartKey(pid)
	if !ok {
		return false // Cannot track if we don't know its start key.
	}

	_, isTracked := sm.trackedStartKeys.Load(startKey)
	return isTracked
}

// GetKnownProcessName is a convenience helper that combines checking if a process
// is known (and tracked) with retrieving its name. It returns the name and true
// only if the process is known/tracked and its name can be resolved.
func (sm *KernelStateManager) GetKnownProcessName(pid uint32) (string, bool) {
	if !sm.IsKnownProcess(pid) {
		return "", false
	}
	return sm.GetProcessName(pid)
}

// GetProcessName returns the process name for a given PID.
// It can resolve names for active processes and processes maked for deletetion (before a scrape).
// If the process is not found, it returns a formatted "unknown" string and false.
func (sm *KernelStateManager) GetProcessName(pid uint32) (string, bool) {
	if val, exists := sm.processes.Load(pid); exists {
		info := val.(*ProcessInfo)
		info.mu.Lock()
		name := info.Name
		info.mu.Unlock()
		return name, true
	}
	return "unknown_" + strconv.FormatUint(uint64(pid), 10), false
}

// GetProcessStartKey returns the unique process start key for a given PID.
func (sm *KernelStateManager) GetProcessStartKey(pid uint32) (uint64, bool) {
	if val, exists := sm.pidToStartKey.Load(pid); exists {
		return val.(uint64), true
	}
	return 0, false
}
