package kernelsysconfig

import (
	"github.com/phuslu/log"
	"github.com/tekert/goetw/etw"

	"etw_exporter/internal/etw/guids"
	"etw_exporter/internal/etw/handlers"
	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"
)

// Handler processes system configuration events from the NT Kernel Logger.
type Handler struct {
	collector *SysConfCollector
	log       log.Logger
}

// NewSystemConfigHandler creates a new system configuration event handler.
func NewSystemConfigHandler(sm *statemanager.KernelStateManager) *Handler {
	return &Handler{
		collector: NewSystemConfigCollector(sm),
		log:       logger.NewLoggerWithContext("system_config_handler"),
	}
}

// // AttachCollector allows a metrics collector to subscribe to the handler's events.
// func (c *Handler) AttachCollector(collector *ProcessCollector) {
// 	c.log.Debug().Msg("Attaching metrics collector to process handler.")
// 	c.collector = collector
// }

// RegisterRoutes tells the EventHandler which ETW events this handler is interested in.
func (h *Handler) RegisterRoutes(router handlers.Router) {

	// Provider: NT Kernel Logger (EventTraceConfig) ({01853a65-418f-4f36-aefc-dc0f1d2fd235})
	configRoutes := map[uint8]handlers.EventHandlerFunc{
		etw.EVENT_TRACE_TYPE_CONFIG_PHYSICALDISK: h.HandleSystemConfigPhyDisk,
		etw.EVENT_TRACE_TYPE_CONFIG_LOGICALDISK:  h.HandleSystemConfigLogDisk,
		etw.EVENT_TRACE_TYPE_CONFIG_SERVICES:     h.HandleSystemConfigServices,
	}

	handlers.RegisterRoutesForGUID(router, guids.SystemConfigGUID, configRoutes)       // < win 10
	handlers.RegisterRoutesForGUID(router, etw.SystemConfigProviderGuid, configRoutes) // >= win 11

	h.log.Debug().Msg("Registered global system config handler routes")
}

func (c *Handler) GetCollector() *SysConfCollector {
	return c.collector
}

/*
 Useful commands:

 > Get-CimClass -Namespace root\wmi -ClassName "SystemConfig_*" | Select-Object -ExpandProperty CimClassName
 > $class = Get-CimClass -Namespace root\wmi -ClassName "SystemConfig_V2_Processors"
 > $class.CimClassProperties | Format-Table *
*/

// evntrace.h (v10.0.19041.0) defines the following event types for system configuration records.
//
// Event types for system configuration records
//
/*
x = not fully reviewed (check)

	#define EVENT_TRACE_TYPE_CONFIG_CPU             0x0A     // CPU Configuration
	#define EVENT_TRACE_TYPE_CONFIG_PHYSICALDISK    0x0B     // Physical Disk Configuration
	#define EVENT_TRACE_TYPE_CONFIG_LOGICALDISK     0x0C     // Logical Disk Configuration
	#define EVENT_TRACE_TYPE_CONFIG_NIC             0x0D     // NIC Configuration
	#define EVENT_TRACE_TYPE_CONFIG_VIDEO           0x0E     // Video Adapter Configuration
	#define EVENT_TRACE_TYPE_CONFIG_SERVICES        0x0F     // Active Services
	#define EVENT_TRACE_TYPE_CONFIG_POWER           0x10     // ACPI Configuration
	#define EVENT_TRACE_TYPE_CONFIG_NETINFO         0x11     // Networking Configuration
x	#define EVENT_TRACE_TYPE_CONFIG_OPTICALMEDIA    0x12     // Optical Media Configuration

	#define EVENT_TRACE_TYPE_CONFIG_IRQ             0x15     // IRQ assigned to devices
	#define EVENT_TRACE_TYPE_CONFIG_PNP             0x16     // PnP device info
	#define EVENT_TRACE_TYPE_CONFIG_IDECHANNEL      0x17     // Primary/Secondary IDE channel Configuration
x	#define EVENT_TRACE_TYPE_CONFIG_NUMANODE        0x18     // Numa configuration
x	#define EVENT_TRACE_TYPE_CONFIG_PLATFORM        0x19     // Platform Configuration
x	#define EVENT_TRACE_TYPE_CONFIG_PROCESSORGROUP  0x1A     // Processor Group Configuration
x	#define EVENT_TRACE_TYPE_CONFIG_PROCESSORNUMBER 0x1B     // ProcessorIndex -> ProcNumber mapping
x	#define EVENT_TRACE_TYPE_CONFIG_DPI             0x1C     // Display DPI Configuration
x	#define EVENT_TRACE_TYPE_CONFIG_CI_INFO         0x1D     // Display System Code Integrity Information
x	#define EVENT_TRACE_TYPE_CONFIG_MACHINEID       0x1E     // SQM Machine Id
x	#define EVENT_TRACE_TYPE_CONFIG_DEFRAG          0x1F     // Logical Disk Defragmenter Information
x	#define EVENT_TRACE_TYPE_CONFIG_MOBILEPLATFORM  0x20     // Mobile Platform Configuration
x	#define EVENT_TRACE_TYPE_CONFIG_DEVICEFAMILY    0x21     // Device Family Information
x	#define EVENT_TRACE_TYPE_CONFIG_FLIGHTID        0x22     // Flights on the machine
x	#define EVENT_TRACE_TYPE_CONFIG_PROCESSOR       0x23     // CentralProcessor recordsx
x	#define EVENT_TRACE_TYPE_CONFIG_VIRTUALIZATION  0x24     // virtualization config info
x	#define EVENT_TRACE_TYPE_CONFIG_BOOT            0x25     // boot config info
*/

