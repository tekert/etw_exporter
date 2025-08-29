package kernelthread

import (
	"math"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/phuslu/log"
	"github.com/prometheus/client_golang/prometheus"

	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"
)

// ThreadStateKey represents a composite key for thread state transitions.
type ThreadStateKey struct {
	State      string
	WaitReason string
}

// ThreadCSCollector implements prometheus.Collector for thread-related metrics.
// This collector is designed for extreme performance, using pre-allocation,
// lock-free atomics, and fine-grained locking to minimize overhead on the hot path.
type ThreadCSCollector struct {
	// Per-CPU data structures for lock-free access on the hot path.
	// Slices are indexed by CPU number.
	contextSwitchesPerCPU  []*int64
	contextSwitchIntervals []*IntervalStats

	// A concurrent map is used for process-level metrics due to the dynamic
	// and sparse nature of Process IDs.
	contextSwitchesPerProcess sync.Map // key: uint32 (PID), value: *int64

	// Optimized state tracking with pre-allocated counters to avoid allocations on the hot path.
	threadStateCounters []*int64
	stateKeyToIndex     map[ThreadStateKey]int
	indexToStateKey     []ThreadStateKey

	log log.Logger

	// Metric Descriptors
	contextSwitchesPerCPUDesc     *prometheus.Desc
	contextSwitchesPerProcessDesc *prometheus.Desc
	contextSwitchIntervalsDesc    *prometheus.Desc
	threadStatesDesc              *prometheus.Desc
}

// ThreadMetricsData holds a snapshot of aggregated data for a single scrape.
type ThreadMetricsData struct {
	ContextSwitchesPerCPU     map[uint16]int64
	ContextSwitchesPerProcess map[uint32]ProcessContextSwitches
	ThreadStates              map[ThreadStateKey]int64
	ContextSwitchIntervals    map[uint16]*IntervalStats // Use pointer to avoid copying the lock
}

// ProcessContextSwitches holds context switch data for a process.
type ProcessContextSwitches struct {
	ProcessID   uint32
	ProcessName string
	Count       int64
}

// IntervalStats holds statistical data for context switch intervals.
type IntervalStats struct {
	mu      sync.Mutex // Protects access to the fields below
	Count   int64
	Sum     float64
	Buckets []int64 // Use a slice for O(1) index access on the hot path
}

// Define histogram buckets in milliseconds (exponential buckets from 1μs to ~16.4ms)
var csIntervalBuckets = prometheus.ExponentialBuckets(0.001, 2, 15)

// NewThreadCSCollector creates a new thread metrics custom collector.
func NewThreadCSCollector() *ThreadCSCollector {
	numCPU := runtime.NumCPU()

	// Pre-allocate per-CPU slices to avoid allocations and bounds checks in the hot path
	csPerCPU := make([]*int64, numCPU)
	csIntervals := make([]*IntervalStats, numCPU)
	for i := range numCPU {
		csPerCPU[i] = new(int64)
		csIntervals[i] = &IntervalStats{
			// Pre-allocate the slice to match the number of buckets
			Buckets: make([]int64, len(csIntervalBuckets)),
		}
	}

	// Pre-build structures for allocation-free thread state tracking.
	// The set of states and wait reasons is small and fixed.
	states := []string{"created", "running", "waiting", "terminated"}
	waitReasons := GetAllWaitReasonStrings()

	stateKeyToIndex := make(map[ThreadStateKey]int)
	indexToStateKey := make([]ThreadStateKey, 0)
	idx := 0

	// Non-waiting states are always paired with "none"
	for _, state := range states {
		if state != "waiting" {
			key := ThreadStateKey{State: state, WaitReason: "none"}
			stateKeyToIndex[key] = idx
			indexToStateKey = append(indexToStateKey, key)
			idx++
		}
	}
	// The "waiting" state is paired with all valid wait reasons
	for _, reason := range waitReasons {
		key := ThreadStateKey{State: "waiting", WaitReason: reason}
		stateKeyToIndex[key] = idx
		indexToStateKey = append(indexToStateKey, key)
		idx++
	}

	threadStateCounters := make([]*int64, len(indexToStateKey))
	for i := range threadStateCounters {
		threadStateCounters[i] = new(int64)
	}

	return &ThreadCSCollector{
		contextSwitchesPerCPU:  csPerCPU,
		contextSwitchIntervals: csIntervals,
		threadStateCounters:    threadStateCounters,
		stateKeyToIndex:        stateKeyToIndex,
		indexToStateKey:        indexToStateKey,
		log:                    logger.NewLoggerWithContext("thread_collector"),

		// Initialize descriptors once
		contextSwitchesPerCPUDesc: prometheus.NewDesc(
			"etw_thread_context_switches_cpu_total",
			"Total number of context switches per CPU",
			[]string{"cpu"}, nil,
		),
		contextSwitchesPerProcessDesc: prometheus.NewDesc(
			"etw_thread_context_switches_process_total",
			"Total number of context switches per process",
			[]string{"process_id", "process_name"}, nil,
		),
		contextSwitchIntervalsDesc: prometheus.NewDesc(
			"etw_thread_context_switch_interval_milliseconds",
			"Histogram of context switch intervals per CPU (time between consecutive switches) in milliseconds.",
			[]string{"cpu"}, nil,
		),
		threadStatesDesc: prometheus.NewDesc(
			"etw_thread_states_total",
			"Total count of thread state transitions by state and wait reason",
			[]string{"state", "wait_reason"}, nil,
		),
	}
}

