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
// https://learn.microsoft.com/en-us/windows/win32/etw/nt-kernel-logger-constants
var (
	// SystemConfig GUID for hardware configuration events
	SystemConfigGUID = etw.MustParseGUID("{01853a65-418f-4f36-aefc-dc0f1d2fd235}") // SystemConfig

	// Manifest provider GUIDs - Modern providers with better event parsing
	MicrosoftWindowsKernelDiskGUID    = etw.MustParseGUID("{c7bde69a-e1e0-4177-b6ef-283ad1525271}") // Microsoft-Windows-Kernel-Disk
	MicrosoftWindowsKernelProcessGUID = etw.MustParseGUID("{22fb2cd6-0e7b-422b-a0c7-2fad1fd0e716}") // Microsoft-Windows-Kernel-Process
	MicrosoftWindowsKernelFileGUID    = etw.MustParseGUID("{edd08927-9cc4-4e65-b970-c2560fb5c289}") // Microsoft-Windows-Kernel-File

	// Kernel provider GUIDs for context switches (these require kernel session)
	ThreadKernelGUID = etw.MustParseGUID("{3d6fa8d1-fe05-11d0-9dda-00c04fd7ba7c}") // Thread
)

// Global provider groups configuration
var (
	// DiskIOGroup uses manifest providers for disk I/O events
	// These are modern providers with better event parsing
	DiskIOGroup = ProviderGroup{
		Name:    "disk_io",
		Enabled: false, // Will be enabled based on config
		// No kernel flags - using manifest providers only
		KernelFlags: 0,
		// Manifest providers for disk I/O
		ManifestProviders: []etw.Provider{
			{
				Name: "Microsoft-Windows-Kernel-Disk",
				GUID: *MicrosoftWindowsKernelDiskGUID,
				// Enable all disk I/O events: Read (10), Write (11), Flush (14)
				EnableLevel:     0xFF, // All levels
				MatchAnyKeyword: 0x0,  // All keywords
				MatchAllKeyword: 0x0,
			},
			{
				Name: "Microsoft-Windows-Kernel-Process",
				GUID: *MicrosoftWindowsKernelProcessGUID,
				// Enable process events: Start (1), Stop (2), Rundown (15)
				EnableLevel:     0xFF, // All levels
				MatchAnyKeyword: 0x10, // WINEVENT_KEYWORD_PROCESS
				MatchAllKeyword: 0x0,
			},
		},
	}

	// ContextSwitchGroup uses kernel session for low-level thread events
	// Context switches require kernel session for optimal performance
	ContextSwitchGroup = ProviderGroup{
		Name:    "context_switch",
		Enabled: false, // Will be enabled based on config
		// Kernel flags for context switches and thread events
		KernelFlags: etw.EVENT_TRACE_FLAG_CSWITCH |
			etw.EVENT_TRACE_FLAG_THREAD |
			etw.EVENT_TRACE_FLAG_DISPATCHER,
		// No manifest providers - using kernel session only
		ManifestProviders: []etw.Provider{},
	}

	// FileIOGroup uses manifest providers for detailed file I/O tracking
	// Used when TrackFileMapping is enabled (heavy metrics)
	FileIOGroup = ProviderGroup{
		Name:    "file_io",
		Enabled: false, // Will be enabled based on TrackFileMapping config
		// No kernel flags - using manifest providers only
		KernelFlags: 0,
		// Manifest providers for file I/O
		ManifestProviders: []etw.Provider{
			{
				Name: "Microsoft-Windows-Kernel-File",
				GUID: *MicrosoftWindowsKernelFileGUID,
				// Enable file I/O events: Read (15), Write (16), Create (12), etc.
				EnableLevel:     0xFF,  // All levels
				MatchAnyKeyword: 0x120, // KERNEL_FILE_KEYWORD_FILEIO | KERNEL_FILE_KEYWORD_READ | KERNEL_FILE_KEYWORD_WRITE
				MatchAllKeyword: 0x0,
			},
		},
	}

	// All available provider groups
	AllProviderGroups = []*ProviderGroup{
		&DiskIOGroup,
		&ContextSwitchGroup,
		&FileIOGroup,
	}
)

// GetEnabledKernelFlags returns combined kernel flags for all enabled groups
func GetEnabledKernelFlags(config *AppCollectorConfig) uint32 {
	var flags uint32

	// Enable ContextSwitch group based on config (requires kernel session)
	if config.ContextSwitch.Enabled {
		flags |= ContextSwitchGroup.KernelFlags
	}

	return flags
}

// GetEnabledManifestProviders returns all manifest providers for enabled groups
func GetEnabledManifestProviders(config *AppCollectorConfig) []etw.Provider {
	var providers []etw.Provider

	// Enable DiskIO group based on config (manifest providers)
	if config.DiskIO.Enabled {
		providers = append(providers, DiskIOGroup.ManifestProviders...)
	}

	// Enable FileIO group based on TrackFileMapping config (heavy metrics)
	if config.DiskIO.Enabled && config.DiskIO.TrackFileMapping {
		providers = append(providers, FileIOGroup.ManifestProviders...)
	}

	return providers
}
