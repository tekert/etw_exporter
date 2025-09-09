package kernelmemory

import (
	"strconv"
	"sync/atomic"

	"etw_exporter/internal/config"
	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"
	"etw_exporter/internal/maps"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// processFaultData holds the PID and counter for a process instance.
type processFaultData struct {
	PID     uint32
	Counter *atomic.Uint64
}

// MemCollector implements prometheus.Collector for memory-related metrics.
// This collector is designed for high performance, using lock-free atomics.
type MemCollector struct {
	config *config.MemoryConfig
	log    *phusluadapter.SampledLogger

	hardPageFaultsTotal      *atomic.Uint64
	hardPageFaultsPerProcess maps.ConcurrentMap[uint64, *processFaultData] // key: StartKey

	hardPageFaultsTotalDesc      *prometheus.Desc
	hardPageFaultsPerProcessDesc *prometheus.Desc
}

// NewMemoryCollector creates a new memory metrics collector.
func NewMemoryCollector(config *config.MemoryConfig) *MemCollector {
	c := &MemCollector{
		config:                   config,
		log:                      logger.NewSampledLoggerCtx("memory_collector"),
		hardPageFaultsTotal:      new(atomic.Uint64),
		hardPageFaultsPerProcess: maps.NewConcurrentMap[uint64, *processFaultData](),
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
		float64(c.hardPageFaultsTotal.Load()),
	)

	if c.config.EnablePerProcess {
		stateManager := statemanager.GetGlobalStateManager()
		c.hardPageFaultsPerProcess.Range(func(startKey uint64, data *processFaultData) bool {
			if processName, ok := stateManager.GetKnownProcessName(data.PID); ok {
				ch <- prometheus.MustNewConstMetric(
					c.hardPageFaultsPerProcessDesc,
					prometheus.CounterValue,
					float64(data.Counter.Load()),
					strconv.FormatUint(uint64(data.PID), 10),
					strconv.FormatUint(startKey, 10),
					processName,
				)
			}
			return true
		})
	}
}

// ProcessHardPageFaultEvent increments the hard page fault counters.
func (c *MemCollector) ProcessHardPageFaultEvent(pid uint32, startKey uint64) {
	c.hardPageFaultsTotal.Add(1)

	if c.config.EnablePerProcess && startKey > 0 {
		if statemanager.GetGlobalStateManager().IsKnownProcess(pid) {
			data := c.hardPageFaultsPerProcess.LoadOrStore(startKey, func() *processFaultData {
				return &processFaultData{
					PID:     pid,
					Counter: new(atomic.Uint64),
				}
			})
			data.Counter.Add(1)
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
	for _, startKey := range terminatedProcs {
		if _, deleted := c.hardPageFaultsPerProcess.LoadAndDelete(startKey); deleted {
			cleanedCount++
		}
	}

	if cleanedCount > 0 {
		c.log.Debug().Int("count", cleanedCount).Msg("Cleaned up page fault counters for terminated processes")
	}
}
