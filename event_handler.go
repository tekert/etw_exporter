package main

import (
	"github.com/phuslu/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/tekert/goetw/etw"
)

// Event handler interfaces for different event types
type DiskEventHandler interface {
	HandleDiskRead(helper *etw.EventRecordHelper) error
	HandleDiskWrite(helper *etw.EventRecordHelper) error
	HandleDiskFlush(helper *etw.EventRecordHelper) error
	HandleSystemConfigPhyDisk(helper *etw.EventRecordHelper) error // TODO: move to SystemConfigHandler
	HandleSystemConfigLogDisk(helper *etw.EventRecordHelper) error // TODO: move to SystemConfigHandler
}

type ProcessEventHandler interface {
	HandleProcessStart(helper *etw.EventRecordHelper) error
	HandleProcessEnd(helper *etw.EventRecordHelper) error
}

type ThreadNtEventHandler interface {
	HandleContextSwitch(helper *etw.EventRecordHelper) error
	HandleReadyThread(helper *etw.EventRecordHelper) error
	HandleThreadStart(helper *etw.EventRecordHelper) error
	HandleThreadEnd(helper *etw.EventRecordHelper) error
}

type FileEventHandler interface {
	HandleFileRead(helper *etw.EventRecordHelper) error
	HandleFileWrite(helper *etw.EventRecordHelper) error
	HandleFileCreate(helper *etw.EventRecordHelper) error
	HandleFileClose(helper *etw.EventRecordHelper) error
	HandleFileDelete(helper *etw.EventRecordHelper) error
}

type PerfInfoNtEventHandler interface {
	HandleISREvent(helper *etw.EventRecordHelper) error
	HandleDPCEvent(helper *etw.EventRecordHelper) error
	HandleImageLoadEvent(helper *etw.EventRecordHelper) error     // TODO: this should go to KernelImage handler.
	HandleHardPageFaultEvent(helper *etw.EventRecordHelper) error // TODO: this should go to KernelPageFault handler.
}

// EventHandler encapsulates state and logic for ETW event processing
// This struct-based approach allows reusability of eventID, helper, and other components
// for better code organization and future extensibility
type EventHandler struct {
	// Collectors for different metric categories
	diskHandler      *DiskIOHandler
	threadCSHandler  *ThreadHandler
	interruptHandler *PerfInfoHandler

	// Future collectors will be added here:
	// networkCollector *NetworkCollector
	// memoryCollector  *MemoryCollector
	// cpuCollector     *CPUCollector

	// Shared state and caches for callbacks
	metrics *ETWMetrics
	config  *CollectorConfig
	log     log.Logger // Event handler logger

	// Routing tables for different event types - hot path optimized
	diskEventHandlers    []DiskEventHandler
	processEventHandlers []ProcessEventHandler
	threadHandlers       []ThreadNtEventHandler
	fileEventHandlers    []FileEventHandler
	perfinfoHandlers     []PerfInfoNtEventHandler
}

// NewEventHandler creates a new handler with dependencies injected
func NewEventHandler(metrics *ETWMetrics, config *CollectorConfig) *EventHandler {
	handler := &EventHandler{
		metrics:              metrics,
		config:               config,
		log:                  GetEventLogger(),
		diskEventHandlers:    make([]DiskEventHandler, 0),
		processEventHandlers: make([]ProcessEventHandler, 0),
		threadHandlers:       make([]ThreadNtEventHandler, 0),
		fileEventHandlers:    make([]FileEventHandler, 0),
		perfinfoHandlers:     make([]PerfInfoNtEventHandler, 0),
	}

	// Always register the global process handler for process events
	// This ensures we have process name mappings available for all collectors
	processHandler := NewProcessHandler()
	handler.RegisterProcessEventHandler(processHandler)
	handler.log.Debug().Msg("Registered global process handler")

	// Initialize enabled collectors based on configuration
	if config.DiskIO.Enabled {
		handler.diskHandler = NewDiskIOHandler()
		// Register the disk handler with the event handler
		handler.RegisterDiskEventHandler(handler.diskHandler)
		// Register for file events for process correlation
		handler.RegisterFileEventHandler(handler.diskHandler)
		// Register the custom collector with Prometheus for high-performance metrics
		prometheus.MustRegister(handler.diskHandler.GetCustomCollector())

		handler.log.Info().Msg("Disk I/O collector enabled and registered with Prometheus")
	}

	if config.ThreadCS.Enabled {
		handler.threadCSHandler = NewThreadHandler()
		// Register the thread collector with the handler
		handler.RegisterThreadEventHandler(handler.threadCSHandler)
		// Register the custom collector with Prometheus for high-performance metrics
		prometheus.MustRegister(handler.threadCSHandler.GetCustomCollector())
		handler.log.Info().Msg("ThreadCS collector enabled and registered with Prometheus")
	}

	if config.PerfInfo.Enabled {
		handler.interruptHandler = NewInterruptHandler(&config.PerfInfo)
		// Register the interrupt handler with the event handler
		handler.RegisterInterruptEventHandler(handler.interruptHandler)
		// Register the custom collector with Prometheus for high-performance metrics
		prometheus.MustRegister(handler.interruptHandler.GetCustomCollector())
		handler.log.Info().Msg("Interrupt latency collector enabled and registered with Prometheus")
	}

	handler.log.Info().
		Int("disk_handlers", len(handler.diskEventHandlers)).
		Int("process_handlers", len(handler.processEventHandlers)).
		Int("thread_handlers", len(handler.threadHandlers)).
		Int("file_handlers", len(handler.fileEventHandlers)).
		Int("interrupt_handlers", len(handler.perfinfoHandlers)).
		Msg("Event handlers initialized")

	return handler
}

