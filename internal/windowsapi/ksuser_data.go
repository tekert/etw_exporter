package windowsapi

import "unsafe"

const (
    kuserSharedDataAddr = 0x7FFE0000
	bootIdOffset = 0x2C4
)

var (
    // _KUSER_SHARED_DATA.BootId is located at a fixed address
	// 0x7FFE0000 + (offset 708 bytes) 0x02C4 = 0x7ffe02C4
	// and is mapped into the address space of every user-mode process.
	// We can read it directly and safely via a pointer dereference.
	// https://learn.microsoft.com/en-us/windows-hardware/drivers/ddi/ntddk/ns-ntddk-kuser_shared_data
	bootId = uint64(*(*uint32)(unsafe.Pointer(uintptr(kuserSharedDataAddr + bootIdOffset))))

	KuserSharedData = (*KUSER_SHARED_DATA)(unsafe.Pointer(uintptr(kuserSharedDataAddr)))
)


const PROCESSOR_FEATURE_MAX = 64

// KSYSTEM_TIME represents a system time structure (12 bytes total).
type KSYSTEM_TIME struct {
	LowPart   uint32 // Offset: 0x000
	High1Time int32  // Offset: 0x004
	High2Time int32  // Offset: 0x008
}

// LARGE_INTEGER represents a 64-bit signed integer.
type LARGE_INTEGER struct {
	QuadPart int64 // Offset: 0x000
}

// XSTATE_FEATURE represents an xstate feature (8 bytes total).
type XSTATE_FEATURE struct {
	Offset uint32 // Offset: 0x000
	Size   uint32 // Offset: 0x004
}

// XSTATE_CONFIGURATION represents the xstate configuration structure.
type XSTATE_CONFIGURATION struct {
	EnabledFeatures uint64             // Offset: 0x000
	Size            uint32             // Offset: 0x008
	OptimizedSave   uint32             // Offset: 0x00C (bitfield stored as uint32)
	Features        [64]XSTATE_FEATURE // Offset: 0x010
}

// ALTERNATIVE_ARCHITECTURE_TYPE represents alternative architecture types.
type ALTERNATIVE_ARCHITECTURE_TYPE uint32

const (
	StandardDesign  ALTERNATIVE_ARCHITECTURE_TYPE = 0
	NEC98x86        ALTERNATIVE_ARCHITECTURE_TYPE = 1
	EndAlternatives ALTERNATIVE_ARCHITECTURE_TYPE = 2
)

// NT_PRODUCT_TYPE represents NT product types.
type NT_PRODUCT_TYPE uint32

const (
	NtProductWinNt    NT_PRODUCT_TYPE = 1
	NtProductLanManNt NT_PRODUCT_TYPE = 2
	NtProductServer   NT_PRODUCT_TYPE = 3
)

