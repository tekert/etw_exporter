package kernelthread

import (
	"runtime"
	"sync/atomic"
	"time"

	"github.com/tekert/goetw/etw"

	"etw_exporter/internal/etw/guids"
	"etw_exporter/internal/etw/handlers"
	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"

	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// Handler handles thread events and acts as a mediator.
// This collector processes ETW events from the NT Kernel Logger (CSwitch) provider
// and integrates with a custom Prometheus collector for optimal performance.
//
// Provider Information:
// - GUID: {3d6fa8d1-fe05-11d0-9dda-00c04fd7ba7c}
// - Name: NT Kernel Logger (CSwitch)
// - Description: Kernel thread events including context switches, thread lifecycle
//
// Supported Event Types:
// - EventType 36: CSwitch (Context Switch) - Thread scheduling events
// - EventType 50: ReadyThread - Thread ready for execution events
// - EventType 1,2,3,4: Thread start/end events
//
// The collector maintains low cardinality by aggregating metrics and using the
// custom collector pattern.
type Handler struct {
	lastCpuSwitch []atomic.Int64
	stateManager  *statemanager.KernelStateManager
	collector     *ThreadCollector // Can be nil
	log           *phusluadapter.SampledLogger

	//collector ThreadCollectorType // Can be nil
}

// NewThreadHandler creates a new thread collector instance with custom collector integration.
// The custom collector provides high-performance metrics aggregation
func NewThreadHandler(sm *statemanager.KernelStateManager) *Handler {
	return &Handler{
		lastCpuSwitch: make([]atomic.Int64, runtime.NumCPU()),
		//customCollector: NewThreadCSCollector(sm),
		stateManager: sm,
		log:          logger.NewSampledLoggerCtx("thread_handler"),
		//log:             logger.NewLoggerWithContext("thread_handler"),
		collector: nil, // Starts with no collector
	}
}

// AttachCollector allows a metrics collector to subscribe to the handler's events.
func (c *Handler) AttachCollector(collector *ThreadCollector) {
	c.log.Debug().Msg("Attaching metrics collector to thread handler.")
	c.collector = collector
}

// GetCustomCollector returns the custom Prometheus collector for thread metrics.
func (c *Handler) GetCustomCollector() *ThreadCollector {
	return c.collector
}

// RegisterRoutes tells the EventHandler which ETW events this handler is interested in.
func (h *Handler) RegisterRoutes(router handlers.Router) {
	// Provider: NT Kernel Logger (Thread) ({3d6fa8d1-fe05-11d0-9dda-00c04fd7ba7c})
	// Note: CSwitch (36) is handled in the raw EventRecordCallback in the main event
	// handler for maximum performance. A route is not registered for it here.

	router.AddRoute(*guids.ThreadKernelGUID, etw.EVENT_TRACE_TYPE_START, h.HandleThreadStart)    // ThreadStart
	router.AddRoute(*guids.ThreadKernelGUID, etw.EVENT_TRACE_TYPE_DC_START, h.HandleThreadStart) // ThreadRundown
	router.AddRoute(*guids.ThreadKernelGUID, etw.EVENT_TRACE_TYPE_DC_END, h.HandleThreadStart)   // ThreadRundownEnd (Go to Start to refresh)
	router.AddRoute(*guids.ThreadKernelGUID, etw.EVENT_TRACE_TYPE_END, h.HandleThreadEnd)        // ThreadEnd

	// Routes needed only for metrics collection
	//router.AddRawRoute(*guids.ThreadKernelGUID, 36, h.HandleContextSwitchRaw) // CSwitch // hardcoded route for performance
	router.AddRoute(*guids.ThreadKernelGUID, 50, h.HandleReadyThread) // ReadyThread
}

// HandleContextSwitchRaw processes context switch events directly from the EVENT_RECORD.
// This is a high-performance "fast path" that reads directly from the UserData
// buffer using known offsets, bypassing the creation of an EventRecordHelper and
// the associated parsing overhead.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger
//   - Provider GUID: {3d6fa8d1-fe05-11d0-9dda-00c04fd7ba7c}
//   - Event ID(s): 36
//   - Event Name(s): CSwitch
//   - Event Version(s): 2, 3, 4
//   - Schema: MOF
//
// Schema (from MOF):
//   - NewThreadId (uint32): The thread identifier of the thread being switched to. Offset: 0.
//   - OldThreadId (uint32): The thread identifier of the thread being switched out. Offset: 4.
//   - NewThreadPriority (int8): The priority of the new thread. Offset: 8.
//   - OldThreadPriority (int8): The priority of the old thread. Offset: 9.
//   - PreviousCState (uint8): The C-state that was last used by the processor. Offset: 10.
//   - SpareByte (int8): Not used. Offset: 11.
//   - OldThreadWaitReason (int8): The reason why the old thread was waiting. Offset: 12.
//   - OldThreadWaitMode (int8): The wait mode (KernelMode or UserMode) of the old thread. Offset: 13.
//   - OldThreadState (int8): The state of the old thread (e.g., Waiting, Terminated). Offset: 14.
//   - OldThreadWaitIdealProcessor (int8): The ideal processor for the old thread to wait on. Offset: 15.
//   - NewThreadWaitTime (uint32): The wait time for the new thread. Offset: 16.
//   - Reserved (uint32): Reserved for future use. Offset: 20.
func (c *Handler) HandleContextSwitchRaw(er *etw.EventRecord) error {
	if c.collector == nil {
		return nil
	}

	var newThreadID uint32
	var oldThreadWaitReason int8

	// Get minimum supported version
	if er.EventHeader.EventDescriptor.Version >= 2 {
		// Supported versions
		//cswitch := (*etw.MofCSwitch_V2)(unsafe.Pointer(er.UserData)) // With v2 we cover what we need
		cswitch, ok := etw.UnsafeCast[etw.MofCSwitch_V2](er)
		if !ok {
			c.log.SampledError("fail-decode").
				Uint8("version", er.EventHeader.EventDescriptor.Version).
				Uint8("opcode", er.EventHeader.EventDescriptor.Opcode).
				Uint16("datalen", er.UserDataLength).
				Msg("Failed to decode CSwitch event")
			return nil // Not a fatal error, just skip processing.
		}

		newThreadID = cswitch.NewThreadId
		oldThreadWaitReason = cswitch.OldThreadWaitReason
	}

	// // Read properties using direct offsets.
	// newThreadID, err := er.GetUint32At(0)
	// if err != nil {
	// 	c.log.SampledError("fail-newthreadid").
	// 		Err(err).Msg("Failed to read NewThreadId from raw CSwitch event")
	// 	return err
	// }
	// oldThreadWaitReason, err := er.GetUint8At(12)
	// if err != nil {
	// 	c.log.SampledError("fail-waitreason").
	// 		Err(err).Msg("Failed to read OldThreadWaitReason from raw CSwitch event")
	// 	return err
	// }

	// Get CPU number from processor index
	cpu := er.ProcessorNumber()

	// Timestamp (already converted from FILETIME by gotetw)
	eventTimeNano := er.Timestamp().UnixNano()

	// Calculate context switch interval using atomic operations.
	var interval time.Duration
	if lastSwitchNano := c.lastCpuSwitch[cpu].Swap(eventTimeNano); lastSwitchNano > 0 {
		interval = time.Duration(eventTimeNano - lastSwitchNano)
	}

	// Get process ID for the NEW thread (incoming thread)
	var startKey uint64
	if pid, exists := c.stateManager.GetProcessIDForThread(uint32(newThreadID)); exists {
		// Now that we have the PID, get its StartKey to form a unique identifier.
		startKey, _ = c.stateManager.GetProcessStartKey(pid)
	}

	// A startKey of 0 means we couldn't attribute the I/O to a known process.
	// The collector will still record the system-wide metric, but will skip the
	// per-process metric if startKey is 0.

	// Record context switch in custom collector
	c.collector.RecordContextSwitch(cpu, newThreadID, startKey, interval)

	// Track thread state transitions using integer constants for maximum performance.
	c.collector.RecordThreadStateTransition(StateWaiting, int8(oldThreadWaitReason))
	c.collector.RecordThreadStateTransition(StateRunning, 0)

	return nil
}

// HandleContextSwitch processes context switch events (CSwitch).
// This handler processes ETW events from the kernel thread provider for context switches,
// which occur when the Windows scheduler switches execution from one thread to another.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger
//   - Provider GUID: {3d6fa8d1-fe05-11d0-9dda-00c04fd7ba7c}
//   - Event ID(s): 36
//   - Event Name(s): CSwitch
//   - Event Version(s): 2, 3, 4
//   - Schema: MOF
//
// Schema (from MOF):
//   - NewThreadId (uint32): The thread identifier of the thread being switched to.
//   - OldThreadId (uint32): The thread identifier of the thread being switched out.
//   - NewThreadPriority (int8): The priority of the new thread.
//   - OldThreadPriority (int8): The priority of the old thread.
//   - PreviousCState (uint8): The C-state that was last used by the processor.
//   - SpareByte (int8): Not used.
//   - OldThreadWaitReason (int8): The reason why the old thread was waiting.
//   - OldThreadWaitMode (int8): The wait mode (KernelMode or UserMode) of the old thread.
//   - OldThreadState (int8): The state of the old thread (e.g., Waiting, Terminated).
//   - OldThreadWaitIdealProcessor (sint8): The ideal processor for the old thread to wait on.
//   - NewThreadWaitTime (uint32): The wait time for the new thread.
//   - Reserved (uint32): Reserved for future use.
//
// This handler tracks context switches per CPU and per process, calculates
// context switch intervals, and records thread state transitions.
func (c *Handler) HandleContextSwitch(helper *etw.EventRecordHelper) error {
	if c.collector == nil {
		return nil
	}
	// NOTE: This is a fallback/slower path. The primary logic has been moved to
	// HandleContextSwitchRaw for performance. This function will not be called
	// for CSwitch events due to the fast-path routing logic in the main event handler.

	// Parse context switch event data according to CSwitch class documentation
	newThreadID, _ := helper.GetPropertyUint("NewThreadId")
	waitReason, _ := helper.GetPropertyInt("OldThreadWaitReason")

	// Get CPU number from processor index
	cpu := uint16(helper.EventRec.ProcessorNumber())

	// Timestamp from the event
	eventTimeNano := helper.Timestamp().UnixNano()

	// Calculate context switch interval using atomic operations to avoid allocations and locks.
	var interval time.Duration
	// Swap the new time and get the old time in a single atomic operation.
	if lastSwitchNano := c.lastCpuSwitch[cpu].Swap(eventTimeNano); lastSwitchNano > 0 {
		interval = time.Duration(eventTimeNano - lastSwitchNano)
	}

	// Get process ID for the NEW thread (incoming thread)
	var startKey uint64
	if pid, exists := c.stateManager.GetProcessIDForThread(uint32(newThreadID)); exists {
		startKey, _ = c.stateManager.GetProcessStartKey(pid)
	}

	// Record context switch in custom collector (handles process name lookup internally)
	c.collector.RecordContextSwitch(cpu, uint32(newThreadID), startKey, interval)

	// Track thread state transitions
	c.collector.RecordThreadStateTransition(StateWaiting, int8(waitReason))
	c.collector.RecordThreadStateTransition(StateRunning, 0)

	return nil
}

// HandleReadyThread processes thread ready events.
// This handler processes ETW events when a thread becomes ready for execution
// (transitions from a waiting state to ready state).
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger
//   - Provider GUID: {3d6fa8d1-fe05-11d0-9dda-00c04fd7ba7c}
//   - Event ID(s): 50
//   - Event Name(s): ReadyThread
//   - Event Version(s): 2
//   - Schema: MOF
//
// Schema (from MOF):
//   - TThreadId (uint32): The thread identifier of the thread being readied for execution.
//   - AdjustReason (sint8): The reason for the priority boost.
//   - AdjustIncrement (sint8): The value by which the priority is being adjusted.
//   - Flag (sint8): State flags indicating if the kernel stack or process address space is swapped out.
//   - Reserved (sint8): Reserved for future use.
//
// This event indicates a thread is transitioning from a wait state to ready state,
// meaning it's eligible for scheduling but not yet running.
func (c *Handler) HandleReadyThread(helper *etw.EventRecordHelper) error {
	// Track thread state transition to ready
	if c.collector != nil {
		c.collector.RecordThreadStateTransition(StateReady, 0)
	}
	return nil
}

// HandleThreadStart processes thread creation and rundown events.
// This handler processes ETW events when a new thread is created (Start) or when
// an existing thread is enumerated during a session start (DCStart/Rundown).
// The rundown events are crucial for populating the initial state of all running
// threads when the exporter starts, preventing "unknown thread" errors.
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger
//   - Provider GUID: {3d6fa8d1-fe05-11d0-9dda-00c04fd7ba7c}
//   - Event ID(s): 1 (Start), 3 (DCStart/Rundown)
//   - Event Name(s): Thread_V2_TypeGroup1, Thread_V3_TypeGroup1, Thread_TypeGroup1
//   - Event Version(s): 2, 3, 4
//   - Schema: MOF
//
// Schema (from MOF):
//   - ProcessId (uint32): Process identifier of the thread's parent process.
//   - TThreadId (uint32): Thread identifier of the newly created or enumerated thread.
//   - StackBase (pointer): Base address of the thread's kernel-mode stack.
//   - StackLimit (pointer): Limit of the thread's kernel-mode stack.
//   - UserStackBase (pointer): Base address of the thread's user-mode stack.
//   - UserStackLimit (pointer): Limit of the thread's user-mode stack.
//   - Affinity (uint32): The set of processors on which the thread is allowed to run.
//   - Win32StartAddr (pointer): Starting address of the function to be executed by this thread.
//   - TebBase (pointer): Thread Environment Block (TEB) base address.
//   - SubProcessTag (uint32): Identifies the service if the thread is owned by a service.
//   - BasePriority (uint8): The scheduler priority of the thread.
//   - PagePriority (uint8): A memory page priority hint for memory pages accessed by the thread.
//   - IoPriority (uint8): An I/O priority hint for scheduling I/O generated by the thread.
//   - ThreadFlags (uint8): Not used.
//
// This handler maintains the thread-to-process mapping for context switch tracking.
func (c *Handler) HandleThreadStart(helper *etw.EventRecordHelper) error {
	threadID, _ := helper.GetPropertyUint("TThreadId")
	processID, _ := helper.GetPropertyUint("ProcessId")

	// Store thread to process mapping for context switch metrics
	c.stateManager.AddThread(uint32(threadID), uint32(processID))

	// Track thread creation
	if c.collector != nil {
		c.collector.RecordThreadCreation()
	}

	return nil
}

// HandleThreadEnd processes thread termination and rundown-end events.
// This handler processes ETW events when a thread terminates (End) or when the
// enumeration of existing threads concludes at the end of a session (DCEnd).
//
// ETW Event Details:
//   - Provider Name: NT Kernel Logger
//   - Provider GUID: {3d6fa8d1-fe05-11d0-9dda-00c04fd7ba7c}
//   - Event ID(s): 2 (End), 4 (DCEnd)
//   - Event Name(s): Thread_V2_TypeGroup1, Thread_V3_TypeGroup1, Thread_TypeGroup1
//   - Event Version(s): 2, 3, 4
//   - Schema: MOF
//
// Schema (from MOF):
//   - ProcessId (uint32): Process identifier of the thread's parent process.
//   - TThreadId (uint32): Thread identifier of the terminated thread.
//   - StackBase (pointer): Base address of the thread's kernel-mode stack.
//   - StackLimit (pointer): Limit of the thread's kernel-mode stack.
//   - UserStackBase (pointer): Base address of the thread's user-mode stack.
//   - UserStackLimit (pointer): Limit of the thread's user-mode stack.
//   - Affinity (uint32): The set of processors on which the thread is allowed to run.
//   - Win32StartAddr (pointer): Starting address of the function to be executed by this thread.
//   - TebBase (pointer): Thread Environment Block (TEB) base address.
//   - SubProcessTag (uint32): Identifies the service if the thread is owned by a service.
//   - BasePriority (uint8): The scheduler priority of the thread.
//   - PagePriority (uint8): A memory page priority hint for memory pages accessed by the thread.
//   - IoPriority (uint8): An I/O priority hint for scheduling I/O generated by the thread.
//   - ThreadFlags (uint8): Not used.
//
// This handler cleans up the thread-to-process mapping and records termination metrics.
func (c *Handler) HandleThreadEnd(helper *etw.EventRecordHelper) error {
	threadID, _ := helper.GetPropertyUint("TThreadId")

	// Mark the thread for deletion. The actual cleanup will happen after the next scrape.
	c.stateManager.MarkThreadForDeletion(uint32(threadID))

	// Track thread termination
	if c.collector != nil {
		c.collector.RecordThreadTermination()
	}
	return nil
}

// GetWaitReasonString converts wait reason code to human-readable string.
// This function maps the numeric wait reason codes from ETW CSwitch events
// to descriptive strings for better observability.
//
// Wait Reason Codes (from Windows kernel documentation):
// These codes indicate why a thread was in a waiting state before the context switch.
// The codes are standardized across Windows versions and map to KWAIT_REASON enum values.
//
// Categories:
// - 0-6: Executive wait reasons (basic kernel waits)
// - 7-19: Wrappable wait reasons (Wr prefix indicates interruptible waits)
// - 20-36: Special wait reasons (quantum end, preemption, synchronization)
//
// Reference: This mapping is based on the Windows Driver Kit (WDK) documentation
// and ETW provider definitions for thread context switch events.
func GetWaitReasonString(waitReason int8) string {
	switch waitReason {
	case 0:
		return "Executive"
	case 1:
		return "FreePage"
	case 2:
		return "PageIn"
	case 3:
		return "PoolAllocation"
	case 4:
		return "DelayExecution"
	case 5:
		return "Suspended"
	case 6:
		return "UserRequest"
	case 7:
		return "WrExecutive"
	case 8:
		return "WrFreePage"
	case 9:
		return "WrPageIn"
	case 10:
		return "WrPoolAllocation"
	case 11:
		return "WrDelayExecution"
	case 12:
		return "WrSuspended"
	case 13:
		return "WrUserRequest"
	case 14:
		return "WrEventPair"
	case 15:
		return "WrQueue"
	case 16:
		return "WrLpcReceive"
	case 17:
		return "WrLpcReply"
	case 18:
		return "WrVirtualMemory"
	case 19:
		return "WrPageOut"
	case 20:
		return "WrRendezvous"
	case 21:
		return "WrKeyedEvent"
	case 22:
		return "WrTerminated"
	case 23:
		return "WrProcessInSwap"
	case 24:
		return "WrCpuRateControl"
	case 25:
		return "WrCalloutStack"
	case 26:
		return "WrKernel"
	case 27:
		return "WrResource"
	case 28:
		return "WrPushLock"
	case 29:
		return "WrMutex"
	case 30:
		return "WrQuantumEnd"
	case 31:
		return "WrDispatchInt"
	case 32:
		return "WrPreempted"
	case 33:
		return "WrYieldExecution"
	case 34:
		return "WrFastMutex"
	case 35:
		return "WrGuardedMutex"
	case 36:
		return "WrRundown"
	case 37:
		return "MaximumWaitReason"
	default:
		return "Unknown"
	}
}

// GetAllWaitReasonStrings returns a slice of all known, valid wait reason strings.
// This provides a definitive list for pre-allocating metrics, ensuring that
// "Unknown" or "Spare" values are not included.
func GetAllWaitReasonStrings() []string {
	return []string{
		"Executive", "FreePage", "PageIn", "PoolAllocation", "DelayExecution",
		"Suspended", "UserRequest", "WrExecutive", "WrFreePage", "WrPageIn",
		"WrPoolAllocation", "WrDelayExecution", "WrSuspended", "WrUserRequest",
		"WrEventPair", "WrQueue", "WrLpcReceive", "WrLpcReply", "WrVirtualMemory",
		"WrPageOut", "WrRendezvous", "WrKeyedEvent", "WrTerminated", "WrProcessInSwap",
		"WrCpuRateControl", "WrCalloutStack", "WrKernel", "WrResource",
		"WrPushLock", "WrMutex", "WrQuantumEnd", "WrDispatchInt", "WrPreempted",
		"WrYieldExecution", "WrFastMutex", "WrGuardedMutex", "WrRundown", "MaximumWaitReason",
	}
}
