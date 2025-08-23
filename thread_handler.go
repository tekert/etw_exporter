package main

import (
	"runtime"
	"sync"
	"sync/atomic"
	"time"

	"github.com/phuslu/log"
	"github.com/tekert/goetw/etw"
)

// ThreadHandler handles thread events and metrics with low cardinality.
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
type ThreadHandler struct {
	lastCpuSwitch   []atomic.Int64     // stores the last context switch timestamp (in ns) for each CPU
	threadToProcess sync.Map           // key: threadID (uint32), value: processID (uint32)
	customCollector *ThreadCSCollector // High-performance custom collector
	log             log.Logger         // Thread collector logger
}

// NewThreadHandler creates a new thread collector instance with custom collector integration.
// The custom collector provides high-performance metrics aggregation
func NewThreadHandler() *ThreadHandler {
	return &ThreadHandler{
		lastCpuSwitch:   make([]atomic.Int64, runtime.NumCPU()),
		customCollector: NewThreadCSCollector(),
		log:             GetThreadLogger(),
	}
}

// HandleContextSwitchRaw processes context switch events directly from the EVENT_RECORD.
// This is a high-performance "fast path" that reads directly from the UserData
// buffer using known offsets, bypassing the creation of an EventRecordHelper and
// the associated parsing overhead.
//
// CSwitch MOF Layout (V2-V4):
// - NewThreadId (uint32): offset 0
// - OldThreadId (uint32): offset 4
// - NewThreadPriority (int8): offset 8
// - OldThreadPriority (int8): offset 9
// - PreviousCState (uint8): offset 10
// - SpareByte (int8): offset 11
// - OldThreadWaitReason (int8): offset 12
// - OldThreadWaitMode (int8): offset 13
// - OldThreadState (int8): offset 14
// - OldThreadWaitIdealProcessor (int8): offset 15
// - NewThreadWaitTime (uint32): offset 16
// - Reserved (uint32): offset 20
func (c *ThreadHandler) HandleContextSwitchRaw(er *etw.EventRecord) error {
	// Read properties using direct offsets.
	newThreadID, err := er.GetUint32At(0)
	if err != nil {
		// TODO: import sampler
		c.log.Error().Err(err).Msg("Failed to read NewThreadId from raw CSwitch event")
		return err
	}
	waitReason, err := er.GetInt8At(12)
	if err != nil {
		// TODO: import sampler
		c.log.Error().Err(err).Msg("Failed to read OldThreadWaitReason from raw CSwitch event")
		return err
	}

	// Get CPU number from processor index
	cpu := er.ProcessorNumber()

	// TODO: make the correct timestamp available to events.
	// Timestamp from the event header. We must convert it from FILETIME.
	eventTimeNano := etw.FromFiletime(er.EventHeader.TimeStamp).UnixNano()

	// Calculate context switch interval using atomic operations.
	var interval time.Duration
	if lastSwitchNano := c.lastCpuSwitch[cpu].Swap(eventTimeNano); lastSwitchNano > 0 {
		interval = time.Duration(eventTimeNano - lastSwitchNano)
	}

	// Get process ID for the NEW thread (incoming thread)
	var processID uint32
	if pid, exists := c.threadToProcess.Load(newThreadID); exists {
		processID = pid.(uint32)
	}

	// Record context switch in custom collector
	c.customCollector.RecordContextSwitch(cpu, newThreadID, processID, interval)

	// Track thread state transitions
	waitReasonStr := GetWaitReasonString(uint8(waitReason))
	c.customCollector.RecordThreadStateTransition("waiting", waitReasonStr)
	c.customCollector.RecordThreadStateTransition("running", "none")

	return nil
}

// HandleContextSwitch processes context switch events (CSwitch).
// This handler processes ETW events from the kernel thread provider for context switches,
// which occur when the Windows scheduler switches execution from one thread to another.
//
// ETW Event Details:
// - Provider: NT Kernel Logger (CSwitch)
// - GUID: {3d6fa8d1-fe05-11d0-9dda-00c04fd7ba7c}
// - Event Type: 36
// - Event Class: CSwitch_V2, CSwitch_V3, CSwitch_V4 (varies by Windows version)
//
// MOF Class Definition (from gen_mof_kerneldef.go):
// CSwitch event properties:
// - NewThreadId (uint32): Thread ID being switched to (format: hex)
// - OldThreadId (uint32): Thread ID being switched from (format: hex)
// - NewThreadPriority (int8): Priority of incoming thread
// - OldThreadPriority (int8): Priority of outgoing thread
// - PreviousCState (uint8): Previous C-state of the processor
// - SpareByte (int8): Reserved/spare byte
// - OldThreadWaitReason (int8): Why the old thread was waiting
// - OldThreadWaitMode (int8): Wait mode of old thread
// - OldThreadState (int8): State of the old thread
// - OldThreadWaitIdealProcessor (int8): Ideal processor for old thread
// - NewThreadWaitTime (uint32): Wait time for new thread (format: hex)
// - Reserved (uint32): Reserved field
//
// This handler tracks context switches per CPU and per process, calculates
// context switch intervals, and records thread state transitions.
func (c *ThreadHandler) HandleContextSwitch(helper *etw.EventRecordHelper) error {
	// This is now the fallback/slower path. The primary logic has been moved to
	// HandleContextSwitchRaw for performance. This function will not be called
	// for CSwitch events due to the routing logic in EventRecordCallback.

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
	var processID uint32
	if pid, exists := c.threadToProcess.Load(uint32(newThreadID)); exists {
		processID = pid.(uint32)
	}

	// Record context switch in custom collector (handles process name lookup internally)
	c.customCollector.RecordContextSwitch(cpu, uint32(newThreadID), processID, interval)

	// Track thread state transitions
	waitReasonStr := GetWaitReasonString(uint8(waitReason))
	c.customCollector.RecordThreadStateTransition("waiting", waitReasonStr)
	c.customCollector.RecordThreadStateTransition("running", "none")

	return nil
}

