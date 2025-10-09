// filepath: e:\Sources\go\projects\etw_exporter\perfinfo_collector.go
package kernelperf

import (
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"etw_exporter/internal/config"
	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
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
// rate(etw_perfinfo_dpc_queued_cpu_total[1m]) - rate(etw_perfinfo_dpc_executed_cpu_total[1m])
//
// A sustained positive result from this query indicates that DPCs are being
// queued faster than they are being executed, which is a clear sign of high
// DPC latency and potential CPU saturation at high IRQL.

// metricsBuffer holds a complete set of metrics for one scrape interval.
// This is the unit that gets swapped during double-buffering.
type metricsBuffer struct {
	// lastDPCPerCPU tracks the last seen DPC event for each CPU. It is the core
	// of our DPC duration calculation logic.
	//
	// The duration of a DPC is the time from its start until the start of the
	// next significant event on the same CPU core. In the Windows kernel, DPCs
	// on a single core are serialized. Therefore, the next significant event
	// will either be:
	//  1. The next DPC event: Its start time marks the end of the previous one.
	//  2. A Context Switch (CSwitch) event: This signifies that the DPC queue
	//     for that core was empty and the scheduler is now running.
	//
	// This map is keyed by the CPU number from the event header.
	// "Whatever comes first" (the next DPC or a CSwitch) on a given CPU correctly
	// finalizes the duration of the pending DPC.
	lastDPCPerCPU map[uint16]pendingDPCInfo

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
}

// pendingDPCInfo holds the data for a DPC event that is waiting for the next
// DPC on the same CPU to determine its duration.
type pendingDPCInfo struct {
	initialTime    time.Time
	routineAddress uint64
}

// newMetricsBuffer creates a new, initialized metrics buffer.
func newMetricsBuffer() *metricsBuffer {
	numCPU := runtime.NumCPU()
	b := &metricsBuffer{
		lastDPCPerCPU:          make(map[uint16]pendingDPCInfo, numCPU),
		pendingISRs:            make(map[uint16][]*ISREvent, numCPU),
		isrToDpcLatencyBuckets: make(map[float64]int64, len(ISRToDPCLatencyBuckets)),
		dpcQueuedCount:         make(map[uint16]int64, numCPU),
		dpcExecutedCount:       make(map[uint16]int64, numCPU),
		dpcDurationHistograms:  make(map[DPCDriverKey]*histogramData, 50),
	}
	for _, bucket := range ISRToDPCLatencyBuckets {
		b.isrToDpcLatencyBuckets[bucket] = 0
	}
	return b
}

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

	// activeBuffer holds the pointer to the metrics buffer currently being written to
	// by the handler goroutine. It is swapped with a clean buffer during collection.
	activeBuffer atomic.Pointer[metricsBuffer]
	bufferPool   sync.Pool

	// SMI gap detection (placeholder for future implementation) // TODO
	smiGapsDesc *prometheus.Desc

	// Synchronization for this collector is handled by the atomic swap of buffers for
	// the writer, and a mutex for the reader to merge data into persistent metrics.
	mu                    sync.Mutex // Protects all persistent metrics below
	totalDpcQueuedCount   map[uint16]uint64
	totalDpcExecutedCount map[uint16]uint64
	totalIsrToDpcLatency  *histogramData
	totalDpcDurations     map[DPCDriverKey]*histogramData

	log *phusluadapter.SampledLogger

	// Metric descriptors
	isrToDpcLatencyDesc *prometheus.Desc
	dpcDurationDesc     *prometheus.Desc
	dpcQueuedDesc       *prometheus.Desc
	dpcExecutedDesc     *prometheus.Desc
	dpcQueuedCPUDesc    *prometheus.Desc
	dpcExecutedCPUDesc  *prometheus.Desc

	cpuStringCache map[uint16]string // Cache for formatCPU to reduce allocations
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
		config: config,
		sm:     sm,
		log:    logger.NewSampledLoggerCtx("perfinfo_collector"),
		bufferPool: sync.Pool{
			New: func() any {
				return newMetricsBuffer()
			},
		},
		cpuStringCache:        make(map[uint16]string, 64), // Cache for formatCPU to reduce allocations
		totalDpcQueuedCount:   make(map[uint16]uint64),
		totalDpcExecutedCount: make(map[uint16]uint64),
		totalIsrToDpcLatency:  &histogramData{buckets: make(map[float64]int64)},
		totalDpcDurations:     make(map[DPCDriverKey]*histogramData),
	}

	// Set the initial active buffer
	collector.activeBuffer.Store(newMetricsBuffer())

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
	// 1. Atomically swap the active buffer with a new, clean one.
	newBuffer := c.bufferPool.Get().(*metricsBuffer)
	oldBuffer := c.activeBuffer.Swap(newBuffer)

	// 2. Merge the data from the old buffer into the persistent, cumulative metrics.
	// This is the only section where a lock is held, and it's on the collector's side,
	// not the high-frequency writer path.
	c.mu.Lock()

	// Merge DPC counters
	for cpu, count := range oldBuffer.dpcQueuedCount {
		c.totalDpcQueuedCount[cpu] += uint64(count)
	}
	for cpu, count := range oldBuffer.dpcExecutedCount {
		c.totalDpcExecutedCount[cpu] += uint64(count)
	}

	// Merge system-wide ISR-to-DPC latency histogram
	c.totalIsrToDpcLatency.count += oldBuffer.isrToDpcLatencyCount
	c.totalIsrToDpcLatency.sum += oldBuffer.isrToDpcLatencySum
	for bucket, count := range oldBuffer.isrToDpcLatencyBuckets {
		c.totalIsrToDpcLatency.buckets[bucket] += count
	}

	// Merge per-driver DPC duration histograms
	for key, hd := range oldBuffer.dpcDurationHistograms {
		totalHd, exists := c.totalDpcDurations[key]
		if !exists {
			totalHd = &histogramData{buckets: make(map[float64]int64, len(DPCDurationBuckets))}
			c.totalDpcDurations[key] = totalHd
		}
		totalHd.count += hd.count
		totalHd.sum += hd.sum
		for bucket, count := range hd.buckets {
			totalHd.buckets[bucket] += count
		}
	}

	// Prune metrics for unloaded drivers from the persistent map.
	cleanedImages := c.sm.Images.GetCleanedImages()
	if len(cleanedImages) > 0 {
		c.log.Debug().Strs("drivers", cleanedImages).Msg("Pruning driver metrics for unloaded images")
		for _, imageName := range cleanedImages {
			delete(c.totalDpcDurations, DPCDriverKey{ImageName: imageName})
		}
	}

	c.mu.Unlock()

	// 3. Collect and send the cumulative metrics to Prometheus.
	c.collectMetrics(ch)

	// 4. After processing, clear the old buffer and return it to the pool for reuse.
	c.cleanupOldISRs(time.Now(), oldBuffer) // Return any remaining ISRs to their pool
	*oldBuffer = *newMetricsBuffer()        // Zero out the buffer's contents
	c.bufferPool.Put(oldBuffer)

	c.log.Debug().Msg("Collected performance metrics")
}