// Describe implements prometheus.Collector.
// It sends the descriptors of all the metrics the collector can possibly export
// to the provided channel. This is called once during registration.
func (c *ThreadCSCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.contextSwitchesPerCPUDesc
	ch <- c.contextSwitchesPerProcessDesc
	ch <- c.contextSwitchIntervalsDesc
	ch <- c.threadStatesDesc
}

// Collect implements prometheus.Collector.
// It is called by Prometheus on each scrape and must create new metrics each time
// to avoid race conditions and ensure stale metrics are not exposed.
func (c *ThreadCSCollector) Collect(ch chan<- prometheus.Metric) {
	// The cleanup of thread and process mappings is now handled exclusively by the
	// ProcessCleanupCollector, which runs after all other collectors have finished
	// their scrapes. This ensures data consistency and prevents race conditions.
	// Therefore, no cleanup logic is needed here.

	data := c.collectData()

	// Create context switches per CPU metrics
	for cpu, count := range data.ContextSwitchesPerCPU {
		ch <- prometheus.MustNewConstMetric(
			c.contextSwitchesPerCPUDesc,
			prometheus.CounterValue,
			float64(count),
			strconv.FormatUint(uint64(cpu), 10),
		)
	}

	// Create context switches per process metrics
	for _, procData := range data.ContextSwitchesPerProcess {
		ch <- prometheus.MustNewConstMetric(
			c.contextSwitchesPerProcessDesc,
			prometheus.CounterValue,
			float64(procData.Count),
			strconv.FormatUint(uint64(procData.ProcessID), 10),
			procData.ProcessName,
		)
	}

	// Create context switch interval histograms
	for cpu, stats := range data.ContextSwitchIntervals {
		// Convert the bucket slice to the map format required by Prometheus
		buckets := make(map[float64]uint64, len(csIntervalBuckets))
		for i, count := range stats.Buckets {
			buckets[csIntervalBuckets[i]] = uint64(count)
		}

		ch <- prometheus.MustNewConstHistogram(
			c.contextSwitchIntervalsDesc,
			uint64(stats.Count),
			stats.Sum,
			buckets,
			strconv.FormatUint(uint64(cpu), 10),
		)
	}

	// Create thread state metrics
	for stateKey, count := range data.ThreadStates {
		ch <- prometheus.MustNewConstMetric(
			c.threadStatesDesc,
			prometheus.CounterValue,
			float64(count),
			stateKey.State,
			stateKey.WaitReason,
		)
	}
}

// collectData creates a snapshot of current metrics data.
// This method is called during metric collection to ensure consistent data.
func (c *ThreadCSCollector) collectData() ThreadMetricsData {
	data := ThreadMetricsData{
		ContextSwitchesPerCPU:     make(map[uint16]int64),
		ContextSwitchesPerProcess: make(map[uint32]ProcessContextSwitches),
		ThreadStates:              make(map[ThreadStateKey]int64),
		ContextSwitchIntervals:    make(map[uint16]*IntervalStats), // Use pointer to avoid copying the lock
	}

	// Collect CPU context switches
	for cpu, countPtr := range c.contextSwitchesPerCPU {
		if countPtr != nil {
			data.ContextSwitchesPerCPU[uint16(cpu)] = atomic.LoadInt64(countPtr)
		}
	}

	// Collect process context switches
	stateManager := statemanager.GetGlobalStateManager()
	c.contextSwitchesPerProcess.Range(func(key, val any) bool {
		pid := key.(uint32)
		countPtr := val.(*int64)
		if countPtr != nil {
			// Only create metrics for processes that are still known at scrape time
			// PID-Name mappings are retained until after scrap by the state manager.
			if processName, isKnown := stateManager.GetProcessName(pid); isKnown {
				count := atomic.LoadInt64(countPtr)
				data.ContextSwitchesPerProcess[pid] = ProcessContextSwitches{
					ProcessID:   pid,
					ProcessName: processName,
					Count:       count,
				}
			}
		}
		return true
	})

	// Collect thread states from the pre-allocated slice
	for i, key := range c.indexToStateKey {
		count := atomic.LoadInt64(c.threadStateCounters[i])
		if count > 0 {
			data.ThreadStates[key] = count
		}
	}

	// Collect context switch interval statistics
	for cpu, stats := range c.contextSwitchIntervals {
		if stats != nil {
			stats.mu.Lock()
			// Create a deep copy of the stats under the per-CPU lock
			copiedStats := &IntervalStats{
				Count:   stats.Count,
				Sum:     stats.Sum,
				Buckets: make([]int64, len(csIntervalBuckets)),
			}
			copy(copiedStats.Buckets, stats.Buckets)
			stats.mu.Unlock()
			data.ContextSwitchIntervals[uint16(cpu)] = copiedStats
		}
	}

	return data
}

