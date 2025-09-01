package etwmain

import (
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
	"etw_exporter/internal/config"
	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"
)

// eventRoute defines a unique key for an ETW event for routing.
type eventRoute struct {
	ProviderGUID etw.GUID
	EventID      uint16
}

// eventHandlerFunc is a generic function signature for all event handlers.
type eventHandlerFunc func(helper *etw.EventRecordHelper) error

// EventHandler encapsulates state and logic for ETW event processing.
// This handler routes events to specialized sub-handlers based on a data-driven map.
type EventHandler struct {
	// Collectors for different metric categories
	diskHandler     *kerneldiskio.DiskIOHandler
	threadCSHandler *kernelthread.ThreadHandler
	perfinfoHandler *kernelperf.PerfInfoHandler
	networkHandler  *kernelnetwork.NetworkHandler
	memoryHandler   *kernelmemory.MemoryHandler

	// Shared state and caches for callbacks
	config       *config.CollectorConfig
	log          *phusluadapter.SampledLogger // Event handler logger
	stateManager *statemanager.KernelStateManager

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

	// ROUTING TABLES for hot path optimized routing
	routeMap      map[eventRoute]eventHandlerFunc
	guidToCounter map[etw.GUID]*atomic.Uint64
}

// NewEventHandler creates a new central event handler. It initializes all collectors
// that are enabled in the provided configuration, registers them for Prometheus
// metrics where applicable, and sets up the internal routing maps.
func NewEventHandler(config *config.CollectorConfig) *EventHandler {
	handler := &EventHandler{
		config:        config,
		log:           logger.NewSampledLoggerCtx("event_handler"),
		routeMap:      make(map[eventRoute]eventHandlerFunc),
		guidToCounter: make(map[etw.GUID]*atomic.Uint64),
	}

	// --- Core Component Initialization ---
	handler.stateManager = statemanager.GetGlobalStateManager()

	// Populate the GUID to counter map.
	handler.guidToCounter[*SystemConfigGUID] = &handler.systemConfigEventCount
	handler.guidToCounter[*MicrosoftWindowsKernelDiskGUID] = &handler.diskEventCount
	handler.guidToCounter[*MicrosoftWindowsKernelProcessGUID] = &handler.processEventCount
	handler.guidToCounter[*MicrosoftWindowsKernelFileGUID] = &handler.fileEventCount
	handler.guidToCounter[*ThreadKernelGUID] = &handler.threadEventCount
	handler.guidToCounter[*PerfInfoKernelGUID] = &handler.perfinfoEventCount
	handler.guidToCounter[*ImageKernelGUID] = &handler.imageEventCount
	handler.guidToCounter[*PageFaultKernelGUID] = &handler.pageFaultEventCount
	handler.guidToCounter[*MicrosoftWindowsKernelNetworkGUID] = &handler.networkEventCount

	// --- Register Routes for All Handlers ---

	// Always register the global process handler for process name mappings.
	// Provider: Microsoft-Windows-Kernel-Process ({22fb2cd6-0e7b-422b-a0c7-2fad1fd0e716})
	processHandler := kernelprocess.NewProcessHandler(handler.stateManager)
	handler.routeMap[eventRoute{*MicrosoftWindowsKernelProcessGUID, 1}] = processHandler.HandleProcessStart  // ProcessStart
	handler.routeMap[eventRoute{*MicrosoftWindowsKernelProcessGUID, 15}] = processHandler.HandleProcessStart // ProcessRundown
	handler.routeMap[eventRoute{*MicrosoftWindowsKernelProcessGUID, 2}] = processHandler.HandleProcessEnd    // ProcessStop
	handler.log.Debug().Msg("Registered global process handler routes")

	// Always register the system config handler.
	// Provider: NT Kernel Logger (EventTraceConfig) ({01853a65-418f-4f36-aefc-dc0f1d2fd235})
	systemConfigHandler := kernelsysconfig.NewSystemConfigHandler()
	handler.routeMap[eventRoute{*SystemConfigGUID, etw.EVENT_TRACE_TYPE_CONFIG_PHYSICALDISK}] = systemConfigHandler.HandleSystemConfigPhyDisk
	handler.routeMap[eventRoute{*SystemConfigGUID, etw.EVENT_TRACE_TYPE_CONFIG_LOGICALDISK}] = systemConfigHandler.HandleSystemConfigLogDisk
	handler.log.Debug().Msg("Registered global system config handler routes")

	// Initialize enabled collectors and register their routes.
	if config.DiskIO.Enabled {
		handler.diskHandler = kerneldiskio.NewDiskIOHandler(&config.DiskIO)
		prometheus.MustRegister(handler.diskHandler.GetCustomCollector())

		// Provider: Microsoft-Windows-Kernel-Disk ({c7bde69a-e1e0-4177-b6ef-283ad1525271})
		handler.routeMap[eventRoute{*MicrosoftWindowsKernelDiskGUID, etw.EVENT_TRACE_TYPE_IO_READ}] = handler.diskHandler.HandleDiskRead   // DiskRead
		handler.routeMap[eventRoute{*MicrosoftWindowsKernelDiskGUID, etw.EVENT_TRACE_TYPE_IO_WRITE}] = handler.diskHandler.HandleDiskWrite // DiskWrite
		handler.routeMap[eventRoute{*MicrosoftWindowsKernelDiskGUID, etw.EVENT_TRACE_TYPE_IO_FLUSH}] = handler.diskHandler.HandleDiskFlush // DiskFlush

		// Provider: Microsoft-Windows-Kernel-File ({edd08927-9cc4-4e65-b970-c2560fb5c289})
		handler.routeMap[eventRoute{*MicrosoftWindowsKernelFileGUID, 12}] = handler.diskHandler.HandleFileCreate // Create
		handler.routeMap[eventRoute{*MicrosoftWindowsKernelFileGUID, 14}] = handler.diskHandler.HandleFileClose  // Close
		handler.routeMap[eventRoute{*MicrosoftWindowsKernelFileGUID, 15}] = handler.diskHandler.HandleFileRead   // Read
		handler.routeMap[eventRoute{*MicrosoftWindowsKernelFileGUID, 16}] = handler.diskHandler.HandleFileWrite  // Write
		handler.routeMap[eventRoute{*MicrosoftWindowsKernelFileGUID, 26}] = handler.diskHandler.HandleFileDelete // DeletePath

		handler.log.Debug().Msg("Disk I/O collector enabled and routes registered")
		kernelsysconfig.GetGlobalSystemConfigCollector().RequestMetrics(
			kernelsysconfig.PhysicalDiskInfoMetricName,
			kernelsysconfig.LogicalDiskInfoMetricName,
		)
	}

	if config.ThreadCS.Enabled {
		handler.threadCSHandler = kernelthread.NewThreadHandler(handler.stateManager)
		prometheus.MustRegister(handler.threadCSHandler.GetCustomCollector())

		// Provider: NT Kernel Logger (Thread) ({3d6fa8d1-fe05-11d0-9dda-00c04fd7ba7c})
		// Note: CSwitch (36) is handled in the raw EventRecordCallback for performance.
		handler.routeMap[eventRoute{*ThreadKernelGUID, 50}] = handler.threadCSHandler.HandleReadyThread                            // ReadyThread
		handler.routeMap[eventRoute{*ThreadKernelGUID, etw.EVENT_TRACE_TYPE_START}] = handler.threadCSHandler.HandleThreadStart    // ThreadStart
		handler.routeMap[eventRoute{*ThreadKernelGUID, etw.EVENT_TRACE_TYPE_DC_START}] = handler.threadCSHandler.HandleThreadStart // ThreadRundown
		handler.routeMap[eventRoute{*ThreadKernelGUID, etw.EVENT_TRACE_TYPE_END}] = handler.threadCSHandler.HandleThreadEnd        // ThreadEnd
		handler.routeMap[eventRoute{*ThreadKernelGUID, etw.EVENT_TRACE_TYPE_DC_END}] = handler.threadCSHandler.HandleThreadEnd     // ThreadRundownEnd
		handler.log.Debug().Msg("ThreadCS collector enabled and routes registered")
	}

	if config.PerfInfo.Enabled {
		handler.perfinfoHandler = kernelperf.NewPerfInfoHandler(&config.PerfInfo)
		prometheus.MustRegister(handler.perfinfoHandler.GetCustomCollector())

		// Provider: NT Kernel Logger (PerfInfo) ({ce1dbfb4-137e-4da6-87b0-3f59aa102cbc})
		handler.routeMap[eventRoute{*PerfInfoKernelGUID, 67}] = handler.perfinfoHandler.HandleISREvent           // ISR
		handler.routeMap[eventRoute{*PerfInfoKernelGUID, 66}] = handler.perfinfoHandler.HandleDPCEvent           // ThreadDPC
		handler.routeMap[eventRoute{*PerfInfoKernelGUID, 68}] = handler.perfinfoHandler.HandleDPCEvent           // DPC
		handler.routeMap[eventRoute{*PerfInfoKernelGUID, 69}] = handler.perfinfoHandler.HandleDPCEvent           // TimerDPC
		handler.routeMap[eventRoute{*PerfInfoKernelGUID, 46}] = handler.perfinfoHandler.HandleSampleProfileEvent // SampleProfile

		// Provider: NT Kernel Logger (Image) ({2cb15d1d-5fc1-11d2-abe1-00a0c911f518})
		handler.routeMap[eventRoute{*ImageKernelGUID, etw.EVENT_TRACE_TYPE_LOAD}] = handler.perfinfoHandler.HandleImageLoadEvent     // Image Load
		handler.routeMap[eventRoute{*ImageKernelGUID, etw.EVENT_TRACE_TYPE_DC_START}] = handler.perfinfoHandler.HandleImageLoadEvent // Image Rundown
		handler.routeMap[eventRoute{*ImageKernelGUID, etw.EVENT_TRACE_TYPE_DC_END}] = handler.perfinfoHandler.HandleImageLoadEvent   // Image Rundown End
		handler.routeMap[eventRoute{*ImageKernelGUID, etw.EVENT_TRACE_TYPE_END}] = handler.perfinfoHandler.HandleImageUnloadEvent    // Image Unload

		// PerfInfo also needs CSwitch events to finalize DPC durations. This is handled
		// in the raw EventRecordCallback, which calls perfinfoHandler.HandleContextSwitchRaw.
		handler.log.Debug().Msg("PerfInfo collector enabled and routes registered")
	}

	if config.Network.Enabled {
		handler.networkHandler = kernelnetwork.NewNetworkHandler(&config.Network)
		prometheus.MustRegister(handler.networkHandler.GetCustomCollector())

		// Provider: Microsoft-Windows-Kernel-Network ({7dd42a49-5329-4832-8dfd-43d979153a88})
		handler.routeMap[eventRoute{*MicrosoftWindowsKernelNetworkGUID, 10}] = handler.networkHandler.HandleTCPDataSent            // TCP Send (IPv4)
		handler.routeMap[eventRoute{*MicrosoftWindowsKernelNetworkGUID, 26}] = handler.networkHandler.HandleTCPDataSent            // TCP Send (IPv6)
		handler.routeMap[eventRoute{*MicrosoftWindowsKernelNetworkGUID, 11}] = handler.networkHandler.HandleTCPDataReceived        // TCP Recv (IPv4)
		handler.routeMap[eventRoute{*MicrosoftWindowsKernelNetworkGUID, 27}] = handler.networkHandler.HandleTCPDataReceived        // TCP Recv (IPv6)
		handler.routeMap[eventRoute{*MicrosoftWindowsKernelNetworkGUID, 12}] = handler.networkHandler.HandleTCPConnectionAttempted // TCP Connect (IPv4)
		handler.routeMap[eventRoute{*MicrosoftWindowsKernelNetworkGUID, 28}] = handler.networkHandler.HandleTCPConnectionAttempted // TCP Connect (IPv6)
		handler.routeMap[eventRoute{*MicrosoftWindowsKernelNetworkGUID, 15}] = handler.networkHandler.HandleTCPConnectionAccepted  // TCP Accept (IPv4)
		handler.routeMap[eventRoute{*MicrosoftWindowsKernelNetworkGUID, 31}] = handler.networkHandler.HandleTCPConnectionAccepted  // TCP Accept (IPv6)
		handler.routeMap[eventRoute{*MicrosoftWindowsKernelNetworkGUID, 14}] = handler.networkHandler.HandleTCPDataRetransmitted   // TCP Retransmit (IPv4)
		handler.routeMap[eventRoute{*MicrosoftWindowsKernelNetworkGUID, 30}] = handler.networkHandler.HandleTCPDataRetransmitted   // TCP Retransmit (IPv6)
		handler.routeMap[eventRoute{*MicrosoftWindowsKernelNetworkGUID, 17}] = handler.networkHandler.HandleTCPConnectionFailed    // TCP Connect Failed
		handler.routeMap[eventRoute{*MicrosoftWindowsKernelNetworkGUID, 42}] = handler.networkHandler.HandleUDPDataSent            // UDP Send (IPv4)
		handler.routeMap[eventRoute{*MicrosoftWindowsKernelNetworkGUID, 58}] = handler.networkHandler.HandleUDPDataSent            // UDP Send (IPv6)
		handler.routeMap[eventRoute{*MicrosoftWindowsKernelNetworkGUID, 43}] = handler.networkHandler.HandleUDPDataReceived        // UDP Recv (IPv4)
		handler.routeMap[eventRoute{*MicrosoftWindowsKernelNetworkGUID, 59}] = handler.networkHandler.HandleUDPDataReceived        // UDP Recv (IPv6)
		handler.routeMap[eventRoute{*MicrosoftWindowsKernelNetworkGUID, 49}] = handler.networkHandler.HandleUDPConnectionFailed    // UDP Connect Failed
		handler.log.Debug().Msg("Network collector enabled and routes registered")
	}

	if config.Memory.Enabled {
		handler.memoryHandler = kernelmemory.NewMemoryHandler(&config.Memory, handler.stateManager)
		prometheus.MustRegister(handler.memoryHandler.GetCustomCollector())

		// Provider: NT Kernel Logger (PageFault) ({3d6fa8d3-fe05-11d0-9dda-00c04fd7ba7c})
		handler.routeMap[eventRoute{*PageFaultKernelGUID, 32}] = handler.memoryHandler.HandleMofHardPageFaultEvent // HardFault
		handler.log.Debug().Msg("Memory collector enabled and routes registered")
	}

	// --- Sentinel Collector Registration ---
	// This collector MUST be registered LAST. Its purpose is to trigger a cleanup
	// of terminated entities in the KernelStateManager AFTER all other collectors
	// have completed their scrape.
	prometheus.MustRegister(statemanager.NewStateCleanupCollector())
	handler.log.Debug().Msg("Registered post-scrape state cleanup collector")

	handler.log.Debug().Int("total_routes", len(handler.routeMap)).Msg("Event handler initialized")
	return handler
}

