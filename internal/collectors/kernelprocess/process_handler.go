package kernelprocess

import (
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

// HandleProcessStart processes ProcessStart events to track process creation.
// This handler is crucial for maintaining the process list in the KernelStateManager,
// which is used by all other collectors to enrich their metrics with process names.
//
// ETW Event Details:
//   - Provider Name: Microsoft-Windows-Kernel-Process
//   - Provider GUID: {22fb2cd6-0e7b-422b-a0c7-2fad1fd0e716}
//   - Event ID(s): 1
//   - Event Name(s): ProcessStart
//   - Event Version(s): 0-3
//   - Schema: Manifest (XML)
//
// Schema (Version 2, from manifest template ProcessStartArgs_V2):
//   - ProcessID (win:UInt32): Process identifier.
//   - CreateTime (win:FILETIME): Process creation time.
//   - ParentProcessID (win:UInt32): Parent process identifier.
//   - SessionID (win:UInt32): Session identifier.
//   - Flags (win:UInt32): Process flags.
//   - ImageName (win:UnicodeString): Process image name (full path).
//   - ImageChecksum (win:UInt32): The checksum of the process image.
//   - TimeDateStamp (win:UInt32): The time and date stamp of the process image.
//   - PackageFullName (win:UnicodeString): The full name of the package, if applicable.
//   - PackageRelativeAppId (win:UnicodeString): The relative application ID within the package.
//
// Note: The code currently extracts fields common to all versions for robust compatibility.
func (ph *Handler) HandleProcessStart(helper *etw.EventRecordHelper) error {
	processID, _ := helper.GetPropertyUint("ProcessID")
	parentProcessID, _ := helper.GetPropertyUint("ParentProcessID")
	processName, _ := helper.GetPropertyString("ImageName")
	timestamp := helper.Timestamp()
	startKey, _ := helper.EventRec.ExtProcessStartKey()

	ph.stateManager.AddProcess(uint32(processID), startKey, processName, uint32(parentProcessID), timestamp)
	return nil
}

// HandleProcessRundown processes ProcessRundown events, which are essential for
// capturing the state of processes that were already running when the ETW session started.
// Like ProcessStart, it adds the process to the KernelStateManager.
//
// ETW Event Details:
//   - Provider Name: Microsoft-Windows-Kernel-Process
//   - Provider GUID: {22fb2cd6-0e7b-422b-a0c7-2fad1fd0e716}
//   - Event ID(s): 15
//   - Event Name(s): ProcessRundown (also known as DCStart)
//   - Event Version(s): 0-1
//   - Schema: Manifest (XML)
//
// Schema (Version 1, from manifest template ProcessRundownArgs_V1):
//   - ProcessID (win:UInt32): Process identifier.
//   - ProcessSequenceNumber (win:UInt64): A sequence number that uniquely identifies a process.
//   - CreateTime (win:FILETIME): Process creation time.
//   - ParentProcessID (win:UInt32): Parent process identifier.
//   - ParentProcessSequenceNumber (win:UInt64): The sequence number of the parent process.
//   - SessionID (win:UInt32): Session identifier.
//   - Flags (win:UInt32): Process flags.
//   - ProcessTokenElevationType (win:UInt32): The type of token elevation.
//   - ProcessTokenIsElevated (win:UInt32): Indicates if the process token is elevated.
//   - MandatoryLabel (win:SID): The mandatory label of the process.
//   - ImageName (win:UnicodeString): Process image name (full path).
//   - ImageChecksum (win:UInt32): The checksum of the process image.
//   - TimeDateStamp (win:UInt32): The time and date stamp of the process image.
//   - PackageFullName (win:UnicodeString): The full name of the package, if applicable.
//   - PackageRelativeAppId (win:UnicodeString): The relative application ID within the package.
//
// Note: Without these events, processes that started before tracing would show
// as "unknown_*" in metrics.
func (ph *Handler) HandleProcessRundown(helper *etw.EventRecordHelper) error {
	processID, _ := helper.GetPropertyUint("ProcessID")
	parentProcessID, _ := helper.GetPropertyUint("ParentProcessID")
	processName, _ := helper.GetPropertyString("ImageName")
	timestamp := helper.Timestamp()
	startKey, _ := helper.EventRec.ExtProcessStartKey()

	ph.stateManager.AddProcess(uint32(processID), startKey, processName, uint32(parentProcessID), timestamp)
	return nil
}

// HandleProcessEnd processes ProcessStop events to mark processes for cleanup.
// When a process terminates, this handler informs the KernelStateManager to mark
// the process for deletion. The actual deletion is delayed until after the next
// Prometheus scrape to prevent data loss for in-flight metrics.
//
// ETW Event Details:
//   - Provider Name: Microsoft-Windows-Kernel-Process
//   - Provider GUID: {22fb2cd6-0e7b-422b-a0c7-2fad1fd0e716}
//   - Event ID(s): 2
//   - Event Name(s): ProcessStop
//   - Event Version(s): 0-2
//   - Schema: Manifest (XML)
//
// Schema (Version 2, from manifest template ProcessStopArgs_V2):
//   - ProcessID (win:UInt32): Process identifier.
//   - ProcessSequenceNumber (win:UInt64): A sequence number that uniquely identifies a process.
//   - CreateTime (win:FILETIME): Process creation time.
//   - ExitTime (win:FILETIME): Process exit time.
//   - ExitCode (win:UInt32): Process exit code.
//   - TokenElevationType (win:UInt32): The type of token elevation.
//   - HandleCount (win:UInt32): The number of open handles.
//   - CommitCharge (win:UInt64): The commit charge for the process in bytes.
//   - CommitPeak (win:UInt64): The peak commit charge for the process in bytes.
//   - CPUCycleCount (win:UInt64): The number of CPU cycles consumed by the process.
//   - ReadOperationCount (win:UInt32): The number of read operations performed.
//   - WriteOperationCount (win:UInt32): The number of write operations performed.
//   - ReadTransferKiloBytes (win:UInt32): The number of kilobytes read.
//   - WriteTransferKiloBytes (win:UInt32): The number of kilobytes written.
//   - HardFaultCount (win:UInt32): The number of hard page faults.
//   - ImageName (win:AnsiString): Process image name (full path).
//
// Note: Newer versions (v1+) include performance counters like CPUCycleCount, HardFaultCount, etc.
func (ph *Handler) HandleProcessEnd(helper *etw.EventRecordHelper) error {
	processID, _ := helper.GetPropertyUint("ProcessID")
	pid := uint32(processID)

	// Mark the process for deletion. The state manager will handle cascading this
	// to the process's threads during the post-scrape cleanup.
	ph.stateManager.MarkProcessForDeletion(pid)

	return nil
}
