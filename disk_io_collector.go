package main

import (
	"strconv"
	"sync"

	"github.com/tekert/golang-etw/etw"
)

// DiskIOCollector handles all disk I/O related events and metrics
type DiskIOCollector struct {
	diskInfoMutex sync.RWMutex
	diskInfoMap   map[uint32]DiskInfo

	diskActivityMutex sync.RWMutex
	diskActivityMap   map[string]*ProcessDiskActivity // key: "pid:disknum"

	processNameMutex sync.RWMutex
	processNameMap   map[uint32]string // ProcessID -> ProcessName mapping
}

// DiskInfo stores physical disk information
type DiskInfo struct {
	DiskNumber   uint32
	DeviceName   string
	Manufacturer string
	Model        string
	Size         uint64
}

// ProcessDiskActivity tracks disk activity by process
type ProcessDiskActivity struct {
	ProcessID    uint32
	ProcessName  string
	DiskNumber   uint32
	BytesRead    uint64
	BytesWritten uint64
	IOCount      uint64
}

// NewDiskIOCollector creates a new disk I/O collector instance
func NewDiskIOCollector() *DiskIOCollector {
	return &DiskIOCollector{
		diskInfoMap:     make(map[uint32]DiskInfo),
		diskActivityMap: make(map[string]*ProcessDiskActivity),
		processNameMap:  make(map[uint32]string),
	}
}

// HandleDiskRead processes disk read completion events
// MOF Event: DiskIo_TypeGroup1 (EventType 10=Read completion)
// Fields: DiskNumber, IrpFlags, TransferSize, ByteOffset, FileObject, Irp, HighResResponseTime
func (c *DiskIOCollector) HandleDiskRead(helper *etw.EventRecordHelper) error {
	diskNumber, err := helper.GetPropertyUint("DiskNumber")
	if err != nil {
		return err // Just return the original error
	}

	transferSize, err := helper.GetPropertyUint("TransferSize")
	if err != nil {
		return err // Just return the original error
	}

	processID := helper.EventRec.EventHeader.ProcessId

	// Update metrics for read operation
	c.updateDiskIOMetrics(uint32(diskNumber), processID, uint32(transferSize), false)

	return nil
}

// HandleDiskWrite processes disk write completion events
// MOF Event: DiskIo_TypeGroup1 (EventType 11=Write completion)
// Fields: DiskNumber, IrpFlags, TransferSize, ByteOffset, FileObject, Irp, HighResResponseTime
func (c *DiskIOCollector) HandleDiskWrite(helper *etw.EventRecordHelper) error {
	diskNumber, err := helper.GetPropertyUint("DiskNumber")
	if err != nil {
		return err
	}

	transferSize, err := helper.GetPropertyUint("TransferSize")
	if err != nil {
		return err
	}

	processID := helper.EventRec.EventHeader.ProcessId

	// Update metrics for write operation
	c.updateDiskIOMetrics(uint32(diskNumber), processID, uint32(transferSize), true)

	return nil
}

// HandleDiskFlush processes disk flush events
func (c *DiskIOCollector) HandleDiskFlush(helper *etw.EventRecordHelper) error {
	diskNumber, err := helper.GetPropertyUint("DiskNumber")
	if err != nil {
		return err
	}

	// Update flush metrics - use strconv for better performance
	metrics := GetMetrics()
	diskLabel := strconv.FormatUint(diskNumber, 10)
	metrics.DiskIOCount.WithLabelValues(diskLabel, "flush").Inc()

	return nil
}

// HandleSystemConfigDisk processes SystemConfig PhyDisk events for disk correlation
// MOF Event: SystemConfig_PhyDisk provides disk number to drive letter mapping
func (c *DiskIOCollector) HandleSystemConfigDisk(helper *etw.EventRecordHelper) error {
	diskNumber, err := helper.GetPropertyUint("DiskNumber")
	if err != nil {
		return err
	}

	deviceName, err := helper.GetPropertyString("DeviceName")
	if err != nil {
		return err
	}

	manufacturer, err := helper.GetPropertyString("Manufacturer")
	if err != nil {
		manufacturer = "Unknown"
	}

	model, err := helper.GetPropertyString("Model")
	if err != nil {
		model = "Unknown"
	}

	size, err := helper.GetPropertyUint("Size")
	if err != nil {
		size = 0
	}

	// Store disk information
	c.diskInfoMutex.Lock()
	c.diskInfoMap[uint32(diskNumber)] = DiskInfo{
		DiskNumber:   uint32(diskNumber),
		DeviceName:   deviceName,
		Manufacturer: manufacturer,
		Model:        model,
		Size:         size,
	}
	c.diskInfoMutex.Unlock()

	// Update disk info metrics - use strconv for performance
	metrics := GetMetrics()
	metrics.DiskInfo.WithLabelValues(
		strconv.FormatUint(diskNumber, 10),
		deviceName,
		manufacturer,
		model,
	).Set(1)

	return nil
}

