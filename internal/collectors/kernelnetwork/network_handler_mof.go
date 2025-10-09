package kernelnetwork

import (
	"github.com/tekert/goetw/etw"
	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"

	"etw_exporter/internal/config"
	"etw_exporter/internal/etw/handlers"
	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"
)

// HandlerMof processes ETW network events and delegates to the network collector.
type HandlerMof struct {
	collector *NetCollector
	log       *phusluadapter.SampledLogger
	sm        *statemanager.KernelStateManager
}

// NewNetworkHandler creates a new network handler instance.
func NewNetworkHandler(config *config.NetworkConfig, sm *statemanager.KernelStateManager) *HandlerMof {
	return &HandlerMof{
		sm:        sm,
		collector: nil, // Will be set via AttachCollector
		log:       logger.NewSampledLoggerCtx("network_handler"),
	}
}

// AttachCollector allows a metrics collector to subscribe to the handler's events.
func (c *HandlerMof) AttachCollector(collector *NetCollector) {
	c.log.Debug().Msg("Attaching metrics collector to network handler.")
	c.collector = collector
}

// RegisterRoutes tells the EventHandler which ETW events this handler is interested in.
func (h *HandlerMof) RegisterRoutes(router handlers.Router) {
	// Provider: TcpIp ({{9a280ac0-c8e0-11d1-84e2-00c04fb998a2})
	networkRoutesTCP := map[uint8]handlers.RawEventHandlerFunc{
		10: h.HandleTCPDataSent,            // TCP Send (IPv4)
		26: h.HandleTCPDataSent,            // TCP Send (IPv6)
		11: h.HandleTCPDataReceived,        // TCP Recv (IPv4)
		27: h.HandleTCPDataReceived,        // TCP Recv (IPv6)
		12: h.HandleTCPConnectionAttempted, // TCP Connect (IPv4)
		28: h.HandleTCPConnectionAttempted, // TCP Connect (IPv6)
		15: h.HandleTCPConnectionAccepted,  // TCP Accept (IPv4)
		31: h.HandleTCPConnectionAccepted,  // TCP Accept (IPv6)
		14: h.HandleTCPDataRetransmitted,   // TCP Retransmit (IPv4)
		30: h.HandleTCPDataRetransmitted,   // TCP Retransmit (IPv6)
		17: h.HandleTCPConnectionFailed,    // TCP Connect Failed
	}
	// Provider: NT Kernel Logger (DiskIo) ({3d6fa8d4-fe05-11d0-9dda-00c04fd7ba7c}) - MOF Win10+
	// Provider: SystemIoProviderGuid {3d5c43e3-0f1c-4202-b817-174c0070dc79} - Win11+
	handlers.RegisterRawRoutesForGUID(router, etw.TcpIpKernelGuid, networkRoutesTCP)      // Win10+
	handlers.RegisterRawRoutesForGUID(router, etw.SystemIoProviderGuid, networkRoutesTCP) // Win11+

	// UDP Provider: UdpIp ({bf3a50c5-a9c9-4988-a005-2df0b7c80f80})
	networkRoutesUDP := map[uint8]handlers.RawEventHandlerFunc{
		42: h.HandleUDPDataSent,         // UDP Send (IPv4)
		58: h.HandleUDPDataSent,         // UDP Send (IPv6)
		43: h.HandleUDPDataReceived,     // UDP Recv (IPv4)
		59: h.HandleUDPDataReceived,     // UDP Recv (IPv6)
		49: h.HandleUDPConnectionFailed, // UDP Connect Failed
	}
	handlers.RegisterRawRoutesForGUID(router, etw.UdpIpKernelGuid, networkRoutesUDP)      // Win10+
	handlers.RegisterRawRoutesForGUID(router, etw.SystemIoProviderGuid, networkRoutesUDP) // Win11+

	h.log.Debug().Msg("Network collector enabled and routes registered")
}

// GetCustomCollector returns the underlying custom collector for Prometheus registration.
func (nh *HandlerMof) GetCustomCollector() *NetCollector {
	return nh.collector
}

