package statemanager

import (
	"etw_exporter/internal/maps"

	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// ServiceManager is responsible for mapping SubProcessTags to service names.
type ServiceManager struct {
	serviceTags maps.ConcurrentMap[uint32, string] // Key: SubProcessTag, Value: ServiceName
	log         *phusluadapter.SampledLogger
}

// newServiceManager creates and initializes a new ServiceManager.
func newServiceManager(log *phusluadapter.SampledLogger) *ServiceManager {
	return &ServiceManager{
		log:         log,
		serviceTags: maps.NewConcurrentMap[uint32, string](),
	}
}

// RegisterServiceTag stores a mapping between a SubProcessTag and a service name.
func (sm *ServiceManager) RegisterServiceTag(tag uint32, name string) {
	if tag > 0 && name != "" {
		sm.serviceTags.Store(tag, name)
	}
}

// resolveAndApplyServiceName checks if a process needs a service name and applies it.
// This is a one-time operation per process instance, triggered by the first thread
// that provides a valid SubProcessTag.
func (sm *ServiceManager) resolveAndApplyServiceName(pData *ProcessData, subProcessTag uint32) {
	if subProcessTag == 0 {
		return
	}

	// Certain system pseudo-processes like 'System' (PID 4) and 'MemCompression'
	// represent kernel activity. ETW events for threads under these processes may
	// carry SubProcessTags from the user-mode process that *caused* the kernel
	// activity. Applying a service name here would be incorrect attribution.
	// We prevent this by explicitly checking the process name.
	pData.Info.mu.RLock()
	name := pData.Info.Name
	pData.Info.mu.RUnlock()
	if name == "System" || name == "MemCompression" {
		sm.log.SampledDebug("service_tag_system").
			Str("process_name", name).
			Uint32("subprocess_tag", subProcessTag).
			Msg("Ignoring service tag for a system pseudo-process to prevent misattribution.")
		return
	}

	pData.Info.mu.Lock()
	// If the service name is already set, our work is done.
	if pData.Info.ServiceName != "" {
		pData.Info.mu.Unlock()
		return
	}
	// Store the tag if it's not already there.
	if pData.Info.SubProcessTag == 0 {
		pData.Info.SubProcessTag = subProcessTag
	} else if pData.Info.SubProcessTag != subProcessTag {
		// If the tag has changed, we log it. This is unexpected but not impossible.
		sm.log.Warn().
			Uint64("start_key", pData.Info.StartKey).
			Uint32("old_tag", pData.Info.SubProcessTag).
			Uint32("new_tag", subProcessTag).
			Msg("Process SubProcessTag changed during its lifetime")
		pData.Info.SubProcessTag = subProcessTag // Update to the new tag.
	}
	pData.Info.mu.Unlock()

	// At this point, we know ServiceName is empty, but we have a tag.
	// Try to resolve it from our cache.
	serviceName, ok := sm.serviceTags.Load(subProcessTag)
	if !ok {
		return // We don't have a mapping for this tag yet. Will be resolved during aggregation.
	}

	// Set the service name on the process instance.
	pData.Info.mu.Lock()
	// Double-check in case another thread set it while we were looking up the tag.
	if pData.Info.ServiceName == "" {
		pData.Info.ServiceName = serviceName
	}
	pData.Info.mu.Unlock()
}

// GetTagForService finds a service tag by its name from the internal cache.
// This is used by the one-time proactive enrichment process.
func (sm *ServiceManager) GetTagForService(name string) (uint32, bool) {
	var foundTag uint32
	var found bool
	sm.serviceTags.Range(func(tag uint32, serviceName string) bool {
		if serviceName == name {
			foundTag = tag
			found = true
			return false // Stop iteration
		}
		return true
	})
	return foundTag, found
}

// --- Debug Provider Methods ---

// GetServiceCount returns the current number of cached service tags.
func (sm *ServiceManager) GetServiceCount() int {
	var count int
	sm.serviceTags.Range(func(u uint32, s string) bool {
		count++
		return true
	})
	return count
}

// RangeServices iterates over services for the debug http handler.
func (sm *ServiceManager) RangeServices(f func(tag uint32, name string)) {
	sm.serviceTags.Range(func(tag uint32, name string) bool {
		f(tag, name)
		return true
	})
}
