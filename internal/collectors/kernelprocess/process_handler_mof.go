package kernelprocess

import (
	"etw_exporter/internal/etw/guids"
	"etw_exporter/internal/etw/handlers"
	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"

	"github.com/tekert/goetw/etw"
	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// HandlerMof processes ETW process events and delegates to the KernelStateManager.
type HandlerMof struct {
	stateManager *statemanager.KernelStateManager
	log          *phusluadapter.SampledLogger
}

// NewProcessHandlerMof creates a new process handler instance.
func NewProcessHandlerMof(sm *statemanager.KernelStateManager) *HandlerMof {
	return &HandlerMof{
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
func (h *HandlerMof) RegisterRoutes(router handlers.Router) {

	processRoutes := map[uint8]handlers.EventHandlerFunc{
		etw.EVENT_TRACE_TYPE_START:    h.HandleProcessStart, // Process Start
		etw.EVENT_TRACE_TYPE_DC_START: h.HandleProcessStart, // Process Rundown open/trigger
		//etw.EVENT_TRACE_TYPE_DC_END:   h.HandleMofProcessStart, // Process Rundown on provider close
		etw.EVENT_TRACE_TYPE_END: h.HandleProcessEnd, // Process End
	}
	handlers.RegisterRoutesForGUID(router, guids.ProcessKernelGUID, processRoutes)

	h.log.Debug().Msg("Registered global process handler routes (mof)")
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
func (ph *HandlerMof) HandleProcessStart(helper *etw.EventRecordHelper) error {
	processID, _ := helper.GetPropertyUint("ProcessId")
	parentProcessID, _ := helper.GetPropertyUint("ParentId")
	processName, _ := helper.GetPropertyString("ImageFileName")
	sessionID, _ := helper.GetPropertyUint("SessionId")
	timestamp := helper.Timestamp()
	// The UniqueProcessKey is the kernel's EPROCESS address.
	uniqueProcessKey, _ := helper.GetPropertyUint("UniqueProcessKey")

	// Generate a synthetic StartKey from the event data alone
	// This works even if the process is already gone
	startKey := ph.stateManager.GenerateStartKey(uint32(processID), uint32(parentProcessID), uniqueProcessKey)
	if startKey == 0 {
		ph.log.Error().Uint32("pid", uint32(processID)).Msg("Failed to generate StartKey")
		return nil
	}

	ph.stateManager.Processes.AddProcess(
		uint32(processID),
		startKey,
		processName,
		uint32(parentProcessID),
		uint32(sessionID),
		timestamp,
	)
	return nil
}

// HandleProcessEnd processes kernel Process End events.
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
func (ph *HandlerMof) HandleProcessEnd(helper *etw.EventRecordHelper) error {
	processID, _ := helper.GetPropertyUint("ProcessId")
	pid := uint32(processID)

	parentProcessID, _ := helper.GetPropertyUint("ParentId")
	parentPID := uint32(parentProcessID)

	uniqueProcessKey, _ := helper.GetPropertyUint("UniqueProcessKey")
	timestamp := helper.Timestamp()

	startKey := ph.stateManager.GenerateStartKey(pid, parentPID, uniqueProcessKey)
	if startKey == 0 {
		ph.log.Error().Uint32("pid", pid).Msg("Failed to generate StartKey")
		return nil
	}

	// Mark the process for deletion using its unique start key.
	ph.stateManager.Processes.MarkProcessForDeletion(pid, startKey, timestamp)

	return nil
}
