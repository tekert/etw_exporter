package kernelnetwork

import (
	"strconv"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"

	"etw_exporter/internal/config"
	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"

	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// Protocol constants for efficient array indexing.
const (
	ProtocolTCP = iota
	ProtocolUDP
	protocolMax // Used for sizing arrays, must be the last element.
)

// AddressFamily constants for IP versions.
const (
	AddressFamilyIPV4 = iota
	AddressFamilyIPV6
)

// Direction constants for efficient array indexing.
const (
	DirectionSent = iota
	DirectionReceived
	directionMax
)

func directionIdx(protocol int, direction int) int {
	return protocol*directionMax + direction
}

// failureKey creates a composite integer key from a protocol, address family, and failure code.
// The protocol is stored in bits 24-31, address family in 16-23, and code in 0-15.
func failureKey(protocol int, addressFamily int, failureCode uint16) uint32 {
	return (uint32(protocol) << 24) | (uint32(addressFamily) << 16) | uint32(failureCode)
}

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

// addressFamilyToString converts an address family constant to its string representation.
func addressFamilyToString(af int) string {
	switch af {
	case AddressFamilyIPV4:
		return "ipv4"
	case AddressFamilyIPV6:
		return "ipv6"
	default:
		return "unknown"
	}
}

// NetCollector implements prometheus.Collector for network-related metrics.
// This collector is designed for high performance, using lock-free atomics for
// system-wide metrics and reading pre-aggregated data from the KernelStateManager
// for per-process metrics.
type NetCollector struct {
	config       *config.NetworkConfig
	stateManager *statemanager.KernelStateManager // Global state manager reference
	log          *phusluadapter.SampledLogger

	// Per-process metrics are now managed and aggregated by the KernelStateManager.
	// The perProcessMetrics and connectionsFailedTotal maps have been removed.

	// System-wide metrics that are not process-specific.
	// trafficBytesTotal is a pre-allocated slice for system-wide traffic counters.
	// It is indexed by a composite key: `protocol*directionMax + direction`.
	// This avoids allocations on the hot path that would occur with sync.Map and struct keys.
	trafficBytesTotal [protocolMax * directionMax]*atomic.Uint64

	// Metric descriptors
	bytesSentPerProcessTotalDesc     *prometheus.Desc
	bytesReceivedperProcessTotalDesc *prometheus.Desc

	connectionsAttemptedTotalDesc *prometheus.Desc
	connectionsAcceptedTotalDesc  *prometheus.Desc
	connectionsFailedTotalDesc    *prometheus.Desc

	trafficSentBytesTotalDesc     *prometheus.Desc
	trafficReceivedBytesTotalDesc *prometheus.Desc

	retransmissionsTotalDesc *prometheus.Desc
}

// NewNetworkCollector creates a new network collector with Prometheus metric descriptors.
func NewNetworkCollector(config *config.NetworkConfig, sm *statemanager.KernelStateManager) *NetCollector {
	nc := &NetCollector{
		config:       config,
		stateManager: sm,
		log:          logger.NewSampledLoggerCtx("network_collector"),

		bytesSentPerProcessTotalDesc: prometheus.NewDesc(
			"etw_network_process_sent_bytes_total",
			"Total bytes sent over network by program and protocol.",
			[]string{"process_name", "service_name", "pe_checksum", "session_id", "protocol"}, nil,
		),
		bytesReceivedperProcessTotalDesc: prometheus.NewDesc(
			"etw_network_process_received_bytes_total",
			"Total bytes received over network by program and protocol.",
			[]string{"process_name", "service_name", "pe_checksum", "session_id", "protocol"}, nil,
		),
	}

	// Initialize the atomic counters for system-wide traffic.
	for i := range nc.trafficBytesTotal {
		nc.trafficBytesTotal[i] = new(atomic.Uint64)
	}

	if config.ConnectionHealth {
		nc.connectionsAttemptedTotalDesc = prometheus.NewDesc(
			"etw_network_process_connections_attempted_total",
			"Total number of network connections attempted by program and protocol.",
			[]string{"process_name", "service_name", "pe_checksum", "session_id", "protocol"}, nil,
		)
		nc.connectionsAcceptedTotalDesc = prometheus.NewDesc(
			"etw_network_process_connections_accepted_total",
			"Total number of network connections accepted by program and protocol.",
			[]string{"process_name", "service_name", "pe_checksum", "session_id", "protocol"}, nil,
		)
		nc.connectionsFailedTotalDesc = prometheus.NewDesc(
			"etw_network_connections_failed_total",
			"Total number of network connection failures by program, protocol, address family, and failure code.",
			[]string{"process_name", "service_name", "pe_checksum", "session_id", "protocol", "address_family", "failure_code"}, nil,
		)
	}

	if config.ByProtocol {
		nc.trafficSentBytesTotalDesc = prometheus.NewDesc(
			"etw_network_sent_bytes_total",
			"Total network traffic bytes sent by protocol.",
			[]string{"protocol"}, nil,
		)
		nc.trafficReceivedBytesTotalDesc = prometheus.NewDesc(
			"etw_network_received_bytes_total",
			"Total network traffic bytes received by protocol.",
			[]string{"protocol"}, nil,
		)
	}

	if config.RetransmissionRate {
		nc.retransmissionsTotalDesc = prometheus.NewDesc(
			"etw_network_process_retransmissions_total",
			"Total number of TCP retransmissions by program.",
			[]string{"process_name", "service_name", "pe_checksum", "session_id"}, nil,
		)
	}

	// The collector no longer manages its own state, so it does not need to
	// register as a PostScrapeCleaner.

	return nc
}

// Describe implements prometheus.Collector.
// It sends the descriptors of all the metrics the collector can possibly export
// to the provided channel. This is called once during registration.
func (nc *NetCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- nc.bytesSentPerProcessTotalDesc
	ch <- nc.bytesReceivedperProcessTotalDesc

	if nc.config.ConnectionHealth {
		ch <- nc.connectionsAttemptedTotalDesc
		ch <- nc.connectionsAcceptedTotalDesc
		ch <- nc.connectionsFailedTotalDesc
	}
	if nc.config.ByProtocol {
		ch <- nc.trafficSentBytesTotalDesc
		ch <- nc.trafficReceivedBytesTotalDesc
	}
	if nc.config.RetransmissionRate {
		ch <- nc.retransmissionsTotalDesc
	}
}

// Collect implements prometheus.Collector.
// It is called by Prometheus on each scrape and must create new metrics each time
// to avoid race conditions and ensure stale metrics are not exposed.
func (nc *NetCollector) Collect(ch chan<- prometheus.Metric) {
	// --- Per-Program Metrics (reading from pre-aggregated state) ---
	nc.stateManager.RangeAggregatedMetrics(func(key statemanager.ProgramAggregationKey, metrics *statemanager.AggregatedProgramMetrics) bool {
		// Check if this program has any network metrics to report.
		if metrics.Network == nil {
			return true // Continue to next program
		}

		peTimestampSrt := "0x" + strconv.FormatUint(uint64(key.UniqueHash), 16)
		sessionIDStr := strconv.FormatUint(uint64(key.SessionID), 10)
		netData := metrics.Network

		// Bytes Sent
		for protocol, count := range netData.BytesSent {
			if count > 0 {
				ch <- prometheus.MustNewConstMetric(
					nc.bytesSentPerProcessTotalDesc,
					prometheus.CounterValue,
					float64(count),
					key.Name,
					key.ServiceName,
					peTimestampSrt,
					sessionIDStr,
					protocolToString(protocol),
				)
			}
		}
		// Bytes Received
		for protocol, count := range netData.BytesReceived {
			if count > 0 {
				ch <- prometheus.MustNewConstMetric(
					nc.bytesReceivedperProcessTotalDesc,
					prometheus.CounterValue,
					float64(count),
					key.Name,
					key.ServiceName,
					peTimestampSrt,
					sessionIDStr,
					protocolToString(protocol),
				)
			}
		}

		if nc.config.ConnectionHealth {
			// Connections Attempted
			for protocol, count := range netData.ConnectionsAttempted {
				if count > 0 {
					ch <- prometheus.MustNewConstMetric(
						nc.connectionsAttemptedTotalDesc,
						prometheus.CounterValue,
						float64(count),
						key.Name,
						key.ServiceName,
						peTimestampSrt,
						sessionIDStr,
						protocolToString(protocol),
					)
				}
			}
			// Connections Accepted
			for protocol, count := range netData.ConnectionsAccepted {
				if count > 0 {
					ch <- prometheus.MustNewConstMetric(
						nc.connectionsAcceptedTotalDesc,
						prometheus.CounterValue,
						float64(count),
						key.Name,
						key.ServiceName,
						peTimestampSrt,
						sessionIDStr,
						protocolToString(protocol),
					)
				}
			}
		}

		if nc.config.RetransmissionRate && netData.RetransmissionsTotal > 0 {
			ch <- prometheus.MustNewConstMetric(
				nc.retransmissionsTotalDesc,
				prometheus.CounterValue,
				float64(netData.RetransmissionsTotal),
				key.Name,
				key.ServiceName,
				peTimestampSrt,
				sessionIDStr,
			)
		}

		if nc.config.ConnectionHealth {
			for fKey, count := range netData.ConnectionsFailed {
				protocol := int(fKey >> 24) // TODO: create unpack
				addressFamily := int((fKey >> 16) & 0xFF)
				failureCode := uint16(fKey)
				ch <- prometheus.MustNewConstMetric(
					nc.connectionsFailedTotalDesc,
					prometheus.CounterValue,
					float64(count),
					key.Name,
					key.ServiceName,
					peTimestampSrt,
					sessionIDStr,
					protocolToString(protocol),
					addressFamilyToString(addressFamily),
					strconv.FormatUint(uint64(failureCode), 10),
				)
			}
		}

		return true // Continue iteration
	})

	// --- System-Wide Metrics ---

	// --- Protocol Distribution Metrics ---
	if nc.config.ByProtocol {
		for p := range protocolMax { // protocol
			protocolStr := protocolToString(p)

			// Sent
			sentIdx := directionIdx(p, DirectionSent)
			sentCount := nc.trafficBytesTotal[sentIdx].Load()
			if sentCount > 0 {
				ch <- prometheus.MustNewConstMetric(
					nc.trafficSentBytesTotalDesc,
					prometheus.CounterValue,
					float64(sentCount),
					protocolStr)
			}

			// Received
			receivedIdx := directionIdx(p, DirectionReceived)
			receivedCount := nc.trafficBytesTotal[receivedIdx].Load()
			if receivedCount > 0 {
				ch <- prometheus.MustNewConstMetric(
					nc.trafficReceivedBytesTotalDesc,
					prometheus.CounterValue,
					float64(receivedCount),
					protocolStr)
			}
		}
	}

	nc.log.Debug().Msg("Collected Network metrics")
}

// RecordDataSent records bytes sent for a process and protocol.
// This is on the hot path and must be highly performant.
func (nc *NetCollector) RecordDataSent(pData *statemanager.ProcessData, protocol int, bytes uint32) {
	if pData != nil {
		pData.RecordDataSent(protocol, bytes)
	}

	if nc.config.ByProtocol {
		// Use a pre-calculated index into a slice of atomics to avoid map/key allocations.
		idx := directionIdx(protocol, DirectionSent)
		nc.trafficBytesTotal[idx].Add(uint64(bytes))
	}
}

// RecordDataReceived records bytes received for a process and protocol.
// This is on the hot path and must be highly performant.
func (nc *NetCollector) RecordDataReceived(pData *statemanager.ProcessData, protocol int, bytes uint32) {
	if pData != nil {
		pData.RecordDataReceived(protocol, bytes)
	}

	if nc.config.ByProtocol {
		// Use a pre-calculated index into a slice of atomics to avoid map/key allocations.
		idx := directionIdx(protocol, DirectionReceived)
		nc.trafficBytesTotal[idx].Add(uint64(bytes))
	}
}

// RecordConnectionAttempted records a connection attempt.
// This is on the hot path and must be highly performant.
func (nc *NetCollector) RecordConnectionAttempted(pData *statemanager.ProcessData, protocol int) {
	if nc.config.ConnectionHealth && pData != nil {
		pData.RecordConnectionAttempted(protocol)
	}
}

// RecordConnectionAccepted records a connection acceptance.
func (nc *NetCollector) RecordConnectionAccepted(pData *statemanager.ProcessData, protocol int) {
	if nc.config.ConnectionHealth && pData != nil {
		pData.RecordConnectionAccepted(protocol)
	}
}

// RecordConnectionFailed records a connection failure.
func (nc *NetCollector) RecordConnectionFailed(pData *statemanager.ProcessData,
	protocol int, addressFamily int, failureCode uint16) {

	if nc.config.ConnectionHealth && pData != nil {
		// The collector is responsible for creating the composite key.
		key := failureKey(protocol, addressFamily, failureCode)
		pData.RecordConnectionFailed(key)
	}
}

// RecordRetransmission records a TCP retransmission.
func (nc *NetCollector) RecordRetransmission(pData *statemanager.ProcessData) {
	if nc.config.RetransmissionRate && pData != nil {
		pData.RecordRetransmission()
	}
}
