package etwmain

import (
	"etw_exporter/internal/config"
	"fmt"

	"github.com/phuslu/log"
	"github.com/tekert/goetw/etw"
)

// ProviderGroup defines a group of related ETW providers for a specific metric category
type ProviderGroup struct {
	Name string // descriptive name
	// For Windows 10 and older (NT Kernel Logger)
	KernelFlags uint32
	// For modern manifest-based providers (works on all OS versions)
	ManifestProviders []etw.Provider
	// For Windows 11+ System Providers (replaces KernelFlags)
	SystemProviders []etw.Provider
	// Function to check if this provider group is enabled based on config
	IsEnabled func(config *config.CollectorConfig) bool
	InitFunc  func(pg *ProviderGroup) error // Optional function to initialize any required privileges
}

// Pre-defined GUIDs for performance (no string comparisons)
// https://learn.microsoft.com/en-us/windows/win32/etw/nt-kernel-logger-constants
var (
	// # System Provider GUIDs (Win11+)
	// https://learn.microsoft.com/en-us/windows/win32/etw/system-providers
	// Guid defined in goetw using etw.System*ProviderGuid
	SystemProcessProviderGuid   = etw.MustParseGUID("{151f55dc-467d-471f-83b5-5f889d46ff66}")
	SystemSchedulerProviderGuid = etw.MustParseGUID("{599a2a76-4d91-4910-9ac7-7d33f2e97a6c}")
	SystemInterruptProviderGuid = etw.MustParseGUID("{d4bbee17-b545-4888-858b-744169015b25}")
	SystemMemoryProviderGuid    = etw.MustParseGUID("{82958ca9-b6cd-47f8-a3a8-03ae85a4bc24}")
	SystemIoProviderGuid        = etw.MustParseGUID("{3d5c43e3-0f1c-4202-b817-174c0070dc79}")
	SystemRegistryProviderGuid  = etw.MustParseGUID("{16156bd9-fab4-4cfa-a232-89d1099058e3}")

	// # Manifest provider GUIDs - Modern providers with better event parsing

	MicrosoftWindowsKernelEventTracingGUID = etw.MustParseGUID("{b675ec37-bdb6-4648-bc92-f3fdc74d3ca2}") // Microsoft-Windows-Kernel-EventTracing
	MicrosoftWindowsKernelDiskGUID         = etw.MustParseGUID("{c7bde69a-e1e0-4177-b6ef-283ad1525271}") // Microsoft-Windows-Kernel-Disk
	MicrosoftWindowsKernelProcessGUID      = etw.MustParseGUID("{22fb2cd6-0e7b-422b-a0c7-2fad1fd0e716}") // Microsoft-Windows-Kernel-Process
	MicrosoftWindowsKernelFileGUID         = etw.MustParseGUID("{edd08927-9cc4-4e65-b970-c2560fb5c289}") // Microsoft-Windows-Kernel-File
	MicrosoftWindowsKernelNetworkGUID      = etw.MustParseGUID("{7dd42a49-5329-4832-8dfd-43d979153a88}") // Microsoft-Windows-Kernel-Network

	// # MOF Providers (Win10 and below) (NT  Kernel Logger) - require kernel session

	// SystemConfig GUID for hardware configuration events (MOF)
	SystemConfigGUID = etw.MustParseGUID("{01853a65-418f-4f36-aefc-dc0f1d2fd235}") // SystemConfig

	// Kernel provider GUIDs for context switches (these require kernel session)
	// [Guid("{3d6fa8d1-fe05-11d0-9dda-00c04fd7ba7c}"), EventVersion(2)]
	ThreadKernelGUID = etw.MustParseGUID("{3d6fa8d1-fe05-11d0-9dda-00c04fd7ba7c}") // Thread V2

	// PerfInfo MOF class - handles ISR, DPC, and system call events
	// [Guid("{ce1dbfb4-137e-4da6-87b0-3f59aa102cbc}"), EventVersion(2)]
	PerfInfoKernelGUID = etw.MustParseGUID("{ce1dbfb4-137e-4da6-87b0-3f59aa102cbc}")

	// Image MOF class - handles image load/unload events
	// [Guid("{2cb15d1d-5fc1-11d2-abe1-00a0c911f518}"), EventVersion(2)]
	ImageKernelGUID = etw.MustParseGUID("{2cb15d1d-5fc1-11d2-abe1-00a0c911f518}")

	// PageFault_V2 MOF class - handles page fault events
	// [Guid("{3d6fa8d3-fe05-11d0-9dda-00c04fd7ba7c}"), EventVersion(2)]
	PageFaultKernelGUID = etw.MustParseGUID("{3d6fa8d3-fe05-11d0-9dda-00c04fd7ba7c}")

	// Registry MOF class - handles registry events
	// [Guid("{ae53722e-c863-11d2-8659-00c04fa321a1}"), EventVersion(1)]
	RegistryKernelGUID = etw.MustParseGUID("{ae53722e-c863-11d2-8659-00c04fa321a1}")
)

