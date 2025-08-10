package main

import (
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/phuslu/log"
	"github.com/prometheus/client_golang/prometheus"
)

// ThreadStateKey represents a composite key for thread state transitions.
type ThreadStateKey struct {
	State      string
	WaitReason string
}

// ThreadCSCollector implements prometheus.Collector for thread-related metrics.
// This collector follows Prometheus best practices by creating new metrics on each scrape
//
// It provides high-performance aggregated metrics for:
// - Context switches per CPU and per process
// - Context switch intervals (time between switches on same CPU)
// - Thread state transitions with wait reasons
// - Thread lifecycle events (creation, termination)
//
// All metrics are designed for low cardinality to maintain performance at scale.
type ThreadCSCollector struct {
	// Atomic counters for high-frequency operations
	contextSwitchesPerCPU     map[uint16]*int64         // CPU -> context switch count
	contextSwitchesPerProcess map[uint32]*int64         // PID -> context switch count
	threadStates              map[ThreadStateKey]*int64 // {state, waitReason} -> count

	// Context switch interval tracking
	contextSwitchIntervals map[uint16][]float64 // CPU -> interval durations (seconds)

	// Synchronization
	mu  sync.RWMutex
	log log.Logger
}

// ThreadMetricsData holds aggregated data for a single scrape
type ThreadMetricsData struct {
	ContextSwitchesPerCPU     map[uint16]int64
	ContextSwitchesPerProcess map[uint32]ProcessContextSwitches
	ThreadStates              map[ThreadStateKey]int64
	ContextSwitchIntervals    map[uint16]IntervalStats
}

// ProcessContextSwitches holds context switch data for a process
type ProcessContextSwitches struct {
	ProcessID   uint32
	ProcessName string
	Count       int64
}

// IntervalStats holds statistical data for context switch intervals
type IntervalStats struct {
	Count   int64
	Sum     float64
	Buckets map[float64]int64 // histogram buckets
}

// NewThreadCSCollector creates a new thread metrics custom collector.
// This collector aggregates thread-related ETW events and exposes them as Prometheus metrics
// following the custom collector pattern for optimal performance and correctness.
func NewThreadCSCollector() *ThreadCSCollector {
	return &ThreadCSCollector{
		contextSwitchesPerCPU:     make(map[uint16]*int64),
		contextSwitchesPerProcess: make(map[uint32]*int64),
		threadStates:              make(map[ThreadStateKey]*int64),
		contextSwitchIntervals:    make(map[uint16][]float64),
		log:                       GetThreadLogger(),
	}
}

// Describe implements prometheus.Collector.
// It sends the descriptors of all the metrics the collector can possibly export
// to the provided channel. This is called once during registration.
func (c *ThreadCSCollector) Describe(ch chan<- *prometheus.Desc) {
	// Context switches per CPU
	ch <- prometheus.NewDesc(
		"etw_context_switches_per_cpu_total",
		"Total number of context switches per CPU",
		[]string{"cpu"}, nil,
	)

	// Context switches per process
	ch <- prometheus.NewDesc(
		"etw_context_switches_per_process_total",
		"Total number of context switches per process",
		[]string{"process_id", "process_name"}, nil,
	)

	// Context switch intervals histogram
	ch <- prometheus.NewDesc(
		"etw_context_switch_interval_seconds",
		"Histogram of context switch intervals per CPU (time between consecutive switches)",
		[]string{"cpu"}, nil,
	)

	// Thread state transitions
	ch <- prometheus.NewDesc(
		"etw_thread_states_total",
		"Total count of thread state transitions by state and wait reason",
		[]string{"state", "wait_reason"}, nil,
	)
}

