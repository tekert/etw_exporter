package kernelnetwork

import (
	"github.com/tekert/goetw/etw"
	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"

	"etw_exporter/internal/config"
	"etw_exporter/internal/logger"
)

// NetworkHandler processes ETW network events and delegates to the network collector.
type NetworkHandler struct {
	collector *NetworkCSCollector
	log       *phusluadapter.SampledLogger
}

// NewNetworkHandler creates a new network handler instance.
func NewNetworkHandler(config *config.NetworkConfig) *NetworkHandler {
	return &NetworkHandler{
		collector: NewNetworkCollector(config),
		log:       logger.NewSampledLoggerCtx("network_handler"),
	}
}

// GetCustomCollector returns the underlying custom collector for Prometheus registration.
func (nh *NetworkHandler) GetCustomCollector() *NetworkCSCollector {
	return nh.collector
}

// HandleTCPDataSent processes TCP data sent events to track network bytes.
// This handler increments counters for bytes sent by a process.
//
// ETW Event Details:
//   - Provider Name: Microsoft-Windows-Kernel-Network
//   - Provider GUID: {7dd42a49-5329-4832-8dfd-43d979153a88}
//   - Event ID(s): 10 (Send, IPv4), 26 (Send, IPv6)
//   - Event Name(s): Datasent.
//   - Event Version(s): 0
//   - Schema: Manifest (XML)
//
// Schema (from manifest):
//   - PID (win:UInt32): Process identifier. Offset: 0
//   - size (win:UInt32): Number of bytes sent. Offset: 4
//   - daddr (win:UInt32/win:Binary): Destination address.
//   - saddr (win:UInt32/win:Binary): Source address.
//   - dport (win:UInt16): Destination port.
//   - sport (win:UInt16): Source port.
//   - startime (win:UInt32): Start time of the operation.
//   - endtime (win:UInt32): End time of the operation.
//   - seqnum (win:UInt32): Sequence number.
//   - connid (win:UInt32): Connection identifier.
func (nh *NetworkHandler) HandleTCPDataSent(helper *etw.EventRecordHelper) error {
	pid, err := helper.GetPropertyUint("PID")
	if err != nil {
		return err
	}
	size, err := helper.GetPropertyUint("size")
	if err != nil {
		return err
	}
	startKey, _ := helper.EventRec.ExtProcessStartKey()

	nh.collector.RecordDataSent(uint32(pid), startKey, "tcp", uint32(size))
	return nil
}

// HandleTCPDataReceived processes TCP data received events to track network bytes.
// This handler increments counters for bytes received by a process.
//
// ETW Event Details:
//   - Provider Name: Microsoft-Windows-Kernel-Network
//   - Provider GUID: {7dd42a49-5329-4832-8dfd-43d979153a88}
//   - Event ID(s): 11 (Recv, IPv4), 27 (Recv, IPv6)
//   - Event Name(s): Datareceived.
//   - Event Version(s): 0
//   - Schema: Manifest (XML)
//
// Schema (from manifest):
//   - PID (win:UInt32): Process identifier. Offset: 0
//   - size (win:UInt32): Number of bytes received. Offset: 4
//   - daddr (win:UInt32/win:Binary): Destination address.
//   - saddr (win:UInt32/win:Binary): Source address.
//   - dport (win:UInt16): Destination port.
//   - sport (win:UInt16): Source port.
//   - seqnum (win:UInt32): Sequence number.
//   - connid (win:UInt32): Connection identifier.
func (nh *NetworkHandler) HandleTCPDataReceived(helper *etw.EventRecordHelper) error {
	processID, err := helper.GetPropertyUint("PID")
	if err != nil {
		return err
	}
	size, err := helper.GetPropertyUint("size")
	if err != nil {
		return err
	}
	startKey, _ := helper.EventRec.ExtProcessStartKey()

	nh.collector.RecordDataReceived(uint32(processID), startKey, "tcp", uint32(size))
	return nil
}

