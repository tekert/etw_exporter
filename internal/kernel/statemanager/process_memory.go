package statemanager

import "sync/atomic"

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

// RecordHardPageFault is the new home for the hard page fault recording logic.
// It's a method on ProcessData, designed for high-performance, concurrent access.
func (pd *ProcessData) RecordHardPageFault() {
	pd.Memory.Metrics.HardPageFaults.Add(1)
}
