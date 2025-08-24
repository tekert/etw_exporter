// filepath: e:\Sources\go\projects\etw_exporter\perfinfo_collector.go
package main

import (
	"strconv"
	"sync"
	"time"

	"github.com/phuslu/log"
	"github.com/prometheus/client_golang/prometheus"
)

// TODO(tekert): do Process-Level Page Faults ?

// DPC Latency is measured as an aproximation in this module,
// It's measured time from the time a DCP is executed
// To the time the next DPC is executed on the same CPU
// OR
// A Context Switch event on the same CPU (whichever comes first).
//
// This is not a perfect measurement, but it's a reasonable approximation
// of DPC latency that can be collected with low overhead.
// It will be slightly higher than the actual DPC latency, but it will
// still be useful for identifying high latency situations.

// # Good references
// - LatencyMon internals: https://www.resplendence.com/latenc
// - Windows ISRs: https://learn.microsoft.com/en-us/windows-hardware/drivers/kernel/introduction-to-interrupt-service-routines
// - Windows DPCs: https://learn.microsoft.com/en-us/windows-hardware/drivers/kernel/introduction-to-dpc-objects
// - OSR Note: https://www.osr.com/nt-insider/2014-issue3/windows-real-time/

// # PromQL queries for DPC queue pressure
// To see the rate of change and infer queue pressure, you can query:
//
// rate(etw_dpc_queued_cpu_total[1m]) - rate(etw_dpc_executed_cpu_total[1m])
//
// A sustained positive result from this query indicates that DPCs are being
// queued faster than they are being executed, which is a clear sign of high
// DPC latency and potential CPU saturation at high IRQL.

// isrEventPool reduces allocations by recycling ISREvent objects.
var isrEventPool = sync.Pool{
	New: func() any {
		return &ISREvent{}
	},
}