// Register methods for event handlers
func (h *EventHandler) RegisterDiskEventHandler(handler DiskEventHandler) {
	h.diskEventHandlers = append(h.diskEventHandlers, handler)
	h.log.Debug().Int("total_disk_handlers", len(h.diskEventHandlers)).Msg("Disk event handler registered")
}

func (h *EventHandler) RegisterProcessEventHandler(handler ProcessEventHandler) {
	h.processEventHandlers = append(h.processEventHandlers, handler)
	h.log.Debug().Int("total_process_handlers", len(h.processEventHandlers)).Msg("Process event handler registered")
}

func (h *EventHandler) RegisterThreadEventHandler(handler ThreadNtEventHandler) {
	h.threadHandlers = append(h.threadHandlers, handler)
	h.log.Debug().Int("total_thread_handlers", len(h.threadHandlers)).Msg("Thread event handler registered")
}

func (h *EventHandler) RegisterFileEventHandler(handler FileEventHandler) {
	h.fileEventHandlers = append(h.fileEventHandlers, handler)
	h.log.Debug().Int("total_file_handlers", len(h.fileEventHandlers)).Msg("File event handler registered")
}

func (h *EventHandler) RegisterInterruptEventHandler(handler PerfInfoNtEventHandler) {
	h.perfinfoHandlers = append(h.perfinfoHandlers, handler)
	h.log.Debug().Int("total_interrupt_handlers", len(h.perfinfoHandlers)).Msg("Interrupt event handler registered")
}

// ETW Callback Methods - these satisfy the ETW consumer callback interface
// Using struct methods allows us to share state and reduce parameter passing

// EventRecordCallback is called for each EVENT_RECORD
func (h *EventHandler) EventRecordCallback(record *etw.EventRecord) bool {
	// Perform any record-level processing here if needed
	// Return true to continue processing, false to stop

	// // From https://learn.microsoft.com/en-us/windows/win32/api/evntprov/ns-evntprov-event_descriptor
	// // Channel values below 16 are reserved for use by Microsoft to enable special treatment
	// // by the ETW runtime. Channel values 16 and above will be ignored by the ETW runtime
	// // (treated the same as channel 0) and can be given user-defined semantics.
	// //
	// // But we still receive these events, we will ignore them here too.
	// if record.EventHeader.EventDescriptor.Channel >= 16 {
	// 	h.log.Trace().
	// 		Uint8("channel", record.EventHeader.EventDescriptor.Channel).
	// 		Str("provider_guid", record.EventHeader.ProviderId.String()).
	// 		Uint16("event_id", record.EventHeader.EventDescriptor.Id).
	// 		Msg("Received event with reserved channel value, skipping processing")

	// 	return false // Skip non-applicable channels
	// }

	// TODO: if event is mof and we dont need it, we can skip it here.

	return true
}

// EventRecordHelperCallback is called as soon as the helper is created
func (h *EventHandler) EventRecordHelperCallback(helper *etw.EventRecordHelper) error {
	// Perform any helper-level processing here if needed
	return nil
}

// EventPreparedCallback is called before we parse the metadata of the event and all the properties
// This is where we route events to appropriate handlers based on provider GUID and event type
func (h *EventHandler) EventPreparedCallback(helper *etw.EventRecordHelper) error {
	defer helper.Skip() // Stop further processing

	// Use the integrated routing for scalable event distribution
	return h.RouteEvent(helper)
}

