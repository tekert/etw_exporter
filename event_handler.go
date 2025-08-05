package main

import (
	"github.com/tekert/golang-etw/etw"
)

// Event handler interfaces for different event types
type DiskEventHandler interface {
	HandleDiskRead(helper *etw.EventRecordHelper) error
	HandleDiskWrite(helper *etw.EventRecordHelper) error
	HandleDiskFlush(helper *etw.EventRecordHelper) error
	HandleSystemConfigPhyDisk(helper *etw.EventRecordHelper) error
	HandleSystemConfigLogDisk(helper *etw.EventRecordHelper) error
}

type ProcessEventHandler interface {
	HandleProcessStart(helper *etw.EventRecordHelper) error
	HandleProcessEnd(helper *etw.EventRecordHelper) error
}

type ContextSwitchEventHandler interface {
	HandleContextSwitch(helper *etw.EventRecordHelper) error
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

// EventHandler encapsulates state and logic for ETW event processing
// This struct-based approach allows reusability of eventID, helper, and other components
// for better code organization and future extensibility
type EventHandler struct {
	// Collectors for different metric categories
	diskCollector          *DiskIOCollector
	contextSwitchCollector *ContextSwitchCollector
	// Future collectors will be added here:
	// networkCollector *NetworkCollector
	// memoryCollector  *MemoryCollector
	// cpuCollector     *CPUCollector

	// Shared state and caches for callbacks
	metrics *ETWMetrics
	config  *AppCollectorConfig

	// Routing tables for different event types - hot path optimized
	diskEventHandlers     []DiskEventHandler
	processEventHandlers  []ProcessEventHandler
	contextSwitchHandlers []ContextSwitchEventHandler
	fileEventHandlers     []FileEventHandler
}

// NewEventHandler creates a new handler with dependencies injected
func NewEventHandler(metrics *ETWMetrics, config *AppCollectorConfig) *EventHandler {
	handler := &EventHandler{
		metrics:               metrics,
		config:                config,
		diskEventHandlers:     make([]DiskEventHandler, 0),
		processEventHandlers:  make([]ProcessEventHandler, 0),
		contextSwitchHandlers: make([]ContextSwitchEventHandler, 0),
		fileEventHandlers:     make([]FileEventHandler, 0),
	}

	// Initialize enabled collectors based on configuration
	if config.DiskIO.Enabled {
		handler.diskCollector = NewDiskIOCollector()
		// Register the disk collector with the handler
		handler.RegisterDiskEventHandler(handler.diskCollector)
		handler.RegisterProcessEventHandler(handler.diskCollector)

		// Register for file events if FileObject mapping is enabled
		if config.DiskIO.TrackFileMapping {
			handler.RegisterFileEventHandler(handler.diskCollector)
		}
	}

	if config.ContextSwitch.Enabled {
		handler.contextSwitchCollector = NewContextSwitchCollector(metrics)
		// Register the context switch collector with the handler
		handler.RegisterContextSwitchEventHandler(handler.contextSwitchCollector)
	}

	return handler
}

// Register methods for event handlers
func (h *EventHandler) RegisterDiskEventHandler(handler DiskEventHandler) {
	h.diskEventHandlers = append(h.diskEventHandlers, handler)
}

func (h *EventHandler) RegisterProcessEventHandler(handler ProcessEventHandler) {
	h.processEventHandlers = append(h.processEventHandlers, handler)
}

func (h *EventHandler) RegisterContextSwitchEventHandler(handler ContextSwitchEventHandler) {
	h.contextSwitchHandlers = append(h.contextSwitchHandlers, handler)
}

func (h *EventHandler) RegisterFileEventHandler(handler FileEventHandler) {
	h.fileEventHandlers = append(h.fileEventHandlers, handler)
}

// ETW Callback Methods - these satisfy the ETW consumer callback interface
// Using struct methods allows us to share state and reduce parameter passing

// EventRecordCallback is called for each EVENT_RECORD
func (h *EventHandler) EventRecordCallback(record *etw.EventRecord) bool {
	// Perform any record-level processing here if needed
	// Return true to continue processing, false to stop
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
	if providerGUID.Equals(MicrosoftWindowsKernelProcessGUID) && h.config.DiskIO.Enabled {
		return h.routeProcessEvents(helper, eventID)
	}

	// Route file events from Microsoft-Windows-Kernel-File
	if providerGUID.Equals(MicrosoftWindowsKernelFileGUID) && h.config.DiskIO.TrackFileMapping {
		return h.routeFileEvents(helper, eventID)
	}

	// Route context switch events from Thread kernel provider
	if providerGUID.Equals(ThreadKernelGUID) && h.config.ContextSwitch.Enabled {
		return h.routeContextSwitchEvents(helper, eventID)
	}

	return nil
}

// routeSystemConfigEvents routes SystemConfig events to disk handlers
func (h *EventHandler) routeSystemConfigEvents(helper *etw.EventRecordHelper, eventID uint16) error {
	if !h.config.DiskIO.Enabled || !h.config.DiskIO.TrackDiskInfo || len(h.diskEventHandlers) == 0 {
		return nil
	}

	switch eventID {
	case 11: // EVENT_TRACE_TYPE_CONFIG_PHYSICALDISK
		for _, handler := range h.diskEventHandlers {
			if err := handler.HandleSystemConfigPhyDisk(helper); err != nil {
				return err
			}
		}
	case 12: // EVENT_TRACE_TYPE_CONFIG_LOGICALDISK
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
	case 10: // DiskIo Read completion
		for _, handler := range h.diskEventHandlers {
			if err := handler.HandleDiskRead(helper); err != nil {
				return err
			}
		}
	case 11: // DiskIo Write completion
		for _, handler := range h.diskEventHandlers {
			if err := handler.HandleDiskWrite(helper); err != nil {
				return err
			}
		}
	case 14: // DiskIo Flush
		for _, handler := range h.diskEventHandlers {
			if err := handler.HandleDiskFlush(helper); err != nil {
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

// routeContextSwitchEvents routes context switch events to all registered handlers
func (h *EventHandler) routeContextSwitchEvents(helper *etw.EventRecordHelper, eventID uint16) error {
	if len(h.contextSwitchHandlers) == 0 {
		return nil
	}

	switch eventID {
	case 36: // Context Switch
		for _, handler := range h.contextSwitchHandlers {
			if err := handler.HandleContextSwitch(helper); err != nil {
				return err
			}
		}
	case 1, 3: // Thread Start, DCStart
		for _, handler := range h.contextSwitchHandlers {
			if err := handler.HandleThreadStart(helper); err != nil {
				return err
			}
		}
	case 2, 4: // Thread End, DCEnd
		for _, handler := range h.contextSwitchHandlers {
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
