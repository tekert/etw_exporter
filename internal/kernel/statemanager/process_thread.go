package statemanager

import "sync/atomic"

// ThreadModule holds all thread-related metrics for a single process instance.
// It is designed for high-performance, concurrent writes from event handlers.
type ThreadModule struct {
	ContextSwitches atomic.Uint64
}

// Reset zeroes out the counters, making the module ready for reuse.
func (tm *ThreadModule) Reset() {
	tm.ContextSwitches.Store(0)
}

// newThreadModule creates and initializes a new ThreadModule.
func newThreadModule() *ThreadModule {
	return &ThreadModule{}
}

// RecordContextSwitch is the new home for the per-process context switch recording logic.
// It is optimized to be lock-free and allocation-free on the hot path.
func (pd *ProcessData) RecordContextSwitch() {
	// With the module pre-allocated, this is just a single, lock-free atomic operation.
	// The lazy-loading checks have been removed for maximum performance.
	pd.Threads.ContextSwitches.Add(1)
}