// HandleSystemConfigPhyDisk processes physical disk configuration events to enrich disk metrics.
// This handler processes the event type class for physical disk configuration events.
// It collects static information about the physical disks in the system,
// such as the manufacturer, to provide descriptive labels for disk metrics.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (EventTraceConfig)
//   - Provider GUID: {01853a65-418f-4f36-aefc-dc0f1d2fd235}
//   - Event ID(s): 11
//   - Event Name(s): PhyDisk
//   - Event Version(s): 0
//   - Schema: MOF
//
// Schema (from MOF):
//   - DiskNumber (uint32): Index number of the disk.
//   - BytesPerSector (uint32): Number of bytes in each sector for the physical disk drive.
//   - SectorsPerTrack (uint32): Number of sectors in each track for this physical disk drive.
//   - TracksPerCylinder (uint32): Number of tracks in each cylinder on the physical disk drive.
//   - Cylinders (uint64): Total number of cylinders on the physical disk drive.
//   - SCSIPort (uint32): SCSI number of the SCSI adapter.
//   - SCSIPath (uint32): SCSI bus number of the SCSI adapter.
//   - SCSITarget (uint32): Contains the number of the target device.
//   - SCSILun (uint32): SCSI logical unit number (LUN) of the SCSI adapter.
//   - Manufacturer (string): Name of the disk drive manufacturer. [Max Length: 256]
//   - PartitionCount (uint32): Number of partitions on this physical disk drive that are recognized by the OS.
//   - WriteCacheEnabled (uint8): True if the write cache is enabled.
//   - Pad (uint8): Not used.
//   - BootDriveLetter (string): Drive letter of the boot drive in the form, "<letter>:". [Max Length: 3]
//   - Spare (string): Not used. [Max Length: 2]
//
// These events provide a one-time snapshot of the disk hardware configuration
// when an NT Kernel Logger session is stopped.
// https://learn.microsoft.com/en-us/windows/win32/etw/systemconfig-phydisk
func (h *Handler) HandleSystemConfigPhyDisk(helper *etw.EventRecordHelper) error {
	diskNumber, err := helper.GetPropertyUint("DiskNumber")
	if err != nil {
		h.log.Error().Err(err).Msg("Failed to get DiskNumber for physical disk config")
		return err
	}

	manufacturer, err := helper.GetPropertyString("Manufacturer")
	if err != nil {
		manufacturer = "Unknown"
	}

	info := PhysicalDiskInfo{
		DiskNumber:   uint32(diskNumber),
		Manufacturer: manufacturer,
	}
	h.collector.AddPhysicalDisk(info)

	return nil
}

// HandleSystemConfigLogDisk processes logical disk configuration events to enrich disk metrics.
// This handler processes the event type class for logical disk configuration events.
// It collects static information about logical disks (volumes/partitions),
// such as drive letter and file system type.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (EventTraceConfig)
//   - Provider GUID: {01853a65-418f-4f36-aefc-dc0f1d2fd235}
//   - Event ID(s): 12
//   - Event Name(s): LogDisk
//   - Event Version(s): 0
//   - Schema: MOF
//
// Schema (from MOF):
//   - StartOffset (uint64): Starting offset (in bytes) of the partition from the beginning of the disk.
//   - PartitionSize (uint64): Total size of the partition, in bytes.
//   - DiskNumber (uint32): Index number of the disk containing this partition.
//   - Size (uint32): Size of the disk drive, in bytes.
//   - DriveType (uint32): Type of disk drive (e.g., 1=Partition, 2=Volume).
//   - DriveLetterString (string): Drive letter of the disk in the form, "<letter>:". [Max Length: 4]
//   - Pad1 (uint32): Not used.
//   - PartitionNumber (uint32): Index number of the partition.
//   - SectorsPerCluster (uint32): Number of sectors in the volume.
//   - BytesPerSector (uint32): Number of bytes in each sector for the physical disk drive.
//   - Pad2 (uint32): Not used.
//   - NumberOfFreeClusters (sint64): Number of free clusters in the specified volume.
//   - TotalNumberOfClusters (sint64): Number of used and free clusters in the volume.
//   - FileSystem (string): File system on the logical disk (e.g., "NTFS"). [Max Length: 16]
//   - VolumeExt (uint32): Reserved.
//   - Pad3 (uint32): Not used.
//
// These events provide a one-time snapshot of the logical disk configuration
// when an NT Kernel Logger session is stopped.
// https://learn.microsoft.com/en-us/windows/win32/etw/systemconfig-logdisk
func (h *Handler) HandleSystemConfigLogDisk(helper *etw.EventRecordHelper) error {
	diskNumber, err := helper.GetPropertyUint("DiskNumber")
	if err != nil {
		h.log.Error().Err(err).Msg("Failed to get DiskNumber for logical disk config")
		return err
	}

	driveLetter, err := helper.GetPropertyString("DriveLetterString")
	if err != nil {
		driveLetter = ""
	}

	fileSystem, err := helper.GetPropertyString("FileSystem")
	if err != nil {
		fileSystem = "Unknown"
	}

	info := LogicalDiskInfo{
		DiskNumber:        uint32(diskNumber),
		DriveLetterString: driveLetter,
		FileSystem:        fileSystem,
	}
	h.collector.AddLogicalDisk(info)

	return nil
}

