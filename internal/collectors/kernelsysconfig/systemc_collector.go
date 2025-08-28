package kernelsysconfig

import (
	"strconv"
	"sync"

	"github.com/phuslu/log"
	"github.com/prometheus/client_golang/prometheus"

	"etw_exporter/internal/logger"
)

// Metric names exported by the SystemConfigCollector.
const (
	PhysicalDiskInfoMetricName = "etw_system_disk_physical_info"
	LogicalDiskInfoMetricName  = "etw_system_disk_logical_info"
)

// PhysicalDiskInfo holds static physical disk information.
type PhysicalDiskInfo struct {
	DiskNumber   uint32
	Manufacturer string
}

// LogicalDiskInfo holds static logical disk information.
type LogicalDiskInfo struct {
	DiskNumber        uint32
	DriveLetterString string
	FileSystem        string
}

// SystemConfigCollector stores static system configuration data collected from ETW.
// It acts as a singleton global repository for this information and a Prometheus collector.
type SystemConfigCollector struct {
	physicalDisks map[uint32]PhysicalDiskInfo // diskNumber -> disk info
	logicalDisks  map[uint32]LogicalDiskInfo  // diskNumber -> logical disk info

	mu  sync.RWMutex
	log log.Logger

	// Metric Descriptors
	physicalDiskInfoDesc *prometheus.Desc
	logicalDiskInfoDesc  *prometheus.Desc

	// Controls which metrics are exposed via the Collector interface
	requestedMetrics map[string]bool
	registerOnce     sync.Once
}

var (
	globalSystemConfigCollector *SystemConfigCollector
	systemConfigOnce            sync.Once
)

// GetGlobalSystemConfigCollector returns the singleton instance of the SystemConfigCollector.
func GetGlobalSystemConfigCollector() *SystemConfigCollector {
	systemConfigOnce.Do(func() {
		globalSystemConfigCollector = &SystemConfigCollector{
			physicalDisks:    make(map[uint32]PhysicalDiskInfo),
			logicalDisks:     make(map[uint32]LogicalDiskInfo),
			log:              logger.NewLoggerWithContext("system_config_collector"),
			requestedMetrics: make(map[string]bool),

			physicalDiskInfoDesc: prometheus.NewDesc(
				PhysicalDiskInfoMetricName,
				"Physical disk information",
				[]string{"disk", "manufacturer"}, nil,
			),
			logicalDiskInfoDesc: prometheus.NewDesc(
				LogicalDiskInfoMetricName,
				"Logical disk information",
				[]string{"disk", "drive_letter", "file_system"}, nil,
			),
		}
	})
	return globalSystemConfigCollector
}

// RequestMetrics allows other collectors to request that specific metrics be exposed.
// This method is thread-safe and ensures the collector is registered with Prometheus
// exactly once when the first metric is requested.
func (sc *SystemConfigCollector) RequestMetrics(metricNames ...string) {
	sc.mu.Lock()

	metricsWereRequested := false
	for _, name := range metricNames {
		var desc *prometheus.Desc
		switch name {
		case PhysicalDiskInfoMetricName:
			desc = sc.physicalDiskInfoDesc
		case LogicalDiskInfoMetricName:
			desc = sc.logicalDiskInfoDesc
		default:
			sc.log.Warn().Str("metric", name).Msg("Unknown system config metric requested.")
			continue
		}

		key := desc.String()
		if !sc.requestedMetrics[key] {
			sc.requestedMetrics[key] = true
			metricsWereRequested = true
			sc.log.Info().Str("metric", name).Msg("System config metric has been requested.")
		}
	}

	needRegister := metricsWereRequested && len(sc.requestedMetrics) > 0
	sc.mu.Unlock()

	// If any new metrics were requested, ensure the collector is registered.
	if needRegister {
		sc.registerOnce.Do(func() {
			prometheus.MustRegister(sc)
			sc.log.Info().Msg("SystemConfigCollector registered with Prometheus.")
		})
	}
}

// AddPhysicalDisk adds or updates information about a physical disk.
func (sc *SystemConfigCollector) AddPhysicalDisk(info PhysicalDiskInfo) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.physicalDisks[info.DiskNumber] = info
	sc.log.Debug().Interface("disk_info", info).Msg("Added physical disk info")
}

// AddLogicalDisk adds or updates information about a logical disk.
func (sc *SystemConfigCollector) AddLogicalDisk(info LogicalDiskInfo) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.logicalDisks[info.DiskNumber] = info
	sc.log.Debug().Interface("disk_info", info).Msg("Added logical disk info")
}

// Describe implements prometheus.Collector.
func (sc *SystemConfigCollector) Describe(ch chan<- *prometheus.Desc) {
	sc.mu.RLock()
	defer sc.mu.RUnlock()

	if sc.requestedMetrics[sc.physicalDiskInfoDesc.String()] {
		ch <- sc.physicalDiskInfoDesc
	}
	if sc.requestedMetrics[sc.logicalDiskInfoDesc.String()] {
		ch <- sc.logicalDiskInfoDesc
	}
}

// Collect implements prometheus.Collector.
func (sc *SystemConfigCollector) Collect(ch chan<- prometheus.Metric) {
	sc.mu.RLock()
	defer sc.mu.RUnlock()

	// Create physical disk information metrics if requested
	if sc.requestedMetrics[sc.physicalDiskInfoDesc.String()] {
		for _, physDisk := range sc.physicalDisks {
			ch <- prometheus.MustNewConstMetric(
				sc.physicalDiskInfoDesc,
				prometheus.GaugeValue,
				1,
				strconv.FormatUint(uint64(physDisk.DiskNumber), 10),
				physDisk.Manufacturer,
			)
		}
	}

	// Create logical disk information metrics if requested
	if sc.requestedMetrics[sc.logicalDiskInfoDesc.String()] {
		for _, logDisk := range sc.logicalDisks {
			ch <- prometheus.MustNewConstMetric(
				sc.logicalDiskInfoDesc,
				prometheus.GaugeValue,
				1,
				strconv.FormatUint(uint64(logDisk.DiskNumber), 10),
				logDisk.DriveLetterString,
				logDisk.FileSystem,
			)
		}
	}
}
