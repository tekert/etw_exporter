package main

import (
	"github.com/tekert/golang-etw/etw"
)

// Global collector instance for handling events
var diskCollector *DiskIOCollector

// handleEventRecord processes raw ETW events - first callback
func handleEventRecord(record *etw.EventRecord) bool {
	// Return true to continue processing, false to skip
	// This is used for early filtering if needed
	return true
}

// handleEventRecordHelper processes events after TraceEventInfo parsing
func handleEventRecordHelper(helper *etw.EventRecordHelper) error {
	// This callback is called after the event structure is parsed
	// but before converting to string representations
	return nil
}

// handlePreparedEvent processes prepared events - main callback for metrics
func handlePreparedEvent(helper *etw.EventRecordHelper) error {
	if diskCollector == nil {
		return nil
	}

	guid := helper.ProviderGUID()
	eventID := helper.EventID()

	// Route events to appropriate handlers based on provider GUID comparison (more efficient)
	if DiskIoKernelGUID.Equals(&guid) {
		switch eventID {
		case 10, 11: // DiskIo Read/Write completion - DiskIo_TypeGroup1
			return handleKernelDiskIO(helper)
		case 12, 13: // DiskIo Read/Write init - DiskIo_TypeGroup1
			return handleKernelDiskIOInit(helper)
		case 15: // DiskIo Flush - DiskIo_TypeGroup1
			return handleKernelDiskFlush(helper)
		}
	} else if ProcessKernelGUID.Equals(&guid) {
		switch eventID {
		case 1, 2, 3, 4: // Process Start/End/DCStart/DCEnd - Process_TypeGroup1
			return handleKernelProcessEvent(helper)
		}
	} else if helper.EventRec.EventHeader.Flags&etw.EVENT_HEADER_FLAG_CLASSIC_HEADER != 0 {
		// Handle SystemConfig events (no specific GUID, identified by classic header + event ID)
		if eventID == 11 { // SystemConfig PhyDisk - EVENT_TRACE_TYPE_CONFIG_PHYSICALDISK
			return handleSystemConfigDisk(helper)
		}
	}

	return nil
}

// handleEvent processes fully parsed events (not used for metrics, just for completion)
func handleEvent(event *etw.Event) error {
	// This would be used if we needed fully parsed string events
	// For performance, we use handlePreparedEvent instead
	return nil
}

// handleKernelDiskIO processes kernel disk I/O completion events
// MOF Event: DiskIo_TypeGroup1
// Provider: {3d6fa8d4-fe05-11d0-9dda-00c04fd7ba7c} (DiskIo)
// EventType 10: DiskReadCompletion
// EventType 11: DiskWriteCompletion
// Event Fields:
//   - DiskNumber (uint32): Physical disk number
//   - IrpFlags (uint32): I/O request packet flags
//   - TransferSize (uint32): Number of bytes transferred
//   - Reserved (uint32): Reserved field
//   - ByteOffset (uint64): Byte offset on disk
//   - FileObject (pointer): File object pointer
//   - Irp (pointer): I/O request packet pointer
//   - HighResResponseTime (uint64): High resolution response time
func handleKernelDiskIO(helper *etw.EventRecordHelper) error {
	eventID := helper.EventID()

	switch eventID {
	case 10: // DiskReadCompletion
		return diskCollector.HandleDiskRead(helper)
	case 11: // DiskWriteCompletion
		return diskCollector.HandleDiskWrite(helper)
	}

	return nil
}

// handleKernelDiskIOInit processes disk I/O initiation events
// MOF Event: DiskIo_TypeGroup1
// Provider: {3d6fa8d4-fe05-11d0-9dda-00c04fd7ba7c} (DiskIo)
// EventType 12: DiskReadInit
// EventType 13: DiskWriteInit
// Event Fields:
//   - DiskNumber (uint32): Physical disk number
//   - IrpFlags (uint32): I/O request packet flags
//   - TransferSize (uint32): Number of bytes to transfer
//   - Reserved (uint32): Reserved field
//   - ByteOffset (uint64): Byte offset on disk
//   - FileObject (pointer): File object pointer
//   - Irp (pointer): I/O request packet pointer
func handleKernelDiskIOInit(helper *etw.EventRecordHelper) error {
	// These are initiation events, we primarily care about completion
	return nil
}

// handleKernelDiskFlush processes disk flush events
// MOF Event: DiskIo_TypeGroup1
// Provider: {3d6fa8d4-fe05-11d0-9dda-00c04fd7ba7c} (DiskIo)
// EventType 15: DiskFlushCompletion
// Event Fields:
//   - DiskNumber (uint32): Physical disk number
//   - IrpFlags (uint32): I/O request packet flags
//   - HighResResponseTime (uint64): High resolution response time
//   - Irp (pointer): I/O request packet pointer
func handleKernelDiskFlush(helper *etw.EventRecordHelper) error {
	return diskCollector.HandleDiskFlush(helper)
}

