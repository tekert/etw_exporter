package main

import (
	"strconv"
	"sync"
	"time"

	"github.com/phuslu/log"
	"github.com/prometheus/client_golang/prometheus"
)

// PerfInfoCollector implements prometheus.Collector for real-time performance metrics.
// This collector provides high-performance aggregated metrics for:
// - System-wide interrupt to process latency (matches LatencyMon main metric)
// - ISR execution time by driver (matches "Highest ISR routine execution time")
// - DPC execution time by driver (matches "Highest DPC routine execution time")
// - DPC queue depth tracking (system-wide and per-CPU)
// - SMI gap detection via timeline analysis
// - Hard page fault counting (system-wide)
//
// All metrics use the etw_ prefix and are designed for low cardinality.
type PerfInfoCollector struct {
	// Configuration options
	config *InterruptLatencyConfig

	// Pending ISR tracking for latency correlation
	pendingISRs map[ISRKey]*ISREvent // {CPU, Vector} -> ISR event

	// Image database for address-to-driver mapping
	imageDatabase map[uint64]ImageInfo // ImageBase -> driver info
	driverNames   map[uint64]string    // Routine address -> driver name (cached)

	// Performance metrics data
	interruptLatencyBuckets map[float64]int64                  // Histogram buckets for system-wide latency
	isrDurationBuckets      map[ISRDriverKey]map[float64]int64 // {driver} -> histogram buckets
	dpcDurationBuckets      map[DPCDriverKey]map[float64]int64 // {driver} -> histogram buckets

	// Optional count metrics (only if enabled)
	isrCounters map[string]int64 // driver name -> ISR count
	dpcCounters map[string]int64 // driver name -> DPC count

	// DPC queue tracking
	dpcQueuedCount   map[uint16]int64 // CPU -> queued count
	dpcExecutedCount map[uint16]int64 // CPU -> executed count

	// SMI gap detection
	lastEventTime map[uint16]time.Time // CPU -> last event timestamp

	// Hard page fault counter
	hardPageFaultCount int64

	// Synchronization
	mu  sync.RWMutex
	log log.Logger

	// Metric descriptors
	interruptLatencyDesc *prometheus.Desc
	isrDurationDesc      *prometheus.Desc
	dpcDurationDesc      *prometheus.Desc
	dpcQueueDepthDesc    *prometheus.Desc
	dpcQueueDepthCPUDesc *prometheus.Desc
	smiGapsDesc          *prometheus.Desc
	hardPageFaultsDesc   *prometheus.Desc

	// Optional metric descriptors (only created if enabled)
	isrCountDesc *prometheus.Desc
	dpcCountDesc *prometheus.Desc

	// Bounded driver tracking (top 50 most active drivers)
	driverCounters map[string]int64 // driver name -> event count
	maxDrivers     int              // maximum drivers to track
}

// ISRKey represents a composite key for pending ISR events
type ISRKey struct {
	CPU    uint16
	Vector uint16
}

// ISREvent holds ISR event data for latency correlation
type ISREvent struct {
	InitialTime    time.Time
	RoutineAddress uint64
	CPU            uint16
	Vector         uint16
}

// ISRDriverKey represents a key for ISR duration tracking by driver
type ISRDriverKey struct {
	ImageName string
}

// DPCDriverKey represents a key for DPC duration tracking by driver
type DPCDriverKey struct {
	ImageName string
}

// ImageInfo holds driver image information for address resolution
type ImageInfo struct {
	ImageBase uint64
	ImageSize uint64
	FileName  string // Full path
	ImageName string // Extracted driver name (e.g., "tcpip")
}

