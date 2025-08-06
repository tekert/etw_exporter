package main

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// ETWMetrics contains all ETW-related metrics
type ETWMetrics struct {
	// Process disk I/O metrics
	ProcessBytesRead    *prometheus.CounterVec
	ProcessBytesWritten *prometheus.CounterVec
	ProcessIOCount      *prometheus.CounterVec

	// Physical disk I/O metrics
	DiskBytesRead    *prometheus.CounterVec
	DiskBytesWritten *prometheus.CounterVec
	DiskIOCount      *prometheus.CounterVec
	DiskIODuration   *prometheus.HistogramVec

	// System disk configuration - separated by event type
	PhyDiskInfo *prometheus.GaugeVec // SystemConfig_PhyDisk events (EventType 11)
	LogDiskInfo *prometheus.GaugeVec // SystemConfig_LogDisk events (EventType 12)

	// Thread and context switch metrics (aggregated for low cardinality)
	ContextSwitchesPerCPU     *prometheus.CounterVec
	ContextSwitchesPerProcess *prometheus.CounterVec
	ContextSwitchInterval     *prometheus.HistogramVec
	ThreadStatesTotal         *prometheus.CounterVec
}

var (
	metrics     *ETWMetrics
	metricsOnce sync.Once
)

// InitMetrics initializes all metrics with proper labels
func InitMetrics() {
	metricsOnce.Do(func() {
		metrics = &ETWMetrics{
			ProcessBytesRead: promauto.NewCounterVec(
				prometheus.CounterOpts{
					Name: "etw_process_disk_bytes_read_total",
					Help: "Total bytes read from disk by process",
				},
				[]string{"process", "disk"},
			),
			ProcessBytesWritten: promauto.NewCounterVec(
				prometheus.CounterOpts{
					Name: "etw_process_disk_bytes_written_total",
					Help: "Total bytes written to disk by process",
				},
				[]string{"process", "disk"},
			),
			ProcessIOCount: promauto.NewCounterVec(
				prometheus.CounterOpts{
					Name: "etw_process_disk_io_operations_total",
					Help: "Total number of disk I/O operations by process",
				},
				[]string{"process", "disk", "type"},
			),
			DiskBytesRead: promauto.NewCounterVec(
				prometheus.CounterOpts{
					Name: "etw_disk_bytes_read_total",
					Help: "Total bytes read from disk",
				},
				[]string{"disk"},
			),
			DiskBytesWritten: promauto.NewCounterVec(
				prometheus.CounterOpts{
					Name: "etw_disk_bytes_written_total",
					Help: "Total bytes written to disk",
				},
				[]string{"disk"},
			),
			DiskIOCount: promauto.NewCounterVec(
				prometheus.CounterOpts{
					Name: "etw_disk_io_operations_total",
					Help: "Total number of disk I/O operations",
				},
				[]string{"disk", "type"},
			),
			DiskIODuration: promauto.NewHistogramVec(
				prometheus.HistogramOpts{
					Name:    "etw_disk_io_duration_seconds",
					Help:    "Duration of disk I/O operations",
					Buckets: prometheus.ExponentialBuckets(0.001, 2, 10), // From 1ms to ~1s
				},
				[]string{"disk", "type"},
			),
			PhyDiskInfo: promauto.NewGaugeVec(
				prometheus.GaugeOpts{
					Name: "etw_disk_physical_info",
					Help: "Information about physical disks from SystemConfig_PhyDisk events",
				},
				[]string{"disk_number", "manufacturer"},
			),
			LogDiskInfo: promauto.NewGaugeVec(
				prometheus.GaugeOpts{
					Name: "etw_disk_logical_info",
					Help: "Information about logical disks from SystemConfig_LogDisk events",
				},
				[]string{"disk_number", "drive_letter", "file_system"},
			),
			ContextSwitchesPerCPU: promauto.NewCounterVec(
				prometheus.CounterOpts{
					Name: "etw_context_switches_per_cpu_total",
					Help: "Total number of context switches per CPU",
				},
				[]string{"cpu"},
			),
			ContextSwitchesPerProcess: promauto.NewCounterVec(
				prometheus.CounterOpts{
					Name: "etw_context_switches_per_process_total",
					Help: "Total number of context switches per process",
				},
				[]string{"process_id", "process_name"},
			),
			ContextSwitchInterval: promauto.NewHistogramVec(
				prometheus.HistogramOpts{
					Name:    "etw_context_switch_interval_seconds",
					Help:    "Time between consecutive context switches on a CPU",
					Buckets: prometheus.ExponentialBuckets(0.000001, 2, 15), // 1us to ~16ms
				},
				[]string{"cpu"},
			),
			ThreadStatesTotal: promauto.NewCounterVec(
				prometheus.CounterOpts{
					Name: "etw_thread_states_total",
					Help: "Total count of thread state transitions",
				},
				[]string{"state", "wait_reason"},
			),
		}
	})
}

// GetMetrics returns the initialized metrics
func GetMetrics() *ETWMetrics {
	if metrics == nil {
		InitMetrics()
	}
	return metrics
}