// HandleTCPDataSent processes TCP data sent events to track network bytes.
// This handler increments counters for bytes sent by a process.
//
// ETW Event Details:
//   - Provider Name: TcpIp
//   - Provider GUID: {9a280ac0-c837-4815-a3c4-1d875054ae2e}
//   - Event ID(s): 10 (SendIPV4), 26 (SendIPV6)
//   - Event Name(s): Send
//   - Event Version(s): 2
//   - Schema: MOF
//
// Schema (from MOF, TcpIp_SendIPV4 & TcpIp_SendIPV6):
//   - PID (uint32): Process identifier. Offset: 0
//   - size (uint32): Number of bytes sent. Offset: 4
//   - daddr (object/uint32 or object/[16]byte): Destination IP address. Offset: [ipv4: 8; ipv6: 8]
//   - saddr (object/uint32 or object/[16]byte): Source IP address. Offset: [ipv4: 12; ipv6: 24]
//   - dport (object/uint16): Destination port number. Offset: [ipv4: 16; ipv6: 40]
//   - sport (object/uint16): Source port number. Offset: [ipv4: 18; ipv6: 42]
//   - startime (uint32): Start send request time. Offset: [ipv4: 20; ipv6: 44]
//   - endtime (uint32): End send request time. Offset: [ipv4: 24; ipv6: 48]
//   - seqnum (uint32): Sequence number. Offset: [ipv4: 28; ipv6: 52]
//   - connid (uint32/Pointer): Connection identifier (special PointerType). Offset: [ipv4: 32; ipv6: 56]
func (nh *HandlerMof) HandleTCPDataSent(er *etw.EventRecord) error {
	pid, err := er.GetUint32At(0)
	if err != nil {
		return err
	}
	size, err := er.GetUint32At(4)
	if err != nil {
		return err
	}
	pData, ok := nh.sm.Processes.GetCurrentProcessDataByPID(pid)
	if !ok {
		return nil // Process not known, skip.
	}

	nh.collector.RecordDataSent(pData, ProtocolTCP, size)
	return nil
}

// HandleTCPDataReceived processes TCP data received events to track network bytes.
// This handler increments counters for bytes received by a process.
//
// ETW Event Details:
//   - Provider Name: TcpIp
//   - Provider GUID: {9a280ac0-c837-4815-a3c4-1d875054ae2e}
//   - Event ID(s): 11 (RecvIPV4), 27 (RecvIPV6)
//   - Event Name(s): Recv
//   - Event Version(s): 2
//   - Schema: MOF
//
// Schema (from MOF, TcpIp_TypeGroup1 & TcpIp_TypeGroup3):
//   - PID (uint32): Process identifier. Offset: 0
//   - size (uint32): Number of bytes received. Offset: 4
//   - daddr (object/uint32 or object/[16]byte): Destination IP address. Offset: [ipv4: 8; ipv6: 8]
//   - saddr (object/uint32 or object/[16]byte): Source IP address. Offset: [ipv4: 12; ipv6: 24]
//   - dport (object/uint16): Destination port number. Offset: [ipv4: 16; ipv6: 40]
//   - sport (object/uint16): Source port number. Offset: [ipv4: 18; ipv6: 42]
//   - seqnum (uint32): Sequence number. Offset: [ipv4: 20; ipv6: 44]
//   - connid (uint32/Pointer): Connection identifier (special PointerType). Offset: [ipv4: 24; ipv6: 48]
func (nh *HandlerMof) HandleTCPDataReceived(er *etw.EventRecord) error {
	processID, err := er.GetUint32At(0)
	if err != nil {
		return err
	}
	size, err := er.GetUint32At(4)
	if err != nil {
		return err
	}
	pData, ok := nh.sm.Processes.GetCurrentProcessDataByPID(processID)
	if !ok {
		return nil // Process not known, skip.
	}

	nh.collector.RecordDataReceived(pData, ProtocolTCP, size)
	return nil
}