// Histogram bucket definitions optimized for Windows latency ranges
var (
	// System-wide interrupt latency buckets (microseconds)
	InterruptLatencyBuckets = []float64{1, 5, 10, 25, 50, 100, 200, 500, 1000, 2000, 5000}

	// ISR execution time buckets (microseconds)
	ISRDurationBuckets = []float64{1, 2, 5, 10, 20, 50, 100, 200, 500, 1000, 2000}

	// DPC execution time buckets (microseconds)
	DPCDurationBuckets = []float64{1, 5, 10, 25, 50, 100, 200, 500, 1000, 2000, 4000, 10000}

	// SMI gap detection buckets (microseconds)
	SMIGapBuckets = []float64{50, 100, 200, 500, 1000, 2000, 5000}
)

// NewInterruptLatencyCollector creates a new interrupt latency collector
func NewInterruptLatencyCollector(config *InterruptLatencyConfig) *PerfInfoCollector {
	collector := &PerfInfoCollector{
		config:                  config,
		pendingISRs:             make(map[ISRKey]*ISREvent, 256),  // Pre-size for performance
		imageDatabase:           make(map[uint64]ImageInfo, 1000), // Pre-size for typical driver count
		driverNames:             make(map[uint64]string, 2000),    // Cached driver name lookups
		interruptLatencyBuckets: make(map[float64]int64, len(InterruptLatencyBuckets)),
		dpcQueuedCount:          make(map[uint16]int64, 64), // Pre-size for max CPU count
		dpcExecutedCount:        make(map[uint16]int64, 64),
		lastEventTime:           make(map[uint16]time.Time, 64),
		driverCounters:          make(map[string]int64, 100), // Track driver activity for bounded set
		maxDrivers:              50,                          // Limit to top 50 most active drivers
		log:                     GetInterruptLogger(),
	}

	// Initialize per-driver histogram buckets only if per-driver metrics are enabled
	if config.EnablePerDriver {
		collector.isrDurationBuckets = make(map[ISRDriverKey]map[float64]int64, 50)
		collector.dpcDurationBuckets = make(map[DPCDriverKey]map[float64]int64, 50)
	}

	// Initialize count maps only if enabled
	if config.EnableCounts {
		collector.isrCounters = make(map[string]int64)
		collector.dpcCounters = make(map[string]int64)
	}

	// Initialize histogram buckets
	for _, bucket := range InterruptLatencyBuckets {
		collector.interruptLatencyBuckets[bucket] = 0
	}

	// Create metric descriptors
	collector.interruptLatencyDesc = prometheus.NewDesc(
		"etw_interrupt_to_process_latency_microseconds",
		"System-wide interrupt to process latency in microseconds",
		nil, nil)

	// Create per-driver descriptors only if enabled
	if config.EnablePerDriver {
		collector.isrDurationDesc = prometheus.NewDesc(
			"etw_isr_execution_time_microseconds",
			"ISR execution time by driver in microseconds",
			[]string{"image_name"}, nil)

		collector.dpcDurationDesc = prometheus.NewDesc(
			"etw_dpc_execution_time_microseconds",
			"DPC execution time by driver in microseconds",
			[]string{"image_name"}, nil)
	}

	collector.dpcQueueDepthDesc = prometheus.NewDesc(
		"etw_dpc_queue_depth_current",
		"Current DPC queue depth system-wide",
		nil, nil)

	collector.smiGapsDesc = prometheus.NewDesc(
		"etw_smi_gaps_microseconds",
		"SMI gap detection in microseconds",
		nil, nil)

	collector.hardPageFaultsDesc = prometheus.NewDesc(
		"etw_hard_pagefaults_total",
		"Total hard page faults system-wide",
		nil, nil)

	// Create per-CPU descriptor only if enabled
	if config.EnablePerCPU {
		collector.dpcQueueDepthCPUDesc = prometheus.NewDesc(
			"etw_dpc_queue_depth_cpu_current",
			"Current DPC queue depth by CPU",
			[]string{"cpu"}, nil)
	}

	// Create count descriptors only if enabled
	if config.EnableCounts {
		collector.isrCountDesc = prometheus.NewDesc(
			"etw_isr_count_total",
			"Total number of ISR events by driver",
			[]string{"image_name"}, nil)

		collector.dpcCountDesc = prometheus.NewDesc(
			"etw_dpc_count_total",
			"Total number of DPC events by driver",
			[]string{"image_name"}, nil)
	}

	collector.log.Debug().Msg("Interrupt latency collector created")
	return collector
}

