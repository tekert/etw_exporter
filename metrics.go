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

	// File mapping metrics (heavy metrics - controlled by TrackFileMapping)
	ProcessFileBytesRead    *prometheus.CounterVec
	ProcessFileBytesWritten *prometheus.CounterVec
	ProcessFileIOCount      *prometheus.CounterVec

	// Context switch metrics
	ContextSwitches      *prometheus.CounterVec
	ContextSwitchLatency *prometheus.HistogramVec
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
			// File mapping metrics (heavy metrics - controlled by TrackFileMapping)
			ProcessFileBytesRead: promauto.NewCounterVec(
				prometheus.CounterOpts{
					Name: "etw_process_file_bytes_read_total",
					Help: "Total bytes read from files by process",
				},
				[]string{"process", "file_path"},
			),
			ProcessFileBytesWritten: promauto.NewCounterVec(
				prometheus.CounterOpts{
					Name: "etw_process_file_bytes_written_total",
					Help: "Total bytes written to files by process",
				},
				[]string{"process", "file_path"},
			),
			ProcessFileIOCount: promauto.NewCounterVec(
				prometheus.CounterOpts{
					Name: "etw_process_file_io_operations_total",
					Help: "Total number of file I/O operations by process",
				},
				[]string{"process", "file_path", "type"},
			),
			// Context switch metrics
			ContextSwitches: promauto.NewCounterVec(
				prometheus.CounterOpts{
					Name: "etw_context_switches_total",
					Help: "Total number of context switches",
				},
				[]string{"old_thread_id", "new_thread_id", "cpu"},
			),
			ContextSwitchLatency: promauto.NewHistogramVec(
				prometheus.HistogramOpts{
					Name:    "etw_context_switch_latency_seconds",
					Help:    "Context switch latency",
					Buckets: prometheus.ExponentialBuckets(0.000001, 2, 20), // From 1μs to ~1s
				},
				[]string{"cpu"},
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