// HandleReadyThread processes thread ready events.
// This handler processes ETW events when a thread becomes ready for execution
// (transitions from a waiting state to ready state).
//
// ETW Event Details:
// - Provider: NT Kernel Logger (CSwitch)
// - GUID: {3d6fa8d1-fe05-11d0-9dda-00c04fd7ba7c}
// - Event Type: 50
// - Event Class: ReadyThread
//
// MOF Class Definition (from gen_mof_kerneldef.go):
// ReadyThread event properties:
// - TThreadId (uint32): Thread ID becoming ready (format: hex)
// - AdjustReason (int8): Reason for any priority adjustment
// - AdjustIncrement (int8): Amount of priority adjustment
// - Flag (int8): Additional state flags
// - Reserved (int8): Reserved field
//
// This event indicates a thread is transitioning from a wait state to ready state,
// meaning it's eligible for scheduling but not yet running.
func (c *ThreadHandler) HandleReadyThread(helper *etw.EventRecordHelper) error {
	// Track thread state transition to ready
	c.customCollector.RecordThreadStateTransition("ready", "none")
	return nil
}

// HandleThreadStart processes thread start events.
// This handler processes ETW events when a new thread is created and started
// within a process.
//
// ETW Event Details:
// - Provider: NT Kernel Logger (CSwitch)
// - GUID: {3d6fa8d1-fe05-11d0-9dda-00c04fd7ba7c}
// - Event Types: 1, 3 (Thread start events)
// - Event Class: Thread_V2_TypeGroup1, Thread_V3_TypeGroup1, Thread_TypeGroup1
//
// MOF Class Definition:
// Thread start event properties:
// - ProcessId (uint32): Process ID that owns the thread (format: hex)
// - TThreadId (uint32): Thread ID of the new thread (format: hex)
// - StackBase (pointer): Base address of the thread stack
// - StackLimit (pointer): Limit address of the thread stack
// - UserStackBase (pointer): User-mode stack base address
// - UserStackLimit (pointer): User-mode stack limit address
// - StartAddr (pointer): Thread start address
// - Win32StartAddr (pointer): Win32 start address for the thread
// - TebBase (pointer): Thread Environment Block base address
// - SubProcessTag (uint32): Sub-process tag (format: hex)
// - BasePriority (uint8): Base priority of the thread
// - PagePriority (uint8): Page priority
// - IoPriority (uint8): I/O priority
// - ThreadFlags (uint8): Thread creation flags
// - ThreadName (string): Thread name (if available, V4+ only)
//
// This handler maintains the thread-to-process mapping for context switch tracking.
func (c *ThreadHandler) HandleThreadStart(helper *etw.EventRecordHelper) error {
	threadID, _ := helper.GetPropertyUint("TThreadId")
	processID, _ := helper.GetPropertyUint("ProcessId")

	// Store thread to process mapping for context switch metrics
	c.threadToProcess.Store(uint32(threadID), uint32(processID))

	// Track thread creation
	c.customCollector.RecordThreadCreation()

	return nil
}

// HandleThreadEnd processes thread end events.
// This handler processes ETW events when a thread terminates and is cleaned up.
//
// ETW Event Details:
// - Provider: NT Kernel Logger (CSwitch)
// - GUID: {3d6fa8d1-fe05-11d0-9dda-00c04fd7ba7c}
// - Event Types: 2, 4 (Thread end events)
// - Event Class: Thread_V2_TypeGroup1, Thread_V3_TypeGroup1, Thread_TypeGroup1
//
// MOF Class Definition:
// Thread end events have the same structure as start events but indicate termination.
// Key properties used:
// - TThreadId (uint32): Thread ID being terminated (format: hex)
// - ProcessId (uint32): Process ID that owned the thread (format: hex)
//
// This handler cleans up the thread-to-process mapping and records termination metrics.
func (c *ThreadHandler) HandleThreadEnd(helper *etw.EventRecordHelper) error {
	threadID, _ := helper.GetPropertyUint("TThreadId")

	// Clean up thread to process mapping
	c.threadToProcess.Delete(uint32(threadID))

	// Track thread termination
	c.customCollector.RecordThreadTermination()

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
func GetWaitReasonString(waitReason uint8) string {
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
		return "Spare2"
	case 22:
		return "Spare3"
	case 23:
		return "Spare4"
	case 24:
		return "Spare5"
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
		"WrPageOut", "WrRendezvous", "WrCalloutStack", "WrKernel", "WrResource",
		"WrPushLock", "WrMutex", "WrQuantumEnd", "WrDispatchInt", "WrPreempted",
		"WrYieldExecution", "WrFastMutex", "WrGuardedMutex", "WrRundown",
	}
}

// GetCustomCollector returns the custom Prometheus collector for thread metrics.
// This method provides access to the ThreadMetricsCustomCollector for registration
// with the Prometheus registry, enabling high-performance metric collection.
//
// Usage Example:
//
//	threadCollector := NewThreadCSCollector()
//	prometheus.MustRegister(threadCollector.GetCustomCollector())
//
// The custom collector follows Prometheus best practices:
// - Implements prometheus.Collector interface
// - Uses atomic operations for high-frequency updates
// - Provides thread-safe metric aggregation
// - Maintains low cardinality for performance
func (c *ThreadHandler) GetCustomCollector() *ThreadCSCollector {
	return c.customCollector
}