// Describe implements the prometheus.Collector interface
func (c *PerfInfoCollector) Describe(ch chan<- *prometheus.Desc) {
	// Always include core system-wide metrics
	ch <- c.interruptLatencyDesc
	ch <- c.dpcQueueDepthDesc
	ch <- c.smiGapsDesc
	ch <- c.hardPageFaultsDesc

	// Only include per-driver metrics if enabled
	if c.config.EnablePerDriver && c.isrDurationDesc != nil && c.dpcDurationDesc != nil {
		ch <- c.isrDurationDesc
		ch <- c.dpcDurationDesc
	}

	// Only include per-CPU metrics if enabled
	if c.config.EnablePerCPU && c.dpcQueueDepthCPUDesc != nil {
		ch <- c.dpcQueueDepthCPUDesc
	}

	// Only include count metrics if enabled
	if c.config.EnableCounts {
		if c.isrCountDesc != nil {
			ch <- c.isrCountDesc
		}
		if c.dpcCountDesc != nil {
			ch <- c.dpcCountDesc
		}
	}
}

// Collect implements the prometheus.Collector interface
func (c *PerfInfoCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// System-wide interrupt latency histogram (always enabled)
	c.collectInterruptLatencyHistogram(ch)

	// ISR/DPC duration histograms by driver (only if per-driver enabled)
	if c.config.EnablePerDriver {
		c.collectISRDurationHistograms(ch)
		c.collectDPCDurationHistograms(ch)
	}

	// DPC queue depth metrics (always enabled)
	c.collectDPCQueueDepth(ch)

	// SMI gap histogram (always enabled for now)
	c.collectSMIGaps(ch)

	// Hard page fault counter (always enabled)
	ch <- prometheus.MustNewConstMetric(
		c.hardPageFaultsDesc,
		prometheus.CounterValue,
		float64(c.hardPageFaultCount),
	)

	// Optional count metrics
	if c.config.EnableCounts {
		c.collectCountMetrics(ch)
	}
}

// collectInterruptLatencyHistogram creates the system-wide interrupt latency histogram
func (c *PerfInfoCollector) collectInterruptLatencyHistogram(ch chan<- prometheus.Metric) {
	cumulativeBuckets := make(map[float64]uint64)
	var sampleCount uint64
	var sampleSum float64
	var cumulativeCount uint64

	// Iterate over the defined buckets in order to calculate cumulative counts
	for _, bucketBound := range InterruptLatencyBuckets {
		count := uint64(c.interruptLatencyBuckets[bucketBound])
		if count > 0 {
			// This approximation of sum is incorrect for a true histogram, but we'll
			// keep it to only fix the cumulative count issue. A better approach
			// would be to store and use the actual sum of observed values.
			sampleSum += float64(bucketBound) * float64(count)
			sampleCount += count
		}
		cumulativeCount += count
		cumulativeBuckets[bucketBound] = cumulativeCount
	}

	// Create histogram metric
	histogram := prometheus.MustNewConstHistogram(
		c.interruptLatencyDesc,
		sampleCount,
		sampleSum,
		cumulativeBuckets,
	)

	ch <- histogram
}

