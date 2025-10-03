// This file defines the core data structures for a process: ProcessData and ProcessInfo.
// It acts as the "struct definition" file.
//
// The lifecycle management logic (creating, finding, deleting processes) is located
// in process_manager.go to separate data from behavior.
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
	PID             uint32
	StartKey        uint64 // Uniquely identifies a process instance during a boot session. (it's not CreateTime)
	Name            string
	ServiceName     string    // Service name if process is a service host (e.g. svchost.exe)
	SubProcessTag   uint32    // Service tag from thread events (0 if not a service)
	CreateTime      time.Time // Actual process creation time from the event payload.
	TerminationTime time.Time // The time the process was marked for deletion.
	LastSeen        time.Time // Timestamp of the ETW event (start or rundown)
	ParentPID       uint32
	SessionID       uint32
	UniqueHash      uint32
	mu              sync.Mutex
}

// reset clears the ProcessInfo struct for reuse.
func (pi *ProcessInfo) reset() {
	pi.PID = 0
	pi.ParentPID = 0
	pi.StartKey = 0
	pi.CreateTime = time.Time{}
	pi.TerminationTime = time.Time{}
	pi.LastSeen = time.Time{}
	pi.Name = ""
	pi.ServiceName = ""
	pi.SubProcessTag = 0
	pi.SessionID = 0
	pi.UniqueHash = 0
}

// Clone creates a safe, deep copy of the ProcessInfo struct.
func (pi *ProcessInfo) Clone() ProcessInfo {
	pi.mu.Lock()
	defer pi.mu.Unlock()

	return ProcessInfo{
		PID:             pi.PID,
		StartKey:        pi.StartKey,
		Name:            pi.Name,
		ServiceName:     pi.ServiceName,
		SubProcessTag:   pi.SubProcessTag,
		CreateTime:      pi.CreateTime,
		TerminationTime: pi.TerminationTime,
		LastSeen:        pi.LastSeen,
		ParentPID:       pi.ParentPID,
		SessionID:       pi.SessionID,
		UniqueHash:      pi.UniqueHash,
		// mu is intentionally omitted, the new struct gets its own zero-value mutex.
	}
}

// TODO: instead of mark for deletion, delete it and aggregate by name+checksum+sessionID?

// --- Process Management ---
// All methods like AddProcess, MarkProcessForDeletion, and all getters have been moved
// to the new process_manager.go file to centralize process lifecycle logic.
// This file now only contains the core data structures (ProcessData, ProcessInfo).

// --- Public Getters (StartKey-based) ---
// These methods are now on the ProcessManager and are accessed via the KernelStateManager facade.
// Example: GetGlobalStateManager().GetProcessDataBySK(startKey)

// GetProcessNameBySK returns the process name for a given start key.
// It can resolve names for active processes.
// If the process is not found, it returns a formatted "unknown" string and false.
func (sm *KernelStateManager) GetProcessNameBySK(startKey uint64) (string, bool) {
	if pData, exists := sm.processManager.GetProcessDataBySK(startKey); exists {
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
	if pData, exists := sm.processManager.GetProcessDataBySK(startKey); exists {
		return pData.Info.Clone(), true
	}
	return ProcessInfo{}, false
}

// GetKnownProcessNameBySK retrieves the name for a known/tracked process.
func (sm *KernelStateManager) GetKnownProcessNameBySK(startKey uint64) (string, bool) {
	if pData, exists := sm.processManager.GetProcessDataBySK(startKey); exists {
		pData.Info.mu.Lock()
		name := pData.Info.Name
		pData.Info.mu.Unlock()
		return name, true
	}
	return "", false
}

// --- Public Getters (PID-based, for convenience) ---

// IsKnownProcessID checks if a process with the given PID is being tracked.
func (sm *KernelStateManager) IsKnownProcessID(pid uint32) bool {
	startKey, ok := sm.GetProcessCurrentStartKey(pid)
	if !ok {
		return false
	}
	return sm.IsTrackedStartKey(startKey)
}

// GetProcessNameByID returns the process name for a given PID.
func (sm *KernelStateManager) GetProcessNameByID(pid uint32) (string, bool) {
	if startKey, ok := sm.GetProcessCurrentStartKey(pid); ok {
		return sm.GetProcessNameBySK(startKey)
	}
	return "unknown_pid_" + strconv.FormatUint(uint64(pid), 10), false
}

// GetKnownProcessNameByID is a convenience helper that combines checking if a process
// is known/tracked with retrieving its name.
func (sm *KernelStateManager) GetKnownProcessNameByID(pid uint32) (string, bool) {
	startKey, ok := sm.GetProcessCurrentStartKey(pid)
	if !ok {
		return "", false
	}
	return sm.GetKnownProcessNameBySK(startKey)
}

// IsTrackedStartKey checks if a process with the given start key is being tracked.
func (sm *KernelStateManager) IsTrackedStartKey(startKey uint64) bool {
	_, exists := sm.processManager.GetProcessDataBySK(startKey)
	return exists
}

// --- Debug Provider Methods ---

// GetTerminatedProcessCount returns the number of processes marked for termination.
func (pm *ProcessManager) GetTerminatedProcessCount() int {
	var count int
	pm.terminatedProcesses.Range(func(u uint64, t time.Time) bool {
		count++
		return true
	})
	return count
}