// PerfInfoInterruptCollector implements prometheus.Collector for real-time performance metrics.
// This collector provides high-performance aggregated metrics for:
// - System-wide ISR to DPC latency (measures DPC queue time)
// - DPC execution time by driver (matches "Highest DPC routine execution time")
// - DPC queue depth tracking (system-wide and per-CPU)
// - SMI gap detection via timeline analysis
// - Hard page fault counting (system-wide)
//
// All metrics use the etw_ prefix and are designed for low cardinality.
type PerfInfoInterruptCollector struct {
	// Configuration options
	config *PerfInfoConfig

	// Pending ISR tracking for latency correlation
	pendingISRs map[ISRKey]*ISREvent // {CPU, Vector} -> ISR event

	// Image database for address-to-driver mapping
	imageDatabase map[uint64]ImageInfo // ImageBase -> driver info
	driverNames   map[uint64]string    // Routine address -> driver name (cached)

	// Performance metrics data
	isrToDpcLatencyBuckets map[float64]int64 // Histogram buckets for system-wide latency
	isrToDpcLatencyCount   uint64
	isrToDpcLatencySum     float64
	dpcDurationHistograms  map[DPCDriverKey]*histogramData // {driver} -> histogram data

	// DPC queue tracking
	dpcQueuedCount   map[uint16]int64 // CPU -> queued count
	dpcExecutedCount map[uint16]int64 // CPU -> executed count

	// SMI gap detection (placeholder for future implementation)
	smiGapsDesc *prometheus.Desc

	// Hard page fault counter
	hardPageFaultCount int64

	// Synchronization
	mu            sync.RWMutex
	log           log.Logger
	lastPruneTime time.Time
	pruneInterval time.Duration

	// Metric descriptors
	isrToDpcLatencyDesc *prometheus.Desc
	dpcDurationDesc     *prometheus.Desc
	dpcQueuedDesc       *prometheus.Desc
	dpcExecutedDesc     *prometheus.Desc
	dpcQueuedCPUDesc    *prometheus.Desc
	dpcExecutedCPUDesc  *prometheus.Desc
	hardPageFaultsDesc  *prometheus.Desc

	// Driver last seen tracking for stale metric cleanup
	driverLastSeen map[string]time.Time // driver name -> last event time
	driverTimeout  time.Duration        // duration after which inactive drivers are pruned
	cpuStringCache map[uint16]string    // Cache for formatCPU to reduce allocations
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

// DPCDriverKey represents a key for DPC duration tracking by driver
type DPCDriverKey struct {
	ImageName string
}

// histogramData holds the data for a single Prometheus histogram.
type histogramData struct {
	buckets map[float64]int64
	count   uint64
	sum     float64
}

// ImageInfo holds driver image information for address resolution
type ImageInfo struct {
	ImageBase uint64
	ImageSize uint64
	FileName  string // Full path
	ImageName string // Extracted driver name (e.g., "tcpip.sys")
}

// Histogram bucket definitions optimized for Windows latency ranges
var (
	// System-wide ISR to DPC latency buckets (microseconds)
	ISRToDPCLatencyBuckets = prometheus.ExponentialBuckets(1.0, 5.0, 11)
	// DPC execution time buckets (microseconds)
	DPCDurationBuckets = prometheus.ExponentialBuckets(1.0, 5.0, 12)
	// SMI gap detection buckets (microseconds) - for future use
	SMIGapBuckets = prometheus.ExponentialBucketsRange(5000.0, 500000.0, 7)
)

// NewPerfInfoCollector creates a new interrupt latency collector
func NewPerfInfoCollector(config *PerfInfoConfig) *PerfInfoInterruptCollector {
	collector := &PerfInfoInterruptCollector{
		config:                 config,
		pendingISRs:            make(map[ISRKey]*ISREvent, 256),  // Pre-size for performance
		imageDatabase:          make(map[uint64]ImageInfo, 1000), // Pre-size for typical driver count
		driverNames:            make(map[uint64]string, 2000),    // Cached driver name lookups
		isrToDpcLatencyBuckets: make(map[float64]int64, len(ISRToDPCLatencyBuckets)),
		isrToDpcLatencyCount:   0,
		isrToDpcLatencySum:     0.0,
		dpcQueuedCount:         make(map[uint16]int64, 64), // Pre-size for max CPU count
		dpcExecutedCount:       make(map[uint16]int64, 64),
		driverLastSeen:         make(map[string]time.Time, 100), // Track driver activity for bounded set
		driverTimeout:          15 * time.Minute,                // Prune drivers inactive for 15 mins
		log:                    GetPerfinfoLogger(),
		lastPruneTime:          time.Now(),
		pruneInterval:          5 * time.Minute,

		cpuStringCache: make(map[uint16]string, 64), // Cache for formatCPU to reduce allocations
	}

	// Initialize per-driver histogram buckets only if per-driver metrics are enabled
	if config.EnablePerDriver {
		collector.dpcDurationHistograms = make(map[DPCDriverKey]*histogramData, 50)
	}

	// Initialize histogram buckets
	for _, bucket := range ISRToDPCLatencyBuckets {
		collector.isrToDpcLatencyBuckets[bucket] = 0
	}

	// These two metrics isrToDpcLatencyDesc and dpcExecutedDesc
	// Measure total interrupt to process latency, an aproximation.
	collector.isrToDpcLatencyDesc = prometheus.NewDesc(
		"etw_isr_to_dpc_latency_microseconds",
		"Latency from ISR entry to DPC entry in microseconds. This measures DPC queue time.",
		nil, nil)

	collector.dpcExecutedDesc = prometheus.NewDesc(
		"etw_dpc_executed_total",
		"Total number of DPCs that began execution system-wide.",
		nil, nil)

	// Count number of DPCs queued system-wide over time this can be
	// used to infer DPC queue pressure.
	collector.dpcQueuedDesc = prometheus.NewDesc(
		"etw_dpc_queued_total",
		"Total number of DPCs queued for execution system-wide.",
		nil, nil)

	// Create per-driver descriptors only if enabled
	if config.EnablePerDriver {
		collector.dpcDurationDesc = prometheus.NewDesc(
			"etw_dpc_execution_time_microseconds",
			"DPC execution time by driver in microseconds",
			[]string{"image_name"}, nil)
	}

	// TODO: Define SMI metric but do not collect data for it yet.
	if config.EnableSMIDetection {
		collector.smiGapsDesc = prometheus.NewDesc(
			"etw_smi_gaps_microseconds",
			"SMI gap detection in microseconds, based on SampledProfile event gaps.",
			nil, nil)
	}

	collector.hardPageFaultsDesc = prometheus.NewDesc(
		"etw_hard_pagefaults_total",
		"Total hard page faults system-wide",
		nil, nil)

	// Create per-CPU descriptor only if enabled
	if config.EnablePerCPU {
		collector.dpcQueuedCPUDesc = prometheus.NewDesc(
			"etw_dpc_queued_cpu_total",
			"Total number of DPCs queued for execution by CPU.",
			[]string{"cpu"}, nil)

		collector.dpcExecutedCPUDesc = prometheus.NewDesc(
			"etw_dpc_executed_cpu_total",
			"Total number of DPCs that began execution by CPU.",
			[]string{"cpu"}, nil)
	}

	collector.log.Debug().Msg("Interrupt latency collector created")
	return collector
}

// Describe implements the prometheus.Collector interface
func (c *PerfInfoInterruptCollector) Describe(ch chan<- *prometheus.Desc) {
	// Always include core system-wide metrics
	ch <- c.isrToDpcLatencyDesc
	ch <- c.dpcQueuedDesc
	ch <- c.dpcExecutedDesc
	ch <- c.hardPageFaultsDesc

	// Placeholder for SMI gap metric
	if c.smiGapsDesc != nil {
		ch <- c.smiGapsDesc
	}

	// Only include per-driver metrics if enabled
	if c.config.EnablePerDriver && c.dpcDurationDesc != nil {
		ch <- c.dpcDurationDesc
	}

	// Only include per-CPU metrics if enabled
	if c.config.EnablePerCPU {
		if c.dpcQueuedCPUDesc != nil {
			ch <- c.dpcQueuedCPUDesc
		}
		if c.dpcExecutedCPUDesc != nil {
			ch <- c.dpcExecutedCPUDesc
		}
	}
}

// Collect implements the prometheus.Collector interface
func (c *PerfInfoInterruptCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Periodically prune stale drivers to manage metric lifetime.
	if time.Since(c.lastPruneTime) > c.pruneInterval {
		c.pruneStaleDrivers()
		c.lastPruneTime = time.Now()
	}

	// System-wide interrupt latency histogram
	c.collectISRToDPCLatencyHistogram(ch)

	// ISR/DPC duration histograms by driver
	if c.config.EnablePerDriver {
		c.collectDPCDurationHistograms(ch)
	}

	// DPC queue counters
	c.collectDPCCounters(ch)

	// SMI gap histogram (only if enabled) // TODO
	if c.config.EnableSMIDetection {
		c.collectSMIGaps(ch)
	}

	// Hard page fault counter
	ch <- prometheus.MustNewConstMetric(
		c.hardPageFaultsDesc,
		prometheus.CounterValue,
		float64(c.hardPageFaultCount),
	)
}

// collectISRToDPCLatencyHistogram creates the system-wide ISR to DPC latency histogram
func (c *PerfInfoInterruptCollector) collectISRToDPCLatencyHistogram(ch chan<- prometheus.Metric) {
	cumulativeBuckets := make(map[float64]uint64)
	var cumulativeCount uint64

	// Iterate over the defined buckets in order to calculate cumulative counts
	for _, bucketBound := range ISRToDPCLatencyBuckets {
		count := uint64(c.isrToDpcLatencyBuckets[bucketBound])
		cumulativeCount += count
		cumulativeBuckets[bucketBound] = cumulativeCount
	}

	// Create histogram metric with accurate count and sum
	histogram := prometheus.MustNewConstHistogram(
		c.isrToDpcLatencyDesc,
		c.isrToDpcLatencyCount,
		c.isrToDpcLatencySum,
		cumulativeBuckets,
	)

	ch <- histogram
}

// collectISRDurationHistograms creates ISR duration histograms by driver
func (c *PerfInfoInterruptCollector) collectDPCDurationHistograms(ch chan<- prometheus.Metric) {
	for driverKey, hd := range c.dpcDurationHistograms {
		cumulativeBuckets := make(map[float64]uint64)
		var cumulativeCount uint64

		// Iterate over the defined buckets in order to calculate cumulative counts
		for _, bucketBound := range DPCDurationBuckets {
			count := uint64(hd.buckets[bucketBound])
			cumulativeCount += count
			cumulativeBuckets[bucketBound] = cumulativeCount
		}

		if hd.count > 0 {
			histogram := prometheus.MustNewConstHistogram(
				c.dpcDurationDesc,
				hd.count,
				hd.sum,
				cumulativeBuckets,
				driverKey.ImageName,
			)
			ch <- histogram
		}
	}
}

// collectDPCQueueDepth creates DPC queue depth metrics
func (c *PerfInfoInterruptCollector) collectDPCCounters(ch chan<- prometheus.Metric) {
	// Create a set of all CPUs from both maps to ensure we don't miss any.
	allCPUs := make(map[uint16]struct{})
	for cpu := range c.dpcQueuedCount {
		allCPUs[cpu] = struct{}{}
	}
	for cpu := range c.dpcExecutedCount {
		allCPUs[cpu] = struct{}{}
	}

	var totalQueued, totalExecuted int64

	for cpu := range allCPUs {
		queued := c.dpcQueuedCount[cpu]
		executed := c.dpcExecutedCount[cpu]
		totalQueued += queued
		totalExecuted += executed

		// Per-CPU DPC counters (only if enabled)
		if c.config.EnablePerCPU && c.dpcQueuedCPUDesc != nil && c.dpcExecutedCPUDesc != nil {
			ch <- prometheus.MustNewConstMetric(
				c.dpcQueuedCPUDesc,
				prometheus.CounterValue,
				float64(queued),
				c.formatCPU(cpu),
			)
			ch <- prometheus.MustNewConstMetric(
				c.dpcExecutedCPUDesc,
				prometheus.CounterValue,
				float64(executed),
				c.formatCPU(cpu),
			)
		}
	}

	// System-wide DPC counters
	ch <- prometheus.MustNewConstMetric(
		c.dpcQueuedDesc,
		prometheus.CounterValue,
		float64(totalQueued),
	)
	ch <- prometheus.MustNewConstMetric(
		c.dpcExecutedDesc,
		prometheus.CounterValue,
		float64(totalExecuted),
	)
}

// collectSMIGaps returns a zero-value histogram as SMI detection is currently disabled.
func (c *PerfInfoInterruptCollector) collectSMIGaps(ch chan<- prometheus.Metric) {
	// This is a placeholder. The SMI detection logic has been removed for now.
	// We will return a zero-value histogram to maintain metric presence.
	histogram := prometheus.MustNewConstHistogram(
		c.smiGapsDesc,
		0,
		0,
		map[float64]uint64{},
	)
	ch <- histogram
}

// ProcessISREvent processes an ISR event for latency tracking
func (c *PerfInfoInterruptCollector) ProcessISREvent(cpu uint16, vector uint16,
	initialTime time.Time, routineAddress uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// An ISR typically queues a DPC, so increment the queued count for this CPU.
	c.dpcQueuedCount[cpu]++

	key := ISRKey{CPU: cpu, Vector: vector}

	// Store pending ISR for later correlation with DPC (using a pooled object)
	isr := isrEventPool.Get().(*ISREvent)
	*isr = ISREvent{InitialTime: initialTime, RoutineAddress: routineAddress, CPU: cpu, Vector: vector}
	c.pendingISRs[key] = isr

	// Only track per-driver metrics if enabled
	if c.config.EnablePerDriver {
		// Resolve driver name and update its last seen time
		driverName := c.resolveDriverNameUnsafe(routineAddress)
		c.driverLastSeen[driverName] = initialTime
	}

	// Clean up old pending ISRs periodically (reduce frequency to improve performance)
	if len(c.pendingISRs)%100 == 0 {
		c.cleanupOldISRs(initialTime)
	}
}

// ProcessDPCEvent processes a DPC event and correlates with ISR for latency calculation
func (c *PerfInfoInterruptCollector) ProcessDPCEvent(cpu uint16, initialTime time.Time,
	routineAddress uint64, durationMicros float64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Update DPC execution count for queue tracking
	c.dpcExecutedCount[cpu]++

	// Only resolve driver name if we need it for per-driver or count metrics
	var driverName string
	if c.config.EnablePerDriver {
		// Resolve driver name and track activity for bounded set management
		driverName = c.resolveDriverNameUnsafe(routineAddress)
		c.driverLastSeen[driverName] = initialTime

		// Record DPC duration for per-driver metrics
		c.recordDPCDuration(driverName, durationMicros)
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
		latencyMicros := float64(closestTime.Microseconds())
		c.recordISRToDPCLatency(latencyMicros)

		// Remove the matched ISR to prevent duplicate matches
		delete(c.pendingISRs, matchedKey)
		// Return the ISREvent to the pool for reuse
		isrEventPool.Put(matchedISR)
	}
}

// ProcessImageLoadEvent processes image load events to build driver database
func (c *PerfInfoInterruptCollector) ProcessImageLoadEvent(imageBase, imageSize uint64, fileName string) {
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

// ProcessImageUnloadEvent processes image unload events to remove driver metrics.
func (c *PerfInfoInterruptCollector) ProcessImageUnloadEvent(imageBase uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	imageInfo, exists := c.imageDatabase[imageBase]
	if !exists {
		return // We weren't tracking this image.
	}

	driverName := imageInfo.ImageName
	c.log.Debug().Str("driver_name", driverName).Msg("Image unloaded, removing associated metrics")

	// Remove all metrics associated with this driver.
	c.removeDriverMetrics(driverName)

	// Remove the image from the database.
	delete(c.imageDatabase, imageBase)

	// Invalidate the routine-to-driver name cache for all routines within this image's address space.
	// This is important to prevent stale cache entries if the same memory is reused.
	for routineAddr, name := range c.driverNames {
		if name == driverName {
			delete(c.driverNames, routineAddr)
		}
	}
}

// ProcessHardPageFaultEvent increments the hard page fault counter
func (c *PerfInfoInterruptCollector) ProcessHardPageFaultEvent() {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.hardPageFaultCount++
}

// resolveDriverNameUnsafe is an optimized version for use within locked contexts
// Assumes caller already holds the mutex
func (c *PerfInfoInterruptCollector) resolveDriverNameUnsafe(routineAddress uint64) string {
	// Check cache first
	if driverName, exists := c.driverNames[routineAddress]; exists {
		return driverName
	}

	// Binary search through image database
	for imageBase, imageInfo := range c.imageDatabase {
		if routineAddress >= imageBase && routineAddress < imageBase+imageInfo.ImageSize {
			driverName := imageInfo.ImageName

			// Cache the result for future lookups
			c.driverNames[routineAddress] = driverName
			return driverName
		}
	}

	// Unknown driver - cache the result to avoid repeated lookups
	c.driverNames[routineAddress] = "unknown"
	return "unknown"
}

// pruneStaleDrivers removes metrics for drivers that have been inactive for a configured timeout.
// This is called periodically from the Collect method.
func (c *PerfInfoInterruptCollector) pruneStaleDrivers() {
	now := time.Now()
	staleDrivers := make([]string, 0)

	for driverName, lastSeen := range c.driverLastSeen {
		if now.Sub(lastSeen) > c.driverTimeout {
			staleDrivers = append(staleDrivers, driverName)
		}
	}

	if len(staleDrivers) > 0 {
		c.log.Debug().Strs("drivers", staleDrivers).Msg("Pruning stale driver metrics due to inactivity")
		for _, driverName := range staleDrivers {
			c.removeDriverMetrics(driverName)
		}
	}
}

// removeDriverMetrics removes all metrics associated with a given driver name.
// The caller must hold the mutex.
func (c *PerfInfoInterruptCollector) removeDriverMetrics(driverName string) {
	// Remove from last seen tracking
	delete(c.driverLastSeen, driverName)

	// Remove from histogram data if per-driver metrics are enabled
	if c.config.EnablePerDriver {
		delete(c.dpcDurationHistograms, DPCDriverKey{ImageName: driverName})
	}
}

// recordISRToDPCLatency records a system-wide ISR to DPC latency sample
func (c *PerfInfoInterruptCollector) recordISRToDPCLatency(latencyMicros float64) {
	c.isrToDpcLatencyCount++
	c.isrToDpcLatencySum += latencyMicros
	// Find appropriate bucket
	for _, bucket := range ISRToDPCLatencyBuckets {
		if latencyMicros <= bucket {
			c.isrToDpcLatencyBuckets[bucket]++
			return
		}
	}
}

// recordDPCDuration records a DPC duration sample for a specific driver
func (c *PerfInfoInterruptCollector) recordDPCDuration(driverName string, durationMicros float64) {
	key := DPCDriverKey{ImageName: driverName}

	// Get or create histogram data for this driver
	hd, exists := c.dpcDurationHistograms[key]
	if !exists {
		hd = &histogramData{
			buckets: make(map[float64]int64, len(DPCDurationBuckets)),
			count:   0,
			sum:     0.0,
		}
		c.dpcDurationHistograms[key] = hd
	}

	// Update histogram data
	hd.count++
	hd.sum += durationMicros

	// Find appropriate bucket
	for _, bucket := range DPCDurationBuckets {
		if durationMicros <= bucket {
			hd.buckets[bucket]++
			return
		}
	}
}

// cleanupOldISRs removes ISRs older than 10ms to prevent memory leaks
func (c *PerfInfoInterruptCollector) cleanupOldISRs(currentTime time.Time) {
	const maxAge = 10 * time.Millisecond

	for key, isr := range c.pendingISRs {
		if currentTime.Sub(isr.InitialTime) > maxAge {
			delete(c.pendingISRs, key)
			// Return the ISREvent to the pool for reuse
			isrEventPool.Put(isr)
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

// formatCPU formats CPU number as string, using a cache to reduce allocations.
func (c *PerfInfoInterruptCollector) formatCPU(cpu uint16) string {
	if str, ok := c.cpuStringCache[cpu]; ok {
		return str
	}
	str := strconv.FormatUint(uint64(cpu), 10)
	c.cpuStringCache[cpu] = str
	return str
}
