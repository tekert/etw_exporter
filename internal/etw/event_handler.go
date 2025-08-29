package etwmain

import (
	"reflect"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/tekert/goetw/etw"
	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"

	"etw_exporter/internal/collectors/kerneldiskio"
	"etw_exporter/internal/collectors/kernelmemory"
	"etw_exporter/internal/collectors/kernelnetwork"
	"etw_exporter/internal/collectors/kernelperf"
	"etw_exporter/internal/collectors/kernelprocess"
	"etw_exporter/internal/collectors/kernelsysconfig"
	"etw_exporter/internal/collectors/kernelthread"
	"etw_exporter/internal/collectors/kernelthread/threadmapping"
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
	HandleImageLoadEvent(helper *etw.EventRecordHelper) error // TODO: this should go to KernelImage handler. or not
	HandleImageUnloadEvent(helper *etw.EventRecordHelper) error
	HandleSampleProfileEvent(helper *etw.EventRecordHelper) error
}

type SystemConfigEventHandler interface {
	HandleSystemConfigPhyDisk(helper *etw.EventRecordHelper) error
	HandleSystemConfigLogDisk(helper *etw.EventRecordHelper) error
}

type NetworkEventHandler interface {
	HandleTCPDataSent(helper *etw.EventRecordHelper) error
	HandleTCPDataReceived(helper *etw.EventRecordHelper) error
	HandleTCPConnectionAttempted(helper *etw.EventRecordHelper) error
	HandleTCPConnectionAccepted(helper *etw.EventRecordHelper) error
	HandleTCPDataRetransmitted(helper *etw.EventRecordHelper) error
	HandleTCPConnectionFailed(helper *etw.EventRecordHelper) error
	HandleUDPDataSent(helper *etw.EventRecordHelper) error
	HandleUDPDataReceived(helper *etw.EventRecordHelper) error
	HandleUDPConnectionFailed(helper *etw.EventRecordHelper) error
}

type MemoryNtEventHandler interface {
	HandleHardPageFaultEvent(helper *etw.EventRecordHelper) error
}

// EventHandler encapsulates state and logic for ETW event processing
// This handler routes events to specialized sub-handlers based on event type
// and provider GUID.
type EventHandler struct {
	// Collectors for different metric categories
	diskHandler     *kerneldiskio.DiskIOHandler
	threadCSHandler *kernelthread.ThreadHandler
	perfinfoHandler *kernelperf.PerfInfoHandler
	networkHandler  *kernelnetwork.NetworkHandler
	memoryHandler   *kernelmemory.MemoryHandler

	// Future collectors will be added here:

	// Shared state and caches for callbacks
	config        *config.CollectorConfig
	log           *phusluadapter.SampledLogger // Event handler logger
	threadMapping *threadmapping.ThreadMapping

	// Event counters by provider - atomic counters for thread safety
	diskEventCount         atomic.Uint64
	processEventCount      atomic.Uint64
	threadEventCount       atomic.Uint64
	fileEventCount         atomic.Uint64
	perfinfoEventCount     atomic.Uint64
	systemConfigEventCount atomic.Uint64
	imageEventCount        atomic.Uint64
	pageFaultEventCount    atomic.Uint64
	networkEventCount      atomic.Uint64

	// ROUTING TABLES for different event types - hot path optimized
	diskEventHandlers         []DiskEventHandler
	processEventHandlers      []ProcessEventHandler
	threadEventHandlers       []ThreadNtEventHandler
	fileEventHandlers         []FileEventHandler
	perfinfoEventHandlers     []PerfInfoNtEventHandler
	systemConfigEventHandlers []SystemConfigEventHandler
	networkEventHandlers      []NetworkEventHandler
	memoryEventHandlers       []MemoryNtEventHandler
}