// HandleSystemConfigCPU processes CPU configuration events.
// This handler processes the event type class for CPU configuration events.
// It collects static information about the CPU configuration in the system,
// such as processor speed, memory size, and system information.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (EventTraceConfig)
//   - Provider GUID: {01853a65-418f-4f36-aefc-dc0f1d2fd235}
//   - Event ID(s): 10
//   - Event Name(s): CPU
//   - Event Version(s): 0
//   - Schema: MOF
//
// Schema (from MOF):
//   - MHz (uint32): Maximum speed of the processor, in megahertz.
//   - NumberOfProcessors (uint32): Number of processors on the computer.
//   - MemSize (uint32): Total amount of physical memory available to the operating system.
//   - PageSize (uint32): Size of a swap page, in bytes.
//   - AllocationGranularity (uint32): Granularity with which virtual memory is allocated.
//   - ComputerName (char16[]): Name of the computer. [Max Length: 256]
//   - DomainName (char16[]): Name of the domain in which the computer is a member. [Max Length: 132]
//   - HyperThreadingFlag (uint32): Indicates if the hyper-threading option is on or off for a processor. Each bit reflects the hyper-threading state of a CPU on the computer.
//
// These events provide a one-time snapshot of the CPU hardware configuration
// when an NT Kernel Logger session is stopped.
// https://learn.microsoft.com/en-us/windows/win32/etw/systemconfig-cpu
func (h *Handler) HandleSystemConfigCPU(helper *etw.EventRecordHelper) error {
	return nil
}

// HandleSystemConfigNIC processes network interface card configuration events.
// This handler processes the event type class for NIC configuration events.
// It collects static information about the network interfaces in the system,
// such as MAC address, IP addresses, and adapter descriptions.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (EventTraceConfig)
//   - Provider GUID: {01853a65-418f-4f36-aefc-dc0f1d2fd235}
//   - Event ID(s): 13
//   - Event Name(s): NIC
//   - Event Version(s): 0
//   - Schema: MOF
//
// Schema (from MOF):
//   - PhysicalAddr (uint64): Hardware address for the adapter.
//   - PhysicalAddrLen (uint32): Length of the hardware address for the adapter.
//   - Ipv4Index (uint32): Adapter index for IPv4 NIC. The adapter index may change when an adapter is disabled and then enabled, or under other circumstances, and should not be considered persistent.
//   - Ipv6Index (uint32): Adapter index for IPv6 NIC. The adapter index may change when an adapter is disabled and then enabled, or under other circumstances, and should not be considered persistent.
//   - NICDescription (string): Description of the adapter.
//   - IpAddresses (string): IP addresses associated with the network interface card. The list of addresses is comma-delimited.
//   - DnsServerAddresses (string): IP addresses to be used in querying for DNS servers. The list of addresses is comma-delimited.
//
// These events provide a one-time snapshot of the network interface configuration
// when an NT Kernel Logger session is stopped.
// https://learn.microsoft.com/en-us/windows/win32/etw/systemconfig-nic
func (h *Handler) HandleSystemConfigNIC(helper *etw.EventRecordHelper) error {
	return nil
}