// newProcessCorrelationProvider is a helper to create a standard provider
// for process name correlation, reducing code duplication.
func newProcessCorrelationProvider() etw.Provider {
	return etw.Provider{
		Name:             "Microsoft-Windows-Kernel-Process",
		GUID:             *MicrosoftWindowsKernelProcessGUID,
		EnableLevel:      0xFF,
		MatchAnyKeyword:  0x10, // WINEVENT_KEYWORD_PROCESS
		MatchAllKeyword:  0x0,
		EnableProperties: etw.EVENT_ENABLE_PROPERTY_PROCESS_START_KEY,
	}
}

// AllProviderGroups contains all available provider groups in a simple slice
var AllProviderGroups = []*ProviderGroup{
	// DiskIOGroup uses manifest providers for disk I/O events and file I/O events
	{
		Name:        "disk_io",
		KernelFlags: 0, // No kernel flags - using manifest providers only
		ManifestProviders: []etw.Provider{
			newProcessCorrelationProvider(), // For disk I/O process name correlation
			{
				Name: "Microsoft-Windows-Kernel-Disk",
				GUID: *MicrosoftWindowsKernelDiskGUID,
				// Enable all disk I/O events: Read (10), Write (11), Flush (14)
				EnableLevel:      0xFF, // All levels
				MatchAnyKeyword:  0x0,  // All keywords
				MatchAllKeyword:  0x0,
				EnableProperties: etw.EVENT_ENABLE_PROPERTY_PROCESS_START_KEY,
			},
			{
				Name: "Microsoft-Windows-Kernel-File",
				GUID: *MicrosoftWindowsKernelFileGUID,
				// Enable file I/O events for process correlation
				EnableLevel:      0xFF,  // All levels
				MatchAnyKeyword:  0x7A0, // KERNEL_FILE_KEYWORD_FILEIO (0x20) | KERNEL_FILE_KEYWORD_CREATE (0x80) | KERNEL_FILE_KEYWORD_READ (0x100) | KERNEL_FILE_KEYWORD_WRITE (0x200) | KERNEL_FILE_KEYWORD_DELETE_PATH (0x400)
				MatchAllKeyword:  0x0,
				EnableProperties: etw.EVENT_ENABLE_PROPERTY_PROCESS_START_KEY,
				Filters: []etw.ProviderFilter{ // NOTE: comment this when doing profiling for pgo, causes lots of events.
					etw.NewEventIDFilter(true, 12, 14, 15, 16, 26),
				},
			},
		},
		IsEnabled: func(config *config.CollectorConfig) bool {
			return config.DiskIO.Enabled
		},
	},

	// ThreadCSGroup uses kernel session for low-level thread context switch events
	{
		Name: "threadcs",
		// --- Legacy (Win10) ---
		KernelFlags: etw.EVENT_TRACE_FLAG_CSWITCH |
			etw.EVENT_TRACE_FLAG_THREAD |
			etw.EVENT_TRACE_FLAG_DISPATCHER,
		// --- Modern (Win11+) ---
		SystemProviders: []etw.Provider{
			{
				Name:            "SystemSchedulerProvider",
				GUID:            *etw.SystemSchedulerProviderGuid,
				MatchAnyKeyword: etw.SYSTEM_SCHEDULER_KW_CONTEXT_SWITCH | etw.SYSTEM_SCHEDULER_KW_DISPATCHER,
			},
			{
				Name:            "SystemProcessProvider",
				GUID:            *etw.SystemProcessProviderGuid,
				MatchAnyKeyword: etw.SYSTEM_PROCESS_KW_THREAD,
			},
		},
		// --- Common ---
		ManifestProviders: []etw.Provider{
			newProcessCorrelationProvider(), // For thread process name correlation
		},
		IsEnabled: func(config *config.CollectorConfig) bool {
			return config.ThreadCS.Enabled
		},
	},

	// PerfInfoGroup uses kernel session for interrupt and DPC events
	{
		Name: "perfinfo",
		// --- Legacy (Win10) ---
		KernelFlags: etw.EVENT_TRACE_FLAG_INTERRUPT | // PerfInfo
			etw.EVENT_TRACE_FLAG_DPC | // PerfInfo
			etw.EVENT_TRACE_FLAG_IMAGE_LOAD | // Image
			etw.EVENT_TRACE_FLAG_CSWITCH, // Thread V2 (Used for DPC duration calculation)
		// --- Modern (Win11+) ---
		SystemProviders: []etw.Provider{
			{
				Name:            "SystemInterruptProvider",
				GUID:            *etw.SystemInterruptProviderGuid,
				MatchAnyKeyword: etw.SYSTEM_INTERRUPT_KW_GENERAL | etw.SYSTEM_INTERRUPT_KW_DPC,
			},
			{
				Name:            "SystemSchedulerProvider", // For DPC duration calculation
				GUID:            *etw.SystemSchedulerProviderGuid,
				MatchAnyKeyword: etw.SYSTEM_SCHEDULER_KW_CONTEXT_SWITCH,
			},
			{
				Name:            "SystemProcessProvider", // For Image Load events
				GUID:            *etw.SystemProcessProviderGuid,
				MatchAnyKeyword: etw.SYSTEM_PROCESS_KW_LOADER,
			},
		},
		//etw.EVENT_TRACE_FLAG_PROFILE, // For Testing
		ManifestProviders: []etw.Provider{}, // No manifest providers
		IsEnabled: func(config *config.CollectorConfig) bool {
			return config.PerfInfo.Enabled
		},
	},

	// NetworkGroup uses manifest providers for network I/O events
	{
		Name:        "network",
		KernelFlags: 0, // No kernel flags - using manifest providers only
		ManifestProviders: []etw.Provider{
			{
				Name: "Microsoft-Windows-Kernel-Network",
				GUID: *MicrosoftWindowsKernelNetworkGUID,
				// Enable network events: TCP/UDP data sent/received, connections
				EnableLevel:      0xFF, // All levels
				MatchAnyKeyword:  0x30, // KERNEL_NETWORK_KEYWORD_IPV4 | KERNEL_NETWORK_KEYWORD_IPV6
				MatchAllKeyword:  0x0,
				EnableProperties: etw.EVENT_ENABLE_PROPERTY_PROCESS_START_KEY,
			},
			newProcessCorrelationProvider(),
		},
		IsEnabled: func(config *config.CollectorConfig) bool {
			return config.Network.Enabled
		},
	},

	// MemoryGroup uses kernel session for memory-related events like hard page faults.
	{
		Name: "memory",
		// --- Legacy (Win10) ---
		KernelFlags: etw.EVENT_TRACE_FLAG_MEMORY_HARD_FAULTS | // For HardFault events
			etw.EVENT_TRACE_FLAG_THREAD, // For TID->PID mapping
		// --- Modern (Win11+) ---
		SystemProviders: []etw.Provider{
			{
				Name:            "SystemMemoryProvider",
				GUID:            *etw.SystemMemoryProviderGuid,
				MatchAnyKeyword: etw.SYSTEM_MEMORY_KW_HARD_FAULTS,
			},
			{
				Name:            "SystemProcessProvider", // For TID->PID mapping
				GUID:            *etw.SystemProcessProviderGuid,
				MatchAnyKeyword: etw.SYSTEM_PROCESS_KW_THREAD,
			},
		},
		ManifestProviders: []etw.Provider{
			newProcessCorrelationProvider(), // For process name correlation
		},
		IsEnabled: func(config *config.CollectorConfig) bool {
			return config.Memory.Enabled
		},
	},

	// RegistryGroup uses kernel session for registry access events.
	{
		Name: "registry",
		// --- Legacy (Win10) ---
		KernelFlags: etw.EVENT_TRACE_FLAG_REGISTRY,
		// --- Modern (Win11+) ---
		SystemProviders: []etw.Provider{
			{
				Name:            "SystemRegistryProvider",
				GUID:            *SystemRegistryProviderGuid,
				MatchAnyKeyword: etw.SYSTEM_REGISTRY_KW_GENERAL,
			},
		},
		ManifestProviders: []etw.Provider{
			newProcessCorrelationProvider(), // For process name correlation
		},
		IsEnabled: func(config *config.CollectorConfig) bool {
			return config.Registry.Enabled
		},
	},
}

