package etwmain

import (
	"github.com/phuslu/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/tekert/goetw/etw"

	"etw_exporter/internal/collectors/kdiskio"
	"etw_exporter/internal/collectors/kernelthread"
	"etw_exporter/internal/collectors/kperfinfo"
	"etw_exporter/internal/collectors/kprocess"
	"etw_exporter/internal/collectors/ksystemconfig"
	"etw_exporter/internal/config"
	"etw_exporter/internal/logger"
)

// Event handler interfaces for different event types
type DiskEventHandler interface {
	HandleDiskRead(helper *etw.EventRecordHelper) error
	HandleDiskWrite(helper *etw.EventRecordHelper) error
	HandleDiskFlush(helper *etw.EventRecordHelper) error
}

type ProcessEventHandler interface {
	HandleProcessStart(helper *etw.EventRecordHelper) error
	HandleProcessEnd(helper *etw.EventRecordHelper) error
}

type ThreadNtEventHandler interface {
	HandleContextSwitchRaw(record *etw.EventRecord) error
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
	HandleImageLoadEvent(helper *etw.EventRecordHelper) error // TODO: this should go to KernelImage handler.
	HandleImageUnloadEvent(helper *etw.EventRecordHelper) error
	HandleHardPageFaultEvent(helper *etw.EventRecordHelper) error // TODO: this should go to KernelPageFault handler.
	HandleSampleProfileEvent(helper *etw.EventRecordHelper) error
}

type SystemConfigEventHandler interface {
	HandleSystemConfigPhyDisk(helper *etw.EventRecordHelper) error
	HandleSystemConfigLogDisk(helper *etw.EventRecordHelper) error
}

// EventHandler encapsulates state and logic for ETW event processing
// This handler routes events to specialized sub-handlers based on event type
// and provider GUID.
type EventHandler struct {
	// Collectors for different metric categories
	diskHandler     *kdiskio.DiskIOHandler
	threadCSHandler *kernelthread.ThreadHandler
	perfinfoHandler *kperfinfo.PerfInfoHandler

	// Future collectors will be added here:
	// networkCollector *NetworkCollector
	// memoryCollector  *MemoryCollector
	// cpuCollector     *CPUCollector

	// Shared state and caches for callbacks
	config *config.CollectorConfig
	log    log.Logger // Event handler logger

	// Routing tables for different event types - hot path optimized
	diskEventHandlers    []DiskEventHandler
	processEventHandlers []ProcessEventHandler
	threadEventHandlers  []ThreadNtEventHandler
	fileEventHandlers    []FileEventHandler
	perfinfoHandlers     []PerfInfoNtEventHandler
	systemConfigHandlers []SystemConfigEventHandler
}

// NewEventHandler creates a new central event handler. It initializes all collectors
// that are enabled in the provided configuration, registers them for Prometheus
// metrics where applicable, and sets up the internal routing tables.
func NewEventHandler(config *config.CollectorConfig) *EventHandler {
	handler := &EventHandler{
		config:               config,
		log:                  logger.NewLoggerWithContext("event_handler"),
		diskEventHandlers:    make([]DiskEventHandler, 0),
		processEventHandlers: make([]ProcessEventHandler, 0),
		threadEventHandlers:  make([]ThreadNtEventHandler, 0),
		fileEventHandlers:    make([]FileEventHandler, 0),
		perfinfoHandlers:     make([]PerfInfoNtEventHandler, 0),
		systemConfigHandlers: make([]SystemConfigEventHandler, 0),
	}

	// Always register the global process handler for process events
	// This ensures we have process name mappings available for all collectors
	processHandler := kprocess.NewProcessHandler()
	handler.RegisterProcessEventHandler(processHandler)
	handler.log.Debug().Msg("Registered global process handler")

	// Always register the system config handler to collect data
	systemConfigHandler := ksystemconfig.NewSystemConfigHandler()
	handler.RegisterSystemConfigEventHandler(systemConfigHandler)
	handler.log.Debug().Msg("Registered global system config handler")

	// Initialize enabled collectors based on configuration
	if config.DiskIO.Enabled {
		handler.diskHandler = kdiskio.NewDiskIOHandler(&config.DiskIO)
		handler.RegisterDiskEventHandler(handler.diskHandler)
		handler.RegisterFileEventHandler(handler.diskHandler)
		prometheus.MustRegister(handler.diskHandler.GetCustomCollector())
		handler.log.Info().Msg("Disk I/O collector enabled and registered with Prometheus")

		// Request the necessary metrics from the system config collector.
		systemConfigCollector := ksystemconfig.GetGlobalSystemConfigCollector()
		systemConfigCollector.RequestMetrics(
			"etw_physical_disk_info",
			"etw_logical_disk_info",
		)

	}

	if config.ThreadCS.Enabled {
		handler.threadCSHandler = kernelthread.NewThreadHandler()
		// Register the thread collector with the handler
		handler.RegisterThreadEventHandler(handler.threadCSHandler)
		// Register the custom collector with Prometheus for high-performance metrics
		prometheus.MustRegister(handler.threadCSHandler.GetCustomCollector())
		handler.log.Info().Msg("ThreadCS collector enabled and registered with Prometheus")
	}

	if config.PerfInfo.Enabled {
		handler.perfinfoHandler = kperfinfo.NewPerfInfoHandler(&config.PerfInfo)
		// Register the interrupt handler with the event handler
		handler.RegisterPerfInfoEventHandler(handler.perfinfoHandler)
		// Also register for thread events to finalize DPC durations with CSwitch events.
		handler.RegisterThreadEventHandler(handler.perfinfoHandler)
		// Register the custom collector with Prometheus for high-performance metrics
		prometheus.MustRegister(handler.perfinfoHandler.GetCustomCollector())
		handler.log.Info().Msg("PerfInfo collector enabled and registered with Prometheus")
	}

	handler.log.Info().
		Int("disk_handlers", len(handler.diskEventHandlers)).
		Int("process_handlers", len(handler.processEventHandlers)).
		Int("thread_handlers", len(handler.threadEventHandlers)).
		Int("file_handlers", len(handler.fileEventHandlers)).
		Int("interrupt_handlers", len(handler.perfinfoHandlers)).
		Int("systemconfig_handlers", len(handler.systemConfigHandlers)).
		Msg("Event handlers initialized")

	return handler
}

