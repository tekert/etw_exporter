package kernelregistry

import (
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"

	"etw_exporter/internal/config"
	"etw_exporter/internal/kernel/statemanager"
)

// RegistryCollector implements prometheus.Collector for registry related metrics.
type RegistryCollector struct {
	operationsTotal        map[OperationKey]*int64
	processOperationsTotal map[ProcessOperationKey]*int64

	mu sync.RWMutex

	operationsTotalDesc        *prometheus.Desc
	processOperationsTotalDesc *prometheus.Desc
	config                     *config.RegistryConfig
}

// OperationKey is a key for system-wide registry operations.
type OperationKey struct {
	Operation string
	Result    string
}

// ProcessOperationKey is a key for per-process registry operations.
type ProcessOperationKey struct {
	ProcessID uint32
	StartKey  uint64
	Operation string
	Result    string
}

// NewRegistryCollector creates a new registry metrics collector.
func NewRegistryCollector(config *config.RegistryConfig) *RegistryCollector {
	c := &RegistryCollector{
		operationsTotal:        make(map[OperationKey]*int64),
		processOperationsTotal: make(map[ProcessOperationKey]*int64),

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
		config: config,
	}

	// Register for post-scrape cleanup.
	statemanager.GetGlobalStateManager().RegisterCleaner(c)

	return c
}

// Describe sends the descriptors of all metrics to the provided channel.
func (c *RegistryCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.operationsTotalDesc
	ch <- c.processOperationsTotalDesc
}

// Collect creates and sends the metrics on each scrape.
func (c *RegistryCollector) Collect(ch chan<- prometheus.Metric) {
	c.mu.RLock()
	defer c.mu.RUnlock()

	stateManager := statemanager.GetGlobalStateManager()

	for key, countPtr := range c.operationsTotal {
		ch <- prometheus.MustNewConstMetric(
			c.operationsTotalDesc,
			prometheus.CounterValue,
			float64(atomic.LoadInt64(countPtr)),
			key.Operation,
			key.Result,
		)
	}

	for key, countPtr := range c.processOperationsTotal {
		// Look up process details during scrape time to avoid overhead in the hot path.
		if processName, isKnown := stateManager.GetKnownProcessName(key.ProcessID); isKnown {
			// Only report metrics for known/tracked processes.
			ch <- prometheus.MustNewConstMetric(
				c.processOperationsTotalDesc,
				prometheus.CounterValue,
				float64(atomic.LoadInt64(countPtr)),
				strconv.FormatUint(uint64(key.ProcessID), 10),
				strconv.FormatUint(key.StartKey, 10),
				processName,
				key.Operation,
				key.Result,
			)
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

	// System-wide metric
	opKey := OperationKey{Operation: operation, Result: result}
	if c.operationsTotal[opKey] == nil {
		c.operationsTotal[opKey] = new(int64)
	}
	atomic.AddInt64(c.operationsTotal[opKey], 1)

	// Per-process metric
	if processID > 0 && c.config.EnablePerProcess {
		stateManager := statemanager.GetGlobalStateManager()
		// First, check if the process is one we are configured to track.
		if stateManager.IsKnownProcess(processID) {
			procOpKey := ProcessOperationKey{
				ProcessID: processID,
				StartKey:  startKey,
				Operation: operation,
				Result:    result,
			}
			if c.processOperationsTotal[procOpKey] == nil {
				c.processOperationsTotal[procOpKey] = new(int64)
			}
			atomic.AddInt64(c.processOperationsTotal[procOpKey], 1)
		}
	}
}

// CleanupTerminatedProcesses implements the statemanager.PostScrapeCleaner interface.
func (c *RegistryCollector) CleanupTerminatedProcesses(terminatedProcs map[uint32]uint64) {
	if !c.config.EnablePerProcess {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()

	for key := range c.processOperationsTotal {
		if _, exists := terminatedProcs[key.ProcessID]; exists {
			delete(c.processOperationsTotal, key)
		}
	}
}
