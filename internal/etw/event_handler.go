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
	"etw_exporter/internal/collectors/kernelregistry"
	"etw_exporter/internal/collectors/kernelsysconfig"
	"etw_exporter/internal/collectors/kernelthread"
	"etw_exporter/internal/config"
	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"
)

// eventHandlerFunc is a generic function signature for all event handlers.
type eventHandlerFunc func(helper *etw.EventRecordHelper) error

// providerHandlers holds the routing table for a single provider.
// It uses a slice for fast lookups of common, low-numbered event IDs,
// and a map for rare, high-numbered event IDs to save memory.
type providerHandlers struct {
	slice       []eventHandlerFunc
	overflowMap map[uint16]eventHandlerFunc

	// Use slice for event IDs 0-255, map for others.
	maxSliceEventID uint16
}

func newProviderHandlers() *providerHandlers {
	return &providerHandlers{
		maxSliceEventID: 256,
	}
}

// get retrieves the handler for a given event ID, checking the fast-path slice first.
// This method is a candidate for inlining by the compiler.
//
//go:inline
func (pr *providerHandlers) get(eventID uint16) eventHandlerFunc {
	if eventID < pr.maxSliceEventID {
		if int(eventID) < len(pr.slice) {
			return pr.slice[eventID]
		}
		return nil // Not in slice, and by design, not in the overflow map.
	}

	// Fall back to the overflow map for high-numbered event IDs.
	if pr.overflowMap != nil {
		return pr.overflowMap[eventID] // Returns handler or nil if not found.
	}

	return nil
}

// SessionWatcher defines the interface for a session watcher handler.
type SessionWatcher interface {
	HandleSessionStop(helper *etw.EventRecordHelper) error
}

// EventHandler encapsulates state and logic for ETW event processing.
// This handler routes events to specialized sub-handlers based on a data-driven map.
type EventHandler struct {
	// Collectors for different metric categories
	diskHandler     *kerneldiskio.Handler
	threadHandler   *kernelthread.Handler
	perfinfoHandler *kernelperf.Handler
	networkHandler  *kernelnetwork.Handler
	memoryHandler   *kernelmemory.Handler
	registryHandler *kernelregistry.Handler

	// Shared state and caches for callbacks
	config       *config.CollectorConfig
	appConfig    *config.AppConfig            // Add AppConfig to access SessionWatcher settings
	log          *phusluadapter.SampledLogger // Event handler logger
	stateManager *statemanager.KernelStateManager

	// Event counters by provider - atomic counters for thread safety
	diskEventCount           atomic.Uint64
	processEventCount        atomic.Uint64
	threadEventCount         atomic.Uint64
	fileEventCount           atomic.Uint64
	perfinfoEventCount       atomic.Uint64
	systemConfigEventCount   atomic.Uint64
	imageEventCount          atomic.Uint64
	pageFaultEventCount      atomic.Uint64
	networkEventCount        atomic.Uint64
	registryEventCount       atomic.Uint64
	sessionWatcherEventCount atomic.Uint64

	// ROUTING TABLES for hot path optimized routing
	routeMap      map[etw.GUID]*providerHandlers
	guidToCounter map[etw.GUID]*atomic.Uint64
}

