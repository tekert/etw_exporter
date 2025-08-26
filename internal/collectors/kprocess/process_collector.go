package kprocess

import (
	"strconv"
	"sync"
	"time"

	"etw_exporter/internal/logger"

	"github.com/tekert/goetw/logsampler/adapters"
)

// ProcessInfo holds information about a process
type ProcessInfo struct {
	PID       uint32
	Name      string
	StartTime time.Time
	LastSeen  time.Time // Last seen timestamp for cleanup
	ParentPID uint32

	// Remember to update the Clone() method if you add new fields
	mu sync.Mutex
}

// ProcessCollector maintains the centralized mapping of process IDs to process information
// This is the single source of truth for all process data across the application
type ProcessCollector struct {
	processes sync.Map // key: uint32 (PID), value: *ProcessInfo
	log       *adapters.SampledLogger
}

// Global process collector instance and a sync.Once to ensure thread-safe initialization
var (
	globalProcessCollector *ProcessCollector
	initOnce               sync.Once
)

// GetGlobalProcessCollector returns the singleton process collector instance.
func GetGlobalProcessCollector() *ProcessCollector {
	initOnce.Do(func() {
		globalProcessCollector = &ProcessCollector{
			//log: logger.NewLoggerWithContext("process_collector"),
			log: logger.NewSampledLoggerCtx("process_collector"),
		}
	})
	return globalProcessCollector
}

// NewProcessCollector creates a new process collector instance (for testing or specialized use)
func NewProcessCollector() *ProcessCollector {
	return &ProcessCollector{
		//log: logger.NewLoggerWithContext("process_collector"),
		log: logger.NewSampledLoggerCtx("process_collector"),
	}
}

// AddProcess adds or updates process information. If the process already exists,
// it updates its details and refreshes its LastSeen timestamp.
func (pc *ProcessCollector) AddProcess(pid uint32, name string, parentPID uint32, eventTimestamp time.Time) {
	// Check if we're updating an existing process
	if existingInfo, existed := pc.processes.Load(pid); existed {
		processInfo := existingInfo.(*ProcessInfo)

		// Update existing process information
		processInfo.mu.Lock()
		oldName := processInfo.Name
		processInfo.Name = name
		processInfo.ParentPID = parentPID
		processInfo.LastSeen = eventTimestamp
		processInfo.mu.Unlock()

		pc.log.Debug().
			Uint32("pid", pid).
			Str("old_name", oldName).
			Str("new_name", name).
			Msg("Process updated in collector")

		return
	}

	// Create a new entry if it doesn't exist
	processInfo := &ProcessInfo{
		PID:       pid,
		Name:      name,
		StartTime: eventTimestamp,
		LastSeen:  eventTimestamp,
		ParentPID: parentPID,
	}
	pc.processes.Store(pid, processInfo)

	pc.log.Debug().
		Uint32("pid", pid).
		Str("name", name).
		Uint32("parent_pid", parentPID).
		Msg("Process added to collector")
}

// RemoveProcess removes a process from tracking
func (pc *ProcessCollector) RemoveProcess(pid uint32) {
	if info, exists := pc.processes.LoadAndDelete(pid); exists {
		processInfo := info.(*ProcessInfo)

		processInfo.mu.Lock()
		name := processInfo.Name
		processInfo.mu.Unlock()

		pc.log.Debug().
			Uint32("pid", pid).
			Str("name", name).
			Msg("Process removed from collector")
	}
}

// TODO: optmize this, no clones. get specific data directly.
// GetProcessInfo returns a safe copy of process information for a given PID.
// It returns a copy instead of a pointer to prevent race conditions in calling code.
func (pc *ProcessCollector) GetProcessInfo(pid uint32) (ProcessInfo, bool) {
	if info, exists := pc.processes.Load(pid); exists {
		processInfo := info.(*ProcessInfo)
		// Use the Clone method for a safe and concise copy.
		return processInfo.Clone(), true
	}
	return ProcessInfo{}, false
}

// Clone creates a safe, deep copy of the ProcessInfo struct.
// It locks the original struct during the copy and returns a new
// struct with its own, fresh mutex.
func (pi *ProcessInfo) Clone() ProcessInfo {
	pi.mu.Lock()
	defer pi.mu.Unlock()

	// Return a copy of the data. The 'mu' field in the new struct
	// will be its default zero-value, which is correct.
	return ProcessInfo{
		PID:       pi.PID,
		Name:      pi.Name,
		StartTime: pi.StartTime,
		LastSeen:  pi.LastSeen,
		ParentPID: pi.ParentPID,
	}
}

// GetProcessName returns the process name for a given PID and a flag indicating if it was found
func (pc *ProcessCollector) GetProcessName(pid uint32) (string, bool) {
	if info, exists := pc.GetProcessInfo(pid); exists {
		return info.Name, true
	}
	return "unknown_" + strconv.FormatUint(uint64(pid), 10), false
}

// GetProcessNameWithFallback returns process name with a fallback to PID string
func (pc *ProcessCollector) GetProcessNameWithFallback(pid uint32) string {
	if info, exists := pc.GetProcessInfo(pid); exists {
		return info.Name
	}
	// Fallback to PID as string if process name is not available
	return "pid_" + strconv.FormatUint(uint64(pid), 10)
}

// GetAllProcesses returns a snapshot copy of all currently tracked processes
func (pc *ProcessCollector) GetAllProcesses() map[uint32]*ProcessInfo {
	processes := make(map[uint32]*ProcessInfo)
	pc.processes.Range(func(key, value any) bool {
		pid := key.(uint32)
		info := value.(*ProcessInfo)
		clone := info.Clone()
		processes[pid] = &clone // safe copy
		return true
	})
	return processes
}

// GetProcessCount returns the number of currently tracked processes
func (pc *ProcessCollector) GetProcessCount() int {
	count := 0
	pc.processes.Range(func(key, value any) bool {
		count++
		return true
	})
	return count
}

// CleanupStaleProcesses removes processes that have not been seen for a specified duration.
// This should be called after a process rundown has been triggered and processed.
func (pc *ProcessCollector) CleanupStaleProcesses(lastSeenIn time.Duration) int {
	cutoff := time.Now().Add(-lastSeenIn)
	var pidsToDelete []uint32

	pc.processes.Range(func(key, value any) bool {
		pid := key.(uint32)
		info := value.(*ProcessInfo)

		info.mu.Lock()
		lastSeen := info.LastSeen
		info.mu.Unlock()

		if lastSeen.Before(cutoff) {
			pidsToDelete = append(pidsToDelete, pid)
		}
		return true
	})

	if len(pidsToDelete) == 0 {
		return 0
	}

	var deletedDetails []string
	for _, pid := range pidsToDelete {
		if info, exists := pc.GetProcessInfo(pid); exists {
			deletedDetails = append(deletedDetails, strconv.FormatUint(uint64(pid), 10)+":"+info.Name)
		} else {
			deletedDetails = append(deletedDetails, strconv.FormatUint(uint64(pid), 10)+":<unknown>")
		}
		pc.RemoveProcess(pid)
	}
	pc.log.Debug().
		Int("stale_count", len(pidsToDelete)).
		Dur("max_age", lastSeenIn).
		Strs("deleted", deletedDetails).
		Msg("Cleaning up stale processes not seen in rundown")

	return len(pidsToDelete)
}
