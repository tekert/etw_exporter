package main

import (
	"maps"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/phuslu/log"
	"github.com/prometheus/client_golang/prometheus"
)

// DiskIOCustomCollector implements prometheus.Collector for disk I/O related metrics.
// This collector follows Prometheus best practices by creating new metrics on each scrape
//
// It provides high-performance aggregated metrics for:
// - Disk I/O operations (read/write/flush) per disk and per process
// - Bytes transferred (read/written) per disk and per process
// - Physical disk information (manufacturer, configuration)
// - Logical disk information (drive letters, file systems)
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

	// Physical and logical disk information
	physicalDisks map[uint32]PhysicalDiskInfo // diskNumber -> disk info
	logicalDisks  map[uint32]LogicalDiskInfo  // diskNumber -> logical disk info

	// Synchronization
	mu  sync.RWMutex
	log log.Logger
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
	PhysicalDisks       map[uint32]PhysicalDiskInfo          // diskNumber -> physical disk info
	LogicalDisks        map[uint32]LogicalDiskInfo           // diskNumber -> logical disk info
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

// PhysicalDiskInfo holds physical disk information
type PhysicalDiskInfo struct {
	DiskNumber   uint32
	Manufacturer string
}

// LogicalDiskInfo holds logical disk information
type LogicalDiskInfo struct {
	DiskNumber        uint32
	DriveLetterString string
	FileSystem        string
}

// NewDiskIOCustomCollector creates a new disk I/O metrics custom collector.
// This collector aggregates disk I/O related ETW events and exposes them as Prometheus metrics
//
// Returns:
//   - *DiskIOCustomCollector: A new custom collector instance
func NewDiskIOCustomCollector() *DiskIOCustomCollector {
	return &DiskIOCustomCollector{
		diskIOCount:         make(map[DiskIOKey]*int64),
		diskBytesRead:       make(map[uint32]*int64),
		diskBytesWritten:    make(map[uint32]*int64),
		processIOCount:      make(map[ProcessIOKey]*int64),
		processBytesRead:    make(map[ProcessBytesKey]*int64),
		processBytesWritten: make(map[ProcessBytesKey]*int64),
		physicalDisks:       make(map[uint32]PhysicalDiskInfo),
		logicalDisks:        make(map[uint32]LogicalDiskInfo),
		log:                 GetDiskIOLogger(),
	}
}

