# ETW Exporter Configuration Guide

This document provides information about configuring the ETW Exporter's server and data collectors. For logging configuration, see `LOGCONFIG.md`.

## Configuration File Format

The ETW Exporter uses TOML (Tom's Obvious, Minimal Language).

```toml
# Comments start with a hash symbol
[section]
key = "value"
```

## Server Configuration

The `[server]` section configures the HTTP server that exposes Prometheus metrics and debugging endpoints.

```toml
[server]
listen_address = "localhost:9189"
metrics_path = "/metrics"
pprof_enabled = true
```

- **listen_address**: The address and port for the HTTP server. Use `":9189"` to listen on all network interfaces.
- **metrics_path**: The URL path where Prometheus metrics are served.
- **pprof_enabled**: If `true`, enables the Go pprof debugging endpoints on `localhost:6060` for performance profiling.

## Collectors Configuration

The `[collectors]` section enables and configures the different ETW data collectors.

### Process Filtering

The `process_filter` section allows you to collect metrics for a specific subset of processes, reducing metric cardinality and focusing on applications of interest.

```toml
[collectors.process_filter]
enabled = false
include_names = ["svchost.exe", "myapp.*\\.exe", "sqlservr.exe"]
```

- **enabled**: Set to `true` to enable process filtering. If `false`, metrics for all processes are collected.
- **include_names**: A list of regular expressions to match against process image names (e.g., `notepad.exe`). If filtering is enabled, only processes whose names match one of these patterns will have their metrics exported.
  - The matching is based on the process's unique "start key". Once a process name matches, all other processes sharing that same start key (even if they have a different PID due to reuse) will also be tracked.
  - The syntax is Go's standard regular expression format, as documented [here](https://golang.org/s/re2syntax).

### Disk I/O Collector

The `disk_io` collector tracks disk read, write, and flush operations.

```toml
[collectors.disk_io]
enabled = true
```

- **enabled**: Set to `true` to enable this collector.

**Metrics Provided:**
- `etw_disk_io_operations_total{disk, operation}`: Total count of I/O operations (`read`, `write`, `flush`) per physical disk.
- `etw_disk_read_bytes_total{disk}`: Total bytes read per physical disk.
- `etw_disk_written_bytes_total{disk}`: Total bytes written per physical disk.
- `etw_disk_process_io_operations_total{process_id, process_start_key, process_name, disk, operation}`: Total count of I/O operations per process and disk.
- `etw_disk_process_read_bytes_total{process_id, process_start_key, process_name, disk}`: Total bytes read per process and disk.
- `etw_disk_process_written_bytes_total{process_id, process_start_key, process_name, disk}`: Total bytes written per process and disk.

### Thread Context Switch Collector

The `threadcs` collector tracks thread scheduling activity, including context switches and state transitions.

```toml
[collectors.threadcs]
enabled = false
```

- **enabled**: Set to `true` to enable this collector. This is a high-frequency collector and may have a performance impact.

**Metrics Provided:**
- `etw_thread_context_switches_cpu_total{cpu}`: Total number of context switches per CPU.
- `etw_thread_context_switches_process_total{process_id, process_name}`: Total number of context switches per process.
- `etw_thread_context_switch_interval_milliseconds{cpu}`: A histogram of the time between context switches on each CPU.
- `etw_thread_states_total{state, wait_reason}`: A count of thread state transitions (e.g., `running`, `waiting`).

### Performance Info Collector

The `perfinfo` collector tracks low-level kernel events related to interrupt and DPC (Deferred Procedure Call) latency.

```toml
[collectors.perfinfo]
enabled = true
enable_per_cpu = false
enable_per_driver = false
enable_smi_detection = false
```

- **enabled**: Enables the core system-wide latency metrics.
- **enable_per_cpu**: Adds a `cpu` label to DPC queue metrics for per-CPU analysis. This increases metric cardinality.
- **enable_per_driver**: Adds an `image_name` label to DPC duration metrics to identify which drivers are causing latency. This increases cardinality.
- **enable_smi_detection**: (Experimental) Enables detection of System Management Interrupts (SMI).

**Metrics Provided:**
- `etw_perfinfo_isr_to_dpc_latency_microseconds`: Histogram of the time from an interrupt request (ISR) to the start of its corresponding DPC.
- `etw_perfinfo_dpc_queued_total`: Total number of DPCs queued for execution.
- `etw_perfinfo_dpc_executed_total`: Total number of DPCs that began execution.
- With `enable_per_cpu`:
    - `etw_perfinfo_dpc_queued_cpu_total{cpu}`
    - `etw_perfinfo_dpc_executed_cpu_total{cpu}`
- With `enable_per_driver`:
    - `etw_perfinfo_dpc_execution_time_microseconds{image_name}`: Histogram of DPC execution time per driver.

### Network Collector

The `network` collector tracks TCP and UDP network activity.

```toml
[collectors.network]
enabled = false
enable_connection_stats = false
enable_by_protocol = false
enable_retrasmission_rate = false
```

- **enabled**: Enables the base network traffic metrics (`sent` and `received` bytes).
- **enable_connection_stats**: Enables metrics for connection attempts, successes, and failures.
- **enable_by_protocol**: Adds a metric that aggregates traffic by protocol and direction.
- **enable_retrasmission_rate**: Enables metrics for tracking TCP retransmissions.

**Metrics Provided:**
- `etw_network_sent_bytes_total{process_id, process_start_key, process_name, protocol}`: Total bytes sent per process and protocol (`tcp`/`udp`).
- `etw_network_received_bytes_total{process_id, process_start_key, process_name, protocol}`: Total bytes received per process and protocol.
- With `enable_connection_stats`:
    - `etw_network_connections_attempted_total{process_id, process_start_key, process_name, protocol}`
    - `etw_network_connections_accepted_total{process_id, process_start_key, process_name, protocol}`
    - `etw_network_connections_failed_total{process_id, process_start_key, process_name, protocol, failure_code}`
- With `enable_by_protocol`:
    - `etw_network_traffic_bytes_total{protocol, direction}`
- With `enable_retrasmission_rate`:
    - `etw_network_retransmissions_total{process_id, process_start_key, process_name}`

### Memory Collector

The `memory` collector tracks memory-related kernel events, such as page faults.

```toml
[collectors.memory]
enabled = true
enable_per_process = true
```

- **enabled**: Enables the memory collector.
- **enable_per_process**: Enables per-process hard page fault metrics.

**Metrics Provided:**
- `etw_memory_hard_pagefaults_total`: Total number of hard page faults system-wide.
- With `enable_per_process`:
    - `etw_memory_hard_pagefaults_per_process_total{process_id, process_name}`: Total hard page faults by process.

### Registry Collector

The `registry` collector tracks registry operations such as key creation, deletion, and value setting.

```toml
[collectors.registry]
enabled = false
enable_per_process = true
```

- **enabled**: Set to `true` to enable this collector. This can be a high-frequency collector.
- **enable_per_process**: Enables per-process registry operation metrics.

**Metrics Provided:**
- `etw_registry_operations_total{operation, result}`: Total number of registry operations by type (e.g., `create_key`, `set_value`) and result (`success`/`failure`).
- With `enable_per_process`:
    - `etw_registry_operations_process_total{process_id, process_start_key, process_name, operation, result}`: Total number of registry operations per process.

## Session Watcher Configuration

The `[session_watcher]` section configures the automatic restart of ETW sessions if they are stopped by an external process. This ensures the exporter remains resilient.

```toml
[session_watcher]
enabled = true
restart_kernel_session = true
restart_exporter_session = true
```

- **enabled**: Set to `true` to enable the session watcher.
- **restart_kernel_session**: If `true`, the watcher will attempt to restart the `NT Kernel Logger` session if it is stopped.
- **restart_exporter_session**: If `true`, the watcher will attempt to restart the main `etw_exporter` session if it is stopped.