// HandleProcessStart processes process start events for name tracking
// MOF Event: Process_TypeGroup1 (EventType 1=Start, 3=DCStart)
// Fields: ImageName, ProcessID, ParentID, SessionID, CommandLine
func (c *DiskIOCollector) HandleProcessStart(helper *etw.EventRecordHelper) error {
	processID := helper.EventRec.EventHeader.ProcessId

	imageName, err := helper.GetPropertyString("ImageName")
	if err != nil {
		// If we can't get ImageName, use ProcessID as string - more efficient than fmt.Sprintf
		imageName = "PID_" + strconv.FormatUint(uint64(processID), 10)
	}

	// Store process name mapping
	c.processNameMutex.Lock()
	c.processNameMap[processID] = imageName
	c.processNameMutex.Unlock()

	return nil
}

// HandleProcessEnd processes process end events
// MOF Event: Process_TypeGroup1 (EventType 2=End, 4=DCEnd)
func (c *DiskIOCollector) HandleProcessEnd(helper *etw.EventRecordHelper) error {
	processID := helper.EventRec.EventHeader.ProcessId

	// Remove process from mapping
	c.processNameMutex.Lock()
	delete(c.processNameMap, processID)
	c.processNameMutex.Unlock()

	return nil
}

// HandleManifestDiskEvent processes manifest provider disk events
// MOF Event: Microsoft-Windows-Kernel-Disk events
func (c *DiskIOCollector) HandleManifestDiskEvent(helper *etw.EventRecordHelper) error {
	eventID := helper.EventID()

	switch eventID {
	case 10: // Read completion
		return c.HandleDiskRead(helper)
	case 11: // Write completion
		return c.HandleDiskWrite(helper)
	case 14: // Flush completion
		return c.HandleDiskFlush(helper)
	}

	return nil
}

// updateDiskIOMetrics updates the Prometheus metrics for disk I/O operations
func (c *DiskIOCollector) updateDiskIOMetrics(diskNumber, processID, transferSize uint32, isWrite bool) {
	// Get process name for labels
	c.processNameMutex.RLock()
	processName, exists := c.processNameMap[processID]
	c.processNameMutex.RUnlock()

	if !exists {
		// Use more efficient string concatenation instead of fmt.Sprintf
		processName = "PID_" + strconv.FormatUint(uint64(processID), 10)
	}

	// Create activity key - avoid fmt.Sprintf for better performance
	activityKey := strconv.FormatUint(uint64(processID), 10) + ":" + strconv.FormatUint(uint64(diskNumber), 10)

	// Update activity tracking
	c.diskActivityMutex.Lock()
	activity, exists := c.diskActivityMap[activityKey]
	if !exists {
		activity = &ProcessDiskActivity{
			ProcessID:   processID,
			ProcessName: processName,
			DiskNumber:  diskNumber,
		}
		c.diskActivityMap[activityKey] = activity
	}

	activity.IOCount++
	if isWrite {
		activity.BytesWritten += uint64(transferSize)
	} else {
		activity.BytesRead += uint64(transferSize)
	}
	c.diskActivityMutex.Unlock()

	// Update Prometheus metrics - use strconv for performance
	metrics := GetMetrics()
	diskLabel := strconv.FormatUint(uint64(diskNumber), 10)
	operation := "read"
	if isWrite {
		operation = "write"
	}

	// Update disk-level metrics
	metrics.DiskIOCount.WithLabelValues(diskLabel, operation).Inc()
	if isWrite {
		metrics.DiskBytesWritten.WithLabelValues(diskLabel).Add(float64(transferSize))
	} else {
		metrics.DiskBytesRead.WithLabelValues(diskLabel).Add(float64(transferSize))
	}

	// Update process-level metrics
	metrics.ProcessIOCount.WithLabelValues(processName, diskLabel, operation).Inc()
	if isWrite {
		metrics.ProcessBytesWritten.WithLabelValues(processName, diskLabel).Add(float64(transferSize))
	} else {
		metrics.ProcessBytesRead.WithLabelValues(processName, diskLabel).Add(float64(transferSize))
	}
}
