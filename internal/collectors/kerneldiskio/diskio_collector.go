package kerneldiskio

import (
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"

	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"
	"etw_exporter/internal/maps"

	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// DiskOperation defines the type for disk I/O operations.
type DiskOperation int

const (
	// OpRead represents a read operation.
	OpRead DiskOperation = iota
	// OpWrite represents a write operation.
	OpWrite
	// OpFlush represents a flush operation.
	OpFlush
	// opCount is an unexported constant that defines the number of operations.
	opCount
)

// opToString maps DiskOperation constants to their string representations for Prometheus labels.
var opToString = [opCount]string{
	OpRead:  "read",
	OpWrite: "write",
	OpFlush: "flush",
}

// processDiskMetrics holds I/O metrics for a single disk within a process.
type processDiskMetrics struct {
	IOCount      [opCount]*atomic.Int64 // Indexed by DiskOperation constants.
	BytesRead    *atomic.Int64
	BytesWritten *atomic.Int64
}

// processMetrics holds all disk I/O metrics for a single process instance.
type processMetrics struct {
	PID   uint32
	mu    sync.RWMutex // Protects the Disks map below
	Disks map[uint32]*processDiskMetrics
}

// DiskCollector implements prometheus.Collector for disk I/O related metrics.
// This collector follows Prometheus best practices by creating new metrics on each scrape
//
// All metrics are designed for low cardinality to maintain performance at scale.
type DiskCollector struct {
	// System-wide metrics, using simple maps protected by the collector's mutex.
	diskIOCount      map[uint32]*[opCount]*atomic.Int64 // disk -> [op] -> count
	diskBytesRead    map[uint32]*atomic.Int64           // disk -> count
	diskBytesWritten map[uint32]*atomic.Int64           // disk -> count

	// Per-process metrics, keyed by the unique process StartKey.
	perProcessMetrics maps.ConcurrentMap[uint64, *processMetrics] // startKey -> metrics

	// Synchronization for system-wide maps.
	mu  sync.RWMutex
	log *phusluadapter.SampledLogger

	// Metric Descriptors
	diskIOCountDesc         *prometheus.Desc
	diskReadBytesDesc       *prometheus.Desc
	diskWrittenBytesDesc    *prometheus.Desc
	processIOCountDesc      *prometheus.Desc
	processReadBytesDesc    *prometheus.Desc
	processWrittenBytesDesc *prometheus.Desc
}

// NewDiskIOCustomCollector creates a new disk I/O metrics custom collector.
func NewDiskIOCustomCollector() *DiskCollector {
	collector := &DiskCollector{
		// Initialize system-wide maps
		diskIOCount:      make(map[uint32]*[opCount]*atomic.Int64),
		diskBytesRead:    make(map[uint32]*atomic.Int64),
		diskBytesWritten: make(map[uint32]*atomic.Int64),

		// Initialize per-process map
		perProcessMetrics: maps.NewConcurrentMap[uint64, *processMetrics](),
		log:               logger.NewSampledLoggerCtx("diskio_collector"),

		// Disk I/O metrics
		diskIOCountDesc: prometheus.NewDesc(
			"etw_disk_io_operations_total",
			"Total number of disk I/O operations per disk and operation type",
			[]string{"disk", "operation"}, nil,
		),
		diskReadBytesDesc: prometheus.NewDesc(
			"etw_disk_read_bytes_total",
			"Total bytes read from disk",
			[]string{"disk"}, nil,
		),
		diskWrittenBytesDesc: prometheus.NewDesc(
			"etw_disk_written_bytes_total",
			"Total bytes written to disk",
			[]string{"disk"}, nil,
		),
		processIOCountDesc: prometheus.NewDesc(
			"etw_disk_process_io_operations_total",
			"Total number of disk I/O operations per process, disk, and operation type",
			[]string{"process_id", "process_start_key", "process_name", "disk", "operation"}, nil,
		),
		processReadBytesDesc: prometheus.NewDesc(
			"etw_disk_process_read_bytes_total",
			"Total bytes read from disk per process and disk",
			[]string{"process_id", "process_start_key", "process_name", "disk"}, nil,
		),
		processWrittenBytesDesc: prometheus.NewDesc(
			"etw_disk_process_written_bytes_total",
			"Total bytes written to disk per process and disk",
			[]string{"process_id", "process_start_key", "process_name", "disk"}, nil,
		),
	}

	// Register for post-scrape cleanup.
	statemanager.GetGlobalStateManager().RegisterCleaner(collector)

	return collector
}

// Describe implements prometheus.Collector.
// It sends the descriptors of all the metrics the collector can possibly export
// to the provided channel. This is called once during registration.
//
// Parameters:
//   - ch: Channel to send metric descriptors to
func (c *DiskCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.diskIOCountDesc
	ch <- c.diskReadBytesDesc
	ch <- c.diskWrittenBytesDesc
	ch <- c.processIOCountDesc
	ch <- c.processReadBytesDesc
	ch <- c.processWrittenBytesDesc
}

// Collect implements prometheus.Collector.
// It is called by Prometheus on each scrape and must create new metrics
//
// Parameters:
//   - ch: Channel to send metrics to
func (c *DiskCollector) Collect(ch chan<- prometheus.Metric) {
	stateManager := statemanager.GetGlobalStateManager()

	c.mu.RLock()
	// --- System-Wide Metrics ---
	for diskNumber, opArray := range c.diskIOCount {
		diskStr := strconv.FormatUint(uint64(diskNumber), 10)
		for op, count := range opArray {
			if count != nil {
				ch <- prometheus.MustNewConstMetric(
					c.diskIOCountDesc,
					prometheus.CounterValue,
					float64(count.Load()),
					diskStr,
					opToString[op],
				)
			}
		}
	}
	for diskNumber, bytes := range c.diskBytesRead {
		ch <- prometheus.MustNewConstMetric(
			c.diskReadBytesDesc,
			prometheus.CounterValue,
			float64(bytes.Load()),
			strconv.FormatUint(uint64(diskNumber), 10),
		)
	}
	for diskNumber, bytes := range c.diskBytesWritten {
		ch <- prometheus.MustNewConstMetric(
			c.diskWrittenBytesDesc,
			prometheus.CounterValue,
			float64(bytes.Load()),
			strconv.FormatUint(uint64(diskNumber), 10),
		)
	}
	c.mu.RUnlock()

	// --- Per-Process Metrics ---
	c.perProcessMetrics.Range(func(startKey uint64, metrics *processMetrics) bool {
		if processName, ok := stateManager.GetKnownProcessName(metrics.PID); ok {
			pidStr := strconv.FormatUint(uint64(metrics.PID), 10)
			startKeyStr := strconv.FormatUint(startKey, 10)

			metrics.mu.RLock()
			for diskNumber, diskMetrics := range metrics.Disks {
				diskStr := strconv.FormatUint(uint64(diskNumber), 10)

				// I/O Operation Counts
				for op, count := range diskMetrics.IOCount {
					ch <- prometheus.MustNewConstMetric(
						c.processIOCountDesc,
						prometheus.CounterValue,
						float64(count.Load()),
						pidStr,
						startKeyStr,
						processName,
						diskStr,
						opToString[op],
					)
				}

				// Bytes Read
				if bytes := diskMetrics.BytesRead.Load(); bytes > 0 {
					ch <- prometheus.MustNewConstMetric(
						c.processReadBytesDesc,
						prometheus.CounterValue,
						float64(bytes),
						pidStr,
						startKeyStr,
						processName,
						diskStr,
					)
				}

				// Bytes Written
				if bytes := diskMetrics.BytesWritten.Load(); bytes > 0 {
					ch <- prometheus.MustNewConstMetric(
						c.processWrittenBytesDesc,
						prometheus.CounterValue,
						float64(bytes),
						pidStr,
						startKeyStr,
						processName,
						diskStr,
					)
				}
			}
			metrics.mu.RUnlock()
		}
		return true
	})
}

// createDiskIoCounts initializes the maps and counters for a given disk number.
// This is called with the collector mutex held to ensure thread safety.
//
// Parameters:
//   - diskNumber: Physical disk number to initialize maps for
func (c *DiskCollector) createDiskIoCounts(diskNumber uint32) {
	c.diskIOCount[diskNumber] = new([opCount]*atomic.Int64)
	for i := range int(opCount) {
		c.diskIOCount[diskNumber][i] = new(atomic.Int64)
	}
	c.diskBytesRead[diskNumber] = new(atomic.Int64)
	c.diskBytesWritten[diskNumber] = new(atomic.Int64)
}

// RecordDiskIO records a disk I/O operation.
//
// Parameters:
//   - diskNumber: Physical disk number where the I/O occurred
//   - processID: Process ID that initiated the I/O operation
//   - startKey: The unique start key of the process
//   - transferSize: Number of bytes transferred in the operation
//   - isWrite: True for write operations, false for read operations
func (c *DiskCollector) RecordDiskIO(
	diskNumber uint32,
	processID uint32,
	startKey uint64,
	transferSize uint32,
	isWrite bool) {

	var operation DiskOperation
	if isWrite {
		operation = OpWrite
	} else {
		operation = OpRead
	}

	// --- System-Wide Metrics ---
	c.mu.Lock()
	// Atomically check and initialize the maps for a given disk number.
	// This prevents the race condition where multiple goroutines could try
	// to initialize the same map entry concurrently.
	if _, ok := c.diskIOCount[diskNumber]; !ok {
		c.createDiskIoCounts(diskNumber)
	}

	// At this point, all maps and counters are guaranteed to be initialized.
	c.diskIOCount[diskNumber][operation].Add(1)
	if isWrite {
		c.diskBytesWritten[diskNumber].Add(int64(transferSize))
	} else {
		c.diskBytesRead[diskNumber].Add(int64(transferSize))
	}
	c.mu.Unlock()

	// --- Per-Process Metrics ---
	if processID > 0 && startKey > 0 && statemanager.GetGlobalStateManager().IsKnownProcess(processID) {
		procMetrics := c.perProcessMetrics.LoadOrStore(startKey, func() *processMetrics {
			return &processMetrics{
				PID:   processID,
				Disks: make(map[uint32]*processDiskMetrics),
			}
		})

		// Lock for the entire duration of map access and atomic updates to ensure correctness.
		procMetrics.mu.Lock()
		diskMetrics, ok := procMetrics.Disks[diskNumber]
		if !ok {
			diskMetrics = &processDiskMetrics{
				BytesRead:    new(atomic.Int64),
				BytesWritten: new(atomic.Int64),
			}
			// Initialize all counters in the array
			for i := range int(opCount) {
				diskMetrics.IOCount[i] = new(atomic.Int64)
			}
			procMetrics.Disks[diskNumber] = diskMetrics
		}

		// Update counters while holding the lock.
		diskMetrics.IOCount[operation].Add(1)
		if isWrite {
			diskMetrics.BytesWritten.Add(int64(transferSize))
		} else {
			diskMetrics.BytesRead.Add(int64(transferSize))
		}
		procMetrics.mu.Unlock()
	}
}

// RecordDiskFlush records a disk flush operation.
//
// Parameters:
//   - diskNumber: Physical disk number where the flush occurred
func (c *DiskCollector) RecordDiskFlush(diskNumber uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Atomically check and initialize the maps for a given disk number.
	if _, ok := c.diskIOCount[diskNumber]; !ok {
		c.createDiskIoCounts(diskNumber)
	}

	// Update counter.
	c.diskIOCount[diskNumber][OpFlush].Add(1)
}

// CleanupTerminatedProcesses implements the statemanager.PostScrapeCleaner interface.
// This method is called by the KernelStateManager after a scrape is complete to
// allow the collector to safely clean up its internal state for terminated processes.
func (c *DiskCollector) CleanupTerminatedProcesses(terminatedProcs map[uint32]uint64) {
	cleanedCount := 0
	for _, startKey := range terminatedProcs {
		if _, deleted := c.perProcessMetrics.LoadAndDelete(startKey); deleted {
			cleanedCount++
		}
	}

	if cleanedCount > 0 {
		c.log.Debug().Int("count", cleanedCount).Msg("Cleaned up disk I/O counters for terminated processes")
	}
}
