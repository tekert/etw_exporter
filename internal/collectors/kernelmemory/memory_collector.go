package kernelmemory

import (
	"strconv"
	"sync"
	"sync/atomic"

	"etw_exporter/internal/config"
	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// MemCollector implements prometheus.Collector for memory-related metrics.
// This collector is designed for high performance, using lock-free atomics.
type MemCollector struct {
	config *config.MemoryConfig
	log    *phusluadapter.SampledLogger

	hardPageFaultsTotal      uint64
	hardPageFaultsPerProcess sync.Map // key: ProcessFaultKey, value: *uint64

	hardPageFaultsTotalDesc      *prometheus.Desc
	hardPageFaultsPerProcessDesc *prometheus.Desc
}

// ProcessFaultKey uniquely identifies a process instance for fault counting.
type ProcessFaultKey struct {
	PID      uint32
	StartKey uint64
}

// NewMemoryCollector creates a new memory metrics collector.
func NewMemoryCollector(config *config.MemoryConfig) *MemCollector {
	c := &MemCollector{
		config: config,
		log:    logger.NewSampledLoggerCtx("memory_collector"),
	}

	c.hardPageFaultsTotalDesc = prometheus.NewDesc(
		"etw_memory_hard_pagefaults_total",
		"Total hard page faults system-wide.",
		nil, nil)

	if config.EnablePerProcess {
		c.hardPageFaultsPerProcessDesc = prometheus.NewDesc(
			"etw_memory_hard_pagefaults_per_process_total",
			"Total hard page faults by process.",
			[]string{"process_id", "process_start_key", "process_name"}, nil)
	}

	// Register for post-scrape cleanup.
	statemanager.GetGlobalStateManager().RegisterCleaner(c)

	return c
}

// Describe implements prometheus.Collector.
func (c *MemCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.hardPageFaultsTotalDesc
	if c.config.EnablePerProcess {
		ch <- c.hardPageFaultsPerProcessDesc
	}
}

// Collect implements prometheus.Collector.
func (c *MemCollector) Collect(ch chan<- prometheus.Metric) {
	ch <- prometheus.MustNewConstMetric(
		c.hardPageFaultsTotalDesc,
		prometheus.CounterValue,
		float64(atomic.LoadUint64(&c.hardPageFaultsTotal)),
	)

	if c.config.EnablePerProcess {
		stateManager := statemanager.GetGlobalStateManager()
		c.hardPageFaultsPerProcess.Range(func(key, val any) bool {
			pKey := key.(ProcessFaultKey)
			pid := pKey.PID
			count := atomic.LoadUint64(val.(*uint64))
			if processName, ok := stateManager.GetKnownProcessName(pid); ok {
				ch <- prometheus.MustNewConstMetric(
					c.hardPageFaultsPerProcessDesc,
					prometheus.CounterValue,
					float64(count),
					strconv.FormatUint(uint64(pid), 10),
					strconv.FormatUint(pKey.StartKey, 10),
					processName,
				)
			}
			return true
		})
	}
}

// ProcessHardPageFaultEvent increments the hard page fault counters.
func (c *MemCollector) ProcessHardPageFaultEvent(pid uint32, startKey uint64) {
	atomic.AddUint64(&c.hardPageFaultsTotal, 1)

	if c.config.EnablePerProcess {
		stateManager := statemanager.GetGlobalStateManager()
		if stateManager.IsKnownProcess(pid) {
			key := ProcessFaultKey{PID: pid, StartKey: startKey}
			val, _ := c.hardPageFaultsPerProcess.LoadOrStore(key, new(uint64))
			atomic.AddUint64(val.(*uint64), 1)
		}
	}
}

// CleanupTerminatedProcesses implements the statemanager.PostScrapeCleaner interface.
// This method is called by the KernelStateManager after a scrape is complete to
// allow the collector to safely clean up its internal state for terminated processes.
func (c *MemCollector) CleanupTerminatedProcesses(terminatedProcs map[uint32]uint64) {
	if !c.config.EnablePerProcess {
		return
	}
	cleanedCount := 0
	c.hardPageFaultsPerProcess.Range(func(key, value any) bool {
		pKey := key.(ProcessFaultKey)
		// Check if the process ID is in the set of terminated processes.
		if termStartKey, isTerminated := terminatedProcs[pKey.PID]; isTerminated {
			// Additionally, ensure the start key matches for correctness. This handles
			// the rare case where a PID is reused before cleanup.
			if pKey.StartKey == termStartKey {
				c.hardPageFaultsPerProcess.Delete(key)
				cleanedCount++
			}
		}
		return true
	})

	if cleanedCount > 0 {
		c.log.Debug().Int("count", cleanedCount).Msg("Cleaned up page fault counters for terminated processes")
	}
}
