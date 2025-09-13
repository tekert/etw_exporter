package kernelnetwork

import (
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"

	"etw_exporter/internal/config"
	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"
	"etw_exporter/internal/maps"

	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// Protocol constants for efficient array indexing.
const (
	ProtocolTCP = iota
	ProtocolUDP
	protocolMax // Used for sizing arrays, must be the last element.
)

// protocolToString converts a protocol constant to its string representation for Prometheus labels.
func protocolToString(p int) string {
	switch p {
	case ProtocolTCP:
		return "tcp"
	case ProtocolUDP:
		return "udp"
	default:
		return "unknown"
	}
}

// processMetrics holds all network metrics for a single process instance.
// It uses fixed-size arrays indexed by protocol constants for maximum performance.
type processMetrics struct {
	BytesSent            [protocolMax]*atomic.Uint64
	BytesReceived        [protocolMax]*atomic.Uint64
	ConnectionsAttempted [protocolMax]*atomic.Uint64
	ConnectionsAccepted  [protocolMax]*atomic.Uint64
	RetransmissionsTotal *atomic.Uint64 // TCP-only, but kept here for simplicity.
}

// failureKey is a composite key for connection failure metrics.
type failureKey struct {
	StartKey    uint64
	Protocol    string
	FailureCode uint16
}

// trafficKey is a composite key for protocol-level traffic metrics.
type trafficKey struct {
	Protocol  string
	Direction string
}

// NetCollector implements prometheus.Collector for network-related metrics.
// This collector is designed for high performance, using lock-free atomics and
// struct-based keys in sync.Map to eliminate allocations on the hot path.
// Metrics are created on each scrape to ensure data consistency.
type NetCollector struct {
	config       *config.NetworkConfig
	stateManager *statemanager.KernelStateManager // Global state manager reference
	log          *phusluadapter.SampledLogger

	// Per-process metrics, keyed by the unique process StartKey.
	perProcessMetrics maps.ConcurrentMap[uint64, *processMetrics]

	// System-wide metrics that are not process-specific or have different keys.
	connectionsFailedTotal sync.Map // key: failureKey, value: *atomic.Uint64
	trafficBytesTotal      sync.Map // key: trafficKey, value: *atomic.Uint64

	// Metric descriptors
	bytesSentTotalDesc     *prometheus.Desc
	bytesReceivedTotalDesc *prometheus.Desc

	connectionsAttemptedTotalDesc *prometheus.Desc
	connectionsAcceptedTotalDesc  *prometheus.Desc
	connectionsFailedTotalDesc    *prometheus.Desc

	trafficBytesTotalDesc *prometheus.Desc

	retransmissionsTotalDesc *prometheus.Desc
}

// NewNetworkCollector creates a new network collector with Prometheus metric descriptors.
func NewNetworkCollector(config *config.NetworkConfig, sm *statemanager.KernelStateManager) *NetCollector {
	nc := &NetCollector{
		config:            config,
		stateManager:      sm,
		log:               logger.NewSampledLoggerCtx("network_collector"),
		perProcessMetrics: maps.NewConcurrentMap[uint64, *processMetrics](),

		bytesSentTotalDesc: prometheus.NewDesc(
			"etw_network_sent_bytes_total",
			"Total bytes sent over network by program and protocol.",
			[]string{"process_name", "image_checksum", "session_id", "protocol"}, nil,
		),
		bytesReceivedTotalDesc: prometheus.NewDesc(
			"etw_network_received_bytes_total",
			"Total bytes received over network by program and protocol.",
			[]string{"process_name", "image_checksum", "session_id", "protocol"}, nil,
		),
	}

	if config.ConnectionHealth {
		nc.connectionsAttemptedTotalDesc = prometheus.NewDesc(
			"etw_network_connections_attempted_total",
			"Total number of network connections attempted by program and protocol.",
			[]string{"process_name", "image_checksum", "session_id", "protocol"}, nil,
		)
		nc.connectionsAcceptedTotalDesc = prometheus.NewDesc(
			"etw_network_connections_accepted_total",
			"Total number of network connections accepted by program and protocol.",
			[]string{"process_name", "image_checksum", "session_id", "protocol"}, nil,
		)
		nc.connectionsFailedTotalDesc = prometheus.NewDesc(
			"etw_network_connections_failed_total",
			"Total number of network connection failures by program, protocol, and failure code.",
			[]string{"process_name", "image_checksum", "session_id", "protocol", "failure_code"}, nil,
		)
	}

	if config.ByProtocol {
		nc.trafficBytesTotalDesc = prometheus.NewDesc(
			"etw_network_traffic_bytes_total",
			"Total network traffic bytes by protocol and direction.",
			[]string{"protocol", "direction"}, nil,
		)
	}

	if config.RetransmissionRate {
		nc.retransmissionsTotalDesc = prometheus.NewDesc(
			"etw_network_retransmissions_total",
			"Total number of TCP retransmissions by program.",
			[]string{"process_name", "image_checksum", "session_id"}, nil,
		)
	}

	// Register for post-scrape cleanup.
	sm.RegisterCleaner(nc)

	return nc
}

// Describe implements prometheus.Collector.
// It sends the descriptors of all the metrics the collector can possibly export
// to the provided channel. This is called once during registration.
func (nc *NetCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- nc.bytesSentTotalDesc
	ch <- nc.bytesReceivedTotalDesc

	if nc.config.ConnectionHealth {
		ch <- nc.connectionsAttemptedTotalDesc
		ch <- nc.connectionsAcceptedTotalDesc
		ch <- nc.connectionsFailedTotalDesc
	}
	if nc.config.ByProtocol {
		ch <- nc.trafficBytesTotalDesc
	}
	if nc.config.RetransmissionRate {
		ch <- nc.retransmissionsTotalDesc
	}
}

// Collect implements prometheus.Collector.
// It is called by Prometheus on each scrape and must create new metrics each time
// to avoid race conditions and ensure stale metrics are not exposed.
func (nc *NetCollector) Collect(ch chan<- prometheus.Metric) {
	stateManager := nc.stateManager

	// --- Per-Program Metrics (with on-the-fly aggregation) ---
	type programKey struct {
		name      string
		checksum  string
		sessionID string
	}
	type aggregatedMetrics struct {
		bytesSent            [protocolMax]uint64
		bytesReceived        [protocolMax]uint64
		connectionsAttempted [protocolMax]uint64
		connectionsAccepted  [protocolMax]uint64
		retransmissionsTotal uint64
	}
	aggregated := make(map[programKey]*aggregatedMetrics)

	nc.perProcessMetrics.Range(func(startKey uint64, metrics *processMetrics) bool {
		if procInfo, ok := stateManager.GetProcessInfoBySK(startKey); ok {
			key := programKey{
				name:      procInfo.Name,
				checksum:  "0x" + strconv.FormatUint(uint64(procInfo.ImageChecksum), 16),
				sessionID: strconv.FormatUint(uint64(procInfo.SessionID), 10),
			}

			agg, exists := aggregated[key]
			if !exists {
				agg = &aggregatedMetrics{}
				aggregated[key] = agg
			}

			for p := 0; p < protocolMax; p++ {
				agg.bytesSent[p] += metrics.BytesSent[p].Load()
				agg.bytesReceived[p] += metrics.BytesReceived[p].Load()
				if nc.config.ConnectionHealth {
					agg.connectionsAttempted[p] += metrics.ConnectionsAttempted[p].Load()
					agg.connectionsAccepted[p] += metrics.ConnectionsAccepted[p].Load()
				}
			}
			if nc.config.RetransmissionRate {
				agg.retransmissionsTotal += metrics.RetransmissionsTotal.Load()
			}
		}
		return true
	})

	for key, data := range aggregated {
		for p := 0; p < protocolMax; p++ {
			protocolStr := protocolToString(p)
			if data.bytesSent[p] > 0 {
				ch <- prometheus.MustNewConstMetric(nc.bytesSentTotalDesc, prometheus.CounterValue, float64(data.bytesSent[p]), key.name, key.checksum, key.sessionID, protocolStr)
			}
			if data.bytesReceived[p] > 0 {
				ch <- prometheus.MustNewConstMetric(nc.bytesReceivedTotalDesc, prometheus.CounterValue, float64(data.bytesReceived[p]), key.name, key.checksum, key.sessionID, protocolStr)
			}
			if nc.config.ConnectionHealth {
				if data.connectionsAttempted[p] > 0 {
					ch <- prometheus.MustNewConstMetric(nc.connectionsAttemptedTotalDesc, prometheus.CounterValue, float64(data.connectionsAttempted[p]), key.name, key.checksum, key.sessionID, protocolStr)
				}
				if data.connectionsAccepted[p] > 0 {
					ch <- prometheus.MustNewConstMetric(nc.connectionsAcceptedTotalDesc, prometheus.CounterValue, float64(data.connectionsAccepted[p]), key.name, key.checksum, key.sessionID, protocolStr)
				}
			}
		}
		if nc.config.RetransmissionRate && data.retransmissionsTotal > 0 {
			ch <- prometheus.MustNewConstMetric(nc.retransmissionsTotalDesc, prometheus.CounterValue, float64(data.retransmissionsTotal), key.name, key.checksum, key.sessionID)
		}
	}

	// --- System-Wide Metrics ---
	if nc.config.ConnectionHealth {
		// Aggregate connection failures
		type failureAggregationKey struct {
			programKey
			protocol    string
			failureCode uint16
		}
		aggregatedFailures := make(map[failureAggregationKey]uint64)

		nc.connectionsFailedTotal.Range(func(key, val any) bool {
			k := key.(failureKey)
			var progKey programKey
			if k.StartKey == 0 {
				progKey = programKey{name: "System", checksum: "0x0", sessionID: "0"}
			} else if procInfo, ok := stateManager.GetProcessInfoBySK(k.StartKey); ok {
				progKey = programKey{
					name:      procInfo.Name,
					checksum:  "0x" + strconv.FormatUint(uint64(procInfo.ImageChecksum), 16),
					sessionID: strconv.FormatUint(uint64(procInfo.SessionID), 10),
				}
			} else {
				return true // Skip terminated process
			}

			aggKey := failureAggregationKey{
				programKey:  progKey,
				protocol:    k.Protocol,
				failureCode: k.FailureCode,
			}
			aggregatedFailures[aggKey] += atomic.LoadUint64(val.(*uint64))
			return true
		})

		for key, count := range aggregatedFailures {
			ch <- prometheus.MustNewConstMetric(
				nc.connectionsFailedTotalDesc,
				prometheus.CounterValue,
				float64(count),
				key.name,
				key.checksum,
				key.sessionID,
				key.protocol,
				strconv.FormatUint(uint64(key.failureCode), 10))
		}
	}

	// --- Protocol Distribution Metrics ---
	if nc.config.ByProtocol {
		nc.trafficBytesTotal.Range(func(key, val any) bool {
			k := key.(trafficKey)
			count := atomic.LoadUint64(val.(*uint64))
			ch <- prometheus.MustNewConstMetric(
				nc.trafficBytesTotalDesc,
				prometheus.CounterValue,
				float64(count),
				k.Protocol, k.Direction)
			return true
		})
	}
}

// getOrCreateProcessMetrics is a helper to retrieve or initialize the metrics struct for a process.
func (nc *NetCollector) getOrCreateProcessMetrics(startKey uint64) *processMetrics {
	procMetrics, _ := nc.perProcessMetrics.LoadOrStore(startKey, func() *processMetrics {
		pm := &processMetrics{
			RetransmissionsTotal: new(atomic.Uint64),
		}
		for i := range protocolMax {
			pm.BytesSent[i] = new(atomic.Uint64)
			pm.BytesReceived[i] = new(atomic.Uint64)
			pm.ConnectionsAttempted[i] = new(atomic.Uint64)
			pm.ConnectionsAccepted[i] = new(atomic.Uint64)
		}
		return pm
	})
	return procMetrics
}

// RecordDataSent records bytes sent for a process and protocol.
// This is on the hot path and must be highly performant.
func (nc *NetCollector) RecordDataSent(startKey uint64, protocol int, bytes uint32) {
	if startKey > 0 && nc.stateManager.IsTrackedStartKey(startKey) {
		metrics := nc.getOrCreateProcessMetrics(startKey)
		metrics.BytesSent[protocol].Add(uint64(bytes))
	}

	if nc.config.ByProtocol {
		tkey := trafficKey{Protocol: protocolToString(protocol), Direction: "sent"}
		tval, _ := nc.trafficBytesTotal.LoadOrStore(tkey, new(uint64))
		atomic.AddUint64(tval.(*uint64), uint64(bytes))
	}
}

// RecordDataReceived records bytes received for a process and protocol.
// This is on the hot path and must be highly performant.
func (nc *NetCollector) RecordDataReceived(startKey uint64, protocol int, bytes uint32) {
	if startKey > 0 && nc.stateManager.IsTrackedStartKey(startKey) {
		metrics := nc.getOrCreateProcessMetrics(startKey)
		metrics.BytesReceived[protocol].Add(uint64(bytes))
	}

	if nc.config.ByProtocol {
		tkey := trafficKey{Protocol: protocolToString(protocol), Direction: "received"}
		tval, _ := nc.trafficBytesTotal.LoadOrStore(tkey, new(uint64))
		atomic.AddUint64(tval.(*uint64), uint64(bytes))
	}
}

// RecordConnectionAttempted records a connection attempt.
// This is on the hot path and must be highly performant.
func (nc *NetCollector) RecordConnectionAttempted(startKey uint64, protocol int) {
	if nc.config.ConnectionHealth && startKey > 0 &&
		nc.stateManager.IsTrackedStartKey(startKey) {

		metrics := nc.getOrCreateProcessMetrics(startKey)
		metrics.ConnectionsAttempted[protocol].Add(1)
	}
}

// RecordConnectionAccepted records a connection acceptance.
func (nc *NetCollector) RecordConnectionAccepted(startKey uint64, protocol int) {
	if nc.config.ConnectionHealth && startKey > 0 &&
		nc.stateManager.IsTrackedStartKey(startKey) {

		metrics := nc.getOrCreateProcessMetrics(startKey)
		metrics.ConnectionsAccepted[protocol].Add(1)
	}
}

// RecordConnectionFailed records a connection failure.
func (nc *NetCollector) RecordConnectionFailed(startKey uint64, protocol int, failureCode uint16) {
	if nc.config.ConnectionHealth {
		// We check for tracking only if startKey > 0. startKey 0 is system-level and always tracked for failures.
		if startKey > 0 && !nc.stateManager.IsTrackedStartKey(startKey) {
			return
		}
		key := failureKey{StartKey: startKey, Protocol: protocolToString(protocol), FailureCode: failureCode}
		val, _ := nc.connectionsFailedTotal.LoadOrStore(key, new(uint64))
		atomic.AddUint64(val.(*uint64), 1)
	}
}

// RecordRetransmission records a TCP retransmission.
func (nc *NetCollector) RecordRetransmission(startKey uint64) {
	if nc.config.RetransmissionRate && startKey > 0 &&
		nc.stateManager.IsTrackedStartKey(startKey) {

		metrics := nc.getOrCreateProcessMetrics(startKey)
		metrics.RetransmissionsTotal.Add(1)
	}
}

// CleanupTerminatedProcesses implements the statemanager.PostScrapeCleaner interface.
// This method is called by the KernelStateManager after a scrape is complete to
// allow the collector to safely clean up its internal state for terminated processes.
func (nc *NetCollector) CleanupTerminatedProcesses(terminatedProcs map[uint64]uint32) {
	cleanedCount := 0
	// Clean up the primary per-process map.
	for startKey := range terminatedProcs {
		if _, deleted := nc.perProcessMetrics.LoadAndDelete(startKey); deleted {
			cleanedCount++
		}
	}

	// Clean up the connection failures map, which has a different key structure.
	nc.connectionsFailedTotal.Range(func(key, _ any) bool {
		k := key.(failureKey)
		// Check if the start key belongs to any of the terminated processes.
		isTerminated := false
		for termStartKey := range terminatedProcs {
			if k.StartKey == termStartKey {
				isTerminated = true
				break
			}
		}
		if isTerminated {
			nc.connectionsFailedTotal.Delete(key)
			cleanedCount++
		}
		return true
	})

	if cleanedCount > 0 {
		nc.log.Debug().Int("count", cleanedCount).Msg("Cleaned up network counters for terminated processes")
	}
}
