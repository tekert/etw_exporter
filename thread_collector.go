package main

import (
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/phuslu/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/tekert/golang-etw/etw"
)

// ThreadCSCollector handles thread events and metrics with low cardinality
type ThreadCSCollector struct {
	lastCpuSwitch   sync.Map        // key: cpu (uint16), value: time.Time
	threadToProcess sync.Map        // key: threadID (uint32), value: processID (uint32)
	processTracker  *ProcessTracker // Reference to the global process tracker
	metrics         *ETWMetrics
	logger          log.Logger // Thread collector logger
}

// NewThreadCSCollector creates a new thread collector instance
func NewThreadCSCollector() *ThreadCSCollector {
	return &ThreadCSCollector{
		processTracker: GetGlobalProcessTracker(),
		metrics:        GetMetrics(),
		logger:         GetThreadLogger(),
	}
}

// HandleContextSwitch processes context switch events (CSwitch)
func (c *ThreadCSCollector) HandleContextSwitch(helper *etw.EventRecordHelper) error {
	// Parse context switch event data according to CSwitch class documentation
	oldThreadID, _ := helper.GetPropertyUint("OldThreadId")
	newThreadID, _ := helper.GetPropertyUint("NewThreadId")
	newThreadPriority, _ := helper.GetPropertyInt("NewThreadPriority")
	oldThreadPriority, _ := helper.GetPropertyInt("OldThreadPriority")
	waitReason, _ := helper.GetPropertyInt("OldThreadWaitReason")
	waitMode, _ := helper.GetPropertyInt("OldThreadWaitMode")

	// Get CPU number from processor index (if available)
	cpu := uint16(helper.EventRec.BufferContext.Processor)

	// Timestamp from the event
	eventTime := helper.EventRec.EventHeader.UTCTimeStamp()

	// Convert to strings for labels
	cpuStr := strconv.FormatUint(uint64(cpu), 10)

	// Track context switches per CPU (low cardinality)
	c.metrics.ContextSwitchesPerCPU.With(prometheus.Labels{
		"cpu": cpuStr,
	}).Inc()

	// Track context switches per process for the NEW thread (only count the incoming process)
	// This avoids double counting and provides a cleaner metric
	if processID, exists := c.threadToProcess.Load(uint32(newThreadID)); exists {
		pid := processID.(uint32)
		processName := c.processTracker.GetProcessName(pid)

		// Only increment if we have a valid process name (not unknown)
		// This prevents duplicate metrics for unknown vs known process names
		if !strings.HasPrefix(processName, "unknown_") {
			c.metrics.ContextSwitchesPerProcess.With(prometheus.Labels{
				"process_id":   strconv.FormatUint(uint64(pid), 10),
				"process_name": processName,
			}).Inc()

			c.logger.Trace().
				Uint32("pid", pid).
				Uint32("thread_id", uint32(newThreadID)).
				Str("process_name", processName).
				Msg("Context switch tracked for known process")
		} else {
			// Log missing process name for debugging - this helps identify timing issues
			c.logger.Trace().
				Uint32("pid", pid).
				Uint32("thread_id", uint32(newThreadID)).
				Str("process_name", processName).
				Msg("Context switch for thread with unknown process name - skipping metric")
		}
	} else {
		// Thread to process mapping missing - log for debugging
		c.logger.Trace().
			Uint32("thread_id", uint32(newThreadID)).
			Msg("Context switch for thread without known process mapping - skipping metric")
	}

	// Calculate context switch interval (time between switches on this CPU)
	if lastCpuSwitchVal, exists := c.lastCpuSwitch.Load(cpu); exists {
		if lastCpuSwitch, ok := lastCpuSwitchVal.(time.Time); ok {
			switchInterval := eventTime.Sub(lastCpuSwitch)

			c.metrics.ContextSwitchInterval.With(prometheus.Labels{
				"cpu": cpuStr,
			}).Observe(switchInterval.Seconds())
		}
	}
	c.lastCpuSwitch.Store(cpu, eventTime)

	// Track thread state transitions (aggregated)
	waitReasonStr := GetWaitReasonString(uint8(waitReason))
	c.metrics.ThreadStatesTotal.With(prometheus.Labels{
		"state":       "waiting",
		"wait_reason": waitReasonStr,
	}).Inc()

	c.metrics.ThreadStatesTotal.With(prometheus.Labels{
		"state":       "running",
		"wait_reason": "none",
	}).Inc()

	// Log additional context switch details (can be removed in production)
	_ = newThreadPriority
	_ = oldThreadPriority
	_ = waitMode
	_ = oldThreadID // We use it for debugging but don't count it

	return nil
}

// HandleReadyThread processes thread ready events
func (c *ThreadCSCollector) HandleReadyThread(helper *etw.EventRecordHelper) error {
	threadID, _ := helper.GetPropertyUint("ThreadId")
	threadPriority, _ := helper.GetPropertyInt("ThreadPriority")
	adjustReason, _ := helper.GetPropertyInt("AdjustReason")
	adjustIncrement, _ := helper.GetPropertyInt("AdjustIncrement")

	// Timestamp from the event (more accurate than time.Now())
	eventTime := helper.EventRec.EventHeader.UTCTimeStamp()

	c.logger.Trace().
		Uint32("thread_id", uint32(threadID)).
		Int64("priority", threadPriority).
		Time("event_time", eventTime).
		Msg("Thread ready event")

	// Track thread state transition to ready (aggregated)
	c.metrics.ThreadStatesTotal.With(prometheus.Labels{
		"state":       "ready",
		"wait_reason": "none",
	}).Inc()

	// Log additional details (can be removed in production)
	_ = adjustReason
	_ = adjustIncrement

	return nil
}

// HandleThreadStart processes thread start events
func (c *ThreadCSCollector) HandleThreadStart(helper *etw.EventRecordHelper) error {
	threadID, _ := helper.GetPropertyUint("TThreadId")
	processID, _ := helper.GetPropertyUint("ProcessId")

	// Store thread to process mapping for context switch metrics
	c.threadToProcess.Store(uint32(threadID), uint32(processID))

	c.logger.Trace().
		Uint32("thread_id", uint32(threadID)).
		Uint32("process_id", uint32(processID)).
		Msg("Thread started - mapping stored")

	// Track thread creation (aggregated)
	c.metrics.ThreadStatesTotal.With(prometheus.Labels{
		"state":       "created",
		"wait_reason": "none",
	}).Inc()

	return nil
}

// HandleThreadEnd processes thread end events
func (c *ThreadCSCollector) HandleThreadEnd(helper *etw.EventRecordHelper) error {
	threadID, _ := helper.GetPropertyUint("TThreadId")

	// Clean up thread to process mapping
	if _, existed := c.threadToProcess.LoadAndDelete(uint32(threadID)); existed {
		c.logger.Trace().
			Uint32("thread_id", uint32(threadID)).
			Msg("Thread ended - mapping removed")
	} else {
		c.logger.Trace().
			Uint32("thread_id", uint32(threadID)).
			Msg("Thread ended - no mapping found to remove")
	}

	// Track thread termination (aggregated)
	c.metrics.ThreadStatesTotal.With(prometheus.Labels{
		"state":       "terminated",
		"wait_reason": "none",
	}).Inc()

	return nil
}

// GetWaitReasonString converts wait reason code to human-readable string
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