// NewEventHandler creates a new central event handler. It initializes all collectors
// that are enabled in the provided configuration, registers them for Prometheus
// metrics where applicable, and sets up the internal routing maps.
func NewEventHandler(appConfig *config.AppConfig) *EventHandler {
	eh := &EventHandler{
		config:        &appConfig.Collectors,
		appConfig:     appConfig,
		log:           logger.NewSampledLoggerCtx("event_handler"),
		routeMap:      make(map[etw.GUID]*providerHandlers),
		guidToCounter: make(map[etw.GUID]*atomic.Uint64),
	}

	// --- Core Component Initialization ---
	eh.stateManager = statemanager.GetGlobalStateManager()

	// Populate the GUID to counter map.
	eh.guidToCounter[*SystemConfigGUID] = &eh.systemConfigEventCount
	eh.guidToCounter[*MicrosoftWindowsKernelDiskGUID] = &eh.diskEventCount
	eh.guidToCounter[*MicrosoftWindowsKernelProcessGUID] = &eh.processEventCount
	eh.guidToCounter[*MicrosoftWindowsKernelFileGUID] = &eh.fileEventCount
	eh.guidToCounter[*ThreadKernelGUID] = &eh.threadEventCount
	eh.guidToCounter[*PerfInfoKernelGUID] = &eh.perfinfoEventCount
	eh.guidToCounter[*ImageKernelGUID] = &eh.imageEventCount
	eh.guidToCounter[*PageFaultKernelGUID] = &eh.pageFaultEventCount
	eh.guidToCounter[*MicrosoftWindowsKernelNetworkGUID] = &eh.networkEventCount
	eh.guidToCounter[*RegistryKernelGUID] = &eh.registryEventCount
	eh.guidToCounter[*MicrosoftWindowsKernelEventTracingGUID] = &eh.sessionWatcherEventCount

	// --- Register Routes for All Handlers ---

	// Always register the global process handler for process name mappings.
	// Provider: Microsoft-Windows-Kernel-Process ({22fb2cd6-0e7b-422b-a0c7-2fad1fd0e716})
	processHandler := kernelprocess.NewProcessHandler(eh.stateManager)
	eh.addRoute(*MicrosoftWindowsKernelProcessGUID, 1, processHandler.HandleProcessStart)    // ProcessStart
	eh.addRoute(*MicrosoftWindowsKernelProcessGUID, 15, processHandler.HandleProcessRundown) // ProcessRundown
	eh.addRoute(*MicrosoftWindowsKernelProcessGUID, 2, processHandler.HandleProcessEnd)      // ProcessStop
	eh.log.Debug().Msg("Registered global process handler routes")

	// Always register the system config handler.
	// Provider: NT Kernel Logger (EventTraceConfig) ({01853a65-418f-4f36-aefc-dc0f1d2fd235})
	systemConfigHandler := kernelsysconfig.NewSystemConfigHandler()
	eh.addRoute(*SystemConfigGUID, etw.EVENT_TRACE_TYPE_CONFIG_PHYSICALDISK, systemConfigHandler.HandleSystemConfigPhyDisk)
	eh.addRoute(*SystemConfigGUID, etw.EVENT_TRACE_TYPE_CONFIG_LOGICALDISK, systemConfigHandler.HandleSystemConfigLogDisk)
	eh.log.Debug().Msg("Registered global system config handler routes")

	// Initialize enabled collectors and register their routes.
	if eh.config.DiskIO.Enabled {
		eh.diskHandler = kerneldiskio.NewDiskIOHandler(&eh.config.DiskIO)
		prometheus.MustRegister(eh.diskHandler.GetCustomCollector())

		// Provider: Microsoft-Windows-Kernel-Disk ({c7bde69a-e1e0-4177-b6ef-283ad1525271})
		eh.addRoute(*MicrosoftWindowsKernelDiskGUID, etw.EVENT_TRACE_TYPE_IO_READ, eh.diskHandler.HandleDiskRead)   // DiskRead
		eh.addRoute(*MicrosoftWindowsKernelDiskGUID, etw.EVENT_TRACE_TYPE_IO_WRITE, eh.diskHandler.HandleDiskWrite) // DiskWrite
		eh.addRoute(*MicrosoftWindowsKernelDiskGUID, etw.EVENT_TRACE_TYPE_IO_FLUSH, eh.diskHandler.HandleDiskFlush) // DiskFlush

		// Provider: Microsoft-Windows-Kernel-File ({edd08927-9cc4-4e65-b970-c2560fb5c289})
		eh.addRoute(*MicrosoftWindowsKernelFileGUID, 12, eh.diskHandler.HandleFileCreate) // Create
		eh.addRoute(*MicrosoftWindowsKernelFileGUID, 14, eh.diskHandler.HandleFileClose)  // Close
		eh.addRoute(*MicrosoftWindowsKernelFileGUID, 15, eh.diskHandler.HandleFileRead)   // Read
		eh.addRoute(*MicrosoftWindowsKernelFileGUID, 16, eh.diskHandler.HandleFileWrite)  // Write
		eh.addRoute(*MicrosoftWindowsKernelFileGUID, 26, eh.diskHandler.HandleFileDelete) // DeletePath

		eh.log.Debug().Msg("Disk I/O collector enabled and routes registered")
		kernelsysconfig.GetGlobalSystemConfigCollector().RequestMetrics(
			kernelsysconfig.PhysicalDiskInfoMetricName,
			kernelsysconfig.LogicalDiskInfoMetricName,
		)
	}

	if eh.config.ThreadCS.Enabled {
		eh.threadHandler = kernelthread.NewThreadHandler(eh.stateManager)
		prometheus.MustRegister(eh.threadHandler.GetCustomCollector())

		// Provider: NT Kernel Logger (Thread) ({3d6fa8d1-fe05-11d0-9dda-00c04fd7ba7c})
		// Note: CSwitch (36) is handled in the raw EventRecordCallback for performance.
		eh.addRoute(*ThreadKernelGUID, 50, eh.threadHandler.HandleReadyThread)                            // ReadyThread
		eh.addRoute(*ThreadKernelGUID, etw.EVENT_TRACE_TYPE_START, eh.threadHandler.HandleThreadStart)    // ThreadStart
		eh.addRoute(*ThreadKernelGUID, etw.EVENT_TRACE_TYPE_DC_START, eh.threadHandler.HandleThreadStart) // ThreadRundown
		eh.addRoute(*ThreadKernelGUID, etw.EVENT_TRACE_TYPE_END, eh.threadHandler.HandleThreadEnd)        // ThreadEnd
		eh.addRoute(*ThreadKernelGUID, etw.EVENT_TRACE_TYPE_DC_END, eh.threadHandler.HandleThreadEnd)     // ThreadRundownEnd
		eh.log.Debug().Msg("ThreadCS collector enabled and routes registered")
	}

	if eh.config.PerfInfo.Enabled {
		eh.perfinfoHandler = kernelperf.NewPerfInfoHandler(&eh.config.PerfInfo)
		prometheus.MustRegister(eh.perfinfoHandler.GetCustomCollector())

		// Provider: NT Kernel Logger (PerfInfo) ({ce1dbfb4-137e-4da6-87b0-3f59aa102cbc})
		eh.addRoute(*PerfInfoKernelGUID, 67, eh.perfinfoHandler.HandleISREvent)           // ISR
		eh.addRoute(*PerfInfoKernelGUID, 66, eh.perfinfoHandler.HandleDPCEvent)           // ThreadDPC
		eh.addRoute(*PerfInfoKernelGUID, 68, eh.perfinfoHandler.HandleDPCEvent)           // DPC
		eh.addRoute(*PerfInfoKernelGUID, 69, eh.perfinfoHandler.HandleDPCEvent)           // TimerDPC
		eh.addRoute(*PerfInfoKernelGUID, 46, eh.perfinfoHandler.HandleSampleProfileEvent) // SampleProfile

		// Provider: NT Kernel Logger (Image) ({2cb15d1d-5fc1-11d2-abe1-00a0c911f518})
		eh.addRoute(*ImageKernelGUID, etw.EVENT_TRACE_TYPE_LOAD, eh.perfinfoHandler.HandleImageLoadEvent)     // Image Load
		eh.addRoute(*ImageKernelGUID, etw.EVENT_TRACE_TYPE_DC_START, eh.perfinfoHandler.HandleImageLoadEvent) // Image Rundown
		eh.addRoute(*ImageKernelGUID, etw.EVENT_TRACE_TYPE_DC_END, eh.perfinfoHandler.HandleImageLoadEvent)   // Image Rundown End
		eh.addRoute(*ImageKernelGUID, etw.EVENT_TRACE_TYPE_END, eh.perfinfoHandler.HandleImageUnloadEvent)    // Image Unload

		// PerfInfo also needs CSwitch events to finalize DPC durations. This is handled
		// in the raw EventRecordCallback, which calls perfinfoHandler.HandleContextSwitchRaw.
		eh.log.Debug().Msg("PerfInfo collector enabled and routes registered")
	}

	if eh.config.Network.Enabled {
		eh.networkHandler = kernelnetwork.NewNetworkHandler(&eh.config.Network)
		prometheus.MustRegister(eh.networkHandler.GetCustomCollector())

		// Provider: Microsoft-Windows-Kernel-Network ({7dd42a49-5329-4832-8dfd-43d979153a88})
		eh.addRoute(*MicrosoftWindowsKernelNetworkGUID, 10, eh.networkHandler.HandleTCPDataSent)            // TCP Send (IPv4)
		eh.addRoute(*MicrosoftWindowsKernelNetworkGUID, 26, eh.networkHandler.HandleTCPDataSent)            // TCP Send (IPv6)
		eh.addRoute(*MicrosoftWindowsKernelNetworkGUID, 11, eh.networkHandler.HandleTCPDataReceived)        // TCP Recv (IPv4)
		eh.addRoute(*MicrosoftWindowsKernelNetworkGUID, 27, eh.networkHandler.HandleTCPDataReceived)        // TCP Recv (IPv6)
		eh.addRoute(*MicrosoftWindowsKernelNetworkGUID, 12, eh.networkHandler.HandleTCPConnectionAttempted) // TCP Connect (IPv4)
		eh.addRoute(*MicrosoftWindowsKernelNetworkGUID, 28, eh.networkHandler.HandleTCPConnectionAttempted) // TCP Connect (IPv6)
		eh.addRoute(*MicrosoftWindowsKernelNetworkGUID, 15, eh.networkHandler.HandleTCPConnectionAccepted)  // TCP Accept (IPv4)
		eh.addRoute(*MicrosoftWindowsKernelNetworkGUID, 31, eh.networkHandler.HandleTCPConnectionAccepted)  // TCP Accept (IPv6)
		eh.addRoute(*MicrosoftWindowsKernelNetworkGUID, 14, eh.networkHandler.HandleTCPDataRetransmitted)   // TCP Retransmit (IPv4)
		eh.addRoute(*MicrosoftWindowsKernelNetworkGUID, 30, eh.networkHandler.HandleTCPDataRetransmitted)   // TCP Retransmit (IPv6)
		eh.addRoute(*MicrosoftWindowsKernelNetworkGUID, 17, eh.networkHandler.HandleTCPConnectionFailed)    // TCP Connect Failed
		eh.addRoute(*MicrosoftWindowsKernelNetworkGUID, 42, eh.networkHandler.HandleUDPDataSent)            // UDP Send (IPv4)
		eh.addRoute(*MicrosoftWindowsKernelNetworkGUID, 58, eh.networkHandler.HandleUDPDataSent)            // UDP Send (IPv6)
		eh.addRoute(*MicrosoftWindowsKernelNetworkGUID, 43, eh.networkHandler.HandleUDPDataReceived)        // UDP Recv (IPv4)
		eh.addRoute(*MicrosoftWindowsKernelNetworkGUID, 59, eh.networkHandler.HandleUDPDataReceived)        // UDP Recv (IPv6)
		eh.addRoute(*MicrosoftWindowsKernelNetworkGUID, 49, eh.networkHandler.HandleUDPConnectionFailed)    // UDP Connect Failed
		eh.log.Debug().Msg("Network collector enabled and routes registered")
	}

	if eh.config.Memory.Enabled {
		eh.memoryHandler = kernelmemory.NewMemoryHandler(&eh.config.Memory, eh.stateManager)
		prometheus.MustRegister(eh.memoryHandler.GetCustomCollector())

		// Provider: NT Kernel Logger (PageFault) ({3d6fa8d3-fe05-11d0-9dda-00c04fd7ba7c})
		eh.addRoute(*PageFaultKernelGUID, 32, eh.memoryHandler.HandleMofHardPageFaultEvent) // HardFault
		eh.log.Debug().Msg("Memory collector enabled and routes registered")
	}

	if eh.config.Registry.Enabled {
		eh.registryHandler = kernelregistry.NewRegistryHandler(&eh.config.Registry)
		prometheus.MustRegister(eh.registryHandler.GetCustomCollector())

		// Provider: NT Kernel Logger (Registry) ({ae53722e-c863-11d2-8659-00c04fa321a1})
		// NOTE: Registry events are now handled in the raw EventRecordCallback for performance.
		// The routing map is no longer used for this provider.
		eh.log.Debug().Msg("Registry collector enabled (raw handling)")
	}

	// --- Sentinel Collector Registration ---
	// This collector MUST be registered LAST. Its purpose is to trigger a cleanup
	// of terminated entities in the KernelStateManager AFTER all other collectors
	// have completed their scrape.
	prometheus.MustRegister(statemanager.NewStateCleanupCollector())
	eh.log.Debug().Msg("Registered post-scrape state cleanup collector")

	eh.log.Debug().Int("total_routes", len(eh.routeMap)).Msg("Event handler initialized")
	return eh
}

