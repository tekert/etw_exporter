package main

import (
	"strconv"
	"sync"
	"time"

	"github.com/phuslu/log"
	"github.com/tekert/golang-etw/etw"
)

// ProcessInfo holds information about a process
type ProcessInfo struct {
	PID       uint32
	Name      string
	StartTime time.Time
	ParentPID uint32
	ImagePath string
}

// ProcessTracker maintains a centralized mapping of process IDs to process information
// This is shared across all collectors that need process information
type ProcessTracker struct {
	processes sync.Map   // key: uint32 (PID), value: *ProcessInfo
	log    log.Logger // Process tracker logger
}

// Global process tracker instance
var globalProcessTracker *ProcessTracker

// GetGlobalProcessTracker returns the singleton process tracker instance
func GetGlobalProcessTracker() *ProcessTracker {
	if globalProcessTracker == nil {
		globalProcessTracker = &ProcessTracker{
			log: GetProcessLogger(),
		}
	}
	return globalProcessTracker
}

// NewProcessTracker creates a new process tracker instance (for testing or specialized use)
func NewProcessTracker() *ProcessTracker {
	return &ProcessTracker{
		log: GetProcessLogger(),
	}
}

// AddProcess adds or updates process information
func (pt *ProcessTracker) AddProcess(pid uint32, name string, parentPID uint32, imagePath string) {
	processInfo := &ProcessInfo{
		PID:       pid,
		Name:      name,
		StartTime: time.Now(),
		ParentPID: parentPID,
		ImagePath: imagePath,
	}

	// Check if we're updating an existing process
	if existingInfo, existed := pt.processes.Load(pid); existed {
		existing := existingInfo.(*ProcessInfo)
		pt.log.Debug().
			Uint32("pid", pid).
			Str("old_name", existing.Name).
			Str("new_name", name).
			Msg("Process updated in tracker")
	} else {
		pt.log.Debug().
			Uint32("pid", pid).
			Str("name", name).
			Uint32("parent_pid", parentPID).
			Str("image_path", imagePath).
			Msg("Process added to tracker")
	}

	pt.processes.Store(pid, processInfo)
}

// RemoveProcess removes a process from tracking
func (pt *ProcessTracker) RemoveProcess(pid uint32) {
	if info, exists := pt.processes.LoadAndDelete(pid); exists {
		processInfo := info.(*ProcessInfo)
		pt.log.Debug().
			Uint32("pid", pid).
			Str("name", processInfo.Name).
			Msg("Process removed from tracker")
	}
}

// GetProcessInfo returns process information for a given PID
func (pt *ProcessTracker) GetProcessInfo(pid uint32) (*ProcessInfo, bool) {
	if info, exists := pt.processes.Load(pid); exists {
		return info.(*ProcessInfo), true
	}
	return nil, false
}

// GetProcessName returns just the process name for a given PID
func (pt *ProcessTracker) GetProcessName(pid uint32) string {
	if info, exists := pt.GetProcessInfo(pid); exists {
		return info.Name
	}
	return "unknown_" + strconv.FormatUint(uint64(pid), 10)
}

// GetProcessNameWithFallback returns process name with a fallback to PID string
func (pt *ProcessTracker) GetProcessNameWithFallback(pid uint32) string {
	if info, exists := pt.GetProcessInfo(pid); exists {
		return info.Name
	}
	// Fallback to PID as string if process name is not available
	return "pid_" + string(rune(pid))
}

