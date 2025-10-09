package statemanager

import (
	"sync"
	"sync/atomic"
)

// Protocol constants for efficient array indexing.
const (
	ProtocolTCP = iota
	ProtocolUDP
	protocolMax // Used for sizing arrays, must be the last element.
)

// NetworkModule holds all network-related metrics for a single process instance.
// It is designed for high-performance, concurrent writes from event handlers.
// All dimensional metrics are stored in maps keyed by a composite key defined by the collector.
type NetworkModule struct {
	// Metrics keyed by protocol (TCP/UDP) are stored in arrays for lock-free access.
	BytesSent            [protocolMax]*atomic.Uint64
	BytesReceived        [protocolMax]*atomic.Uint64
	ConnectionsAttempted [protocolMax]*atomic.Uint64
	ConnectionsAccepted  [protocolMax]*atomic.Uint64
	RetransmissionsTotal *atomic.Uint64 // TCP-only

	// Metrics with high-cardinality keys (e.g., failure codes) use a map.
	ConnectionsFailed map[uint32]*atomic.Uint64
	failedMu          sync.RWMutex // Protects only the ConnectionsFailed map.
}

// Reset clears the maps and counters, making the module ready for reuse.
func (nm *NetworkModule) Reset() {
	nm.RetransmissionsTotal.Store(0)
	for i := range protocolMax {
		nm.BytesSent[i].Store(0)
		nm.BytesReceived[i].Store(0)
		nm.ConnectionsAttempted[i].Store(0)
		nm.ConnectionsAccepted[i].Store(0)
	}
	// Clear the map for the garbage collector.
	clear(nm.ConnectionsFailed)
}

// newNetworkModule creates and initializes a new NetworkModule.
func newNetworkModule() *NetworkModule {
	nm := &NetworkModule{
		ConnectionsFailed:    make(map[uint32]*atomic.Uint64),
		RetransmissionsTotal: new(atomic.Uint64),
	}
	for i := range protocolMax {
		nm.BytesSent[i] = new(atomic.Uint64)
		nm.BytesReceived[i] = new(atomic.Uint64)
		nm.ConnectionsAttempted[i] = new(atomic.Uint64)
		nm.ConnectionsAccepted[i] = new(atomic.Uint64)
	}
	return nm
}

// getOrCreateFailureCounter handles counter creation for the ConnectionsFailed map.
// This is on a colder path than data transfer metrics.
func (nm *NetworkModule) getOrCreateFailureCounter(key uint32) *atomic.Uint64 {
	nm.failedMu.RLock()
	counter, ok := nm.ConnectionsFailed[key]
	nm.failedMu.RUnlock()
	if ok {
		return counter
	}

	nm.failedMu.Lock()
	defer nm.failedMu.Unlock()
	// Double-check in case another goroutine created it while we were waiting for the lock.
	if counter, ok = nm.ConnectionsFailed[key]; ok {
		return counter
	}
	counter = new(atomic.Uint64)
	nm.ConnectionsFailed[key] = counter
	return counter
}

// --- Hot-Path Recording Methods ---

func (pd *ProcessData) RecordDataSent(protocol int, bytes uint32) {
	pd.Network.BytesSent[protocol].Add(uint64(bytes))
}

func (pd *ProcessData) RecordDataReceived(protocol int, bytes uint32) {
	pd.Network.BytesReceived[protocol].Add(uint64(bytes))
}

func (pd *ProcessData) RecordConnectionAttempted(protocol int) {
	pd.Network.ConnectionsAttempted[protocol].Add(1)
}

func (pd *ProcessData) RecordConnectionAccepted(protocol int) {
	pd.Network.ConnectionsAccepted[protocol].Add(1)
}

func (pd *ProcessData) RecordRetransmission() {
	pd.Network.RetransmissionsTotal.Add(1)
}

func (pd *ProcessData) RecordConnectionFailed(failureKey uint32) {
	counter := pd.Network.getOrCreateFailureCounter(failureKey)
	counter.Add(1)
}
