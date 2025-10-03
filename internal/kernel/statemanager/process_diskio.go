package statemanager

import (
	"sync"
	"sync/atomic"
)

// DiskOperation defines the type for disk I/O operations.
type DiskOperation int

const (
	// DiskOpRead represents a read operation.
	DiskOpRead DiskOperation = iota
	// DiskOpWrite represents a write operation.
	DiskOpWrite
	// DiskOpFlush represents a flush operation.
	DiskOpFlush
	// opCount is an constant that defines the number of operations.
	DiskOpCount
)

// DiskOpToString maps DiskOperation constants to their string representations for Prometheus labels.
var DiskOpToString = [DiskOpCount]string{
	DiskOpRead:  "read",
	DiskOpWrite: "write",
	DiskOpFlush: "flush",
}

// DiskModule holds all disk-related metrics for a single process instance.
// It is designed for high-performance, concurrent writes from event handlers.
type DiskModule struct {
	// Keyed by the physical disk number.
	Disks map[uint32]*DiskMetrics
	mu    sync.RWMutex // Protects the Disks map.
}

// Reset clears the maps and counters, making the module ready for reuse.
func (dm *DiskModule) Reset() {
	clear(dm.Disks)
}

// DiskMetrics contains the atomic counters for a single disk.
type DiskMetrics struct {
	IOCount      [DiskOpCount]atomic.Int64
	BytesRead    atomic.Int64
	BytesWritten atomic.Int64
}

// newDiskModule creates and initializes a new DiskModule.
func newDiskModule() *DiskModule {
	return &DiskModule{
		Disks: make(map[uint32]*DiskMetrics),
	}
}

// newDiskMetrics creates and initializes a new DiskMetrics struct with all counters.
func newDiskMetrics() *DiskMetrics {
	// With atomic values instead of pointers, the zero value of the struct is ready to use.
	// This eliminates 5 heap allocations per call compared to the previous implementation.
	return &DiskMetrics{}
}

// getOrCreateDiskMetrics retrieves or creates the metrics for a specific disk number.
// This method is thread-safe.
func (dm *DiskModule) getOrCreateDiskMetrics(diskNumber uint32) *DiskMetrics {
	// Fast path: check with a read lock.
	dm.mu.RLock()
	metrics, ok := dm.Disks[diskNumber]
	dm.mu.RUnlock()
	if ok {
		return metrics
	}

	// Slow path: create with a write lock.
	dm.mu.Lock()
	defer dm.mu.Unlock()

	// Double-check in case another goroutine created it while we waited for the lock.
	if metrics, ok = dm.Disks[diskNumber]; ok {
		return metrics
	}

	metrics = newDiskMetrics()
	dm.Disks[diskNumber] = metrics
	return metrics
}

// RecordDiskIO records a disk I/O operation for this process.
func (pd *ProcessData) RecordDiskIO(diskNumber uint32, transferSize uint32, isWrite bool) {
	diskMetrics := pd.Disk.getOrCreateDiskMetrics(diskNumber)

	var operation DiskOperation
	if isWrite {
		operation = DiskOpWrite
		diskMetrics.BytesWritten.Add(int64(transferSize))
	} else {
		operation = DiskOpRead
		diskMetrics.BytesRead.Add(int64(transferSize))
	}
	diskMetrics.IOCount[operation].Add(1)
}
