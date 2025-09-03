package kernelnetwork

import (
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"

	"etw_exporter/internal/config"
	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"

	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// dataKey is a composite key for per-process network metrics.
type dataKey struct {
	PID      uint32
	StartKey uint64
	Protocol string
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

// retransmissionKey is a composite key for retransmission metrics.
type retransmissionKey struct {
	PID      uint32
	StartKey uint64
}

// NetCollector implements prometheus.Collector for network-related metrics.
// This collector is designed for high performance, using lock-free atomics and
// struct-based keys in sync.Map to eliminate allocations on the hot path.
// Metrics are created on each scrape to ensure data consistency.
type NetCollector struct {
	config *config.NetworkConfig
	log    *phusluadapter.SampledLogger

	// Data Volume metrics
	bytesSentTotal     sync.Map // key: dataKey, value: *uint64
	bytesReceivedTotal sync.Map // key: dataKey, value: *uint64

	// Connection Health metrics
	connectionsAttemptedTotal sync.Map // key: dataKey, value: *uint64
	connectionsAcceptedTotal  sync.Map // key: dataKey, value: *uint64
	connectionsFailedTotal    sync.Map // key: failureKey, value: *uint64

	// Protocol Distribution metrics
	trafficBytesTotal sync.Map // key: trafficKey, value: *uint64

	// Retransmission metrics
	retransmissionsTotal sync.Map // key: retransmissionKey, value: *uint64

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
		config: config,
		log:    logger.NewSampledLoggerCtx("network_collector"),

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

	// --- Data Volume Metrics ---
	nc.bytesSentTotal.Range(func(key, val any) bool {
		k := key.(dataKey)
		if processName, ok := stateManager.GetProcessName(k.PID); ok {
			count := atomic.LoadUint64(val.(*uint64))
			ch <- prometheus.MustNewConstMetric(nc.bytesSentTotalDesc, prometheus.CounterValue, float64(count),
				strconv.FormatUint(uint64(k.PID), 10), strconv.FormatUint(k.StartKey, 10), processName, k.Protocol)
		}
		return true
	})
	nc.bytesReceivedTotal.Range(func(key, val any) bool {
		k := key.(dataKey)
		if processName, ok := stateManager.GetProcessName(k.PID); ok {
			count := atomic.LoadUint64(val.(*uint64))
			ch <- prometheus.MustNewConstMetric(nc.bytesReceivedTotalDesc, prometheus.CounterValue, float64(count),
				strconv.FormatUint(uint64(k.PID), 10), strconv.FormatUint(k.StartKey, 10), processName, k.Protocol)
		}
		return true
	})

	// --- Connection Health Metrics ---
	if nc.config.ConnectionHealth {
		nc.connectionsAttemptedTotal.Range(func(key, val any) bool {
			k := key.(dataKey)
			if processName, ok := stateManager.GetProcessName(k.PID); ok {
				count := atomic.LoadUint64(val.(*uint64))
				ch <- prometheus.MustNewConstMetric(nc.connectionsAttemptedTotalDesc, prometheus.CounterValue, float64(count),
					strconv.FormatUint(uint64(k.PID), 10), strconv.FormatUint(k.StartKey, 10), processName, k.Protocol)
			}
			return true
		})
		nc.connectionsAcceptedTotal.Range(func(key, val any) bool {
			k := key.(dataKey)
			if processName, ok := stateManager.GetProcessName(k.PID); ok {
				count := atomic.LoadUint64(val.(*uint64))
				ch <- prometheus.MustNewConstMetric(nc.connectionsAcceptedTotalDesc, prometheus.CounterValue, float64(count),
					strconv.FormatUint(uint64(k.PID), 10), strconv.FormatUint(k.StartKey, 10), processName, k.Protocol)
			}
			return true
		})
		nc.connectionsFailedTotal.Range(func(key, val any) bool {
			k := key.(failureKey)
			processName, ok := stateManager.GetProcessName(k.PID)
			if !ok && k.PID == 0 { // Handle system-level failures
				processName, ok = "system", true
			}
			if ok {
				count := atomic.LoadUint64(val.(*uint64))
				ch <- prometheus.MustNewConstMetric(nc.connectionsFailedTotalDesc, prometheus.CounterValue, float64(count),
					strconv.FormatUint(uint64(k.PID), 10), strconv.FormatUint(k.StartKey, 10), processName, k.Protocol, strconv.FormatUint(uint64(k.FailureCode), 10))
			}
			return true
		})
	}

	// --- Protocol Distribution Metrics ---
	if nc.config.ByProtocol {
		nc.trafficBytesTotal.Range(func(key, val any) bool {
			k := key.(trafficKey)
			count := atomic.LoadUint64(val.(*uint64))
			ch <- prometheus.MustNewConstMetric(nc.trafficBytesTotalDesc, prometheus.CounterValue, float64(count),
				k.Protocol, k.Direction)
			return true
		})
	}

	// --- Retransmission Metrics ---
	if nc.config.RetransmissionRate {
		nc.retransmissionsTotal.Range(func(key, val any) bool {
			k := key.(retransmissionKey)
			if processName, ok := stateManager.GetProcessName(k.PID); ok {
				count := atomic.LoadUint64(val.(*uint64))
				ch <- prometheus.MustNewConstMetric(nc.retransmissionsTotalDesc, prometheus.CounterValue, float64(count),
					strconv.FormatUint(uint64(k.PID), 10), strconv.FormatUint(k.StartKey, 10), processName)
			}
			return true
		})
	}
}

// RecordDataSent records bytes sent for a process and protocol.
// This is on the hot path and must be highly performant.
func (nc *NetCollector) RecordDataSent(pid uint32, startKey uint64, protocol string, bytes uint32) {
	key := dataKey{PID: pid, StartKey: startKey, Protocol: protocol}
	val, _ := nc.bytesSentTotal.LoadOrStore(key, new(uint64))
	atomic.AddUint64(val.(*uint64), uint64(bytes))

	if nc.config.ByProtocol {
		tkey := trafficKey{Protocol: protocol, Direction: "sent"}
		tval, _ := nc.trafficBytesTotal.LoadOrStore(tkey, new(uint64))
		atomic.AddUint64(tval.(*uint64), uint64(bytes))
	}
}

// RecordDataReceived records bytes received for a process and protocol.
// This is on the hot path and must be highly performant.
func (nc *NetCollector) RecordDataReceived(pid uint32, startKey uint64, protocol string, bytes uint32) {
	key := dataKey{PID: pid, StartKey: startKey, Protocol: protocol}
	val, _ := nc.bytesReceivedTotal.LoadOrStore(key, new(uint64))
	atomic.AddUint64(val.(*uint64), uint64(bytes))

	if nc.config.ByProtocol {
		tkey := trafficKey{Protocol: protocol, Direction: "received"}
		tval, _ := nc.trafficBytesTotal.LoadOrStore(tkey, new(uint64))
		atomic.AddUint64(tval.(*uint64), uint64(bytes))
	}
}

// RecordConnectionAttempted records a connection attempt.
// This is on the hot path and must be highly performant.
func (nc *NetCollector) RecordConnectionAttempted(pid uint32, startKey uint64, protocol string) {
	if nc.config.ConnectionHealth {
		key := dataKey{PID: pid, StartKey: startKey, Protocol: protocol}
		val, _ := nc.connectionsAttemptedTotal.LoadOrStore(key, new(uint64))
		atomic.AddUint64(val.(*uint64), 1)
	}
}

// RecordConnectionAccepted records a connection acceptance.
func (nc *NetCollector) RecordConnectionAccepted(pid uint32, startKey uint64, protocol string) {
	if nc.config.ConnectionHealth {
		key := dataKey{PID: pid, StartKey: startKey, Protocol: protocol}
		val, _ := nc.connectionsAcceptedTotal.LoadOrStore(key, new(uint64))
		atomic.AddUint64(val.(*uint64), 1)
	}
}

// RecordConnectionFailed records a connection failure.
func (nc *NetCollector) RecordConnectionFailed(pid uint32, startKey uint64, protocol string, failureCode uint16) {
	if nc.config.ConnectionHealth {
		key := failureKey{PID: pid, StartKey: startKey, Protocol: protocol, FailureCode: failureCode}
		val, _ := nc.connectionsFailedTotal.LoadOrStore(key, new(uint64))
		atomic.AddUint64(val.(*uint64), 1)
	}
}

// RecordRetransmission records a TCP retransmission.
func (nc *NetCollector) RecordRetransmission(pid uint32, startKey uint64) {
	if nc.config.RetransmissionRate {
		key := retransmissionKey{PID: pid, StartKey: startKey}
		val, _ := nc.retransmissionsTotal.LoadOrStore(key, new(uint64))
		atomic.AddUint64(val.(*uint64), 1)
	}
}
