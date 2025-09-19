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

// Used to collect and aggregate metrics before sending to Prometheus.
type programKey struct {
	name      string
	checksum  uint32
	sessionID uint32
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
	reportedPrograms := make(map[programKey]struct{})

	// Iterate over all processes that were active at any point during the scrape interval.
	stateManager.RangeProcesses(func(p *statemanager.ProcessInfo) bool {
		// We clone the process info to get a consistent snapshot.
		procInfo := p.Clone()

		key := programKey{
			name:      procInfo.Name,
			checksum:  procInfo.ImageChecksum,
			sessionID: procInfo.SessionID,
		}

		// If we haven't created an info metric for this program signature yet, create one.
		if _, reported := reportedPrograms[key]; !reported {
			ch <- prometheus.MustNewConstMetric(
				c.processInfoDesc,
				prometheus.GaugeValue,
				1,
				key.name,
				"0x"+strconv.FormatUint(uint64(key.checksum), 16),
				strconv.FormatUint(uint64(key.sessionID), 10),
			)
			reportedPrograms[key] = struct{}{}
		}
		return true // Continue iteration
	})

	c.log.Debug().Int("unique_programs_reported", len(reportedPrograms)).
		Msg("Collected process info metrics")
}
