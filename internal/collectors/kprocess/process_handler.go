package kprocess

import (
	"etw_exporter/internal/logger"

	"github.com/tekert/goetw/etw"
	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// ProcessHandler processes ETW process events and delegates to the process collector
type ProcessHandler struct {
	processCollector *ProcessCollector
	log              *phusluadapter.SampledLogger
}

// NewProcessHandler creates a new process handler instance
func NewProcessHandler() *ProcessHandler {
	return &ProcessHandler{
		processCollector: GetGlobalProcessCollector(),
		log:              logger.NewSampledLoggerCtx("process_handler"),
	}
}

// HandleProcessStart processes process start and rundown events for name tracking
// Process Event: Microsoft-Windows-Kernel-Process
// Provider GUID: {22fb2cd6-0e7b-422b-a0c7-2fad1fd0e716}
// Event ID: 1 (ProcessStart) - New process creation
// Event ID: 15 (ProcessRundown/DCStart) - Existing processes when tracing starts
//
// Properties from manifest (ProcessStartArgs template):
//
//	ProcessID (win:UInt32) - Process identifier
//	CreateTime (win:FILETIME) - Process creation time
//	ParentProcessID (win:UInt32) - Parent process identifier
//	SessionID (win:UInt32) - Session identifier
//	ImageName (win:UnicodeString) - Process image name
//
// Note: ProcessRundown events are crucial for capturing process names of processes
// that were already running when ETW tracing started. Without these events,
// processes that started before tracing would show as "unknown_*" in metrics.
func (ph *ProcessHandler) HandleProcessStart(helper *etw.EventRecordHelper) error {
	processID, _ := helper.GetPropertyUint("ProcessID")
	parentProcessID, _ := helper.GetPropertyUint("ParentProcessID")

	// Try to get process name from different possible property names
	var processName string
	if name, err := helper.GetPropertyString("ImageName"); err == nil {
		processName = name
		ph.log.Trace().
			Uint32("pid", uint32(processID)).
			Str("property", "ImageName").
			Str("name", processName).
			Msg("Retrieved process name from ImageName")
	} else {
		processName = "unknown"
		ph.log.SampledWarn("unknown_process").
			Uint32("pid", uint32(processID)).
			Err(err).
			Msg("Failed to get process name from ImageName property")
	}

	// use the timestamp of the event as LastSeen
	timestamp := helper.Timestamp()

	ph.processCollector.AddProcess(uint32(processID), processName, uint32(parentProcessID), timestamp)
	return nil
}

// HandleProcessEnd processes process end events to clean up the collector
// Process Event: Microsoft-Windows-Kernel-Process
// Provider GUID: {22fb2cd6-0e7b-422b-a0c7-2fad1fd0e716}
// Event ID: 2 (ProcessStop)
//
// Properties from manifest (ProcessStopArgs template):
//
//	ProcessID (win:UInt32) - Process identifier
//	CreateTime (win:FILETIME) - Process creation time
//	ExitTime (win:FILETIME) - Process exit time
//	ExitCode (win:UInt32) - Process exit code
//	ImageName (win:AnsiString) - Process image name
func (ph *ProcessHandler) HandleProcessEnd(helper *etw.EventRecordHelper) error {
	processID, _ := helper.GetPropertyUint("ProcessID")

	ph.processCollector.log.Trace().
		Uint32("pid", uint32(processID)).
		Msg("Process end event received")

	ph.processCollector.RemoveProcess(uint32(processID))
	return nil
}
