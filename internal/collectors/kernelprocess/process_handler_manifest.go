package kernelprocess

import (
	"etw_exporter/internal/etw/guids"
	"etw_exporter/internal/etw/handlers"
	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"
	"etw_exporter/internal/windowsapi"

	"github.com/tekert/goetw/etw"
	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// NOTE: not useful, no thread rundowns for these process events, this in unusable in win10.

// HandlerManifest processes ETW process, thread, and image events from the
// modern manifest-based provider and delegates to the KernelStateManager.
type HandlerManifest struct {
	stateManager *statemanager.KernelStateManager
	log          *phusluadapter.SampledLogger
}

// NewProcessHandlerManifest creates a new process handler instance.
func NewProcessHandlerManifest(sm *statemanager.KernelStateManager) *HandlerManifest {
	return &HandlerManifest{
		stateManager: sm,
		log:          logger.NewSampledLoggerCtx("process_handler_manifest"),
	}
}

// RegisterRoutes tells the EventHandler which ETW events this handler is interested in.
func (h *HandlerManifest) RegisterRoutes(router handlers.Router) {
	// Process Events (using EventRecordHelper due to variable-length strings)
	// Note: ProcessRundown (Event ID 15) has the same schema as ProcessStart (Event ID 1)
	router.AddRoute(*guids.MicrosoftWindowsKernelProcessGUID, 1, h.HandleProcessStart)  // ProcessStart
	router.AddRoute(*guids.MicrosoftWindowsKernelProcessGUID, 15, h.HandleProcessStart) // ProcessRundown
	router.AddRoute(*guids.MicrosoftWindowsKernelProcessGUID, 2, h.HandleProcessEnd)    // ProcessStop

	// Thread Events (using raw handlers for performance as schema is fixed-size)
	router.AddRawRoute(*guids.MicrosoftWindowsKernelProcessGUID, 3, h.HandleThreadStartRaw) // ThreadStart
	router.AddRawRoute(*guids.MicrosoftWindowsKernelProcessGUID, 4, h.HandleThreadEndRaw)   // ThreadStop

	// Image Events (using EventRecordHelper due to variable-length strings)
	router.AddRoute(*guids.MicrosoftWindowsKernelProcessGUID, 5, h.HandleImageLoad)   // ImageLoad
	router.AddRoute(*guids.MicrosoftWindowsKernelProcessGUID, 6, h.HandleImageUnload) // ImageUnload
}

// HandleProcessStart processes ProcessStart events to track process creation.
// This handler is crucial for maintaining the process list in the KernelStateManager.
// It extracts key process identifiers and metadata to create a new process entry.
//
// ETW Event Details:
//   - Provider Name: Microsoft-Windows-Kernel-Process
//   - Provider GUID: {22fb2cd6-0e7b-422b-a0c7-2fad1fd0e716}
//   - Event ID(s): 1
//   - Event Name(s): ProcessStart
//   - Event Version(s): 0-3
//   - Schema: Manifest (XML)
//
// Schema (from manifest, version 1):
//   - ProcessID (win:UInt32): Process identifier.
//   - ProcessSequenceNumber (win:UInt64): A sequence number that uniquely identifies a process.
//   - CreateTime (win:FILETIME): Process creation time.
//   - ParentProcessID (win:UInt32): Parent process identifier.
//   - ParentProcessSequenceNumber (win:UInt64): The sequence number of the parent process.
//   - SessionID (win:UInt32): Session identifier.
//   - Flags (win:UInt32): Process flags.
//   - ProcessTokenElevationType (win:UInt32): The type of token elevation.
//   - ProcessTokenIsElevated (win:UInt32): Indicates if the process token is elevated.
//   - MandatoryLabel (win:SID): The mandatory label of the process.
//   - ImageName (win:UnicodeString): Process image name (full path).
//   - ImageChecksum (win:UInt32): The checksum of the process image.
//   - TimeDateStamp (win:UInt32): The time and date stamp of the process image.
//   - PackageFullName (win:UnicodeString): The full name of the package, if applicable.
//   - PackageRelativeAppId (win:UnicodeString): The relative application ID within the package.
//
// The ProcessSequenceNumber is the most critical field, as it serves as the unique StartKey
// for a process instance, allowing correct handling of PID reuse.
func (ph *HandlerManifest) HandleProcessStart(helper *etw.EventRecordHelper) error {
	processID, _ := helper.GetPropertyUint("ProcessID")
	parentProcessID, _ := helper.GetPropertyUint("ParentProcessID")
	processName, _ := helper.GetPropertyString("ImageName")
	sessionID, _ := helper.GetPropertyUint("SessionID")
	timestamp := helper.Timestamp()

	// For manifest events, the StartKey is the ProcessSequenceNumber.
	sequenceNumber, _ := helper.GetPropertyUint("ProcessSequenceNumber")
	startKey := windowsapi.GetStartKeyFromSequence(sequenceNumber)
	//startKey3, _ := helper.EventRec.ExtProcessStartKey()

	ph.stateManager.AddProcess(
		uint32(processID),
		startKey,
		processName,
		uint32(parentProcessID),
		uint32(sessionID),
		timestamp,
	)
	return nil
}

// HandleProcessEnd processes ProcessStop events to mark processes for cleanup.
//
// ETW Event Details:
//   - Provider Name: Microsoft-Windows-Kernel-Process
//   - Provider GUID: {22fb2cd6-0e7b-422b-a0c7-2fad1fd0e716}
//   - Event ID(s): 2
//   - Event Name(s): ProcessStop
//   - Event Version(s): 0-2
//   - Schema: Manifest (XML)
//
// Schema (from manifest, version 2):
//   - ProcessID (win:UInt32): Process identifier.
//   - ProcessSequenceNumber (win:UInt64): A sequence number that uniquely identifies a process.
//   - CreateTime (win:FILETIME): Process creation time.
//   - ExitTime (win:FILETIME): Process exit time.
//   - ExitCode (win:UInt32): The exit code of the process.
//   - TokenElevationType (win:UInt32): The type of token elevation.
//   - HandleCount (win:UInt32): The number of open handles.
//   - CommitCharge (win:UInt64): The commit charge for the process in bytes.
//   - CommitPeak (win:UInt64): The peak commit charge for the process in bytes.
//   - CPUCycleCount (win:UInt64): The number of CPU cycles consumed by the process.
//   - ReadOperationCount (win:UInt32): The number of read operations performed.
//   - WriteOperationCount (win:UInt32): The number of write operations performed.
//   - ReadTransferKiloBytes (win:UInt32): The number of kilobytes read.
//   - WriteTransferKiloBytes (win:UInt32): The number of kilobytes written.
//   - HardFaultCount (win:UInt32): The number of hard page faults.
//   - ImageName (win:AnsiString): Process image name (full path).
func (ph *HandlerManifest) HandleProcessEnd(helper *etw.EventRecordHelper) error {
	processID, _ := helper.GetPropertyUint("ProcessID")
	timestamp := helper.Timestamp()

	// For manifest events, the StartKey is the ProcessSequenceNumber.
	sequenceNumber, _ := helper.GetPropertyUint("ProcessSequenceNumber")
	startKey := windowsapi.GetStartKeyFromSequence(sequenceNumber)

	ph.stateManager.MarkProcessForDeletion(uint32(processID), startKey, timestamp)
	return nil
}

// HandleThreadStartRaw processes ThreadStart events to track thread creation.
// This is a high-performance "fast path" that reads directly from the UserData
// buffer using known offsets, as the event schema has a fixed size.
//
// ETW Event Details:
//   - Provider Name: Microsoft-Windows-Kernel-Process
//   - Provider GUID: {22fb2cd6-0e7b-422b-a0c7-2fad1fd0e716}
//   - Event ID(s): 3
//   - Event Name(s): ThreadStart
//   - Event Version(s): 0-1
//   - Schema: Manifest (XML)
//
// Schema (from manifest, version 1):
//   - ProcessID (win:UInt32): Process identifier of the parent process. Offset: 0.
//   - ThreadID (win:UInt32): Thread identifier. Offset: 4.
//   - StackBase (win:Pointer): The base address of the thread's stack. Offset: 8.
//   - StackLimit (win:Pointer): The limit of the thread's stack. Offset: 16.
//   - UserStackBase (win:Pointer): The base address of the thread's user-mode stack. Offset: 24.
//   - UserStackLimit (win:Pointer): The limit of the thread's user-mode stack. Offset: 32.
//   - StartAddr (win:Pointer): The starting address of the thread. Offset: 40.
//   - Win32StartAddr (win:Pointer): The Win32 starting address of the thread. Offset: 48.
//   - TebBase (win:Pointer): The base address of the Thread Environment Block. Offset: 56.
//   - SubProcessTag (win:UInt32): Tag for service attribution (svchost). Offset: 64.
func (ph *HandlerManifest) HandleThreadStartRaw(er *etw.EventRecord) error {
	processID, err := er.GetUint32At(0)
	if err != nil {
		return err
	}
	threadID, err := er.GetUint32At(4)
	if err != nil {
		return err
	}
	subProcessTag, err := er.GetUint32At(64)
	if err != nil {
		// SubProcessTag may not exist on older event versions, ignore error.
		subProcessTag = 0
	}

	ph.stateManager.AddThread(threadID, processID, er.Timestamp(), subProcessTag)
	return nil
}

// HandleThreadEndRaw processes ThreadStop events to mark threads for cleanup.
// This is a high-performance "fast path" that reads directly from the UserData
// buffer using known offsets, as the event schema has a fixed size.
//
// ETW Event Details:
//   - Provider Name: Microsoft-Windows-Kernel-Process
//   - Provider GUID: {22fb2cd6-0e7b-422b-a0c7-2fad1fd0e716}
//   - Event ID(s): 4
//   - Event Name(s): ThreadStop
//   - Event Version(s): 0-1
//   - Schema: Manifest (XML)
//
// Schema (from manifest, version 1):
//   - ProcessID (win:UInt32): Process identifier of the parent process. Offset: 0.
//   - ThreadID (win:UInt32): Thread identifier. Offset: 4.
//   - StackBase (win:Pointer): The base address of the thread's stack. Offset: 8.
//   - StackLimit (win:Pointer): The limit of the thread's stack. Offset: 16.
//   - UserStackBase (win:Pointer): The base address of the thread's user-mode stack. Offset: 24.
//   - UserStackLimit (win:Pointer): The limit of the thread's user-mode stack. Offset: 32.
//   - StartAddr (win:Pointer): The starting address of the thread. Offset: 40.
//   - Win32StartAddr (win:Pointer): The Win32 starting address of the thread. Offset: 48.
//   - TebBase (win:Pointer): The base address of the Thread Environment Block. Offset: 56.
//   - SubProcessTag (win:UInt32): Tag for service attribution (svchost). Offset: 64.
//   - CycleTime (win:UInt64): CPU cycles consumed by the thread. Offset: 68.
func (ph *HandlerManifest) HandleThreadEndRaw(er *etw.EventRecord) error {
	threadID, err := er.GetUint32At(4)
	if err != nil {
		return err
	}

	ph.stateManager.MarkThreadForDeletion(threadID)
	return nil
}

// HandleImageLoad processes ImageLoad events to track DLLs and executables.
//
// ETW Event Details:
//   - Provider Name: Microsoft-Windows-Kernel-Process
//   - Provider GUID: {22fb2cd6-0e7b-422b-a0c7-2fad1fd0e716}
//   - Event ID(s): 5
//   - Event Name(s): ImageLoad
//   - Event Version(s): 0
//   - Schema: Manifest (XML)
//
// Schema (from manifest, version 0):
//   - ImageBase (win:Pointer): Base address of the loaded image.
//   - ImageSize (win:Pointer): Size of the loaded image.
//   - ProcessID (win:UInt32): Process identifier where the image was loaded.
//   - ImageCheckSum (win:UInt32): Checksum of the image file.
//   - TimeDateStamp (win:UInt32): Timestamp from the image file header.
//   - DefaultBase (win:Pointer): The default base address of the image.
//   - ImageName (win:UnicodeString): Full path to the image file.
func (ph *HandlerManifest) HandleImageLoad(helper *etw.EventRecordHelper) error {
	processID, _ := helper.GetPropertyUint("ProcessID")
	imageBase, _ := helper.GetPropertyUint("ImageBase")
	imageSize, _ := helper.GetPropertyUint("ImageSize")
	imageName, _ := helper.GetPropertyString("ImageName")
	imageChecksum, _ := helper.GetPropertyUint("ImageCheckSum")
	timeDateStamp, _ := helper.GetPropertyUint("TimeDateStamp")
	// timestamp := helper.Timestamp()

	ph.stateManager.AddImage(
		uint32(processID),
		imageBase,
		imageSize,
		imageName,
		uint32(timeDateStamp),
		uint32(imageChecksum),
	)
	return nil
}

// HandleImageUnload processes ImageUnload events.
//
// ETW Event Details:
//   - Provider Name: Microsoft-Windows-Kernel-Process
//   - Provider GUID: {22fb2cd6-0e7b-422b-a0c7-2fad1fd0e716}
//   - Event ID(s): 6
//   - Event Name(s): ImageUnload
//   - Event Version(s): 0
//   - Schema: Manifest (XML)
//
// Schema (from manifest, version 0):
//   - ImageBase (win:Pointer): Base address of the unloaded image.
//   - ImageSize (win:Pointer): Size of the unloaded image.
//   - ProcessID (win:UInt32): Process identifier from which the image was unloaded.
//   - ImageCheckSum (win:UInt32): Checksum of the image file.
//   - TimeDateStamp (win:UInt32): Timestamp from the image file header.
//   - DefaultBase (win:Pointer): The default base address of the image.
//   - ImageName (win:UnicodeString): Full path to the image file.
//
// The ImageBase is used as the unique key to identify and remove the image from the state manager.
func (ph *HandlerManifest) HandleImageUnload(helper *etw.EventRecordHelper) error {
	imageBase, _ := helper.GetPropertyUint("ImageBase")

	ph.stateManager.UnloadImage(imageBase)
	return nil
}