// RecordContextSwitch records a context switch event.
//
// Parameters:
// - cpu: CPU number where the context switch occurred
// - newThreadID: Thread ID of the thread being switched to
// - processID: Process ID that owns the new thread (0 if unknown)
// - interval: Time since last context switch on this CPU
func (c *ThreadCSCollector) RecordContextSwitch(
	cpu uint16,
	newThreadID uint32,
	processID uint32,
	interval time.Duration) {

	// Record context switch per CPU (lock-free)
	if int(cpu) < len(c.contextSwitchesPerCPU) {
		atomic.AddInt64(c.contextSwitchesPerCPU[cpu], 1)
	}

	// Record context switch per process (concurrent map)
	if processID > 0 {
		stateManager := statemanager.GetGlobalStateManager()
		// Only create metrics for processes that are still known at scrape time
		// PID-Name mappings are retained until after scrap by the state manager.
		if stateManager.IsKnownProcess(processID) {
			val, _ := c.contextSwitchesPerProcess.LoadOrStore(processID, new(int64))
			atomic.AddInt64(val.(*int64), 1)
		}
	}

	// Record context switch interval (fine-grained lock)
	if interval > 0 && int(cpu) < len(c.contextSwitchIntervals) {
		intervalMs := float64(interval.Nanoseconds()) / 1_000_000.0

		stats := c.contextSwitchIntervals[cpu]
		stats.mu.Lock()

		stats.Count++
		stats.Sum += intervalMs

		// // Optimized bucket update using binary search.
		// // This is much faster than a linear scan for every event.
		// // Find the index of the first bucket that the interval fits into.
		// idx := sort.Search(len(csIntervalBuckets), func(i int) bool { return csIntervalBuckets[i] >= intervalMs })

		// Optimized bucket update using a direct mathematical calculation.
		// This is faster than a search for exponential buckets.
		var idx int
		if intervalMs > csIntervalBuckets[0] {
			// Calculate the index via logarithm for intervals larger than the first bucket.
			// The constant is 1.0 / 0.001, which is the base of our exponential scale.
			idx = int(math.Ceil(math.Log2(intervalMs * 1000)))
		}
		// For intervals smaller than the first bucket, idx remains 0.

		// Increment all buckets from that index onwards.
		for i := idx; i < len(csIntervalBuckets); i++ {
			stats.Buckets[i]++
		}
		stats.mu.Unlock()
	}
}

// RecordThreadStateTransition records a thread state transition event.
//
// Parameters:
// - state: The thread state (e.g., "ready", "waiting", "running", "terminated")
// - waitReason: The wait reason if state is "waiting", otherwise "none"
func (c *ThreadCSCollector) RecordThreadStateTransition(state, waitReason string) {
	// This hot path is allocation-free and lock-free.
	stateKey := ThreadStateKey{State: state, WaitReason: waitReason}
	if idx, ok := c.stateKeyToIndex[stateKey]; ok {
		atomic.AddInt64(c.threadStateCounters[idx], 1)
	}
}

// RecordThreadCreation records a thread creation event.
// This is a convenience method that records a "created" state transition.
func (c *ThreadCSCollector) RecordThreadCreation() {
	c.RecordThreadStateTransition("created", "none")
}

// RecordThreadTermination records a thread termination event.
// This is a convenience method that records a "terminated" state transition.
func (c *ThreadCSCollector) RecordThreadTermination() {
	c.RecordThreadStateTransition("terminated", "none")
}
