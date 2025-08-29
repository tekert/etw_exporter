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
func (sm *KernelStateManager) AddProcess(pid uint32, name string, parentPID uint32, eventTimestamp time.Time) {
	// If the process was recently terminated, remove it from the terminated list
	// as it might be a PID reuse scenario.
	sm.terminatedProcesses.Delete(pid)

	if val, existed := sm.processes.Load(pid); existed {
		// Update existing process information
		info := val.(*ProcessInfo)
		info.mu.Lock()
		info.Name = name
		info.ParentPID = parentPID
		info.LastSeen = eventTimestamp
		info.mu.Unlock()
		return
	}

	sm.processes.Store(pid, &ProcessInfo{
		PID:       pid,
		Name:      name,
		StartTime: eventTimestamp,
		LastSeen:  eventTimestamp,
		ParentPID: parentPID,
	})

	sm.log.Debug().
		Uint32("pid", pid).
		Str("name", name).
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
	_, exists := sm.processes.Load(pid)
	return exists
}

// GetProcessName returns the process name for a given PID.
// It can resolve names for active processes. If the process is not found,
// it returns a formatted "unknown" string and false.
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