// collectMetrics sends all cumulative metrics to the Prometheus channel.
// This function assumes the caller has already handled buffer swapping and aggregation.
func (c *PerfCollector) collectMetrics(ch chan<- prometheus.Metric) {
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
}

// collectISRToDPCLatencyHistogram creates the system-wide ISR to DPC latency histogram
func (c *PerfCollector) collectISRToDPCLatencyHistogram(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()

	cumulativeBuckets := make(map[float64]uint64)
	var cumulativeCount uint64

	// Iterate over the defined buckets in order to calculate cumulative counts
	for _, bucketBound := range ISRToDPCLatencyBuckets {
		count := uint64(c.totalIsrToDpcLatency.buckets[bucketBound])
		cumulativeCount += count
		cumulativeBuckets[bucketBound] = cumulativeCount
	}

	// Create histogram metric with accurate count and sum
	histogram := prometheus.MustNewConstHistogram(
		c.isrToDpcLatencyDesc,
		c.totalIsrToDpcLatency.count,
		c.totalIsrToDpcLatency.sum,
		cumulativeBuckets,
	)

	ch <- histogram
}

// collectDPCDurationHistograms creates DPC duration histograms by driver
func (c *PerfCollector) collectDPCDurationHistograms(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()

	for driverKey, hd := range c.totalDpcDurations {
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

// collectDPCCounters creates DPC queue depth metrics
func (c *PerfCollector) collectDPCCounters(ch chan<- prometheus.Metric) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Create a set of all CPUs from both maps to ensure we don't miss any.
	allCPUs := make(map[uint16]struct{})
	for cpu := range c.totalDpcQueuedCount {
		allCPUs[cpu] = struct{}{}
	}
	for cpu := range c.totalDpcExecutedCount {
		allCPUs[cpu] = struct{}{}
	}

	var totalQueued, totalExecuted uint64

	for cpu := range allCPUs {
		queued := c.totalDpcQueuedCount[cpu]
		executed := c.totalDpcExecutedCount[cpu]
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

	// Load the current active buffer. This is a single atomic read.
	buffer := c.activeBuffer.Load()

	// An ISR typically queues a DPC, so increment the queued count for this CPU.
	buffer.dpcQueuedCount[cpu]++

	// Store pending ISR for later correlation with DPC (using a pooled object)
	isr := isrEventPool.Get().(*ISREvent)
	*isr = ISREvent{InitialTime: initialTime, RoutineAddress: routineAddress, CPU: cpu, Vector: vector}
	buffer.pendingISRs[cpu] = append(buffer.pendingISRs[cpu], isr)

	// Clean up old pending ISRs periodically (reduce frequency to improve performance)
	if len(buffer.pendingISRs)%100 == 0 {
		c.cleanupOldISRs(initialTime, buffer)
	}
}

// ProcessDPCEvent processes a DPC event. It finalizes the previous DPC on the same
// CPU and stores the current one as pending.
func (c *PerfCollector) ProcessDPCEvent(cpu uint16, eventTime, initialTime time.Time, routineAddress uint64) {
	buffer := c.activeBuffer.Load()

	// A new DPC event marks the end of any previously pending DPC on the same CPU.
	// We use the current event's timestamp as the end time for the previous one.
	// If there was no previous DPC (e.g., after a context switch), this function does nothing.
	// A new DPC event marks the end of any previously pending DPC on the same CPU.
	// We use the current event's timestamp as the end time for the previous one.
	// If there was no previous DPC (e.g., after a context switch), this function does nothing.
	c.finalizeAndClearPendingDPC(buffer, cpu, eventTime)

	// The current DPC is now stored as the new "pending" event. It is NOT lost.
	// Its duration will be calculated and it will be counted when the *next*
	// DPC or a context switch occurs on this CPU.
	buffer.lastDPCPerCPU[cpu] = pendingDPCInfo{
		initialTime:    initialTime,
		routineAddress: routineAddress,
	}
}

// ProcessCSwitchEvent processes a context switch event to finalize a pending DPC.
func (c *PerfCollector) ProcessCSwitchEvent(cpu uint16, eventTime time.Time) {
	buffer := c.activeBuffer.Load()

	// A context switch signifies that the DPC queue is idle, marking the end
	// of any pending DPC on that CPU.
	c.finalizeAndClearPendingDPC(buffer, cpu, eventTime)
}

// finalizeAndClearPendingDPC checks for a pending DPC on a given CPU, processes it,
// and clears it. This is called by the writer goroutine and operates on the active buffer.
func (c *PerfCollector) finalizeAndClearPendingDPC(buffer *metricsBuffer, cpu uint16, endTime time.Time) {
	if lastDPC, exists := buffer.lastDPCPerCPU[cpu]; exists {
		durationMicros := float64(endTime.Sub(lastDPC.initialTime).Microseconds())

		// Update DPC execution count for queue tracking
		buffer.dpcExecutedCount[cpu]++

		// Only resolve driver name if we need it for per-driver metrics
		if c.config.EnablePerDriver {
			driverName := c.resolveDriverName(lastDPC.routineAddress)
			if driverName != "unknown" {
				// For per-driver metrics
				c.recordDPCDuration(buffer, driverName, durationMicros)
			}
		}

		// Find and correlate with a matching ISR
		c.correlateISR(buffer, cpu, lastDPC.initialTime)

		// The DPC is no longer pending and its duration has been recorded.
		// We must remove it from the map to prevent it from being processed again.
		delete(buffer.lastDPCPerCPU, cpu)
	}
}

// correlateISR finds a matching ISR for a completed DPC event to calculate latency.
func (c *PerfCollector) correlateISR(buffer *metricsBuffer, cpu uint16, dpcInitialTime time.Time) {
	var matchedISR *ISREvent
	var matchedIndex = -1 // To remove the element from the slice later

	// Look for ISR with closest timestamp on the same CPU. This is much faster.
	closestTime := time.Hour // Start with large value
	if isrsOnCPU, ok := buffer.pendingISRs[cpu]; ok {
		for i, isr := range isrsOnCPU {
			timeDiff := dpcInitialTime.Sub(isr.InitialTime)
			if timeDiff >= 0 && timeDiff < closestTime {
				closestTime = timeDiff
				matchedISR = isr
				matchedIndex = i
			}
		}
	}

	if matchedISR != nil {
		latencyMicros := float64(closestTime.Microseconds())
		c.recordISRToDPCLatency(buffer, latencyMicros)

		// Remove the matched ISR from the slice to prevent duplicate matches.
		// This is an efficient way to remove an element from a slice without preserving order.
		isrsOnCPU := buffer.pendingISRs[cpu]
		isrsOnCPU[matchedIndex] = isrsOnCPU[len(isrsOnCPU)-1]
		buffer.pendingISRs[cpu] = isrsOnCPU[:len(isrsOnCPU)-1]

		isrEventPool.Put(matchedISR)
	}
}

// resolveDriverName is an optimized version that does not require a lock.
func (c *PerfCollector) resolveDriverName(routineAddress uint64) string {
	// The state manager now has a high-performance cache. We query it directly.
	imageInfo, ok := c.sm.Images.GetImageForAddress(routineAddress)
	if !ok {
		return "unknown"
	}
	return imageInfo.ImageName
}

// recordISRToDPCLatency records a system-wide ISR to DPC latency sample
func (c *PerfCollector) recordISRToDPCLatency(buffer *metricsBuffer, latencyMicros float64) {
	buffer.isrToDpcLatencyCount++
	buffer.isrToDpcLatencySum += latencyMicros
	// Find appropriate bucket
	for _, bucket := range ISRToDPCLatencyBuckets {
		if latencyMicros <= bucket {
			buffer.isrToDpcLatencyBuckets[bucket]++
			return
		}
	}
}

// recordDPCDuration records a DPC duration sample for a specific driver
func (c *PerfCollector) recordDPCDuration(buffer *metricsBuffer, driverName string, durationMicros float64) {
	key := DPCDriverKey{ImageName: driverName}

	// Get or create histogram data for this driver
	hd, exists := buffer.dpcDurationHistograms[key]
	if !exists {
		hd = &histogramData{
			buckets: make(map[float64]int64, len(DPCDurationBuckets)),
			count:   0,
			sum:     0.0,
		}
		buffer.dpcDurationHistograms[key] = hd
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
func (c *PerfCollector) cleanupOldISRs(currentTime time.Time, buffer *metricsBuffer) {
	const maxAge = 10 * time.Millisecond

	for cpu, isrs := range buffer.pendingISRs {
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
			buffer.pendingISRs[cpu] = survivingISRs
		} else {
			// If no ISRs survived on this CPU, delete the entry to keep the map clean.
			delete(buffer.pendingISRs, cpu)
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
