package ksystemconfig

import (
	"github.com/phuslu/log"
	"github.com/tekert/goetw/etw"

	"etw_exporter/internal/logger"
)

// SystemConfigHandler processes system configuration events from the NT Kernel Logger.
type SystemConfigHandler struct {
	collector *SystemConfigCollector
	log       log.Logger
}

// NewSystemConfigHandler creates a new system configuration event handler.
func NewSystemConfigHandler() *SystemConfigHandler {
	return &SystemConfigHandler{
		collector: GetGlobalSystemConfigCollector(),
		log:       logger.NewLoggerWithContext("system_config_handler"),
	}
}

// HandleSystemConfigPhyDisk processes physical disk configuration events to enrich disk metrics.
// This handler collects static information about the physical disks in the system,
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
//   - BytesPerSector (uint32): Number of bytes in each sector.
//   - SectorsPerTrack (uint32): Number of sectors in each track.
//   - TracksPerCylinder (uint32): Number of tracks in each cylinder.
//   - Cylinders (uint64): Total number of cylinders.
//   - SCSIPort (uint32): SCSI port number.
//   - SCSIPath (uint32): SCSI path (bus) number.
//   - SCSITarget (uint32): SCSI target device number.
//   - SCSILun (uint32): SCSI logical unit number (LUN).
//   - Manufacturer (string): Name of the disk drive manufacturer. [Max Length: 256]
//   - PartitionCount (uint32): Number of partitions on the disk.
//   - WriteCacheEnabled (uint8): True if the write cache is enabled.
//   - Pad (uint8): Not used.
//   - BootDriveLetter (string): Drive letter of the boot drive. [Max Length: 3]
//   - Spare (string): Not used. [Max Length: 2]
//
// These events provide a one-time snapshot of the disk hardware configuration
// when an NT Kernel Logger session is stopped.
func (h *SystemConfigHandler) HandleSystemConfigPhyDisk(helper *etw.EventRecordHelper) error {
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
// This handler collects static information about logical disks (volumes/partitions),
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
//   - StartOffset (uint64): Starting offset of the partition in bytes.
//   - PartitionSize (uint64): Total size of the partition in bytes.
//   - DiskNumber (uint32): Index number of the physical disk for this partition.
//   - Size (uint32): Size of the disk drive in bytes.
//   - DriveType (uint32): Type of disk drive (e.g., Partition, Volume).
//   - DriveLetterString (string): Drive letter of the disk (e.g., "C:"). [Max Length: 4]
//   - Pad1 (uint32): Not used.
//   - PartitionNumber (uint32): Index number of the partition.
//   - SectorsPerCluster (uint32): Number of sectors in the volume.
//   - BytesPerSector (uint32): Number of bytes in each sector.
//   - Pad2 (uint32): Not used.
//   - NumberOfFreeClusters (sint64): Number of free clusters in the volume.
//   - TotalNumberOfClusters (sint64): Total number of clusters in the volume.
//   - FileSystem (string): File system on the logical disk (e.g., "NTFS"). [Max Length: 16]
//   - VolumeExt (uint32): Reserved.
//   - Pad3 (uint32): Not used.
//
// These events provide a one-time snapshot of the logical disk configuration
// when an NT Kernel Logger session is stopped.
func (h *SystemConfigHandler) HandleSystemConfigLogDisk(helper *etw.EventRecordHelper) error {
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