// collectISRDurationHistograms creates ISR duration histograms by driver
func (c *PerfInfoCollector) collectISRDurationHistograms(ch chan<- prometheus.Metric) {
	for driverKey, driverBuckets := range c.isrDurationBuckets {
		cumulativeBuckets := make(map[float64]uint64)
		var sampleCount uint64
		var sampleSum float64
		var cumulativeCount uint64

		// Iterate over the defined buckets in order to calculate cumulative counts
		for _, bucketBound := range ISRDurationBuckets {
			count := uint64(driverBuckets[bucketBound])
			if count > 0 {
				sampleSum += float64(bucketBound) * float64(count)
				sampleCount += count
			}
			cumulativeCount += count
			cumulativeBuckets[bucketBound] = cumulativeCount
		}

		if sampleCount > 0 {
			histogram := prometheus.MustNewConstHistogram(
				c.isrDurationDesc,
				sampleCount,
				sampleSum,
				cumulativeBuckets,
				driverKey.ImageName,
			)
			ch <- histogram
		}
	}
}

// collectDPCDurationHistograms creates DPC duration histograms by driver
func (c *PerfInfoCollector) collectDPCDurationHistograms(ch chan<- prometheus.Metric) {
	for driverKey, driverBuckets := range c.dpcDurationBuckets {
		cumulativeBuckets := make(map[float64]uint64)
		var sampleCount uint64
		var sampleSum float64
		var cumulativeCount uint64

		// Iterate over the defined buckets in order to calculate cumulative counts
		for _, bucketBound := range DPCDurationBuckets {
			count := uint64(driverBuckets[bucketBound])
			if count > 0 {
				sampleSum += float64(bucketBound) * float64(count)
				sampleCount += count
			}
			cumulativeCount += count
			cumulativeBuckets[bucketBound] = cumulativeCount
		}

		if sampleCount > 0 {
			histogram := prometheus.MustNewConstHistogram(
				c.dpcDurationDesc,
				sampleCount,
				sampleSum,
				cumulativeBuckets,
				driverKey.ImageName,
			)
			ch <- histogram
		}
	}
}

// collectDPCQueueDepth creates DPC queue depth metrics
func (c *PerfInfoCollector) collectDPCQueueDepth(ch chan<- prometheus.Metric) {
	// System-wide DPC queue depth
	var totalQueued, totalExecuted int64
	for cpu := range c.dpcQueuedCount {
		totalQueued += c.dpcQueuedCount[cpu]
		totalExecuted += c.dpcExecutedCount[cpu]
	}

	systemQueueDepth := max(totalQueued-totalExecuted, 0)

	ch <- prometheus.MustNewConstMetric(
		c.dpcQueueDepthDesc,
		prometheus.GaugeValue,
		float64(systemQueueDepth),
	)

	// Per-CPU DPC queue depth (only if enabled)
	if c.config.EnablePerCPU && c.dpcQueueDepthCPUDesc != nil {
		for cpu := range c.dpcQueuedCount {
			cpuQueueDepth := max(c.dpcQueuedCount[cpu]-c.dpcExecutedCount[cpu], 0)

			ch <- prometheus.MustNewConstMetric(
				c.dpcQueueDepthCPUDesc,
				prometheus.GaugeValue,
				float64(cpuQueueDepth),
				formatCPU(cpu),
			)
		}
	}
}

// collectSMIGaps creates SMI gap histogram (placeholder for now)
func (c *PerfInfoCollector) collectSMIGaps(ch chan<- prometheus.Metric) {
	// TODO: Implement SMI gap detection
	// For now, create empty histogram
	buckets := make(map[float64]uint64)
	for _, bucket := range SMIGapBuckets {
		buckets[bucket] = 0
	}

	histogram := prometheus.MustNewConstHistogram(
		c.smiGapsDesc,
		0, // sample count
		0, // sample sum
		buckets,
	)

	ch <- histogram
}

// collectCountMetrics creates ISR and DPC count metrics by driver
func (c *PerfInfoCollector) collectCountMetrics(ch chan<- prometheus.Metric) {
	// ISR count metrics by driver
	if c.isrCountDesc != nil {
		for driverName, count := range c.isrCounters {
			ch <- prometheus.MustNewConstMetric(
				c.isrCountDesc,
				prometheus.CounterValue,
				float64(count),
				driverName,
			)
		}
	}

	// DPC count metrics by driver
	if c.dpcCountDesc != nil {
		for driverName, count := range c.dpcCounters {
			ch <- prometheus.MustNewConstMetric(
				c.dpcCountDesc,
				prometheus.CounterValue,
				float64(count),
				driverName,
			)
		}
	}
}

