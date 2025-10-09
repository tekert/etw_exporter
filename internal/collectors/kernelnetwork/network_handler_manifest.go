package kernelnetwork

import (
	"github.com/tekert/goetw/etw"
	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"

	"etw_exporter/internal/config"
	"etw_exporter/internal/etw/guids"
	"etw_exporter/internal/etw/handlers"
	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"
)

// Handler processes ETW network events and delegates to the network collector.
type HandlerManifest struct {
	collector *NetCollector
	log       *phusluadapter.SampledLogger
	sm        *statemanager.KernelStateManager
}

// NewNetworkHandler creates a new network handler instance.
func NewNetworkHandlerManifest(config *config.NetworkConfig, sm *statemanager.KernelStateManager) *HandlerManifest {
	return &HandlerManifest{
		sm:        sm,
		collector: nil, // Will be set via AttachCollector
		log:       logger.NewSampledLoggerCtx("network_handler"),
	}
}

// AttachCollector allows a metrics collector to subscribe to the handler's events.
func (c *HandlerManifest) AttachCollector(collector *NetCollector) {
	c.log.Debug().Msg("Attaching metrics collector to network handler.")
	c.collector = collector
}

// RegisterRoutes tells the EventHandler which ETW events this handler is interested in.
func (h *HandlerManifest) RegisterRoutes(router handlers.Router) {
	// Provider: Microsoft-Windows-Kernel-Network ({7dd42a49-5329-4832-8dfd-43d979153a88})
	router.AddRoute(*guids.MicrosoftWindowsKernelNetworkGUID, 10, h.HandleTCPDataSent)            // TCP Send (IPv4)
	router.AddRoute(*guids.MicrosoftWindowsKernelNetworkGUID, 26, h.HandleTCPDataSent)            // TCP Send (IPv6)
	router.AddRoute(*guids.MicrosoftWindowsKernelNetworkGUID, 11, h.HandleTCPDataReceived)        // TCP Recv (IPv4)
	router.AddRoute(*guids.MicrosoftWindowsKernelNetworkGUID, 27, h.HandleTCPDataReceived)        // TCP Recv (IPv6)
	router.AddRoute(*guids.MicrosoftWindowsKernelNetworkGUID, 12, h.HandleTCPConnectionAttempted) // TCP Connect (IPv4)
	router.AddRoute(*guids.MicrosoftWindowsKernelNetworkGUID, 28, h.HandleTCPConnectionAttempted) // TCP Connect (IPv6)
	router.AddRoute(*guids.MicrosoftWindowsKernelNetworkGUID, 15, h.HandleTCPConnectionAccepted)  // TCP Accept (IPv4)
	router.AddRoute(*guids.MicrosoftWindowsKernelNetworkGUID, 31, h.HandleTCPConnectionAccepted)  // TCP Accept (IPv6)
	router.AddRoute(*guids.MicrosoftWindowsKernelNetworkGUID, 14, h.HandleTCPDataRetransmitted)   // TCP Retransmit (IPv4)
	router.AddRoute(*guids.MicrosoftWindowsKernelNetworkGUID, 30, h.HandleTCPDataRetransmitted)   // TCP Retransmit (IPv6)
	router.AddRoute(*guids.MicrosoftWindowsKernelNetworkGUID, 17, h.HandleTCPConnectionFailed)    // TCP Connect Failed
	router.AddRoute(*guids.MicrosoftWindowsKernelNetworkGUID, 42, h.HandleUDPDataSent)            // UDP Send (IPv4)
	router.AddRoute(*guids.MicrosoftWindowsKernelNetworkGUID, 58, h.HandleUDPDataSent)            // UDP Send (IPv6)
	router.AddRoute(*guids.MicrosoftWindowsKernelNetworkGUID, 43, h.HandleUDPDataReceived)        // UDP Recv (IPv4)
	router.AddRoute(*guids.MicrosoftWindowsKernelNetworkGUID, 59, h.HandleUDPDataReceived)        // UDP Recv (IPv6)
	router.AddRoute(*guids.MicrosoftWindowsKernelNetworkGUID, 49, h.HandleUDPConnectionFailed)    // UDP Connect Failed

	h.log.Debug().Msg("Network collector enabled and routes registered")
}

