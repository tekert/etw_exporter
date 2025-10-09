package kernelsysconfig

import (
	"strconv"
	"sync"

	"github.com/phuslu/log"
	"github.com/prometheus/client_golang/prometheus"

	"etw_exporter/internal/kernel/statemanager"
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

// ServiceInfo holds the data parsed from a SystemConfig_Services ETW event.
// It provides a structured representation of a service's configuration.
type ServiceInfo struct {
	ProcessId     uint32
	ServiceState  uint32
	SubProcessTag uint32
	ServiceName   string
	DisplayName   string
	ProcessName   string
}

// SysConfCollector stores static system configuration data collected from ETW.
// It acts as a singleton global repository for this information and a Prometheus collector.
type SysConfCollector struct {
	physicalDisks map[uint32]PhysicalDiskInfo // diskNumber -> disk info
	logicalDisks  map[uint32]LogicalDiskInfo  // diskNumber -> logical disk info

	stateManager *statemanager.KernelStateManager
	mu           sync.RWMutex
	log          log.Logger

	// Metric Descriptors
	physicalDiskInfoDesc *prometheus.Desc
	logicalDiskInfoDesc  *prometheus.Desc

	// Controls which metrics are exposed via the Collector interface
	requestedMetrics map[string]bool
	registerOnce     sync.Once
}

// NewSystemConfigCollector creates a new system configuration collector.
func NewSystemConfigCollector(sm *statemanager.KernelStateManager) *SysConfCollector {
	return &SysConfCollector{
		physicalDisks:    make(map[uint32]PhysicalDiskInfo),
		logicalDisks:     make(map[uint32]LogicalDiskInfo),
		stateManager:     sm,
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
}

// Describe implements prometheus.Collector.
func (sc *SysConfCollector) Describe(ch chan<- *prometheus.Desc) {
	sc.mu.RLock()
	defer sc.mu.RUnlock()

	if sc.requestedMetrics[PhysicalDiskInfoMetricName] {
		ch <- sc.physicalDiskInfoDesc
	}
	if sc.requestedMetrics[LogicalDiskInfoMetricName] {
		ch <- sc.logicalDiskInfoDesc
	}
}

// Collect implements prometheus.Collector.
func (sc *SysConfCollector) Collect(ch chan<- prometheus.Metric) {
	sc.mu.RLock()
	defer sc.mu.RUnlock()

	// Create physical disk information metrics if requested
	if sc.requestedMetrics[PhysicalDiskInfoMetricName] {
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
	if sc.requestedMetrics[LogicalDiskInfoMetricName] {
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

	sc.log.Debug().Msg("Collected system configuration metrics")
}

// RequestMetrics allows other collectors to request that specific metrics be exposed.
// This method is thread-safe and ensures the collector is registered with Prometheus
// exactly once when the first metric is requested.
func (sc *SysConfCollector) RequestMetrics(metricNames ...string) {
	needsRegister := false
	sc.mu.Lock()
	for _, name := range metricNames {
		if !sc.requestedMetrics[name] {
			sc.requestedMetrics[name] = true
			needsRegister = true
			sc.log.Debug().Str("metric", name).Msg("System config metric has been requested.")
		}
	}
	sc.mu.Unlock()

	// If any new metrics were requested, ensure the collector is registered.
	// This is done outside the lock to prevent a deadlock, as prometheus.Register
	// will call back into the collector's Describe method, which needs to re-acquire the lock.
	if needsRegister {
		sc.registerOnce.Do(func() {
			prometheus.MustRegister(sc)
			sc.log.Debug().Msg("SystemConfigCollector registered with Prometheus.")
		})
	}
}

// AddPhysicalDisk adds or updates information about a physical disk.
func (sc *SysConfCollector) AddPhysicalDisk(info PhysicalDiskInfo) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.physicalDisks[info.DiskNumber] = info
	sc.log.Trace().Interface("disk_info", info).Msg("Added physical disk info")
}

// AddLogicalDisk adds or updates information about a logical disk.
func (sc *SysConfCollector) AddLogicalDisk(info LogicalDiskInfo) {
	sc.mu.Lock()
	defer sc.mu.Unlock()
	sc.logicalDisks[info.DiskNumber] = info
	sc.log.Trace().Interface("disk_info", info).Msg("Added logical disk info")
}

// AddServiceInfo adds a service tag mapping to the central state manager.
func (sc *SysConfCollector) AddServiceInfo(info ServiceInfo) {
	sc.stateManager.RegisterServiceTag(info.SubProcessTag, info.ServiceName)
	sc.log.Trace().
		Uint32("tag", info.SubProcessTag).
		Str("name", info.ServiceName).
		Msg("Reported service tag mapping to state manager")
}
