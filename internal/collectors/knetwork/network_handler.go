package knetwork

import (
	"github.com/tekert/goetw/etw"

	"etw_exporter/internal/collectors/kprocess"
)

// NetworkHandler processes ETW network events and delegates to the network collector
type NetworkHandler struct {
	collector        *NetworkCollector
	processCollector *kprocess.ProcessCollector
}

// NewNetworkHandler creates a new network handler instance
func NewNetworkHandler(collector *NetworkCollector) *NetworkHandler {
	return &NetworkHandler{
		collector:        collector,
		processCollector: kprocess.GetGlobalProcessCollector(),
	}
}

// HandleTCPDataSent processes TCP data sent events to track network bytes
// Provider: Microsoft-Windows-Kernel-Network
// Provider GUID: {7dd42a49-5329-4832-8dfd-43d979153a88}
// Event ID: 10 (IPv4), 26 (IPv6) - TCP Data Sent
//
// Schema (from manifest):
//   - PID (UInt32): Process identifier
//   - size (UInt32): Number of bytes sent
//   - daddr (UInt32 for IPv4, Binary for IPv6): Destination address
//   - saddr (UInt32 for IPv4, Binary for IPv6): Source address
//   - dport (UInt16): Destination port
//   - sport (UInt16): Source port
//   - startime (UInt32): Start time (IPv4 only)
//   - endtime (UInt32): End time (IPv4 only)
//   - seqnum (UInt32): Sequence number
//   - connid (UInt32): Connection identifier
//
// This event captures TCP data transmission for tracking network usage by process.
func (nh *NetworkHandler) HandleTCPDataSent(helper *etw.EventRecordHelper) error {
	pid, _ := helper.GetPropertyUint("PID")
	size, _ := helper.GetPropertyUint("size")

	processName, ok := nh.processCollector.GetProcessName(uint32(pid))
	if ok {
		nh.collector.RecordDataSent(processName, "tcp", uint32(size))
	}

	return nil
}

// HandleTCPDataReceived processes TCP data received events to track network bytes
// Provider: Microsoft-Windows-Kernel-Network
// Provider GUID: {7dd42a49-5329-4832-8dfd-43d979153a88}
// Event ID: 11 (IPv4), 27 (IPv6) - TCP Data Received
//
// Schema (from manifest):
//   - PID (UInt32): Process identifier
//   - size (UInt32): Number of bytes received
//   - daddr (UInt32 for IPv4, Binary for IPv6): Destination address
//   - saddr (UInt32 for IPv4, Binary for IPv6): Source address
//   - dport (UInt16): Destination port
//   - sport (UInt16): Source port
//   - seqnum (UInt32): Sequence number
//   - connid (UInt32): Connection identifier
//
// This event captures TCP data reception for tracking network usage by process.
func (nh *NetworkHandler) HandleTCPDataReceived(helper *etw.EventRecordHelper) error {
	pid, _ := helper.GetPropertyUint("PID")
	size, _ := helper.GetPropertyUint("size")

	processName, ok := nh.processCollector.GetProcessName(uint32(pid))
	if ok {
		nh.collector.RecordDataReceived(processName, "tcp", uint32(size))
	}

	return nil
}

// HandleTCPConnectionAttempted processes TCP connection attempt events
// Provider: Microsoft-Windows-Kernel-Network
// Provider GUID: {7dd42a49-5329-4832-8dfd-43d979153a88}
// Event ID: 12 (IPv4), 28 (IPv6) - TCP Connection Attempted
//
// Schema (from manifest):
//   - PID (UInt32): Process identifier
//   - size (UInt32): Size parameter
//   - daddr (UInt32 for IPv4, Binary for IPv6): Destination address
//   - saddr (UInt32 for IPv4, Binary for IPv6): Source address
//   - dport (UInt16): Destination port
//   - sport (UInt16): Source port
//   - mss (UInt16): Maximum segment size
//   - sackopt (UInt16): SACK option
//   - tsopt (UInt16): Timestamp option
//   - wsopt (UInt16): Window scale option
//   - rcvwin (UInt32): Receive window
//   - rcvwinscale (UInt16): Receive window scale
//   - sndwinscale (UInt16): Send window scale
//   - seqnum (UInt32): Sequence number
//   - connid (UInt32): Connection identifier
//
// This event captures TCP connection attempts for connection health tracking.
func (nh *NetworkHandler) HandleTCPConnectionAttempted(helper *etw.EventRecordHelper) error {
	pid, _ := helper.GetPropertyUint("PID")

	processName, ok := nh.processCollector.GetProcessName(uint32(pid))
	if ok {
		nh.collector.RecordConnectionAttempted(processName, "tcp")
	}

	return nil
}

