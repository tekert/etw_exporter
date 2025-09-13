package kernelprocess

import (
	"strconv"

	"etw_exporter/internal/config"
	"etw_exporter/internal/kernel/statemanager"
	"etw_exporter/internal/logger"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// ProcessCollector implements prometheus.Collector for process metadata.
type ProcessCollector struct {
	config *config.ProcessConfig
	sm     *statemanager.KernelStateManager
	log    *phusluadapter.SampledLogger

	// Metric Descriptors
	processInfoDesc *prometheus.Desc
}

// NewProcessCollector creates a new process metadata collector.
func NewProcessCollector(config *config.ProcessConfig, sm *statemanager.KernelStateManager) *ProcessCollector {
	c := &ProcessCollector{
		config: config,
		sm:     sm,
		log:    logger.NewSampledLoggerCtx("process_collector"),

		processInfoDesc: prometheus.NewDesc(
			"etw_process_info",
			"A info metric, providing metadata for a program. The metric's value is 1 as long as at least one instance is running.",
			[]string{"process_name", "image_checksum", "session_id"}, nil,
		),
	}

	// This collector doesn't need to be a cleaner, as it only reads from the state manager.
	// The state manager's own cleanup cycle is sufficient.

	return c
}

// Describe sends the descriptors of all metrics to the provided channel.
func (c *ProcessCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.processInfoDesc
}

// Collect creates and sends the metrics on each scrape.
func (c *ProcessCollector) Collect(ch chan<- prometheus.Metric) {
	stateManager := c.sm

	// Use a map to ensure we only report each unique program once per scrape.
	type programKey struct {
		name      string
		checksum  string
		sessionID string
	}
	reportedPrograms := make(map[programKey]struct{})

	// Iterate over all processes that were active at any point during the scrape interval.
	stateManager.RangeProcesses(func(p *statemanager.ProcessInfo) bool {
		// We clone the process info to get a consistent snapshot.
		procInfo := p.Clone()

		key := programKey{
			name:      procInfo.Name,
			checksum:  "0x" + strconv.FormatUint(uint64(procInfo.ImageChecksum), 16),
			sessionID: strconv.FormatUint(uint64(procInfo.SessionID), 10),
		}

		// If we haven't created an info metric for this program signature yet, create one.
		if _, reported := reportedPrograms[key]; !reported {
			ch <- prometheus.MustNewConstMetric(
				c.processInfoDesc,
				prometheus.GaugeValue,
				1,
				key.name,
				key.checksum,
				key.sessionID,
			)
			reportedPrograms[key] = struct{}{}
		}
		return true // Continue iteration
	})
}