// init initializes the provider group, calling any InitFunc if defined
func (pg *ProviderGroup) init() error {
	if pg.InitFunc != nil {
		return pg.InitFunc(pg)
	}
	return nil
}

// InitProviders initializes all enabled provider groups based on the config
// and ensures necessary privileges are set
func InitProviders(config *config.CollectorConfig) error {
	// Initialize all enabled provider groups
	var once bool = false
	for _, group := range GetEnabledProviders(config) {
		// Ensure we have the necessary privileges for kernel sessions if PROFILE flag is set
		if group.KernelFlags&etw.EVENT_TRACE_FLAG_PROFILE != 0 && !once {
			etw.EnableProfilingPrivileges()
			log.Warn().Msg("SE_SYSTEM_PROFILE_NAME Privilege enabled for profiling")
			once = true
		}
		if err := group.init(); err != nil {
			log.Error().Err(err).Str("group", group.Name).Msg("ProviderGroup Init failed")
			return fmt.Errorf("failed to initialize provider group %s: %w", group.Name, err)
		}
	}
	return nil
}

// GetEnabledProviders returns all enabled provider groups
func GetEnabledProviders(config *config.CollectorConfig) []*ProviderGroup {
	var enabled []*ProviderGroup

	for _, group := range AllProviderGroups {
		if group.IsEnabled(config) {
			enabled = append(enabled, group)
		}
	}

	return enabled
}