// HandleSystemConfigVideo processes video configuration events.
// This handler processes the event type class for video configuration events.
// It collects static information about the video adapters in the system,
// such as resolution, color depth, and adapter details.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (EventTraceConfig)
//   - Provider GUID: {01853a65-418f-4f36-aefc-dc0f1d2fd235}
//   - Event ID(s): 14
//   - Event Name(s): Video
//   - Event Version(s): 0
//   - Schema: MOF
//
// Schema (from MOF):
//   - MemorySize (uint32): Maximum amount of memory supported, in bytes.
//   - XResolution (uint32): Current number of horizontal pixels.
//   - YResolution (uint32): Current number of vertical pixels.
//   - BitsPerPixel (uint32): Number of bits used to display each pixel.
//   - VRefresh (uint32): Current refresh rate, in hertz.
//   - ChipType (char16[]): Chip name of the adapter. [Max Length: 256]
//   - DACType (char16[]): Digital-to-analog converter (DAC) chip name of the adapter. [Max Length: 256]
//   - AdapterString (char16[]): Name or description of the adapter. [Max Length: 256]
//   - BiosString (char16[]): BIOS name of the adapter. [Max Length: 256]
//   - DeviceId (char16[]): Address or other identifying information to uniquely name the logical device. [Max Length: 256]
//   - StateFlags (uint32): Device state flags. It can be any reasonable combination of the following:
//   - DISPLAY_DEVICE_ATTACHED_TO_DESKTOP 1 (0x1): The device is part of the desktop.
//   - DISPLAY_DEVICE_MIRRORING_DRIVER 8 (0x8): Represents a pseudo device used to mirror application drawing for connecting to a remote computer or other purposes.
//   - DISPLAY_DEVICE_MODESPRUNED 134217728 (0x8000000): The device has more display modes than its output devices support.
//   - DISPLAY_DEVICE_PRIMARY_DEVICE 4 (0x4): The primary desktop is on the device.
//   - DISPLAY_DEVICE_REMOVABLE 32 (0x20): The device is removable; it cannot be the primary display.
//   - DISPLAY_DEVICE_VGA_COMPATIBLE 16 (0x10): The device is VGA compatible.
//
// These events provide a one-time snapshot of the video adapter configuration
// when an NT Kernel Logger session is stopped.
// https://learn.microsoft.com/en-us/windows/win32/etw/systemconfig-video
func (h *Handler) HandleSystemConfigVideo(helper *etw.EventRecordHelper) error {
	return nil
}

// HandleSystemConfigServices processes services configuration events.
// This handler processes the event type class for services configuration events.
// It collects static information about the active services in the system,
// such as service names, display names, and process information.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (EventTraceConfig)
//   - Provider GUID: {01853a65-418f-4f36-aefc-dc0f1d2fd235}
//   - Event ID(s): 15
//   - Event Name(s): Services
//   - Event Version(s): 0
//   - Schema: MOF
//
// Schema (from MOF):
//   - ProcessId (uint32): Identifier of the process in which the service runs.
//   - ServiceState (uint32): Current state of the service. For possible values, see the dwCurrentState member of SERVICE_STATUS_PROCESS.
//   - SubProcessTag (uint32): Identifies the service.
//   - ServiceName (string[]): Unique identifier of the service. The identifier provides an indication of the functionality the service provides.
//   - DisplayName (string[]): Display name of the service. The name is case-preserved in the Service Control Manager. However, display name comparisons are always case-insensitive.
//   - ProcessName (string[]): Name of the process in which the service runs.
//
// These events provide a one-time snapshot of the active services configuration
// when an NT Kernel Logger session is stopped.
// https://learn.microsoft.com/en-us/windows/win32/etw/systemconfig-services
func (h *Handler) HandleSystemConfigServices(helper *etw.EventRecordHelper) error {
	subProcessTag, err := helper.GetPropertyUint("SubProcessTag")
	if err != nil {
		h.log.Error().Err(err).Msg("Failed to get SubProcessTag for services config")
		return err
	}

	ProcessId, err := helper.GetPropertyUint("ProcessId")
	if err != nil {
		h.log.Error().Err(err).Msg("Failed to get ProcessId for services config")
		return err
	}

	displayName, err := helper.GetPropertyString("DisplayName")
	if err != nil {
		// Log the service display name for informational purposes.
		h.log.Warn().Err(err).Msgf("Service Display Name: %s", displayName)
	}

	// The MOF schema indicates ServiceName is a string array, but in practice it's a single string.
	serviceName, err := helper.GetPropertyString("ServiceName")
	if err != nil {
		h.log.Error().Err(err).Msg("Failed to get ServiceName for services config")
		return err
	}

	// Pass the raw data to the collector, which will handle updating the state manager.
	info := ServiceInfo{
		ProcessId:     uint32(ProcessId),
		DisplayName:   displayName,
		SubProcessTag: uint32(subProcessTag),
		ServiceName:   serviceName,
	}
	h.collector.AddServiceInfo(info)

	return nil
}

// HandleSystemConfigPower processes power configuration events.
// This handler processes the event type class for power configuration events.
// It collects static information about the power management capabilities of the system,
// such as supported sleep states.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (EventTraceConfig)
//   - Provider GUID: {01853a65-418f-4f36-aefc-dc0f1d2fd235}
//   - Event ID(s): 16
//   - Event Name(s): Power
//   - Event Version(s): 0
//   - Schema: MOF
//
// Schema (from MOF):
//   - s1 (uint8): True indicates the system supports sleep state S1.
//   - s2 (uint8): True indicates the system supports sleep state S2.
//   - s3 (uint8): True indicates the system supports sleep state S3.
//   - s4 (uint8): True indicates the system supports sleep state S4.
//   - s5 (uint8): True indicates the system supports sleep state S5.
//   - Pad1 (uint8): Not used.
//   - Pad2 (uint8): Not used.
//   - Pad3 (uint8): Not used.
//
// These events provide a one-time snapshot of the power management configuration
// when an NT Kernel Logger session is stopped.
// https://learn.microsoft.com/en-us/windows/win32/etw/systemconfig-power
func (h *Handler) HandleSystemConfigPower(helper *etw.EventRecordHelper) error {
	return nil
}

