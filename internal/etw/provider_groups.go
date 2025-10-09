package etwmain

import (
	"etw_exporter/internal/config"

	"github.com/tekert/goetw/etw"

	guids "etw_exporter/internal/etw/guids"
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

// newProcessCorrelationProvider is a helper to create a standard provider
// for process name correlation, reducing code duplication.
// TODO: on win10 this has no real use, no thread rundowns or image rundowns
//
//	process are the same as the NT Kernel Logger provider and supports rundown but no thread/image.
func newProcessCorrelationProvider() etw.Provider {
	return etw.Provider{
		// Name:             "Microsoft-Windows-Kernel-Process",
		// GUID:             *guids.MicrosoftWindowsKernelProcessGUID,
		// EnableLevel:      0xFF,
		// MatchAnyKeyword:  0x10 | 0x20 | 0x40, // WINEVENT_KEYWORD_PROCESS, WINEVENT_KEYWORD_THREAD, WINEVENT_KEYWORD_IMAGE
		// MatchAllKeyword:  0x0,
		// //EnableProperties: etw.EVENT_ENABLE_PROPERTY_PROCESS_START_KEY,
	}
}

// newProcessCorrelationProvider is a helper to create a standard provider
// for process name correlation, reducing code duplication.
func newKernelProcessCorrelationProvider() uint32 {
	return etw.EVENT_TRACE_FLAG_PROCESS | etw.EVENT_TRACE_FLAG_IMAGE_LOAD
}

// AllProviderGroups contains all available provider groups in a simple slice
var AllProviderGroups = []*ProviderGroup{
	// DiskIOGroup uses manifest providers for disk I/O events and file I/O events
	{
		Name: "disk_io",
		// --- Legacy (Win10) ---
		// Enable MOF-based DiskIO and Thread providers.
		// EVENT_TRACE_FLAG_DISK_IO is for the legacy DiskIo provider which contains IssuingThreadId.
		// EVENT_TRACE_FLAG_THREAD is required to map the IssuingThreadId back to a PID.
		KernelFlags: etw.EVENT_TRACE_FLAG_DISK_IO | etw.EVENT_TRACE_FLAG_THREAD |
			newKernelProcessCorrelationProvider(),
		// --- Modern (Win11+) ---
		SystemProviders: []etw.Provider{
			{
				Name:            "SystemProcessProvider", // For TID->PID mapping
				GUID:            *etw.SystemProcessProviderGuid,
				MatchAnyKeyword: etw.SYSTEM_PROCESS_KW_THREAD | etw.SYSTEM_PROCESS_KW_GENERAL,
			},
			{
				Name:            "SystemIoProvider",
				GUID:            *etw.SystemIoProviderGuid,
				MatchAnyKeyword: etw.SYSTEM_IO_KW_DISK,
			},
			{
				Name:            "SystemConfigProvider", // For disk info events
				GUID:            *etw.SystemConfigProviderGuid,
				MatchAnyKeyword: etw.SYSTEM_CONFIG_KW_STORAGE,
			},
		},
		// --- Common ---
		ManifestProviders: []etw.Provider{
			// Use System providers, Microsoft-Windows-Kernel-Disk only reports user space and
			// doesnt match the clock in the NT Kernel logger session.
			// (and manifest process doesnt support thread rundowns, for simplicity we use the NT Kernel logger in win10)
			// newProcessCorrelationProvider(), // For disk I/O process name correlation
			// {
			// 	Name: "Microsoft-Windows-Kernel-Disk",
			// 	GUID: *guids.MicrosoftWindowsKernelDiskGUID,
			// 	// Enable all disk I/O events: Read (10), Write (11), Flush (14)
			// 	EnableLevel:     0xFF, // All levels
			// 	MatchAnyKeyword: 0x0,  // All keywords
			// 	MatchAllKeyword: 0x0,
			// 	//EnableProperties: etw.EVENT_ENABLE_PROPERTY_PROCESS_START_KEY,
			// },
			// {
			// 	Name: "Microsoft-Windows-Kernel-File",
			// 	GUID: *MicrosoftWindowsKernelFileGUID,
			// 	// Enable file I/O events for process correlation
			// 	EnableLevel:      0xFF,  // All levels
			// 	MatchAnyKeyword:  0x7A0, // KERNEL_FILE_KEYWORD_FILEIO (0x20) | KERNEL_FILE_KEYWORD_CREATE (0x80) | KERNEL_FILE_KEYWORD_READ (0x100) | KERNEL_FILE_KEYWORD_WRITE (0x200) | KERNEL_FILE_KEYWORD_DELETE_PATH (0x400)
			// 	MatchAllKeyword:  0x0,
			// 	EnableProperties: etw.EVENT_ENABLE_PROPERTY_PROCESS_START_KEY,
			// 	Filters: []etw.ProviderFilter{ // NOTE: comment this when doing profiling for pgo, causes lots of events.
			// 		etw.NewEventIDFilter(true, 12, 14, 15, 16, 26),
			// 	},
			// },
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
			etw.EVENT_TRACE_FLAG_DISPATCHER |
			newKernelProcessCorrelationProvider(),
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
				MatchAnyKeyword: etw.SYSTEM_PROCESS_KW_THREAD | etw.SYSTEM_PROCESS_KW_GENERAL,
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
		KernelFlags: etw.EVENT_TRACE_FLAG_NETWORK_TCPIP | newKernelProcessCorrelationProvider(),
		SystemProviders: []etw.Provider{
			{
				Name:            "SystemProcessProvider",
				GUID:            *etw.SystemProcessProviderGuid,
				MatchAnyKeyword: etw.SYSTEM_PROCESS_KW_GENERAL,
			},
			{
				Name:            "SystemIoProvider",
				GUID:            *etw.SystemIoProviderGuid,
				MatchAnyKeyword: etw.SYSTEM_IO_KW_NETWORK,
			},
		},
		// --- Common ---
		ManifestProviders: []etw.Provider{
			// {
			// 	Name: "Microsoft-Windows-Kernel-Network",
			// 	GUID: *guids.MicrosoftWindowsKernelNetworkGUID,
			// 	// Enable network events: TCP/UDP data sent/received, connections
			// 	EnableLevel:      0xFF, // All levels
			// 	MatchAnyKeyword:  0x30, // KERNEL_NETWORK_KEYWORD_IPV4 | KERNEL_NETWORK_KEYWORD_IPV6
			// 	MatchAllKeyword:  0x0,
			// 	EnableProperties: etw.EVENT_ENABLE_PROPERTY_PROCESS_START_KEY,
			// },
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
			etw.EVENT_TRACE_FLAG_THREAD | // For TID->PID mapping
			newKernelProcessCorrelationProvider(),
		// --- Modern (Win11+) ---
		SystemProviders: []etw.Provider{
			{
				Name:            "SystemMemoryProvider",
				GUID:            *etw.SystemMemoryProviderGuid,
				MatchAnyKeyword: etw.SYSTEM_MEMORY_KW_HARD_FAULTS,
			},
			{
				Name:            "SystemProcessProvider",
				GUID:            *etw.SystemProcessProviderGuid,
				MatchAnyKeyword: etw.SYSTEM_PROCESS_KW_THREAD | etw.SYSTEM_PROCESS_KW_GENERAL,
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
		KernelFlags: etw.EVENT_TRACE_FLAG_REGISTRY | newKernelProcessCorrelationProvider(),
		// --- Modern (Win11+) ---
		SystemProviders: []etw.Provider{
			{
				Name:            "SystemRegistryProvider",
				GUID:            *guids.SystemRegistryProviderGuid,
				MatchAnyKeyword: etw.SYSTEM_REGISTRY_KW_GENERAL,
			},
			{
				Name:            "SystemProcessProvider",
				GUID:            *etw.SystemProcessProviderGuid,
				MatchAnyKeyword: etw.SYSTEM_PROCESS_KW_GENERAL,
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

// IsProcessManagerNeeded inspects the configuration and enabled provider
// groups to decide if the full ProcessManager needs to be enabled.
func IsProcessManagerNeeded(config *config.CollectorConfig) bool {
	for _, group := range GetEnabledProviders(config) {
		if (group.KernelFlags & etw.EVENT_TRACE_FLAG_PROCESS) != 0 {
			return true
		}
		for _, p := range group.ManifestProviders {
			if p.GUID == *guids.MicrosoftWindowsKernelProcessGUID {
				return true
			}
		}
		for _, p := range group.SystemProviders {
			if p.GUID == *etw.SystemProcessProviderGuid &&
				(p.MatchAnyKeyword&etw.SYSTEM_PROCESS_KW_GENERAL != 0) {
				return true
			}
		}
	}
	return false
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
