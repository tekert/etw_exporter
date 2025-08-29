package kerneldiskio

import (
	"sync"

	"github.com/tekert/goetw/etw"

	"etw_exporter/internal/config"
	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"

	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// processIdentifier holds the PID and unique StartKey for a process.
type processIdentifier struct {
	PID      uint32
	StartKey uint64
}

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
	config          *config.DiskIOConfig   // Collector configuration

	fileObjectMutex sync.RWMutex
	fileObjectMap   map[uint64]processIdentifier // FileObject -> {PID, StartKey} mapping

	log *phusluadapter.SampledLogger // Disk I/O handler logger
}

// NewDiskIOHandler creates a new disk I/O handler instance with custom collector integration.
// The custom collector provides high-performance metrics aggregation for disk I/O operations.
//
// Returns:
//   - *DiskIOHandler: A new disk I/O handler instance
func NewDiskIOHandler(config *config.DiskIOConfig) *DiskIOHandler {
	return &DiskIOHandler{
		customCollector: NewDiskIOCustomCollector(),
		config:          config,
		fileObjectMap:   make(map[uint64]processIdentifier),
		//log:             logger.NewLoggerWithContext("diskio_handler"),
		log: logger.NewSampledLoggerCtx("diskio_handler"),
	}
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
// Returns:
//   - *DiskIOCustomCollector: The custom collector instance for registration
func (d *DiskIOHandler) GetCustomCollector() *DiskIOCustomCollector {
	return d.customCollector
}

// HandleDiskRead processes disk read completion events to track disk read I/O.
//
// ETW Event Details:
//   - Provider Name: Microsoft-Windows-Kernel-Disk
//   - Provider GUID: {c7bde69a-e1e0-4177-b6ef-283ad1525271}
//   - Event ID(s): 10
//   - Event Name(s): DiskRead
//   - Event Version(s): 2
//   - Schema: Manifest (XML)
//
// Schema (from manifest):
//   - DiskNumber (win:UInt32): Index number of the disk.
//   - IrpFlags (win:HexInt32): IRP flags for the operation.
//   - TransferSize (win:UInt32): Transfer size in bytes.
//   - Reserved (win:UInt32): Reserved field.
//   - ByteOffset (win:UInt64): Offset into the file where the I/O begins.
//   - FileObject (win:Pointer): Pointer to the file object for correlation.
//   - IORequestPacket (win:Pointer): Pointer to the I/O request packet.
//   - HighResResponseTime (win:UInt64): High-resolution response time.
//
// This handler extracts the disk number, transfer size, and FileObject to record
// disk read metrics, attributing the I/O to a specific process via FileObject mapping.
func (d *DiskIOHandler) HandleDiskRead(helper *etw.EventRecordHelper) error {
	diskNumber, err := helper.GetPropertyUint("DiskNumber")
	if err != nil {
		d.log.SampledError("fail-disknumber").Err(err).Msg("Failed to get DiskNumber property for disk read")
		return err
	}

	transferSize, err := helper.GetPropertyUint("TransferSize")
	if err != nil {
		d.log.SampledError("failed-transfersize").Err(err).Msg("Failed to get TransferSize property for disk read")
		return err
	}

	// Get FileObject for process correlation
	fileObject, err := helper.GetPropertyUint("FileObject")
	if err != nil {
		// If we can't get FileObject, fall back to EventHeader ProcessId (may be incorrect)
		pid := helper.EventRec.EventHeader.ProcessId
		startKey, _ := statemanager.GetGlobalStateManager().GetProcessStartKey(pid)
		d.log.Trace().
			Uint32("disk_number", uint32(diskNumber)).
			Uint32("transfer_size", uint32(transferSize)).
			Uint32("process_id", pid).
			Msg("Disk read - no FileObject, using EventHeader ProcessId")
		d.customCollector.RecordDiskIO(uint32(diskNumber), pid, startKey, uint32(transferSize), false)
		return nil
	}

	// Look up the real process ID using FileObject correlation
	d.fileObjectMutex.RLock()
	ident, exists := d.fileObjectMap[fileObject]
	d.fileObjectMutex.RUnlock()

	var pid uint32
	var startKey uint64

	if !exists {
		// If no mapping exists, fall back to EventHeader ProcessId (may be incorrect) // TODO(tekert): check this case.
		pid = helper.EventRec.EventHeader.ProcessId
		startKey, _ = statemanager.GetGlobalStateManager().GetProcessStartKey(pid)
		d.log.Trace().
			Uint32("disk_number", uint32(diskNumber)).
			Uint32("transfer_size", uint32(transferSize)).
			Uint64("file_object", fileObject).
			Uint32("fallback_process_id", pid).
			Msg("Disk read - no FileObject mapping, using fallback ProcessId")
	} else {
		pid = ident.PID
		startKey = ident.StartKey
		d.log.Trace().
			Uint32("disk_number", uint32(diskNumber)).
			Uint32("transfer_size", uint32(transferSize)).
			Uint64("file_object", fileObject).
			Uint32("process_id", pid).
			Msg("Disk read - using mapped ProcessId from FileObject")
	}

	// Record read operation in custom collector
	d.customCollector.RecordDiskIO(uint32(diskNumber), pid, startKey, uint32(transferSize), false)

	return nil
}

// HandleDiskWrite processes disk write completion events to track disk write I/O.
//
// ETW Event Details:
//   - Provider Name: Microsoft-Windows-Kernel-Disk
//   - Provider GUID: {c7bde69a-e1e0-4177-b6ef-283ad1525271}
//   - Event ID(s): 11
//   - Event Name(s): DiskWrite
//   - Event Version(s): 2
//   - Schema: Manifest (XML)
//
// Schema (from manifest):
//   - DiskNumber (win:UInt32): Index number of the disk.
//   - IrpFlags (win:HexInt32): IRP flags for the operation.
//   - TransferSize (win:UInt32): Transfer size in bytes.
//   - Reserved (win:UInt32): Reserved field.
//   - ByteOffset (win:UInt64): Offset into the file where the I/O begins.
//   - FileObject (win:Pointer): Pointer to the file object for correlation.
//   - IORequestPacket (win:Pointer): Pointer to the I/O request packet.
//   - HighResResponseTime (win:UInt64): High-resolution response time.
//
// This handler extracts the disk number, transfer size, and FileObject to record
// disk write metrics, attributing the I/O to a specific process via FileObject mapping.
func (d *DiskIOHandler) HandleDiskWrite(helper *etw.EventRecordHelper) error {
	diskNumber, err := helper.GetPropertyUint("DiskNumber")
	if err != nil {
		d.log.SampledError("fail-disknumber").Err(err).Msg("Failed to get DiskNumber property for disk write")
		return err
	}

	transferSize, err := helper.GetPropertyUint("TransferSize")
	if err != nil {
		d.log.SampledError("failed-transfersize").Err(err).Msg("Failed to get TransferSize property for disk write")
		return err
	}

	// Get FileObject for process correlation
	fileObject, err := helper.GetPropertyUint("FileObject")
	if err != nil {
		// If we can't get FileObject, fall back to EventHeader ProcessId (may be incorrect) // TODO(tekert): check this.
		pid := helper.EventRec.EventHeader.ProcessId
		startKey, _ := statemanager.GetGlobalStateManager().GetProcessStartKey(pid)
		d.log.Trace().
			Uint32("disk_number", uint32(diskNumber)).
			Uint32("transfer_size", uint32(transferSize)).
			Uint32("process_id", pid).
			Msg("Disk write - no FileObject, using EventHeader ProcessId")
		d.customCollector.RecordDiskIO(uint32(diskNumber), pid, startKey, uint32(transferSize), true)
		return nil
	}

	// Look up the real process ID using FileObject correlation
	d.fileObjectMutex.RLock()
	ident, exists := d.fileObjectMap[fileObject]
	d.fileObjectMutex.RUnlock()

	var pid uint32
	var startKey uint64

	if !exists {
		// If no mapping exists, fall back to EventHeader ProcessId (may be incorrect) // TODO(tekert): check this.
		pid = helper.EventRec.EventHeader.ProcessId
		startKey, _ = statemanager.GetGlobalStateManager().GetProcessStartKey(pid)
		d.log.Trace().
			Uint32("disk_number", uint32(diskNumber)).
			Uint32("transfer_size", uint32(transferSize)).
			Uint64("file_object", fileObject).
			Uint32("fallback_process_id", pid).
			Msg("Disk write - no FileObject mapping, using fallback ProcessId")
	} else {
		pid = ident.PID
		startKey = ident.StartKey
		d.log.Trace().
			Uint32("disk_number", uint32(diskNumber)).
			Uint32("transfer_size", uint32(transferSize)).
			Uint64("file_object", fileObject).
			Uint32("process_id", pid).
			Msg("Disk write - using mapped ProcessId from FileObject")
	}

	// Record write operation in custom collector
	d.customCollector.RecordDiskIO(uint32(diskNumber), pid, startKey, uint32(transferSize), true)

	return nil
}

// HandleDiskFlush processes disk flush events to count disk flush operations.
//
// ETW Event Details:
//   - Provider Name: Microsoft-Windows-Kernel-Disk
//   - Provider GUID: {c7bde69a-e1e0-4177-b6ef-283ad1525271}
//   - Event ID(s): 14
//   - Event Name(s): DiskFlush
//   - Event Version(s): 2
//   - Schema: Manifest (XML)
//
// Schema (from manifest):
//   - DiskNumber (win:UInt32): Index number of the disk.
//   - IrpFlags (win:HexInt32): IRP flags for the operation.
//   - HighResResponseTime (win:UInt64): High-resolution response time.
//   - IORequestPacket (win:Pointer): Pointer to the I/O request packet.
//
// This handler is responsible for counting disk flush operations per disk.
func (d *DiskIOHandler) HandleDiskFlush(helper *etw.EventRecordHelper) error {
	diskNumber, err := helper.GetPropertyUint("DiskNumber")
	if err != nil {
		d.log.SampledError("fail-disknumber").Err(err).Msg("Failed to get DiskNumber property for disk flush")
		return err
	}

	// Record flush operation in custom collector
	d.customCollector.RecordDiskFlush(uint32(diskNumber))

	return nil
}

// HandleFileCreate processes File Create events to build the FileObject-to-ProcessID map.
//
// ETW Event Details:
//   - Provider Name: Microsoft-Windows-Kernel-File
//   - Provider GUID: {edd08927-9cc4-4e65-b970-c2560fb5c289}
//   - Event ID(s): 12
//   - Event Name(s): Create
//   - Event Version(s): 2
//   - Schema: Manifest (XML)
//
// Schema (from manifest):
//   - Irp (win:Pointer): Pointer to the I/O request packet.
//   - FileObject (win:Pointer): Pointer to the file object for correlation.
//   - IssuingThreadId (win:UInt32): Thread ID that issued the I/O (V1 template).
//   - CreateOptions (win:UInt32): Create options.
//   - CreateAttributes (win:UInt32): Create attributes.
//   - ShareAccess (win:UInt32): Share access.
//   - FileName (win:UnicodeString): The name of the file being created/opened.
//
// This handler is crucial for correlating subsequent disk I/O events (which only have a
// FileObject) back to the process that initiated them by mapping the FileObject to the
// ProcessId from the event header.
func (d *DiskIOHandler) HandleFileCreate(helper *etw.EventRecordHelper) error {
	fileObject, err := helper.GetPropertyUint("FileObject")
	if err != nil {
		return nil // Can't correlate without FileObject
	}

	// The process ID from the event header is correct for File I/O events
	processID := helper.EventRec.EventHeader.ProcessId
	startKey, _ := helper.EventRec.ExtProcessStartKey()

	// Store the FileObject -> {PID, StartKey} mapping
	d.fileObjectMutex.Lock()
	d.fileObjectMap[fileObject] = processIdentifier{PID: processID, StartKey: startKey}
	d.fileObjectMutex.Unlock()

	d.log.Trace().
		Uint64("file_object", fileObject).
		Uint32("process_id", processID).
		Msg("File create - stored FileObject mapping")

	return nil
}

// HandleFileClose processes File Close events to clean up the FileObject map.
//
// ETW Event Details:
//   - Provider Name: Microsoft-Windows-Kernel-File
//   - Provider GUID: {edd08927-9cc4-4e65-b970-c2560fb5c289}
//   - Event ID(s): 14
//   - Event Name(s): Close
//   - Event Version(s): 2
//   - Schema: Manifest (XML)
//
// Schema (from manifest):
//   - Irp (win:Pointer): Pointer to the I/O request packet.
//   - FileObject (win:Pointer): Pointer to the file object for correlation.
//   - FileKey (win:Pointer): File key.
//   - IssuingThreadId (win:UInt32): Thread ID that issued the I/O (V1 template).
//
// This handler removes the FileObject-to-ProcessID mapping when a file handle is
// closed to prevent the map from growing indefinitely and holding stale entries.
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

// HandleFileWrite processes File Write events to update the FileObject map.
//
// ETW Event Details:
//   - Provider Name: Microsoft-Windows-Kernel-File
//   - Provider GUID: {edd08927-9cc4-4e65-b970-c2560fb5c289}
//   - Event ID(s): 16
//   - Event Name(s): Write
//   - Event Version(s): 2
//   - Schema: Manifest (XML)
//
// Schema (from manifest):
//   - ByteOffset (win:UInt64): Offset into the file where the I/O begins.
//   - Irp (win:Pointer): Pointer to the I/O request packet.
//   - FileObject (win:Pointer): Pointer to the file object for correlation.
//   - FileKey (win:Pointer): File key.
//   - IssuingThreadId (win:UInt32): Thread ID that issued the I/O (V1 template).
//   - IOSize (win:UInt32): I/O size in bytes.
//   - IOFlags (win:UInt32): I/O flags.
//   - ExtraFlags (win:UInt32): Extra flags (V1 template).
//
// This handler ensures the FileObject-to-ProcessID mapping is kept up-to-date, as
// a file handle might be used by a process long after it was created.
func (d *DiskIOHandler) HandleFileWrite(helper *etw.EventRecordHelper) error {
	fileObject, err := helper.GetPropertyUint("FileObject")
	if err != nil {
		return nil // Can't correlate without FileObject
	}

	// The process ID from the event header is correct for File I/O events
	processID := helper.EventRec.EventHeader.ProcessId
	startKey, _ := helper.EventRec.ExtProcessStartKey()

	// Store/update the FileObject -> {PID, StartKey} mapping
	d.fileObjectMutex.Lock()
	d.fileObjectMap[fileObject] = processIdentifier{PID: processID, StartKey: startKey}
	d.fileObjectMutex.Unlock()

	d.log.Trace().
		Uint64("file_object", fileObject).
		Uint32("process_id", processID).
		Msg("File write - updated FileObject mapping")

	return nil
}

// HandleFileRead processes File Read events to update the FileObject map.
//
// ETW Event Details:
//   - Provider Name: Microsoft-Windows-Kernel-File
//   - Provider GUID: {edd08927-9cc4-4e65-b970-c2560fb5c289}
//   - Event ID(s): 15
//   - Event Name(s): Read
//   - Event Version(s): 2
//   - Schema: Manifest (XML)
//
// Schema (from manifest):
//   - ByteOffset (win:UInt64): Offset into the file where the I/O begins.
//   - Irp (win:Pointer): Pointer to the I/O request packet.
//   - FileObject (win:Pointer): Pointer to the file object for correlation.
//   - FileKey (win:Pointer): File key.
//   - IssuingThreadId (win:UInt32): Thread ID that issued the I/O (V1 template).
//   - IOSize (win:UInt32): I/O size in bytes.
//   - IOFlags (win:UInt32): I/O flags.
//   - ExtraFlags (win:UInt32): Extra flags (V1 template).
//
// This handler ensures the FileObject-to-ProcessID mapping is kept up-to-date, as
// a file handle might be used by a process long after it was created.
func (d *DiskIOHandler) HandleFileRead(helper *etw.EventRecordHelper) error {
	fileObject, err := helper.GetPropertyUint("FileObject")
	if err != nil {
		return nil // Can't correlate without FileObject
	}

	// The process ID from the event header is correct for File I/O events
	processID := helper.EventRec.EventHeader.ProcessId
	startKey, _ := helper.EventRec.ExtProcessStartKey()

	// Store/update the FileObject -> {PID, StartKey} mapping
	d.fileObjectMutex.Lock()
	d.fileObjectMap[fileObject] = processIdentifier{PID: processID, StartKey: startKey}
	d.fileObjectMutex.Unlock()

	d.log.Trace().
		Uint64("file_object", fileObject).
		Uint32("process_id", processID).
		Msg("File read - updated FileObject mapping")

	return nil
}

// HandleFileDelete processes File Delete events to clean up the FileObject map.
//
// ETW Event Details:
//   - Provider Name: Microsoft-Windows-Kernel-File
//   - Provider GUID: {edd08927-9cc4-4e65-b970-c2560fb5c289}
//   - Event ID(s): 26
//   - Event Name(s): DeletePath
//   - Event Version(s): 2
//   - Schema: Manifest (XML)
//
// Schema (from manifest):
//   - Irp (win:Pointer): Pointer to the I/O request packet.
//   - FileObject (win:Pointer): Pointer to the file object for correlation.
//   - FileKey (win:Pointer): File key.
//   - ExtraInformation (win:Pointer): Extra information.
//   - IssuingThreadId (win:UInt32): Thread ID that issued the I/O (V1 template).
//   - InfoClass (win:UInt32): Information class.
//   - FilePath (win:UnicodeString): File path.
//
// This handler removes the FileObject-to-ProcessID mapping when a file is deleted.
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
