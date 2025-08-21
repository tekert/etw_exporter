package main

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/BurntSushi/toml"
	"github.com/phuslu/log"
)

// Configuration system:
// - CONFIG.md contains detailed documentation
// - config.example.toml is auto-generated using go generate
// - Use brief comments here for reference only

// AppConfig represents the complete application configuration
type AppConfig struct {
	// Server configuration
	Server ServerConfig `toml:"server"`

	// Collector configurations
	Collectors CollectorConfig `toml:"collectors"`

	// Logging configuration
	Logging LoggingConfig `toml:"logging"`
}

// ServerConfig contains HTTP server settings
type ServerConfig struct {
	// Listen address (default: ":9189")
	ListenAddress string `toml:"listen_address"`

	// Metrics endpoint path (default: "/metrics")
	MetricsPath string `toml:"metrics_path"`
}

// CollectorConfig defines which collectors are enabled and their settings
type CollectorConfig struct {
	// Disk I/O collector configuration
	DiskIO DiskIOConfig `toml:"disk_io"`

	// ThreadCS collector configuration
	ThreadCS ThreadCSConfig `toml:"threadcs"`

	// Interrupt latency collector configuration
	PerfInfo InterruptLatencyConfig `toml:"interrupt_latency"`

	// Future collector configs can be added here:
	// Network NetworkConfig `toml:"network"`
	// Memory  MemoryConfig  `toml:"memory"`
	// CPU     CPUConfig     `toml:"cpu"`
}

// DiskIOConfig contains disk I/O collector settings
type DiskIOConfig struct {
	// Enable disk I/O event collection (default: true)
	Enabled bool `toml:"enabled"`

	// Track additional disk information (default: true)
	TrackDiskInfo bool `toml:"track_disk_info"`
}

// ThreadCSConfig contains thread context switch collector settings
type ThreadCSConfig struct {
	// Enable thread context switch event collection (default: true)
	Enabled bool `toml:"enabled"`
}

// InterruptLatencyConfig contains interrupt latency collector settings
type InterruptLatencyConfig struct {
	// Enable interrupt latency event collection (default: true)
	// This enables the System-Wide Latency Metrics (core metrics always on)
	Enabled bool `toml:"enabled"`

	// Enable per-driver latency metrics (default: false, adds per-driver labels)
	EnablePerDriver bool `toml:"enable_per_driver"`

	// Enable per-CPU metrics (default: false, can be high cardinality)
	EnablePerCPU bool `toml:"enable_per_cpu"`

	// Enable ISR/DPC count metrics (default: false, can be high cardinality)
	EnableCounts bool `toml:"enable_counts"`
}

// LoggingConfig contains the complete logging configuration
type LoggingConfig struct {
	// Default logging settings applied to all loggers
	Defaults LogDefaults `toml:"defaults"`

	// Output configurations - can have multiple outputs
	Outputs []LogOutput `toml:"outputs"`

	// ETW library log level (default: "warn")
	LibLevel string `toml:"lib_level"`
}

// LogDefaults contains default logger settings
type LogDefaults struct {
	// Log level (default: "info")
	Level string `toml:"level"`

	// Include caller information (default: 0)
	Caller int `toml:"caller"`

	// Time field name (default: "time")
	TimeField string `toml:"time_field"`

	// Time format (default: "" = RFC3339 with milliseconds)
	TimeFormat string `toml:"time_format"`

	// Time zone (default: "Local")
	TimeLocation string `toml:"time_location"`
}

// LogOutput represents a single output configuration
type LogOutput struct {
	// Output type: "console", "file", "syslog", "eventlog"
	Type string `toml:"type"`

	// Enable this output (default: true)
	Enabled bool `toml:"enabled"`

	// Configuration specific to the output type
	Console  *ConsoleConfig  `toml:"console,omitempty"`
	File     *FileConfig     `toml:"file,omitempty"`
	Syslog   *SyslogConfig   `toml:"syslog,omitempty"`
	Eventlog *EventlogConfig `toml:"eventlog,omitempty"`
}