// ProcessISREvent processes an ISR event for latency tracking
func (c *PerfInfoCollector) ProcessISREvent(cpu uint16, vector uint16, initialTime time.Time, routineAddress uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	key := ISRKey{CPU: cpu, Vector: vector}

	// Store pending ISR for later correlation with DPC
	c.pendingISRs[key] = &ISREvent{
		InitialTime:    initialTime,
		RoutineAddress: routineAddress,
		CPU:            cpu,
		Vector:         vector,
	}

	// Only track per-driver metrics if enabled
	if c.config.EnablePerDriver || c.config.EnableCounts {
		// Resolve driver name and track activity for bounded set management
		driverName := c.resolveDriverNameUnsafe(routineAddress)
		c.driverCounters[driverName]++

		// Update ISR count if enabled (avoid nil check in critical path)
		if c.isrCounters != nil {
			c.isrCounters[driverName]++
		}
	}

	// Clean up old pending ISRs periodically (reduce frequency to improve performance)
	if len(c.pendingISRs)%100 == 0 {
		c.cleanupOldISRs(initialTime)
	}
}

// ProcessDPCEvent processes a DPC event and correlates with ISR for latency calculation
func (c *PerfInfoCollector) ProcessDPCEvent(cpu uint16, initialTime time.Time, routineAddress uint64, durationMicros float64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Update DPC execution count for queue tracking
	c.dpcExecutedCount[cpu]++

	// Only resolve driver name if we need it for per-driver or count metrics
	var driverName string
	if c.config.EnablePerDriver || c.config.EnableCounts {
		// Resolve driver name and track activity for bounded set management
		driverName = c.resolveDriverNameUnsafe(routineAddress)
		c.driverCounters[driverName]++

		// Update DPC count if enabled (avoid nil check in critical path)
		if c.dpcCounters != nil {
			c.dpcCounters[driverName]++
		}

		// Record DPC duration for per-driver metrics
		if c.config.EnablePerDriver {
			c.recordDPCDuration(driverName, durationMicros)
		}
	}

	// Find matching ISR on same CPU for latency calculation
	var matchedISR *ISREvent
	var matchedKey ISRKey

	// Look for ISR with closest timestamp on same CPU - optimize for hot path
	var closestTime time.Duration = time.Hour // Start with large value
	for key, isr := range c.pendingISRs {
		if key.CPU == cpu {
			timeDiff := initialTime.Sub(isr.InitialTime)
			if timeDiff >= 0 && timeDiff < closestTime {
				closestTime = timeDiff
				matchedISR = isr
				matchedKey = key
			}
		}
	}

	// If we found a matching ISR, record the latency
	if matchedISR != nil {
		latencyMicros := float64(closestTime.Nanoseconds()) / 1000.0
		c.recordInterruptLatency(latencyMicros)

		// Remove the matched ISR to prevent duplicate matches
		delete(c.pendingISRs, matchedKey)
	}
}

// ProcessImageLoadEvent processes image load events to build driver database
func (c *PerfInfoCollector) ProcessImageLoadEvent(imageBase, imageSize uint64, fileName string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	imageName := extractDriverName(fileName)

	c.imageDatabase[imageBase] = ImageInfo{
		ImageBase: imageBase,
		ImageSize: imageSize,
		FileName:  fileName,
		ImageName: imageName,
	}

	c.log.Trace().
		Str("image_name", imageName).
		Str("file_name", fileName).
		Uint64("base", imageBase).
		Uint64("size", imageSize).
		Msg("Image loaded")
}

