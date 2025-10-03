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
type NetworkModule struct {
	Metrics NetworkMetrics

	// Keyed by a composite key: (protocol << 16) | failureCode
	ConnectionsFailed map[uint32]*atomic.Uint64
	mu                sync.RWMutex // Protects the ConnectionsFailed map.
}

// Reset clears the maps and counters, making the module ready for reuse.
func (nm *NetworkModule) Reset() {
	nm.Metrics.RetransmissionsTotal.Store(0)
	for i := range protocolMax {
		nm.Metrics.BytesSent[i].Store(0)
		nm.Metrics.BytesReceived[i].Store(0)
		nm.Metrics.ConnectionsAttempted[i].Store(0)
		nm.Metrics.ConnectionsAccepted[i].Store(0)
	}
	// Clear the map for the garbage collector.
	clear(nm.ConnectionsFailed)
}

// NetworkMetrics contains the atomic counters for network-related events.
type NetworkMetrics struct {
	BytesSent            [protocolMax]*atomic.Uint64
	BytesReceived        [protocolMax]*atomic.Uint64
	ConnectionsAttempted [protocolMax]*atomic.Uint64
	ConnectionsAccepted  [protocolMax]*atomic.Uint64
	RetransmissionsTotal *atomic.Uint64 // TCP-only
}

// newNetworkModule creates and initializes a new NetworkModule.
func newNetworkModule() *NetworkModule {
	nm := &NetworkModule{
		ConnectionsFailed: make(map[uint32]*atomic.Uint64),
		Metrics: NetworkMetrics{
			RetransmissionsTotal: new(atomic.Uint64),
		},
	}
	for i := range protocolMax {
		nm.Metrics.BytesSent[i] = new(atomic.Uint64)
		nm.Metrics.BytesReceived[i] = new(atomic.Uint64)
		nm.Metrics.ConnectionsAttempted[i] = new(atomic.Uint64)
		nm.Metrics.ConnectionsAccepted[i] = new(atomic.Uint64)
	}
	return nm
}

// failureKey creates a composite integer key from a protocol and failure code.
func failureKey(protocol int, failureCode uint16) uint32 {
	return (uint32(protocol) << 16) | uint32(failureCode)
}

// getOrCreateFailureCounter retrieves or creates the counter for a specific failure type.
func (nm *NetworkModule) getOrCreateFailureCounter(key uint32) *atomic.Uint64 {
	nm.mu.RLock()
	counter, ok := nm.ConnectionsFailed[key]
	nm.mu.RUnlock()
	if ok {
		return counter
	}

	nm.mu.Lock()
	defer nm.mu.Unlock()
	if counter, ok = nm.ConnectionsFailed[key]; ok {
		return counter
	}
	counter = new(atomic.Uint64)
	nm.ConnectionsFailed[key] = counter
	return counter
}

// --- Hot-Path Recording Methods ---

func (pd *ProcessData) RecordDataSent(protocol int, bytes uint32) {
	pd.Network.Metrics.BytesSent[protocol].Add(uint64(bytes))
}

func (pd *ProcessData) RecordDataReceived(protocol int, bytes uint32) {
	pd.Network.Metrics.BytesReceived[protocol].Add(uint64(bytes))
}

func (pd *ProcessData) RecordConnectionAttempted(protocol int) {
	pd.Network.Metrics.ConnectionsAttempted[protocol].Add(1)
}

func (pd *ProcessData) RecordConnectionAccepted(protocol int) {
	pd.Network.Metrics.ConnectionsAccepted[protocol].Add(1)
}

func (pd *ProcessData) RecordRetransmission() {
	pd.Network.Metrics.RetransmissionsTotal.Add(1)
}

func (pd *ProcessData) RecordConnectionFailed(protocol int, failureCode uint16) {
	key := failureKey(protocol, failureCode)
	counter := pd.Network.getOrCreateFailureCounter(key)
	counter.Add(1)
}
