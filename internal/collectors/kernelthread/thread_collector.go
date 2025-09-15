package kernelthread

import (
	"math/bits"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"

	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"
)

// Define constants for thread states to be used as array indices.
const (
	StateCreated = iota
	StateRunning
	StateReady
	StateTerminated
	StateWaiting // Must be last of the simple states

	// The offset for waiting states. Wait reasons (0-37) will be added to this.
	StateWaitingOffset = 40
)

// ThreadStateKey represents a composite key for thread state transitions.
type ThreadStateKey struct {
	State      string
	WaitReason string
}

// ThreadCollector implements prometheus.Collector for thread-related metrics.
// This collector is designed for extreme performance, using pre-allocation,
// lock-free atomics, and fine-grained locking to minimize overhead on the hot path.
type ThreadCollector struct {
	// Per-CPU data structures for lock-free access on the hot path.
	// Slices are indexed by CPU number.
	contextSwitchesPerCPU  []*atomic.Int64
	contextSwitchIntervals []*IntervalStats

	// Double-buffered maps for lock-free writes on the CSwitch hot path.
	// The handler writes to 'active', the collector reads from 'inactive' after a swap.
	activeCsPerProcess   map[uint64]int64
	inactiveCsPerProcess map[uint64]int64
	csMapMutex           sync.Mutex

	// Optimized state tracking with pre-allocated counters to avoid allocations on the hot path.
	threadStateCounters []*atomic.Int64
	indexToStateKey     []ThreadStateKey

	log *phusluadapter.SampledLogger

	// Metric Descriptors
	contextSwitchesPerCPUDesc     *prometheus.Desc
	contextSwitchesPerProcessDesc *prometheus.Desc
	contextSwitchIntervalsDesc    *prometheus.Desc
	threadStatesDesc              *prometheus.Desc

	// Reference to the global kernel state manager (process, images, threads, etc).
	stateManager *statemanager.KernelStateManager
}

// ThreadMetricsData holds a snapshot of aggregated data for a single scrape.
type ThreadMetricsData struct {
	ContextSwitchesPerCPU     map[uint16]int64
	ContextSwitchesPerProcess map[uint64]int64 // Snapshot of the inactive map after a swap
	ThreadStates              map[ThreadStateKey]int64
	ContextSwitchIntervals    map[uint16]*IntervalStats // Use pointer to avoid copying the lock
}

