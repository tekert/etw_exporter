package kernelregistry

import (
	"strconv"
	"sync"
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
	log    *phusluadapter.SampledLogger
	mu     sync.RWMutex

	// System-wide metrics, keyed by operation and result.
	operationsTotal map[OperationKey]*atomic.Int64

	// Per-process metrics, keyed by the unique process StartKey.
	perProcessMetrics map[uint64]*processRegistryMetrics

	// Metric Descriptors
	operationsTotalDesc        *prometheus.Desc
	processOperationsTotalDesc *prometheus.Desc
}

// OperationKey is a key for system-wide registry operations.
type OperationKey struct {
	Operation string
	Result    string
}

// processRegistryMetrics holds all registry metrics for a single process instance.
type processRegistryMetrics struct {
	PID        uint32
	Operations map[OperationKey]*atomic.Int64
}

// NewRegistryCollector creates a new registry metrics collector.
func NewRegistryCollector(config *config.RegistryConfig) *RegistryCollector {
	c := &RegistryCollector{
		config:            config,
		log:               logger.NewSampledLoggerCtx("registry_collector"),
		operationsTotal:   make(map[OperationKey]*atomic.Int64),
		perProcessMetrics: make(map[uint64]*processRegistryMetrics),

		operationsTotalDesc: prometheus.NewDesc(
			"etw_registry_operations_total",
			"Total number of registry operations.",
			[]string{"operation", "result"}, nil,
		),
		processOperationsTotalDesc: prometheus.NewDesc(
			"etw_registry_operations_process_total",
			"Total number of registry operations per process.",
			[]string{"process_id", "process_start_key", "process_name", "operation", "result"}, nil,
		),
	}

	// Register for post-scrape cleanup.
	statemanager.GetGlobalStateManager().RegisterCleaner(c)

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
	c.mu.RLock()
	defer c.mu.RUnlock()

	stateManager := statemanager.GetGlobalStateManager()

	// Collect system-wide metrics
	for key, countPtr := range c.operationsTotal {
		ch <- prometheus.MustNewConstMetric(
			c.operationsTotalDesc,
			prometheus.CounterValue,
			float64(countPtr.Load()),
			key.Operation,
			key.Result,
		)
	}

	if !c.config.EnablePerProcess {
		return
	}

	// Collect per-process metrics
	for startKey, metrics := range c.perProcessMetrics {
		// Look up process details during scrape time to avoid overhead in the hot path.
		if processName, isKnown := stateManager.GetKnownProcessName(metrics.PID); isKnown {
			pidStr := strconv.FormatUint(uint64(metrics.PID), 10)
			startKeyStr := strconv.FormatUint(startKey, 10)

			for opKey, countPtr := range metrics.Operations {
				ch <- prometheus.MustNewConstMetric(
					c.processOperationsTotalDesc,
					prometheus.CounterValue,
					float64(countPtr.Load()),
					pidStr,
					startKeyStr,
					processName,
					opKey.Operation,
					opKey.Result,
				)
			}
		}
	}
}

// RecordRegistryOperation records a single registry operation.
func (c *RegistryCollector) RecordRegistryOperation(processID uint32, startKey uint64, operation string, status uint32) {
	c.mu.Lock()
	defer c.mu.Unlock()

	result := "success"
	if status != 0 {
		result = "failure"
	}

	opKey := OperationKey{Operation: operation, Result: result}

	// System-wide metric
	if _, ok := c.operationsTotal[opKey]; !ok {
		c.operationsTotal[opKey] = new(atomic.Int64)
	}
	c.operationsTotal[opKey].Add(1)

	// Per-process metric
	if c.config.EnablePerProcess && startKey > 0 {
		stateManager := statemanager.GetGlobalStateManager()
		// First, check if the process is one we are configured to track.
		if stateManager.IsKnownProcess(processID) {
			// Get or create the metrics struct for the process instance (by StartKey).
			procMetrics, ok := c.perProcessMetrics[startKey]
			if !ok {
				procMetrics = &processRegistryMetrics{
					PID:        processID,
					Operations: make(map[OperationKey]*atomic.Int64),
				}
				c.perProcessMetrics[startKey] = procMetrics
			}

			// Get or create the counter for the specific operation.
			if _, ok := procMetrics.Operations[opKey]; !ok {
				procMetrics.Operations[opKey] = new(atomic.Int64)
			}
			procMetrics.Operations[opKey].Add(1)
		}
	}
}

// CleanupTerminatedProcesses implements the statemanager.PostScrapeCleaner interface.
// This is now a highly efficient O(k) operation, where k is the number of terminated processes.
func (c *RegistryCollector) CleanupTerminatedProcesses(terminatedProcs map[uint32]uint64) {
	if !c.config.EnablePerProcess {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	cleanedCount := 0
	for _, startKey := range terminatedProcs {
		if _, exists := c.perProcessMetrics[startKey]; exists {
			delete(c.perProcessMetrics, startKey)
			cleanedCount++
		}
	}

	if cleanedCount > 0 {
		c.log.Debug().Int("count", cleanedCount).Msg("Cleaned up registry counters for terminated processes")
	}
}
