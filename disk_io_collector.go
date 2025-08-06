package main

import (
	"strconv"
	"sync"

	"github.com/phuslu/log"
	"github.com/tekert/golang-etw/etw"
)

// DiskIOCollector handles all disk I/O related events and metrics
type DiskIOCollector struct {
	diskActivityMutex sync.RWMutex
	diskActivityMap   map[DiskActivityKey]*ProcessDiskActivity // key: DiskActivityKey

	processTracker *ProcessTracker // Reference to the global process tracker

	fileObjectMutex sync.RWMutex
	fileObjectMap   map[uint64]uint32 // FileObject -> ProcessID mapping

	logger log.Logger // Disk I/O collector logger
}

// DiskInfo stores physical disk information
type DiskInfo struct {
	DiskNumber   uint32
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

// DiskActivityKey is used as a key for diskActivityMap instead of a string.
type DiskActivityKey struct {
	PID     uint32
	DiskNum uint32
}

// NewDiskIOCollector creates a new disk I/O collector instance
func NewDiskIOCollector() *DiskIOCollector {
	return &DiskIOCollector{
		diskActivityMap: make(map[DiskActivityKey]*ProcessDiskActivity),
		processTracker:  GetGlobalProcessTracker(),
		fileObjectMap:   make(map[uint64]uint32),
		logger:          GetDiskIOLogger(),
	}
}

// HandleDiskRead processes disk read completion events
// Disk I/O Event: Microsoft-Windows-Kernel-Disk
// Provider GUID: {c7bde69a-e1e0-4177-b6ef-283ad1525271}
// Event ID: 10 (DiskRead)
//
// Properties from manifest:
//
//	DiskNumber (win:UInt32) - Index number of the disk containing this partition
//	IrpFlags (win:HexInt32) - IRP flags
//	TransferSize (win:UInt32) - Transfer size in bytes
//	Reserved (win:UInt32) - Reserved field
//	ByteOffset (win:UInt64) - Offset into the file where the I/O begins
//	FileObject (win:Pointer) - Pointer to the file object (used for correlation)
//	IORequestPacket (win:Pointer) - Pointer to the I/O request packet
//	HighResResponseTime (win:UInt64) - High-resolution response time
func (d *DiskIOCollector) HandleDiskRead(helper *etw.EventRecordHelper) error {
	diskNumber, err := helper.GetPropertyUint("DiskNumber")
	if err != nil {
		d.logger.Error().Err(err).Msg("Failed to get DiskNumber property for disk read")
		return err
	}

	transferSize, err := helper.GetPropertyUint("TransferSize")
	if err != nil {
		d.logger.Error().Err(err).Msg("Failed to get TransferSize property for disk read")
		return err
	}

	// Get FileObject for process correlation
	fileObject, err := helper.GetPropertyUint("FileObject")
	if err != nil {
		// If we can't get FileObject, fall back to EventHeader ProcessId (may be incorrect)
		processID := helper.EventRec.EventHeader.ProcessId
		d.logger.Trace().
			Uint32("disk_number", uint32(diskNumber)).
			Uint32("transfer_size", uint32(transferSize)).
			Uint32("process_id", processID).
			Msg("Disk read - no FileObject, using EventHeader ProcessId")
		d.updateDiskIOMetrics(uint32(diskNumber), processID, uint32(transferSize), false)
		return nil
	}

	// Look up the real process ID using FileObject correlation
	d.fileObjectMutex.RLock()
	processID, exists := d.fileObjectMap[fileObject]
	d.fileObjectMutex.RUnlock()

	if !exists {
		// If no mapping exists, fall back to EventHeader ProcessId (may be incorrect)
		processID = helper.EventRec.EventHeader.ProcessId
		d.logger.Trace().
			Uint32("disk_number", uint32(diskNumber)).
			Uint32("transfer_size", uint32(transferSize)).
			Uint64("file_object", fileObject).
			Uint32("fallback_process_id", processID).
			Msg("Disk read - no FileObject mapping, using fallback ProcessId")
	} else {
		d.logger.Trace().
			Uint32("disk_number", uint32(diskNumber)).
			Uint32("transfer_size", uint32(transferSize)).
			Uint64("file_object", fileObject).
			Uint32("process_id", processID).
			Msg("Disk read - using mapped ProcessId from FileObject")
	}

	// Update metrics for read operation
	d.updateDiskIOMetrics(uint32(diskNumber), processID, uint32(transferSize), false)

	return nil
}

// HandleDiskWrite processes disk write completion events
// Disk I/O Event: Microsoft-Windows-Kernel-Disk
// Provider GUID: {c7bde69a-e1e0-4177-b6ef-283ad1525271}
// Event ID: 11 (DiskWrite)
//
// Properties from manifest:
//
//	DiskNumber (win:UInt32) - Index number of the disk containing this partition
//	IrpFlags (win:HexInt32) - IRP flags
//	TransferSize (win:UInt32) - Transfer size in bytes
//	Reserved (win:UInt32) - Reserved field
//	ByteOffset (win:UInt64) - Offset into the file where the I/O begins
//	FileObject (win:Pointer) - Pointer to the file object (used for correlation)
//	IORequestPacket (win:Pointer) - Pointer to the I/O request packet
//	HighResResponseTime (win:UInt64) - High-resolution response time
func (d *DiskIOCollector) HandleDiskWrite(helper *etw.EventRecordHelper) error {
	diskNumber, err := helper.GetPropertyUint("DiskNumber")
	if err != nil {
		return err
	}

	transferSize, err := helper.GetPropertyUint("TransferSize")
	if err != nil {
		return err
	}

	// Get FileObject for process correlation
	fileObject, err := helper.GetPropertyUint("FileObject")
	if err != nil {
		// If we can't get FileObject, fall back to EventHeader ProcessId (may be incorrect)
		processID := helper.EventRec.EventHeader.ProcessId
		d.updateDiskIOMetrics(uint32(diskNumber), processID, uint32(transferSize), true)
		return nil
	}

	// Look up the real process ID using FileObject correlation
	d.fileObjectMutex.RLock()
	processID, exists := d.fileObjectMap[fileObject]
	d.fileObjectMutex.RUnlock()

	if !exists {
		// If no mapping exists, fall back to EventHeader ProcessId (may be incorrect)
		processID = helper.EventRec.EventHeader.ProcessId
	}

	// Update metrics for write operation
	d.updateDiskIOMetrics(uint32(diskNumber), processID, uint32(transferSize), true)

	return nil
}

// HandleDiskFlush processes disk flush events
// Disk I/O Event: Microsoft-Windows-Kernel-Disk
// Provider GUID: {c7bde69a-e1e0-4177-b6ef-283ad1525271}
// Event ID: 14 (DiskFlush)
//
// Properties from manifest:
//
//	DiskNumber (win:UInt32) - Index number of the disk containing this partition
//	IrpFlags (win:HexInt32) - IRP flags
//	HighResResponseTime (win:UInt64) - High-resolution response time
//	IORequestPacket (win:Pointer) - Pointer to the I/O request packet
func (d *DiskIOCollector) HandleDiskFlush(helper *etw.EventRecordHelper) error {
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

// HandleFileCreate processes file create events for FileObject correlation
// File I/O Event: Microsoft-Windows-Kernel-File
// Provider GUID: {edd08927-9cc4-4e65-b970-c2560fb5c289}
// Event ID: 12 (Create)
//
// Properties from manifest:
//
//	Irp (win:Pointer) - Pointer to the I/O request packet
//	FileObject (win:Pointer) - Pointer to the file object (used for correlation)
//	IssuingThreadId (win:UInt32) - Thread ID that issued the I/O (V1 template)
//	CreateOptions (win:UInt32) - Create options
//	CreateAttributes (win:UInt32) - Create attributes
//	ShareAccess (win:UInt32) - Share access
//	FileName (win:UnicodeString) - File name
func (d *DiskIOCollector) HandleFileCreate(helper *etw.EventRecordHelper) error {
	fileObject, err := helper.GetPropertyUint("FileObject")
	if err != nil {
		return nil // Can't correlate without FileObject
	}

	// The process ID from the event header is correct for File I/O events
	processID := helper.EventRec.EventHeader.ProcessId

	// Store the FileObject -> ProcessID mapping
	d.fileObjectMutex.Lock()
	d.fileObjectMap[fileObject] = processID
	d.fileObjectMutex.Unlock()

	return nil
}

// HandleFileClose processes file close events to clean up FileObject mappings
// File I/O Event: Microsoft-Windows-Kernel-File
// Provider GUID: {edd08927-9cc4-4e65-b970-c2560fb5c289}
// Event ID: 14 (Close)
//
// Properties from manifest:
//
//	Irp (win:Pointer) - Pointer to the I/O request packet
//	FileObject (win:Pointer) - Pointer to the file object (used for correlation)
//	FileKey (win:Pointer) - File key
//	IssuingThreadId (win:UInt32) - Thread ID that issued the I/O (V1 template)
func (d *DiskIOCollector) HandleFileClose(helper *etw.EventRecordHelper) error {
	fileObject, err := helper.GetPropertyUint("FileObject")
	if err != nil {
		return nil // Can't correlate without FileObject
	}

	// Remove the FileObject mapping to prevent stale entries
	d.fileObjectMutex.Lock()
	delete(d.fileObjectMap, fileObject)
	d.fileObjectMutex.Unlock()

	return nil
}

// HandleFileWrite processes file write events for FileObject correlation
// File I/O Event: Microsoft-Windows-Kernel-File
// Provider GUID: {edd08927-9cc4-4e65-b970-c2560fb5c289}
// Event ID: 16 (Write)
//
// Properties from manifest:
//
//	ByteOffset (win:UInt64) - Offset into the file where the I/O begins
//	Irp (win:Pointer) - Pointer to the I/O request packet
//	FileObject (win:Pointer) - Pointer to the file object (used for correlation)
//	FileKey (win:Pointer) - File key
//	IssuingThreadId (win:UInt32) - Thread ID that issued the I/O (V1 template)
//	IOSize (win:UInt32) - I/O size in bytes
//	IOFlags (win:UInt32) - I/O flags
//	ExtraFlags (win:UInt32) - Extra flags (V1 template)
func (d *DiskIOCollector) HandleFileWrite(helper *etw.EventRecordHelper) error {
	fileObject, err := helper.GetPropertyUint("FileObject")
	if err != nil {
		return nil // Can't correlate without FileObject
	}

	// The process ID from the event header is correct for File I/O events
	processID := helper.EventRec.EventHeader.ProcessId

	// Store/update the FileObject -> ProcessID mapping
	d.fileObjectMutex.Lock()
	d.fileObjectMap[fileObject] = processID
	d.fileObjectMutex.Unlock()

	return nil
}

// HandleFileRead processes file read events for FileObject correlation
// File I/O Event: Microsoft-Windows-Kernel-File
// Provider GUID: {edd08927-9cc4-4e65-b970-c2560fb5c289}
// Event ID: 15 (Read)
//
// Properties from manifest:
//
//	ByteOffset (win:UInt64) - Offset into the file where the I/O begins
//	Irp (win:Pointer) - Pointer to the I/O request packet
//	FileObject (win:Pointer) - Pointer to the file object (used for correlation)
//	FileKey (win:Pointer) - File key
//	IssuingThreadId (win:UInt32) - Thread ID that issued the I/O (V1 template)
//	IOSize (win:UInt32) - I/O size in bytes
//	IOFlags (win:UInt32) - I/O flags
//	ExtraFlags (win:UInt32) - Extra flags (V1 template)
func (d *DiskIOCollector) HandleFileRead(helper *etw.EventRecordHelper) error {
	fileObject, err := helper.GetPropertyUint("FileObject")
	if err != nil {
		return nil // Can't correlate without FileObject
	}

	// The process ID from the event header is correct for File I/O events
	processID := helper.EventRec.EventHeader.ProcessId

	// Store/update the FileObject -> ProcessID mapping
	d.fileObjectMutex.Lock()
	d.fileObjectMap[fileObject] = processID
	d.fileObjectMutex.Unlock()

	return nil
}

// HandleFileDelete processes file delete events for FileObject correlation
// File I/O Event: Microsoft-Windows-Kernel-File
// Provider GUID: {edd08927-9cc4-4e65-b970-c2560fb5c289}
// Event ID: 26 (DeletePath)
//
// Properties from manifest:
//
//	Irp (win:Pointer) - Pointer to the I/O request packet
//	FileObject (win:Pointer) - Pointer to the file object (used for correlation)
//	FileKey (win:Pointer) - File key
//	ExtraInformation (win:Pointer) - Extra information
//	IssuingThreadId (win:UInt32) - Thread ID that issued the I/O (V1 template)
//	InfoClass (win:UInt32) - Information class
//	FilePath (win:UnicodeString) - File path
func (d *DiskIOCollector) HandleFileDelete(helper *etw.EventRecordHelper) error {
	fileObject, err := helper.GetPropertyUint("FileObject")
	if err != nil {
		return nil // Can't correlate without FileObject
	}

	// Remove the FileObject mapping when file is deleted
	d.fileObjectMutex.Lock()
	delete(d.fileObjectMap, fileObject)
	d.fileObjectMutex.Unlock()

	return nil
}

// HandleSystemConfigPhyDisk processes SystemConfig PhyDisk events for disk correlation
// MOF Event: SystemConfig_PhyDisk
// Provider: {01853a65-418f-4f36-aefc-dc0f1d2fd235} (EventTraceConfig) - classic header format
// EventType: EVENT_TRACE_TYPE_CONFIG_PHYSICALDISK (0x0B = 11)
//
// MOF Class Definition from Microsoft documentation:
// [EventType(11), EventTypeName("PhyDisk")]
// class SystemConfig_PhyDisk : SystemConfig
//
//	{
//	  uint32 DiskNumber;           // Index number of the disk containing this partition
//	  uint32 BytesPerSector;       // Number of bytes in each sector for the physical disk drive
//	  uint32 SectorsPerTrack;      // Number of sectors in each track for this physical disk drive
//	  uint32 TracksPerCylinder;    // Number of tracks in each cylinder on the physical disk drive
//	  uint64 Cylinders;            // Total number of cylinders on the physical disk drive
//	  uint32 SCSIPort;             // SCSI number of the SCSI adapter
//	  uint32 SCSIPath;             // SCSI bus number of the SCSI adapter
//	  uint32 SCSITarget;           // Contains the number of the target device
//	  uint32 SCSILun;              // SCSI logical unit number (LUN) of the SCSI adapter
//	  char16 Manufacturer[];       // Name of the disk drive manufacturer (Max 256)
//	  uint32 PartitionCount;       // Number of partitions on this physical disk drive
//	  uint8  WriteCacheEnabled;    // True if the write cache is enabled
//	  uint8  Pad;                  // Not used
//	  char16 BootDriveLetter[];    // Drive letter of the boot drive in the form "<letter>:" (Max 3)
//	  char16 Spare[];              // Not used (Max 2)
//	};
//
// NOTE: Fields like DeviceName, Model, Size are NOT available in this MOF class.
// Only the fields listed above are present in SystemConfig_PhyDisk events.
func (d *DiskIOCollector) HandleSystemConfigPhyDisk(helper *etw.EventRecordHelper) error {
	// Get required DiskNumber field
	diskNumber, err := helper.GetPropertyUint("DiskNumber")
	if err != nil {
		return err
	}

	// Get Manufacturer (available in MOF)
	manufacturer, err := helper.GetPropertyString("Manufacturer")
	if err != nil {
		manufacturer = "Unknown" // Optional field, use default if not available
	}

	// Update PhyDisk info metrics - only use labels that exist in the event
	metrics := GetMetrics()
	metrics.PhyDiskInfo.WithLabelValues(
		strconv.FormatUint(diskNumber, 10),
		manufacturer,
	).Set(1)

	return nil
}

// HandleSystemConfigLogDisk processes SystemConfig LogDisk events for partition/volume information
// MOF Event: SystemConfig_LogDisk
// Provider: {01853a65-418f-4f36-aefc-dc0f1d2fd235} (EventTraceConfig) - classic header format
// EventType: EVENT_TRACE_TYPE_CONFIG_LOGICALDISK (0x0C = 12)
//
// MOF Class Definition from Microsoft documentation:
// [EventType(12), EventTypeName("LogDisk")]
// class SystemConfig_LogDisk : SystemConfig
//
//	{
//	  uint64 StartOffset;          // Starting offset (in bytes) of the partition from the beginning of the disk
//	  uint64 PartitionSize;        // Total size of the partition, in bytes
//	  uint32 DiskNumber;           // Index number of the disk containing this partition
//	  uint32 Size;                 // Size of the disk drive, in bytes
//	  uint32 DriveType;            // Type of disk drive (1=Partition, 2=Volume, 3=Extended partition)
//	  char16 DriveLetterString[];  // Drive letter of the disk in the form, "<letter>:" (Max 4)
//	  uint32 Pad1;                 // Not used
//	  uint32 PartitionNumber;      // Index number of the partition
//	  uint32 SectorsPerCluster;    // Number of sectors in the volume
//	  uint32 BytesPerSector;       // Number of bytes in each sector for the physical disk drive
//	  uint32 Pad2;                 // Not used
//	  sint64 NumberOfFreeClusters; // Number of free clusters in the specified volume
//	  sint64 TotalNumberOfClusters; // Number of used and free clusters in the volume
//	  char16 FileSystem;           // File system on the logical disk, for example, NTFS (Max 16)
//	  uint32 VolumeExt;            // Reserved
//	  uint32 Pad3;                 // Not used
//	};
func (d *DiskIOCollector) HandleSystemConfigLogDisk(helper *etw.EventRecordHelper) error {
	// Get required DiskNumber field
	diskNumber, err := helper.GetPropertyUint("DiskNumber")
	if err != nil {
		return err
	}

	// Get drive letter
	driveLetterString, err := helper.GetPropertyString("DriveLetterString")
	if err != nil {
		driveLetterString = "" // Optional field
	}

	// Get file system info (not currently used but available for future enhancement)
	fileSystem, err := helper.GetPropertyString("FileSystem")
	if err != nil {
		fileSystem = "Unknown" // FileSystem field is optional
	}

	// Update LogDisk info metrics - only use labels that exist in the event
	metrics := GetMetrics()
	metrics.LogDiskInfo.WithLabelValues(
		strconv.FormatUint(diskNumber, 10),
		driveLetterString,
		fileSystem,
	).Set(1)

	return nil
}

// Helper methods for internal state management

// getProcessName retrieves the process name for a given PID
func (d *DiskIOCollector) getProcessName(pid uint32) string {
	return d.processTracker.GetProcessName(pid)
}

// getDiskActivityKey creates a unique key for disk activity tracking
func (d *DiskIOCollector) getDiskActivityKey(pid uint32, diskNum uint32) DiskActivityKey {
	return DiskActivityKey{PID: pid, DiskNum: diskNum}
}

// updateDiskIOMetrics updates the Prometheus metrics for disk I/O operations
func (d *DiskIOCollector) updateDiskIOMetrics(diskNumber, processID, transferSize uint32, isWrite bool) {
	// Get process name for labels
	processName := d.getProcessName(processID)

	// Create activity key
	activityKey := d.getDiskActivityKey(processID, diskNumber)

	// Update activity tracking
	d.diskActivityMutex.Lock()
	activity, exists := d.diskActivityMap[activityKey]
	if !exists {
		activity = &ProcessDiskActivity{
			ProcessID:   processID,
			ProcessName: processName,
			DiskNumber:  diskNumber,
		}
		d.diskActivityMap[activityKey] = activity
	}

	activity.IOCount++
	if isWrite {
		activity.BytesWritten += uint64(transferSize)
	} else {
		activity.BytesRead += uint64(transferSize)
	}
	d.diskActivityMutex.Unlock()

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
