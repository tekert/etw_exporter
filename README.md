# ETW Exporter

A high-performance Windows ETW (Event Tracing for Windows) exporter that exposes detailed kernel metrics for Prometheus monitoring.

## Metrics Exposed

The exporter provides several categories of metrics, which can be enabled via the configuration file:

- **Disk I/O**: Tracks reads, writes, and bytes transferred per disk and per process.
- **Thread Scheduling**: Collects data on context switches, thread states, and scheduling latency.
- **Interrupt & DPC Latency**: Measures low-level kernel latencies, including ISR-to-DPC latency and DPC execution time by driver.
- **Network**: Monitors TCP/UDP traffic, connection stats, and retransmissions by process.

> If you need more metrics just open an issue, there is tons of additional info from the kernel, the config exposed here is the one that requieres less cardinality for metrics.

Configurable per process filters will be in v0.4

## Configuration

The exporter is configured using a TOML file. Below is an example configuration. For a full list of options, see `config/CONFIG.md` and `config/LOGCONFIG.md`.

```toml
# Example config.toml

[server]
listen_address = ":9189"
metrics_path = "/metrics"
pprof_enabled = true

[collectors]
[collectors.disk_io]
enabled = true

[collectors.threadcs]
enabled = false # Can have performance impact

[collectors.perfinfo]
enabled = true
enable_per_driver = true # Useful for debugging driver latency

[collectors.network]
enabled = true
enable_connection_stats = true

[logging.defaults]
level = "info"

[[logging.outputs]]
type = "console"
enabled = true
```

### Configuration Options
- **`[server]`**: Configures the HTTP endpoint for Prometheus scrapes and `pprof`.
- **`[collectors]`**: Enables or disables specific metric collectors.
  - `disk_io`: Disk activity metrics.
  - `threadcs`: Thread context switch and state metrics.
  - `perfinfo`: Kernel interrupt and DPC latency metrics.
  - `network`: TCP/UDP network metrics.
- **`[logging]`**: Configures logging outputs. Supported types include `console`, `file`, `syslog`, and `eventlog`. See `config/LOGCONFIG.md` for details.

## Usage

```powershell
# Run with a custom configuration file
.\etw_exporter.exe -config config.toml

# Override a specific setting from the command line
.\etw_exporter.exe -config config.toml -web.listen-address ":9099"
```

- **Metrics Endpoint**: `http://localhost:9189/metrics`

## Requirements

- **Windows**: Administrator privileges are required to access ETW sessions.
- **Go**: 1.22+ for building from source.

## Build

```powershell
go build -v .
```

The binary (`etw_exporter.exe`) can be deployed without external dependencies.