// Collect implements prometheus.Collector.
// It is called by Prometheus on each scrape and must create new metrics each time
// to avoid race conditions and ensure stale metrics are not exposed.
func (c *ThreadCSCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Collect current data snapshot
	data := c.collectData()

	// Create context switches per CPU metrics
	for cpu, count := range data.ContextSwitchesPerCPU {
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(
				"etw_context_switches_per_cpu_total",
				"Total number of context switches per CPU",
				[]string{"cpu"}, nil,
			),
			prometheus.CounterValue,
			float64(count),
			strconv.FormatUint(uint64(cpu), 10),
		)
	}

	// Create context switches per process metrics
	for _, procData := range data.ContextSwitchesPerProcess {
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(
				"etw_context_switches_per_process_total",
				"Total number of context switches per process",
				[]string{"process_id", "process_name"}, nil,
			),
			prometheus.CounterValue,
			float64(procData.Count),
			strconv.FormatUint(uint64(procData.ProcessID), 10),
			procData.ProcessName,
		)
	}

	// Create context switch interval histograms
	for cpu, stats := range data.ContextSwitchIntervals {
		// Convert buckets from int64 to uint64 for Prometheus
		buckets := make(map[float64]uint64)
		for bucket, count := range stats.Buckets {
			buckets[bucket] = uint64(count)
		}

		// Create cumulative histogram
		ch <- prometheus.MustNewConstHistogram(
			prometheus.NewDesc(
				"etw_context_switch_interval_seconds",
				"Histogram of context switch intervals per CPU (time between consecutive switches)",
				[]string{"cpu"}, nil,
			),
			uint64(stats.Count),
			stats.Sum,
			buckets,
			strconv.FormatUint(uint64(cpu), 10),
		)
	}

	// Create thread state metrics
	for stateKey, count := range data.ThreadStates {
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(
				"etw_thread_states_total",
				"Total count of thread state transitions by state and wait reason",
				[]string{"state", "wait_reason"}, nil,
			),
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
		ContextSwitchIntervals:    make(map[uint16]IntervalStats),
	}

	// Collect CPU context switches
	for cpu, countPtr := range c.contextSwitchesPerCPU {
		if countPtr != nil {
			data.ContextSwitchesPerCPU[cpu] = atomic.LoadInt64(countPtr)
		}
	}

	// Collect process context switches
	processCollector := GetGlobalProcessCollector()
	for pid, countPtr := range c.contextSwitchesPerProcess {
		if countPtr != nil {
			// Only create metrics for processes that are still known at scrape time
			if processName, isKnown := processCollector.GetProcessName(pid); isKnown {
				count := atomic.LoadInt64(countPtr)
				data.ContextSwitchesPerProcess[pid] = ProcessContextSwitches{
					ProcessID:   pid,
					ProcessName: processName,
					Count:       count,
				}
			}
		}
	}

	// Collect thread states
	for stateKey, countPtr := range c.threadStates {
		if countPtr != nil {
			data.ThreadStates[stateKey] = atomic.LoadInt64(countPtr)
		}
	}

	// Collect and calculate context switch interval statistics
	for cpu, intervals := range c.contextSwitchIntervals {
		if len(intervals) > 0 {
			stats := c.calculateIntervalStats(intervals)
			data.ContextSwitchIntervals[cpu] = stats
		}
	}

	return data
}

// calculateIntervalStats computes histogram statistics from interval data.
// This includes count, sum, and histogram buckets for context switch intervals.
func (c *ThreadCSCollector) calculateIntervalStats(intervals []float64) IntervalStats {
	stats := IntervalStats{
		Count:   int64(len(intervals)),
		Sum:     0,
		Buckets: make(map[float64]int64),
	}

	// Define histogram buckets (exponential buckets from 1μs to ~16ms)
	buckets := []float64{
		0.000001, 0.000002, 0.000004, 0.000008, 0.000016,
		0.000032, 0.000064, 0.000128, 0.000256, 0.000512,
		0.001024, 0.002048, 0.004096, 0.008192, 0.016384,
	}

	// Initialize all buckets to zero
	for _, bucket := range buckets {
		stats.Buckets[bucket] = 0
	}

	// Calculate sum and count observations in each bucket
	for _, interval := range intervals {
		stats.Sum += interval

		// Count observations in each bucket (cumulative histogram)
		for _, bucket := range buckets {
			if interval <= bucket {
				stats.Buckets[bucket]++
			}
		}
	}

	return stats
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

	c.mu.Lock()
	defer c.mu.Unlock()

	// Record context switch per CPU
	if c.contextSwitchesPerCPU[cpu] == nil {
		c.contextSwitchesPerCPU[cpu] = new(int64)
	}
	atomic.AddInt64(c.contextSwitchesPerCPU[cpu], 1)

	// Record context switch per process (only for known processes with valid names)
	if processID > 0 {
		// Check if process is known by the global process collector
		processCollector := GetGlobalProcessCollector()
		if _, isKnown := processCollector.GetProcessName(processID); isKnown {
			if c.contextSwitchesPerProcess[processID] == nil {
				c.contextSwitchesPerProcess[processID] = new(int64)
			}
			atomic.AddInt64(c.contextSwitchesPerProcess[processID], 1)
		}
	}

	// Record context switch interval
	if interval > 0 {
		c.contextSwitchIntervals[cpu] = append(c.contextSwitchIntervals[cpu], interval.Seconds())

		// Limit interval history to prevent memory growth
		if len(c.contextSwitchIntervals[cpu]) > 1000 {
			// Keep the most recent 800 intervals
			copy(c.contextSwitchIntervals[cpu], c.contextSwitchIntervals[cpu][200:])
			c.contextSwitchIntervals[cpu] = c.contextSwitchIntervals[cpu][:800]
		}
	}
}

// RecordThreadStateTransition records a thread state transition event.
//
// Parameters:
// - state: The thread state (e.g., "ready", "waiting", "running", "terminated")
// - waitReason: The wait reason if state is "waiting", otherwise "none"
func (c *ThreadCSCollector) RecordThreadStateTransition(state, waitReason string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	stateKey := ThreadStateKey{State: state, WaitReason: waitReason}
	if c.threadStates[stateKey] == nil {
		c.threadStates[stateKey] = new(int64)
	}
	atomic.AddInt64(c.threadStates[stateKey], 1)
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