// NewEventHandler creates a new central event handler. It initializes all collectors
// that are enabled in the provided configuration, registers them for Prometheus
// metrics where applicable, and sets up the internal routing tables.
func NewEventHandler(config *config.CollectorConfig) *EventHandler {
	handler := &EventHandler{
		config:                    config,
		log:                       logger.NewSampledLoggerCtx("event_handler"),
		diskEventHandlers:         make([]DiskEventHandler, 0),
		processEventHandlers:      make([]ProcessEventHandler, 0),
		threadEventHandlers:       make([]ThreadNtEventHandler, 0),
		fileEventHandlers:         make([]FileEventHandler, 0),
		perfinfoEventHandlers:     make([]PerfInfoNtEventHandler, 0),
		systemConfigEventHandlers: make([]SystemConfigEventHandler, 0),
		networkEventHandlers:      make([]NetworkEventHandler, 0),
		memoryEventHandlers:       make([]MemoryNtEventHandler, 0),
	}

	// --- Core Component Initialization ---
	// ThreadMapping is a shared dependency for TID-PID tracking and lifecycle management.
	threadMapping := threadmapping.NewThreadMapping()
	handler.threadMapping = threadMapping

	// Always register the global process handler for process events
	// This ensures we have process name mappings available for all collectors
	processHandler := kernelprocess.NewProcessHandler(threadMapping)
	handler.RegisterProcessEventHandler(processHandler)
	handler.log.Debug().Msg("Registered global process handler")

	// Always register the system config handler to collect data
	systemConfigHandler := kernelsysconfig.NewSystemConfigHandler()
	handler.RegisterSystemConfigEventHandler(systemConfigHandler)
	handler.log.Debug().Msg("Registered global system config handler")

	// Initialize enabled collectors based on configuration
	if config.DiskIO.Enabled {
		handler.diskHandler = kerneldiskio.NewDiskIOHandler(&config.DiskIO)
		handler.RegisterDiskEventHandler(handler.diskHandler)
		handler.RegisterFileEventHandler(handler.diskHandler)
		prometheus.MustRegister(handler.diskHandler.GetCustomCollector())
		handler.log.Info().Msg("Disk I/O collector enabled and registered with Prometheus")

		// Request the necessary metrics from the system config collector.
		systemConfigCollector := kernelsysconfig.GetGlobalSystemConfigCollector()
		systemConfigCollector.RequestMetrics(
			kernelsysconfig.PhysicalDiskInfoMetricName,
			kernelsysconfig.LogicalDiskInfoMetricName,
		)

	}

	if config.ThreadCS.Enabled {
		handler.threadCSHandler = kernelthread.NewThreadHandler(threadMapping)
		// Register the thread collector with the handler
		handler.RegisterThreadEventHandler(handler.threadCSHandler)
		// Register the custom collector
		prometheus.MustRegister(handler.threadCSHandler.GetCustomCollector())
		handler.log.Info().Msg("ThreadCS collector enabled and registered with Prometheus")
	}

	if config.PerfInfo.Enabled {
		handler.perfinfoHandler = kernelperf.NewPerfInfoHandler(&config.PerfInfo)
		// Register the interrupt handler with the event handler
		handler.RegisterPerfInfoEventHandler(handler.perfinfoHandler)
		// Also register for thread events to finalize DPC durations with CSwitch events.
		handler.RegisterThreadEventHandler(handler.perfinfoHandler)
		// Register the custom collector
		prometheus.MustRegister(handler.perfinfoHandler.GetCustomCollector())
		handler.log.Info().Msg("PerfInfo collector enabled and registered with Prometheus")
	}

	if config.Network.Enabled {
		handler.networkHandler = kernelnetwork.NewNetworkHandler(&config.Network)
		// Register the network handler with the event handler
		handler.RegisterNetworkEventHandler(handler.networkHandler)
		// Register the custom collector
		prometheus.MustRegister(handler.networkHandler.GetCustomCollector())
		handler.log.Info().Msg("Network collector enabled and registered with Prometheus")
	}

	if config.Memory.Enabled {
		handler.memoryHandler = kernelmemory.NewMemoryHandler(&config.Memory, threadMapping)
		// Register the memory handler for hard fault events
		handler.RegisterMemoryEventHandler(handler.memoryHandler)
		// Register the custom collector
		prometheus.MustRegister(handler.memoryHandler.GetCustomCollector())
		handler.log.Info().Msg("Memory collector enabled and registered with Prometheus")
	}

	// --- Sentinel Collector Registration ---
	// This collector MUST be registered LAST. Its purpose is to trigger a cleanup
	// of terminated processes in the ProcessCollector AFTER all other collectors
	// have completed their scrape. This prevents race conditions where a process
	// terminates and its metrics are lost before they can be scraped.
	prometheus.MustRegister(kernelprocess.NewProcessCleanupCollector(threadMapping))
	handler.log.Debug().Msg("Registered post-scrape process cleanup collector")

	handler.LogHandlerCounts()
	return handler
}

// GetThreadMapping returns the shared thread mapping instance.
func (h *EventHandler) GetThreadMapping() *threadmapping.ThreadMapping {
	return h.threadMapping
}

func (h *EventHandler) LogHandlerCounts() {
	v := reflect.ValueOf(h).Elem() // Get the value of the EventHandler struct
	t := v.Type()                  // Get the type of the EventHandler struct

	log := h.log.Info() // Start building the log message

	// Iterate over the fields of the struct
	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)
		fieldValue := v.Field(i)

		// Check if the field name ends with "Handlers" and is a slice
		if fieldValue.Kind() == reflect.Slice && field.Name[len(field.Name)-8:] == "Handlers" {
			log = log.Int(field.Name, fieldValue.Len())
		}
	}

	log.Msg("Event handlers initialized")
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
	h.perfinfoEventHandlers = append(h.perfinfoEventHandlers, handler)
	h.log.Debug().Int("total_perfinfo_handlers", len(h.perfinfoEventHandlers)).Msg("PerfInfo event handler registered")
}