// HandleSystemConfigNetwork processes network configuration events.
// This handler processes the event type class for network configuration events.
// It collects static information about the network stack configuration of the system,
// such as TCP settings and port ranges.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (EventTraceConfig)
//   - Provider GUID: {01853a65-418f-4f36-aefc-dc0f1d2fd235}
//   - Event ID(s): 17
//   - Event Name(s): Network
//   - Event Version(s): 0
//   - Schema: MOF
//
// Schema (from MOF):
//   - TcbTablePartitions (uint32): The number of partitions in the Transport Control Block table. Partitioning the Transport Control Block table minimizes contention for table access. This is especially useful on multiprocessor systems.
//   - MaxHashTableSize (uint32): The size of the hash table in which TCP control blocks (TCBs) are stored. TCP stores control blocks in a hash table so it can find them very quickly.
//   - MaxUserPort (uint32): The highest port number TCP can assign when an application requests an available user port from the system. Typically, ephemeral ports (those used briefly) are allocated to port numbers 1024 through 5000. The value for the highest user port number TCP can assign is controlled by a registry setting.
//   - TcpTimedWaitDelay (uint32): The time that must elapse before TCP can release a closed connection and reuse its resources. This interval between closure and release is known as the TIME_WAIT state or 2MSL state. During this time, the connection can be reopened at much less cost to the client and server than establishing a new connection.
//
// These events provide a one-time snapshot of the network stack configuration
// when an NT Kernel Logger session is stopped.
// https://learn.microsoft.com/en-us/windows/win32/etw/systemconfig-network
func (h *Handler) HandleSystemConfigNetwork(helper *etw.EventRecordHelper) error {
	return nil
}

// HandleSystemConfigIRQ processes interrupt request configuration events.
// This handler processes the event type class for IRQ configuration events.
// It collects static information about the interrupt request (IRQ) assignments in the system,
// such as IRQ numbers and device descriptions.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (EventTraceConfig)
//   - Provider GUID: {01853a65-418f-4f36-aefc-dc0f1d2fd235}
//   - Event ID(s): 21
//   - Event Name(s): IRQ
//   - Event Version(s): 0
//   - Schema: MOF
//
// Schema (from MOF):
//   - IRQAffinity (uint64): IRQ affinity mask. The affinity mask identifies the specific processors (or groups of processors) that can receive the IRQ.
//   - IRQNum (uint32): Interrupt request line number.
//   - DeviceDescriptionLen (uint32): Length, in characters, of the string in DeviceDescription.
//   - DeviceDescription (string): Description of the device or software making the request.
//
// These events provide a one-time snapshot of the IRQ assignments
// when an NT Kernel Logger session is stopped.
// https://learn.microsoft.com/en-us/windows/win32/etw/systemconfig-irq
func (h *Handler) HandleSystemConfigIRQ(helper *etw.EventRecordHelper) error {
	return nil
}

// HandleSystemConfigPnP processes Plug and Play configuration events.
// This handler processes the event type class for PnP configuration events.
// It collects static information about the Plug and Play devices in the system,
// such as device IDs, descriptions, and friendly names.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (EventTraceConfig)
//   - Provider GUID: {01853a65-418f-4f36-aefc-dc0f1d2fd235}
//   - Event ID(s): 22
//   - Event Name(s): PnP
//   - Event Version(s): 0
//   - Schema: MOF
//
// Schema (from MOF):
//   - IDLength (uint32): Length, in characters, of the DeviceID string.
//   - DescriptionLength (uint32): Length, in characters, of the DeviceDescription string.
//   - FriendlyNameLength (uint32): Length, in characters, of the FriendlyName string.
//   - DeviceID (string): Identifies the PnP device.
//   - DeviceDescription (string): Description of the PnP device.
//   - FriendlyName (string): Name of the PnP device to use in a user interface.
//
// These events provide a one-time snapshot of the PnP device configuration
// when an NT Kernel Logger session is stopped.
// https://learn.microsoft.com/en-us/windows/win32/etw/systemconfig-pnp
func (h *Handler) HandleSystemConfigPnP(helper *etw.EventRecordHelper) error {
	return nil
}

