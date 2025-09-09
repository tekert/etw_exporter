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
	PID                  uint32
	BytesSent            [protocolMax]*atomic.Uint64
	BytesReceived        [protocolMax]*atomic.Uint64
	ConnectionsAttempted [protocolMax]*atomic.Uint64
	ConnectionsAccepted  [protocolMax]*atomic.Uint64
	RetransmissionsTotal *atomic.Uint64 // TCP-only, but kept here for simplicity.
}

// failureKey is a composite key for connection failure metrics.
type failureKey struct {
	PID         uint32
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
	config *config.NetworkConfig
	log    *phusluadapter.SampledLogger

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
func NewNetworkCollector(config *config.NetworkConfig) *NetCollector {
	nc := &NetCollector{
		config:            config,
		log:               logger.NewSampledLoggerCtx("network_collector"),
		perProcessMetrics: maps.NewConcurrentMap[uint64, *processMetrics](),

		bytesSentTotalDesc: prometheus.NewDesc(
			"etw_network_sent_bytes_total",
			"Total bytes sent over network by process and protocol.",
			[]string{"process_id", "process_start_key", "process_name", "protocol"}, nil,
		),
		bytesReceivedTotalDesc: prometheus.NewDesc(
			"etw_network_received_bytes_total",
			"Total bytes received over network by process and protocol.",
			[]string{"process_id", "process_start_key", "process_name", "protocol"}, nil,
		),
	}

	if config.ConnectionHealth {
		nc.connectionsAttemptedTotalDesc = prometheus.NewDesc(
			"etw_network_connections_attempted_total",
			"Total number of network connections attempted by process and protocol.",
			[]string{"process_id", "process_start_key", "process_name", "protocol"}, nil,
		)
		nc.connectionsAcceptedTotalDesc = prometheus.NewDesc(
			"etw_network_connections_accepted_total",
			"Total number of network connections accepted by process and protocol.",
			[]string{"process_id", "process_start_key", "process_name", "protocol"}, nil,
		)
		nc.connectionsFailedTotalDesc = prometheus.NewDesc(
			"etw_network_connections_failed_total",
			"Total number of network connection failures by process, protocol, and failure code.",
			[]string{"process_id", "process_start_key", "process_name", "protocol", "failure_code"}, nil,
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
			"Total number of TCP retransmissions by process.",
			[]string{"process_id", "process_start_key", "process_name"}, nil,
		)
	}

	// Register for post-scrape cleanup.
	statemanager.GetGlobalStateManager().RegisterCleaner(nc)

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
	stateManager := statemanager.GetGlobalStateManager()

	// --- Per-Process Metrics ---
	nc.perProcessMetrics.Range(func(startKey uint64, metrics *processMetrics) bool {
		if processName, ok := stateManager.GetKnownProcessName(metrics.PID); ok {
			pidStr := strconv.FormatUint(uint64(metrics.PID), 10)
			startKeyStr := strconv.FormatUint(startKey, 10)

			for p := 0; p < protocolMax; p++ {
				protocolStr := protocolToString(p)
				// Bytes Sent/Received
				if count := metrics.BytesSent[p].Load(); count > 0 {
					ch <- prometheus.MustNewConstMetric(
						nc.bytesSentTotalDesc,
						prometheus.CounterValue,
						float64(count),
						pidStr, startKeyStr, processName, protocolStr)
				}
				if count := metrics.BytesReceived[p].Load(); count > 0 {
					ch <- prometheus.MustNewConstMetric(
						nc.bytesReceivedTotalDesc,
						prometheus.CounterValue,
						float64(count),
						pidStr, startKeyStr, processName, protocolStr)
				}

				// Connection Health
				if nc.config.ConnectionHealth {
					if count := metrics.ConnectionsAttempted[p].Load(); count > 0 {
						ch <- prometheus.MustNewConstMetric(
							nc.connectionsAttemptedTotalDesc,
							prometheus.CounterValue,
							float64(count),
							pidStr, startKeyStr, processName, protocolStr)
					}
					if count := metrics.ConnectionsAccepted[p].Load(); count > 0 {
						ch <- prometheus.MustNewConstMetric(
							nc.connectionsAcceptedTotalDesc,
							prometheus.CounterValue,
							float64(count),
							pidStr, startKeyStr, processName, protocolStr)
					}
				}
			}

			// Retransmissions (TCP only)
			if nc.config.RetransmissionRate {
				if count := metrics.RetransmissionsTotal.Load(); count > 0 {
					ch <- prometheus.MustNewConstMetric(
						nc.retransmissionsTotalDesc,
						prometheus.CounterValue,
						float64(count),
						pidStr, startKeyStr, processName)
				}
			}
		}
		return true
	})

	// --- System-Wide Metrics ---
	if nc.config.ConnectionHealth {
		nc.connectionsFailedTotal.Range(func(key, val any) bool {
			k := key.(failureKey)
			// For failures, we check tracking only if PID > 0. PID 0 is system-level.
			if k.PID == 0 || stateManager.IsKnownProcess(k.PID) {
				processName, ok := stateManager.GetProcessName(k.PID)
				if !ok && k.PID == 0 { // Handle system-level failures
					processName, ok = "system", true
				}
				if ok {
					count := atomic.LoadUint64(val.(*uint64))
					ch <- prometheus.MustNewConstMetric(
						nc.connectionsFailedTotalDesc,
						prometheus.CounterValue,
						float64(count),
						strconv.FormatUint(uint64(k.PID), 10),
						strconv.FormatUint(k.StartKey, 10),
						processName,
						k.Protocol,
						strconv.FormatUint(uint64(k.FailureCode), 10))
				}
			}
			return true
		})
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
func (nc *NetCollector) getOrCreateProcessMetrics(pid uint32, startKey uint64) *processMetrics {
	return nc.perProcessMetrics.LoadOrStore(startKey, func() *processMetrics {
		pm := &processMetrics{
			PID:                  pid,
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
}

// RecordDataSent records bytes sent for a process and protocol.
// This is on the hot path and must be highly performant.
func (nc *NetCollector) RecordDataSent(pid uint32, startKey uint64, protocol int, bytes uint32) {
	if startKey > 0 && statemanager.GetGlobalStateManager().IsKnownProcess(pid) {
		metrics := nc.getOrCreateProcessMetrics(pid, startKey)
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
func (nc *NetCollector) RecordDataReceived(pid uint32, startKey uint64, protocol int, bytes uint32) {
	if startKey > 0 && statemanager.GetGlobalStateManager().IsKnownProcess(pid) {
		metrics := nc.getOrCreateProcessMetrics(pid, startKey)
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
func (nc *NetCollector) RecordConnectionAttempted(pid uint32, startKey uint64, protocol int) {
	if nc.config.ConnectionHealth && startKey > 0 &&
		statemanager.GetGlobalStateManager().IsKnownProcess(pid) {

		metrics := nc.getOrCreateProcessMetrics(pid, startKey)
		metrics.ConnectionsAttempted[protocol].Add(1)
	}
}

// RecordConnectionAccepted records a connection acceptance.
func (nc *NetCollector) RecordConnectionAccepted(pid uint32, startKey uint64, protocol int) {
	if nc.config.ConnectionHealth && startKey > 0 &&
		statemanager.GetGlobalStateManager().IsKnownProcess(pid) {

		metrics := nc.getOrCreateProcessMetrics(pid, startKey)
		metrics.ConnectionsAccepted[protocol].Add(1)
	}
}

// RecordConnectionFailed records a connection failure.
func (nc *NetCollector) RecordConnectionFailed(pid uint32, startKey uint64, protocol int, failureCode uint16) {
	if nc.config.ConnectionHealth {
		// We check for tracking only if PID > 0. PID 0 is system-level and always tracked for failures.
		if pid > 0 && !statemanager.GetGlobalStateManager().IsKnownProcess(pid) {
			return
		}
		key := failureKey{PID: pid, StartKey: startKey, Protocol: protocolToString(protocol), FailureCode: failureCode}
		val, _ := nc.connectionsFailedTotal.LoadOrStore(key, new(uint64))
		atomic.AddUint64(val.(*uint64), 1)
	}
}

// RecordRetransmission records a TCP retransmission.
func (nc *NetCollector) RecordRetransmission(pid uint32, startKey uint64) {
	if nc.config.RetransmissionRate && startKey > 0 &&
		statemanager.GetGlobalStateManager().IsKnownProcess(pid) {

		metrics := nc.getOrCreateProcessMetrics(pid, startKey)
		metrics.RetransmissionsTotal.Add(1)
	}
}

// CleanupTerminatedProcesses implements the statemanager.PostScrapeCleaner interface.
// This method is called by the KernelStateManager after a scrape is complete to
// allow the collector to safely clean up its internal state for terminated processes.
func (nc *NetCollector) CleanupTerminatedProcesses(terminatedProcs map[uint32]uint64) {
	cleanedCount := 0
	// Clean up the primary per-process map.
	for _, startKey := range terminatedProcs {
		if _, deleted := nc.perProcessMetrics.LoadAndDelete(startKey); deleted {
			cleanedCount++
		}
	}

	// Clean up the connection failures map, which has a different key structure.
	nc.connectionsFailedTotal.Range(func(key, _ any) bool {
		k := key.(failureKey)
		if termStartKey, isTerminated := terminatedProcs[k.PID]; isTerminated && k.StartKey == termStartKey {
			nc.connectionsFailedTotal.Delete(key)
			cleanedCount++
		}
		return true
	})

	if cleanedCount > 0 {
		nc.log.Debug().Int("count", cleanedCount).Msg("Cleaned up network counters for terminated processes")
	}
}
