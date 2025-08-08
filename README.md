# ETW Exporter

A high-performance Windows ETW (Event Tracing for Windows) exporter that exposes kernel metrics for Prometheus monitoring.

## Features

- **Work in progress..**

## Metrics

Work in progress..

## Configuration

### TOML Configuration File
```toml
[server]
listen_address = ":9189"
metrics_path = "/metrics"

[collectors.disk_io]
enabled = true
track_disk_info = true

[logging.defaults]
level = "info"

[[logging.outputs]]
type = "console"
enabled = true
```

See `config.example.toml` for a complete configuration example with detailed comments.

**Note**: The `config.example.toml` file is auto-generated from `CONFIG.md`. To regenerate it:

```bash
# Option 1: Use go generate (recommended)
go generate

# Option 2: Use the main executable directly
go run . -generate-config config.example.toml
```

### Configuration Features
- **Work in progress**

## Usage

### Basic Usage
```powershell
# Run with defaults
.\etw_exporter.exe

# Run with custom configuration
.\etw_exporter.exe -config config.toml

# Override specific settings
.\etw_exporter.exe -config config.toml -web.listen-address ":8080"
```

For detailed configuration options, see [CONFIG.md](CONFIG.md).

### Metrics Endpoint
- **Metrics**: `http://localhost:9189/metrics`
- **Status**: `http://localhost:9189/`


## Development

### Adding New Collectors

Work in progress...

## Requirements

- **Windows**: Administrator privileges required for ETW access
- **Go**: 1.19+ for building from source 
- > (recommended 2.24+ for performance jump on cgo callbacks)
- **Dependencies**: tekert/golang-etw, prometheus/client_golang

## Build

```powershell
go build -v .
```

The binary (`etw_exporter.exe`) can be deployed without external dependencies.
