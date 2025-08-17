package main

import (
	"sync"

	"github.com/phuslu/log"
	"github.com/tekert/goetw/etw"
)

// DiskIOHandler handles disk I/O events from ETW providers.
// This handler processes ETW events from multiple providers related to disk and file operations:
//
// Disk I/O Provider:
// - GUID: {c7bde69a-e1e0-4177-b6ef-283ad1525271}
// - Name: Microsoft-Windows-Kernel-Disk
// - Description: Kernel disk I/O operations
//
// File I/O Provider:
// - GUID: {edd08927-9cc4-4e65-b970-c2560fb5c289}
// - Name: Microsoft-Windows-Kernel-File
// - Description: Kernel file operations for FileObject correlation
//
// SystemConfig Provider:
// - GUID: {01853a65-418f-4f36-aefc-dc0f1d2fd235}
// - Name: EventTraceConfig
// - Description: System configuration events including disk information
//
// The handler maintains FileObject-to-ProcessID mappings for accurate attribution
// of disk operations to the correct processes.
type DiskIOHandler struct {
	customCollector *DiskIOCustomCollector // High-performance custom collector

	fileObjectMutex sync.RWMutex
	fileObjectMap   map[uint64]uint32 // FileObject -> ProcessID mapping

	log log.Logger // Disk I/O handler logger
}