// addRoute is a helper to simplify adding entries to the nested route map.
func (h *EventHandler) addRoute(guid etw.GUID, eventID uint16, handler eventHandlerFunc) {
	routes, ok := h.routeMap[guid]
	if !ok {
		routes = newProviderHandlers() // should set maxSliceEventID = 256
		routes.maxSliceEventID = uint16(256) // just in case some refactors don't.
		h.routeMap[guid] = routes
	}

	if eventID < routes.maxSliceEventID {
		// Use the fast-path slice
		if int(eventID) >= len(routes.slice) {
			// Grow slice to accommodate the new eventID
			newSlice := make([]eventHandlerFunc, eventID+1)
			copy(newSlice, routes.slice)
			routes.slice = newSlice
		}
		routes.slice[eventID] = handler
	} else {
		// Use the overflow map for high-numbered or sparse event IDs (saves some kilobytes of mem)
		if routes.overflowMap == nil {
			routes.overflowMap = make(map[uint16]eventHandlerFunc)
		}
		routes.overflowMap[eventID] = handler
	}
}

// RegisterWatcherRoutes registers the event routes for the session watcher.
func (h *EventHandler) RegisterWatcherRoutes(watcher SessionWatcher) {
	h.addRoute(*MicrosoftWindowsKernelEventTracingGUID, 11, watcher.HandleSessionStop) // Session Stop
	h.log.Debug().Msg("Registered session watcher handler routes")
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
func (h *EventHandler) GetRegistryEventCount() uint64     { return h.registryEventCount.Load() }
func (h *EventHandler) GetSessionWatcherEventCount() uint64 {
	return h.sessionWatcherEventCount.Load()
}

// ETW Callback Methods

// EventRecordCallback is the first-stage, hot-path callback for every event record.
// It performs fast-path filtering for CSwitch events and routes them to raw handlers,
// bypassing more expensive processing. It returns 'false' to stop further processing.
func (h *EventHandler) EventRecordCallback(record *etw.EventRecord) bool {
	providerID := record.EventHeader.ProviderId

	// --- Fast Path ---
	// CSwitch Event: {3d6fa8d1-fe05-11d0-9dda-00c04fd7ba7c}, Opcode 36
	// This is the fastest path, handling raw events directly.
	if providerID.Equals(ThreadKernelGUID) &&
		record.EventHeader.EventDescriptor.Opcode == 36 {

		h.threadEventCount.Add(1)

		// Route to thread collector for context switch metrics.
		if h.threadHandler != nil {
			_ = h.threadHandler.HandleContextSwitchRaw(record)
		}
		// Route to perfinfo collector to finalize DPC durations.
		if h.perfinfoHandler != nil {
			_ = h.perfinfoHandler.HandleContextSwitchRaw(record)
		}
		return false // Stop processing, we've handled it.
	}

	// Registry Event: {ae53722e-c863-11d2-8659-00c04fa321a1}
	// This is another high-frequency provider, handled via fast path.
	if providerID.Equals(RegistryKernelGUID) {
		h.registryEventCount.Add(1)

		if h.registryHandler != nil {
			_ = h.registryHandler.HandleRegistryEventRaw(record)
		}
		return false // Stop processing, we've handled it.
	}

	// TODO.
	// Determine the event ID. For MOF events, this is the Opcode.
	// var eventID uint16
	// if eventRecord.IsMof() {
	// 	eventID = uint16(eventRecord.EventHeader.EventDescriptor.Opcode)
	// } else {
	// 	eventID = eventRecord.EventHeader.EventDescriptor.Id
	// }
	// --- Scalable Path (Hypothetical for other raw events) ---
	// If I had more raw providers, I could add a map lookup here as a fallback.
	// if rawProviderHandlers, ok := h.rawRouteMap[providerID]; ok {
	// 	if handler, ok := rawProviderHandlers[eventID]; ok {
	// 		handler(record)
	// 		return false // Stop processing
	// 	}
	// }

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
	eventID := eventRecord.EventID()

	// Route the event
	if handlers, ok := h.routeMap[providerGUID]; ok {
		if handlerFunc := handlers.get(eventID); handlerFunc != nil {
			return handlerFunc(helper)
		}
	}

	return nil
}

// EventCallback is called for higher-level event processing
func (h *EventHandler) EventCallback(event *etw.Event) error {
	// Perform any event-level processing here if needed
	return nil
}
