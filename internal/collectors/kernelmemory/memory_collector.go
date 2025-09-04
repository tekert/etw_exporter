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
	hardPageFaultsPerProcess sync.Map // key: uint32 (PID), value: *uint64

	hardPageFaultsTotalDesc      *prometheus.Desc
	hardPageFaultsPerProcessDesc *prometheus.Desc
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
			[]string{"process_id", "process_name"}, nil)
	}

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
			pid := key.(uint32)
			count := atomic.LoadUint64(val.(*uint64))
			if processName, ok := stateManager.GetKnownProcessName(pid); ok {
				ch <- prometheus.MustNewConstMetric(
					c.hardPageFaultsPerProcessDesc,
					prometheus.CounterValue,
					float64(count),
					strconv.FormatUint(uint64(pid), 10),
					processName,
				)
			}
			return true
		})
	}
}

// ProcessHardPageFaultEvent increments the hard page fault counters.
func (c *MemCollector) ProcessHardPageFaultEvent(pid uint32) {
	atomic.AddUint64(&c.hardPageFaultsTotal, 1)

	if c.config.EnablePerProcess {
		stateManager := statemanager.GetGlobalStateManager()
		if stateManager.IsKnownProcess(pid) {
			val, _ := c.hardPageFaultsPerProcess.LoadOrStore(pid, new(uint64))
			atomic.AddUint64(val.(*uint64), 1)
		}
	}
}
