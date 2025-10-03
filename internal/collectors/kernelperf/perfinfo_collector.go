// filepath: e:\Sources\go\projects\etw_exporter\perfinfo_collector.go
package kernelperf

import (
	"strconv"
	"sync"
	"time"

	"etw_exporter/internal/config"
	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"

	"github.com/phuslu/log"
	"github.com/prometheus/client_golang/prometheus"
)

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

// PerfCollector implements the logic for collecting real-time performance metrics.
type PerfCollector struct {
	// Configuration options
	config *config.PerfInfoConfig

	// State manager reference for image lookups
	sm *statemanager.KernelStateManager

	// Pending ISR tracking for latency correlation
	pendingISRs map[uint16][]*ISREvent // CPU -> slice of pending ISRs

	// Performance metrics data
	isrToDpcLatencyBuckets map[float64]int64 // Histogram buckets for system-wide latency
	isrToDpcLatencyCount   uint64
	isrToDpcLatencySum     float64
	dpcDurationHistograms  map[DPCDriverKey]*histogramData // {driver} -> histogram data

	// DPC queue tracking
	dpcQueuedCount   map[uint16]int64 // CPU -> queued count
	dpcExecutedCount map[uint16]int64 // CPU -> executed count

	// SMI gap detection (placeholder for future implementation) // TODO
	smiGapsDesc *prometheus.Desc

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
func NewPerfInfoCollector(config *config.PerfInfoConfig, sm *statemanager.KernelStateManager) *PerfCollector {
	collector := &PerfCollector{
		config:                 config,
		sm:                     sm,
		pendingISRs:            make(map[uint16][]*ISREvent, 64), // Pre-size for max CPU count
		isrToDpcLatencyBuckets: make(map[float64]int64, len(ISRToDPCLatencyBuckets)),
		isrToDpcLatencyCount:   0,
		isrToDpcLatencySum:     0.0,
		dpcQueuedCount:         make(map[uint16]int64, 64), // Pre-size for max CPU count
		dpcExecutedCount:       make(map[uint16]int64, 64),
		driverLastSeen:         make(map[string]time.Time, 100), // Track driver activity for bounded set
		driverTimeout:          15 * time.Minute,                // Prune drivers inactive for 15 mins
		log:                    logger.NewLoggerWithContext("perfinfo_collector"),
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
		"etw_perfinfo_isr_to_dpc_latency_microseconds",
		"Latency from ISR entry to DPC entry in microseconds. This measures DPC queue time.",
		nil, nil)

	collector.dpcExecutedDesc = prometheus.NewDesc(
		"etw_perfinfo_dpc_executed_total",
		"Total number of DPCs that began execution system-wide.",
		nil, nil)

	// Count number of DPCs queued system-wide over time this can be
	// used to infer DPC queue pressure.
	collector.dpcQueuedDesc = prometheus.NewDesc(
		"etw_perfinfo_dpc_queued_total",
		"Total number of DPCs queued for execution system-wide.",
		nil, nil)

	// Create per-driver descriptors only if enabled
	if config.EnablePerDriver {
		collector.dpcDurationDesc = prometheus.NewDesc(
			"etw_perfinfo_dpc_execution_time_microseconds",
			"DPC execution time by driver in microseconds",
			[]string{"image_name"}, nil)
	}

	// TODO: Define SMI metric but do not collect data for it yet.
	if config.EnableSMIDetection {
		collector.smiGapsDesc = prometheus.NewDesc(
			"etw_perfinfo_smi_gaps_microseconds",
			"SMI gap detection in microseconds, based on SampledProfile event gaps.",
			nil, nil)
	}

	// Create per-CPU descriptor only if enabled
	if config.EnablePerCPU {
		collector.dpcQueuedCPUDesc = prometheus.NewDesc(
			"etw_perfinfo_dpc_queued_cpu_total",
			"Total number of DPCs queued for execution by CPU.",
			[]string{"cpu"}, nil)

		collector.dpcExecutedCPUDesc = prometheus.NewDesc(
			"etw_perfinfo_dpc_executed_cpu_total",
			"Total number of DPCs that began execution by CPU.",
			[]string{"cpu"}, nil)
	}

	collector.log.Debug().Msg("Interrupt latency collector created")
	return collector
}

// Describe implements the prometheus.Collector interface
func (c *PerfCollector) Describe(ch chan<- *prometheus.Desc) {
	// Always include core system-wide metrics
	ch <- c.isrToDpcLatencyDesc
	ch <- c.dpcQueuedDesc
	ch <- c.dpcExecutedDesc

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
func (c *PerfCollector) Collect(ch chan<- prometheus.Metric) {
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

	c.log.Debug().Msg("Collected performance metrics")
}

// collectISRToDPCLatencyHistogram creates the system-wide ISR to DPC latency histogram
func (c *PerfCollector) collectISRToDPCLatencyHistogram(ch chan<- prometheus.Metric) {
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
func (c *PerfCollector) collectDPCDurationHistograms(ch chan<- prometheus.Metric) {
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
func (c *PerfCollector) collectDPCCounters(ch chan<- prometheus.Metric) {
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
func (c *PerfCollector) collectSMIGaps(ch chan<- prometheus.Metric) {
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
func (c *PerfCollector) ProcessISREvent(cpu uint16, vector uint16,
	initialTime time.Time, routineAddress uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// An ISR typically queues a DPC, so increment the queued count for this CPU.
	c.dpcQueuedCount[cpu]++

	// Store pending ISR for later correlation with DPC (using a pooled object)
	isr := isrEventPool.Get().(*ISREvent)
	*isr = ISREvent{InitialTime: initialTime, RoutineAddress: routineAddress, CPU: cpu, Vector: vector}
	c.pendingISRs[cpu] = append(c.pendingISRs[cpu], isr)

	// Only track per-driver metrics if enabled
	if c.config.EnablePerDriver {
		// Resolve driver name and update its last seen time
		driverName := c.resolveDriverNameUnsafe(routineAddress)
		if driverName != "unknown" {
			c.driverLastSeen[driverName] = initialTime
		}
	}

	// Clean up old pending ISRs periodically (reduce frequency to improve performance)
	if len(c.pendingISRs)%100 == 0 {
		c.cleanupOldISRs(initialTime)
	}
}

// ProcessDPCEvent processes a DPC event and correlates with ISR for latency calculation
func (c *PerfCollector) ProcessDPCEvent(cpu uint16, initialTime time.Time,
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
		if driverName != "unknown" {
			c.driverLastSeen[driverName] = initialTime

			// Record DPC duration for per-driver metrics
			c.recordDPCDuration(driverName, durationMicros)
		}
	}

	// Find matching ISR on same CPU for latency calculation
	var matchedISR *ISREvent
	var matchedIndex = -1 // To remove the element from the slice later

	// Look for ISR with closest timestamp on the same CPU. This is much faster.
	closestTime := time.Hour // Start with large value
	if isrsOnCPU, ok := c.pendingISRs[cpu]; ok {
		for i, isr := range isrsOnCPU {
			timeDiff := initialTime.Sub(isr.InitialTime)
			if timeDiff >= 0 && timeDiff < closestTime {
				closestTime = timeDiff
				matchedISR = isr
				matchedIndex = i
			}
		}
	}

	// If we found a matching ISR, record the latency
	if matchedISR != nil {
		latencyMicros := float64(closestTime.Microseconds())
		c.recordISRToDPCLatency(latencyMicros)

		// Remove the matched ISR from the slice to prevent duplicate matches.
		// This is an efficient way to remove an element from a slice without preserving order.
		isrsOnCPU := c.pendingISRs[cpu]
		isrsOnCPU[matchedIndex] = isrsOnCPU[len(isrsOnCPU)-1]
		c.pendingISRs[cpu] = isrsOnCPU[:len(isrsOnCPU)-1]

		// Return the ISREvent to the pool for reuse
		isrEventPool.Put(matchedISR)
	}
}

// ProcessImageLoadEvent is now a NO-OP. The state manager handles this directly.
func (c *PerfCollector) ProcessImageLoadEvent(imageBase, imageSize uint64, fileName string) {
	// This is intentionally left blank.
	// The KernelStateManager is now the source of truth for image information.
	// The handler will call KernelStateManager.AddImage directly.
}

// ProcessImageUnloadEvent processes image unload events to clear internal caches.
func (c *PerfCollector) ProcessImageUnloadEvent(imageBase uint64) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// The state manager handles the actual image removal and cache invalidation.
	// We still need to get the image name before it's gone to clean up our metrics.
	stateManager := c.sm
	info, ok := stateManager.GetImageForAddress(imageBase) // Get info before it's deleted
	if !ok {
		return
	}

	c.log.Trace().Str("driver_name", info.ImageName).Msg("Image unloaded, removing associated metrics")

	// Remove all metrics associated with this driver.
	c.removeDriverMetrics(info.ImageName)
}

// resolveDriverNameUnsafe is an optimized version for use within locked contexts
// Assumes caller already holds the mutex
func (c *PerfCollector) resolveDriverNameUnsafe(routineAddress uint64) string {
	// The state manager now has a high-performance cache. We query it directly.
	imageInfo, ok := c.sm.GetImageForAddress(routineAddress)
	if !ok {
		// Returning "unknown" for this single event is correct. The ImageLoad event
		// might be delayed, and we will try the full lookup again if this address
		// is seen in a future event.
		return "unknown"
	}

	return imageInfo.ImageName
}

// pruneStaleDrivers removes metrics for drivers that have been inactive for a configured timeout.
// This is called periodically from the Collect method.
func (c *PerfCollector) pruneStaleDrivers() {
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
func (c *PerfCollector) removeDriverMetrics(driverName string) {
	// Remove from last seen tracking
	delete(c.driverLastSeen, driverName)

	// Remove from histogram data if per-driver metrics are enabled
	if c.config.EnablePerDriver {
		delete(c.dpcDurationHistograms, DPCDriverKey{ImageName: driverName})
	}

	// The local routine address cache has been removed, so we no longer need to clean it.
}

// recordISRToDPCLatency records a system-wide ISR to DPC latency sample
func (c *PerfCollector) recordISRToDPCLatency(latencyMicros float64) {
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
func (c *PerfCollector) recordDPCDuration(driverName string, durationMicros float64) {
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
func (c *PerfCollector) cleanupOldISRs(currentTime time.Time) {
	const maxAge = 10 * time.Millisecond

	for cpu, isrs := range c.pendingISRs {
		survivingISRs := isrs[:0] // Re-slice to avoid allocation
		for _, isr := range isrs {
			if currentTime.Sub(isr.InitialTime) <= maxAge {
				survivingISRs = append(survivingISRs, isr)
			} else {
				// This ISR is old, return it to the pool.
				isrEventPool.Put(isr)
			}
		}

		if len(survivingISRs) > 0 {
			c.pendingISRs[cpu] = survivingISRs
		} else {
			// If no ISRs survived on this CPU, delete the entry to keep the map clean.
			delete(c.pendingISRs, cpu)
		}
	}
}

// formatCPU formats CPU number as string, using a cache to reduce allocations.
func (c *PerfCollector) formatCPU(cpu uint16) string {
	if str, ok := c.cpuStringCache[cpu]; ok {
		return str
	}
	str := strconv.FormatUint(uint64(cpu), 10)
	c.cpuStringCache[cpu] = str
	return str
}
