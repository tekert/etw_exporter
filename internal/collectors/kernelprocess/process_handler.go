package kernelprocess

import (
	"etw_exporter/internal/etw/guids"
	"etw_exporter/internal/etw/handlers"
	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"

	"github.com/tekert/goetw/etw"
	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// Handler processes ETW process events and delegates to the KernelStateManager.
type Handler struct {
	stateManager *statemanager.KernelStateManager
	log          *phusluadapter.SampledLogger
}

// NewProcessHandler creates a new process handler instance.
func NewProcessHandler(sm *statemanager.KernelStateManager) *Handler {
	return &Handler{
		stateManager: sm,
		log:          logger.NewSampledLoggerCtx("process_handler"),
	}
}

// // AttachCollector allows a metrics collector to subscribe to the handler's events.
// func (c *Handler) AttachCollector(collector *ProcessCollector) {
// 	c.log.Debug().Msg("Attaching metrics collector to process handler.")
// 	c.collector = collector
// }

// RegisterRoutes tells the EventHandler which ETW events this handler is interested in.
func (h *Handler) RegisterRoutes(router handlers.Router) {

	// // --- Manifest-based Provider Routes ---
	// // Process Events
	// router.AddRoute(*guids.MicrosoftWindowsKernelProcessGUID, 1, h.HandleProcessStart)    // ProcessStart
	// router.AddRoute(*guids.MicrosoftWindowsKernelProcessGUID, 15, h.HandleProcessRundown) // ProcessRundown
	// router.AddRoute(*guids.MicrosoftWindowsKernelProcessGUID, 2, h.HandleProcessEnd)      // ProcessStop

	// Thread Events (for single-session correlation testing)
	// router.AddRawRoute(*guids.MicrosoftWindowsKernelProcessGUID, 3, h.HandleMofThreadStart) // ThreadStart
	// router.AddRawRoute(*guids.MicrosoftWindowsKernelProcessGUID, 4, h.HandleMofThreadEnd)   // ThreadStop

	processRoutes := map[uint8]handlers.EventHandlerFunc{
		etw.EVENT_TRACE_TYPE_START:    h.HandleMofProcessStart, // Process Start
		etw.EVENT_TRACE_TYPE_DC_START: h.HandleMofProcessStart, // Process Rundown open/trigger
		etw.EVENT_TRACE_TYPE_DC_END:   h.HandleMofProcessStart, // Process Rundown close
		etw.EVENT_TRACE_TYPE_END:      h.HandleMofProcessEnd,   // Process End
	}
	handlers.RegisterRoutesForGUID(router, guids.ProcessKernelGUID, processRoutes)

	h.log.Debug().Msg("Registered global process handler routes (manifest)")
}

// HandleProcessStartRaw processes kernel Process Start and Rundown events.
// This raw handler processes MOF-based events from the NT Kernel Logger, which is
// faster than manifest-based parsing and ensures process events are ordered correctly
// with other kernel events like thread starts.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger
//   - Provider GUID: {3d6fa8d0-fe05-11d0-9dda-00c04fd7ba7c}
//   - Event ID(s): Opcode 1 (Start), Opcode 3 (DCStart/Rundown)
//   - Event Name(s): Process_V2_TypeGroup1
//   - Event Version(s): 2 (and compatible versions)
//   - Schema: MOF
//
// Schema (V3):
//   - UniqueProcessKey (pointer): The kernel address of the EPROCESS structure, used as a start key.
//   - ProcessId (uint32): Process identifier.
//   - ParentId (uint32): Parent process identifier.
//   - SessionId (uint32): Session identifier.
//   - ExitStatus (sint32): The exit code of the process (used in End events).
//   - UserSID (pointer): Pointer to the user's SID.
//   - ImageFileName (string): The process image name (8-bit ANSI string).
//   - CommandLine (string): The command line used to start the process.
func (ph *Handler) HandleMofProcessStart(helper *etw.EventRecordHelper) error {
	processID, _ := helper.GetPropertyUint("ProcessId")
	parentProcessID, _ := helper.GetPropertyUint("ParentId")
	processName, _ := helper.GetPropertyString("ImageFileName")
	sessionID, _ := helper.GetPropertyUint("SessionId")
	timestamp := helper.Timestamp()

	// The UniqueProcessKey is the kernel's EPROCESS address. It's our most reliable
	// fallback identifier if API-based enrichment fails.
	uniqueProcessKey, _ := helper.GetPropertyUint("UniqueProcessKey")

	ph.stateManager.AddProcess(
		uint32(processID),
		uniqueProcessKey,
		processName,
		uint32(parentProcessID),
		uint32(sessionID),
		timestamp,
	)
	return nil
}

// HandleMofProcessEnd processes kernel Process End events.
// This raw handler processes MOF-based events from the NT Kernel Logger.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger
//   - Provider GUID: {3d6fa8d0-fe05-11d0-9dda-00c04fd7ba7c}
//   - Event ID(s): Opcode 2 (End)
//   - Event Name(s): Process_V2_TypeGroup1
//   - Event Version(s): 2 (and compatible versions)
//   - Schema: MOF
//
// Schema (V2): See HandleMofProcessStart.
func (ph *Handler) HandleMofProcessEnd(helper *etw.EventRecordHelper) error {
	processID, _ := helper.GetPropertyUint("ProcessId")
	pid := uint32(processID)

	parentProcessID, _ := helper.GetPropertyUint("ParentId")
	parentPID := uint32(parentProcessID)

	uniqueProcessKey, _ := helper.GetPropertyUint("UniqueProcessKey")
	timestamp := helper.Timestamp()

	// Mark the process for deletion using its unique start key.
	ph.stateManager.MarkProcessForDeletion(pid, parentPID, uniqueProcessKey, timestamp)

	return nil
}

// // HandleProcessStart processes ProcessStart events to track process creation.
// // This handler is crucial for maintaining the process list in the KernelStateManager,
// // which is used by all other collectors to enrich their metrics with process names.
// //
// // ETW Event Details:
// //   - Provider Name: Microsoft-Windows-Kernel-Process
// //   - Provider GUID: {22fb2cd6-0e7b-422b-a0c7-2fad1fd0e716}
// //   - Event ID(s): 1
// //   - Event Name(s): ProcessStart
// //   - Event Version(s): 0-3
// //   - Schema: Manifest (XML)
// //
// // Schema (Version 2, from manifest template ProcessStartArgs_V2):
// //   - ProcessID (win:UInt32): Process identifier.
// //   - CreateTime (win:FILETIME): Process creation time.
// //   - ParentProcessID (win:UInt32): Parent process identifier.
// //   - SessionID (win:UInt32): Session identifier.
// //   - Flags (win:UInt32): Process flags.
// //   - ImageName (win:UnicodeString): Process image name (full path).
// //   - ImageChecksum (win:UInt32): The checksum of the process image.
// //   - TimeDateStamp (win:UInt32): The time and date stamp of the process image.
// //   - PackageFullName (win:UnicodeString): The full name of the package, if applicable.
// //   - PackageRelativeAppId (win:UnicodeString): The relative application ID within the package.
// //
// // Note: The code currently extracts fields common to all versions for robust compatibility.
// func (ph *Handler) HandleProcessStart(helper *etw.EventRecordHelper) error {
// 	processID, _ := helper.GetPropertyUint("ProcessID")
// 	parentProcessID, _ := helper.GetPropertyUint("ParentProcessID")
// 	processName, _ := helper.GetPropertyString("ImageName")
// 	sessionID, _ := helper.GetPropertyUint("SessionID")
// 	imageChecksum, _ := helper.GetPropertyUint("ImageChecksum")
// 	createTime, _ := helper.GetPropertyFileTime("CreateTime")

// 	timestamp := helper.Timestamp()
// 	//startKey, _ := helper.EventRec.ExtProcessStartKey()
// 	// For manifest events, the StartKey is the ProcessSequenceNumber.
// 	startKey, _ := helper.GetPropertyUint("ProcessSequenceNumber")

// 	if imageChecksum == 0 {
// 		ph.log.Trace().
// 			Uint32("pid", uint32(processID)).
// 			Str("name", processName).
// 			Msg("Process started without image checksum.")
// 	}

// 	ph.stateManager.AddProcess(
// 		uint32(processID),
// 		startKey,
// 		processName,
// 		uint32(parentProcessID),
// 		uint32(sessionID),
// 		uint32(imageChecksum),
// 		createTime,
// 		timestamp,
// 	)
// 	return nil
// }

// // HandleProcessRundown processes ProcessRundown events, which are essential for
// // capturing the state of processes that were already running when the ETW session started.
// // Like ProcessStart, it adds the process to the KernelStateManager.
// //
// // ETW Event Details:
// //   - Provider Name: Microsoft-Windows-Kernel-Process
// //   - Provider GUID: {22fb2cd6-0e7b-422b-a0c7-2fad1fd0e716}
// //   - Event ID(s): 15
// //   - Event Name(s): ProcessRundown (also known as DCStart)
// //   - Event Version(s): 0-1
// //   - Schema: Manifest (XML)
// //
// // Schema (Version 1, from manifest template ProcessRundownArgs_V1):
// //   - ProcessID (win:UInt32): Process identifier.
// //   - ProcessSequenceNumber (win:UInt64): A sequence number that uniquely identifies a process.
// //   - CreateTime (win:FILETIME): Process creation time.
// //   - ParentProcessID (win:UInt32): Parent process identifier.
// //   - ParentProcessSequenceNumber (win:UInt64): The sequence number of the parent process.
// //   - SessionID (win:UInt32): Session identifier.
// //   - Flags (win:UInt32): Process flags.
// //   - ProcessTokenElevationType (win:UInt32): The type of token elevation.
// //   - ProcessTokenIsElevated (win:UInt32): Indicates if the process token is elevated.
// //   - MandatoryLabel (win:SID): The mandatory label of the process.
// //   - ImageName (win:UnicodeString): Process image name (full path).
// //   - ImageChecksum (win:UInt32): The checksum of the process image.
// //   - TimeDateStamp (win:UInt32): The time and date stamp of the process image.
// //   - PackageFullName (win:UnicodeString): The full name of the package, if applicable.
// //   - PackageRelativeAppId (win:UnicodeString): The relative application ID within the package.
// //
// // Note: Without these events, processes that started before tracing would show
// // as "unknown_*" in metrics.
// func (ph *Handler) HandleProcessRundown(helper *etw.EventRecordHelper) error {
// 	processID, _ := helper.GetPropertyUint("ProcessID")
// 	parentProcessID, _ := helper.GetPropertyUint("ParentProcessID")
// 	processName, _ := helper.GetPropertyString("ImageName")
// 	sessionID, _ := helper.GetPropertyUint("SessionID")
// 	imageChecksum, _ := helper.GetPropertyUint("ImageChecksum")
// 	createTime, _ := helper.GetPropertyFileTime("CreateTime")

// 	timestamp := helper.Timestamp()
// 	//startKey, _ := helper.EventRec.ExtProcessStartKey()
// 	// For manifest events, the StartKey is the ProcessSequenceNumber.
// 	startKey, _ := helper.GetPropertyUint("ProcessSequenceNumber")

// 	if imageChecksum == 0 {
// 		ph.log.Trace().
// 			Uint32("pid", uint32(processID)).
// 			Str("name", processName).
// 			Msg("Process Rundown without image checksum.")
// 	}

// 	ph.stateManager.AddProcess(
// 		uint32(processID),
// 		startKey,
// 		processName,
// 		uint32(parentProcessID),
// 		uint32(sessionID),
// 		uint32(imageChecksum),
// 		createTime,
// 		timestamp)
// 	return nil
// }

// // HandleProcessEnd processes ProcessStop events to mark processes for cleanup.
// // When a process terminates, this handler informs the KernelStateManager to mark
// // the process for deletion. The actual deletion is delayed until after the next
// // Prometheus scrape to prevent data loss for in-flight metrics.
// //
// // ETW Event Details:
// //   - Provider Name: Microsoft-Windows-Kernel-Process
// //   - Provider GUID: {22fb2cd6-0e7b-422b-a0c7-2fad1fd0e716}
// //   - Event ID(s): 2
// //   - Event Name(s): ProcessStop
// //   - Event Version(s): 0-2
// //   - Schema: Manifest (XML)
// //
// // Schema (Version 2, from manifest template ProcessStopArgs_V2):
// //   - ProcessID (win:UInt32): Process identifier.
// //   - ProcessSequenceNumber (win:UInt64): A sequence number that uniquely identifies a process.
// //   - CreateTime (win:FILETIME): Process creation time.
// //   - ExitTime (win:FILETIME): Process exit time.
// //   - ExitCode (win:UInt32): Process exit code.
// //   - TokenElevationType (win:UInt32): The type of token elevation.
// //   - HandleCount (win:UInt32): The number of open handles.
// //   - CommitCharge (win:UInt64): The commit charge for the process in bytes.
// //   - CommitPeak (win:UInt64): The peak commit charge for the process in bytes.
// //   - CPUCycleCount (win:UInt64): The number of CPU cycles consumed by the process.
// //   - ReadOperationCount (win:UInt32): The number of read operations performed.
// //   - WriteOperationCount (win:UInt32): The number of write operations performed.
// //   - ReadTransferKiloBytes (win:UInt32): The number of kilobytes read.
// //   - WriteTransferKiloBytes (win:UInt32): The number of kilobytes written.
// //   - HardFaultCount (win:UInt32): The number of hard page faults.
// //   - ImageName (win:AnsiString): Process image name (full path).
// //
// // Note: Newer versions (v1+) include performance counters like CPUCycleCount, HardFaultCount, etc.
// func (ph *Handler) HandleProcessEnd(helper *etw.EventRecordHelper) error {
// 	processID, _ := helper.GetPropertyUint("ProcessID")
// 	pid := uint32(processID)
// 	//startKey, _ := helper.EventRec.ExtProcessStartKey()
// 	// For manifest end events, the StartKey is the ProcessSequenceNumber.
// 	startKey, _ := helper.GetPropertyUint("ProcessSequenceNumber")

// 	// Mark the process for deletion. The state manager will handle cascading this
// 	// to the process's threads during the post-scrape cleanup.
// 	ph.stateManager.MarkProcessForDeletion(pid, startKey)

// 	return nil
// }