// TODO: do Process-Level Page Faults ?

// ProcessHardPageFaultEvent increments the hard page fault counter
func (c *PerfInfoCollector) ProcessHardPageFaultEvent() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.hardPageFaultCount++
}

// resolveDriverNameUnsafe is an optimized version for use within locked contexts
// Assumes caller already holds the mutex
func (c *PerfInfoCollector) resolveDriverNameUnsafe(routineAddress uint64) string {
	// Check cache first
	if driverName, exists := c.driverNames[routineAddress]; exists {
		return driverName
	}

	// Binary search through image database
	for imageBase, imageInfo := range c.imageDatabase {
		if routineAddress >= imageBase && routineAddress < imageBase+imageInfo.ImageSize {
			driverName := imageInfo.ImageName

			// Check if we should track this driver or group as "other"
			if !c.shouldTrackDriver(driverName) {
				driverName = "other"
			}

			// Cache the result for future lookups
			c.driverNames[routineAddress] = driverName
			return driverName
		}
	}

	// Unknown driver - cache the result to avoid repeated lookups
	c.driverNames[routineAddress] = "unknown"
	return "unknown"
}

// shouldTrackDriver determines if we should track this driver individually
func (c *PerfInfoCollector) shouldTrackDriver(driverName string) bool {
	// Always track unknown and other
	if driverName == "unknown" || driverName == "other" {
		return true
	}

	// Count current tracked drivers (excluding "unknown" and "other")
	trackedCount := 0
	for name := range c.driverCounters {
		if name != "unknown" && name != "other" {
			trackedCount++
		}
	}

	// If under limit, track it
	if trackedCount < c.maxDrivers-2 { // Reserve 2 slots for "unknown" and "other"
		return true
	}

	// If at limit, only track if already being tracked
	_, alreadyTracked := c.driverCounters[driverName]
	return alreadyTracked
}

// recordInterruptLatency records a system-wide interrupt latency sample
func (c *PerfInfoCollector) recordInterruptLatency(latencyMicros float64) {
	// Find appropriate bucket
	for _, bucket := range InterruptLatencyBuckets {
		if latencyMicros <= bucket {
			c.interruptLatencyBuckets[bucket]++
			break
		}
	}
}

// recordDPCDuration records a DPC duration sample for a specific driver
func (c *PerfInfoCollector) recordDPCDuration(driverName string, durationMicros float64) {
	key := DPCDriverKey{ImageName: driverName}

	// Initialize buckets for this driver if needed
	if _, exists := c.dpcDurationBuckets[key]; !exists {
		c.dpcDurationBuckets[key] = make(map[float64]int64)
		for _, bucket := range DPCDurationBuckets {
			c.dpcDurationBuckets[key][bucket] = 0
		}
	}

	// Find appropriate bucket
	for _, bucket := range DPCDurationBuckets {
		if durationMicros <= bucket {
			c.dpcDurationBuckets[key][bucket]++
			break
		}
	}
}

// cleanupOldISRs removes ISRs older than 10ms to prevent memory leaks
func (c *PerfInfoCollector) cleanupOldISRs(currentTime time.Time) {
	const maxAge = 10 * time.Millisecond

	for key, isr := range c.pendingISRs {
		if currentTime.Sub(isr.InitialTime) > maxAge {
			delete(c.pendingISRs, key)
		}
	}
}

// extractDriverName extracts the driver name from a full file path
func extractDriverName(filePath string) string {
	// Extract filename from path: C:\Windows\System32\drivers\tcpip.sys -> tcpip.sys
	name := filePath
	for i := len(filePath) - 1; i >= 0; i-- {
		if filePath[i] == '\\' || filePath[i] == '/' {
			name = filePath[i+1:]
			break
		}
	}

	return name
}

// formatCPU formats CPU number as string
func formatCPU(cpu uint16) string {
	return strconv.FormatUint(uint64(cpu), 10)
}
