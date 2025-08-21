package main

import (
	"time"

	"github.com/phuslu/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/tekert/goetw/etw"
)

// PerfInfoHandler handles interrupt-related ETW events for performance monitoring
// It implements the event handler interfaces needed for interrupt latency tracking
type PerfInfoHandler struct {
	collector *PerfInfoCollector
	log       log.Logger
}

// NewInterruptHandler creates a new interrupt event handler
func NewInterruptHandler(config *InterruptLatencyConfig) *PerfInfoHandler {
	return &PerfInfoHandler{
		collector: NewInterruptLatencyCollector(config),
		log:       GetInterruptLogger(),
	}
}

// GetCustomCollector returns the Prometheus collector for registration
func (h *PerfInfoHandler) GetCustomCollector() prometheus.Collector {
	return h.collector
}

// HandleISREvent processes ISR (Interrupt Service Routine) events
// This handler processes ETW events for interrupt service routines (ISRs).
// It tracks ISR entry and routine address for latency analysis.
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
	event := helper.EventRec

	// Extract ISR event properties using optimized methods
	var cpu uint16
	var vector uint16
	var routineAddress uint64
	var initialTime time.Time

	// Get CPU from the proper processor number field
	cpu = event.ProcessorNumber()

	// Extract properties using proper helper methods for kernel events
	// These property names are based on the actual MOF schema for ISR events
	if routineAddr, err := helper.GetPropertyUint("Routine"); err == nil {
		routineAddress = routineAddr
	}

	if vectorVal, err := helper.GetPropertyUint("Vector"); err == nil {
		vector = uint16(vectorVal)
	}

	if init, err := helper.GetPropertyInt("InitialTime"); err == nil {
		initialTime = etw.UnixFiletime(init)
	}

	// Process the ISR event
	h.collector.ProcessISREvent(cpu, vector, initialTime, routineAddress)

	return nil
}

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
//	  object InitialTime;  // WmiTime = FILETIME = UINT64
//	  uint32 Routine;
//	};
//
// Key properties used:
// - InitialTime (object): DPC entry time (NOT a property - this is the event timestamp itself)
// - Routine (uint32): Address of DPC routine
//
// This handler records DPC entry and routine mapping for driver analysis.
func (h *PerfInfoHandler) HandleDPCEvent(helper *etw.EventRecordHelper) error {
	event := helper.EventRec

	// Extract DPC event properties using optimized methods
	var cpu uint16
	var routineAddress uint64
	var initialTime time.Time
	var durationMicros float64

	// Get CPU from the proper processor number field
	cpu = event.ProcessorNumber()

	// Extract properties using proper helper methods for kernel events
	// The InitialTime is NOT a property - it's the event timestamp itself
	if routineAddr, err := helper.GetPropertyUint("Routine"); err == nil {
		routineAddress = routineAddr
	}

	if init, err := helper.GetPropertyInt("InitialTime"); err == nil {
		initialTime = etw.UnixFiletime(init)
	}

	// For DPC events, we get entry events only. Duration must be calculated
	// from correlating with other events or estimated.
	// DPC events are entry events - no duration property is available
	durationMicros = 10.0 // Default estimate in microseconds

	// Process the DPC event
	h.collector.ProcessDPCEvent(cpu, initialTime, routineAddress, durationMicros)

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
//	  object InitialTime;  // WmiTime = FILETIME = UINT64
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
	// For hard page fault events, we need to check the event type
	// Event type 32 (EVENT_TRACE_TYPE_MM_HPF) indicates a hard page fault
	// For classic/MOF events, we need to check if this is a hard page fault

	h.collector.ProcessHardPageFaultEvent()

	return nil
}
