package kernelperf

import (
	"sync"
	"time"

	"etw_exporter/internal/config"
	"etw_exporter/internal/etw/guids"
	"etw_exporter/internal/etw/handlers"
	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"

	"github.com/tekert/goetw/etw"
	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// pendingDPCInfo holds the data for a DPC event that is waiting for the next
// DPC on the same CPU to determine its duration.
type pendingDPCInfo struct {
	initialTime    time.Time
	routineAddress uint64
}

// Handler handles interrupt-related ETW events for performance monitoring
// It implements the event handler interfaces needed for interrupt latency tracking
type Handler struct {
	collector    *PerfCollector
	config       *config.PerfInfoConfig
	stateManager *statemanager.KernelStateManager
	log          *phusluadapter.SampledLogger

	// lastDPCPerCPU tracks the last seen DPC event for each CPU. It is the core
	// of our DPC duration calculation logic.
	//
	// The duration of a DPC is the time from its start until the start of the
	// next significant event on the same CPU core. In the Windows kernel, DPCs
	// on a single core are serialized. Therefore, the next significant event
	// will either be:
	//  1. The next DPC event: Its start time marks the end of the previous one.
	//  2. A Context Switch (CSwitch) event: This signifies that the DPC queue
	//     for that core was empty and the scheduler is now running.
	//
	// This map is keyed by the CPU number from the event header. The logic is
	// thread-safe, protected by dpcMutex, as events from the different sessions can be
	// are executed by different goroutines. "Whatever comes first" (the next DPC
	// or a CSwitch) on a given CPU correctly finalizes the duration of the
	// pending DPC.
	lastDPCPerCPU map[uint16]pendingDPCInfo
	dpcMutex      sync.Mutex
}

// NewPerfInfoHandler creates a new interrupt event handler
func NewPerfInfoHandler(config *config.PerfInfoConfig, sm *statemanager.KernelStateManager) *Handler {
	return &Handler{
		stateManager:  sm,
		collector:     nil, // Collector is attached later via AttachCollector
		config:        config,
		log:           logger.NewSampledLoggerCtx("perfinfo_handler"),
		lastDPCPerCPU: make(map[uint16]pendingDPCInfo),
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
	perfInfoRoutes := map[uint8]handlers.EventHandlerFunc{
		67: h.HandleISREvent,           // ISR
		66: h.HandleDPCEvent,           // ThreadDPC
		68: h.HandleDPCEvent,           // DPC
		69: h.HandleDPCEvent,           // TimerDPC
		46: h.HandleSampleProfileEvent, // SampleProfile
	}
	handlers.RegisterRoutesForGUID(router, guids.PerfInfoKernelGUID, perfInfoRoutes)
	handlers.RegisterRoutesForGUID(router, etw.SystemInterruptProviderGuid, perfInfoRoutes) // Win11+

	// TODO: move these to image handler? make guid internal var?
	// Provider: NT Kernel Logger (Image) ({2cb15d1d-5fc1-11d2-abe1-00a0c911f518})
	// Provider: System Process Provider ({151f55dc-467d-471f-83b5-5f889d46ff66}) - Windows 11+
	imageRoutes := map[uint8]handlers.EventHandlerFunc{
		etw.EVENT_TRACE_TYPE_LOAD:     h.HandleImageLoadEvent,   // Image Load
		etw.EVENT_TRACE_TYPE_DC_START: h.HandleImageLoadEvent,   // Image Rundown
		etw.EVENT_TRACE_TYPE_DC_END:   h.HandleImageLoadEvent,   // Image Rundown End
		etw.EVENT_TRACE_TYPE_END:      h.HandleImageUnloadEvent, // Image Unload
	}
	handlers.RegisterRoutesForGUID(router, guids.ImageKernelGUID, imageRoutes)
	handlers.RegisterRoutesForGUID(router, etw.SystemProcessProviderGuid, imageRoutes) // Win11+

	// PerfInfo also needs CSwitch events to finalize DPC durations. This is handled
	// in the raw EventRecordCallback, which calls perfinfoHandler.HandleContextSwitchRaw.
	h.log.Debug().Msg("PerfInfo collector enabled and routes registered")
}

// GetCustomCollector returns the Prometheus collector for registration
func (h *Handler) GetCustomCollector() *PerfCollector {
	return h.collector
}

// finalizeAndClearPendingDPC checks for a pending/previous DPC on a given CPU, processes it
// if one exists, and clears it from the pending map. The mutex must be held by the caller.
func (h *Handler) finalizeAndClearPendingDPC(cpu uint16, endTime time.Time) {
	if lastDPC, exists := h.lastDPCPerCPU[cpu]; exists {
		// Calculate the duration from the DPC start to the provided end time.
		durationMicros := float64(endTime.Sub(lastDPC.initialTime).Microseconds())

		// Process the completed DPC event now that we have its duration.
		h.collector.ProcessDPCEvent(cpu, lastDPC.initialTime, lastDPC.routineAddress, durationMicros)

		// The DPC is no longer pending and its duration has been recorded.
		// We must remove it from the map to prevent it from being processed again.
		delete(h.lastDPCPerCPU, cpu)
	}
}

// HandleISREvent processes Interrupt Service Routine (ISR) events to track interrupt latency.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (PerfInfo)
//   - Provider GUID: {ce1dbfb4-137e-4da6-87b0-3f59aa102cbc}
//   - Event ID(s): 67
//   - Event Name(s): ISR
//   - Event Version(s): 2
//   - Schema: MOF
//
// Schema (from gen_mof_kerneldef.go):
//   - InitialTime (object): ISR entry time. The format depends on the session's ClientContext.
//   - Routine (uint32): Address of the ISR routine.
//   - ReturnValue (uint8): Value returned by the ISR.
//   - Vector (uint8): Interrupt vector number.
//   - Reserved (uint16): Reserved field.
//
// This handler records ISR entry time and routine address for driver latency analysis.
func (h *Handler) HandleISREvent(helper *etw.EventRecordHelper) error {
	if h.collector == nil {
		return nil
	}

	cpu := helper.EventRec.ProcessorNumber()

	routineAddress, err := helper.GetPropertyUint("Routine")
	if err != nil {
		return err
	}

	vector, err := helper.GetPropertyUint("Vector")
	if err != nil {
		return err
	}

	initialTime, err := helper.GetPropertyWmiTime("InitialTime")
	if err != nil {
		return err
	}

	// Process the ISR event
	h.collector.ProcessISREvent(cpu, uint16(vector), initialTime, routineAddress)

	return nil
}

// HandleDPCEvent processes Deferred Procedure Call (DPC) events to track DPC latency.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (PerfInfo)
//   - Provider GUID: {ce1dbfb4-137e-4da6-87b0-3f59aa102cbc}
//   - Event ID(s): 66, 68, 69
//   - Event Name(s): ThreadDPC, DPC, TimerDPC
//   - Event Version(s): 2
//   - Schema: MOF
//
// Schema (from gen_mof_kerneldef.go):
//   - InitialTime (object): DPC entry time.
//   - Routine (uint32): Address of the DPC routine.
//
// This handler tracks the start of a DPC. The duration is calculated later when the
// next DPC or a context switch occurs on the same CPU. The InitialTime property's
// clock source (QPC, SystemTime, CPUTick) depends on the session's ClientContext.
func (h *Handler) HandleDPCEvent(helper *etw.EventRecordHelper) error {
	if h.collector == nil {
		return nil
	}

	cpu := helper.EventRec.ProcessorNumber()
	eventTime := helper.Timestamp()

	routineAddress, err := helper.GetPropertyUint("Routine")
	if err != nil {
		return nil // Cannot process without routine address.
	}

	// NOTE: This prop will be in QPC is ClientContext is 1, SystemTime if 2, and CPUTick if 3
	// We must use the dedicated GetPropertyWmiTime, which calculates the absolute
	// time based on the trace ClockType.
	// InitialTime is usally ~30 nanoseconds earlier than event generated TimeStamp
	initialTime, err := helper.GetPropertyWmiTime("InitialTime")
	if err != nil {
		return nil // Cannot process without a timestamp
	}

	// The eventTime is the most accurate timestamp for when the DPC *started*.
	// We use this for latency calculations.
	h.dpcMutex.Lock()
	defer h.dpcMutex.Unlock()

	// A new DPC event marks the end of any previously pending DPC on the same CPU.
	// We use the current event's timestamp as the end time for the previous one.
	// If there was no previous DPC (e.g., after a context switch), this function does nothing.
	h.finalizeAndClearPendingDPC(cpu, eventTime)

	// The current DPC is now stored as the new "pending" event. It is NOT lost.
	// Its duration will be calculated and it will be counted when the *next*
	// DPC or a context switch occurs on this CPU.
	h.lastDPCPerCPU[cpu] = pendingDPCInfo{
		initialTime:    initialTime, // Use the property's timestamp as the start time.
		routineAddress: routineAddress,
	}

	return nil
}

// HandleContextSwitch processes context switch events to finalize DPC durations.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (CSwitch)
//   - Provider GUID: {3d6fa8d1-fe05-11d0-9dda-00c04fd7ba7c}
//   - Event ID(s): 36
//   - Event Name(s): CSwitch
//   - Event Version(s): 2, 3, 4
//   - Schema: MOF
//
// Schema (from gen_mof_kerneldef.go, V2-V4):
//   - NewThreadId (uint32): Thread ID being switched to.
//   - OldThreadId (uint32): Thread ID being switched from.
//   - NewThreadPriority (int8): Priority of the incoming thread.
//   - OldThreadPriority (int8): Priority of the outgoing thread.
//   - PreviousCState (uint8): Previous C-state of the processor.
//   - SpareByte (int8): Reserved/spare byte.
//   - OldThreadWaitReason (int8): Why the old thread was waiting.
//   - OldThreadWaitMode (int8): Wait mode of the old thread.
//   - OldThreadState (int8): State of the old thread.
//   - OldThreadWaitIdealProcessor (int8): Ideal processor for the old thread.
//   - NewThreadWaitTime (uint32): Wait time for the new thread.
//   - Reserved (uint32): Reserved field.
//
// When a context switch occurs on a CPU, it signifies the end of any pending DPC
// execution on that core. Since DPCs run at a high IRQL, they must complete before
// the scheduler can run. Therefore, the timestamp of a CSwitch event provides a
// reliable end time for any DPC that was running just before it.
func (h *Handler) HandleContextSwitch(helper *etw.EventRecordHelper) error {
	if h.collector == nil {
		return nil
	}

	// The only properties we need from the CSwitch event are its timestamp and
	// the CPU it occurred on, which are available in the event header.
	cpu := helper.EventRec.ProcessorNumber()
	eventTime := helper.Timestamp()

	h.dpcMutex.Lock()
	defer h.dpcMutex.Unlock()

	// A context switch signifies that the DPC queue is idle, marking the end
	// of any pending DPC on that CPU.
	h.finalizeAndClearPendingDPC(cpu, eventTime)

	return nil
}

// HandleContextSwitchRaw processes context switch events to finalize DPC durations.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (CSwitch)
//   - Provider GUID: {3d6fa8d1-fe05-11d0-9dda-00c04fd7ba7c}
//   - Event ID(s): 36
//   - Event Name(s): CSwitch
//   - Event Version(s): 2, 3, 4
//   - Schema: MOF
//
// Schema (from gen_mof_kerneldef.go, V2-V4):
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
//
// This is a high-performance "fast path" that reads directly from the UserData
// buffer using known offsets, bypassing parsing overhead. It uses the event's
// timestamp to finalize the duration of a pending DPC on the same CPU.
func (h *Handler) HandleContextSwitchRaw(er *etw.EventRecord) error {
	if h.collector == nil {
		return nil
	}

	// The only properties we need from the CSwitch event are its timestamp and
	// the CPU it occurred on, which are available in the event header.
	cpu := er.ProcessorNumber()
	eventTime := er.Timestamp()

	h.dpcMutex.Lock()
	defer h.dpcMutex.Unlock()

	// A context switch signifies that the DPC queue is idle, marking the end
	// of any pending DPC on that CPU.
	h.finalizeAndClearPendingDPC(cpu, eventTime)

	return nil
}

// HandleImageLoadEvent processes image load events to map routine addresses to drivers.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (Image)
//   - Provider GUID: {2cb15d1d-5fc1-11d2-abe1-00a0c911f518}
//   - Event ID(s): 10, 3, 4
//   - Event Name(s): Load, DCStart, DCEnd
//   - Event Version(s): 2, 3
//   - Schema: MOF
//
// Schema (from gen_mof_kerneldef.go):
//   - ImageBase (pointer): Base address of the loaded image.
//   - ImageSize (uint32): Size of the loaded image in bytes.
//   - ProcessId (uint32): ID of the process loading the image.
//   - ImageChecksum (uint32): The checksum of the image.
//   - TimeDateStamp (uint32): The timestamp the image was loaded (0 most of the time)
//   - Reserved0 (uint32): Reserved.
//   - DefaultBase (pointer): The default base address of the image.
//   - Reserved1 (uint32): Reserved.
//   - Reserved2 (uint32): Reserved.
//   - Reserved3 (uint32): Reserved.
//   - Reserved4 (uint32): Reserved.
//   - FileName (string): Full path to the image file.
//
// This handler collects information about loaded modules (drivers, executables, DLLs)
// to resolve routine addresses from DPC/ISR events to a driver name.
func (h *Handler) HandleImageLoadEvent(helper *etw.EventRecordHelper) error {
	// Extract image load properties using optimized methods
	var imageBase uint64
	var imageSize uint64
	var fileName string
	var processID uint32
	var timeDateStamp uint32
	var imageChecksum  uint32

	// Extract properties using proper helper methods
	if base, err := helper.GetPropertyUint("ImageBase"); err == nil {
		imageBase = base
	}
	if size, err := helper.GetPropertyUint("ImageSize"); err == nil {
		imageSize = size
	}
	if name, err := helper.GetPropertyString("FileName"); err == nil {
		fileName = name
	}
	if pid, err := helper.GetPropertyUint("ProcessId"); err == nil {
		processID = uint32(pid)
	}
	if ts, err := helper.GetPropertyUint("TimeDateStamp"); err == nil {
		timeDateStamp = uint32(ts)
	}
	if ts, err := helper.GetPropertyUint("ImageChecksum"); err == nil {
		imageChecksum = uint32(ts)
	}

	// if processID == 34916 {
	// 	h.log.Debug().
	// 		Uint32("pid", processID).
	// 		Uint64("base", imageBase).
	// 		Uint64("size", imageSize).
	// 		Str("file", fileName).
	// 		Uint32("timestamp", timeDateStamp).
	// 		Msg("Image Load event for executable")
	// }

	// The state manager's AddImage method now contains all logic for storing the image,
	// reference counting, and triggering process enrichment for main executables.
	h.stateManager.AddImage(processID, imageBase, imageSize, fileName, timeDateStamp, imageChecksum)

	return nil
}

// HandleImageUnloadEvent processes image unload events to remove driver mappings.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (Image)
//   - Provider GUID: {2cb15d1d-5fc1-11d2-abe1-00a0c911f518}
//   - Event ID(s): 2
//   - Event Name(s): Unload
//   - Event Version(s): 2, 3
//   - Schema: MOF
//
// Schema (from gen_mof_kerneldef.go):
//   - ImageBase (pointer): Base address of the unloaded image.
//   - ImageSize (uint32): Size of the unloaded image in bytes.
//   - ProcessId (uint32): ID of the process unloading the image.
//   - ImageChecksum (uint32): The checksum of the image.
//   - TimeDateStamp (uint32): The timestamp from the image header.
//   - Reserved0 (uint32): Reserved.
//   - DefaultBase (pointer): The default base address of the image.
//   - Reserved1 (uint32): Reserved.
//   - Reserved2 (uint32): Reserved.
//   - Reserved3 (uint32): Reserved.
//   - Reserved4 (uint32): Reserved.
//   - FileName (string): Full path to the image file.
//
// This handler removes the address-to-driver mapping when a module is unloaded
// to prevent stale data.
func (h *Handler) HandleImageUnloadEvent(helper *etw.EventRecordHelper) error {
	var imageBase uint64
	if base, err := helper.GetPropertyUint("ImageBase"); err == nil {
		imageBase = base
	} else {
		return nil // Cannot process without image base
	}

	// Notify the collector first so it can clear its caches before the state is gone.
	// The collector needs to do this synchronously to prevent using stale cache entries.
	if h.collector != nil {
		h.collector.ProcessImageUnloadEvent(imageBase)
	}

	// Mark the image for deletion in the central state manager.
	// The actual removal will happen post-scrape, ensuring that any in-flight
	// events can still resolve addresses to this image.
	//h.stateManager.MarkImageForDeletion(imageBase)

	// Decrement the image's reference count. The state manager will mark it for
	// deletion if the count reaches zero.
	h.stateManager.UnloadImage(imageBase)

	return nil
}

// HandleSampleProfileEvent processes SampledProfile events for SMI gap detection.
func (h *Handler) HandleSampleProfileEvent(helper *etw.EventRecordHelper) error {
	return nil
}