// ConsoleConfig contains console/terminal output settings
type ConsoleConfig struct {
	// Use fast JSON output (default: false)
	FastIO bool `toml:"fast_io"`

	// Output format when fast_io=false (default: "auto")
	Format string `toml:"format"`

	// Enable colored output (default: true)
	ColorOutput bool `toml:"color_output"`

	// Quote string values (default: true)
	QuoteString bool `toml:"quote_string"`

	// Output destination (default: "stderr")
	Writer string `toml:"writer"`

	// Use asynchronous writing (default: false)
	Async bool `toml:"async"`
}

// FileConfig contains file output settings
type FileConfig struct {
	// Log file path (required)
	Filename string `toml:"filename"`

	// Maximum file size in megabytes (default: 10)
	MaxSize int64 `toml:"max_size"`

	// Maximum number of old log files to keep (default: 7)
	MaxBackups int `toml:"max_backups"`

	// Time format for rotated filenames (default: "2006-01-02T15-04-05")
	TimeFormat string `toml:"time_format"`

	// Use local time for rotation timestamps (default: true)
	LocalTime bool `toml:"local_time"`

	// Include hostname in filename (default: true)
	HostName bool `toml:"host_name"`

	// Include process ID in filename (default: true)
	ProcessID bool `toml:"process_id"`

	// Create directory if it doesn't exist (default: true)
	EnsureFolder bool `toml:"ensure_folder"`

	// Use asynchronous writing (default: true)
	Async bool `toml:"async"`
}

// SyslogConfig contains syslog output settings
type SyslogConfig struct {
	// Network protocol (default: "udp")
	Network string `toml:"network"`

	// Syslog server address (default: "localhost:514")
	Address string `toml:"address"`

	// Hostname for syslog messages (default: system hostname)
	Hostname string `toml:"hostname"`

	// Syslog tag/program name (default: "etw_exporter")
	Tag string `toml:"tag"`

	// Message prefix marker (default: "@cee:")
	Marker string `toml:"marker"`

	// Use asynchronous writing (default: true)
	Async bool `toml:"async"`
}

// EventlogConfig contains Windows Event Log settings
type EventlogConfig struct {
	// Event source name (default: "ETW Exporter")
	Source string `toml:"source"`

	// Event ID for log entries (default: 1000)
	ID int `toml:"id"`

	// Target host (default: local machine)
	Host string `toml:"host"`

	// Use asynchronous writing (default: false)
	Async bool `toml:"async"`
}

// DefaultConfig returns a configuration with sensible defaults
func DefaultConfig() *AppConfig {
	return &AppConfig{
		Server: ServerConfig{
			ListenAddress: "localhost:9189",
			MetricsPath:   "/metrics",
		},
		Collectors: CollectorConfig{
			DiskIO: DiskIOConfig{
				Enabled:       true,
				TrackDiskInfo: true,
			},
			ThreadCS: ThreadCSConfig{
				Enabled: false,
			},
			PerfInfo: InterruptLatencyConfig{
				Enabled:         true,  // Core system-wide metrics enabled by default
				EnablePerDriver: false, // Per-driver metrics disabled by default
				EnablePerCPU:    false, // Disabled by default to reduce cardinality
				EnableCounts:    false, // Disabled by default to reduce cardinality
			},
		},
		Logging: LoggingConfig{
			Defaults: LogDefaults{
				Level:        "info",
				Caller:       0,
				TimeField:    "time",
				TimeFormat:   "",
				TimeLocation: "Local",
			},
			Outputs: []LogOutput{
				{
					Type:    "console",
					Enabled: true,
					Console: &ConsoleConfig{
						FastIO:      false,
						Format:      "auto",
						ColorOutput: true,
						QuoteString: true,
						Writer:      "stderr",
						Async:       false,
					},
				},
				{
					Type:    "file",
					Enabled: false,
					File: &FileConfig{
						Filename:     "logs/app.log",
						MaxSize:      10, // 10MB
						MaxBackups:   7,
						TimeFormat:   "2006-01-02T15-04-05",
						LocalTime:    true,
						HostName:     true,
						ProcessID:    true,
						EnsureFolder: true,
						Async:        true,
					},
				},
				{
					Type:    "syslog",
					Enabled: false,
					Syslog: &SyslogConfig{
						Network:  "udp",
						Address:  "localhost:514",
						Tag:      "etw_exporter",
						Hostname: "", // Uses system hostname by default
						Marker:   "@cee:",
						Async:    true, // Syslog is typically asynchronous
					},
				},
				{
					Type:    "eventlog",
					Enabled: false,
					Eventlog: &EventlogConfig{
						Source: "ETW Exporter",
						ID:     1000,
						Host:   "localhost",
						Async:  false, // Event log is typically synchronous
					},
				},
			},
			LibLevel: "warn",
		},
	}
}

