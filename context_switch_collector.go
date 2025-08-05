package main

import (
	"strconv"
	"sync"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/tekert/golang-etw/etw"
)

// ContextSwitchCollector handles context switch events and metrics
type ContextSwitchCollector struct {
	lastSwitchTime sync.Map // key: threadID (uint32), value: time.Time
	metrics        *ETWMetrics
}

// NewContextSwitchCollector creates a new context switch collector instance
func NewContextSwitchCollector(metrics *ETWMetrics) *ContextSwitchCollector {
	return &ContextSwitchCollector{
		metrics: metrics,
	}
}

// HandleContextSwitch processes context switch events (CSwitch)
func (c *ContextSwitchCollector) HandleContextSwitch(helper *etw.EventRecordHelper) error {
	// Parse context switch event data according to CSwitch class documentation

	oldThreadID, _ := helper.GetPropertyUint("OldThreadId")
	newThreadID, _ := helper.GetPropertyUint("NewThreadId")
	newThreadPriority, _ := helper.GetPropertyInt("NewThreadPriority")
	oldThreadPriority, _ := helper.GetPropertyInt("OldThreadPriority")
	waitReason, _ := helper.GetPropertyInt("OldThreadWaitReason")
	waitMode, _ := helper.GetPropertyInt("OldThreadWaitMode")

	// Get CPU number from processor index (if available)
	cpu := uint16(helper.EventRec.BufferContext.Processor)

	now := time.Now()

	// Calculate context switch latency if we have previous switch time for the old thread
	if lastTime, exists := c.lastSwitchTime.Load(uint32(oldThreadID)); exists {
		if prevTime, ok := lastTime.(time.Time); ok {
			latency := now.Sub(prevTime).Seconds()
			c.metrics.ContextSwitchLatency.With(prometheus.Labels{
				"cpu": strconv.FormatUint(uint64(cpu), 10),
			}).Observe(latency)
		}
	}

	// Update last switch time for new thread
	c.lastSwitchTime.Store(uint32(newThreadID), now)

	// Increment context switch counter
	c.metrics.ContextSwitches.With(prometheus.Labels{
		"old_thread_id": strconv.FormatUint(oldThreadID, 10),
		"new_thread_id": strconv.FormatUint(newThreadID, 10),
		"cpu":           strconv.FormatUint(uint64(cpu), 10),
	}).Inc()

	// Log additional context switch details (can be removed in production)
	_ = newThreadPriority
	_ = oldThreadPriority
	_ = waitReason
	_ = waitMode

	now = time.Now()

	// Calculate context switch latency if we have previous switch time for the old thread
	if lastTime, exists := c.lastSwitchTime.Load(uint32(oldThreadID)); exists {
		if prevTime, ok := lastTime.(time.Time); ok {
			latency := now.Sub(prevTime).Seconds()
			c.metrics.ContextSwitchLatency.With(prometheus.Labels{
				"cpu": strconv.FormatUint(uint64(cpu), 10),
			}).Observe(latency)
		}
	}

	// Update last switch time for new thread
	c.lastSwitchTime.Store(uint32(newThreadID), now)

	// Increment context switch counter
	c.metrics.ContextSwitches.With(prometheus.Labels{
		"old_thread_id": strconv.FormatUint(oldThreadID, 10),
		"new_thread_id": strconv.FormatUint(newThreadID, 10),
		"cpu":           strconv.FormatUint(uint64(cpu), 10),
	}).Inc()

	return nil
}

// HandleThreadStart processes thread start events
func (c *ContextSwitchCollector) HandleThreadStart(helper *etw.EventRecordHelper) error {
	if err := helper.ParseProperties("TThreadId", "ProcessId"); err != nil {
		return err
	}

	threadID, _ := helper.GetPropertyUint("TThreadId")
	processID, _ := helper.GetPropertyUint("ProcessId")

	// Initialize thread tracking
	c.lastSwitchTime.Store(uint32(threadID), time.Now())

	// Could add thread creation metrics here if needed
	_ = processID // Use processID if needed for future metrics

	return nil
}

// HandleThreadEnd processes thread end events
func (c *ContextSwitchCollector) HandleThreadEnd(helper *etw.EventRecordHelper) error {
	if err := helper.ParseProperties("TThreadId"); err != nil {
		return err
	}

	threadID, _ := helper.GetPropertyUint("TThreadId")

	// Clean up thread tracking
	c.lastSwitchTime.Delete(uint32(threadID))

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