// RouteEvent routes events to appropriate handlers based on provider GUID and event type
// This is the hot path - optimized for performance
func (h *EventHandler) RouteEvent(helper *etw.EventRecordHelper) error {
	// Extract event information for routing
	eventRecord := helper.EventRec
	providerGUID := eventRecord.EventHeader.ProviderId
	var eventID uint16 = 0
	if helper.EventRec.IsMof() {
		eventID = uint16(eventRecord.EventHeader.EventDescriptor.Opcode)
	} else {
		eventID = eventRecord.EventHeader.EventDescriptor.Id
	}

	// Route SystemConfig events (available in both kernel and manifest modes)
	if providerGUID.Equals(SystemConfigGUID) {
		return h.routeSystemConfigEvents(helper, eventID)
	}

	// Route disk events from Microsoft-Windows-Kernel-Disk
	if providerGUID.Equals(MicrosoftWindowsKernelDiskGUID) && h.config.DiskIO.Enabled {
		return h.routeDiskEvents(helper, eventID)
	}

	// Route process events from Microsoft-Windows-Kernel-Process
	// Always route process events if we have handlers (needed for process name tracking)
	if providerGUID.Equals(MicrosoftWindowsKernelProcessGUID) && len(h.processEventHandlers) > 0 {
		return h.routeProcessEvents(helper, eventID)
	}

	// Route file events from Microsoft-Windows-Kernel-File
	if providerGUID.Equals(MicrosoftWindowsKernelFileGUID) && h.config.DiskIO.Enabled {
		return h.routeFileEvents(helper, eventID)
	}

	// Route thread events from Thread kernel provider
	if providerGUID.Equals(ThreadKernelGUID) && h.config.ThreadCS.Enabled {
		return h.routeThreadEvents(helper, eventID)
	}

	// Route interrupt latency events from kernel providers
	if h.config.PerfInfo.Enabled && len(h.perfinfoHandlers) > 0 {
		// Route PerfInfo events (ISR, DPC)
		if providerGUID.Equals(PerfInfoKernelGUID) {
			return h.routePerfInfoEvents(helper, eventID)
		}

		// Route Image events (Image)
		if providerGUID.Equals(ImageKernelGUID) {
			return h.routeImageEvents(helper, eventID)
		}

		// Route PageFault events
		if providerGUID.Equals(PageFaultKernelGUID) {
			return h.routePageFaultEvents(helper, eventID)
		}
	}

	return nil
}

// routeSystemConfigEvents routes SystemConfig events to disk handlers
func (h *EventHandler) routeSystemConfigEvents(helper *etw.EventRecordHelper, eventID uint16) error {
	if !h.config.DiskIO.Enabled || !h.config.DiskIO.TrackDiskInfo || len(h.diskEventHandlers) == 0 {
		return nil
	}

	switch eventID {
	case etw.EVENT_TRACE_TYPE_CONFIG_PHYSICALDISK: // 11
		for _, handler := range h.diskEventHandlers {
			if err := handler.HandleSystemConfigPhyDisk(helper); err != nil {
				return err
			}
		}
	case etw.EVENT_TRACE_TYPE_CONFIG_LOGICALDISK: // 12
		for _, handler := range h.diskEventHandlers {
			if err := handler.HandleSystemConfigLogDisk(helper); err != nil {
				return err
			}
		}
	}

	return nil
}

// routeDiskEvents routes disk I/O events to all registered disk handlers
func (h *EventHandler) routeDiskEvents(helper *etw.EventRecordHelper, eventID uint16) error {
	if len(h.diskEventHandlers) == 0 {
		return nil
	}

	switch eventID {
	case etw.EVENT_TRACE_TYPE_IO_READ: // 10 - DiskIo Read completion
		for _, handler := range h.diskEventHandlers {
			if err := handler.HandleDiskRead(helper); err != nil {
				h.log.Error().Err(err).Uint16("event_id", eventID).Msg("Error handling disk read event")
				return err
			}
		}
	case etw.EVENT_TRACE_TYPE_IO_WRITE: // 11 - DiskIo Write completion
		for _, handler := range h.diskEventHandlers {
			if err := handler.HandleDiskWrite(helper); err != nil {
				h.log.Error().Err(err).Uint16("event_id", eventID).Msg("Error handling disk write event")
				return err
			}
		}
	case etw.EVENT_TRACE_TYPE_IO_FLUSH: // 14 - DiskIo Flush
		for _, handler := range h.diskEventHandlers {
			if err := handler.HandleDiskFlush(helper); err != nil {
				h.log.Error().Err(err).Uint16("event_id", eventID).Msg("Error handling disk flush event")
				return err
			}
		}
	}

	return nil
}

// routeProcessEvents routes process events to all registered process handlers
func (h *EventHandler) routeProcessEvents(helper *etw.EventRecordHelper, eventID uint16) error {
	if len(h.processEventHandlers) == 0 {
		return nil
	}

	switch eventID {
	case 1, 15: // Process Start, ProcessRundown
		for _, handler := range h.processEventHandlers {
			if err := handler.HandleProcessStart(helper); err != nil {
				return err
			}
		}
	case 2: // Process End
		for _, handler := range h.processEventHandlers {
			if err := handler.HandleProcessEnd(helper); err != nil {
				return err
			}
		}
	}

	return nil
}

