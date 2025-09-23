package kernelregistry

import (
	"strconv"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"

	"etw_exporter/internal/config"
	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"

	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// RegistryCollector implements prometheus.Collector for registry related metrics.
// It uses a single RWMutex to protect its internal maps, which is suitable for its
// moderate event throughput.
type RegistryCollector struct {
	config *config.RegistryConfig
	sm     *statemanager.KernelStateManager
	log    *phusluadapter.SampledLogger

	// System-wide metrics are now stored in a pre-allocated array for lock-free
	// and allocation-free writes on the hot path, mirroring the per-process design.
	// Index = (opcode * resultMax) + result
	operationsTotal [statemanager.MaxRegistryArray]atomic.Uint64

	// Per-process metrics are now managed and aggregated by the KernelStateManager.

	// Metric Descriptors
	operationsTotalDesc        *prometheus.Desc
	processOperationsTotalDesc *prometheus.Desc
}

// NewRegistryCollector creates a new registry metrics collector.
func NewRegistryCollector(config *config.RegistryConfig, sm *statemanager.KernelStateManager) *RegistryCollector {
	c := &RegistryCollector{
		sm:     sm,
		config: config,
		log:    logger.NewSampledLoggerCtx("registry_collector"),
		// The operationsTotal array is zero-initialized by default.

		operationsTotalDesc: prometheus.NewDesc(
			"etw_registry_operations_total",
			"Total number of registry operations.",
			[]string{"operation", "result"}, nil,
		),
		processOperationsTotalDesc: prometheus.NewDesc(
			"etw_registry_operations_process_total",
			"Total number of registry operations per program.",
			[]string{"process_name", "image_checksum", "session_id", "operation", "result"}, nil,
		),
	}

	// The collector no longer manages its own state, so it does not need to
	// register as a PostScrapeCleaner.

	return c
}

// Describe sends the descriptors of all metrics to the provided channel.
func (c *RegistryCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.operationsTotalDesc
	if c.config.EnablePerProcess {
		ch <- c.processOperationsTotalDesc
	}
}

// Collect creates and sends the metrics on each scrape.
func (c *RegistryCollector) Collect(ch chan<- prometheus.Metric) {
	// Collect system-wide metrics
	for i := range &c.operationsTotal {
		count := c.operationsTotal[i].Load()
		if count > 0 {
			opcode := uint8(i / int(statemanager.ResultMax))
			result := statemanager.RegistryResult(i % int(statemanager.ResultMax))

			opString := statemanager.RegistryOpcodeToString[opcode]
			if opString == "" {
				opString = "unknown"
			}
			resultStr := "success"
			if result == statemanager.ResultFailure {
				resultStr = "failure"
			}

			ch <- prometheus.MustNewConstMetric(
				c.operationsTotalDesc,
				prometheus.CounterValue,
				float64(count),
				opString,
				resultStr,
			)
		}
	}

	if !c.config.EnablePerProcess {
		c.log.Debug().Msg("Collected registry metrics")
		return
	}

	// --- Per-Program Metrics (reading from pre-aggregated state) ---
	c.sm.RangeAggregatedMetrics(func(key statemanager.ProgramAggregationKey,
		metrics *statemanager.AggregatedProgramMetrics) bool {

		if metrics.Registry == nil {
			return true // Continue
		}

		checksumStr := "0x" + strconv.FormatUint(uint64(key.ImageChecksum), 16)
		sessionIDStr := strconv.FormatUint(uint64(key.SessionID), 10)

		for opKey, count := range metrics.Registry.Operations {
			opString := statemanager.RegistryOpcodeToString[opKey.Opcode]
			if opString == "" {
				opString = "unknown"
			}
			result := "success"
			if opKey.Result == statemanager.ResultFailure {
				result = "failure"
			}

			ch <- prometheus.MustNewConstMetric(
				c.processOperationsTotalDesc,
				prometheus.CounterValue,
				float64(count),
				key.Name,
				checksumStr,
				sessionIDStr,
				opString,
				result,
			)
		}
		return true // Continue
	})

	c.log.Debug().Msg("Collected registry metrics")
}

// RecordRegistryOperation records a single registry operation.
func (c *RegistryCollector) RecordRegistryOperation(startKey uint64, opcode uint8, status uint32) {
	result := statemanager.ResultSuccess
	if status != 0 {
		result = statemanager.ResultFailure
	}

	// --- System-wide metric (now lock-free and allocation-free) ---
	index := int(opcode)*int(statemanager.ResultMax) + int(result)
	c.operationsTotal[index].Add(1)

	// --- Per-process metric ---
	if c.config.EnablePerProcess && startKey > 0 {
		// Delegate to the new centralized state manager logic.
		if pData, ok := c.sm.GetProcessDataBySK(startKey); ok {
			pData.RecordRegistryOperation(opcode, status)
		}
	}
}
