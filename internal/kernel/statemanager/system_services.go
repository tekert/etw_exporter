package statemanager

// RegisterServiceTag stores a mapping between a SubProcessTag and a service name.
// This is called by the SystemConfig collector when it discovers service information.
func (sm *SystemState) RegisterServiceTag(tag uint32, name string) {
	if tag > 0 && name != "" {
		sm.serviceTags.Store(tag, name)
	}
}

// resolveAndApplyServiceName checks if a process needs a service name and applies it.
// This is a one-time operation per process instance, triggered by the first thread
// that provides a valid SubProcessTag.
func (sm *SystemState) resolveAndApplyServiceName(pData *ProcessData, subProcessTag uint32) {
	if subProcessTag == 0 {
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
		pData.Info.ServiceName = serviceName // ! TESTING
	}
	pData.Info.mu.Unlock()
}

// GetServiceCount returns the current number of cached service tags.
func (ss *SystemState) GetServiceCount() int {
	var count int
	ss.serviceTags.Range(func(u uint32, s string) bool {
		count++
		return true
	})
	return count
}

// RangeServices iterates over services for the debug http handler.
func (ss *SystemState) RangeServices(f func(tag uint32, name string)) {
	ss.serviceTags.Range(func(tag uint32, name string) bool {
		f(tag, name)
		return true
	})
}