// HandleTCPConnectionAttempted processes TCP connection attempt events.
// This handler tracks connection attempts by a process.
//
// ETW Event Details:
//   - Provider Name: Microsoft-Windows-Kernel-Network
//   - Provider GUID: {7dd42a49-5329-4832-8dfd-43d979153a88}
//   - Event ID(s): 12 (Connect, IPv4), 28 (Connect, IPv6)
//   - Event Name(s): Connectionattempted.
//   - Event Version(s): 0
//   - Schema: Manifest (XML)
//
// Schema (from manifest):
//   - PID (win:UInt32): Process identifier. Offset: 0
//   - daddr (win:UInt32/win:Binary): Destination address.
//   - saddr (win:UInt32/win:Binary): Source address.
//   - dport (win:UInt16): Destination port.
//   - sport (win:UInt16): Source port.
//   - mss (win:UInt16): Maximum segment size.
//   - sackopt (win:UInt16): Selective acknowledgment option.
//   - tsopt (win:UInt16): Timestamp option.
//   - wsopt (win:UInt16): Window scaling option.
//   - rcvwin (win:UInt32): Receive window size.
//   - rcvwinscale (win:UInt16): Receive window scale.
//   - sndwinscale (win:UInt16): Send window scale.
//   - seqnum (win:UInt32): Sequence number.
//   - connid (win:UInt32): Connection identifier.
func (nh *NetworkHandler) HandleTCPConnectionAttempted(helper *etw.EventRecordHelper) error {
	pid, err := helper.GetPropertyUint("PID")
	if err != nil {
		return err
	}
	startKey, _ := helper.EventRec.ExtProcessStartKey()

	nh.collector.RecordConnectionAttempted(uint32(pid), startKey, "tcp")
	return nil
}

// HandleTCPConnectionAccepted processes TCP connection accepted events.
// This handler tracks successful connection acceptances by a process.
//
// ETW Event Details:
//   - Provider Name: Microsoft-Windows-Kernel-Network
//   - Provider GUID: {7dd42a49-5329-4832-8dfd-43d979153a88}
//   - Event ID(s): 15 (Accept, IPv4), 31 (Accept, IPv6)
//   - Event Name(s): Connectionaccepted.
//   - Event Version(s): 0
//   - Schema: Manifest (XML)
//
// Schema (from manifest):
//   - PID (win:UInt32): Process identifier. Offset: 0
//   - connid (win:UInt32): Connection identifier.
func (nh *NetworkHandler) HandleTCPConnectionAccepted(helper *etw.EventRecordHelper) error {
	pid, err := helper.GetPropertyUint("PID")
	if err != nil {
		return err
	}
	startKey, _ := helper.EventRec.ExtProcessStartKey()

	nh.collector.RecordConnectionAccepted(uint32(pid), startKey, "tcp")
	return nil
}

// HandleTCPDataRetransmitted processes TCP data retransmission events.
// This handler tracks retransmissions by a process.
//
// ETW Event Details:
//   - Provider Name: Microsoft-Windows-Kernel-Network
//   - Provider GUID: {7dd42a49-5329-4832-8dfd-43d979153a88}
//   - Event ID(s): 14 (Retransmit, IPv4), 30 (Retransmit, IPv6)
//   - Event Name(s): Dataretransmitted.
//   - Event Version(s): 0
//   - Schema: Manifest (XML)
//
// Schema (from manifest):
//   - PID (win:UInt32): Process identifier. Offset: 0
//   - connid (win:UInt32): Connection identifier.
func (nh *NetworkHandler) HandleTCPDataRetransmitted(helper *etw.EventRecordHelper) error {
	pid, err := helper.GetPropertyUint("PID")
	if err != nil {
		return err
	}
	startKey, _ := helper.EventRec.ExtProcessStartKey()

	nh.collector.RecordRetransmission(uint32(pid), startKey)
	return nil
}

