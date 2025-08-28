package kernelprocess

import (
	"strconv"
	"sync"
	"time"

	"etw_exporter/internal/logger"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
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

// ProcessCollector maintains the centralized mapping of process IDs to process information.
// It is the single source of truth for all process data and manages the lifecycle
// of process information, including a delayed cleanup mechanism to prevent race
// conditions with Prometheus scrapes.
type ProcessCollector struct {
	processes           sync.Map // key: uint32 (PID), value: *ProcessInfo
	terminatedProcesses sync.Map // key: uint32 (PID), value: time.Time (when marked for deletion)
	log                 *phusluadapter.SampledLogger
	mu                  sync.Mutex // Protects the cleanup logic
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

// ProcessCleanupCollector is a sentinel collector that triggers cleanup after a scrape.
type ProcessCleanupCollector struct {
	pc *ProcessCollector
}

// NewProcessCleanupCollector creates a new sentinel collector.
func NewProcessCleanupCollector() *ProcessCleanupCollector {
	return &ProcessCleanupCollector{
		pc: GetGlobalProcessCollector(),
	}
}

// Describe does nothing. It's a sentinel collector.
func (c *ProcessCleanupCollector) Describe(ch chan<- *prometheus.Desc) {}

// Collect triggers the post-scrape cleanup in the ProcessCollector.
// This method is called by Prometheus during a scrape. By registering this
// collector last, we ensure cleanup happens after all other collectors are done.
func (c *ProcessCleanupCollector) Collect(ch chan<- prometheus.Metric) {
	c.pc.PostScrapeCleanup()
}

// AddProcess adds or updates process information. If the process already exists,
// it updates its details and refreshes its LastSeen timestamp.
func (pc *ProcessCollector) AddProcess(pid uint32, name string, parentPID uint32, eventTimestamp time.Time) {
	// If the process was recently terminated, remove it from the terminated list
	// as it might be a PID reuse scenario.
	pc.terminatedProcesses.Delete(pid)

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

// MarkProcessForDeletion moves a process to the terminated list for delayed cleanup.
// It does NOT immediately delete the process info, allowing in-flight scrapes to
// complete successfully.
func (pc *ProcessCollector) MarkProcessForDeletion(pid uint32) {
	if _, exists := pc.processes.Load(pid); exists {
		pc.terminatedProcesses.Store(pid, time.Now())
		pc.log.Trace().Uint32("pid", pid).Msg("Process marked for deletion post-scrape")
	}
}

// IsKnownProcess checks if a process with the given PID is being tracked.
// This is a fast, allocation-free check.
func (pc *ProcessCollector) IsKnownProcess(pid uint32) bool {
	_, exists := pc.processes.Load(pid)
	return exists
}

// GetProcessName returns the process name for a given PID.
// It can resolve names for active and recently terminated processes.
func (pc *ProcessCollector) GetProcessName(pid uint32) (string, bool) {
	if info, exists := pc.processes.Load(pid); exists {
		processInfo := info.(*ProcessInfo)
		processInfo.mu.Lock()
		name := processInfo.Name
		processInfo.mu.Unlock()
		return name, true
	}
	return "unknown_" + strconv.FormatUint(uint64(pid), 10), false
}

// GetProcessStartTime returns the start time for a given PID.
func (pc *ProcessCollector) GetProcessStartTime(pid uint32) (time.Time, bool) {
	if info, exists := pc.processes.Load(pid); exists {
		processInfo := info.(*ProcessInfo)
		processInfo.mu.Lock()
		startTime := processInfo.StartTime
		processInfo.mu.Unlock()
		return startTime, true
	}
	return time.Time{}, false
}

// GetProcessNameWithFallback returns process name with a fallback to PID string
func (pc *ProcessCollector) GetProcessNameWithFallback(pid uint32) string {
	if name, exists := pc.GetProcessName(pid); exists {
		return name
	}
	// Fallback to PID as string if process name is not available
	return "pid_" + strconv.FormatUint(uint64(pid), 10)
}

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

// PostScrapeCleanup performs the actual deletion of processes that were marked.
// This should be called by the sentinel collector AFTER a Prometheus scrape is complete.
func (pc *ProcessCollector) PostScrapeCleanup() {
	pc.mu.Lock()
	defer pc.mu.Unlock()

	var cleanedCount int
	pc.terminatedProcesses.Range(func(key, value any) bool {
		pid := key.(uint32)
		pc.processes.Delete(pid)
		pc.terminatedProcesses.Delete(pid)
		cleanedCount++
		return true
	})

	if cleanedCount > 0 {
		pc.log.Debug().Int("count", cleanedCount).Msg("Post-scrape cleanup of terminated processes complete")
	}
}

// CleanupStaleProcesses marks processes that have not been seen for a specified duration for deletion.
func (pc *ProcessCollector) CleanupStaleProcesses(lastSeenIn time.Duration) int {
	cutoff := time.Now().Add(-lastSeenIn)
	var pidsToMark []uint32

	pc.processes.Range(func(key, value any) bool {
		pid := key.(uint32)
		info := value.(*ProcessInfo)

		info.mu.Lock()
		lastSeen := info.LastSeen
		info.mu.Unlock()

		if lastSeen.Before(cutoff) {
			pidsToMark = append(pidsToMark, pid)
		}
		return true
	})

	if len(pidsToMark) == 0 {
		return 0
	}

	for _, pid := range pidsToMark {
		pc.MarkProcessForDeletion(pid)
	}
	pc.log.Debug().
		Int("stale_count", len(pidsToMark)).
		Dur("max_age", lastSeenIn).
		Msg("Marked stale processes for cleanup post-scrape")

	return len(pidsToMark)
}
