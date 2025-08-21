package main

import (
	"github.com/tekert/goetw/etw"
)

// ProviderGroup defines a group of related ETW providers for a specific metric category
type ProviderGroup struct {
	Name              string // descriptive name
	KernelFlags       uint32 // if set, this provider will be used in a kernel session
	ManifestProviders []etw.Provider
	// Function to check if this provider group is enabled based on config
	IsEnabled func(config *CollectorConfig) bool
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

	// Kernel session MOF class GUIDs for interrupt latency tracking
	// Based on MOF documentation:

	// PerfInfo MOF class - handles ISR, DPC, and system call events
	// [Guid("{ce1dbfb4-137e-4da6-87b0-3f59aa102cbc}"), EventVersion(2)]
	PerfInfoKernelGUID = etw.MustParseGUID("{ce1dbfb4-137e-4da6-87b0-3f59aa102cbc}")

	// Image MOF class - handles image load/unload events
	// [Guid("{2cb15d1d-5fc1-11d2-abe1-00a0c911f518}"), EventVersion(2)]
	ImageKernelGUID = etw.MustParseGUID("{2cb15d1d-5fc1-11d2-abe1-00a0c911f518}")

	// PageFault_V2 MOF class - handles page fault events
	// [Guid("{3d6fa8d3-fe05-11d0-9dda-00c04fd7ba7c}"), EventVersion(2)]
	PageFaultKernelGUID = etw.MustParseGUID("{3d6fa8d3-fe05-11d0-9dda-00c04fd7ba7c}")
)

// AllProviderGroups contains all available provider groups in a simple slice
var AllProviderGroups = []*ProviderGroup{
	// DiskIOGroup uses manifest providers for disk I/O events and file I/O events
	{
		Name:        "disk_io",
		KernelFlags: 0, // No kernel flags - using manifest providers only
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
			{
				Name: "Microsoft-Windows-Kernel-File",
				GUID: *MicrosoftWindowsKernelFileGUID,
				// Enable file I/O events for process correlation
				EnableLevel:     0xFF,  // All levels
				MatchAnyKeyword: 0x120, // KERNEL_FILE_KEYWORD_FILEIO | KERNEL_FILE_KEYWORD_READ | KERNEL_FILE_KEYWORD_WRITE
				MatchAllKeyword: 0x0,
			},
		},
		IsEnabled: func(config *CollectorConfig) bool {
			return config.DiskIO.Enabled
		},
	},

	// ThreadCSGroup uses kernel session for low-level thread context switch events
	{
		Name: "threadcs",
		KernelFlags: etw.EVENT_TRACE_FLAG_CSWITCH |
			etw.EVENT_TRACE_FLAG_THREAD |
			etw.EVENT_TRACE_FLAG_DISPATCHER,
		ManifestProviders: []etw.Provider{}, // No manifest providers - using kernel session only
		IsEnabled: func(config *CollectorConfig) bool {
			return config.ThreadCS.Enabled
		},
	},

	// InterruptLatencyGroup uses kernel session for interrupt and DPC events
	{
		Name: "interrupt_latency",
		KernelFlags: etw.EVENT_TRACE_FLAG_INTERRUPT |
			etw.EVENT_TRACE_FLAG_DPC |
			etw.EVENT_TRACE_FLAG_IMAGE_LOAD |
			etw.EVENT_TRACE_FLAG_MEMORY_PAGE_FAULTS,
		ManifestProviders: []etw.Provider{}, // No manifest providers - using kernel session only
		IsEnabled: func(config *CollectorConfig) bool {
			return config.PerfInfo.Enabled
		},
	},
}

// GetEnabledProviders returns all enabled provider groups
func GetEnabledProviders(config *CollectorConfig) []*ProviderGroup {
	var enabled []*ProviderGroup

	for _, group := range AllProviderGroups {
		if group.IsEnabled(config) {
			enabled = append(enabled, group)
		}
	}

	return enabled
}

// GetEnabledKernelFlags returns combined kernel flags for all enabled groups
func GetEnabledKernelFlags(config *CollectorConfig) uint32 {
	var flags uint32

	for _, group := range AllProviderGroups {
		if group.IsEnabled(config) {
			flags |= group.KernelFlags
		}
	}

	return flags
}

// GetEnabledManifestProviders returns all manifest providers for enabled groups
// Combines providers with the same GUID by merging their keywords
func GetEnabledManifestProviders(config *CollectorConfig) []etw.Provider {
	// Map to combine providers with same GUID
	providerMap := make(map[string]*etw.Provider)

	for _, group := range AllProviderGroups {
		if group.IsEnabled(config) {
			for _, provider := range group.ManifestProviders {
				key := provider.GUID.String()
				if existing, exists := providerMap[key]; exists {
					// Combine keywords for same provider
					existing.MatchAnyKeyword |= provider.MatchAnyKeyword
					existing.MatchAllKeyword |= provider.MatchAllKeyword
					// Use the highest enable level
					if provider.EnableLevel > existing.EnableLevel {
						existing.EnableLevel = provider.EnableLevel
					}
				} else {
					// Create a copy of the provider
					newProvider := provider
					providerMap[key] = &newProvider
				}
			}
		}
	}

	// Convert map back to slice
	var providers []etw.Provider
	for _, provider := range providerMap {
		providers = append(providers, *provider)
	}

	return providers
}