// HandleTCPConnectionFailed processes TCP connection failure events.
// This handler tracks failed connection attempts.
//
// ETW Event Details:
//   - Provider Name: Microsoft-Windows-Kernel-Network
//   - Provider GUID: {7dd42a49-5329-4832-8dfd-43d979153a88}
//   - Event ID(s): 17 (TCP Connection Attempt Failed)
//   - Event Name(s): TCPconnectionattemptfailed.
//   - Event Version(s): 0
//   - Schema: Manifest (XML)
//
// Schema (from manifest):
//   - Proto (win:UInt16): Protocol type.
//   - FailureCode (win:UInt16): Failure code.
//
// Note: This event does not contain a PID. We attribute it to PID 0 ("system").
func (nh *NetworkHandler) HandleTCPConnectionFailed(helper *etw.EventRecordHelper) error {
	failureCode, err := helper.GetPropertyUint("FailureCode")
	if err != nil {
		return err
	}

	nh.collector.RecordConnectionFailed(0, 0, "tcp", uint16(failureCode))
	return nil
}

// HandleUDPDataSent processes UDP data sent events to track network bytes.
// This handler increments counters for bytes sent over UDP.
//
// ETW Event Details:
//   - Provider Name: Microsoft-Windows-Kernel-Network
//   - Provider GUID: {7dd42a49-5329-4832-8dfd-43d979153a88}
//   - Event ID(s): 42 (Send, IPv4), 58 (Send, IPv6)
//   - Event Name(s): DatasentoverUDPprotocol.
//   - Event Version(s): 0
//   - Schema: Manifest (XML)
//
// Schema (from manifest):
//   - PID (win:UInt32): Process identifier. Offset: 0
//   - size (win:UInt32): Number of bytes sent. Offset: 4
//   - connid (win:UInt32): Connection identifier.
func (nh *NetworkHandler) HandleUDPDataSent(helper *etw.EventRecordHelper) error {
	pid, err := helper.GetPropertyUint("PID")
	if err != nil {
		return err
	}
	size, err := helper.GetPropertyUint("size")
	if err != nil {
		return err
	}
	startKey, _ := helper.EventRec.ExtProcessStartKey()

	nh.collector.RecordDataSent(uint32(pid), startKey, "udp", uint32(size))
	return nil
}

// HandleUDPDataReceived processes UDP data received events to track network bytes.
// This handler increments counters for bytes received over UDP.
//
// ETW Event Details:
//   - Provider Name: Microsoft-Windows-Kernel-Network
//   - Provider GUID: {7dd42a49-5329-4832-8dfd-43d979153a88}
//   - Event ID(s): 43 (Recv, IPv4), 59 (Recv, IPv6)
//   - Event Name(s): DatareceivedoverUDPprotocol.
//   - Event Version(s): 0
//   - Schema: Manifest (XML)
//
// Schema (from manifest):
//   - PID (win:UInt32): Process identifier. Offset: 0
//   - size (win:UInt32): Number of bytes received. Offset: 4
//   - connid (win:UInt32): Connection identifier.
func (nh *NetworkHandler) HandleUDPDataReceived(helper *etw.EventRecordHelper) error {
	pid, err := helper.GetPropertyUint("PID")
	if err != nil {
		return err
	}
	size, err := helper.GetPropertyUint("size")
	if err != nil {
		return err
	}
	startKey, _ := helper.EventRec.ExtProcessStartKey()

	nh.collector.RecordDataReceived(uint32(pid), startKey, "udp", uint32(size))
	return nil
}

// HandleUDPConnectionFailed processes UDP connection failure events.
// This handler tracks failed UDP connection attempts.
//
// ETW Event Details:
//   - Provider Name: Microsoft-Windows-Kernel-Network
//   - Provider GUID: {7dd42a49-5329-4832-8dfd-43d979153a88}
//   - Event ID(s): 49 (UDP Connection Attempt Failed)
//   - Event Name(s): UDPconnectionattemptfailed.
//   - Event Version(s): 0
//   - Schema: Manifest (XML)
//
// Schema (from manifest):
//   - Proto (win:UInt16): Protocol type.
//   - FailureCode (win:UInt16): Failure code.
//
// Note: This event does not contain a PID. We attribute it to PID 0 ("system").
func (nh *NetworkHandler) HandleUDPConnectionFailed(helper *etw.EventRecordHelper) error {
	failureCode, err := helper.GetPropertyUint("FailureCode")
	if err != nil {
		return err
	}

	nh.collector.RecordConnectionFailed(0, 0, "udp", uint16(failureCode))
	return nil
}
