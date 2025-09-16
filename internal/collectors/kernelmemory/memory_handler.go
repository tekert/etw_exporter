package kernelmemory

import (
	"etw_exporter/internal/config"
	"etw_exporter/internal/etw/guids"
	"etw_exporter/internal/etw/handlers"
	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/tekert/goetw/etw"
	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// Handler processes ETW memory events and delegates to the memory collector.
type Handler struct {
	collector    *MemCollector
	stateManager *statemanager.KernelStateManager
	log          *phusluadapter.SampledLogger
}

// NewMemoryHandler creates a new memory handler instance.
func NewMemoryHandler(config *config.MemoryConfig,
	stateManager *statemanager.KernelStateManager) *Handler {

	return &Handler{
		collector:    nil, // Will be set via AttachCollector
		stateManager: stateManager,
		log:          logger.NewSampledLoggerCtx("memory_handler"),
	}
}

// AttachCollector allows a metrics collector to subscribe to the handler's events.
func (c *Handler) AttachCollector(collector *MemCollector) {
	c.log.Debug().Msg("Attaching metrics collector to memory handler.")
	c.collector = collector
}

// RegisterRoutes tells the EventHandler which ETW events this handler is interested in.
func (h *Handler) RegisterRoutes(router handlers.Router) {
	// Provider: NT Kernel Logger (PageFault) ({3d6fa8d3-fe05-11d0-9dda-00c04fd7ba7c})
	// Provider: System Interrupt Provider (Win11+) ({9e814aad-3204-11d2-9a82-006008a86939})
	memoryRoutes := map[uint8]handlers.EventHandlerFunc{
		32: h.HandleMofHardPageFaultEvent, // HardFault
	}
	handlers.RegisterRoutesForGUID(router, guids.PageFaultKernelGUID, memoryRoutes)
	handlers.RegisterRoutesForGUID(router, etw.SystemMemoryProviderGuid, memoryRoutes)

	h.log.Debug().Msg("Memory collector enabled and routes registered")
}

// GetCustomCollector returns the underlying custom collector for Prometheus registration.
func (h *Handler) GetCustomCollector() prometheus.Collector {
	return h.collector
}

// HandleMofHardPageFaultEvent processes hard page fault events from the NT Kernel Logger (MOF schema).
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (PageFault)
//   - Provider GUID: {3d6fa8d3-fe05-11d0-9dda-00c04fd7ba7c}
//   - Event ID(s): 32 (Opcode)
//   - Event Name(s): HardFault
//   - Event Version(s): 2
//   - Schema: MOF
//
// Schema (from gen_mof_kerneldef.go):
//   - InitialTime (object): Timestamp of the page fault.
//   - ReadOffset (uint64): Read offset in the file.
//   - VirtualAddress (pointer): Virtual address that caused the fault.
//   - FileObject (pointer): Pointer to the file object.
//   - TThreadId (uint32): Thread ID that encountered the fault.
//   - ByteCount (uint32): Amount of data read.
//
// This handler increments a counter for each hard page fault. It resolves the
// Thread ID to a Process ID to attribute the fault correctly.
func (h *Handler) HandleMofHardPageFaultEvent(helper *etw.EventRecordHelper) error {
	threadID, err := helper.GetPropertyUint("TThreadId")
	if err != nil {
		h.log.SampledWarn("pagefault_tid_error").Err(err).Msg("Failed to get thread ID from HardFault event")
		return err
	}

	// First, try to resolve the PID from the TID. This is the most reliable method.
	pid, isKnown := h.stateManager.GetProcessIDForThread(uint32(threadID))

	// If the TID is not known (due to event ordering races), fall back to the PID in the event header.
	if !isKnown {
		// We cannot attribute this event.
		h.log.SampledWarn("pagefault_pid_error").
			Uint32("tid", uint32(threadID)).
			Msg("Could not resolve PID for thread and fallback PID is not a known process")
		return nil
	}

	// Get the start key for the resolved PID to ensure correct attribution.
	startKey, ok := h.stateManager.GetProcessStartKey(pid)
	if !ok {
		// Cannot attribute if we don't have a start key.
		return nil
	}

	h.collector.ProcessHardPageFaultEvent(startKey)
	return nil
}

// HandleManifestHardPageFaultEvent is a placeholder for a hypothetical manifest-based provider.
// It demonstrates how a new provider for the same logical event would be handled.
//
// ETW Event Details:
//   - Provider Name: Microsoft-Windows-Kernel-Memory
//   - Provider GUID: TODO
//   - Event ID(s): TODO
//   - Schema: Manifest (XML)
//
// Schema (Hypothetical):
//
//	// TODO
func (h *Handler) HandleManifestHardPageFaultEvent(helper *etw.EventRecordHelper) error {
	// Manifest events often provide the PID directly, simplifying the logic.
	// pid, err := helper.GetPropertyUint("ProcessID")
	// if err != nil {
	// 	h.log.SampledWarn("pagefault_pid_error").Err(err).Msg("Failed to get ProcessID from manifest HardFault event")
	// 	return err
	// }

	// h.collector.ProcessHardPageFaultEvent(uint32(pid))
	return nil
}