func (h *EventHandler) RegisterSystemConfigEventHandler(handler SystemConfigEventHandler) {
	h.systemConfigEventHandlers = append(h.systemConfigEventHandlers, handler)
	h.log.Debug().Int("total_systemconfig_handlers", len(h.systemConfigEventHandlers)).Msg("SystemConfig event handler registered")
}

func (h *EventHandler) RegisterNetworkEventHandler(handler NetworkEventHandler) {
	h.networkEventHandlers = append(h.networkEventHandlers, handler)
	h.log.Debug().Int("total_network_handlers", len(h.networkEventHandlers)).Msg("Network event handler registered")
}

func (h *EventHandler) RegisterMemoryEventHandler(handler MemoryNtEventHandler) {
	h.memoryEventHandlers = append(h.memoryEventHandlers, handler)
	h.log.Debug().Int("total_memory_handlers", len(h.networkEventHandlers)).Msg("Memory event handler registered")
}

// GetEventCounts returns the current event counts for each provider.
// These methods are used by the ETW stats collector to expose metrics.
func (h *EventHandler) GetDiskEventCount() uint64         { return h.diskEventCount.Load() }
func (h *EventHandler) GetProcessEventCount() uint64      { return h.processEventCount.Load() }
func (h *EventHandler) GetThreadEventCount() uint64       { return h.threadEventCount.Load() }
func (h *EventHandler) GetFileEventCount() uint64         { return h.fileEventCount.Load() }
func (h *EventHandler) GetPerfInfoEventCount() uint64     { return h.perfinfoEventCount.Load() }
func (h *EventHandler) GetSystemConfigEventCount() uint64 { return h.systemConfigEventCount.Load() }
func (h *EventHandler) GetImageEventCount() uint64        { return h.imageEventCount.Load() }
func (h *EventHandler) GetPageFaultEventCount() uint64    { return h.pageFaultEventCount.Load() }
func (h *EventHandler) GetNetworkEventCount() uint64      { return h.networkEventCount.Load() }

// ETW Callback Methods - these satisfy the ETW consumer callback interface

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
		h.threadEventCount.Add(1)

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
		h.systemConfigEventCount.Add(1)
		return h.routeSystemConfigEvents(helper, eventID)
	}

	// Route disk events from Microsoft-Windows-Kernel-Disk
	if providerGUID.Equals(MicrosoftWindowsKernelDiskGUID) {
		h.diskEventCount.Add(1)
		return h.routeDiskEvents(helper, eventID)
	}

	// Route process events from Microsoft-Windows-Kernel-Process
	if providerGUID.Equals(MicrosoftWindowsKernelProcessGUID) {
		h.processEventCount.Add(1)
		return h.routeProcessEvents(helper, eventID)
	}

	// Route file events from Microsoft-Windows-Kernel-File
	if providerGUID.Equals(MicrosoftWindowsKernelFileGUID) {
		h.fileEventCount.Add(1)
		return h.routeFileEvents(helper, eventID)
	}

	// Route thread events from Thread kernel provider
	if providerGUID.Equals(ThreadKernelGUID) {
		h.threadEventCount.Add(1)
		return h.routeThreadEvents(helper, eventID)
	}

	// Route PerfInfo events (ISR, DPC)
	if providerGUID.Equals(PerfInfoKernelGUID) {
		h.perfinfoEventCount.Add(1)
		return h.routePerfInfoEvents(helper, eventID)
	}

	// Route Image events (Image)
	if providerGUID.Equals(ImageKernelGUID) {
		h.imageEventCount.Add(1)
		return h.routeImageEvents(helper, eventID)
	}

	// Route PageFault events
	if providerGUID.Equals(PageFaultKernelGUID) {
		h.pageFaultEventCount.Add(1)
		return h.routePageFaultEvents(helper, eventID)
	}

	// Route network events from Microsoft-Windows-Kernel-Network
	if providerGUID.Equals(MicrosoftWindowsKernelNetworkGUID) {
		h.networkEventCount.Add(1)
		return h.routeNetworkEvents(helper, eventID)
	}

	return nil
}

