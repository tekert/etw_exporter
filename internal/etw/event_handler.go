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
	"etw_exporter/internal/etw/guids"
	"etw_exporter/internal/etw/handlers"
	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"
)

// handlerUnion holds either a raw or a prepared event handler.
// This allows storing both types in the same routing table while maintaining
// type safety and avoiding interface-based dynamic dispatch.
type handlerUnion struct {
	preparedHandler handlers.EventHandlerFunc
	rawHandler      handlers.RawEventHandlerFunc
}

// providerHandlers holds the routing table for a single provider.
// It uses a slice for fast lookups of common, low-numbered event IDs,
// and a map for rare, high-numbered event IDs to save memory.
type providerHandlers struct {
	slice       []handlerUnion
	overflowMap map[uint16]handlerUnion

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
func (pr *providerHandlers) get(eventID uint16) handlerUnion {
	if eventID < pr.maxSliceEventID {
		if int(eventID) < len(pr.slice) {
			return pr.slice[eventID]
		}
		return handlerUnion{} // Not in slice, and by design, not in the overflow map.
	}

	// Fall back to the overflow map for high-numbered event IDs.
	if pr.overflowMap != nil {
		return pr.overflowMap[eventID] // Returns handler or zero value if not found.
	}

	return handlerUnion{}
}

// SessionWatcher defines the interface for a session watcher handler.
type SessionWatcher interface {
	HandleSessionStop(helper *etw.EventRecordHelper) error
}

// EventHandler encapsulates state and logic for ETW event processing.
// This handler routes events to specialized sub-handlers based on a data-driven map.
type EventHandler struct {
	// Handlers are foundational services, always instantiated.
	processHandler   *kernelprocess.Handler
	diskHandler      *kerneldiskio.Handler
	threadHandler    *kernelthread.Handler
	perfinfoHandler  *kernelperf.Handler
	networkHandler   *kernelnetwork.Handler
	memoryHandler    *kernelmemory.Handler
	registryHandler  *kernelregistry.Handler
	sysconfigHandler *kernelsysconfig.Handler

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
	eh.guidToCounter[*guids.SystemConfigGUID] = &eh.systemConfigEventCount
	eh.guidToCounter[*guids.MicrosoftWindowsKernelDiskGUID] = &eh.diskEventCount
	eh.guidToCounter[*guids.DiskIOKernelGUID] = &eh.diskEventCount // MOF provider uses the same counter
	eh.guidToCounter[*guids.MicrosoftWindowsKernelProcessGUID] = &eh.processEventCount
	eh.guidToCounter[*guids.MicrosoftWindowsKernelFileGUID] = &eh.fileEventCount
	eh.guidToCounter[*guids.ThreadKernelGUID] = &eh.threadEventCount
	eh.guidToCounter[*guids.PerfInfoKernelGUID] = &eh.perfinfoEventCount
	eh.guidToCounter[*guids.ImageKernelGUID] = &eh.imageEventCount
	eh.guidToCounter[*guids.PageFaultKernelGUID] = &eh.pageFaultEventCount
	eh.guidToCounter[*guids.MicrosoftWindowsKernelNetworkGUID] = &eh.networkEventCount
	eh.guidToCounter[*guids.RegistryKernelGUID] = &eh.registryEventCount
	eh.guidToCounter[*guids.MicrosoftWindowsKernelEventTracingGUID] = &eh.sessionWatcherEventCount

	// --- Register Routes for All Handlers ---

	// Always register the global process handler for process name mappings.
	// Provider: Microsoft-Windows-Kernel-Process ({22fb2cd6-0e7b-422b-a0c7-2fad1fd0e716})
	eh.processHandler = kernelprocess.NewProcessHandler(eh.stateManager)
	eh.processHandler.RegisterRoutes(eh)

	// Always register the system config handler.
	// Provider: NT Kernel Logger (EventTraceConfig) ({01853a65-418f-4f36-aefc-dc0f1d2fd235})
	eh.sysconfigHandler = kernelsysconfig.NewSystemConfigHandler(eh.stateManager)
	eh.sysconfigHandler.RegisterRoutes(eh)

	// Helper to Register the thread handler for thread name mappings.
	// Provider: NT Kernel Logger (Thread) ({3d6fa8d1-fe05-11d0-9dda-00c04fd7ba7c})
	eh.threadHandler = kernelthread.NewThreadHandler(eh.stateManager)
	eh.threadHandler.RegisterRoutes(eh)

	eh.diskHandler = kerneldiskio.NewDiskIOHandler(&eh.config.DiskIO, eh.stateManager)
	eh.diskHandler.RegisterRoutes(eh)

	eh.perfinfoHandler = kernelperf.NewPerfInfoHandler(&eh.config.PerfInfo, eh.stateManager)
	eh.perfinfoHandler.RegisterRoutes(eh)

	eh.networkHandler = kernelnetwork.NewNetworkHandler(&eh.config.Network, eh.stateManager)
	eh.networkHandler.RegisterRoutes(eh)

	eh.memoryHandler = kernelmemory.NewMemoryHandler(&eh.config.Memory, eh.stateManager)
	eh.memoryHandler.RegisterRoutes(eh)

	eh.registryHandler = kernelregistry.NewRegistryHandler(&eh.config.Registry, eh.stateManager)
	//eh.registryHandler.RegisterRoutes(eh)
	// NOTE: Registry events are now handled in the raw EventRecordCallback for performance.
	// The routing map is no longer used for this provider.

	// Register process info collector if enabled. This provides the lookup metric for other collectors.
	if eh.config.Process.Enabled {
		collector := kernelprocess.NewProcessCollector(&eh.config.Process, eh.stateManager)
		prometheus.MustRegister(collector)
		eh.log.Debug().Msg("Process info collector enabled.")
	}

	// Initialize enabled collectors and register their routes.
	if eh.config.DiskIO.Enabled {
		collector := kerneldiskio.NewDiskIOCustomCollector(&eh.config.DiskIO, eh.stateManager)
		eh.diskHandler.AttachCollector(collector) // Attach it to the existing handler
		prometheus.MustRegister(collector)

		// Request initial metrics for physical and logical disks.
		eh.sysconfigHandler.GetCollector().RequestMetrics(
			kernelsysconfig.PhysicalDiskInfoMetricName,
			kernelsysconfig.LogicalDiskInfoMetricName,
		)
		eh.log.Debug().Msg("DiskIO collector enabled and attached.")
	}

	if eh.config.ThreadCS.Enabled {
		collector := kernelthread.NewThreadCSCollector(&eh.config.ThreadCS, eh.stateManager)
		eh.threadHandler.AttachCollector(collector) // Attach it to the existing handler
		prometheus.MustRegister(collector)
		eh.log.Debug().Msg("ThreadCS collector enabled and attached.")
	}

	if eh.config.PerfInfo.Enabled {
		collector := kernelperf.NewPerfInfoCollector(&eh.config.PerfInfo, eh.stateManager)
		eh.perfinfoHandler.AttachCollector(collector)
		prometheus.MustRegister(collector)
		eh.log.Debug().Msg("PerfInfo collector enabled and attached.")
	}

	if eh.config.Network.Enabled {
		collector := kernelnetwork.NewNetworkCollector(&eh.config.Network, eh.stateManager)
		eh.networkHandler.AttachCollector(collector)
		prometheus.MustRegister(collector)
		eh.log.Debug().Msg("Network collector enabled and attached.")
	}

	if eh.config.Memory.Enabled {
		collector := kernelmemory.NewMemoryCollector(&eh.config.Memory, eh.stateManager)
		eh.memoryHandler.AttachCollector(collector)
		prometheus.MustRegister(collector)
		eh.log.Debug().Msg("Memory collector enabled and attached.")
	}

	if eh.config.Registry.Enabled {
		collector := kernelregistry.NewRegistryCollector(&eh.config.Registry, eh.stateManager)
		eh.registryHandler.AttachCollector(collector)
		prometheus.MustRegister(collector)
		eh.log.Debug().Msg("Registry collector enabled (raw handling)")
	}

	eh.log.Debug().Int("total_routes", len(eh.routeMap)).Msg("Event handler initialized")
	return eh
}

// _addRoute is the private helper to add a handlerUnion to the route map.
// It handles the creation of provider-specific maps and the slice-vs-map logic.
func (h *EventHandler) _addRoute(guid etw.GUID, eventID uint16, handler handlerUnion) {
	routes, ok := h.routeMap[guid]
	if !ok {
		routes = newProviderHandlers()
		h.routeMap[guid] = routes
	}

	if eventID < routes.maxSliceEventID {
		// Use the fast-path slice
		if int(eventID) >= len(routes.slice) {
			// Grow slice to accommodate the new eventID
			newSlice := make([]handlerUnion, eventID+1)
			copy(newSlice, routes.slice)
			routes.slice = newSlice
		}
		routes.slice[eventID] = handler
	} else {
		// Use the overflow map for high-numbered or sparse event IDs
		if routes.overflowMap == nil {
			routes.overflowMap = make(map[uint16]handlerUnion)
		}
		routes.overflowMap[eventID] = handler
	}
}

// TODO: What if i want to register a route but for ANY id? (e.g., all events from a provider)

// AddRoute adds a handler for a standard, "prepared" event that uses an EventRecordHelper.
func (h *EventHandler) AddRoute(guid etw.GUID, eventID uint16, handler handlers.EventHandlerFunc) {
	h._addRoute(guid, eventID, handlerUnion{preparedHandler: handler})
}

// AddRawRoute adds a handler for a high-performance, "raw" event that uses an EventRecord.
func (h *EventHandler) AddRawRoute(guid etw.GUID, eventID uint16, handler handlers.RawEventHandlerFunc) {
	h._addRoute(guid, eventID, handlerUnion{rawHandler: handler})
}

// RegisterWatcherRoutes registers the event routes for the session watcher.
func (h *EventHandler) RegisterWatcherRoutes(watcher SessionWatcher) {
	h.AddRoute(*guids.MicrosoftWindowsKernelEventTracingGUID, 11, watcher.HandleSessionStop) // Session Stop
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
// It performs fast-path filtering for events and routes them to raw handlers,
// bypassing more expensive processing. It returns 'false' to stop further processing.
func (h *EventHandler) EventRecordCallback(record *etw.EventRecord) bool {
	providerID := record.EventHeader.ProviderId

	// --- Fast Path ---
	// CSwitch Event: {3d6fa8d1-fe05-11d0-9dda-00c04fd7ba7c}, Opcode 36
	// This is the fastest path, handling raw events directly.
	if (providerID.Equals(guids.ThreadKernelGUID) ||
		providerID.Equals(etw.SystemSchedulerProviderGuid)) &&
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
	if providerID.Equals(guids.RegistryKernelGUID) ||
		providerID.Equals(etw.SystemRegistryProviderGuid) {

		h.registryEventCount.Add(1)

		if h.registryHandler != nil {
			_ = h.registryHandler.HandleRegistryEventRaw(record)
		}
		return false // Stop processing, we've handled it.
	}

	// TODO: finish porting all known system collectors to use the raw handler
	eventID := record.EventID()
	// Route the rest of the registered raw events
	if handlers, ok := h.routeMap[providerID]; ok {
		if handler := handlers.get(eventID); handler.rawHandler != nil {
			// Increment counter for raw-routed events.
			if counter, ok := h.guidToCounter[providerID]; ok {
				counter.Add(1)
			}
			handler.rawHandler(record)
			return false // Stop processing
		}
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
	eventID := eventRecord.EventID()

	// Route the event
	if handlers, ok := h.routeMap[providerGUID]; ok {
		if handler := handlers.get(eventID); handler.preparedHandler != nil {
			return handler.preparedHandler(helper)
		}
	}

	return nil
}

// EventCallback is called for higher-level event processing
func (h *EventHandler) EventCallback(event *etw.Event) error {
	// Perform any event-level processing here if needed
	return nil
}
