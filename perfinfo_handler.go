package main

import (
	"sync"
	"time"

	"github.com/phuslu/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/tekert/goetw/etw"
)

// pendingDPCInfo holds the data for a DPC event that is waiting for the next
// DPC on the same CPU to determine its duration.
type pendingDPCInfo struct {
	initialTime    time.Time
	routineAddress uint64
}

// PerfInfoHandler handles interrupt-related ETW events for performance monitoring
// It implements the event handler interfaces needed for interrupt latency tracking
type PerfInfoHandler struct {
	collector *PerfInfoInterruptCollector
	config    *PerfInfoConfig
	log       log.Logger

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
func NewPerfInfoHandler(config *PerfInfoConfig) *PerfInfoHandler {
	return &PerfInfoHandler{
		collector:     NewPerfInfoCollector(config),
		config:        config,
		log:           GetPerfinfoLogger(),
		lastDPCPerCPU: make(map[uint16]pendingDPCInfo),
	}
}

// GetCustomCollector returns the Prometheus collector for registration
func (h *PerfInfoHandler) GetCustomCollector() prometheus.Collector {
	return h.collector
}

// finalizeAndClearPendingDPC checks for a pending/previous DPC on a given CPU, processes it
// if one exists, and clears it from the pending map. The mutex must be held by the caller.
func (h *PerfInfoHandler) finalizeAndClearPendingDPC(cpu uint16, endTime time.Time) {
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

// HandleISREvent processes ISR (Interrupt Service Routine) events
// This handler processes ETW events for interrupt service routines (ISRs).
// / It tracks ISR entry and routine address for latency analysis.
//
// ETW Event Details:
// - Provider: PerfInfo
// - GUID: {ce1dbfb4-137e-4da6-87b0-3f59aa102cbc}
// - Event Type: 67 (ISR)
// - Event Class: ISR : PerfInfo
//
// MOF Class Definition:
// [EventType{67}, EventTypeName{"ISR"}]
//
//	class ISR : PerfInfo {
//	  object InitialTime; // WmiTime = FILETIME = UINT64
//	  uint32 Routine;
//	  uint8  ReturnValue;
//	  uint8  Vector;
//	  uint16 Reserved;
//	};
//
// Key properties used:
// - InitialTime (object): ISR entry time
// - Routine (uint32): Address of ISR routine
// - Vector (uint8): Interrupt vector number
//
// This handler records ISR entry and routine mapping for driver analysis.
func (h *PerfInfoHandler) HandleISREvent(helper *etw.EventRecordHelper) error {
	cpu := helper.EventRec.ProcessorNumber()

	routineAddress, err := helper.GetPropertyUint("Routine")
	if err != nil {
		return err
	}

	vector, err := helper.GetPropertyUint("Vector")
	if err != nil {
		return err
	}

	initialTimeVal, err := helper.GetPropertyInt("InitialTime")
	if err != nil {
		return err
	}
	initialTime := helper.TimestampFrom(initialTimeVal)

	// Process the ISR event
	h.collector.ProcessISREvent(cpu, uint16(vector), initialTime, routineAddress)

	return nil
}

var times int64 // ! TESTING log lib crash

// HandleDPCEvent processes DPC (Deferred Procedure Call) events
// This handler processes ETW events for deferred procedure calls (DPCs).
// It tracks DPC entry and routine address for latency analysis.
//
// ETW Event Details:
// - Provider: PerfInfo
// - GUID: {ce1dbfb4-137e-4da6-87b0-3f59aa102cbc}
// - Event Types: 66, 68, 69 (ThreadDPC, DPC, TimerDPC)
// - Event Class: DPC : PerfInfo
//
// MOF Class Definition:
// [EventType{66, 68, 69}, EventTypeName{"ThreadDPC", "DPC", "TimerDPC"}]
//
//	class DPC : PerfInfo {
//	  object InitialTime;   // WmiTime = This changed based on Wnode.ClientContext (Tested)
//	  uint32 Routine;
//	};
//
// Key properties used:
// - InitialTime (object): DPC entry time (NOT a property - this is the event timestamp itself)
// - Routine (uint32): Address of DPC routine
//
// This handler records DPC entry and routine mapping for driver analysis.
func (h *PerfInfoHandler) HandleDPCEvent(helper *etw.EventRecordHelper) error {
	cpu := helper.EventRec.ProcessorNumber()
	eventTime := helper.Timestamp()

	routineAddress, err := helper.GetPropertyUint("Routine")
	if err != nil {
		return nil // Cannot process without routine address.
	}

	// NOTE: this will be in QPC is ClientContext is 1, SystemTime if 2, and CPUTick if 3
	// Is usally ~30 nanoseconds earlier than eventTime
	initialTimeTicks, err := helper.GetPropertyInt("InitialTime")
	if err != nil {
		return nil // Cannot process without a timestamp
	}
	// This prop will be in QPC is ClientContext is 1, SystemTime if 2, and CPUTick if 3
	// We must use the dedicated TimestampFrom converter, which calculates the absolute
	// time based on the session's BootTime.
	initialTime := helper.TimestampFrom(initialTimeTicks)

	// ! TESTING crash
	// //initialTime := etw.FromFiletime(initialTimeTicks)
	// fromq := helper.FromQPC(initialTimeTicks)
	// times++
	// if times%10 == 0 {
	// 	log.Info().Int64("initialTimeTicks", initialTimeTicks).Msg("TEST")
	// 	log.Info().Int64("EventRec.Timestamp", helper.EventRec.EventHeader.TimeStamp).Msg("TEST")
	// 	log.Info().Time("event_time", eventTime).Msg("TEST")
	// 	log.Info().Time("initialTime", initialTime).Msg("TEST")
	// 	log.Info().Time("fromq", fromq).Msg("TEST")
	// 	os.Exit(1)
	// }

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
// When a context switch occurs on a CPU, it signifies the end of any pending DPC
// execution on that core. This handler calculates the duration of the last DPC
// in a chain.
//
// ETW Event Details:
// - Provider: NT Kernel Logger (CSwitch)
// - GUID: {3d6fa8d1-fe05-11d0-9dda-00c04fd7ba7c}
// - Event Type: 36
// - Event Class: CSwitch
//
// The CSwitch event marks the point where the scheduler switches from one thread
// to another. Since DPCs run at a high IRQL, they must complete before the
// scheduler can run and perform a context switch. Therefore, the timestamp of a
// CSwitch event on a given CPU provides a reliable end time for any DPC that
// was running just before it.
func (h *PerfInfoHandler) HandleContextSwitch(helper *etw.EventRecordHelper) error {
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

// HandleReadyThread is a stub implementation to satisfy the ThreadNtEventHandler interface.
func (h *PerfInfoHandler) HandleReadyThread(helper *etw.EventRecordHelper) error {
	return nil // PerfInfoHandler does not process this event.
}

// HandleThreadStart is a stub implementation to satisfy the ThreadNtEventHandler interface.
func (h *PerfInfoHandler) HandleThreadStart(helper *etw.EventRecordHelper) error {
	return nil // PerfInfoHandler does not process this event.
}

// HandleThreadEnd is a stub implementation to satisfy the ThreadNtEventHandler interface.
func (h *PerfInfoHandler) HandleThreadEnd(helper *etw.EventRecordHelper) error {
	return nil // PerfInfoHandler does not process this event.
}

// HandleContextSwitchRaw processes context switch events directly from the EVENT_RECORD.
// This is a high-performance "fast path" that reads directly from the UserData
// buffer using known offsets, bypassing the creation of an EventRecordHelper and
// the associated parsing overhead.
//
// CSwitch MOF Layout (V2-V4):
// - NewThreadId (uint32): offset 0
// - OldThreadId (uint32): offset 4
// - NewThreadPriority (int8): offset 8
// - OldThreadPriority (int8): offset 9
// - PreviousCState (uint8): offset 10
// - SpareByte (int8): offset 11
// - OldThreadWaitReason (int8): offset 12
// - OldThreadWaitMode (int8): offset 13
// - OldThreadState (int8): offset 14
// - OldThreadWaitIdealProcessor (int8): offset 15
// - NewThreadWaitTime (uint32): offset 16
// - Reserved (uint32): offset 20
func (h *PerfInfoHandler) HandleContextSwitchRaw(er *etw.EventRecord) error {
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

// HandleImageLoadEvent processes image load events for driver mapping
// This handler processes ETW events for image load/unload and rundown.
// It maps driver and module addresses for routine resolution.
//
// ETW Event Details:
// - Provider: Image
// - GUID: {2cb15d1d-5fc1-11d2-abe1-00a0c911f518}
// - Event Types: 10 (Load), 2 (Unload), 3 (DCStart), 4 (DCEnd)
// - Event Class: Image_Load : Image
//
// MOF Class Definition:
// [EventType(10, 2, 3, 4), EventTypeName("Load", "Unload", "DCStart", "DCEnd")]
//
//	class Image_Load : Image {
//	  uint32 ImageBase;
//	  uint32 ImageSize;
//	  uint32 ProcessId;
//	  uint32 ImageChecksum;
//	  uint32 TimeDateStamp;
//	  uint32 Reserved0;
//	  uint32 DefaultBase;
//	  uint32 Reserved1;
//	  uint32 Reserved2;
//	  uint32 Reserved3;
//	  uint32 Reserved4;
//	  string FileName;
//	};
//
// Key properties used:
// - ImageBase (uint32): Base address of loaded image
// - ImageSize (uint32): Size of loaded image
// - FileName (string): Full path to image file
//
// This handler records image mapping for routine address resolution.
func (h *PerfInfoHandler) HandleImageLoadEvent(helper *etw.EventRecordHelper) error {
	// Extract image load properties using optimized methods
	var imageBase uint64
	var imageSize uint64
	var fileName string

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

	// Process the image load event
	h.collector.ProcessImageLoadEvent(imageBase, imageSize, fileName)

	return nil
}

// HandleImageUnloadEvent processes image unload events to remove driver metrics.
func (h *PerfInfoHandler) HandleImageUnloadEvent(helper *etw.EventRecordHelper) error {
	var imageBase uint64
	if base, err := helper.GetPropertyUint("ImageBase"); err == nil {
		imageBase = base
	} else {
		return nil // Cannot process without image base
	}

	h.collector.ProcessImageUnloadEvent(imageBase)
	return nil
}

// HandleSampleProfileEvent processes SampledProfile events for SMI gap detection.
func (h *PerfInfoHandler) HandleSampleProfileEvent(helper *etw.EventRecordHelper) error {
	return nil
}

// HandleHardPageFaultEvent processes page fault events for hard page fault counting
// This handler processes ETW events for hard page faults.
// It counts and records hard page fault occurrences for memory analysis.
//
// ETW Event Details:
// - Provider: PageFault
// - GUID: {2cb15d1d-5fc1-11d2-abe1-00a0c911f518}
// - Event Type: 32 (HardFault)
// - Event Class: PageFault_HardFault : PageFault_V2
//
// MOF Class Definition:
// [EventType{32}, EventTypeName{"HardFault"}]
//
//	class PageFault_HardFault : PageFault_V2 {
//	  object InitialTime;  // WmiTime = UINT64 = FILETIME if Wnode.ClientContext == 1
//	  uint64 ReadOffset;
//	  uint32 VirtualAddress;
//	  uint32 FileObject;
//	  uint32 TThreadId;
//	  uint32 ByteCount;
//	};
//
// Key properties used:
// - InitialTime (object): Time stamp of page fault
// - TThreadId (uint32): Thread ID encountering the fault
// - ByteCount (uint32): Amount of data read
//
// This handler counts hard page faults for memory performance metrics.
func (h *PerfInfoHandler) HandleHardPageFaultEvent(helper *etw.EventRecordHelper) error {

	h.collector.ProcessHardPageFaultEvent()

	return nil
}
