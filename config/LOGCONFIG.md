# ETW Exporter Logging Configuration

This document provides detailed information about configuring logging for the ETW Exporter.

## Logging Configuration

The `[logging]` section provides comprehensive control over logging behavior.

### Default Settings

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
- Higher levels include all lower levels.
- `"trace"` = maximum verbosity, `"fatal"` = only critical errors.

**caller**: Include source code location in logs.
- `0` = disabled (no caller information).
- `1` = file:line format (e.g., `"main.go:42"`).
- `-1` = full path format (e.g., `"/path/to/main.go:42"`).

**time_field**: JSON field name for timestamps.
- Standard field name for structured logging.

**time_format**: Format for log timestamps.
- `""` = RFC3339 with milliseconds (recommended for performance).
- `"2006-01-02T15:04:05"` = custom format using Go time layout.
- `"Unix"` = Unix timestamp (seconds since epoch).
- `"UnixMs"` = Unix timestamp with milliseconds.

**time_location**: Time zone for timestamps.
- `"Local"` = system local time zone.
- `"UTC"` = Coordinated Universal Time.
- `"America/New_York"`, `"Europe/London"` = named time zones.

### Library Logging

```toml
[logging]
lib_level = "warn"  # Default: "warn"
```

**lib_level**: Log level for the underlying `goetw` library.
- Controls verbosity of the ETW event processing library.
- It is recommended to keep this at `"warn"` or higher to avoid excessive output.

### Output Configuration

Outputs are configured as an array of tables using `[[logging.outputs]]` syntax. You can enable multiple outputs simultaneously.

#### Console Output

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

**fast_io**: `true` for raw JSON output (machine-readable, high performance), `false` for formatted, human-readable output.
**format**: Output format when `fast_io = false`. Can be `"auto"`, `"logfmt"`, or `"glog"`.
**color_output**: Enable colored text output for the `"auto"` format.
**writer**: Output destination, either `"stdout"` or `"stderr"`.
**async**: `true` to enable asynchronous, buffered writing for better performance.

#### File Output

```toml
[[logging.outputs]]
type = "file"
enabled = false

[logging.outputs.file]
filename = "logs/app.log"     # Required
max_size = 10                 # Default: 10 (megabytes)
max_backups = 7               # Default: 7
time_format = "2006-01-02T15-04-05"
local_time = true
ensure_folder = true
async = true
```

**filename**: Path to the log file.
**max_size**: Maximum file size in megabytes before rotation.
**max_backups**: Number of old log files to keep.
**ensure_folder**: If `true`, creates the log directory if it doesn't exist.
**async**: Asynchronous writing is enabled by default for performance.

#### Syslog Output

```toml
[[logging.outputs]]
type = "syslog"
enabled = false

[logging.outputs.syslog]
network = "udp"
address = "localhost:514"
tag = "etw_exporter"
marker = "@cee:"
async = true
```

**network**: Protocol for syslog (`"udp"`, `"tcp"`, or `"unixgram"`).
**address**: Syslog server address (`"host:port"`).
**tag**: Program name to identify the application in syslog.
**marker**: A prefix for structured logging, such as `@cee:`.

#### Windows Event Log Output

```toml
[[logging.outputs]]
type = "eventlog"
enabled = false

[logging.outputs.eventlog]
source = "ETW Exporter"
id = 1000
host = ""
async = false
```

**source**: The event source name, which must be registered in the Windows Registry.
**id**: The Event ID for log entries.
**host**: Target machine for logging (empty for local machine).