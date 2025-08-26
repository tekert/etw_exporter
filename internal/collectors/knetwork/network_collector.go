package knetwork

import (
	"strconv"
	"sync"

	"github.com/phuslu/log"
	"github.com/prometheus/client_golang/prometheus"

	"etw_exporter/internal/config"
	"etw_exporter/internal/logger"
)

// NetworkCollector collects network metrics from ETW events
type NetworkCollector struct {
	config *config.NetworkConfig
	log    log.Logger

	// Data Volume metrics - basic requirement
	bytesSentTotal     *prometheus.CounterVec
	bytesReceivedTotal *prometheus.CounterVec

	// Connection Health metrics - optional
	connectionsAttemptedTotal *prometheus.CounterVec
	connectionsAcceptedTotal  *prometheus.CounterVec
	connectionsFailedTotal    *prometheus.CounterVec

	// Protocol Distribution metrics - optional
	trafficBytesTotal *prometheus.CounterVec

	// Retransmission metrics - optional
	retransmissionsTotal *prometheus.CounterVec
	retransmissionRatio  *prometheus.GaugeVec

	// Internal state for ratio calculation
	retransmissionData map[string]*retransmissionStats
	mu                 sync.RWMutex
}

// retransmissionStats tracks retransmission data for ratio calculation
type retransmissionStats struct {
	totalSent       uint64
	retransmissions uint64
}

// NewNetworkCollector creates a new network collector with Prometheus metrics
func NewNetworkCollector(config *config.NetworkConfig) *NetworkCollector {
	nc := &NetworkCollector{
		config:             config,
		log:                logger.NewLoggerWithContext("network_collector"),
		retransmissionData: make(map[string]*retransmissionStats),
	}

	// Data Volume metrics (always enabled when collector is enabled)
	nc.bytesSentTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "etw_network_sent_bytes_total",
			Help: "Total bytes sent over network by process and protocol",
		},
		[]string{"process_name", "protocol"},
	)

	nc.bytesReceivedTotal = prometheus.NewCounterVec(
		prometheus.CounterOpts{
			Name: "etw_network_received_bytes_total",
			Help: "Total bytes received over network by process and protocol",
		},
		[]string{"process_name", "protocol"},
	)

	// Connection Health metrics (optional)
	if config.ConnectionHealth {
		nc.connectionsAttemptedTotal = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "etw_network_connections_attempted_total",
				Help: "Total number of network connections attempted by process and protocol",
			},
			[]string{"process_name", "protocol"},
		)

		nc.connectionsAcceptedTotal = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "etw_network_connections_accepted_total",
				Help: "Total number of network connections accepted by process and protocol",
			},
			[]string{"process_name", "protocol"},
		)

		nc.connectionsFailedTotal = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "etw_network_connections_failed_total",
				Help: "Total number of network connection failures by process, protocol, and failure code",
			},
			[]string{"process_name", "protocol", "failure_code"},
		)
	}

	// Protocol Distribution metrics (optional)
	if config.ByProtocol {
		nc.trafficBytesTotal = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "etw_network_traffic_bytes_total",
				Help: "Total network traffic bytes by protocol and direction",
			},
			[]string{"protocol", "direction"},
		)
	}

	// Retransmission metrics (optional)
	if config.RetransmissionRate {
		nc.retransmissionsTotal = prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "etw_network_retransmissions_total",
				Help: "Total number of TCP retransmissions by process",
			},
			[]string{"process_name"},
		)

		nc.retransmissionRatio = prometheus.NewGaugeVec(
			prometheus.GaugeOpts{
				Name: "etw_network_retransmission_ratio",
				Help: "TCP retransmission ratio (retransmissions / total_sent) by process",
			},
			[]string{"process_name"},
		)
	}

	return nc
}

