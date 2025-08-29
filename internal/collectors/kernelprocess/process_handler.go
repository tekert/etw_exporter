package kernelprocess

import (
	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"

	"github.com/tekert/goetw/etw"
	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// ProcessHandler processes ETW process events and delegates to the KernelStateManager.
type ProcessHandler struct {
	stateManager *statemanager.KernelStateManager
	log          *phusluadapter.SampledLogger
}

// NewProcessHandler creates a new process handler instance.
func NewProcessHandler(sm *statemanager.KernelStateManager) *ProcessHandler {
	return &ProcessHandler{
		stateManager: sm,
		log:          logger.NewSampledLoggerCtx("process_handler"),
	}
}

// HandleProcessStart processes ProcessStart and ProcessRundown events to track process creation.
// This handler is crucial for maintaining the process list in the KernelStateManager,
// which is used by all other collectors to enrich their metrics with process names.
//
// ETW Event Details:
//   - Provider Name: Microsoft-Windows-Kernel-Process
//   - Provider GUID: {22fb2cd6-0e7b-422b-a0c7-2fad1fd0e716}
//   - Event ID(s): 1, 15
//   - Event Name(s): ProcessStart, ProcessRundown (also known as DCStart)
//   - Event Version(s): 3 (for ProcessStart), 2 (for ProcessRundown)
//   - Schema: Manifest (XML)
//
// Schema (from manifest, ProcessStartArgs template):
//   - ProcessID (win:UInt32): Process identifier.
//   - CreateTime (win:FILETIME): Process creation time.
//   - ParentProcessID (win:UInt32): Parent process identifier.
//   - SessionID (win:UInt32): Session identifier.
//   - ImageName (win:UnicodeString): Process image name (full path).
//
// Note: ProcessRundown events are crucial for capturing process names of processes
// that were already running when ETW tracing started. Without these events,
// processes that started before tracing would show as "unknown_*" in metrics.
func (ph *ProcessHandler) HandleProcessStart(helper *etw.EventRecordHelper) error {
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
//   - Event Version(s): 3
//   - Schema: Manifest (XML)
//
// Schema (from manifest, ProcessStopArgs template):
//   - ProcessID (win:UInt32): Process identifier.
//   - CreateTime (win:FILETIME): Process creation time.
//   - ExitTime (win:FILETIME): Process exit time.
//   - ExitCode (win:UInt32): Process exit code.
//   - ImageName (win:AnsiString): Process image name (often just the executable name).
func (ph *ProcessHandler) HandleProcessEnd(helper *etw.EventRecordHelper) error {
	processID, _ := helper.GetPropertyUint("ProcessID")
	pid := uint32(processID)

	// Mark the process for deletion. The state manager will handle cascading this
	// to the process's threads during the post-scrape cleanup.
	ph.stateManager.MarkProcessForDeletion(pid)

	return nil
}
