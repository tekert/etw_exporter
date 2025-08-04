package main

import (
    "sync"

    "github.com/prometheus/client_golang/prometheus"
    "github.com/prometheus/client_golang/prometheus/promauto"
)

// DiskIOMetrics contains all disk I/O related metrics
type DiskIOMetrics struct {
    // Process disk I/O metrics
    ProcessBytesRead *prometheus.CounterVec
    ProcessBytesWritten *prometheus.CounterVec
    ProcessIOCount *prometheus.CounterVec

    // Physical disk I/O metrics
    DiskBytesRead *prometheus.CounterVec
    DiskBytesWritten *prometheus.CounterVec
    DiskIOCount *prometheus.CounterVec
    DiskIODuration *prometheus.HistogramVec

    // System disk configuration
    DiskInfo *prometheus.GaugeVec
}

var (
    metrics     *DiskIOMetrics
    metricsOnce sync.Once
)

// InitMetrics initializes all metrics with proper labels
func InitMetrics() {
    metricsOnce.Do(func() {
        metrics = &DiskIOMetrics{
            ProcessBytesRead: promauto.NewCounterVec(
                prometheus.CounterOpts{
                    Name: "windows_process_disk_bytes_read_total",
                    Help: "Total bytes read from disk by process",
                },
                []string{"process", "disk"},
            ),
            ProcessBytesWritten: promauto.NewCounterVec(
                prometheus.CounterOpts{
                    Name: "windows_process_disk_bytes_written_total",
                    Help: "Total bytes written to disk by process",
                },
                []string{"process", "disk"},
            ),
            ProcessIOCount: promauto.NewCounterVec(
                prometheus.CounterOpts{
                    Name: "windows_process_disk_io_operations_total",
                    Help: "Total number of disk I/O operations by process",
                },
                []string{"process", "disk", "type"},
            ),
            DiskBytesRead: promauto.NewCounterVec(
                prometheus.CounterOpts{
                    Name: "windows_disk_bytes_read_total",
                    Help: "Total bytes read from disk",
                },
                []string{"disk"},
            ),
            DiskBytesWritten: promauto.NewCounterVec(
                prometheus.CounterOpts{
                    Name: "windows_disk_bytes_written_total",
                    Help: "Total bytes written to disk",
                },
                []string{"disk"},
            ),
            DiskIOCount: promauto.NewCounterVec(
                prometheus.CounterOpts{
                    Name: "windows_disk_io_operations_total",
                    Help: "Total number of disk I/O operations",
                },
                []string{"disk", "type"},
            ),
            DiskIODuration: promauto.NewHistogramVec(
                prometheus.HistogramOpts{
                    Name: "windows_disk_io_duration_seconds",
                    Help: "Duration of disk I/O operations",
                    Buckets: prometheus.ExponentialBuckets(0.001, 2, 10), // From 1ms to ~1s
                },
                []string{"disk", "type"},
            ),
            DiskInfo: promauto.NewGaugeVec(
                prometheus.GaugeOpts{
                    Name: "windows_disk_info",
                    Help: "Information about physical disks (1 = present)",
                },
                []string{"disk_number", "device_name", "manufacturer", "model"},
            ),
        }
    })
}

// GetMetrics returns the initialized metrics
func GetMetrics() *DiskIOMetrics {
    if metrics == nil {
        InitMetrics()
    }
    return metrics
}