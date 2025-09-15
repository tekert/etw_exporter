package kernelregistry

import (
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"

	"etw_exporter/internal/config"
	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"
	"etw_exporter/internal/maps"

	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// RegistryCollector implements prometheus.Collector for registry related metrics.
// It uses a single RWMutex to protect its internal maps, which is suitable for its
// moderate event throughput.
type RegistryCollector struct {
	config *config.RegistryConfig
	sm     *statemanager.KernelStateManager
	log    *phusluadapter.SampledLogger
	mu     sync.RWMutex

	// System-wide metrics, keyed by operation and result.
	operationsTotal map[OperationKey]*atomic.Int64

	// Per-process metrics, keyed by the unique process StartKey.
	perProcessMetrics maps.ConcurrentMap[uint64, *processRegistryMetrics]

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
	mu         sync.RWMutex // Protects the Operations map
	Operations map[OperationKey]*atomic.Int64
}

// NewRegistryCollector creates a new registry metrics collector.
func NewRegistryCollector(config *config.RegistryConfig, sm *statemanager.KernelStateManager) *RegistryCollector {
	c := &RegistryCollector{
		sm:                sm,
		config:            config,
		log:               logger.NewSampledLoggerCtx("registry_collector"),
		operationsTotal:   make(map[OperationKey]*atomic.Int64),
		perProcessMetrics: maps.NewConcurrentMap[uint64, *processRegistryMetrics](),

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

	// Register for post-scrape cleanup.
	sm.RegisterCleaner(c)

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
	stateManager := c.sm

	c.mu.RLock()
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
	c.mu.RUnlock()

	if !c.config.EnablePerProcess {
		return
	}

	// --- Per-Program Metrics (with on-the-fly aggregation) ---
	type aggregationKey struct {
		processName   string
		imageChecksum string
		sessionID     string
		operation     string
		result        string
	}
	aggregatedMetrics := make(map[aggregationKey]int64)

	c.perProcessMetrics.Range(func(startKey uint64, metrics *processRegistryMetrics) bool {
		if procInfo, ok := stateManager.GetProcessInfoBySK(startKey); ok {
			metrics.mu.RLock() // Lock for reading the map
			for opKey, countPtr := range metrics.Operations {
				key := aggregationKey{
					processName:   procInfo.Name,
					imageChecksum: "0x" + strconv.FormatUint(uint64(procInfo.ImageChecksum), 16),
					sessionID:     strconv.FormatUint(uint64(procInfo.SessionID), 10),
					operation:     opKey.Operation,
					result:        opKey.Result,
				}
				aggregatedMetrics[key] += countPtr.Load()
			}
			metrics.mu.RUnlock() // Unlock after reading
		}
		return true
	})

	for key, count := range aggregatedMetrics {
		ch <- prometheus.MustNewConstMetric(
			c.processOperationsTotalDesc,
			prometheus.CounterValue,
			float64(count),
			key.processName,
			key.imageChecksum,
			key.sessionID,
			key.operation,
			key.result,
		)
	}
}

// RecordRegistryOperation records a single registry operation.
func (c *RegistryCollector) RecordRegistryOperation(startKey uint64, operation string, status uint32) {
	result := "success"
	if status != 0 {
		result = "failure"
	}

	opKey := OperationKey{Operation: operation, Result: result}

	// System-wide metric
	c.mu.Lock()
	if _, ok := c.operationsTotal[opKey]; !ok {
		c.operationsTotal[opKey] = new(atomic.Int64)
	}
	c.operationsTotal[opKey].Add(1)
	c.mu.Unlock()

	// Per-process metric
	if c.config.EnablePerProcess && startKey > 0 {
		// First, check if the process is one we are configured to track.
		if c.sm.IsTrackedStartKey(startKey) {
			// Get or create the metrics struct for the process instance (by StartKey).
			procMetrics, _ := c.perProcessMetrics.LoadOrStore(startKey, func() *processRegistryMetrics {
				return &processRegistryMetrics{
					Operations: make(map[OperationKey]*atomic.Int64),
				}
			})

			// Lock before accessing the inner map to prevent a race condition.
			procMetrics.mu.Lock()
			// Get or create the counter for the specific operation.
			if _, ok := procMetrics.Operations[opKey]; !ok {
				procMetrics.Operations[opKey] = new(atomic.Int64)
			}
			procMetrics.Operations[opKey].Add(1)
			procMetrics.mu.Unlock()
        }
    }
}

// CleanupTerminatedProcesses implements the statemanager.PostScrapeCleaner interface.
// This is now a highly efficient O(k) operation, where k is the number of terminated processes.
func (c *RegistryCollector) CleanupTerminatedProcesses(terminatedProcs map[uint64]uint32) {
	if !c.config.EnablePerProcess {
		return
	}

	cleanedCount := 0
	for startKey := range terminatedProcs {
		if _, deleted := c.perProcessMetrics.LoadAndDelete(startKey); deleted {
			cleanedCount++
		}
	}

	if cleanedCount > 0 {
		c.log.Debug().Int("count", cleanedCount).Msg("Cleaned up registry counters for terminated processes")
	}
}
