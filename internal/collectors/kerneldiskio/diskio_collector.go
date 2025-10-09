package kerneldiskio

import (
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"

	"etw_exporter/internal/config"
	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"

	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// DiskCollector implements the logic for collecting disk I/O metrics.
// It collects system-wide metrics directly and reads pre-aggregated per-process
// metrics from the KernelStateManager.
type DiskCollector struct {
	// System-wide metrics, keyed by disk number.
	diskIOCount      map[uint32]*[statemanager.DiskOpCount]atomic.Int64 // disk -> [op] -> count
	diskBytesRead    map[uint32]*atomic.Int64                           // disk -> count
	diskBytesWritten map[uint32]*atomic.Int64                           // disk -> count

	// Per-process metrics are now managed and aggregated by the KernelStateManager.
	// This collector is now a "dumb" reader of that aggregated state.

	// Synchronization for system-wide maps.
	mu     sync.RWMutex
	log    *phusluadapter.SampledLogger
	config *config.DiskIOConfig

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
func NewDiskIOCustomCollector(config *config.DiskIOConfig, sm *statemanager.KernelStateManager) *DiskCollector {
	collector := &DiskCollector{
		log:          logger.NewSampledLoggerCtx("diskio_collector"),
		stateManager: sm,
		config:       config,

		// Initialize system-wide maps
		diskIOCount:      make(map[uint32]*[statemanager.DiskOpCount]atomic.Int64),
		diskBytesRead:    make(map[uint32]*atomic.Int64),
		diskBytesWritten: make(map[uint32]*atomic.Int64),

		// Per-process map has been removed.

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
			[]string{"process_name", "service_name", "pe_checksum", "session_id", "disk", "operation"}, nil,
		),
		processReadBytesDesc: prometheus.NewDesc(
			"etw_disk_process_read_bytes_total",
			"Total bytes read from disk per process and disk",
			[]string{"process_name", "service_name", "pe_checksum", "session_id", "disk"}, nil,
		),
		processWrittenBytesDesc: prometheus.NewDesc(
			"etw_disk_process_written_bytes_total",
			"Total bytes written to disk per process and disk",
			[]string{"process_name", "service_name", "pe_checksum", "session_id", "disk"}, nil,
		),
	}

	// This collector no longer manages its own state, so it does not need to
	// register as a PostScrapeCleaner.

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
	c.mu.RLock()
	// --- System-Wide Metrics ---
	for diskNumber, opArray := range c.diskIOCount {
		diskStr := strconv.FormatUint(uint64(diskNumber), 10)
		for op := range opArray {
			ch <- prometheus.MustNewConstMetric(
				c.diskIOCountDesc,
				prometheus.CounterValue,
				float64(opArray[op].Load()),
				diskStr,
				statemanager.DiskOpToString[op],
			)
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

	// --- Per-Process Metrics (reading from pre-aggregated state) ---
	c.stateManager.RangeAggregatedMetrics(func(key statemanager.ProgramAggregationKey, metrics *statemanager.AggregatedProgramMetrics) bool {
		// Check if this program has any disk metrics to report.
		if metrics.Disk == nil {
			return true // Continue to next program
		}

		peTimestampStr := "0x" + strconv.FormatUint(uint64(key.UniqueHash), 16)
		sessionIDStr := strconv.FormatUint(uint64(key.SessionID), 10)

		for diskNum, diskData := range metrics.Disk.Disks {
			diskStr := strconv.FormatUint(uint64(diskNum), 10)

			// I/O Operation Counts
			for op, count := range diskData.IOCount {
				if count == 0 {
					continue
				}
				ch <- prometheus.MustNewConstMetric(
					c.processIOCountDesc,
					prometheus.CounterValue,
					float64(count),
					key.Name,
					key.ServiceName,
					peTimestampStr,
					sessionIDStr,
					diskStr,
					statemanager.DiskOpToString[op],
				)
			}

			// Bytes Read
			if diskData.BytesRead > 0 {
				ch <- prometheus.MustNewConstMetric(
					c.processReadBytesDesc,
					prometheus.CounterValue,
					float64(diskData.BytesRead),
					key.Name,
					key.ServiceName,
					peTimestampStr,
					sessionIDStr,
					diskStr,
				)
			}

			// Bytes Written
			if diskData.BytesWritten > 0 {
				ch <- prometheus.MustNewConstMetric(
					c.processWrittenBytesDesc,
					prometheus.CounterValue,
					float64(diskData.BytesWritten),
					key.Name,
					key.ServiceName,
					peTimestampStr,
					sessionIDStr,
					diskStr,
				)
			}
		}
		return true // Continue iteration
	})

	c.log.Debug().Msg("Collected disk I/O metrics")
}

// createDiskIoCounts initializes the maps and counters for a given disk number.
// This is called with the collector mutex held to ensure thread safety.
//
// Parameters:
//   - diskNumber: Physical disk number to initialize maps for
func (c *DiskCollector) createDiskIoCounts(diskNumber uint32) {
	// This function is now only for system-wide metrics.
	// The array is of value types, not pointers.
	c.diskIOCount[diskNumber] = new([statemanager.DiskOpCount]atomic.Int64)
	c.diskBytesRead[diskNumber] = new(atomic.Int64)
	c.diskBytesWritten[diskNumber] = new(atomic.Int64)
}

// RecordDiskIO records a disk I/O operation.
// This now only updates system-wide metrics and delegates the per-process
// recording to the high-performance KernelStateManager.
func (c *DiskCollector) RecordDiskIO(
	diskNumber uint32,
	pData *statemanager.ProcessData,
	transferSize uint32,
	isWrite bool) {

	var operation statemanager.DiskOperation
	if isWrite {
		operation = statemanager.DiskOpWrite
	} else {
		operation = statemanager.DiskOpRead
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
	// Delegate to the new centralized state manager logic.
	//if config.DiskIOConfig.PerProcessMetrics {
	if pData != nil {
		pData.RecordDiskIO(diskNumber, transferSize, isWrite)
	}
	//}
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
	c.diskIOCount[diskNumber][statemanager.DiskOpFlush].Add(1)
}
