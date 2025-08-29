package kernelmemory

import (
	"etw_exporter/internal/collectors/kernelthread/threadmapping"
	"etw_exporter/internal/config"
	"etw_exporter/internal/logger"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/tekert/goetw/etw"
	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// MemoryHandler processes ETW memory events and delegates to the memory collector.
type MemoryHandler struct {
	collector     *MemoryCollector
	threadMapping *threadmapping.ThreadMapping
	log           *phusluadapter.SampledLogger
}

// NewMemoryHandler creates a new memory handler instance.
func NewMemoryHandler(config *config.MemoryConfig,
	threadMapping *threadmapping.ThreadMapping) *MemoryHandler {

	return &MemoryHandler{
		collector:     NewMemoryCollector(config),
		threadMapping: threadMapping,
		log:           logger.NewSampledLoggerCtx("memory_handler"),
	}
}

// GetCustomCollector returns the underlying custom collector for Prometheus registration.
func (h *MemoryHandler) GetCustomCollector() prometheus.Collector {
	return h.collector
}

// HandleHardPageFaultEvent processes hard page fault events to count memory faults.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (PageFault)
//   - Provider GUID: {3d6fa8d1-fe05-11d0-9dda-00c04fd7ba7c}
//   - Event ID(s): 32
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
func (h *MemoryHandler) HandleHardPageFaultEvent(helper *etw.EventRecordHelper) error {
	threadID, err := helper.GetPropertyUint("TThreadId")
	if err != nil {
		h.log.SampledWarn("pagefault_tid_error").Err(err).Msg("Failed to get thread ID from HardFault event")
		return err
	}

	// Resolve Thread ID to Process ID using the global thread handler.
	// This is reliable because the 'memory' provider group now enables the THREAD kernel flag.
	pid, isKnown := h.threadMapping.GetProcessID(uint32(threadID))
	if !isKnown {
		h.log.Debug().Uint32("tid", uint32(threadID)).Msg("Could not resolve PID for thread causing page fault")
		return nil // Cannot attribute the fault without a known PID.
	}

	h.collector.ProcessHardPageFaultEvent(pid)
	return nil
}
