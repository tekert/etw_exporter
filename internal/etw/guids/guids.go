package guids

import "github.com/tekert/goetw/etw"

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

	// DiskIO MOF class - provides IssuingThreadId
	// [Guid("{3d6fa8d4-fe05-11d0-9dda-00c04fd7ba7c}")]
	DiskIOKernelGUID = etw.MustParseGUID("{3d6fa8d4-fe05-11d0-9dda-00c04fd7ba7c}")

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