func (h *EventHandler) RegisterDiskEventHandler(handler DiskEventHandler) {
	h.diskEventHandlers = append(h.diskEventHandlers, handler)
	h.log.Debug().Int("total_disk_handlers", len(h.diskEventHandlers)).Msg("Disk event handler registered")
}

func (h *EventHandler) RegisterProcessEventHandler(handler ProcessEventHandler) {
	h.processEventHandlers = append(h.processEventHandlers, handler)
	h.log.Debug().Int("total_process_handlers", len(h.processEventHandlers)).Msg("Process event handler registered")
}

func (h *EventHandler) RegisterThreadEventHandler(handler ThreadNtEventHandler) {
	h.threadEventHandlers = append(h.threadEventHandlers, handler)
	h.log.Debug().Int("total_thread_handlers", len(h.threadEventHandlers)).Msg("Thread event handler registered")
}

func (h *EventHandler) RegisterFileEventHandler(handler FileEventHandler) {
	h.fileEventHandlers = append(h.fileEventHandlers, handler)
	h.log.Debug().Int("total_file_handlers", len(h.fileEventHandlers)).Msg("File event handler registered")
}

func (h *EventHandler) RegisterPerfInfoEventHandler(handler PerfInfoNtEventHandler) {
	h.perfinfoHandlers = append(h.perfinfoHandlers, handler)
	h.log.Debug().Int("total_perfinfo_handlers", len(h.perfinfoHandlers)).Msg("PerfInfo event handler registered")
}

func (h *EventHandler) RegisterSystemConfigEventHandler(handler SystemConfigEventHandler) {
	h.systemConfigHandlers = append(h.systemConfigHandlers, handler)
	h.log.Debug().Int("total_systemconfig_handlers", len(h.systemConfigHandlers)).Msg("SystemConfig event handler registered")
}

// ETW Callback Methods - these satisfy the ETW consumer callback interface
// Using struct methods allows us to share state and reduce parameter passing

// EventRecordCallback is the first-stage, hot-path callback for every event record.
// It performs fast-path filtering for extremely high-frequency events (like CSwitch)
// and routes them to specialized raw handlers, bypassing more expensive processing stages.
// It returns 'false' for handled events to prevent further processing by the consumer.
func (h *EventHandler) EventRecordCallback(record *etw.EventRecord) bool {
	// Fast-path for high-frequency events. We identify them here and route them
	// to specialized raw handlers, bypassing the expensive EventRecordHelper creation.
	// We return 'false' to prevent further processing by the goetw consumer.

	// CSwitch Event: {3d6fa8d1-fe05-11d0-9dda-00c04fd7ba7c}, Opcode 36
	if record.EventHeader.ProviderId.Equals(ThreadKernelGUID) &&
		record.EventHeader.EventDescriptor.Opcode == 36 {

		for _, handler := range h.threadEventHandlers {
			// We ignore the error here for performance. The raw handler should log it.
			_ = handler.HandleContextSwitchRaw(record)
		}
		return false // Stop processing, we've handled it.
	}

	// For all other events, continue to the next callback stage.
	return true
}

// EventRecordHelperCallback is a second-stage callback, invoked after a helper
// object has been created for an event record. It is currently a placeholder.
func (h *EventHandler) EventRecordHelperCallback(helper *etw.EventRecordHelper) error {
	// Perform any helper-level processing here if needed
	return nil
}