// HandleTCPConnectionAccepted processes TCP connection accepted events
// Provider: Microsoft-Windows-Kernel-Network
// Provider GUID: {7dd42a49-5329-4832-8dfd-43d979153a88}
// Event ID: 15 (IPv4), 31 (IPv6) - TCP Connection Accepted
//
// Schema (from manifest): Same as connection attempted
//
// This event captures successful TCP connection acceptance for connection health tracking.
func (nh *NetworkHandler) HandleTCPConnectionAccepted(helper *etw.EventRecordHelper) error {
	pid, _ := helper.GetPropertyUint("PID")

	processName, ok := nh.processCollector.GetProcessName(uint32(pid))
	if ok {
		nh.collector.RecordConnectionAccepted(processName, "tcp")
	}

	return nil
}

// HandleTCPDataRetransmitted processes TCP data retransmission events
// Provider: Microsoft-Windows-Kernel-Network
// Provider GUID: {7dd42a49-5329-4832-8dfd-43d979153a88}
// Event ID: 14 (IPv4), 30 (IPv6) - TCP Data Retransmitted
//
// Schema (from manifest): Same as data received
//
// This event captures TCP retransmissions for reliability tracking.
func (nh *NetworkHandler) HandleTCPDataRetransmitted(helper *etw.EventRecordHelper) error {
	pid, _ := helper.GetPropertyUint("PID")

	processName, ok := nh.processCollector.GetProcessName(uint32(pid))
	if ok {
		nh.collector.RecordRetransmission(processName)
	}

	return nil
}

// HandleTCPConnectionFailed processes TCP connection failure events
// Provider: Microsoft-Windows-Kernel-Network
// Provider GUID: {7dd42a49-5329-4832-8dfd-43d979153a88}
// Event ID: 17 - TCP Connection Attempt Failed
//
// Schema (from manifest):
//   - Proto (UInt16): Protocol type
//   - FailureCode (UInt16): Failure code
//
// This event captures failed TCP connection attempts for connection health tracking.
func (nh *NetworkHandler) HandleTCPConnectionFailed(helper *etw.EventRecordHelper) error {
	failureCode, _ := helper.GetPropertyUint("FailureCode")

	// Note: TCP connection failed events don't include PID, so we use "system" as process name
	nh.collector.RecordConnectionFailed("system", "tcp", uint16(failureCode))

	return nil
}

// HandleUDPDataSent processes UDP data sent events
// Provider: Microsoft-Windows-Kernel-Network
// Provider GUID: {7dd42a49-5329-4832-8dfd-43d979153a88}
// Event ID: 42 (IPv4), 58 (IPv6) - UDP Data Sent
//
// Schema (from manifest): Same as TCP data received (no start/end time)
//
// This event captures UDP data transmission for tracking network usage by process.
func (nh *NetworkHandler) HandleUDPDataSent(helper *etw.EventRecordHelper) error {
	pid, _ := helper.GetPropertyUint("PID")
	size, _ := helper.GetPropertyUint("size")

	processName, ok := nh.processCollector.GetProcessName(uint32(pid))
	if ok {
		nh.collector.RecordDataSent(processName, "udp", uint32(size))
	}

	return nil
}

// HandleUDPDataReceived processes UDP data received events
// Provider: Microsoft-Windows-Kernel-Network
// Provider GUID: {7dd42a49-5329-4832-8dfd-43d979153a88}
// Event ID: 43 (IPv4), 59 (IPv6) - UDP Data Received
//
// Schema (from manifest): Same as TCP data received
//
// This event captures UDP data reception for tracking network usage by process.
func (nh *NetworkHandler) HandleUDPDataReceived(helper *etw.EventRecordHelper) error {
	pid, _ := helper.GetPropertyUint("PID")
	size, _ := helper.GetPropertyUint("size")

	processName, ok := nh.processCollector.GetProcessName(uint32(pid))
	if ok {
		nh.collector.RecordDataReceived(processName, "udp", uint32(size))
	}

	return nil
}

// HandleUDPConnectionFailed processes UDP connection failure events
// Provider: Microsoft-Windows-Kernel-Network
// Provider GUID: {7dd42a49-5329-4832-8dfd-43d979153a88}
// Event ID: 49 - UDP Connection Attempt Failed
//
// Schema (from manifest): Same as TCP connection failed
//
// This event captures failed UDP connection attempts for connection health tracking.
func (nh *NetworkHandler) HandleUDPConnectionFailed(helper *etw.EventRecordHelper) error {
	failureCode, _ := helper.GetPropertyUint("FailureCode")

	// Note: UDP connection failed events don't include PID, so we use "system" as process name
	nh.collector.RecordConnectionFailed("system", "udp", uint16(failureCode))

	return nil
}
