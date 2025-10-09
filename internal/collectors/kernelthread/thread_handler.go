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
type Handler struct {
	lastCpuSwitch []atomic.Int64
	stateManager  *statemanager.KernelStateManager
	collector     *ThreadCollector // Can be nil
	log           *phusluadapter.SampledLogger
}

// NewThreadHandler creates a new thread collector instance with custom collector integration.
// The custom collector provides high-performance metrics aggregation
func NewThreadHandler(sm *statemanager.KernelStateManager) *Handler {
	return &Handler{
		lastCpuSwitch: make([]atomic.Int64, runtime.NumCPU()),
		stateManager:  sm,
		log:           logger.NewSampledLoggerCtx("thread_handler"),
		collector:     nil, // Starts with no collector
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

func (h *Handler) RegisterRoutes(router handlers.Router) {
	// Provider: NT Kernel Logger (Thread) ({3d6fa8d1-fe05-11d0-9dda-00c04fd7ba7c})
	// Provider: System Scheduler ({9e814aad-3204-11d2-9a82-006008a86939})
	// Define routes for ThreadKernelGUID
	threadRoutes := map[uint8]handlers.RawEventHandlerFunc{
		etw.EVENT_TRACE_TYPE_START:    h.HandleThreadStartRaw,   // ThreadStart
		etw.EVENT_TRACE_TYPE_DC_START: h.HandleThreadStartRaw,   // ThreadRundown
		etw.EVENT_TRACE_TYPE_DC_END:   h.HandleThreadStartRaw,   // ThreadRundownEnd (Go to Start to refresh)
		etw.EVENT_TRACE_TYPE_END:      h.HandleThreadEndRaw,     // ThreadEnd
		50:                            h.HandleReadyThreadRaw,   // ReadyThread
		36:                            h.HandleContextSwitchRaw, // CSwitch (very high frequency)
	}

	handlers.RegisterRawRoutesForGUID(router, guids.ThreadKernelGUID, threadRoutes)          // <= win 10
	handlers.RegisterRawRoutesForGUID(router, etw.SystemSchedulerProviderGuid, threadRoutes) // >= win 11
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

	// Read properties using direct offsets.
	newThreadID, err := er.GetUint32At(0)
	if err != nil {
		c.log.SampledError("fail-newthreadid").
			Err(err).Msg("Failed to read NewThreadId from raw CSwitch event")
		return err
	}
	oldThreadWaitReason, err := er.GetUint8At(12)
	if err != nil {
		c.log.SampledError("fail-waitreason").
			Err(err).Msg("Failed to read OldThreadWaitReason from raw CSwitch event")
		return err
	}

	// Get CPU number from processor index
	cpu := er.ProcessorNumber()

	// Timestamp (already converted from FILETIME by gotetw)
	//eventTimeNano := er.Timestamp().UnixNano() // slower
	eventTimeNano := er.TimestampNanos()

	// Calculate context switch interval using atomic operations.
	var interval time.Duration
	if lastSwitchNano := c.lastCpuSwitch[cpu].Swap(eventTimeNano); lastSwitchNano > 0 {
		interval = time.Duration(eventTimeNano - lastSwitchNano)
	}

	// Directly resolve the thread to its process and increment the counter on the hot path.
	// This simplifies the aggregation logic significantly.
	if pData, ok := c.stateManager.Threads.GetCurrentProcessDataByThread(newThreadID); ok {
		pData.RecordContextSwitch()
	}

	// Records system-wide (non-process-specific) metrics directly with the collector.
	c.collector.RecordCpuContextSwitch(cpu, interval)

	// Track thread state transitions using integer constants for maximum performance.
	c.collector.RecordThreadStateTransition(StateWaiting, oldThreadWaitReason)
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
func (c *Handler) HandleReadyThreadRaw(record *etw.EventRecord) error {
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
//   - ProcessId (uint32): Process identifier of the thread's parent process.           [offset 0]
//   - TThreadId (uint32): Thread identifier of the newly created or enumerated thread. [offset 4]
//   - StackBase (pointer): Base address of the thread's kernel-mode stack.             [offset 8]
//   - StackLimit (pointer): Limit of the thread's kernel-mode stack.                   [offset 16]
//   - UserStackBase (pointer): Base address of the thread's user-mode stack.           [offset 24]
//   - UserStackLimit (pointer): Limit of the thread's user-mode stack.                 [offset 32]
//   - Affinity (uint32): The set of processors on which the thread is allowed to run.  [offset 40]
//   - Win32StartAddr (pointer): Starting address of the function to be executed by this thread. [offset 48]
//   - TebBase (pointer): Thread Environment Block (TEB) base address.                  [offset 56]
//   - SubProcessTag (uint32): Identifies the service if the thread is owned by a service. [offset 64]
//   - BasePriority (uint8): The scheduler priority of the thread.                      [offset 68]
//   - PagePriority (uint8): A memory page priority hint for memory pages accessed by the thread. [offset 69]
//   - IoPriority (uint8): An I/O priority hint for scheduling I/O generated by the thread. [offset 70]
//   - ThreadFlags (uint8): Not used. [offset 71].
//   - ThreadName (uint16[], >v3 only): uint16[] The name of the thread (if set).       [offset 72]
//
// This handler maintains the thread-to-process mapping for context switch tracking.
func (c *Handler) HandleThreadStartRaw(er *etw.EventRecord) error {

	processID, _ := er.GetUint32At(0)
	newThreadID, _ := er.GetUint32At(4)

	// SubProcessTag is at offset 64 on 64-bit systems
	subProcessTag, _ := er.GetUint32At(64)

	// Store thread to process mapping for context switch metrics
	// Pass the event's timestamp to AddThread to prevent state corruption from PID reuse.
	c.stateManager.Threads.AddThread(newThreadID, processID, er.Timestamp(), subProcessTag)

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
//   - ThreadName (string, v4 only): [Patched In] The name of the thread (if set).
//
// This handler cleans up the thread-to-process mapping and records termination metrics.
func (c *Handler) HandleThreadEndRaw(er *etw.EventRecord) error {
	newThreadID, _ := er.GetUint32At(4)

	// Mark the thread for deletion. The actual cleanup will happen after the next scrape.
	c.stateManager.Threads.MarkThreadForDeletion(newThreadID)

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