// GetEnabledKernelFlags returns combined kernel flags for all enabled groups
func GetEnabledKernelFlags(config *config.CollectorConfig) etw.KernelNtFlag {
	var flags uint32

	for _, group := range AllProviderGroups {
		if group.IsEnabled(config) {
			flags |= group.KernelFlags
		}
	}

	return etw.KernelNtFlag(flags)
}

// GetEnabledManifestProviders returns all manifest providers for enabled groups
// Combines providers with the same GUID by merging their keywords
func GetEnabledManifestProviders(config *config.CollectorConfig) []etw.Provider {
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
					existing.EnableProperties |= provider.EnableProperties
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

// GetEnabledSystemProviders returns all system providers for enabled groups
// Combines providers with the same GUID by merging their keywords
func GetEnabledSystemProviders(config *config.CollectorConfig) []etw.Provider {
	// Map to combine providers with same GUID
	providerMap := make(map[string]*etw.Provider)

	for _, group := range AllProviderGroups {
		if group.IsEnabled(config) {
			for _, provider := range group.SystemProviders {
				key := provider.GUID.String()
				if existing, exists := providerMap[key]; exists {
					// Combine keywords for same provider
					existing.MatchAnyKeyword |= provider.MatchAnyKeyword
					existing.MatchAllKeyword |= provider.MatchAllKeyword
					existing.EnableProperties |= provider.EnableProperties
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