// handleSystemConfigDisk processes SystemConfig disk events for disk correlation
// MOF Event: SystemConfig_PhyDisk
// Provider: NT Kernel Logger (system configuration)
// EventType 11: EVENT_TRACE_TYPE_CONFIG_PHYSICALDISK
// Event Fields:
//   - DiskNumber (uint32): Physical disk number
//   - BytesPerSector (uint32): Bytes per sector
//   - SectorsPerTrack (uint32): Sectors per track
//   - TracksPerCylinder (uint32): Tracks per cylinder
//   - Cylinders (uint64): Total cylinders
//   - SCSIPort (uint32): SCSI port number
//   - SCSIPath (uint32): SCSI path number
//   - SCSITarget (uint32): SCSI target number
//   - SCSILun (uint32): SCSI LUN number
//   - Manufacturer (string): Device manufacturer
//   - PartitionCount (uint32): Number of partitions
//   - WriteCacheEnabled (bool): Write cache enabled flag
//   - Pad (uint8): Padding
//   - BootDriveLetter (string): Boot drive letter
//   - Spare (string): Spare field
func handleSystemConfigDisk(helper *etw.EventRecordHelper) error {
	return diskCollector.HandleSystemConfigDisk(helper)
}

// handleKernelProcessEvent processes process start/end events for name tracking
// MOF Event: Process_TypeGroup1
// Provider: {3d6fa8d0-fe05-11d0-9dda-00c04fd7ba7c} (Process)
// EventType 1: ProcessStart
// EventType 2: ProcessEnd
// EventType 3: ProcessDCStart (data collection/rundown start)
// EventType 4: ProcessDCEnd (data collection/rundown end)
// Event Fields:
//   - ImageName (string): Process executable name
//   - ProcessID (uint32): Process identifier
//   - ParentID (uint32): Parent process identifier
//   - SessionID (uint32): Session identifier
//   - ExitStatus (uint32): Process exit status (End events only)
//   - DirectoryTableBase (uint32): Page directory base
//   - Flags (uint32): Process flags
//   - UserSID (SID): User security identifier
//   - ImageChecksum (uint32): Image checksum
//   - TimeDateStamp (uint32): Image timestamp
//   - Reserved0 (uint32): Reserved field
//   - DefaultHeapSize (uint32): Default heap size
//   - ProcessVersion (uint32): Process version
//   - LoaderFlags (uint32): Loader flags
//   - Reserved1 (uint32): Reserved field
//   - Reserved2 (uint32): Reserved field
//   - Reserved3 (uint32): Reserved field
//   - Reserved4 (uint32): Reserved field
//   - CommandLine (string): Command line (Start events only)
func handleKernelProcessEvent(helper *etw.EventRecordHelper) error {
	eventID := helper.EventID()

	switch eventID {
	case 1: // ProcessStart
		return diskCollector.HandleProcessStart(helper)
	case 2: // ProcessEnd
		return diskCollector.HandleProcessEnd(helper)
	case 3: // ProcessDCStart (rundown start)
		return diskCollector.HandleProcessStart(helper)
	case 4: // ProcessDCEnd (rundown end)
		return diskCollector.HandleProcessEnd(helper)
	}

	return nil
}

// handleDiskManifestEvent - DISABLED: Manifest provider events
//
// This function would process Microsoft-Windows-Kernel-Disk manifest provider events,
// but we've disabled manifest providers in favor of NT Kernel Logger for the following reasons:
//
// 1. Avoid duplicated events: Both NT Kernel Logger and manifest providers produce
//    similar disk I/O events (Read/Write/Flush), leading to double-counting in metrics
//
// 2. SystemConfig events: NT Kernel Logger provides SystemConfig PhyDisk events (Event ID 11)
//    which are essential for correlating disk numbers to actual drive letters and models.
//    Manifest providers don't provide these correlation events.
//
// 3. Single session simplicity: NT Kernel Logger handles both disk I/O and SystemConfig
//    in one session, reducing complexity and ensuring event ordering consistency.
//
// 4. Compatibility: NT Kernel Logger works reliably on Windows build 19045 and earlier,
//    while newer system providers require Windows SDK 20348+ which isn't available
//    in this environment.
//
// If manifest providers are re-enabled in the future, this function should handle:
// - Event ID 10: DiskRead completion
// - Event ID 11: DiskWrite completion
// - Event ID 14: DiskFlush completion
//
// The manifest events would call the same handlers as kernel events:
// - diskCollector.HandleDiskRead(helper)
// - diskCollector.HandleDiskWrite(helper)
// - diskCollector.HandleDiskFlush(helper)
//
// func handleDiskManifestEvent(helper *etw.EventRecordHelper) error {
//     return diskCollector.HandleManifestDiskEvent(helper)
// }