// RegisterMetrics registers all collector metrics with Prometheus
func (nc *NetworkCollector) RegisterMetrics(registry prometheus.Registerer) error {
	collectors := []prometheus.Collector{
		nc.bytesSentTotal,
		nc.bytesReceivedTotal,
	}

	if nc.config.ConnectionHealth {
		collectors = append(collectors,
			nc.connectionsAttemptedTotal,
			nc.connectionsAcceptedTotal,
			nc.connectionsFailedTotal,
		)
	}

	if nc.config.ByProtocol {
		collectors = append(collectors, nc.trafficBytesTotal)
	}

	if nc.config.RetransmissionRate {
		collectors = append(collectors,
			nc.retransmissionsTotal,
			nc.retransmissionRatio,
		)
	}

	for _, collector := range collectors {
		if err := registry.Register(collector); err != nil {
			return err
		}
	}

	nc.log.Info().Msg("Network metrics registered with Prometheus")
	return nil
}

// RecordDataSent records bytes sent for a process and protocol
func (nc *NetworkCollector) RecordDataSent(processName, protocol string, bytes uint32) {
	nc.bytesSentTotal.WithLabelValues(processName, protocol).Add(float64(bytes))

	if nc.config.ByProtocol {
		nc.trafficBytesTotal.WithLabelValues(protocol, "sent").Add(float64(bytes))
	}

	// Update retransmission tracking
	if nc.config.RetransmissionRate && protocol == "tcp" {
		nc.updateRetransmissionStats(processName, bytes, 0)
	}
}

// RecordDataReceived records bytes received for a process and protocol
func (nc *NetworkCollector) RecordDataReceived(processName, protocol string, bytes uint32) {
	nc.bytesReceivedTotal.WithLabelValues(processName, protocol).Add(float64(bytes))

	if nc.config.ByProtocol {
		nc.trafficBytesTotal.WithLabelValues(protocol, "received").Add(float64(bytes))
	}
}

// RecordConnectionAttempted records a connection attempt
func (nc *NetworkCollector) RecordConnectionAttempted(processName, protocol string) {
	if nc.config.ConnectionHealth {
		nc.connectionsAttemptedTotal.WithLabelValues(processName, protocol).Inc()
	}
}

// RecordConnectionAccepted records a connection acceptance
func (nc *NetworkCollector) RecordConnectionAccepted(processName, protocol string) {
	if nc.config.ConnectionHealth {
		nc.connectionsAcceptedTotal.WithLabelValues(processName, protocol).Inc()
	}
}

// RecordConnectionFailed records a connection failure
func (nc *NetworkCollector) RecordConnectionFailed(processName, protocol string, failureCode uint16) {
	if nc.config.ConnectionHealth {
		failureCodeStr := strconv.FormatUint(uint64(failureCode), 10)
		nc.connectionsFailedTotal.WithLabelValues(processName, protocol, failureCodeStr).Inc()
	}
}

// RecordRetransmission records a TCP retransmission
func (nc *NetworkCollector) RecordRetransmission(processName string) {
	if nc.config.RetransmissionRate {
		nc.retransmissionsTotal.WithLabelValues(processName).Inc()
		nc.updateRetransmissionStats(processName, 0, 1)
	}
}

// updateRetransmissionStats updates internal retransmission tracking and ratio calculation
func (nc *NetworkCollector) updateRetransmissionStats(processName string, sentBytes uint32, retransmissions uint32) {
	nc.mu.Lock()
	defer nc.mu.Unlock()

	stats, exists := nc.retransmissionData[processName]
	if !exists {
		stats = &retransmissionStats{}
		nc.retransmissionData[processName] = stats
	}

	if sentBytes > 0 {
		stats.totalSent += uint64(sentBytes)
	}
	if retransmissions > 0 {
		stats.retransmissions += uint64(retransmissions)
	}

	// Calculate and update ratio (avoid division by zero)
	if stats.totalSent > 0 {
		ratio := float64(stats.retransmissions) / float64(stats.totalSent)
		nc.retransmissionRatio.WithLabelValues(processName).Set(ratio)
	}
}
