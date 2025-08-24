package main

import (
	"sync"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promauto"
)

// ETWMetrics contains all ETW-related metrics
type ETWMetrics struct {
	// SystemConfig events
	PhyDiskInfo *prometheus.GaugeVec // SystemConfig_PhyDisk events (EventType 11)
	LogDiskInfo *prometheus.GaugeVec // SystemConfig_LogDisk events (EventType 12)
}

var (
	metrics     *ETWMetrics
	metricsOnce sync.Once
)

// TODO: move this to systemconfig collector

// InitMetrics initializes all metrics with proper labels
func InitMetrics() {
	metricsOnce.Do(func() {
		metrics = &ETWMetrics{
			PhyDiskInfo: promauto.NewGaugeVec(
				prometheus.GaugeOpts{
					Name: "etw_system_disk_physical_info",
					Help: "Information about physical disks from SystemConfig_PhyDisk events",
				},
				[]string{"disk_number", "manufacturer"},
			),
			LogDiskInfo: promauto.NewGaugeVec(
				prometheus.GaugeOpts{
					Name: "etw_system_disk_logical_info",
					Help: "Information about logical disks from SystemConfig_LogDisk events",
				},
				[]string{"disk_number", "drive_letter", "file_system"},
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