// Describe implements prometheus.Collector.
// It sends the descriptors of all the metrics the collector can possibly export
// to the provided channel. This is called once during registration.
//
// Parameters:
//   - ch: Channel to send metric descriptors to
func (c *DiskIOCustomCollector) Describe(ch chan<- *prometheus.Desc) {
	// Disk I/O count metrics
	ch <- prometheus.NewDesc(
		"etw_disk_io_operations_total",
		"Total number of disk I/O operations per disk and operation type",
		[]string{"disk", "operation"}, nil,
	)

	// Disk bytes read metrics
	ch <- prometheus.NewDesc(
		"etw_disk_bytes_read_total",
		"Total bytes read from disk",
		[]string{"disk"}, nil,
	)

	// Disk bytes written metrics
	ch <- prometheus.NewDesc(
		"etw_disk_bytes_written_total",
		"Total bytes written to disk",
		[]string{"disk"}, nil,
	)

	// Process I/O count metrics
	ch <- prometheus.NewDesc(
		"etw_process_disk_io_operations_total",
		"Total number of disk I/O operations per process, disk, and operation type",
		[]string{"process_id", "process_name", "disk", "operation"}, nil,
	)

	// Process bytes read metrics
	ch <- prometheus.NewDesc(
		"etw_process_disk_bytes_read_total",
		"Total bytes read from disk per process",
		[]string{"process_id", "process_name", "disk"}, nil,
	)

	// Process bytes written metrics
	ch <- prometheus.NewDesc(
		"etw_process_disk_bytes_written_total",
		"Total bytes written to disk per process",
		[]string{"process_id", "process_name", "disk"}, nil,
	)

	// Physical disk information metrics
	ch <- prometheus.NewDesc(
		"etw_physical_disk_info",
		"Physical disk information",
		[]string{"disk", "manufacturer"}, nil,
	)

	// Logical disk information metrics
	ch <- prometheus.NewDesc(
		"etw_logical_disk_info",
		"Logical disk information",
		[]string{"disk", "drive_letter", "file_system"}, nil,
	)
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
			prometheus.NewDesc(
				"etw_disk_io_operations_total",
				"Total number of disk I/O operations per disk and operation type",
				[]string{"disk", "operation"}, nil,
			),
			prometheus.CounterValue,
			float64(ioData.Count),
			strconv.FormatUint(uint64(ioData.DiskNumber), 10),
			ioData.Operation,
		)
	}

	// Create disk bytes read metrics
	for diskNumber, bytes := range data.DiskBytesRead {
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(
				"etw_disk_bytes_read_total",
				"Total bytes read from disk",
				[]string{"disk"}, nil,
			),
			prometheus.CounterValue,
			float64(bytes),
			strconv.FormatUint(uint64(diskNumber), 10),
		)
	}

	// Create disk bytes written metrics
	for diskNumber, bytes := range data.DiskBytesWritten {
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(
				"etw_disk_bytes_written_total",
				"Total bytes written to disk",
				[]string{"disk"}, nil,
			),
			prometheus.CounterValue,
			float64(bytes),
			strconv.FormatUint(uint64(diskNumber), 10),
		)
	}

	// Create process I/O count metrics
	for _, procData := range data.ProcessIOCount {
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(
				"etw_process_disk_io_operations_total",
				"Total number of disk I/O operations per process, disk, and operation type",
				[]string{"process_id", "process_name", "disk", "operation"}, nil,
			),
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
			prometheus.NewDesc(
				"etw_process_disk_bytes_read_total",
				"Total bytes read from disk per process",
				[]string{"process_id", "process_name", "disk"}, nil,
			),
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
			prometheus.NewDesc(
				"etw_process_disk_bytes_written_total",
				"Total bytes written to disk per process",
				[]string{"process_id", "process_name", "disk"}, nil,
			),
			prometheus.CounterValue,
			float64(procData.Bytes),
			strconv.FormatUint(uint64(procData.ProcessID), 10),
			procData.ProcessName,
			strconv.FormatUint(uint64(procData.DiskNumber), 10),
		)
	}

	// Create physical disk information metrics
	for _, physDisk := range data.PhysicalDisks {
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(
				"etw_physical_disk_info",
				"Physical disk information",
				[]string{"disk", "manufacturer"}, nil,
			),
			prometheus.GaugeValue,
			1,
			strconv.FormatUint(uint64(physDisk.DiskNumber), 10),
			physDisk.Manufacturer,
		)
	}

	// Create logical disk information metrics
	for _, logDisk := range data.LogicalDisks {
		ch <- prometheus.MustNewConstMetric(
			prometheus.NewDesc(
				"etw_logical_disk_info",
				"Logical disk information",
				[]string{"disk", "drive_letter", "file_system"}, nil,
			),
			prometheus.GaugeValue,
			1,
			strconv.FormatUint(uint64(logDisk.DiskNumber), 10),
			logDisk.DriveLetterString,
			logDisk.FileSystem,
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
		PhysicalDisks:       make(map[uint32]PhysicalDiskInfo),
		LogicalDisks:        make(map[uint32]LogicalDiskInfo),
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
	processCollector := GetGlobalProcessCollector()
	for key, countPtr := range c.processIOCount {
		if countPtr != nil {
			// Only create metrics for processes that are still known at scrape time
			if processName, isKnown := processCollector.GetProcessName(key.ProcessID); isKnown {
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
			if processName, isKnown := processCollector.GetProcessName(key.ProcessID); isKnown {
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
			if processName, isKnown := processCollector.GetProcessName(key.ProcessID); isKnown {
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

	// Copy disk information
	maps.Copy(data.PhysicalDisks, c.physicalDisks)

	maps.Copy(data.LogicalDisks, c.logicalDisks)

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
		// Check if process is known by the global process collector
		processCollector := GetGlobalProcessCollector()
		if _, isKnown := processCollector.GetProcessName(processID); isKnown {
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

// RecordPhysicalDiskInfo records physical disk information.
//
// Parameters:
//   - diskNumber: Physical disk number
//   - manufacturer: Disk manufacturer name
func (c *DiskIOCustomCollector) RecordPhysicalDiskInfo(diskNumber uint32, manufacturer string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Store physical disk information
	c.physicalDisks[diskNumber] = PhysicalDiskInfo{
		DiskNumber:   diskNumber,
		Manufacturer: manufacturer,
	}
}

// RecordLogicalDiskInfo records logical disk information.
//
// Parameters:
//   - diskNumber: Physical disk number containing the logical disk
//   - driveLetterString: Drive letter (e.g., "C:")
//   - fileSystem: File system type (e.g., "NTFS", "FAT32")
func (c *DiskIOCustomCollector) RecordLogicalDiskInfo(diskNumber uint32, driveLetterString, fileSystem string) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Store logical disk information
	c.logicalDisks[diskNumber] = LogicalDiskInfo{
		DiskNumber:        diskNumber,
		DriveLetterString: driveLetterString,
		FileSystem:        fileSystem,
	}
}