// HandleTCPConnectionAttempted processes TCP connection attempt events.
// This handler tracks connection attempts by a process.
//
// ETW Event Details:
//   - Provider Name: TcpIp
//   - Provider GUID: {9a280ac0-c837-4815-a3c4-1d875054ae2e}
//   - Event ID(s): 12 (ConnectIPV4), 28 (ConnectIPV6)
//   - Event Name(s): Connect
//   - Event Version(s): 2
//   - Schema: MOF
//
// Schema (from MOF, TcpIp_TypeGroup2 & TcpIp_TypeGroup4):
//   - PID (uint32): Process identifier. Offset: 0
//   - size (uint32): Packet size. Offset: 4
//   - daddr (object/uint32 or object/[16]byte): Destination IP address. Offset: [ipv4: 8; ipv6: 8]
//   - saddr (object/uint32 or object/[16]byte): Source IP address. Offset: [ipv4: 12; ipv6: 24]
//   - dport (object/uint16): Destination port number. Offset: [ipv4: 16; ipv6: 40]
//   - sport (object/uint16): Source port number. Offset: [ipv4: 18; ipv6: 42]
//   - mss (uint16): Maximum segment size. Offset: [ipv4: 20; ipv6: 44]
//   - sackopt (uint16): SACK option. Offset: [ipv4: 22; ipv6: 46]
//   - tsopt (uint16): Time Stamp option. Offset: [ipv4: 24; ipv6: 48]
//   - wsopt (uint16): Window Scale option. Offset: [ipv4: 26; ipv6: 50]
//   - rcvwin (uint32): TCP Receive Window size. Offset: [ipv4: 28; ipv6: 52]
//   - rcvwinscale (sint16): TCP Receive Window Scaling factor. Offset: [ipv4: 32; ipv6: 56]
//   - sndwinscale (sint16): TCP Send Window Scaling factor. Offset: [ipv4: 34; ipv6: 58]
//   - seqnum (uint32): Sequence number. Offset: [ipv4: 36; ipv6: 60]
//   - connid (uint32/Pointer): Connection identifier (special PointerType). Offset: [ipv4: 40; ipv6: 64]
func (nh *HandlerMof) HandleTCPConnectionAttempted(er *etw.EventRecord) error {
	pid, err := er.GetUint32At(0)
	if err != nil {
		return err
	}
	pData, ok := nh.sm.Processes.GetCurrentProcessDataByPID(pid)
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
//   - Provider Name: TcpIp
//   - Provider GUID: {9a280ac0-c837-4815-a3c4-1d875054ae2e}
//   - Event ID(s): 15 (AcceptIPV4), 31 (AcceptIPV6)
//   - Event Name(s): Accept
//   - Event Version(s): 2
//   - Schema: MOF
//
// Schema (from MOF, TcpIp_TypeGroup2 & TcpIp_TypeGroup4):
//   - PID (uint32): Process identifier. Offset: 0
//   - size (uint32): Packet size. Offset: 4
//   - daddr (object/uint32 or object/[16]byte): Destination IP address. Offset: [ipv4: 8; ipv6: 8]
//   - saddr (object/uint32 or object/[16]byte): Source IP address. Offset: [ipv4: 12; ipv6: 24]
//   - dport (object/uint16): Destination port number. Offset: [ipv4: 16; ipv6: 40]
//   - sport (object/uint16): Source port number. Offset: [ipv4: 18; ipv6: 42]
//   - mss (uint16): Maximum segment size. Offset: [ipv4: 20; ipv6: 44]
//   - sackopt (uint16): SACK option. Offset: [ipv4: 22; ipv6: 46]
//   - tsopt (uint16): Time Stamp option. Offset: [ipv4: 24; ipv6: 48]
//   - wsopt (uint16): Window Scale option. Offset: [ipv4: 26; ipv6: 50]
//   - rcvwin (uint32): TCP Receive Window size. Offset: [ipv4: 28; ipv6: 52]
//   - rcvwinscale (sint16): TCP Receive Window Scaling factor. Offset: [ipv4: 32; ipv6: 56]
//   - sndwinscale (sint16): TCP Send Window Scaling factor. Offset: [ipv4: 34; ipv6: 58]
//   - seqnum (uint32): Sequence number. Offset: [ipv4: 36; ipv6: 60]
//   - connid (uint32/Pointer): Connection identifier (special PointerType). Offset: [ipv4: 40; ipv6: 64]
func (nh *HandlerMof) HandleTCPConnectionAccepted(er *etw.EventRecord) error {
	pid, err := er.GetUint32At(0)
	if err != nil {
		return err
	}
	pData, ok := nh.sm.Processes.GetCurrentProcessDataByPID(pid)
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
//   - Provider Name: TcpIp
//   - Provider GUID: {9a280ac0-c837-4815-a3c4-1d875054ae2e}
//   - Event ID(s): 14 (RetransmitIPV4), 30 (RetransmitIPV6)
//   - Event Name(s): Retransmit
//   - Event Version(s): 2
//   - Schema: MOF
//
// Schema (from MOF, TcpIp_TypeGroup1 & TcpIp_TypeGroup3):
//   - PID (uint32): Process identifier. Offset: 0
//   - size (uint32): Packet size. Offset: 4
//   - daddr (object/uint32 or object/[16]byte): Destination IP address. Offset: [ipv4: 8; ipv6: 8]
//   - saddr (object/uint32 or object/[16]byte): Source IP address. Offset: [ipv4: 12; ipv6: 24]
//   - dport (object/uint16): Destination port number. Offset: [ipv4: 16; ipv6: 40]
//   - sport (object/uint16): Source port number. Offset: [ipv4: 18; ipv6: 42]
//   - seqnum (uint32): Sequence number. Offset: [ipv4: 20; ipv6: 44]
//   - connid (uint32/Pointer): Connection identifier (special PointerType). Offset: [ipv4: 24; ipv6: 48]
func (nh *HandlerMof) HandleTCPDataRetransmitted(er *etw.EventRecord) error {
	pid, err := er.GetUint32At(0)
	if err != nil {
		return err
	}
	pData, ok := nh.sm.Processes.GetCurrentProcessDataByPID(pid)
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
//   - Provider Name: TcpIp
//   - Provider GUID: {9a280ac0-c837-4815-a3c4-1d875054ae2e}
//   - Event ID(s): 17
//   - Event Name(s): Fail
//   - Event Version(s): 2
//   - Schema: MOF
//
// Schema (from MOF, TcpIp_Fail):
//   - Proto (uint16): Protocol type (AF_INET=2, AF_INET6=23). Offset: 0
//   - FailureCode (uint16): Failure code. Offset: 2
//
// Note: This event does not contain a PID. We attribute it to PID 0 ("system").
func (nh *HandlerMof) HandleTCPConnectionFailed(er *etw.EventRecord) error {
	proto, err := er.GetUint16At(0)
	if err != nil {
		return err
	}
	failureCode, err := er.GetUint16At(2)
	if err != nil {
		return err
	}

	addressFamily := AddressFamilyIPV4
	if proto == 23 { // AF_INET6
		addressFamily = AddressFamilyIPV6
	}

	// System-level failures are attributed to the "System" process (PID 4).
	pData, ok := nh.sm.Processes.GetCurrentProcessDataByPID(statemanager.SystemProcessID)
	if !ok {
		nh.log.SampledWarn("network_process_error").Msg("Could not find System process (PID 4) to attribute connection failure.")
		return nil
	}

	nh.collector.RecordConnectionFailed(pData, ProtocolTCP, addressFamily, failureCode)
	return nil
}

// HandleUDPDataSent processes UDP data sent events to track network bytes.
// This handler increments counters for bytes sent over UDP.
//
// ETW Event Details:
//   - Provider Name: UdpIp
//   - Provider GUID: {bf3a50c5-a9c9-4988-a005-2df0b7c80f80}
//   - Event ID(s): 10 (SendIPV4), 26 (SendIPV6)
//   - Event Name(s): Send
//   - Event Version(s): 2
//   - Schema: MOF
//
// Schema (from MOF, UdpIp_TypeGroup1 & UdpIp_TypeGroup2):
//   - PID (uint32): Process identifier. Offset: 0
//   - size (uint32): Number of bytes sent. Offset: 4
//   - daddr (object/uint32 or object/[16]byte): Destination IP address. Offset: [ipv4: 8; ipv6: 8]
//   - saddr (object/uint32 or object/[16]byte): Source IP address. Offset: [ipv4: 12; ipv6: 24]
//   - dport (object/uint16): Destination port number. Offset: [ipv4: 16; ipv6: 40]
//   - sport (object/uint16): Source port number. Offset: [ipv4: 18; ipv6: 42]
//   - seqnum (uint32): Sequence number. Offset: [ipv4: 20; ipv6: 44]
//   - connid (uint32/Pointer): Connection identifier (special PointerType). Offset: [ipv4: 24; ipv6: 48]
func (nh *HandlerMof) HandleUDPDataSent(er *etw.EventRecord) error {
	pid, err := er.GetUint32At(0)
	if err != nil {
		return err
	}
	size, err := er.GetUint32At(4)
	if err != nil {
		return err
	}
	pData, ok := nh.sm.Processes.GetCurrentProcessDataByPID(pid)
	if !ok {
		return nil // Process not known, skip.
	}

	nh.collector.RecordDataSent(pData, ProtocolUDP, size)
	return nil
}

// HandleUDPDataReceived processes UDP data received events to track network bytes.
// This handler increments counters for bytes received over UDP.
//
// ETW Event Details:
//   - Provider Name: UdpIp
//   - Provider GUID: {bf3a50c5-a9c9-4988-a005-2df0b7c80f80}
//   - Event ID(s): 11 (RecvIPV4), 27 (RecvIPV6)
//   - Event Name(s): Recv
//   - Event Version(s): 2
//   - Schema: MOF
//
// Schema (from MOF, UdpIp_TypeGroup1 & UdpIp_TypeGroup2):
//   - PID (uint32): Process identifier. Offset: 0
//   - size (uint32): Number of bytes received. Offset: 4
//   - daddr (object/uint32 or object/[16]byte): Destination IP address. Offset: [ipv4: 8; ipv6: 8]
//   - saddr (object/uint32 or object/[16]byte): Source IP address. Offset: [ipv4: 12; ipv6: 24]
//   - dport (object/uint16): Destination port number. Offset: [ipv4: 16; ipv6: 40]
//   - sport (object/uint16): Source port number. Offset: [ipv4: 18; ipv6: 42]
//   - seqnum (uint32): Sequence number. Offset: [ipv4: 20; ipv6: 44]
//   - connid (uint32/Pointer): Connection identifier (special PointerType). Offset: [ipv4: 24; ipv6: 48]
func (nh *HandlerMof) HandleUDPDataReceived(er *etw.EventRecord) error {
	pid, err := er.GetUint32At(0)
	if err != nil {
		return err
	}
	size, err := er.GetUint32At(4)
	if err != nil {
		return err
	}
	pData, ok := nh.sm.Processes.GetCurrentProcessDataByPID(pid)
	if !ok {
		return nil // Process not known, skip.
	}

	nh.collector.RecordDataReceived(pData, ProtocolUDP, size)
	return nil
}

// HandleUDPConnectionFailed processes UDP connection failure events.
// This handler tracks failed UDP connection attempts.
//
// ETW Event Details:
//   - Provider Name: UdpIp
//   - Provider GUID: {bf3a50c5-a9c9-4988-a005-2df0b7c80f80}
//   - Event ID(s): 17
//   - Event Name(s): Fail
//   - Event Version(s): 2
//   - Schema: MOF
//
// Schema (from MOF, UdpIp_Fail):
//   - Proto (uint16): Protocol type (AF_INET=2, AF_INET6=23). Offset: 0
//   - FailureCode (uint16): Failure code. Offset: 2
//
// Note: This event does not contain a PID. We attribute it to PID 0 ("system").
func (nh *HandlerMof) HandleUDPConnectionFailed(er *etw.EventRecord) error {
	failureCode, err := er.GetUint16At(2)
	if err != nil {
		return err
	}
	proto, err := er.GetUint16At(0)
	if err != nil {
		return err
	}

	addressFamily := AddressFamilyIPV4
	if proto == 23 { // AF_INET6
		addressFamily = AddressFamilyIPV6
	}

	// System-level failures are attributed to the "System" process (PID 4).
	pData, ok := nh.sm.Processes.GetCurrentProcessDataByPID(statemanager.SystemProcessID)
	if !ok {
		nh.log.SampledWarn("network_system_process_error").
			Msg("Could not find System process (PID 4) to attribute connection failure.")
		return nil
	}

	nh.collector.RecordConnectionFailed(pData, ProtocolUDP, addressFamily, failureCode)
	return nil
}