// HandleSystemConfigIDEChannel processes IDE channel configuration events.
// This handler processes the event type class for IDE channel configuration events.
// It collects static information about the IDE channels in the system,
// such as device types, timing modes, and location information.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (EventTraceConfig)
//   - Provider GUID: {01853a65-418f-4f36-aefc-dc0f1d2fd235}
//   - Event ID(s): 23
//   - Event Name(s): IDEChannel
//   - Event Version(s): 0
//   - Schema: MOF
//
// Schema (from MOF):
//   - TargetId (uint32): The identifier of the disk.
//   - DeviceType (uint32): The device type. The following are the possible values:
//   - ATA (1)
//   - ATAPI (2)
//   - DeviceTimingMode (uint32): The mode in which the IDE could function. The following are the possible values:
//   - PIO_MODE0 (0x1)
//   - PIO_MODE1 (0x2)
//   - PIO_MODE2 (0x4)
//   - PIO_MODE3 (0x8)
//   - PIO_MODE4 (0x10)
//   - SWDMA_MODE0 (0x20)
//   - SWDMA_MODE1 (0x40)
//   - SWDMA_MODE2 (0x80)
//   - MWDMA_MODE0 (0x100)
//   - MWDMA_MODE2 (0x200)
//   - MWDMA_MODE3 (0x400)
//   - UDMA_MODE0 (0x800)
//   - UDMA_MODE1 (0x1000)
//   - UDMA_MODE2 (0x2000)
//   - UDMA_MODE3 (0x4000)
//   - UDMA_MODE4 (0x8000)
//   - UDMA_MODE5 (0x10000)
//   - LocationInformationLen (uint32): Length of the LocationInformation string.
//   - LocationInformation (string): The IDE channel (for example, Primary Channel, Secondary Channel, and so on).
//
// These events provide a one-time snapshot of the IDE channel configuration
// when an NT Kernel Logger session is stopped.
// https://learn.microsoft.com/en-us/windows/win32/etw/systemconfig-idechannel
func (h *Handler) HandleSystemConfigIDEChannel(helper *etw.EventRecordHelper) error {
	return nil
}

// HandleSystemConfigOpticalMedia processes optical media configuration events.
// This handler processes events containing information about optical drives and media,
// such as drive letters, file systems, and manufacturer names.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (EventTraceConfig)
//   - Provider GUID: {01853a65-418f-4f36-aefc-dc0f1d2fd235}
//   - Event ID(s) (Opcodes): 18
//   - Event Name(s): OpticalMedia
//   - Event Version(s): 2
//   - Schema: MOF (SystemConfig_V2_OpticalMedia)
//
// Schema (from MOF):
//   - DiskNumber (uint16): The disk number of the optical drive.
//   - BusType (uint16): The bus type of the optical drive.
//   - DeviceType (uint16): The device type of the optical drive.
//   - MediaType (uint16): The type of media currently in the drive.
//   - StartingOffset (uint64): The starting offset of the media.
//   - Size (uint64): The total size of the media in bytes.
//   - NumberOfFreeBlocks (uint64): The number of free blocks available on the media.
//   - TotalNumberOfBlocks (uint64): The total number of blocks on the media.
//   - NextWritableAddress (uint64): The next writable address on the media.
//   - NumberOfSessions (uint32): The number of sessions on the media.
//   - NumberOfTracks (uint32): The number of tracks on the media.
//   - BytesPerSector (uint32): The number of bytes per sector on the media.
//   - DiscStatus (uint16): The status of the disc.
//   - LastSessionStatus (uint16): The status of the last session.
//   - DriveLetter (string): The drive letter assigned to the optical drive. [StringTermination("NullTerminated")]
//   - FileSystemName (string): The name of the file system on the media. [StringTermination("NullTerminated")]
//   - DeviceName (string): The device name of the optical drive. [StringTermination("NullTerminated")]
//   - ManufacturerName (string): The manufacturer name of the optical drive. [StringTermination("NullTerminated")]
func (h *Handler) HandleSystemConfigOpticalMedia(helper *etw.EventRecordHelper) error {
	return nil
}

// HandleSystemConfigNumaNode processes NUMA node configuration events.
// This handler processes events describing the system's Non-Uniform Memory Access (NUMA) topology,
// which is important for performance analysis on multi-socket systems.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (EventTraceConfig)
//   - Provider GUID: {01853a65-418f-4f36-aefc-dc0f1d2fd235}
//   - Event ID(s) (Opcodes): 24
//   - Event Name(s): NumaNode
//   - Event Version(s): 2
//   - Schema: MOF (SystemConfig_V2_NumaNode)
//
// Schema (from MOF):
//   - NodeCount (uint32): The total number of NUMA nodes in the system.
//   - NodeMap (uint64[]): A bitmap representing the mapping of nodes. [WmiSizeIs("NodeCount")]
func (h *Handler) HandleSystemConfigNumaNode(helper *etw.EventRecordHelper) error {
	return nil
}

// HandleSystemConfigPlatform processes platform configuration events.
// This handler processes events containing system manufacturer and BIOS information,
// which is useful for identifying the hardware platform.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (EventTraceConfig)
//   - Provider GUID: {01853a65-418f-4f36-aefc-dc0f1d2fd235}
//   - Event ID(s) (Opcodes): 25
//   - Event Name(s): Platform
//   - Event Version(s): 2
//   - Schema: MOF (SystemConfig_V2_Platform)
//
// Schema (from MOF):
//   - SystemManufacturer (string): The manufacturer of the computer system. [StringTermination("NullTerminated")]
//   - SystemProductName (string): The product name of the computer system. [StringTermination("NullTerminated")]
//   - BiosDate (string): The release date of the system BIOS. [StringTermination("NullTerminated")]
//   - BiosVersion (string): The version of the system BIOS. [StringTermination("NullTerminated")]
func (h *Handler) HandleSystemConfigPlatform(helper *etw.EventRecordHelper) error {
	return nil
}

