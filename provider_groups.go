package main

import "github.com/tekert/golang-etw/etw"

// ProviderGroup defines a group of related ETW providers for a specific metric category
type ProviderGroup struct {
	Name              string
	Enabled           bool
	KernelFlags       uint32
	ManifestProviders []etw.Provider
}

// Pre-defined GUIDs for performance (no string comparisons)
var (
	// Kernel provider GUIDs - These are the actual providers we use
	DiskIoKernelGUID  = etw.MustParseGUID("{3d6fa8d4-fe05-11d0-9dda-00c04fd7ba7c}") // DiskIo
	ProcessKernelGUID = etw.MustParseGUID("{3d6fa8d0-fe05-11d0-9dda-00c04fd7ba7c}") // Process

	// Manifest provider GUIDs - UNUSED: Kept for reference only
	// We use NT Kernel Logger instead of manifest providers to avoid duplicated events
	// and to ensure we get both disk events AND SystemConfig events in a single session.
	// On Windows build 19045, manifest providers don't provide SystemConfig events needed
	// for disk number to drive letter correlation, making NT Kernel Logger the better choice.
	// MicrosoftWindowsKernelDiskGUID = etw.MustParseGUID("{c7bde69a-e1e0-4177-b6ef-283ad1525271}") // Microsoft-Windows-Kernel-Disk
)

// Global provider groups configuration
var (
	// DiskIOGroup provides comprehensive disk I/O monitoring using NT Kernel Logger only
	// We use ONLY the NT Kernel Logger to avoid duplicated events and ensure complete coverage.
	// Rationale for NT Kernel Logger over manifest providers:
	//   1. Single session gets both disk I/O events AND SystemConfig events
	//   2. SystemConfig events are essential for correlating disk numbers to drive letters/models
	//   3. Manifest providers (Microsoft-Windows-Kernel-Disk) don't provide SystemConfig events
	//   4. Using both would result in duplicated disk I/O events
	//   5. NT Kernel Logger works reliably on Windows build 19045 and earlier
	DiskIOGroup = ProviderGroup{
		Name:    "disk_io",
		Enabled: true,
		// Kernel flags for disk I/O events, process rundown for process names, and SystemConfig
		KernelFlags: etw.EVENT_TRACE_FLAG_DISK_IO_INIT | etw.EVENT_TRACE_FLAG_DISK_IO | etw.EVENT_TRACE_FLAG_PROCESS,
		// No manifest providers - we rely entirely on NT Kernel Logger
		ManifestProviders: []etw.Provider{},
	}

	// All available provider groups
	AllProviderGroups = []*ProviderGroup{
		&DiskIOGroup,
	}
)

// GetEnabledKernelFlags returns combined kernel flags for all enabled groups
func GetEnabledKernelFlags() uint32 {
	var flags uint32
	for _, group := range AllProviderGroups {
		if group.Enabled {
			flags |= group.KernelFlags
		}
	}
	return flags
}

// GetEnabledManifestProviders returns all manifest providers for enabled groups
func GetEnabledManifestProviders() []etw.Provider {
	var providers []etw.Provider
	for _, group := range AllProviderGroups {
		if group.Enabled {
			providers = append(providers, group.ManifestProviders...)
		}
	}
	return providers
}
