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

	"etw_exporter/internal/config"
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
	contextSwitchesPerCPU []*atomic.Int64
	// contextSwitchIntervals uses atomic pointers for lock-free double-buffering.
	contextSwitchIntervals []*atomic.Pointer[IntervalStats]
	intervalStatsPool      sync.Pool

	// Per-process context switch counting is now delegated to the KernelStateManager.
	// The double-buffering mechanism (activeCsPerProcess, csPerProcessPool, keysForCleanup, csMapMutex)
	// has been removed to simplify the collector and centralize state management.

	// Optimized state tracking with pre-allocated counters to avoid allocations on the hot path.
	threadStateCounters []*atomic.Int64
	indexToStateKey     []ThreadStateKey

	log    *phusluadapter.SampledLogger
	config *config.ThreadCSConfig

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
// All fields use atomics to allow for lock-free updates on the hot path.
type IntervalStats struct {
	Count   atomic.Int64
	Sum     atomic.Uint64 // Sum of intervals in nanoseconds
	Buckets []atomic.Int64
}

// Reset clears the stats for reuse by the sync.Pool.
func (is *IntervalStats) Reset() {
	is.Count.Store(0)
	is.Sum.Store(0)
	for i := range is.Buckets {
		is.Buckets[i].Store(0)
	}
}

// Define histogram buckets in milliseconds (exponential buckets from 1μs to ~16.4ms)
var csIntervalBuckets = prometheus.ExponentialBuckets(0.001, 2, 15)