// LoadConfig loads configuration from a TOML file, falling back to defaults
func LoadConfig(configPath string) (*AppConfig, error) {
	config := DefaultConfig()

	// If no config file specified or doesn't exist, use defaults
	if configPath == "" {
		return config, nil
	}

	if _, err := os.Stat(configPath); errors.Is(err, fs.ErrNotExist) {
		return config, nil
	}

	// Parse TOML file
	if _, err := toml.DecodeFile(configPath, config); err != nil {
		return nil, fmt.Errorf("failed to parse config file %s: %w", configPath, err)
	}

	return config, nil
}

// SaveConfig saves the configuration to a TOML file
func SaveConfig(configPath string, config *AppConfig) error {
	// Ensure directory exists
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	// Create file
	file, err := os.Create(configPath)
	if err != nil {
		return fmt.Errorf("failed to create config file %s: %w", configPath, err)
	}
	defer file.Close()

	// Encode to TOML
	if err := toml.NewEncoder(file).Encode(config); err != nil {
		return fmt.Errorf("failed to encode config to TOML: %w", err)
	}

	return nil
}

// GenerateExampleConfig generates a TOML configuration file with default values
func GenerateExampleConfig(outputPath string) error {
	file, err := os.Create(outputPath)
	if err != nil {
		return fmt.Errorf("failed to create output file: %w", err)
	}
	defer file.Close()

	// Write header comments
	header := `# ETW Exporter Example Configuration
# This file is auto-generated and serves as an example configuration.
# For detailed documentation of all configuration options, see CONFIG.md
# Copy this file to create your own configuration and modify as needed.
#
# Format: TOML (Tom's Obvious, Minimal Language)

`
	if _, err := file.WriteString(header); err != nil {
		return fmt.Errorf("failed to write header: %w", err)
	}

	// Create default config and encode to TOML
	config := DefaultConfig()
	encoder := toml.NewEncoder(file)
	if err := encoder.Encode(config); err != nil {
		return fmt.Errorf("failed to encode config to TOML: %w", err)
	}

	return nil
}

// Validate checks the configuration for errors
func (c *AppConfig) Validate() error {
	// Validate server config
	if c.Server.ListenAddress == "" {
		return fmt.Errorf("server.listen_address cannot be empty")
	}
	if c.Server.MetricsPath == "" {
		return fmt.Errorf("server.metrics_path cannot be empty")
	}

	// Validate that at least one collector is enabled
	if !c.Collectors.DiskIO.Enabled && !c.Collectors.ThreadCS.Enabled && !c.Collectors.PerfInfo.Enabled {
		return fmt.Errorf("at least one collector must be enabled")
	}

	// Validate that at least one output is enabled
	hasEnabledOutput := false
	for _, output := range c.Logging.Outputs {
		if output.Enabled {
			hasEnabledOutput = true
			break
		}
	}
	if !hasEnabledOutput {
		return fmt.Errorf("at least one logging output must be enabled")
	}

	return nil
}

// configureLoggers sets up the application and library loggers based on configuration
func configureLoggers(config *AppConfig) error {
	// Configure main application logging
	if err := ConfigureLogging(config.Logging); err != nil {
		return fmt.Errorf("failed to configure application logging: %w", err)
	}

	// Log configuration summary using the new logger
	log.Info().
		Str("app_level", config.Logging.Defaults.Level).
		Str("lib_level", config.Logging.LibLevel).
		Int("outputs", len(config.Logging.Outputs)).
		Msg("🔧 Loggers configured")

	return nil
}