// EventPreparedCallback is the third-stage callback, called just before event
// properties are parsed. This is the main entry point to the event routing logic.
func (h *EventHandler) EventPreparedCallback(helper *etw.EventRecordHelper) error {
	defer helper.Skip() // Stop further processing

	// Use the integrated routing for scalable event distribution
	return h.RouteEvent(helper)
}

// RouteEvent routes events to appropriate handlers based on provider GUID and event type.
// This is the hot path - optimized for performance. The router is intentionally kept
// simple and does not check collector configurations; it only routes events to
// handlers that have been registered. If a handler is registered, it will receive events.
func (h *EventHandler) RouteEvent(helper *etw.EventRecordHelper) error {
	// Extract event information for routing
	eventRecord := helper.EventRec
	providerGUID := &eventRecord.EventHeader.ProviderId
	var eventID uint16 = 0
	if helper.EventRec.IsMof() {
		eventID = uint16(eventRecord.EventHeader.EventDescriptor.Opcode)
	} else {
		eventID = eventRecord.EventHeader.EventDescriptor.Id
	}

	// Route SystemConfig events
	if providerGUID.Equals(SystemConfigGUID) {
		return h.routeSystemConfigEvents(helper, eventID)
	}

	// Route disk events from Microsoft-Windows-Kernel-Disk
	if providerGUID.Equals(MicrosoftWindowsKernelDiskGUID) {
		return h.routeDiskEvents(helper, eventID)
	}

	// Route process events from Microsoft-Windows-Kernel-Process
	if providerGUID.Equals(MicrosoftWindowsKernelProcessGUID) {
		return h.routeProcessEvents(helper, eventID)
	}

	// Route file events from Microsoft-Windows-Kernel-File
	if providerGUID.Equals(MicrosoftWindowsKernelFileGUID) {
		return h.routeFileEvents(helper, eventID)
	}

	// Route thread events from Thread kernel provider
	if providerGUID.Equals(ThreadKernelGUID) {
		return h.routeThreadEvents(helper, eventID)
	}

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

	return nil
}

// routeSystemConfigEvents routes SystemConfig events to disk handlers
func (h *EventHandler) routeSystemConfigEvents(helper *etw.EventRecordHelper, eventID uint16) error {
	if len(h.systemConfigHandlers) == 0 {
		return nil
	}

	switch eventID {
	case etw.EVENT_TRACE_TYPE_CONFIG_PHYSICALDISK: // 11
		for _, handler := range h.systemConfigHandlers {
			if err := handler.HandleSystemConfigPhyDisk(helper); err != nil {
				return err
			}
		}
	case etw.EVENT_TRACE_TYPE_CONFIG_LOGICALDISK: // 12
		for _, handler := range h.systemConfigHandlers {
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
	if len(h.threadEventHandlers) == 0 {
		return nil
	}

	switch eventID {
	case 36: // CSwitch (not used here, handled in raw path)
		for _, handler := range h.threadEventHandlers {
			if err := handler.HandleContextSwitch(helper); err != nil {
				return err
			}
		}
	case 50: // ReadyThread
		for _, handler := range h.threadEventHandlers {
			if err := handler.HandleReadyThread(helper); err != nil {
				return err
			}
		}
	case etw.EVENT_TRACE_TYPE_START, etw.EVENT_TRACE_TYPE_DC_START: // 1, 3
		for _, handler := range h.threadEventHandlers {
			if err := handler.HandleThreadStart(helper); err != nil {
				return err
			}
		}
	case etw.EVENT_TRACE_TYPE_END, etw.EVENT_TRACE_TYPE_DC_END: // 2, 4
		for _, handler := range h.threadEventHandlers {
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
	case 67: // ISR
		for _, handler := range h.perfinfoHandlers {
			if err := handler.HandleISREvent(helper); err != nil {
				return err
			}
		}
	case 66, 68, 69: // DPC
		for _, handler := range h.perfinfoHandlers {
			if err := handler.HandleDPCEvent(helper); err != nil {
				return err
			}
		}

	case 46: // Sample Profile
		for _, handler := range h.perfinfoHandlers {
			if err := handler.HandleSampleProfileEvent(helper); err != nil {
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
	case etw.EVENT_TRACE_TYPE_DC_START,
		etw.EVENT_TRACE_TYPE_DC_END,
		etw.EVENT_TRACE_TYPE_LOAD: // Image Load events
		for _, handler := range h.perfinfoHandlers {
			if err := handler.HandleImageLoadEvent(helper); err != nil {
				return err
			}
		}
	case etw.EVENT_TRACE_TYPE_END: // Image Unload event
		for _, handler := range h.perfinfoHandlers {
			if err := handler.HandleImageUnloadEvent(helper); err != nil {
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
