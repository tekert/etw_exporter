package statemanager

import (
	"sync/atomic"
)

// MemoryModule holds all memory-related metrics for a single process instance.
// It is designed for high-performance, concurrent writes from event handlers.
type MemoryModule struct {
	// Contains the atomic counters for memory events.
	Metrics MemoryMetrics
}

// MemoryMetrics contains the atomic counters for memory-related events.
type MemoryMetrics struct {
	HardPageFaults atomic.Uint64
}

// Reset zeroes out the counters, making the module ready for reuse.
func (mm *MemoryModule) Reset() {
	mm.Metrics.HardPageFaults.Store(0)
}

// newMemoryModule creates and initializes a new MemoryModule.
func newMemoryModule() *MemoryModule {
	return &MemoryModule{}
}

// RecordHardPageFault increments the hard page fault counter by one.
func (pd *ProcessData) RecordHardPageFault() {
	pd.Memory.Metrics.HardPageFaults.Add(1)
}

// RecordHardPageFaults increments the hard page fault counter by a given amount.
// This is used to transfer pending metrics.
func (pd *ProcessData) RecordHardPageFaults(count uint64) {
	pd.Memory.Metrics.HardPageFaults.Add(count)
}

// RecordHardPageFaultForThread has been moved to the new system_state.go file.
// The KernelStateManager facade will delegate calls to it.
// This keeps all global, cross-process attribution logic in one place.