// HandleSystemConfigProcGroup processes processor group configuration events.
// This handler processes events describing processor groups and their affinities,
// which is relevant for systems with more than 64 logical processors.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (EventTraceConfig)
//   - Provider GUID: {01853a65-418f-4f36-aefc-dc0f1d2fd235}
//   - Event ID(s) (Opcodes): 26
//   - Event Name(s): ProcGroup
//   - Event Version(s): 2
//   - Schema: MOF (SystemConfig_V2_ProcGroup)
//
// Schema (from MOF):
//   - GroupCount (uint32): The number of processor groups.
//   - Affinity (uint32[]): An array of affinity masks for each group. [WmiSizeIs("GroupCount")]
func (h *Handler) HandleSystemConfigProcGroup(helper *etw.EventRecordHelper) error {
	return nil
}

// HandleSystemConfigProcNumber processes processor number mapping events.
// This handler processes events that map processor indices to their corresponding processor numbers,
// providing a system-wide view of processor enumeration.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (EventTraceConfig)
//   - Provider GUID: {01853a65-418f-4f36-aefc-dc0f1d2fd235}
//   - Event ID(s) (Opcodes): 27
//   - Event Name(s): ProcNumber
//   - Event Version(s): 2
//   - Schema: MOF (SystemConfig_V2_ProcNumber)
//
// Schema (from MOF):
//   - ProcessorCount (uint32): The total number of processors being mapped.
//   - ProcessorNumber (uint32[]): An array of processor numbers. [WmiSizeIs("ProcessorCount")]
func (h *Handler) HandleSystemConfigProcNumber(helper *etw.EventRecordHelper) error {
	return nil
}

// HandleSystemConfigDPI processes display DPI configuration events.
// This handler processes events related to machine-wide and per-user Dots Per Inch (DPI) settings,
// which affect UI scaling.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (EventTraceConfig)
//   - Provider GUID: {01853a65-418f-4f36-aefc-dc0f1d2fd235}
//   - Event ID(s) (Opcodes): 28
//   - Event Name(s): DPI
//   - Event Version(s): 2
//   - Schema: MOF (SystemConfig_V2_DPI)
//
// Schema (from MOF):
//   - MachineDPI (uint32): The system-wide DPI setting.
//   - UserDPI (uint32): The DPI setting for the current user.
func (h *Handler) HandleSystemConfigDPI(helper *etw.EventRecordHelper) error {
	return nil
}

// HandleSystemConfigCodeIntegrity processes Code Integrity (CI) information events.
// This handler captures information about the system's code integrity configuration,
// which is a key security feature.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (EventTraceConfig)
//   - Provider GUID: {01853a65-418f-4f36-aefc-dc0f1d2fd235}
//   - Event ID(s) (Opcodes): 29
//   - Event Name(s): CI_INFO
//   - Event Version(s): 2
//   - Schema: MOF (SystemConfig_V2_CodeIntegrity)
//
// Schema (from MOF):
//   - CodeIntegrityInfo (uint32): A bitmask containing flags about the code integrity state.
func (h *Handler) HandleSystemConfigCodeIntegrity(helper *etw.EventRecordHelper) error {
	return nil
}

// HandleSystemConfigMachineID processes SQM (Software Quality Metrics) Machine ID events.
// This handler captures the unique, non-personally identifiable machine ID used for telemetry.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (EventTraceConfig)
//   - Provider GUID: {01853a65-418f-4f36-aefc-dc0f1d2fd235}
//   - Event ID(s) (Opcodes): 30
//   - Event Name(s): MachineId
//   - Event Version(s): 2
//   - Schema: MOF (SystemConfig_V2_TelemetryInfo)
//
// Schema (from MOF):
//   - MachineId (GUID): The unique machine identifier. [extension("GUID")]
func (h *Handler) HandleSystemConfigMachineID(helper *etw.EventRecordHelper) error {
	return nil
}

// HandleSystemConfigDefrag processes logical disk defragmenter information events.
// This handler captures detailed statistics about the state of a volume's fragmentation
// and the results of defragmentation runs.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (EventTraceConfig)
//   - Provider GUID: {01853a65-418f-4f36-aefc-dc0f1d2fd235}
//   - Event ID(s) (Opcodes): 31
//   - Event Name(s): Defrag
//   - Event Version(s): 2
//   - Schema: MOF (SystemConfig_V2_Defrag)
//
// Schema (from MOF):
//   - Contains numerous fields detailing volume and defragmentation statistics.
//   - VolumeId (GUID): The unique identifier for the volume. [extension("GUID")]
//   - VolumePathNames (string): The path names for the volume. [StringTermination("NullTerminated")]
func (h *Handler) HandleSystemConfigDefrag(helper *etw.EventRecordHelper) error {
	return nil
}

// HandleSystemConfigMobilePlatform processes mobile platform configuration events.
// This handler captures detailed hardware and software identification for mobile-class devices.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (EventTraceConfig)
//   - Provider GUID: {01853a65-418f-4f36-aefc-dc0f1d2fd235}
//   - Event ID(s) (Opcodes): 32
//   - Event Name(s): MobilePlatform
//   - Event Version(s): 2, 3
//   - Schema: MOF (SystemConfig_V2_MobilePlatform, SystemConfig_V3_MobilePlatform)
//
// Schema (from MOF):
//   - Contains various string fields identifying the device manufacturer, model, operator, and firmware versions.
//   - All string fields are [StringTermination("NullTerminated")].
func (h *Handler) HandleSystemConfigMobilePlatform(helper *etw.EventRecordHelper) error {
	return nil
}

