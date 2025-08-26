# ETW Exporter Configuration Guide

This document provides comprehensive information about configuring the ETW Exporter using TOML format.

Written by IA (while i work on the interface, this was reviewed dont worry)

## Configuration File Format

The ETW Exporter uses TOML (Tom's Obvious, Minimal Language) for configuration. TOML is designed to be easy to read and write, with a clear and minimal syntax.

### Basic TOML Syntax

```toml
# Comments start with hash
key = "string value"
number = 42
boolean = true
array = ["item1", "item2"]

[section]
nested_key = "value"

[[array_of_tables]]
name = "first"

[[array_of_tables]]  
name = "second"
```

## Configuration Sections

### Server Configuration

The `[server]` section configures the HTTP server that exposes Prometheus metrics.

```toml
[server]
listen_address = ":9189"    # Default: "localhost:9189"
metrics_path = "/metrics"   # Default: "/metrics"
```

**listen_address**: The address and port where the HTTP server listens for requests.
- Format: `"[host]:port"`
- Examples: 
  - `":9189"` - Listen on all interfaces, port 9189
  - `"localhost:9189"` - Listen only on localhost
  - `"127.0.0.1:9189"` - Listen only on loopback interface
  - `"0.0.0.0:9189"` - Explicitly listen on all interfaces

**metrics_path**: The URL path where Prometheus metrics are served.
- Must start with `/`
- Example: `"/metrics"` serves metrics at `http://host:port/metrics`

**pprof_enabled**: Whether to enable the Go pprof debugging endpoints.
- Default: `true`
- When enabled, the pprof server runs on `localhost:6060`.
- This is useful for performance profiling and debugging. It is recommended to disable this in production environments if the port is exposed externally.

### Collectors Configuration

The `[collectors]` section enables and configures different ETW event collectors.

#### Disk I/O Collector

```toml
[collectors.disk_io]
enabled = true           # Default: true
```

**enabled**: Whether to collect disk I/O events.
- When enabled, tracks disk read/write operations with detailed metrics
- Performance impact: Low depending on disk activity

#### ThreadCS Collector

```toml
[collectors.threadcs]
enabled = false           # Default: false
```

**enabled**: Whether to collect thread context switch events.
- Tracks thread context switches (when threads are scheduled/descheduled by the OS)
- High-frequency events that may impact performance under heavy load
- Performance impact: high

#### Interrupt Latency Collector

```toml
[collectors.perfinfo]
enabled = true               # Default: true
enable_per_driver = false    # Default: false
enable_per_cpu = false       # Default: false
enable_smi_detection = false # Default: false
```

**enabled**: Whether to collect interrupt latency metrics.
- Provides system-wide interrupt to process latency, DPC queue depth, and hard page fault counts.
- These base metrics have a **low performance impact** and are safe to enable in most environments.
- Additional high-cardinality metrics can be enabled for detailed analysis.

**enable_per_driver**: Whether to collect ISR and DPC execution time by driver.
- Adds `image_name` labels to ISR/DPC duration histograms.
- Increases cardinality based on the number of active drivers.
- Useful for identifying drivers causing high latency.
- Performance impact: Low to Medium.

**enable_per_cpu**: Whether to include per-CPU metrics for DPC queue depth.
- Adds `cpu` labels to queue depth metrics.
- Increases cardinality significantly on systems with many CPUs.
- Only enable if per-CPU analysis is specifically needed.

**enable_smi_detection**: Experimental, requieres profiling to be active.
- TODO: Not easy with only ETW.

#### Network Collector

```toml
[collectors.network]
enabled = false              # Default: false
enable_connection_stats = false    # Default: false
enable_by_protocol = false          # Default: false
enable_retrasmission_rate = false  # Default: false
```

**enabled**: Whether to collect network events.
- Tracks TCP/UDP data transfer, connection attempts, and failures
- Provides insights into network usage by process and protocol
- Performance impact: Low to medium depending on network activity

**enable_connection_stats**: Whether to collect connection health metrics.
- Tracks connection attempts, acceptances, and failures with failure codes
- Useful for diagnosing network connectivity issues
- Adds metrics: `etw_network_connections_attempted_total`, `etw_network_connections_accepted_total`, `etw_network_connections_failed_total`

**enable_by_protocol**: Whether to include protocol-specific traffic metrics.
- Adds protocol distribution metrics aggregated across all processes
- Provides `etw_network_traffic_bytes_total` with protocol and direction labels
- Useful for understanding overall network traffic patterns

**enable_retrasmission_rate**: Whether to track TCP retransmission metrics.
- Monitors TCP reliability by tracking retransmission events
- Calculates retransmission ratios for each process
- Adds metrics: `etw_network_retransmissions_total`, `etw_network_retransmission_ratio`
- Useful for identifying network quality issues

**Core Metrics** (always available when enabled):
- `etw_network_bytes_sent_total{process_name, protocol}`: Bytes sent by process and protocol
- `etw_network_bytes_received_total{process_name, protocol}`: Bytes received by process and protocol

**Performance Impact**:
- **Base metrics (enabled=true)**: Low. Suitable for most environments.
- **Additional metrics**: Low to medium. Enable based on monitoring requirements.

### Logging Configuration

The `[logging]` section provides comprehensive control over logging behavior.

#### Default Settings

```toml
[logging.defaults]
level = "info"           # Default: "info"
caller = 0               # Default: 0
time_field = "time"      # Default: "time"
time_format = ""         # Default: "" (RFC3339 with milliseconds)
time_location = "Local"  # Default: "Local"
```

**level**: Minimum log level to output.
- Levels (in order): `"trace"`, `"debug"`, `"info"`, `"warn"`, `"error"`, `"fatal"`
- Higher levels include all lower levels
- `"trace"` = maximum verbosity, `"fatal"` = only critical errors

**caller**: Include source code location in logs.
- `0` = disabled (no caller information)
- `1` = file:line format (e.g., `"main.go:42"`)
- `-1` = full path format (e.g., `"/path/to/main.go:42"`)

**time_field**: JSON field name for timestamps.
- Standard field name for structured logging
- Change only if you need compatibility with specific log processors

**time_format**: Format for log timestamps.
- `""` = RFC3339 with milliseconds (recommended for performance)
- `"2006-01-02T15:04:05"` = custom format using Go time layout
- `"Unix"` = Unix timestamp (seconds since epoch)
- `"UnixMs"` = Unix timestamp with milliseconds

**time_location**: Time zone for timestamps.
- `"Local"` = system local time zone
- `"UTC"` = Coordinated Universal Time
- `"America/New_York"`, `"Europe/London"` = named time zones

#### Library Logging

```toml
[logging]
lib_level = "warn"  # Default: "warn"
```

**lib_level**: Log level for the ETW library itself.
- Controls verbosity of the underlying ETW event processing library
- Usually should be `"warn"` or higher to avoid excessive output
- Set to `"debug"` or `"trace"` only when troubleshooting ETW issues

#### Output Configuration

Outputs are configured as an array of tables using `[[logging.outputs]]` syntax.

##### Console Output

```toml
[[logging.outputs]]
type = "console"
enabled = true

[logging.outputs.console]
fast_io = false         # Default: false
format = "auto"         # Default: "auto"
color_output = true     # Default: true  
quote_string = true     # Default: true
writer = "stderr"       # Default: "stderr"
async = false           # Default: false
```

**fast_io**: Performance vs. readability trade-off.
- `false` = formatted output with colors and structure (human-readable)
- `true` = raw JSON output (machine-readable, higher performance)
- Note: Hot path modules always use fast JSON logging regardless of this setting

**format**: Output format when `fast_io = false`.
- `"auto"` = colorized console format with key=value pairs
- `"logfmt"` = logfmt format (`key=value key=value`)
- `"glog"` = Google glog format (`LEVEL mmdd hh:mm:ss.uuuuuu threadid file:line] msg`)

**color_output**: Enable colored text output.
- Only applies to `"auto"` format
- Automatically disabled if output is redirected to a file
- Improves readability in terminals

**quote_string**: Quote string values in formatted output.
- Helps distinguish string values from numeric values
- Only applies to non-JSON formats

**writer**: Output destination.
- `"stdout"` = standard output (for normal program output)
- `"stderr"` = standard error (recommended for logs)

**async**: Use asynchronous writing.
- `false` = synchronous (immediate write, may block)
- `true` = asynchronous (buffered write, better performance)
- Generally not needed for console output

##### File Output

```toml
[[logging.outputs]]
type = "file"
enabled = true

[logging.outputs.file]
filename = "logs/app.log"     # Required
# File permissions are fixed to 0644 on Windows
max_size = 10                 # Default: 10 (megabytes)
max_backups = 7               # Default: 7
time_format = "2006-01-02T15-04-05"  # Default: "2006-01-02T15-04-05"
local_time = true             # Default: true
host_name = true              # Default: true
process_id = true             # Default: true
ensure_folder = true          # Default: true
async = true                  # Default: true
```

**filename**: Path to the log file.
- Can be relative or absolute path
- Directory structure will be created if `ensure_folder = true`

**file permissions**: On Windows, file permissions are always set to (TODO)

**max_size**: Maximum file size before rotation.
- Size in megabytes (e.g., 10 = 10MB)
- When exceeded, file is rotated and new file started

**max_backups**: Number of old log files to keep.
- `0` = keep all files (not recommended for long-running services)
- Older files beyond this limit are automatically deleted

**time_format**: Format for rotated file timestamps.
- Can use Go time layout format (see [Go time layouts](https://pkg.go.dev/time#pkg-constants)) for values.
- `"2006-01-02T15-04-05"` = ISO date format by default.
- `"Unix"` / `"UnixMs"` = numeric timestamps

**local_time**: Use local time for file rotation.
- `true` = use system local time
- `false` = use UTC time

**host_name**: Include hostname in filename.
- Useful when aggregating logs from multiple servers
- Helps identify log source in centralized systems

**process_id**: Include process ID in filename.
- Helps distinguish between multiple instances
- Useful for debugging and process tracking

**ensure_folder**: Create directory structure automatically.
- `true` = create directories as needed
- `false` = fail if directory doesn't exist

**async**: Use asynchronous file writing.
- `true` = buffered writing (recommended for performance)
- `false` = synchronous writing (may impact performance)

##### Syslog Output

```toml
[[logging.outputs]]
type = "syslog"
enabled = false

[logging.outputs.syslog]
network = "udp"               # Default: "udp"
address = "localhost:514"     # Default: "localhost:514"
hostname = ""                 # Default: system hostname
tag = "etw_exporter"          # Default: "etw_exporter"
marker = "@cee:"              # Default: "@cee:"
async = true                  # Default: true
```

RFC 3164

**network**: Network protocol for syslog.
- `"udp"` = fast but may lose messages
- `"tcp"` = reliable but slower
- `"unixgram"` = Unix domain socket (Linux/Unix only)

**address**: Syslog server address.
- `"host:port"` for network protocols
- `"/path/to/socket"` for Unix domain sockets

**hostname**: Hostname in syslog messages.
- Empty string uses system hostname
- Override for custom identification

**tag**: Program name in syslog messages.
- Identifies the application in syslog
- Used for filtering and routing

**marker**: Message prefix for structured logging.
- `"@cee:"` = Common Event Expression marker
- `""` = no marker (plain text)

##### Windows Event Log Output

```toml
[[logging.outputs]]
type = "eventlog"
enabled = false

[logging.outputs.eventlog]
source = "ETW Exporter"       # Default: "ETW Exporter"
id = 1000                     # Default: 1000
host = ""                     # Default: local machine
async = false                 # Default: false
```

**source**: Event source name in Windows Event Log.
- Must be registered in Windows Registry before use
- Use `eventcreate` command or PowerShell to register

**id**: Event ID for log entries.
- Used to categorize and filter events in Event Viewer
- Choose unique IDs for different message types

**host**: Target machine for event logging.
- Empty string = local machine
- Can specify remote machine for centralized logging

**async**: Asynchronous event writing.
- `false` = synchronous (recommended for reliability)
- `true` = asynchronous (better performance, may lose events)

## Performance Considerations

### Logging Optimization

The ETW Exporter automatically optimizes logging for high-frequency event processing:

- **Async File Writing**: Recommended for file outputs to prevent I/O blocking

### CPU Impact

- **Console Formatting**: Colorized console output has higher CPU overhead
- **Caller Information**: Adds overhead for stack trace collection

## Examples

### Development Configuration

For development with human-readable logs:

```toml
[logging.defaults]
level = "debug"
caller = 1

[[logging.outputs]]
type = "console"
enabled = true

[logging.outputs.console]
fast_io = false
format = "auto"
color_output = true
```

### Production Configuration

For production with structured logging:

```toml
[logging.defaults]
level = "info"
caller = 0

[[logging.outputs]]
type = "console"
enabled = true

[logging.outputs.console]
fast_io = true

[[logging.outputs]]
type = "file"
enabled = true

[logging.outputs.file]
filename = "/var/log/etw_exporter/app.log"
max_size = 512  # 512MB
max_backups = 30
async = true
```

### Syslog Logging

For centralized logging with syslog:

```toml
[[logging.outputs]]
type = "syslog"
enabled = true

[logging.outputs.syslog]
network = "tcp"
address = "logserver.example.com:514"
tag = "etw_exporter"
marker = "@cee:"
```

## Troubleshooting

### Debug Configuration

To troubleshoot configuration issues:

```toml
[logging]
lib_level = "debug"

[logging.defaults]
level = "debug"
```

This will provide detailed information about the ETW library and application behavior.

## Validation

The application validates configuration on startup and will report errors for:

- Invalid log levels
- Missing required fields
- Invalid file paths
- Network connectivity issues (for syslog)

Check the application logs for detailed validation error messages.