// KUSER_SHARED_DATA represents the kernel-user shared data structure.
// This structure is located at address 0x7FFE0000 in user mode.
type KUSER_SHARED_DATA struct {
	TickCountLowDeprecated            uint32                        // Offset: 0x000 - Current low 32-bit of tick count
	TickCountMultiplier               uint32                        // Offset: 0x004 - Tick count multiplier
	InterruptTime                     KSYSTEM_TIME                  // Offset: 0x008 - Current 64-bit interrupt time in 100ns units
	SystemTime                        KSYSTEM_TIME                  // Offset: 0x014 - Current 64-bit system time in 100ns units
	TimeZoneBias                      KSYSTEM_TIME                  // Offset: 0x020 - Current 64-bit time zone bias
	ImageNumberLow                    uint16                        // Offset: 0x02C - Low image magic number
	ImageNumberHigh                   uint16                        // Offset: 0x02E - High image magic number
	NtSystemRoot                      [260]uint16                   // Offset: 0x030 - System root path (520 bytes)
	MaxStackTraceDepth                uint32                        // Offset: 0x238 - Maximum stack trace depth
	CryptoExponent                    uint32                        // Offset: 0x23C - Crypto exponent value
	TimeZoneId                        uint32                        // Offset: 0x240 - Time zone ID
	LargePageMinimum                  uint32                        // Offset: 0x244 - Large page minimum
	AitSamplingValue                  uint32                        // Offset: 0x248 - AIT sampling rate control
	AppCompatFlag                     uint32                        // Offset: 0x24C - Switchback processing control
	RNGSeedVersion                    uint64                        // Offset: 0x250 - Kernel root RNG state seed version
	GlobalValidationRunlevel          uint32                        // Offset: 0x258 - Assertion failure handling control
	TimeZoneBiasStamp                 int32                         // Offset: 0x25C - Time zone bias stamp
	NtBuildNumber                     uint32                        // Offset: 0x260 - NT build number
	NtProductType                     NT_PRODUCT_TYPE               // Offset: 0x264 - Product type
	ProductTypeIsValid                uint8                         // Offset: 0x268 - Product type validity flag
	Reserved0                         [1]uint8                      // Offset: 0x269 - Reserved
	NativeProcessorArchitecture       uint16                        // Offset: 0x26A - Native processor architecture
	NtMajorVersion                    uint32                        // Offset: 0x26C - NT major version
	NtMinorVersion                    uint32                        // Offset: 0x270 - NT minor version
	ProcessorFeatures                 [PROCESSOR_FEATURE_MAX]uint8  // Offset: 0x274 - Processor features (PROCESSOR_FEATURE_MAX = 64)
	Reserved1                         uint32                        // Offset: 0x2B4 - Reserved
	Reserved3                         uint32                        // Offset: 0x2B8 - Reserved
	TimeSlip                          uint32                        // Offset: 0x2BC - Time slippage in debugger
	AlternativeArchitecture           ALTERNATIVE_ARCHITECTURE_TYPE // Offset: 0x2C0 - Alternative architecture type
	BootId                            uint32                        // Offset: 0x2C4 - Boot sequence counter
	SystemExpirationDate              LARGE_INTEGER                 // Offset: 0x2C8 - System expiration date
	SuiteMask                         uint32                        // Offset: 0x2D0 - Suite support mask
	KdDebuggerEnabled                 uint8                         // Offset: 0x2D4 - Kernel debugger enabled flag
	MitigationPolicies                uint8                         // Offset: 0x2D5 - Mitigation policies (union with bitfields)
	CyclesPerYield                    uint16                        // Offset: 0x2D6 - Cycles per yield
	ActiveConsoleId                   uint32                        // Offset: 0x2D8 - Active console session ID
	DismountCount                     uint32                        // Offset: 0x2DC - Dismount serial number
	ComPlusPackage                    uint32                        // Offset: 0x2E0 - COM+ package status
	LastSystemRITEventTickCount       uint32                        // Offset: 0x2E4 - Last system RIT event tick
	NumberOfPhysicalPages             uint32                        // Offset: 0x2E8 - Number of physical pages
	SafeBootMode                      uint8                         // Offset: 0x2EC - Safe boot mode flag
	VirtualizationFlags               uint8                         // Offset: 0x2ED - Virtualization flags
	Reserved12                        [2]uint8                      // Offset: 0x2EE - Reserved
	SharedDataFlags                   uint32                        // Offset: 0x2F0 - Shared data flags (union with bitfields)
	DataFlagsPad                      [1]uint32                     // Offset: 0x2F4 - Padding
	TestRetInstruction                uint64                        // Offset: 0x2F8 - Test return instruction
	QpcFrequency                      int64                         // Offset: 0x300 - QPC frequency
	SystemCall                        uint32                        // Offset: 0x308 - System call mechanism
	Reserved2                         uint32                        // Offset: 0x30C - Reserved
	FullNumberOfPhysicalPages         uint64                        // Offset: 0x310 - Full number of physical pages
	SystemCallPad                     [1]uint64                     // Offset: 0x318 - Padding
	TickCount                         KSYSTEM_TIME                  // Offset: 0x320 - 64-bit tick count (union with TickCountQuad)
	Cookie                            uint32                        // Offset: 0x32C - Pointer encoding cookie
	CookiePad                         [1]uint32                     // Offset: 0x330 - Padding
	_                                 [4]byte                       // Offset: 0x334 - Padding to align next field to 8 bytes
	ConsoleSessionForegroundProcessId int64                         // Offset: 0x338 - Console session foreground process ID
	TimeUpdateLock                    uint64                        // Offset: 0x340 - Time update lock
	BaselineSystemTimeQpc             uint64                        // Offset: 0x348 - Baseline system time QPC
	BaselineInterruptTimeQpc          uint64                        // Offset: 0x350 - Baseline interrupt time QPC
	QpcSystemTimeIncrement            uint64                        // Offset: 0x358 - QPC system time increment
	QpcInterruptTimeIncrement         uint64                        // Offset: 0x360 - QPC interrupt time increment
	QpcSystemTimeIncrementShift       uint8                         // Offset: 0x368 - QPC system time increment shift
	QpcInterruptTimeIncrementShift    uint8                         // Offset: 0x369 - QPC interrupt time increment shift
	UnparkedProcessorCount            uint16                        // Offset: 0x36A - Unparked processor count
	EnclaveFeatureMask                [4]uint32                     // Offset: 0x36C - Enclave feature mask
	TelemetryCoverageRound            uint32                        // Offset: 0x37C - Telemetry coverage round
	UserModeGlobalLogger              [16]uint16                    // Offset: 0x380 - User mode global logger (32 bytes)
	ImageFileExecutionOptions         uint32                        // Offset: 0x3A0 - Image file execution options
	LangGenerationCount               uint32                        // Offset: 0x3A4 - Language generation count
	Reserved4                         uint64                        // Offset: 0x3A8 - Reserved
	InterruptTimeBias                 uint64                        // Offset: 0x3B0 - Interrupt time bias
	QpcBias                           uint64                        // Offset: 0x3B8 - QPC bias
	ActiveProcessorCount              uint32                        // Offset: 0x3C0 - Active processor count
	ActiveGroupCount                  uint8                         // Offset: 0x3C4 - Active group count
	Reserved9                         uint8                         // Offset: 0x3C5 - Reserved
	QpcData                           uint16                        // Offset: 0x3C6 - QPC data (union with QpcBypassEnabled)
	TimeZoneBiasEffectiveStart        LARGE_INTEGER                 // Offset: 0x3C8 - Time zone bias effective start
	TimeZoneBiasEffectiveEnd          LARGE_INTEGER                 // Offset: 0x3D0 - Time zone bias effective end
	XState                            XSTATE_CONFIGURATION          // Offset: 0x3D8 - Extended state configuration (1024 bytes)
	FeatureConfigurationChangeStamp   KSYSTEM_TIME                  // Offset: 0x7D8 - Feature configuration change stamp
	Spare                             uint32                        // Offset: 0x7E4 - Spare
	UserPointerAuthMask               uint64                        // Offset: 0x7E8 - User pointer auth mask
	XStateArm64                       XSTATE_CONFIGURATION          // Offset: 0x7F0 - ARM64 extended state (1024 bytes)
	Reserved10                        [210]uint32                   // Offset: 0xBF0 - Reserved (840 bytes)
}