// NewThreadCSCollector creates a new thread metrics custom collector.
func NewThreadCSCollector(config *config.ThreadCSConfig, sm *statemanager.KernelStateManager) *ThreadCollector {
	numCPU := runtime.NumCPU()

	// Pre-allocate per-CPU slices to avoid allocations and bounds checks in the hot path
	csPerCPU := make([]*atomic.Int64, numCPU)
	csIntervals := make([]*atomic.Pointer[IntervalStats], numCPU)
	for i := range numCPU {
		csPerCPU[i] = new(atomic.Int64)

		// For each CPU, create an initial IntervalStats object and store it in an atomic pointer.
		is := &IntervalStats{
			Buckets: make([]atomic.Int64, len(csIntervalBuckets)),
		}
		ptr := new(atomic.Pointer[IntervalStats])
		ptr.Store(is)
		csIntervals[i] = ptr
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
		config:                 config,
		contextSwitchesPerCPU:  csPerCPU,
		contextSwitchIntervals: csIntervals,
		intervalStatsPool: sync.Pool{
			New: func() any {
				return &IntervalStats{
					Buckets: make([]atomic.Int64, len(csIntervalBuckets)),
				}
			},
		},
		threadStateCounters: threadStateCounters,
		indexToStateKey:     indexToStateKey,
		log:                 logger.NewSampledLoggerCtx("thread_collector"),

		// Initialize descriptors once
		contextSwitchesPerCPUDesc: prometheus.NewDesc(
			"etw_thread_context_switches_cpu_total",
			"Total number of context switches per CPU",
			[]string{"cpu"}, nil,
		),
		contextSwitchesPerProcessDesc: prometheus.NewDesc(
			"etw_thread_context_switches_process_total",
			"Total number of context switches per program.",
			[]string{"process_name", "service_name", "pe_checksum", "session_id"}, nil,
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

	// The collector no longer manages per-process state directly, so it does not
	// need to register as a PostScrapeCleaner.

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
	// --- System-Wide Metrics ---

	// Create context switches per CPU metrics
	for cpu, countPtr := range c.contextSwitchesPerCPU {
		if count := countPtr.Load(); count > 0 {
			ch <- prometheus.MustNewConstMetric(
				c.contextSwitchesPerCPUDesc,
				prometheus.CounterValue,
				float64(count),
				strconv.FormatUint(uint64(cpu), 10),
			)
		}
	}

	// --- Per-Program Context Switch Metrics (reading from pre-aggregated state) ---
	c.stateManager.RangeAggregatedMetrics(func(key statemanager.ProgramAggregationKey, metrics *statemanager.AggregatedProgramMetrics) bool {
		if metrics.Threads != nil && metrics.Threads.ContextSwitches > 0 {
			ch <- prometheus.MustNewConstMetric(
				c.contextSwitchesPerProcessDesc,
				prometheus.CounterValue,
				float64(metrics.Threads.ContextSwitches),
				key.Name,
				key.ServiceName,
				"0x"+strconv.FormatUint(uint64(key.PeChecksum), 16),
				strconv.FormatUint(uint64(key.SessionID), 10),
			)
		}
		return true // Continue iteration
	})

	// Create context switch interval histograms
	for cpu, statsPtr := range c.contextSwitchIntervals {
		// Get a new, clean stats object from the pool to swap in.
		newStats := c.intervalStatsPool.Get().(*IntervalStats)
		newStats.Reset()

		// Atomically swap the new stats object with the old one.
		// We now have exclusive ownership of oldStats and can read its values safely.
		oldStats := statsPtr.Swap(newStats)

		count := oldStats.Count.Load()
		if count == 0 {
			// If there was no activity, put both objects back in the pool.
			c.intervalStatsPool.Put(newStats)
			c.intervalStatsPool.Put(oldStats)
			continue
		}

		// Convert the nanosecond sum to the millisecond sum required by the histogram.
		sum := float64(oldStats.Sum.Load()) / 1_000_000.0

		// Convert the individual bucket counts to the cumulative counts required by Prometheus.
		buckets := make(map[float64]uint64, len(csIntervalBuckets))
		var cumulativeCount uint64
		for i := range oldStats.Buckets {
			cumulativeCount += uint64(oldStats.Buckets[i].Load())
			buckets[csIntervalBuckets[i]] = cumulativeCount
		}

		ch <- prometheus.MustNewConstHistogram(
			c.contextSwitchIntervalsDesc,
			uint64(count),
			sum,
			buckets,
			strconv.FormatUint(uint64(cpu), 10),
		)

		// After processing, return the old stats object to the pool for reuse.
		c.intervalStatsPool.Put(oldStats)
	}

	// Create thread state metrics
	for i, key := range c.indexToStateKey {
		if count := c.threadStateCounters[i].Load(); count > 0 {
			ch <- prometheus.MustNewConstMetric(
				c.threadStatesDesc,
				prometheus.CounterValue,
				float64(count),
				key.State,
				key.WaitReason,
			)
		}
	}

	c.log.Debug().Msg("Collected thread metrics")
}

// RecordContextSwitch records a context switch event.
//
// Parameters:
//   - cpu: CPU number where the context switch occurred
//   - startKey: The unique start key of the process
//   - interval: Time since last context switch on this CPU
func (c *ThreadCollector) RecordContextSwitch(
	cpu uint16,
	startKey uint64,
	interval time.Duration) {

	// Record context switch per CPU (lock-free)
	if int(cpu) < len(c.contextSwitchesPerCPU) {
		c.contextSwitchesPerCPU[cpu].Add(1)
	}

	// Record context switch per process by delegating to the state manager.
	if startKey > 0 {
		if pData, ok := c.stateManager.GetProcessDataBySK(startKey); ok {
			pData.RecordContextSwitch()
		}
	}

	// Record context switch interval (now completely lock-free)
	if interval > 0 && int(cpu) < len(c.contextSwitchIntervals) {
		// Atomically load the current stats object for this CPU.
		stats := c.contextSwitchIntervals[cpu].Load()

		stats.Count.Add(1)
		stats.Sum.Add(uint64(interval.Nanoseconds()))

		intervalUs := uint64(interval / time.Microsecond)
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

		// Atomically increment the single correct bucket. Prometheus scrape handles the cumulative sum.
		if idx < len(stats.Buckets) {
			stats.Buckets[idx].Add(1)
		} else {
			// If the interval is larger than our largest bucket, increment the last bucket (+Inf).
			stats.Buckets[len(stats.Buckets)-1].Add(1)
		}
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
