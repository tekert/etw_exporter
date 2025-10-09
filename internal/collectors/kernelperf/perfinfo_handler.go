package kernelperf

import (
	"etw_exporter/internal/config"
	"etw_exporter/internal/etw/guids"
	"etw_exporter/internal/etw/handlers"
	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"

	"github.com/tekert/goetw/etw"
	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// Handler handles interrupt-related ETW events for performance monitoring.
// It implements the event handler interfaces needed for interrupt latency tracking.
type Handler struct {
	collector    *PerfCollector
	config       *config.PerfInfoConfig
	stateManager *statemanager.KernelStateManager
	log          *phusluadapter.SampledLogger
}

// NewPerfInfoHandler creates a new interrupt event handler
func NewPerfInfoHandler(config *config.PerfInfoConfig, sm *statemanager.KernelStateManager) *Handler {
	return &Handler{
		stateManager: sm,
		collector:    nil, // Collector is attached later via AttachCollector
		config:       config,
		log:          logger.NewSampledLoggerCtx("perfinfo_handler"),
	}
}

// AttachCollector allows a metrics collector to subscribe to the handler's events.
func (c *Handler) AttachCollector(collector *PerfCollector) {
	c.log.Debug().Msg("Attaching metrics collector to perfinfo handler.")
	c.collector = collector
}

// RegisterRoutes tells the EventHandler which ETW events this handler is interested in.
func (h *Handler) RegisterRoutes(router handlers.Router) {
	// Provider: NT Kernel Logger (PerfInfo) ({ce1dbfb4-137e-4da6-87b0-3f59aa102cbc})
	// Provider: System Interrupt Provider ({9e814aad-3204-11d2-9a82-006008a86939}) - Windows 11+
	perfInfoRoutes := map[uint8]handlers.RawEventHandlerFunc{
		67: h.HandleISREventRaw,           // ISR
		66: h.HandleDPCEventRaw,           // ThreadDPC
		68: h.HandleDPCEventRaw,           // DPC
		69: h.HandleDPCEventRaw,           // TimerDPC
		46: h.HandleSampleProfileEventRaw, // SampleProfile
	}
	handlers.RegisterRawRoutesForGUID(router, guids.PerfInfoKernelGUID, perfInfoRoutes)
	handlers.RegisterRawRoutesForGUID(router, etw.SystemInterruptProviderGuid, perfInfoRoutes) // Win11+

	// PerfInfo also needs CSwitch events to finalize DPC durations. This is handled
	// in the raw EventRecordCallback, which calls perfinfoHandler.HandleContextSwitchRaw.
	h.log.Debug().Msg("PerfInfo collector enabled and routes registered")
}

// GetCustomCollector returns the Prometheus collector for registration
func (h *Handler) GetCustomCollector() *PerfCollector {
	return h.collector
}

// HandleISREventRaw processes ISR events to track interrupt latency.
// This handler records ISR entry time and routine address for driver latency analysis.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (PerfInfo)
//   - Provider GUID: {ce1dbfb4-137e-4da6-87b0-3f59aa102cbc}
//   - Event ID(s): 67, 95
//   - Event Name(s): ISR
//   - Event Version(s): 2
//   - Schema: MOF
//
// Schema (PerfInfo_V2 ISR 64bit):
//   - InitialTime (int64): ISR entry time (The format depends on the session's ClientContext. ). [Offset: 0]
//   - Routine (pointer): Address of the ISR routine. [Offset: 8]
//   - ReturnValue (uInt8): Value returned by the ISR. [Offset: 16]
//   - Vector (uInt16): Interrupt vector number. [Offset: 17]
//   - Reserved (uInt8): Reserved field. [Offset: 19]
func (h *Handler) HandleISREventRaw(er *etw.EventRecord) error {
	if h.collector == nil {
		return nil
	}

	cpu := er.ProcessorNumber()

	routineAddress, err := er.GetUint64At(8)
	if err != nil {
		return err
	}

	vector, err := er.GetUint16At(17)
	if err != nil {
		return err
	}

	initialTime, err := er.GetWmiTimeAt(0)
	if err != nil {
		return err
	}

	// Process the ISR event
	h.collector.ProcessISREvent(cpu, vector, initialTime, routineAddress)

	return nil
}

// HandleDPCEventRaw processes DPC events to track DPC latency.
// This handler tracks the start of a DPC. The duration is calculated later when the
// next DPC or a context switch occurs on the same CPU.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (PerfInfo)
//   - Provider GUID: {ce1dbfb4-137e-4da6-87b0-3f59aa102cbc}
//   - Event ID(s): 66, 68, 69, 70
//   - Event Name(s): ThreadDPC, DPC, TimerDPC
//   - Event Version(s): 2
//   - Schema: MOF
//
// Schema (PerfInfo_V2 - DPC - 64bit):
//   - InitialTime (int64): DPC entry time. [Offset: 0]
//   - Routine (pointer): Address of the DPC routine. [Offset: 8]
//
// The InitialTime property's clock source (QPC, SystemTime, CPUTick) depends on the
// session's ClientContext. Using GetWmiTimeAt correctly handles this.
func (h *Handler) HandleDPCEventRaw(er *etw.EventRecord) error {
	if h.collector == nil {
		return nil
	}

	cpu := er.ProcessorNumber()
	eventTime := er.Timestamp()

	routineAddress, err := er.GetUint64At(8)
	if err != nil {
		return nil // Cannot process without routine address.
	}

	initialTime, err := er.GetWmiTimeAt(0)
	if err != nil {
		return nil // Cannot process without a timestamp
	}

	// The eventTime is the most accurate timestamp for when the DPC *started*.
	// We use this for latency calculations.
	h.collector.ProcessDPCEvent(cpu, eventTime, initialTime, routineAddress)

	return nil
}

// HandleContextSwitchRaw processes context switch events to finalize DPC durations.
// This is a high-performance "fast path" that reads directly from the UserData
// buffer using known offsets, bypassing parsing overhead. It uses the event's
// timestamp to finalize the duration of a pending DPC on the same CPU.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (CSwitch)
//   - Provider GUID: {3d6fa8d1-fe05-11d0-9dda-00c04fd7ba7c}
//   - Event ID(s): 36
//   - Event Name(s): CSwitch
//   - Event Version(s): 2, 3, 4
//   - Schema: MOF
//
// Schema (from MOF, V2-V4 64-bit):
//   - NewThreadId (uint32): Thread ID being switched to. [Offset: 0]
//   - OldThreadId (uint32): Thread ID being switched from. [Offset: 4]
//   - NewThreadPriority (int8): Priority of the incoming thread. [Offset: 8]
//   - OldThreadPriority (int8): Priority of the outgoing thread. [Offset: 9]
//   - PreviousCState (uint8): Previous C-state of the processor. [Offset: 10]
//   - SpareByte (int8): Reserved/spare byte. [Offset: 11]
//   - OldThreadWaitReason (int8): Why the old thread was waiting. [Offset: 12]
//   - OldThreadWaitMode (int8): Wait mode of the old thread. [Offset: 13]
//   - OldThreadState (int8): State of the old thread. [Offset: 14]
//   - OldThreadWaitIdealProcessor (int8): Ideal processor for the old thread. [Offset: 15]
//   - NewThreadWaitTime (uint32): Wait time for the new thread. [Offset: 16]
//   - Reserved (uint32): Reserved field. [Offset: 20]
func (h *Handler) HandleContextSwitchRaw(er *etw.EventRecord) error {
	if h.collector == nil {
		return nil
	}

	// The only properties we need from the CSwitch event are its timestamp and
	// the CPU it occurred on, which are available in the event header.
	cpu := er.ProcessorNumber()
	eventTime := er.Timestamp()

	// A context switch signifies that the DPC queue is idle, marking the end
	// of any pending DPC on that CPU.
	h.collector.ProcessCSwitchEvent(cpu, eventTime)

	return nil
}

// HandleSampleProfileEventRaw processes SampledProfile events for SMI gap detection.
func (h *Handler) HandleSampleProfileEventRaw(helper *etw.EventRecord) error {
	return nil
}