// GetStateManager returns the shared state manager instance.
func (h *EventHandler) GetStateManager() *statemanager.KernelStateManager {
	return h.stateManager
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

// ETW Callback Methods

// EventRecordCallback is the first-stage, hot-path callback for every event record.
// It performs fast-path filtering for CSwitch events and routes them to raw handlers,
// bypassing more expensive processing. It returns 'false' to stop further processing.
func (h *EventHandler) EventRecordCallback(record *etw.EventRecord) bool {
	// CSwitch Event: {3d6fa8d1-fe05-11d0-9dda-00c04fd7ba7c}, Opcode 36
	// This is the fastest path, handling raw events directly.
	if record.EventHeader.ProviderId.Equals(ThreadKernelGUID) &&
		record.EventHeader.EventDescriptor.Opcode == 36 {
		h.threadEventCount.Add(1)

		// Route to thread collector for context switch metrics.
		if h.threadCSHandler != nil {
			_ = h.threadCSHandler.HandleContextSwitchRaw(record)
		}
		// Route to perfinfo collector to finalize DPC durations.
		if h.perfinfoHandler != nil {
			_ = h.perfinfoHandler.HandleContextSwitchRaw(record)
		}
		return false // Stop processing, we've handled it.
	}

	return true // Continue to the next callback stage for all other events.
}

// // EventRecordHelperCallback is a second-stage callback, invoked after a helper
// // object has been created for an event record. It is currently a placeholder.
// func (h *EventHandler) EventRecordHelperCallback(helper *etw.EventRecordHelper) error {
// 	// Perform any helper-level processing here if needed
// 	return nil
// }

// EventPreparedCallback is the main entry point to the event routing logic for non-raw events.
func (h *EventHandler) EventPreparedCallback(helper *etw.EventRecordHelper) error {
	defer helper.Skip() // Stop further processing in goetw
	return h.RouteEvent(helper)
}

// RouteEvent routes events to appropriate handlers using a map lookup.
// This is the hot path for all non-CSwitch events.
func (h *EventHandler) RouteEvent(helper *etw.EventRecordHelper) error {
	eventRecord := helper.EventRec
	providerGUID := eventRecord.EventHeader.ProviderId

	// Increment provider-specific counter.
	if counter, ok := h.guidToCounter[providerGUID]; ok {
		counter.Add(1)
	}

	// Determine the event ID. For MOF events, this is the Opcode.
	var eventID uint16
	if eventRecord.IsMof() {
		eventID = uint16(eventRecord.EventHeader.EventDescriptor.Opcode)
	} else {
		eventID = eventRecord.EventHeader.EventDescriptor.Id
	}

	// Route the event using a direct map lookup.
	route := eventRoute{ProviderGUID: providerGUID, EventID: eventID}
	if handlerFunc, ok := h.routeMap[route]; ok {
		return handlerFunc(helper)
	}

	return nil
}

// EventCallback is called for higher-level event processing
func (h *EventHandler) EventCallback(event *etw.Event) error {
	// Perform any event-level processing here if needed
	return nil
}