// ProcessContextSwitches holds context switch data for a process.
type ProcessContextSwitches struct {
	StartKey uint64
	Count    int64
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
func NewThreadCSCollector(sm *statemanager.KernelStateManager) *ThreadCollector {
	numCPU := runtime.NumCPU()

	// Pre-allocate per-CPU slices to avoid allocations and bounds checks in the hot path
	csPerCPU := make([]*atomic.Int64, numCPU)
	csIntervals := make([]*IntervalStats, numCPU)
	for i := range numCPU {
		csPerCPU[i] = new(atomic.Int64)
		csIntervals[i] = &IntervalStats{
			// Pre-allocate the slice to match the number of buckets
			Buckets: make([]int64, len(csIntervalBuckets)),
		}
	}

	// Pre-build structures for allocation-free thread state tracking.
	waitReasons := GetAllWaitReasonStrings()

	// Calculate total size needed for all state counters
	maxIndex := StateWaitingOffset + len(waitReasons)
	threadStateCounters := make([]*atomic.Int64, maxIndex)
	indexToStateKey := make([]ThreadStateKey, maxIndex)

	// Initialize all counters
	for i := range threadStateCounters {
		threadStateCounters[i] = new(atomic.Int64)
	}

	// Map simple states (created, running, ready, terminated)
	simpleStates := []struct {
		Name  string
		Index int
	}{
		{Name: "created", Index: StateCreated},
		{Name: "running", Index: StateRunning},
		{Name: "ready", Index: StateReady},
		{Name: "terminated", Index: StateTerminated},
	}

	for _, s := range simpleStates {
		indexToStateKey[s.Index] = ThreadStateKey{State: s.Name, WaitReason: "none"}
	}

	// Map waiting states with all wait reasons
	for i, reason := range waitReasons {
		idx := StateWaitingOffset + i
		indexToStateKey[idx] = ThreadStateKey{State: "waiting", WaitReason: reason}
	}

	collector := &ThreadCollector{
		stateManager:           sm,
		contextSwitchesPerCPU:  csPerCPU,
		contextSwitchIntervals: csIntervals,
		activeCsPerProcess:     make(map[uint64]int64, 4096),
		inactiveCsPerProcess:   make(map[uint64]int64, 4096),
		threadStateCounters:    threadStateCounters,
		indexToStateKey:        indexToStateKey,
		log:                    logger.NewSampledLoggerCtx("thread_collector"),

		// Initialize descriptors once
		contextSwitchesPerCPUDesc: prometheus.NewDesc(
			"etw_thread_context_switches_cpu_total",
			"Total number of context switches per CPU",
			[]string{"cpu"}, nil,
		),
		contextSwitchesPerProcessDesc: prometheus.NewDesc(
			"etw_thread_context_switches_process_total",
			"Total number of context switches per program.",
			[]string{"process_name", "image_checksum", "session_id"}, nil,
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

	// Register the collector with the state manager for post-scrape cleanup.
	sm.RegisterCleaner(collector)

	return collector
}

// Describe implements prometheus.Collector.
// It sends the descriptors of all the metrics the collector can possibly export
// to the provided channel. This is called once during registration.
func (c *ThreadCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.contextSwitchesPerCPUDesc
	ch <- c.contextSwitchesPerProcessDesc
	ch <- c.contextSwitchIntervalsDesc
	ch <- c.threadStatesDesc
}

// Collect implements prometheus.Collector.
// It is called by Prometheus on each scrape and must create new metrics each time
// to avoid race conditions and ensure stale metrics are not exposed.
func (c *ThreadCollector) Collect(ch chan<- prometheus.Metric) {
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

	// --- Per-Program Context Switch Metrics (with on-the-fly aggregation) ---
	type programKey struct {
		name      string
		checksum  string
		sessionID string
	}
	aggregatedSwitches := make(map[programKey]int64)
	stateManager := c.stateManager

	for startKey, count := range data.ContextSwitchesPerProcess {
		if procInfo, ok := stateManager.GetProcessInfoBySK(startKey); ok {
			key := programKey{
				name:      procInfo.Name,
				checksum:  "0x" + strconv.FormatUint(uint64(procInfo.ImageChecksum), 16),
				sessionID: strconv.FormatUint(uint64(procInfo.SessionID), 10),
			}
			aggregatedSwitches[key] += count
		}
	}

	for key, count := range aggregatedSwitches {
		ch <- prometheus.MustNewConstMetric(
			c.contextSwitchesPerProcessDesc,
			prometheus.CounterValue,
			float64(count),
			key.name,
			key.checksum,
			key.sessionID,
		)
	}

	// Create context switch interval histograms
	for cpu, stats := range data.ContextSwitchIntervals {
		// Convert the individual bucket counts to the cumulative counts required by Prometheus.
		buckets := make(map[float64]uint64, len(csIntervalBuckets))
		var cumulativeCount uint64
		for i, count := range stats.Buckets {
			cumulativeCount += uint64(count)
			buckets[csIntervalBuckets[i]] = cumulativeCount
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
func (c *ThreadCollector) collectData() ThreadMetricsData {
	data := ThreadMetricsData{
		ContextSwitchesPerCPU:  make(map[uint16]int64),
		ThreadStates:           make(map[ThreadStateKey]int64),
		ContextSwitchIntervals: make(map[uint16]*IntervalStats), // Use pointer to avoid copying the lock
	}

	// Collect CPU context switches
	for cpu, countPtr := range c.contextSwitchesPerCPU {
		if countPtr != nil {
			data.ContextSwitchesPerCPU[uint16(cpu)] = countPtr.Load()
		}
	}

	// --- Swap and collect process context switches ---
	c.csMapMutex.Lock()
	// Swap the active and inactive maps. The handler continues writing to the new active map.
	c.activeCsPerProcess, c.inactiveCsPerProcess = c.inactiveCsPerProcess, c.activeCsPerProcess
	c.csMapMutex.Unlock()

	// The collector now has exclusive access to the inactive map for this scrape.
	data.ContextSwitchesPerProcess = c.inactiveCsPerProcess // This is now a map[uint64]int64

	// After the data is collected, clear the inactive map so it's ready for the next swap.
	// This is crucial to reset the counters for the next collection interval.
	for k := range c.inactiveCsPerProcess {
		delete(c.inactiveCsPerProcess, k)
	}

	// Collect thread states from the pre-allocated slice
	for i, key := range c.indexToStateKey {
		count := c.threadStateCounters[i].Load()
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
// - startKey: The unique start key of the process
// - interval: Time since last context switch on this CPU
func (c *ThreadCollector) RecordContextSwitch(
	cpu uint16,
	newThreadID uint32,
	startKey uint64,
	interval time.Duration) {

	// Record context switch per CPU (lock-free)
	if int(cpu) < len(c.contextSwitchesPerCPU) {
		c.contextSwitchesPerCPU[cpu].Add(1)
	}

	// Record context switch per process (lock-free on hot path)
	if startKey > 0 {
		// Only create metrics for processes that are tracked
		if c.stateManager.IsTrackedStartKey(startKey) {
			// This write is not protected by a lock. It is safe because this handler
			// is guaranteed to be called from a single goroutine. The only contention
			// is with the Collect method, which is handled by swapping map pointers.
			c.activeCsPerProcess[startKey]++
		}
	}

	// Record context switch interval (fine-grained lock)
	if interval > 0 && int(cpu) < len(c.contextSwitchIntervals) {
		// Convert to microseconds for integer-based bit manipulation.
		intervalUs := uint64(interval / time.Microsecond)

		stats := c.contextSwitchIntervals[cpu]
		stats.mu.Lock()

		stats.Count++
		stats.Sum += float64(interval.Nanoseconds()) / 1_000_000.0

		// Optimized bucket update using bit manipulation. This is an O(1) operation.
		// For a value `n`, `bits.Len64(n)-1` is equivalent to `floor(log2(n))`,
		// which directly gives the index into our power-of-two bucket array.
		// Buckets (µs): [1, 2, 4, 8, ...] -> Indices: [0, 1, 2, 3, ...]
		//
		// Example: intervalUs = 8 (1000 in binary)
		//   - bits.Len64(8) = 4
		//   - idx = 4 - 1 = 3. Correctly maps to bucket index 3 (value 8µs).
		//
		// Example: intervalUs = 7 (0111 in binary)
		//   - bits.Len64(7) = 3
		//   - idx = 3 - 1 = 2. Correctly maps to bucket index 2 (value 4µs), as it's < 8.
		//     Prometheus histograms are cumulative, so this is correct; it will be counted
		//     in the le="8" bucket.
		var idx int
		if intervalUs > 0 {
			idx = bits.Len64(intervalUs) - 1
		}
		// For intervalUs = 0, idx remains 0.

		// Increment the single correct bucket. Prometheus handles the cumulative sum.
		if idx < len(stats.Buckets) {
			stats.Buckets[idx]++
		} else {
			// If the interval is larger than our largest bucket, increment the last bucket (+Inf).
			stats.Buckets[len(stats.Buckets)-1]++
		}
		stats.mu.Unlock()
	}
}

// RecordThreadStateTransition records a thread state transition event.
//
// Parameters:
// - state: The thread state (e.g., "ready", "waiting", "running", "terminated")
// - waitReason: The wait reason if state is "waiting", otherwise "none"
func (c *ThreadCollector) RecordThreadStateTransition(state int, waitReason int8) {
	// This hot path is now an allocation-free, lock-free, and hash-free array lookup.
	var idx int
	if state == StateWaiting {
		// For waiting states, the index is a direct calculation.
		idx = StateWaitingOffset + int(waitReason)
	} else {
		// For simple states, the state constant is the index.
		idx = state
	}

	if idx < len(c.threadStateCounters) {
		c.threadStateCounters[idx].Add(1)
	}
}

// RecordThreadCreation records a thread creation event.
// This is a convenience method that records a "created" state transition.
func (c *ThreadCollector) RecordThreadCreation() {
	c.RecordThreadStateTransition(StateCreated, 0)
}

// RecordThreadTermination records a thread termination event.
// This is a convenience method that records a "terminated" state transition.
func (c *ThreadCollector) RecordThreadTermination() {
	c.RecordThreadStateTransition(StateTerminated, 0)
}

// CleanupTerminatedProcesses implements the statemanager.PostScrapeCleaner interface.
// This method is called by the KernelStateManager after a scrape is complete to
// allow the collector to safely clean up its internal state for terminated processes.
func (c *ThreadCollector) CleanupTerminatedProcesses(terminatedProcs map[uint64]uint32) {
	cleanedCount := 0
	// The map of terminated processes gives us StartKey -> PID.
	// We must lock to safely clean keys from both buffers.
	c.csMapMutex.Lock()
	for startKey := range terminatedProcs {
		// A key could be in either map, so we delete from both.
		delete(c.activeCsPerProcess, startKey)
		delete(c.inactiveCsPerProcess, startKey)
		cleanedCount++
	}
	c.csMapMutex.Unlock()

	if cleanedCount > 0 {
		c.log.Debug().Int("count", cleanedCount).Msg("Cleaned up context switch counters for terminated processes")
	}
}