// routeFileEvents routes file I/O events to all registered file handlers
func (h *EventHandler) routeFileEvents(helper *etw.EventRecordHelper, eventID uint16) error {
	if len(h.fileEventHandlers) == 0 {
		return nil
	}

	switch eventID {
	case 15: // File Read
		for _, handler := range h.fileEventHandlers {
			if err := handler.HandleFileRead(helper); err != nil {
				return err
			}
		}
	case 16: // File Write
		for _, handler := range h.fileEventHandlers {
			if err := handler.HandleFileWrite(helper); err != nil {
				return err
			}
		}
	case 12: // File Create
		for _, handler := range h.fileEventHandlers {
			if err := handler.HandleFileCreate(helper); err != nil {
				return err
			}
		}
	case 14: // File Close
		for _, handler := range h.fileEventHandlers {
			if err := handler.HandleFileClose(helper); err != nil {
				return err
			}
		}
	case 26: // File Delete
		for _, handler := range h.fileEventHandlers {
			if err := handler.HandleFileDelete(helper); err != nil {
				return err
			}
		}
	}

	return nil
}

// routeThreadEvents routes thread events to all registered handlers
func (h *EventHandler) routeThreadEvents(helper *etw.EventRecordHelper, eventID uint16) error {
	if len(h.threadHandlers) == 0 {
		return nil
	}

	switch eventID {
	case 36: // TODO: Context Switch - need to find ETW constant
		for _, handler := range h.threadHandlers {
			if err := handler.HandleContextSwitch(helper); err != nil {
				return err
			}
		}
	case 50: // TODO: ReadyThread - need to find ETW constant
		for _, handler := range h.threadHandlers {
			if err := handler.HandleReadyThread(helper); err != nil {
				return err
			}
		}
	case etw.EVENT_TRACE_TYPE_START, etw.EVENT_TRACE_TYPE_DC_START: // 1, 3
		for _, handler := range h.threadHandlers {
			if err := handler.HandleThreadStart(helper); err != nil {
				return err
			}
		}
	case etw.EVENT_TRACE_TYPE_END, etw.EVENT_TRACE_TYPE_DC_END: // 2, 4
		for _, handler := range h.threadHandlers {
			if err := handler.HandleThreadEnd(helper); err != nil {
				return err
			}
		}
	}

	return nil
}

// EventCallback is called for higher-level event processing
func (h *EventHandler) EventCallback(event *etw.Event) error {
	// Perform any event-level processing here if needed
	return nil
}

// routePerfInfoEvents routes interrupt and DPC events to all registered interrupt handlers
func (h *EventHandler) routePerfInfoEvents(helper *etw.EventRecordHelper, eventID uint16) error {
	if len(h.perfinfoHandlers) == 0 {
		return nil
	}

	switch eventID {
	case 67: // MOF
		for _, handler := range h.perfinfoHandlers {
			if err := handler.HandleISREvent(helper); err != nil {
				return err
			}
		}
	case 66, 68, 69: // MOF
		for _, handler := range h.perfinfoHandlers {
			if err := handler.HandleDPCEvent(helper); err != nil {
				return err
			}
		}
	}

	return nil
}

// routeImageEvents routes image load events to all registered interrupt handlers
func (h *EventHandler) routeImageEvents(helper *etw.EventRecordHelper, eventID uint16) error {
	if len(h.perfinfoHandlers) == 0 {
		return nil
	}

	switch eventID {
	case etw.EVENT_TRACE_TYPE_END,
		etw.EVENT_TRACE_TYPE_DC_START,
		etw.EVENT_TRACE_TYPE_DC_END,
		etw.EVENT_TRACE_TYPE_LOAD: // Image Load events
		for _, handler := range h.perfinfoHandlers {
			if err := handler.HandleImageLoadEvent(helper); err != nil {
				return err
			}
		}
	}

	return nil
}

// routePageFaultEvents routes page fault events to all registered interrupt handlers
func (h *EventHandler) routePageFaultEvents(helper *etw.EventRecordHelper, eventID uint16) error {
	if len(h.perfinfoHandlers) == 0 {
		return nil
	}

	// Page fault events - need to check specific event IDs for hard page faults
	switch eventID {
	case 32: // 32 - Hard page fault
		for _, handler := range h.perfinfoHandlers {
			if err := handler.HandleHardPageFaultEvent(helper); err != nil {
				return err
			}
		}
	}

	return nil
}