// routeSystemConfigEvents routes SystemConfig events to disk handlers
func (h *EventHandler) routeSystemConfigEvents(helper *etw.EventRecordHelper, eventID uint16) error {
	if len(h.systemConfigEventHandlers) == 0 {
		return nil
	}

	switch eventID {
	case etw.EVENT_TRACE_TYPE_CONFIG_PHYSICALDISK: // 11
		for _, handler := range h.systemConfigEventHandlers {
			if err := handler.HandleSystemConfigPhyDisk(helper); err != nil {
				return err
			}
		}
	case etw.EVENT_TRACE_TYPE_CONFIG_LOGICALDISK: // 12
		for _, handler := range h.systemConfigEventHandlers {
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
	if len(h.perfinfoEventHandlers) == 0 {
		return nil
	}

	switch eventID {
	case 67: // ISR
		for _, handler := range h.perfinfoEventHandlers {
			if err := handler.HandleISREvent(helper); err != nil {
				return err
			}
		}
	case 66, 68, 69: // DPC
		for _, handler := range h.perfinfoEventHandlers {
			if err := handler.HandleDPCEvent(helper); err != nil {
				return err
			}
		}

	case 46: // Sample Profile
		for _, handler := range h.perfinfoEventHandlers {
			if err := handler.HandleSampleProfileEvent(helper); err != nil {
				return err
			}
		}
	}

	return nil
}

// routeImageEvents routes image load events to all registered interrupt handlers
func (h *EventHandler) routeImageEvents(helper *etw.EventRecordHelper, eventID uint16) error {
	if len(h.perfinfoEventHandlers) == 0 {
		return nil
	}

	switch eventID {
	case etw.EVENT_TRACE_TYPE_DC_START,
		etw.EVENT_TRACE_TYPE_DC_END,
		etw.EVENT_TRACE_TYPE_LOAD: // Image Load events
		for _, handler := range h.perfinfoEventHandlers {
			if err := handler.HandleImageLoadEvent(helper); err != nil {
				return err
			}
		}
	case etw.EVENT_TRACE_TYPE_END: // Image Unload event
		for _, handler := range h.perfinfoEventHandlers {
			if err := handler.HandleImageUnloadEvent(helper); err != nil {
				return err
			}
		}
	}

	return nil
}

// routePageFaultEvents routes page fault events to all registered interrupt handlers
func (h *EventHandler) routePageFaultEvents(helper *etw.EventRecordHelper, eventID uint16) error {
	if len(h.memoryEventHandlers) == 0 {
		return nil
	}

	// Page fault events - need to check specific event IDs for hard page faults
	switch eventID {
	case 32: // 32 - Hard page fault
		for _, handler := range h.memoryEventHandlers {
			if err := handler.HandleHardPageFaultEvent(helper); err != nil {
				return err
			}
		}
	}

	return nil
}

// routeNetworkEvents routes network events to all registered network handlers
func (h *EventHandler) routeNetworkEvents(helper *etw.EventRecordHelper, eventID uint16) error {
	if len(h.networkEventHandlers) == 0 {
		return nil
	}

	switch eventID {
	case 10, 26: // TCP Data Sent (IPv4/IPv6)
		for _, handler := range h.networkEventHandlers {
			if err := handler.HandleTCPDataSent(helper); err != nil {
				return err
			}
		}
	case 11, 27: // TCP Data Received (IPv4/IPv6)
		for _, handler := range h.networkEventHandlers {
			if err := handler.HandleTCPDataReceived(helper); err != nil {
				return err
			}
		}
	case 12, 28: // TCP Connection Attempted (IPv4/IPv6)
		for _, handler := range h.networkEventHandlers {
			if err := handler.HandleTCPConnectionAttempted(helper); err != nil {
				return err
			}
		}
	case 15, 31: // TCP Connection Accepted (IPv4/IPv6)
		for _, handler := range h.networkEventHandlers {
			if err := handler.HandleTCPConnectionAccepted(helper); err != nil {
				return err
			}
		}
	case 14, 30: // TCP Data Retransmitted (IPv4/IPv6)
		for _, handler := range h.networkEventHandlers {
			if err := handler.HandleTCPDataRetransmitted(helper); err != nil {
				return err
			}
		}
	case 17: // TCP Connection Failed
		for _, handler := range h.networkEventHandlers {
			if err := handler.HandleTCPConnectionFailed(helper); err != nil {
				return err
			}
		}
	case 42, 58: // UDP Data Sent (IPv4/IPv6)
		for _, handler := range h.networkEventHandlers {
			if err := handler.HandleUDPDataSent(helper); err != nil {
				return err
			}
		}
	case 43, 59: // UDP Data Received (IPv4/IPv6)
		for _, handler := range h.networkEventHandlers {
			if err := handler.HandleUDPDataReceived(helper); err != nil {
				return err
			}
		}
	case 49: // UDP Connection Failed
		for _, handler := range h.networkEventHandlers {
			if err := handler.HandleUDPConnectionFailed(helper); err != nil {
				return err
			}
		}
	}

	return nil
}
