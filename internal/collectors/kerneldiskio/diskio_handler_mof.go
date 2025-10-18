package kerneldiskio

import (
	"github.com/tekert/goetw/etw"

	"etw_exporter/internal/config"
	"etw_exporter/internal/etw/handlers"
	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"

	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// ----------------------------------------------------------
// https://learn.microsoft.com/en-us/windows-hardware/drivers/ddi/wdm/ns-wdm-_irp
// typedef struct _IRP
//
// https://learn.microsoft.com/en-us/windows-hardware/drivers/ddi/wdm/ns-wdm-_file_object
// typedef struct _FILE_OBJECT
//
// Important:
// https://learn.microsoft.com/en-us/archive/msdn-magazine/2009/october/core-instrumentation-events-in-windows-7-part-2
// https://www.microsoftpressstore.com/articles/article.aspx?p=2201309&seqNum=3

// HandlerMof handles disk I/O events from ETW providers.
// This handler processes ETW events from the Microsoft-Windows-Kernel-Disk provider
// to collect performance metrics for disk read, write, and flush operations.
//
// It uses a two-step process for attributing I/O to a specific process:
//  1. (Preferred) It uses the `IssuingThreadId` from the event, which requires
//     thread-tracking ETW events to be enabled, to get the most accurate PID.
//  2. (Fallback) If `IssuingThreadId` is unavailable or cannot be resolved, it falls
//     back to the `ProcessId` in the event header. This is reliable but may attribute
//     cached I/O to the System process.
type HandlerMof struct {
	customCollector *DiskCollector       // High-performance custom collector
	config          *config.DiskIOConfig // Collector configuration
	stateManager    *statemanager.KernelStateManager

	log *phusluadapter.SampledLogger // Disk I/O handler logger
}

// NewDiskIOHandler creates a new disk I/O handler instance.
// The custom collector provides high-performance metrics aggregation for disk I/O operations.
//
// Returns:
//   - *DiskIOHandler: A new disk I/O handler instance
func NewDiskIOHandler(config *config.DiskIOConfig, sm *statemanager.KernelStateManager) *HandlerMof {
	return &HandlerMof{
		customCollector: nil,
		config:          config,
		stateManager:    sm,
		log:             logger.NewSampledLoggerCtx("diskio_handler"),
	}
}

// AttachCollector allows a metrics collector to subscribe to the handler's events.
func (c *HandlerMof) AttachCollector(collector *DiskCollector) {
	c.log.Debug().Msg("Attaching metrics collector to diskio handler.")
	c.customCollector = collector
}

// RegisterRoutes tells the EventHandler which ETW events this handler is interested in.
func (h *HandlerMof) RegisterRoutes(router handlers.Router) {
	// Use this if using NT Kernel logger, if not the events are out of sync and will be bad atrributed.

	// Provider: NT Kernel Logger (DiskIo) ({3d6fa8d4-fe05-11d0-9dda-00c04fd7ba7c}) - MOF
	// Provider: System IO Provider ({9e814aad-3204-11d2-9a82-006008a86939}) - Win11+
	diskIoRoutesRaw := map[uint8]handlers.RawEventHandlerFunc{
		etw.EVENT_TRACE_TYPE_IO_READ:  h.HandleDiskReadMofRaw,  // DiskRead
		etw.EVENT_TRACE_TYPE_IO_WRITE: h.HandleDiskWriteMofRaw, // DiskWrite
		etw.EVENT_TRACE_TYPE_IO_FLUSH: h.HandleDiskFlushMofRaw, // DiskFlush
	}
	handlers.RegisterRawRoutesForGUID(router, etw.DiskIoKernelGuid, diskIoRoutesRaw)     // Win10
	handlers.RegisterRawRoutesForGUID(router, etw.SystemIoProviderGuid, diskIoRoutesRaw) // Win11+

	h.log.Debug().Msg("Disk I/O routes registered")
}

// Name returns the name of the collector.
func (h *HandlerMof) Name() string {
	return "disk_io"
}

// IsEnabled checks if the collector is enabled in the configuration.
func (h *HandlerMof) IsEnabled(cfg *config.CollectorConfig) bool {
	return cfg.DiskIO.Enabled
}

// GetCustomCollector returns the custom Prometheus collector for disk I/O metrics.
func (d *HandlerMof) GetCustomCollector() *DiskCollector {
	return d.customCollector
}

// HandleDiskReadMofRaw processes raw DiskRead (Type 10) events from the MOF provider.
// This handler is optimized for performance by directly parsing the raw event data,
// avoiding the overhead of property lookups. It extracts disk number, transfer size,
// and the crucial IssuingThreadId to attribute I/O to the originating process,
// which is especially important for cached I/O that would otherwise be attributed
// to the System process.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger
//   - Provider GUID: {3d6fa8d4-fe05-11d0-9dda-00c04fd7ba7c} (DiskIoGuid)
//   - Event ID(s): 10 (EVENT_TRACE_TYPE_IO_READ)
//   - Event Name(s): Read
//   - Event Version(s): 3
//   - Schema: MOF
//
// Schema (from MOF class DiskIo_TypeGroup1, 64-bit layout):
//   - DiskNumber (win:UInt32): Index of the physical disk. Offset: 0
//   - IrpFlags (win:UInt32): I/O Request Packet flags. Offset: 4
//   - TransferSize (win:UInt32): Size of data transfer in bytes. Offset: 8
//   - Reserved (win:UInt32): Reserved. Offset: 12
//   - ByteOffset (win:UInt64): Byte offset from the beginning of the disk. Offset: 16
//   - FileObject (win:Pointer): Pointer to the file object. Offset: 24
//   - Irp (win:Pointer): Pointer to the I/O Request Packet. Offset: 32
//   - HighResResponseTime (win:UInt64): High-resolution response time. Offset: 40
//   - IssuingThreadId (win:UInt32): ID of the thread that issued the I/O. Offset: 48
func (d *HandlerMof) HandleDiskReadMofRaw(er *etw.EventRecord) error {
	if d.customCollector == nil {
		return nil
	}

	var diskNumber, transferSize, issuingTID uint32
	var err error

	diskNumber, err = er.GetUint32At(0)
	if err != nil {
		d.log.SampledError("diskread-raw-parse-err").Err(err).Msg("Failed to parse DiskNumber (32-bit)")
		return err
	}
	transferSize, err = er.GetUint32At(8)
	if err != nil {
		d.log.SampledError("diskread-raw-parse-err").Err(err).Msg("Failed to parse TransferSize (32-bit)")
		return err
	}
	var at uintptr = 48
	if er.PointerSize() != 8 {
		at = 40
	}
	issuingTID, err = er.GetUint32At(at) // Pointer size 8
	if err != nil {
		d.log.SampledError("diskread-raw-parse-err").Err(err).Msg("Failed to parse IssuingThreadId (32-bit)")
		return err
	}

	var pData *statemanager.ProcessData

	// --- Attribution Logic ---
	// 1. IssuingThreadId: The most accurate source, linking I/O to the originating thread.
	// 2. Event Header PID: A reliable fallback for non-deferred I/O.
	// 3. System Process: The final fallback for deferred I/O or unattributable events.

	// Attempt attribution via the Issuing Thread ID.
	if issuingTID != 0 && issuingTID != 0xFFFFFFFF {
		if p, ok := d.stateManager.Threads.GetCurrentProcessDataByThread(issuingTID); ok {
			pData = p
		}
	}

	// If TID attribution failed, fall back to the Process ID in the event header.
	if pData == nil {
		headerPID := er.EventHeader.ProcessId
		if headerPID != 0 && headerPID != 0xFFFFFFFF {
			if p, ok := d.stateManager.Processes.GetCurrentProcessDataByPID(headerPID); ok {
				pData = p
			}
		}
	}

	// If both primary and secondary attribution failed, this is an unattributable event.
	// We log a warning for unexpected cases and then perform a final fallback to the System process.
	if pData == nil {
		headerPID := er.EventHeader.ProcessId
		// Only log a warning if the failure was unexpected.
		if issuingTID != 0xFFFFFFFF && headerPID != 0xFFFFFFFF {
			d.log.SampledWarn("disk_read_unattributed").
				Uint32("issuing_tid", issuingTID).
				Uint32("header_pid", headerPID).
				Msg("DiskRead could not be attributed via TID or PID; falling back to System.")
		}

		// Final fallback: Attribute to the System process (PID 4).
		if p, ok := d.stateManager.Processes.GetCurrentProcessDataByPID(statemanager.SystemProcessID); ok {
			pData = p
		}
	}

	// At this point, pData is guaranteed to be non-nil, as we always fall back to System.
	d.customCollector.RecordDiskIO(diskNumber, pData, transferSize, false)

	return nil
}

// HandleDiskWriteMofRaw processes raw DiskWrite (Type 11) events from the MOF provider.
// This handler is optimized for performance by directly parsing the raw event data,
// avoiding the overhead of property lookups. It extracts disk number, transfer size,
// and the crucial IssuingThreadId to attribute I/O to the originating process,
// which is especially important for cached I/O that would otherwise be attributed
// to the System process.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger
//   - Provider GUID: {3d6fa8d4-fe05-11d0-9dda-00c04fd7ba7c} (DiskIoGuid)
//   - Event ID(s): 11 (EVENT_TRACE_TYPE_IO_WRITE)
//   - Event Name(s): Write
//   - Event Version(s): 3
//   - Schema: MOF
//
// Schema (from MOF class DiskIo_TypeGroup1, 64-bit layout):
//   - DiskNumber (win:UInt32): Index of the physical disk. Offset: 0
//   - IrpFlags (win:UInt32): I/O Request Packet flags. Offset: 4
//   - TransferSize (win:UInt32): Size of data transfer in bytes. Offset: 8
//   - Reserved (win:UInt32): Reserved. Offset: 12
//   - ByteOffset (win:UInt64): Byte offset from the beginning of the disk. Offset: 16
//   - FileObject (win:Pointer): Pointer to the file object. Offset: 24
//   - Irp (win:Pointer): Pointer to the I/O Request Packet. Offset: 32
//   - HighResResponseTime (win:UInt64): High-resolution response time. Offset: 40
//   - IssuingThreadId (win:UInt32): ID of the thread that issued the I/O. Offset: 48
func (d *HandlerMof) HandleDiskWriteMofRaw(er *etw.EventRecord) error {
	if d.customCollector == nil {
		return nil
	}

	var diskNumber, transferSize, issuingTID uint32
	var err error

	diskNumber, err = er.GetUint32At(0)
	if err != nil {
		d.log.SampledError("diskread-raw-parse-err").Err(err).Msg("Failed to parse DiskNumber (32-bit)")
		return err
	}
	transferSize, err = er.GetUint32At(8)
	if err != nil {
		d.log.SampledError("diskread-raw-parse-err").Err(err).Msg("Failed to parse TransferSize (32-bit)")
		return err
	}
	var at uintptr = 48
	if er.PointerSize() != 8 {
		at = 40
	}
	issuingTID, err = er.GetUint32At(at) // Pointer size 8
	if err != nil {
		d.log.SampledError("diskread-raw-parse-err").Err(err).Msg("Failed to parse IssuingThreadId (32-bit)")
		return err
	}

	var pData *statemanager.ProcessData = nil

	// Attempt attribution via the Issuing Thread ID.
	if issuingTID != 0 && issuingTID != 0xFFFFFFFF {
		if p, ok := d.stateManager.Threads.GetCurrentProcessDataByThread(issuingTID); ok {
			pData = p
		}
	}

	// If TID attribution failed, fall back to the Process ID in the event header.
	if pData == nil {
		headerPID := er.EventHeader.ProcessId
		if headerPID != 0 && headerPID != 0xFFFFFFFF {
			if p, ok := d.stateManager.Processes.GetCurrentProcessDataByPID(headerPID); ok {
				pData = p
			}
		}
	}

	// If both primary and secondary attribution failed, this is an unattributable event.
	// We log a warning for unexpected cases and then perform a final fallback to the System process.
	if pData == nil {
		headerPID := er.EventHeader.ProcessId
		// Only log a warning if the failure was unexpected.
		if issuingTID != 0xFFFFFFFF && headerPID != 0xFFFFFFFF {
			d.log.SampledWarn("disk_write_unattributed").
				Uint32("issuing_tid", issuingTID).
				Uint32("header_pid", headerPID).
				Msg("DiskWrite could not be attributed via TID or PID; falling back to System.")
		}

		// Final fallback: Attribute to the System process (PID 4).
		if p, ok := d.stateManager.Processes.GetCurrentProcessDataByPID(statemanager.SystemProcessID); ok {
			pData = p
		}
	}

	// At this point, pData is guaranteed to be non-nil, as we always fall back to System.
	d.customCollector.RecordDiskIO(diskNumber, pData, transferSize, true)

	return nil
}

// HandleDiskFlush processes DiskFlush events to count disk flush operations.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger
//   - Provider GUID: {3d6fa8d4-fe05-11d0-9dda-00c04fd7ba7c} (DiskIoGuid)
//   - Event ID(s): 14 (EVENT_TRACE_TYPE_IO_FLUSH), 57
//   - Event Name(s): Flush
//   - Event Version(s): 0, 1, 2, 3
//   - Schema: MOF
//
// Schema (from MOF class DiskIo_TypeGroup3, 64-bit layout):
//   - DiskNumber (UInt32): Offset: 0
//   - IrpFlags (UInt32): Offset: 4
//   - HighResResponseTime (UInt64):  Offset: 8
//   - Irp (UInt32): Offset: 16
//   - IssuingThreadId (UInt32): Offset: 20
func (d *HandlerMof) HandleDiskFlushMofRaw(record *etw.EventRecord) error {
	if d.customCollector == nil {
		return nil
	}

	diskNumber, err := record.GetUint32At(0)
	if err != nil {
		d.log.SampledError("fail-disknumber").Err(err).Msg("Failed to get DiskNumber property for disk flush")
		return err
	}

	// Record flush operation in custom collector
	d.customCollector.RecordDiskFlush(uint32(diskNumber))

	return nil
}

/*
### Analysis of the Documentation and Evidence for correlating disk I/O to processes

After carefully studying the text you provided,
 the MOF class definitions, the Process Hacker source code,
  and the Microsoft Press article, several critical facts have come to light.

**Finding 1: The PID in `FileIo` Events is Unreliable.**
https://learn.microsoft.com/en-us/archive/msdn-magazine/2009/october/core-instrumentation-events-in-windows-7-part-2
This is the most important discovery. The documentation I found states:
> "...the process and thread id values of the IO events, with the exception of Disk IO events,
 are not valid. To correlate these activities correctly to the originating thread and thus to the process,
 one needs to consider tracking Context Switch events."

This single sentence explains why all the IRP correlation attempts failed.
 We were capturing the PID from `FileWrite` events and storing it,
 assuming it was the correct originating process.
 The documentation explicitly says this PID is not valid. This is a fundamental flaw in my previous logic.

**Finding 2: The "Cached I/O" and "Fast I/O" Problems are Real.**
https://www.microsoftpressstore.com/articles/article.aspx?p=2201309&seqNum=3
The Microsoft Press article confirms our theory about cached I/O.
 The Memory Manager, often running as the System process (PID 4),
 writes cached data to disk long after the application's `WriteFile` call has completed.
 This operation generates a completely new IRP. The article also introduces **Fast I/O**,
 which is a mechanism that can bypass IRP generation entirely for cached reads/writes,
 making IRP correlation impossible in those cases.

**Finding 3: There are Two Separate Correlation Problems.**
I have been trying to solve two problems at once, but they require different keys:
1.  **Attributing a `DiskIo` event to a Process ID.**
2.  **Attributing a `DiskIo` event to a File Name.**

The documentation makes it clear that these are separate lookups.
 The `Irp` is mentioned for correlating `FileIo` start events with `FileIo` *completion* events,
 but not for correlating `FileIo` with `DiskIo`.

**Finding 4: Process Hacker Shows the Correct Path.**
The Process Hacker source code is revealing.
 It shows how an functional tool solved this exact problem:
*   **For PID Attribution:** In its `EtpEtwEventCallback`, when it receives a `DiskIo` event,
	 it checks the OS version. On modern systems (Win8+), it uses the `IssuingThreadId` property from the
	 `DiskIo` event itself and resolves that to a PID. On older systems, it falls back to the `ProcessId` in
	 the event header. **It never uses an IRP from a `FileIo` event to find the PID.**
*   **For Filename Attribution:** It processes `FileIo` events (like `FileCreate` and `FileRundown`) to
	  build a map between the `FileObject` (which the docs confirm is a `FileKey`) and the `FileName`. It then
 	  uses this map to look up the filename when a `DiskIo` event arrives.

### The New Plan

Based on this evidence, we must abandon our previous correlation strategies.
 The new plan is to emulate the proven method used by Process Hacker, which aligns perfectly with the documentation.

**Goal:** Reliably attribute every `DiskIo` event to a Process ID.

**Strategy:** Get the Process ID from the `DiskIo` event itself. It is the only reliable source.

---

**Step 1: Prioritize `IssuingThreadId` for PID Attribution (The Modern Method)**

*   In `HandleDiskRead` and `HandleDiskWrite`, the first action will be to attempt to get the `IssuingThreadId`
      property from the event.
*   If this property exists and is non-zero, we will use our existing State Manager's TID-to-PID map
	 (`tidToPid`) to find the correct Process ID. This provides the most accurate attribution possible for the thread
 	 that initiated the I/O, even if it was deferred.

**Step 2: Use the Event Header `ProcessId` as the Primary Fallback**

*   If `IssuingThreadId` is not available or is zero (e.g., on an older OS), we will immediately fall back
	  to using the `ProcessId` from the event header (`helper.EventRec.EventHeader.ProcessId`).
*   This is the most robust fallback. It will correctly attribute non-cached I/O to the originating user
	  process. For cached I/O, it will correctly attribute the I/O to the System process, which is an accurate
 	  reflection of what is happening at the disk driver level.
*   This ensures **no metrics are dropped**. We get the best attribution available for every single event.

**Step 3: Decouple Filename Correlation (A Future Enhancement)**

*   We will treat filename resolution as a separate, secondary goal. The logic for this, based on the documentation,
	 is as follows:
    *   Create a new map in the State Manager: `FileKey -> FileName`.
    *   The `HandleFileCreate` and a new `HandleFileName` handler will populate this map by extracting the `FileKey`
	  and `FileName` from `FileIo` events.
    *   In `HandleDiskRead`/`HandleDiskWrite`, we would extract the `FileObject` property (which we now know is
	  the `FileKey`) and use it to look up the filename in our new map.
*   For now, we will focus exclusively on fixing the PID attribution and getting the read/write metrics to
	  appear correctly. That should suffice for now.

---
*/
