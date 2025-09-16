package handlers

import (
	"github.com/tekert/goetw/etw"
)

// EventHandlerFunc is a generic function signature for all event handlers.
type EventHandlerFunc func(helper *etw.EventRecordHelper) error

// RawEventHandlerFunc is a function signature for raw event handlers.
type RawEventHandlerFunc func(record *etw.EventRecord) error

// This interface defines a common interface for registering routes from the handlers to the event handler.
// So not to cause cicle imports, we define it here and use it in the handlers.
type Router interface {
	AddRoute(guid etw.GUID, id uint16, handler EventHandlerFunc)
	AddRawRoute(guid etw.GUID, id uint16, handler RawEventHandlerFunc)
}

// RegisterRoutesForGUID registers multiple event handlers for a specific provider GUID.
func RegisterRoutesForGUID(router Router, guid *etw.GUID, routes map[uint8]EventHandlerFunc) {
	for eventType, handler := range routes {
		router.AddRoute(*guid, uint16(eventType), handler)
	}
}

// RegisterRawRoutesForGUID registers multiple raw event handlers for a specific provider GUID.
func RegisterRawRoutesForGUID(router Router, guid *etw.GUID, routes map[uint8]RawEventHandlerFunc) {
	for eventType, handler := range routes {
		router.AddRawRoute(*guid, uint16(eventType), handler)
	}
}