// NewDiskIOHandler creates a new disk I/O handler instance with custom collector integration.
// The custom collector provides high-performance metrics aggregation for disk I/O operations.
//
// Returns:
//   - *DiskIOHandler: A new disk I/O handler instance
func NewDiskIOHandler() *DiskIOHandler {
	return &DiskIOHandler{
		customCollector: NewDiskIOCustomCollector(),
		fileObjectMap:   make(map[uint64]uint32),
		log:             GetDiskIOLogger(),
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
//
// Parameters:
//   - helper: ETW event record helper containing the event data
//
// Returns:
//   - error: Error if event processing fails, nil otherwise
func (d *DiskIOHandler) HandleDiskRead(helper *etw.EventRecordHelper) error {
	diskNumber, err := helper.GetPropertyUint("DiskNumber")
	if err != nil {
		d.log.Error().Err(err).Msg("Failed to get DiskNumber property for disk read")
		return err
	}

	transferSize, err := helper.GetPropertyUint("TransferSize")
	if err != nil {
		d.log.Error().Err(err).Msg("Failed to get TransferSize property for disk read")
		return err
	}

	// Get FileObject for process correlation
	fileObject, err := helper.GetPropertyUint("FileObject")
	if err != nil {
		// If we can't get FileObject, fall back to EventHeader ProcessId (may be incorrect)
		processID := helper.EventRec.EventHeader.ProcessId
		d.log.Trace().
			Uint32("disk_number", uint32(diskNumber)).
			Uint32("transfer_size", uint32(transferSize)).
			Uint32("process_id", processID).
			Msg("Disk read - no FileObject, using EventHeader ProcessId")
		d.customCollector.RecordDiskIO(uint32(diskNumber), processID, uint32(transferSize), false)
		return nil
	}

	// Look up the real process ID using FileObject correlation
	d.fileObjectMutex.RLock()
	processID, exists := d.fileObjectMap[fileObject]
	d.fileObjectMutex.RUnlock()

	if !exists {
		// If no mapping exists, fall back to EventHeader ProcessId (may be incorrect)
		processID = helper.EventRec.EventHeader.ProcessId
		d.log.Trace().
			Uint32("disk_number", uint32(diskNumber)).
			Uint32("transfer_size", uint32(transferSize)).
			Uint64("file_object", fileObject).
			Uint32("fallback_process_id", processID).
			Msg("Disk read - no FileObject mapping, using fallback ProcessId")
	} else {
		d.log.Trace().
			Uint32("disk_number", uint32(diskNumber)).
			Uint32("transfer_size", uint32(transferSize)).
			Uint64("file_object", fileObject).
			Uint32("process_id", processID).
			Msg("Disk read - using mapped ProcessId from FileObject")
	}

	// Record read operation in custom collector
	d.customCollector.RecordDiskIO(uint32(diskNumber), processID, uint32(transferSize), false)

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
//
// Parameters:
//   - helper: ETW event record helper containing the event data
//
// Returns:
//   - error: Error if event processing fails, nil otherwise
func (d *DiskIOHandler) HandleDiskWrite(helper *etw.EventRecordHelper) error {
	diskNumber, err := helper.GetPropertyUint("DiskNumber")
	if err != nil {
		d.log.Error().Err(err).Msg("Failed to get DiskNumber property for disk write")
		return err
	}

	transferSize, err := helper.GetPropertyUint("TransferSize")
	if err != nil {
		d.log.Error().Err(err).Msg("Failed to get TransferSize property for disk write")
		return err
	}

	// Get FileObject for process correlation
	fileObject, err := helper.GetPropertyUint("FileObject")
	if err != nil {
		// If we can't get FileObject, fall back to EventHeader ProcessId (may be incorrect)
		processID := helper.EventRec.EventHeader.ProcessId
		d.log.Trace().
			Uint32("disk_number", uint32(diskNumber)).
			Uint32("transfer_size", uint32(transferSize)).
			Uint32("process_id", processID).
			Msg("Disk write - no FileObject, using EventHeader ProcessId")
		d.customCollector.RecordDiskIO(uint32(diskNumber), processID, uint32(transferSize), true)
		return nil
	}

	// Look up the real process ID using FileObject correlation
	d.fileObjectMutex.RLock()
	processID, exists := d.fileObjectMap[fileObject]
	d.fileObjectMutex.RUnlock()

	if !exists {
		// If no mapping exists, fall back to EventHeader ProcessId (may be incorrect)
		processID = helper.EventRec.EventHeader.ProcessId
		d.log.Trace().
			Uint32("disk_number", uint32(diskNumber)).
			Uint32("transfer_size", uint32(transferSize)).
			Uint64("file_object", fileObject).
			Uint32("fallback_process_id", processID).
			Msg("Disk write - no FileObject mapping, using fallback ProcessId")
	} else {
		d.log.Trace().
			Uint32("disk_number", uint32(diskNumber)).
			Uint32("transfer_size", uint32(transferSize)).
			Uint64("file_object", fileObject).
			Uint32("process_id", processID).
			Msg("Disk write - using mapped ProcessId from FileObject")
	}

	// Record write operation in custom collector
	d.customCollector.RecordDiskIO(uint32(diskNumber), processID, uint32(transferSize), true)

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
//
// Parameters:
//   - helper: ETW event record helper containing the event data
//
// Returns:
//   - error: Error if event processing fails, nil otherwise
func (d *DiskIOHandler) HandleDiskFlush(helper *etw.EventRecordHelper) error {
	diskNumber, err := helper.GetPropertyUint("DiskNumber")
	if err != nil {
		d.log.Error().Err(err).Msg("Failed to get DiskNumber property for disk flush")
		return err
	}

	// Record flush operation in custom collector
	d.customCollector.RecordDiskFlush(uint32(diskNumber))

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
//
// Parameters:
//   - helper: ETW event record helper containing the event data
//
// Returns:
//   - error: Error if event processing fails, nil otherwise
func (d *DiskIOHandler) HandleFileCreate(helper *etw.EventRecordHelper) error {
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

	d.log.Trace().
		Uint64("file_object", fileObject).
		Uint32("process_id", processID).
		Msg("File create - stored FileObject mapping")

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
//
// Parameters:
//   - helper: ETW event record helper containing the event data
//
// Returns:
//   - error: Error if event processing fails, nil otherwise
func (d *DiskIOHandler) HandleFileClose(helper *etw.EventRecordHelper) error {
	fileObject, err := helper.GetPropertyUint("FileObject")
	if err != nil {
		return nil // Can't correlate without FileObject
	}

	// Remove the FileObject mapping to prevent stale entries
	d.fileObjectMutex.Lock()
	delete(d.fileObjectMap, fileObject)
	d.fileObjectMutex.Unlock()

	d.log.Trace().
		Uint64("file_object", fileObject).
		Msg("File close - removed FileObject mapping")

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
//
// Parameters:
//   - helper: ETW event record helper containing the event data
//
// Returns:
//   - error: Error if event processing fails, nil otherwise
func (d *DiskIOHandler) HandleFileWrite(helper *etw.EventRecordHelper) error {
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

	d.log.Trace().
		Uint64("file_object", fileObject).
		Uint32("process_id", processID).
		Msg("File write - updated FileObject mapping")

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
//
// Parameters:
//   - helper: ETW event record helper containing the event data
//
// Returns:
//   - error: Error if event processing fails, nil otherwise
func (d *DiskIOHandler) HandleFileRead(helper *etw.EventRecordHelper) error {
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

	d.log.Trace().
		Uint64("file_object", fileObject).
		Uint32("process_id", processID).
		Msg("File read - updated FileObject mapping")

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
//
// Parameters:
//   - helper: ETW event record helper containing the event data
//
// Returns:
//   - error: Error if event processing fails, nil otherwise
func (d *DiskIOHandler) HandleFileDelete(helper *etw.EventRecordHelper) error {
	fileObject, err := helper.GetPropertyUint("FileObject")
	if err != nil {
		return nil // Can't correlate without FileObject
	}

	// Remove the FileObject mapping when file is deleted
	d.fileObjectMutex.Lock()
	delete(d.fileObjectMap, fileObject)
	d.fileObjectMutex.Unlock()

	d.log.Trace().
		Uint64("file_object", fileObject).
		Msg("File delete - removed FileObject mapping")

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
//
// Parameters:
//   - helper: ETW event record helper containing the event data
//
// Returns:
//   - error: Error if event processing fails, nil otherwise
func (d *DiskIOHandler) HandleSystemConfigPhyDisk(helper *etw.EventRecordHelper) error {
	// Get required DiskNumber field
	diskNumber, err := helper.GetPropertyUint("DiskNumber")
	if err != nil {
		d.log.Error().Err(err).Msg("Failed to get DiskNumber property for physical disk config")
		return err
	}

	// Get Manufacturer (available in MOF)
	manufacturer, err := helper.GetPropertyString("Manufacturer")
	if err != nil {
		manufacturer = "Unknown" // Optional field, use default if not available
	}

	// Record physical disk information in custom collector
	d.customCollector.RecordPhysicalDiskInfo(uint32(diskNumber), manufacturer)

	d.log.Trace().
		Uint32("disk_number", uint32(diskNumber)).
		Str("manufacturer", manufacturer).
		Msg("Physical disk configuration recorded")

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
//
// Parameters:
//   - helper: ETW event record helper containing the event data
//
// Returns:
//   - error: Error if event processing fails, nil otherwise
func (d *DiskIOHandler) HandleSystemConfigLogDisk(helper *etw.EventRecordHelper) error {
	// Get required DiskNumber field
	diskNumber, err := helper.GetPropertyUint("DiskNumber")
	if err != nil {
		d.log.Error().Err(err).Msg("Failed to get DiskNumber property for logical disk config")
		return err
	}

	// Get drive letter
	driveLetterString, err := helper.GetPropertyString("DriveLetterString")
	if err != nil {
		driveLetterString = "" // Optional field
	}

	// Get file system info
	fileSystem, err := helper.GetPropertyString("FileSystem")
	if err != nil {
		fileSystem = "Unknown" // FileSystem field is optional
	}

	// Record logical disk information in custom collector
	d.customCollector.RecordLogicalDiskInfo(uint32(diskNumber), driveLetterString, fileSystem)

	d.log.Trace().
		Uint32("disk_number", uint32(diskNumber)).
		Str("drive_letter", driveLetterString).
		Str("file_system", fileSystem).
		Msg("Logical disk configuration recorded")

	return nil
}

// GetCustomCollector returns the custom Prometheus collector for disk I/O metrics.
// This method provides access to the DiskIOCustomCollector for registration
// with the Prometheus registry, enabling high-performance metric collection.
//
// Usage Example:
//
//	diskIOHandler := NewDiskIOHandler()
//	prometheus.MustRegister(diskIOHandler.GetCustomCollector())
//
// The custom collector follows Prometheus best practices:
// - Implements prometheus.Collector interface
// - Uses atomic operations for high-frequency updates
// - Provides thread-safe metric aggregation
// - Maintains low cardinality for performance
//
// Returns:
//   - *DiskIOCustomCollector: The custom collector instance for registration
func (d *DiskIOHandler) GetCustomCollector() *DiskIOCustomCollector {
	return d.customCollector
}
