package kernelmemory

import (
	"strconv"
	"sync"
	"sync/atomic"

	"etw_exporter/internal/config"
	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"
	"etw_exporter/internal/maps"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// processFaultData holds the counter for a process instance.
type processFaultData struct {
	Counter *atomic.Uint64
}

// MemCollector implements prometheus.Collector for memory-related metrics.
// This collector is designed for high performance, using lock-free atomics.
type MemCollector struct {
	config       *config.MemoryConfig
	log          *phusluadapter.SampledLogger
	stateManager *statemanager.KernelStateManager

	hardPageFaultsTotal      *atomic.Uint64
	hardPageFaultsPerProcess maps.ConcurrentMap[uint64, *processFaultData] // key: StartKey

	aggregationPool *sync.Pool

	hardPageFaultsTotalDesc      *prometheus.Desc
	hardPageFaultsPerProcessDesc *prometheus.Desc
}

// Used to collect and aggregate metrics before sending to Prometheus.
type programKey struct {
	name      string
	checksum  uint32
	sessionID uint32
}

// NewMemoryCollector creates a new memory metrics collector.
func NewMemoryCollector(config *config.MemoryConfig, sm *statemanager.KernelStateManager) *MemCollector {
	c := &MemCollector{
		config:                   config,
		stateManager:             sm,
		log:                      logger.NewSampledLoggerCtx("memory_collector"),
		hardPageFaultsTotal:      new(atomic.Uint64),
		hardPageFaultsPerProcess: maps.NewConcurrentMap[uint64, *processFaultData](),
	}

	c.aggregationPool = &sync.Pool{
		New: func() any {
			// Pre-size the map assuming a reasonable number of unique processes.
			return make(map[programKey]uint64, 256)
		},
	}

	c.hardPageFaultsTotalDesc = prometheus.NewDesc(
		"etw_memory_hard_pagefaults_total",
		"Total hard page faults system-wide.",
		nil, nil)

	if config.EnablePerProcess {
		c.hardPageFaultsPerProcessDesc = prometheus.NewDesc(
			"etw_memory_hard_pagefaults_per_process_total",
			"Total hard page faults by program.",
			[]string{"process_name", "image_checksum", "session_id"}, nil)
	}

	// Register for post-scrape cleanup.
	sm.RegisterCleaner(c)

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
		// --- Per-Program Metrics (with on-the-fly aggregation) ---
		aggregatedFaults := c.aggregationPool.Get().(map[programKey]uint64)
		defer func() {
			// Clear map for reuse and return to pool.
			for k := range aggregatedFaults {
				delete(aggregatedFaults, k)
			}
			c.aggregationPool.Put(aggregatedFaults)
		}()

		stateManager := c.stateManager

		c.hardPageFaultsPerProcess.Range(func(startKey uint64, data *processFaultData) bool {
			if procInfo, ok := stateManager.GetProcessInfoBySK(startKey); ok {
				key := programKey{
					name:      procInfo.Name,
					checksum:  procInfo.ImageChecksum,
					sessionID: procInfo.SessionID,
				}
				aggregatedFaults[key] += data.Counter.Load()
			}
			return true
		})

		for key, count := range aggregatedFaults {
			ch <- prometheus.MustNewConstMetric(
				c.hardPageFaultsPerProcessDesc,
				prometheus.CounterValue,
				float64(count),
				key.name,
				"0x"+strconv.FormatUint(uint64(key.checksum), 16),
				strconv.FormatUint(uint64(key.sessionID), 10),
			)
		}
	}

	c.log.Debug().Msg("Collected memory metrics")
}

// ProcessHardPageFaultEvent increments the hard page fault counters.
func (c *MemCollector) ProcessHardPageFaultEvent(startKey uint64) {
	c.hardPageFaultsTotal.Add(1)

	if c.config.EnablePerProcess && startKey > 0 {
		if c.stateManager.IsTrackedStartKey(startKey) {
			data, _ := c.hardPageFaultsPerProcess.LoadOrStore(startKey, func() *processFaultData {
				return &processFaultData{
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
func (c *MemCollector) CleanupTerminatedProcesses(terminatedProcs map[uint64]uint32) {
	if !c.config.EnablePerProcess {
		return
	}
	cleanedCount := 0
	for startKey, _ := range terminatedProcs {
		if _, deleted := c.hardPageFaultsPerProcess.LoadAndDelete(startKey); deleted {
			cleanedCount++
		}
	}

	if cleanedCount > 0 {
		c.log.Debug().Int("count", cleanedCount).Msg("Cleaned up page fault counters for terminated processes")
	}
}