// HandleProcessStart processes process start and rundown events for name tracking
// Process Event: Microsoft-Windows-Kernel-Process
// Provider GUID: {22fb2cd6-0e7b-422b-a0c7-2fad1fd0e716}
// Event ID: 1 (ProcessStart) - New process creation
// Event ID: 15 (ProcessRundown/DCStart) - Existing processes when tracing starts
//
// Properties from manifest (ProcessStartArgs template):
//
//	ProcessID (win:UInt32) - Process identifier
//	CreateTime (win:FILETIME) - Process creation time
//	ParentProcessID (win:UInt32) - Parent process identifier
//	SessionID (win:UInt32) - Session identifier
//	ImageName (win:UnicodeString) - Process image name
//
// Note: ProcessRundown events are crucial for capturing process names of processes
// that were already running when ETW tracing started. Without these events,
// processes that started before tracing would show as "unknown_*" in metrics.
func (pt *ProcessTracker) HandleProcessStart(helper *etw.EventRecordHelper) error {
	processID, _ := helper.GetPropertyUint("ProcessID")
	parentProcessID, _ := helper.GetPropertyUint("ParentProcessID")

	// Try to get process name from different possible property names
	var processName string
	if name, err := helper.GetPropertyString("ImageName"); err == nil {
		processName = name
		pt.log.Trace().
			Uint32("pid", uint32(processID)).
			Str("property", "ImageName").
			Str("name", processName).
			Msg("Retrieved process name from ImageName")
	} else {
		processName = "unknown"
		pt.log.Warn().
			Uint32("pid", uint32(processID)).
			Err(err).
			Msg("Failed to get process name from ImageName property")
	}

	// TODO: processName already has the full path name
	// Try to get full image path
	var imagePath string
	if path, err := helper.GetPropertyString("ImagePathName"); err == nil {
		imagePath = path
	} else if path, err := helper.GetPropertyString("CommandLine"); err == nil {
		imagePath = path
	} else {
		imagePath = ""
		pt.log.Trace().
			Uint32("pid", uint32(processID)).
			Msg("No image path available for process")
	}

	pt.AddProcess(uint32(processID), processName, uint32(parentProcessID), imagePath)
	return nil
}

// HandleProcessEnd processes process end events to clean up the tracker
// Process Event: Microsoft-Windows-Kernel-Process
// Provider GUID: {22fb2cd6-0e7b-422b-a0c7-2fad1fd0e716}
// Event ID: 2 (ProcessStop)
//
// Properties from manifest (ProcessStopArgs template):
//
//	ProcessID (win:UInt32) - Process identifier
//	CreateTime (win:FILETIME) - Process creation time
//	ExitTime (win:FILETIME) - Process exit time
//	ExitCode (win:UInt32) - Process exit code
//	ImageName (win:AnsiString) - Process image name
func (pt *ProcessTracker) HandleProcessEnd(helper *etw.EventRecordHelper) error {
	processID, _ := helper.GetPropertyUint("ProcessID")

	pt.log.Trace().
		Uint32("pid", uint32(processID)).
		Msg("Process end event received")

	pt.RemoveProcess(uint32(processID))
	return nil
}

// GetAllProcesses returns a snapshot of all currently tracked processes
func (pt *ProcessTracker) GetAllProcesses() map[uint32]*ProcessInfo {
	processes := make(map[uint32]*ProcessInfo)
	pt.processes.Range(func(key, value interface{}) bool {
		pid := key.(uint32)
		info := value.(*ProcessInfo)
		processes[pid] = info
		return true
	})
	return processes
}

// GetProcessCount returns the number of currently tracked processes
func (pt *ProcessTracker) GetProcessCount() int {
	count := 0
	pt.processes.Range(func(key, value interface{}) bool {
		count++
		return true
	})
	return count
}

// CleanupStaleProcesses removes processes that haven't been seen for a specified duration
// This helps prevent memory leaks from long-running sessions
func (pt *ProcessTracker) CleanupStaleProcesses(maxAge time.Duration) int {
	cutoff := time.Now().Add(-maxAge)
	var staleProcesses []uint32

	pt.processes.Range(func(key, value interface{}) bool {
		pid := key.(uint32)
		info := value.(*ProcessInfo)
		if info.StartTime.Before(cutoff) {
			staleProcesses = append(staleProcesses, pid)
		}
		return true
	})

	pt.log.Debug().
		Int("stale_count", len(staleProcesses)).
		Dur("max_age", maxAge).
		Msg("Cleaning up stale processes")

	for _, pid := range staleProcesses {
		pt.RemoveProcess(pid)
	}

	if len(staleProcesses) > 0 {
		pt.log.Info().
			Int("cleaned_count", len(staleProcesses)).
			Msg("Stale processes cleaned up")
	}

	return len(staleProcesses)
}
