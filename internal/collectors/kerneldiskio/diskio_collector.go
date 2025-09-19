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
	mu    sync.RWMutex // Protects the Disks map below
	Disks map[uint32]*processDiskMetrics
}

// aggregationKey is used for on-the-fly aggregation in the Collect method.
type aggregationKey struct {
	processName   string
	imageChecksum uint32
	sessionID     uint32
	diskNumber    uint32
}

// aggregatedMetric holds aggregated metrics for a unique process signature.
type aggregatedMetric struct {
	ioCount      [opCount]int64
	bytesRead    int64
	bytesWritten int64
}

// DiskCollector implements the logic for collecting disk I/O metrics.
type DiskCollector struct {
	// System-wide metrics, keyed by disk number.
	diskIOCount      map[uint32]*[opCount]*atomic.Int64 // disk -> [op] -> count
	diskBytesRead    map[uint32]*atomic.Int64           // disk -> count
	diskBytesWritten map[uint32]*atomic.Int64           // disk -> count

	// Per-process metrics, keyed by the unique process StartKey.
	perProcessMetrics maps.ConcurrentMap[uint64, *processMetrics] // startKey -> metrics

	// Synchronization for system-wide maps.
	mu  sync.RWMutex
	log *phusluadapter.SampledLogger

	// Aggregation map pool
	aggregationPool *sync.Pool

	// Global state manager for process information.
	stateManager *statemanager.KernelStateManager

	// Metric Descriptors
	diskIOCountDesc         *prometheus.Desc
	diskReadBytesDesc       *prometheus.Desc
	diskWrittenBytesDesc    *prometheus.Desc
	processIOCountDesc      *prometheus.Desc
	processReadBytesDesc    *prometheus.Desc
	processWrittenBytesDesc *prometheus.Desc
}

// NewDiskIOCustomCollector creates a new disk I/O metrics custom collector.
func NewDiskIOCustomCollector(sm *statemanager.KernelStateManager) *DiskCollector {
	collector := &DiskCollector{
		log:          logger.NewSampledLoggerCtx("diskio_collector"),
		stateManager: sm,

		// Initialize system-wide maps
		diskIOCount:      make(map[uint32]*[opCount]*atomic.Int64),
		diskBytesRead:    make(map[uint32]*atomic.Int64),
		diskBytesWritten: make(map[uint32]*atomic.Int64),

		// Initialize per-process map
		perProcessMetrics: maps.NewConcurrentMap[uint64, *processMetrics](),

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
			[]string{"process_name", "image_checksum", "session_id", "disk", "operation"}, nil,
		),
		processReadBytesDesc: prometheus.NewDesc(
			"etw_disk_process_read_bytes_total",
			"Total bytes read from disk per process and disk",
			[]string{"process_name", "image_checksum", "session_id", "disk"}, nil,
		),
		processWrittenBytesDesc: prometheus.NewDesc(
			"etw_disk_process_written_bytes_total",
			"Total bytes written to disk per process and disk",
			[]string{"process_name", "image_checksum", "session_id", "disk"}, nil,
		),
	}

	collector.aggregationPool = &sync.Pool{
		New: func() any {
			return make(map[aggregationKey]*aggregatedMetric, 512)
		},
	}

	// Register for post-scrape cleanup.
	sm.RegisterCleaner(collector)

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
	stateManager := c.stateManager

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

	// --- Per-Process Metrics (with on-the-fly aggregation) ---

	// This map will store the aggregated metrics, keyed by the program's identity.
	aggregatedMetrics := c.aggregationPool.Get().(map[aggregationKey]*aggregatedMetric)
	defer func() {
		// Clear map for reuse and return to pool.
		for k := range aggregatedMetrics {
			delete(aggregatedMetrics, k)
		}
		c.aggregationPool.Put(aggregatedMetrics)
	}()

	c.perProcessMetrics.Range(func(startKey uint64, metrics *processMetrics) bool {
		// Look up the process metadata from the state manager using the start key.
		procInfo, ok := stateManager.GetProcessInfoBySK(startKey)
		if !ok {
			return true // Process terminated and cleaned up, skip.
		}

		metrics.mu.RLock()
		for diskNumber, diskMetrics := range metrics.Disks {
			key := aggregationKey{
				processName:   procInfo.Name,
				imageChecksum: procInfo.ImageChecksum,
				sessionID:     procInfo.SessionID,
				diskNumber:    diskNumber,
			}

			// Find or create the aggregate entry for this key.
			agg, exists := aggregatedMetrics[key]
			if !exists {
				agg = &aggregatedMetric{}
				aggregatedMetrics[key] = agg
			}

			// Add the current process instance's stats to the aggregate.
			for op, count := range diskMetrics.IOCount {
				agg.ioCount[op] += count.Load()
			}
			agg.bytesRead += diskMetrics.BytesRead.Load()
			agg.bytesWritten += diskMetrics.BytesWritten.Load()
		}
		metrics.mu.RUnlock()
		return true
	})

	// Now, create the Prometheus metrics from the aggregated data.
	for key, data := range aggregatedMetrics {
		diskStr := strconv.FormatUint(uint64(key.diskNumber), 10)
		checksumStr := "0x" + strconv.FormatUint(uint64(key.imageChecksum), 16)
		sessionIDStr := strconv.FormatUint(uint64(key.sessionID), 10)

		// I/O Operation Counts
		for op, count := range data.ioCount {
			if count == 0 {
				continue
			}
			ch <- prometheus.MustNewConstMetric(
				c.processIOCountDesc,
				prometheus.CounterValue,
				float64(count),
				key.processName,
				checksumStr,
				sessionIDStr,
				diskStr,
				opToString[op],
			)
		}

		// Bytes Read
		if data.bytesRead > 0 {
			ch <- prometheus.MustNewConstMetric(
				c.processReadBytesDesc,
				prometheus.CounterValue,
				float64(data.bytesRead),
				key.processName,
				checksumStr,
				sessionIDStr,
				diskStr,
			)
		}

		// Bytes Written
		if data.bytesWritten > 0 {
			ch <- prometheus.MustNewConstMetric(
				c.processWrittenBytesDesc,
				prometheus.CounterValue,
				float64(data.bytesWritten),
				key.processName,
				checksumStr,
				sessionIDStr,
				diskStr,
			)
		}
	}
	c.log.Debug().Msg("Collected disk I/O metrics")
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
//   - startKey: The unique start key of the process
//   - transferSize: Number of bytes transferred in the operation
//   - isWrite: True for write operations, false for read operations
func (c *DiskCollector) RecordDiskIO(
	diskNumber uint32,
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
	if startKey > 0 && c.stateManager.IsTrackedStartKey(startKey) {
		procMetrics, _ := c.perProcessMetrics.LoadOrStore(startKey, func() *processMetrics {
			return &processMetrics{
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
func (c *DiskCollector) CleanupTerminatedProcesses(terminatedProcs map[uint64]uint32) {
	cleanedCount := 0
	for startKey := range terminatedProcs {
		if _, deleted := c.perProcessMetrics.LoadAndDelete(startKey); deleted {
			cleanedCount++
		}
	}

	if cleanedCount > 0 {
		c.log.Debug().Int("count", cleanedCount).Msg("Cleaned up disk I/O counters for terminated processes")
	}
}