// HandleSystemConfigDeviceFamily processes device family information events.
// This handler captures the device family (e.g., Desktop, Mobile, Xbox) and form factor.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (EventTraceConfig)
//   - Provider GUID: {01853a65-418f-4f36-aefc-dc0f1d2fd235}
//   - Event ID(s) (Opcodes): 33
//   - Event Name(s): DeviceFamily
//   - Event Version(s): 2
//   - Schema: MOF (SystemConfig_V2_DeviceFamily)
//
// Schema (from MOF):
//   - UAPInfo (uint64): Universal Application Platform information.
//   - DeviceFamily (uint32): An identifier for the family of devices.
//   - DeviceForm (uint32): An identifier for the specific form factor of the device.
func (h *Handler) HandleSystemConfigDeviceFamily(helper *etw.EventRecordHelper) error {
	return nil
}

// HandleSystemConfigFlightIDs processes flighting (Windows Insider Program) information events.
// This handler captures the IDs associated with pre-release software builds (flights) on the machine.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (EventTraceConfig)
//   - Provider GUID: {01853a65-418f-4f36-aefc-dc0f1d2fd235}
//   - Event ID(s) (Opcodes): 34
//   - Event Name(s): FlightId
//   - Event Version(s): 2
//   - Schema: MOF (SystemConfig_V2_FlightIds)
//
// Schema (from MOF):
//   - UpdateId (string): The ID of the update. [StringTermination("NullTerminated")]
//   - FlightIdList (string): A list of flight IDs active on the machine. [StringTermination("NullTerminated")]
func (h *Handler) HandleSystemConfigFlightIDs(helper *etw.EventRecordHelper) error {
	return nil
}

// HandleSystemConfigProcessor processes detailed central processor records.
// This handler captures detailed information about each processor in the system,
// including its name, identifier, and feature set.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (EventTraceConfig)
//   - Provider GUID: {01853a65-418f-4f36-aefc-dc0f1d2fd235}
//   - Event ID(s) (Opcodes): 35
//   - Event Name(s): Processor
//   - Event Version(s): 2
//   - Schema: MOF (SystemConfig_V2_Processors)
//
// Schema (from MOF):
//   - ProcessorIndex (uint32): The index of the processor.
//   - FeatureSet (uint32): A bitmask of processor features.
//   - ProcessorSpeed (uint32): The speed of the processor in MHz.
//   - ProcessorName (string): The name of the processor. [MAX(64)]
//   - VendorIdentifier (string): The vendor identifier string. [MAX(16)]
//   - ProcessorIdentifier (string): The detailed processor identifier string. [MAX(128)]
func (h *Handler) HandleSystemConfigProcessor(helper *etw.EventRecordHelper) error {
	return nil
}

// HandleSystemConfigVirtualization processes virtualization configuration information events.
// This handler captures the status of virtualization-based security (VBS), Hypervisor-Enforced
// Code Integrity (HVCI), and whether a hypervisor is present.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (EventTraceConfig)
//   - Provider GUID: {01853a65-418f-4f36-aefc-dc0f1d2fd235}
//   - Event ID(s) (Opcodes): 36
//   - Event Name(s): Virtualization
//   - Event Version(s): 2
//   - Schema: MOF (SystemConfig_Virtualization)
//
// Schema (from MOF):
//   - VbsEnabled (uint8): True if Virtualization-Based Security is enabled.
//   - HvciEnabled (uint8): True if Hypervisor-Enforced Code Integrity is enabled.
//   - HyperVisorEnabled (uint8): True if a hypervisor is running.
//   - Reserved (uint8): Reserved for future use.
func (h *Handler) HandleSystemConfigVirtualization(helper *etw.EventRecordHelper) error {
	return nil
}

// HandleSystemConfigBoot processes boot configuration information events.
// This handler captures key information about the system's boot process, including
// firmware type and Secure Boot status.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger (EventTraceConfig)
//   - Provider GUID: {01853a65-418f-4f36-aefc-dc0f1d2fd235}
//   - Event ID(s) (Opcodes): 37
//   - Event Name(s): Boot
//   - Event Version(s): 2
//   - Schema: MOF (SystemConfig_Boot)
//
// Schema (from MOF):
//   - BootFlags (uint64): Flags related to the boot process.
//   - FirmwareType (uint32): The type of firmware (e.g., BIOS, UEFI).
//   - SecureBootEnabled (uint8): True if Secure Boot is currently enabled.
//   - SecureBootCapable (uint8): True if the system is capable of Secure Boot.
//   - Reserved1 (uint8): Reserved for future use.
//   - Reserved2 (uint8): Reserved for future use.
func (h *Handler) HandleSystemConfigBoot(helper *etw.EventRecordHelper) error {
	return nil
}
