package kernelmemory

import (
	"strconv"
	"sync/atomic"

	"etw_exporter/internal/config"
	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// MemCollector implements prometheus.Collector for memory-related metrics.
// This collector is designed for high performance, using lock-free atomics for
// system-wide metrics and reading pre-aggregated data from the KernelStateManager
// for per-process metrics.
type MemCollector struct {
	config       *config.MemoryConfig
	log          *phusluadapter.SampledLogger
	stateManager *statemanager.KernelStateManager

	hardPageFaultsTotal *atomic.Uint64
	// Per-process data is now fully managed by the KernelStateManager.
	// The hardPageFaultsPerProcess map has been removed.

	hardPageFaultsTotalDesc      *prometheus.Desc
	hardPageFaultsPerProcessDesc *prometheus.Desc
}

// NewMemoryCollector creates a new memory metrics collector.
func NewMemoryCollector(config *config.MemoryConfig, sm *statemanager.KernelStateManager) *MemCollector {
	c := &MemCollector{
		config:              config,
		stateManager:        sm,
		log:                 logger.NewSampledLoggerCtx("memory_collector"),
		hardPageFaultsTotal: new(atomic.Uint64),
	}

	c.hardPageFaultsTotalDesc = prometheus.NewDesc(
		"etw_memory_hard_pagefaults_total",
		"Total hard page faults system-wide.",
		nil, nil)

	if config.EnablePerProcess {
		c.hardPageFaultsPerProcessDesc = prometheus.NewDesc(
			"etw_memory_hard_pagefaults_per_process_total",
			"Total hard page faults by program.",
			[]string{"process_name", "service_name", "pe_checksum", "session_id"}, nil)
	}

	// The collector no longer manages instance state, so it doesn't need to be a cleaner.

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
		// --- Per-Program Metrics (reading from pre-aggregated state) ---
		c.stateManager.RangeAggregatedMetrics(func(key statemanager.ProgramAggregationKey,
			metrics *statemanager.AggregatedProgramMetrics) bool {

			// Check if this program has any memory metrics to report.
			if metrics.Memory == nil || metrics.Memory.HardPageFaults == 0 {
				return true // Continue to next program
			}

			imageChecksumHex := "0x" + strconv.FormatUint(uint64(key.PeChecksum), 16)
			sessionIDStr := strconv.FormatUint(uint64(key.SessionID), 10)
			ch <- prometheus.MustNewConstMetric(
				c.hardPageFaultsPerProcessDesc,
				prometheus.CounterValue,
				float64(metrics.Memory.HardPageFaults),
				key.Name,
				key.ServiceName,
				imageChecksumHex,
				sessionIDStr,
			)
			return true // Continue iteration
		})
	}

	c.log.Debug().Msg("Collected memory metrics")
}

// // ProcessHardPageFaultEvent increments the hard page fault counters for a known process.
// func (c *MemCollector) ProcessHardPageFaultEvent(startKey uint64) {
// 	c.hardPageFaultsTotal.Add(1)

// 	if c.config.EnablePerProcess {
// 		// Delegate to the new centralized state manager logic.
// 		if pData, ok := c.stateManager.GetProcessDataBySK(startKey); ok {
// 			pData.RecordHardPageFault()
// 		} else {
// 			//c.log.SampledWarn("memory_collect_known").
// 			c.log.Error().
// 				Uint64("start_key", startKey).
// 				Msg("Failed to find process data for hard page fault event")
// 		}
// 	}
// }

// // ProcessPendingHardPageFaultEvent increments the hard page fault counters for a
// // process that is not yet known to the state manager.
// func (c *MemCollector) ProcessPendingHardPageFaultEvent(pid uint32) {
// 	c.hardPageFaultsTotal.Add(1)

// 	if c.config.EnablePerProcess {
// 		c.stateManager.RecordPendingHardPageFault(pid)
// 	}
// }

// IncrementTotalHardPageFaults is called by the handler for every hard fault event.
// It increments the global, system-wide counter.
func (c *MemCollector) IncrementTotalHardPageFaults() {
	c.hardPageFaultsTotal.Add(1)
}

// IsPerProcessEnabled is a helper for the handler to quickly check if it needs
// to extract per-process data, avoiding unnecessary work if the feature is disabled.
func (c *MemCollector) IsPerProcessEnabled() bool {
	return c.config.EnablePerProcess
}
