package kerneldiskio

import (
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"

	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"

	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// DiskIOCustomCollector implements prometheus.Collector for disk I/O related metrics.
// This collector follows Prometheus best practices by creating new metrics on each scrape
//
// It provides high-performance aggregated metrics for:
// - Disk I/O operations (read/write/flush) per disk and per process
// - Bytes transferred (read/written) per disk and per process
//
// All metrics are designed for low cardinality to maintain performance at scale.
type DiskIOCustomCollector struct {
	// Atomic counters for high-frequency operations
	diskIOCount      map[DiskIOKey]*int64 // {diskNumber, operation} -> count
	diskBytesRead    map[uint32]*int64    // diskNumber -> bytes read
	diskBytesWritten map[uint32]*int64    // diskNumber -> bytes written

	processIOCount      map[ProcessIOKey]*int64    // {processID, diskNumber, operation} -> count
	processBytesRead    map[ProcessBytesKey]*int64 // {processID, diskNumber} -> bytes read
	processBytesWritten map[ProcessBytesKey]*int64 // {processID, diskNumber} -> bytes written

	// Synchronization
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

// DiskIOKey represents a composite key for disk I/O operations.
type DiskIOKey struct {
	DiskNumber uint32
	Operation  string
}

// ProcessIOKey represents a composite key for process I/O operations.
type ProcessIOKey struct {
	ProcessID  uint32
	DiskNumber uint32
	Operation  string
}

// ProcessBytesKey represents a composite key for process bytes transferred.
type ProcessBytesKey struct {
	ProcessID  uint32
	DiskNumber uint32
}

// DiskIOMetricsData holds aggregated data for a single scrape
type DiskIOMetricsData struct {
	DiskIOCount         map[DiskIOKey]DiskIOCountData        // {diskNumber, operation} -> count data
	DiskBytesRead       map[uint32]int64                     // diskNumber -> bytes read
	DiskBytesWritten    map[uint32]int64                     // diskNumber -> bytes written
	ProcessIOCount      map[ProcessIOKey]ProcessIOCountData  // {processID, diskNumber, operation} -> count data
	ProcessBytesRead    map[ProcessBytesKey]ProcessBytesData // {processID, diskNumber} -> bytes read data
	ProcessBytesWritten map[ProcessBytesKey]ProcessBytesData // {processID, diskNumber} -> bytes written data
}

// DiskIOCountData holds disk I/O count data with labels
type DiskIOCountData struct {
	DiskNumber uint32
	Operation  string
	Count      int64
}

// ProcessIOCountData holds process I/O count data with labels
type ProcessIOCountData struct {
	ProcessID   uint32
	ProcessName string
	DiskNumber  uint32
	Operation   string
	Count       int64
}

// ProcessBytesData holds process bytes transfer data with labels
type ProcessBytesData struct {
	ProcessID   uint32
	ProcessName string
	DiskNumber  uint32
	Bytes       int64
}

// NewDiskIOCustomCollector creates a new disk I/O metrics custom collector.
func NewDiskIOCustomCollector() *DiskIOCustomCollector {
	return &DiskIOCustomCollector{
		diskIOCount:         make(map[DiskIOKey]*int64),
		diskBytesRead:       make(map[uint32]*int64),
		diskBytesWritten:    make(map[uint32]*int64),
		processIOCount:      make(map[ProcessIOKey]*int64),
		processBytesRead:    make(map[ProcessBytesKey]*int64),
		processBytesWritten: make(map[ProcessBytesKey]*int64),
		log:                 logger.NewSampledLoggerCtx("diskio_collector"),

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
			[]string{"process_id", "process_name", "disk", "operation"}, nil,
		),
		processReadBytesDesc: prometheus.NewDesc(
			"etw_disk_process_read_bytes_total",
			"Total bytes read from disk per process",
			[]string{"process_id", "process_name", "disk"}, nil,
		),
		processWrittenBytesDesc: prometheus.NewDesc(
			"etw_disk_process_written_bytes_total",
			"Total bytes written to disk per process",
			[]string{"process_id", "process_name", "disk"}, nil,
		),
	}
}

// Describe implements prometheus.Collector.
// It sends the descriptors of all the metrics the collector can possibly export
// to the provided channel. This is called once during registration.
//
// Parameters:
//   - ch: Channel to send metric descriptors to
func (c *DiskIOCustomCollector) Describe(ch chan<- *prometheus.Desc) {
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
func (c *DiskIOCustomCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	// Collect current data snapshot
	data := c.collectData()

	// Create disk I/O count metrics
	for _, ioData := range data.DiskIOCount {
		ch <- prometheus.MustNewConstMetric(
			c.diskIOCountDesc,
			prometheus.CounterValue,
			float64(ioData.Count),
			strconv.FormatUint(uint64(ioData.DiskNumber), 10),
			ioData.Operation,
		)
	}

	// Create disk bytes read metrics
	for diskNumber, bytes := range data.DiskBytesRead {
		ch <- prometheus.MustNewConstMetric(
			c.diskReadBytesDesc,
			prometheus.CounterValue,
			float64(bytes),
			strconv.FormatUint(uint64(diskNumber), 10),
		)
	}

	// Create disk bytes written metrics
	for diskNumber, bytes := range data.DiskBytesWritten {
		ch <- prometheus.MustNewConstMetric(
			c.diskWrittenBytesDesc,
			prometheus.CounterValue,
			float64(bytes),
			strconv.FormatUint(uint64(diskNumber), 10),
		)
	}

	// Create process I/O count metrics
	for _, procData := range data.ProcessIOCount {
		ch <- prometheus.MustNewConstMetric(
			c.processIOCountDesc,
			prometheus.CounterValue,
			float64(procData.Count),
			strconv.FormatUint(uint64(procData.ProcessID), 10),
			procData.ProcessName,
			strconv.FormatUint(uint64(procData.DiskNumber), 10),
			procData.Operation,
		)
	}

	// Create process bytes read metrics
	for _, procData := range data.ProcessBytesRead {
		ch <- prometheus.MustNewConstMetric(
			c.processReadBytesDesc,
			prometheus.CounterValue,
			float64(procData.Bytes),
			strconv.FormatUint(uint64(procData.ProcessID), 10),
			procData.ProcessName,
			strconv.FormatUint(uint64(procData.DiskNumber), 10),
		)
	}

	// Create process bytes written metrics
	for _, procData := range data.ProcessBytesWritten {
		ch <- prometheus.MustNewConstMetric(
			c.processWrittenBytesDesc,
			prometheus.CounterValue,
			float64(procData.Bytes),
			strconv.FormatUint(uint64(procData.ProcessID), 10),
			procData.ProcessName,
			strconv.FormatUint(uint64(procData.DiskNumber), 10),
		)
	}
}

// collectData creates a snapshot of current metrics data.
// This method is called during metric collection to ensure consistent data.
//
// Returns:
//   - DiskIOMetricsData: Snapshot of current metric data
func (c *DiskIOCustomCollector) collectData() DiskIOMetricsData {
	data := DiskIOMetricsData{
		DiskIOCount:         make(map[DiskIOKey]DiskIOCountData),
		DiskBytesRead:       make(map[uint32]int64),
		DiskBytesWritten:    make(map[uint32]int64),
		ProcessIOCount:      make(map[ProcessIOKey]ProcessIOCountData),
		ProcessBytesRead:    make(map[ProcessBytesKey]ProcessBytesData),
		ProcessBytesWritten: make(map[ProcessBytesKey]ProcessBytesData),
	}

	// Collect disk I/O counts
	for key, countPtr := range c.diskIOCount {
		if countPtr != nil {
			count := atomic.LoadInt64(countPtr)
			data.DiskIOCount[key] = DiskIOCountData{
				DiskNumber: key.DiskNumber,
				Operation:  key.Operation,
				Count:      count,
			}
		}
	}

	// Collect disk bytes
	for diskNumber, countPtr := range c.diskBytesRead {
		if countPtr != nil {
			data.DiskBytesRead[diskNumber] = atomic.LoadInt64(countPtr)
		}
	}

	for diskNumber, countPtr := range c.diskBytesWritten {
		if countPtr != nil {
			data.DiskBytesWritten[diskNumber] = atomic.LoadInt64(countPtr)
		}
	}

	// Collect process I/O counts
	stateManager := statemanager.GetGlobalStateManager()
	for key, countPtr := range c.processIOCount {
		if countPtr != nil {
			// Only create metrics for processes that are still known at scrape time
			if processName, isKnown := stateManager.GetProcessName(key.ProcessID); isKnown {
				count := atomic.LoadInt64(countPtr)
				data.ProcessIOCount[key] = ProcessIOCountData{
					ProcessID:   key.ProcessID,
					ProcessName: processName,
					DiskNumber:  key.DiskNumber,
					Operation:   key.Operation,
					Count:       count,
				}
			}
		}
	}

	// Collect process bytes
	for key, countPtr := range c.processBytesRead {
		if countPtr != nil {
			// Only create metrics for processes that are still known at scrape time
			if processName, isKnown := stateManager.GetProcessName(key.ProcessID); isKnown {
				bytes := atomic.LoadInt64(countPtr)
				data.ProcessBytesRead[key] = ProcessBytesData{
					ProcessID:   key.ProcessID,
					ProcessName: processName,
					DiskNumber:  key.DiskNumber,
					Bytes:       bytes,
				}
			}
		}
	}

	// Collect process bytes written
	for key, countPtr := range c.processBytesWritten {
		if countPtr != nil {
			// Only create metrics for processes that are still known at scrape time
			if processName, isKnown := stateManager.GetProcessName(key.ProcessID); isKnown {
				bytes := atomic.LoadInt64(countPtr)
				data.ProcessBytesWritten[key] = ProcessBytesData{
					ProcessID:   key.ProcessID,
					ProcessName: processName,
					DiskNumber:  key.DiskNumber,
					Bytes:       bytes,
				}
			}
		}
	}

	return data
}

// RecordDiskIO records a disk I/O operation.
//
// Parameters:
//   - diskNumber: Physical disk number where the I/O occurred
//   - processID: Process ID that initiated the I/O operation
//   - transferSize: Number of bytes transferred in the operation
//   - isWrite: True for write operations, false for read operations
func (c *DiskIOCustomCollector) RecordDiskIO(
	diskNumber uint32,
	processID uint32,
	transferSize uint32,
	isWrite bool) {

	c.mu.Lock()
	defer c.mu.Unlock()

	// Determine operation type
	operation := "read"
	if isWrite {
		operation = "write"
	}

	// Record disk-level I/O count
	diskIOKey := DiskIOKey{DiskNumber: diskNumber, Operation: operation}
	if c.diskIOCount[diskIOKey] == nil {
		c.diskIOCount[diskIOKey] = new(int64)
	}
	atomic.AddInt64(c.diskIOCount[diskIOKey], 1)

	// Record disk-level bytes transferred
	if isWrite {
		if c.diskBytesWritten[diskNumber] == nil {
			c.diskBytesWritten[diskNumber] = new(int64)
		}
		atomic.AddInt64(c.diskBytesWritten[diskNumber], int64(transferSize))
	} else {
		if c.diskBytesRead[diskNumber] == nil {
			c.diskBytesRead[diskNumber] = new(int64)
		}
		atomic.AddInt64(c.diskBytesRead[diskNumber], int64(transferSize))
	}

	// Record process-level metrics (only for known processes with valid names)
	if processID > 0 {
		// Check if process is known by the global state manager
		stateManager := statemanager.GetGlobalStateManager()
		if stateManager.IsKnownProcess(processID) {
			// Record process I/O count
			processIOKey := ProcessIOKey{ProcessID: processID, DiskNumber: diskNumber, Operation: operation}
			if c.processIOCount[processIOKey] == nil {
				c.processIOCount[processIOKey] = new(int64)
			}
			atomic.AddInt64(c.processIOCount[processIOKey], 1)

			// Record process bytes transferred
			processBytesKey := ProcessBytesKey{ProcessID: processID, DiskNumber: diskNumber}
			if isWrite {
				if c.processBytesWritten[processBytesKey] == nil {
					c.processBytesWritten[processBytesKey] = new(int64)
				}
				atomic.AddInt64(c.processBytesWritten[processBytesKey], int64(transferSize))
			} else {
				if c.processBytesRead[processBytesKey] == nil {
					c.processBytesRead[processBytesKey] = new(int64)
				}
				atomic.AddInt64(c.processBytesRead[processBytesKey], int64(transferSize))
			}
		}
	}
}

// RecordDiskFlush records a disk flush operation.
//
// Parameters:
//   - diskNumber: Physical disk number where the flush occurred
func (c *DiskIOCustomCollector) RecordDiskFlush(diskNumber uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Record disk flush operation
	diskIOKey := DiskIOKey{DiskNumber: diskNumber, Operation: "flush"}
	if c.diskIOCount[diskIOKey] == nil {
		c.diskIOCount[diskIOKey] = new(int64)
	}
	atomic.AddInt64(c.diskIOCount[diskIOKey], 1)
}