// GetCustomCollector returns the underlying custom collector for Prometheus registration.
func (nh *HandlerManifest) GetCustomCollector() *NetCollector {
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
func (nh *HandlerManifest) HandleTCPDataSent(helper *etw.EventRecordHelper) error {
	pid, err := helper.GetPropertyUint("PID")
	if err != nil {
		return err
	}
	size, err := helper.GetPropertyUint("size")
	if err != nil {
		return err
	}
	pData, ok := nh.sm.GetCurrentProcessDataByPID(uint32(pid))
	if !ok {
		return nil // Process not known, skip.
	}

	nh.collector.RecordDataSent(pData, ProtocolTCP, uint32(size))
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
func (nh *HandlerManifest) HandleTCPDataReceived(helper *etw.EventRecordHelper) error {
	processID, err := helper.GetPropertyUint("PID")
	if err != nil {
		return err
	}
	size, err := helper.GetPropertyUint("size")
	if err != nil {
		return err
	}
	pData, ok := nh.sm.GetCurrentProcessDataByPID(uint32(processID))
	if !ok {
		return nil // Process not known, skip.
	}

	nh.collector.RecordDataReceived(pData, ProtocolTCP, uint32(size))
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
func (nh *HandlerManifest) HandleTCPConnectionAttempted(helper *etw.EventRecordHelper) error {
	pid, err := helper.GetPropertyUint("PID")
	if err != nil {
		return err
	}
	pData, ok := nh.sm.GetCurrentProcessDataByPID(uint32(pid))
	if !ok {
		return nil // Process not known, skip.
	}

	nh.collector.RecordConnectionAttempted(pData, ProtocolTCP)
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
func (nh *HandlerManifest) HandleTCPConnectionAccepted(helper *etw.EventRecordHelper) error {
	pid, err := helper.GetPropertyUint("PID")
	if err != nil {
		return err
	}
	pData, ok := nh.sm.GetCurrentProcessDataByPID(uint32(pid))
	if !ok {
		return nil // Process not known, skip.
	}

	nh.collector.RecordConnectionAccepted(pData, ProtocolTCP)
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
func (nh *HandlerManifest) HandleTCPDataRetransmitted(helper *etw.EventRecordHelper) error {
	pid, err := helper.GetPropertyUint("PID")
	if err != nil {
		return err
	}
	pData, ok := nh.sm.GetCurrentProcessDataByPID(uint32(pid))
	if !ok {
		return nil // Process not known, skip.
	}

	nh.collector.RecordRetransmission(pData)
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
func (nh *HandlerManifest) HandleTCPConnectionFailed(helper *etw.EventRecordHelper) error {
	proto, err := helper.GetPropertyUint("Proto")
	if err != nil {
		return err
	}
	failureCode, err := helper.GetPropertyUint("FailureCode")
	if err != nil {
		return err
	}

	addressFamily := AddressFamilyIPV4
	if proto == 23 { // AF_INET6
		addressFamily = AddressFamilyIPV6
	}

	// System-level failures are attributed to the "System" process (PID 4).
	pData, ok := nh.sm.GetCurrentProcessDataByPID(statemanager.SystemProcessID)
	if !ok {
		nh.log.SampledWarn("network_process_error").Msg("Could not find System process (PID 4) to attribute connection failure.")
		return nil
	}

	nh.collector.RecordConnectionFailed(pData, ProtocolTCP, addressFamily, uint16(failureCode))
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
func (nh *HandlerManifest) HandleUDPDataSent(helper *etw.EventRecordHelper) error {
	pid, err := helper.GetPropertyUint("PID")
	if err != nil {
		return err
	}
	size, err := helper.GetPropertyUint("size")
	if err != nil {
		return err
	}
	pData, ok := nh.sm.GetCurrentProcessDataByPID(uint32(pid))
	if !ok {
		return nil // Process not known, skip.
	}

	nh.collector.RecordDataSent(pData, ProtocolUDP, uint32(size))
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
func (nh *HandlerManifest) HandleUDPDataReceived(helper *etw.EventRecordHelper) error {
	pid, err := helper.GetPropertyUint("PID")
	if err != nil {
		return err
	}
	size, err := helper.GetPropertyUint("size")
	if err != nil {
		return err
	}
	pData, ok := nh.sm.GetCurrentProcessDataByPID(uint32(pid))
	if !ok {
		return nil // Process not known, skip.
	}

	nh.collector.RecordDataReceived(pData, ProtocolUDP, uint32(size))
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
func (nh *HandlerManifest) HandleUDPConnectionFailed(helper *etw.EventRecordHelper) error {
	proto, err := helper.GetPropertyUint("Proto")
	if err != nil {
		return err
	}
	failureCode, err := helper.GetPropertyUint("FailureCode")
	if err != nil {
		return err
	}

	addressFamily := AddressFamilyIPV4
	if proto == 23 { // AF_INET6
		addressFamily = AddressFamilyIPV6
	}

	// System-level failures are attributed to the "System" process (PID 4).
	pData, ok := nh.sm.GetCurrentProcessDataByPID(statemanager.SystemProcessID)
	if !ok {
		nh.log.SampledWarn("network_process_error").Msg("Could not find System process (PID 4) to attribute connection failure.")
		return nil
	}

	nh.collector.RecordConnectionFailed(pData, ProtocolUDP, addressFamily, uint16(failureCode))
	return nil
}
