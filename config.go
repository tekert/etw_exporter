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
	// Listen address for the HTTP server (default: ":9189")
	// Examples: ":9189", "localhost:9189", "127.0.0.1:9189"
	ListenAddress string `toml:"listen_address"`

	// Metrics endpoint path (default: "/metrics")
	// This is where Prometheus can scrape metrics
	MetricsPath string `toml:"metrics_path"`
}

// CollectorConfig defines which collectors are enabled and their settings
type CollectorConfig struct {
	// Disk I/O collector configuration
	DiskIO DiskIOConfig `toml:"disk_io"`

	// Thread collector configuration
	Thread ThreadConfig `toml:"thread"`

	// Future collector configs can be added here:
	// Network NetworkConfig `toml:"network"`
	// Memory  MemoryConfig  `toml:"memory"`
	// CPU     CPUConfig     `toml:"cpu"`
}

// DiskIOConfig contains disk I/O collector settings
type DiskIOConfig struct {
	// Enable disk I/O event collection (default: true)
	Enabled bool `toml:"enabled"`

	// Track additional disk information from SystemConfig events (default: true)
	// This provides disk model, size, and other metadata
	TrackDiskInfo bool `toml:"track_disk_info"`
}

// ThreadConfig contains thread collector settings
type ThreadConfig struct {
	// Enable thread event collection (default: true)
	Enabled bool `toml:"enabled"`

	// Track context switch events (default: true)
	// Context switches show when threads are scheduled/descheduled
	ContextSwitches bool `toml:"context_switches"`
}

// LoggingConfig contains the complete logging configuration
type LoggingConfig struct {
	// Default logging settings applied to all loggers
	Defaults LogDefaults `toml:"defaults"`

	// Output configurations - can have multiple outputs
	Outputs []LogOutput `toml:"outputs"`

	// ETW library log level (default: "warn")
	// Levels: "trace", "debug", "info", "warn", "error", "fatal"
	LibLevel string `toml:"lib_level"`
}

// LogDefaults contains default logger settings
type LogDefaults struct {
	// Log level for application loggers (default: "info")
	// Levels: "trace", "debug", "info", "warn", "error", "fatal"
	Level string `toml:"level"`

	// Include caller information in logs (default: 1)
	// 0 = disabled, 1 = file:line, -1 = full path
	Caller int `toml:"caller"`

	// Time field name in log output (default: "time")
	TimeField string `toml:"time_field"`

	// Time format for log timestamps (default: "" = RFC3339 with milliseconds)
	// Examples: "", "2006-01-02T15:04:05", "Unix", "UnixMs"
	TimeFormat string `toml:"time_format"`

	// Time zone for timestamps (default: "Local")
	// Examples: "Local", "UTC", "America/New_York"
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
	// Use fast JSON output via IOWriter instead of formatted console output (default: false)
	// When true: Uses high-performance JSON logging suitable for hot paths
	// When false: Uses formatted, colorized output for human readability
	// NOTE: Hot path modules (disk-io, thread, events) always use fast_io regardless of this setting
	FastIO bool `toml:"fast_io"`

	// Output format when fast_io=false (default: "auto")
	// Formats: "auto", "json", "logfmt", "glog"
	// "auto" = colorized console format with key=value pairs
	Format string `toml:"format"`

	// Enable colored output when fast_io=false (default: true)
	// Only applies to console format, ignored for JSON/logfmt
	ColorOutput bool `toml:"color_output"`

	// Quote string values when fast_io=false (default: true)
	QuoteString bool `toml:"quote_string"`

	// Output destination (default: "stderr")
	// Options: "stdout", "stderr"
	Writer string `toml:"writer"`

	// Use asynchronous writing (default: false)
	// Improves performance but may lose recent logs on crash
	Async bool `toml:"async"`
}

// FileConfig contains file output settings
type FileConfig struct {
	// Log file path (required)
	// Example: "logs/app.log"
	Filename string `toml:"filename"`

	// Maximum file size in bytes before rotation (default: 100MB)
	MaxSize int64 `toml:"max_size"`

	// Maximum number of old log files to keep (default: 7)
	MaxBackups int `toml:"max_backups"`

	// Time format for rotated filenames (default: "2006-01-02T15-04-05")
	// Special values: "Unix", "UnixMs" for timestamps
	TimeFormat string `toml:"time_format"`

	// Use local time for rotation timestamps (default: true)
	LocalTime bool `toml:"local_time"`

	// Include hostname in filename (default: false)
	HostName bool `toml:"host_name"`

	// Include process ID in filename (default: true)
	ProcessID bool `toml:"process_id"`

	// Create directory if it doesn't exist (default: true)
	EnsureFolder bool `toml:"ensure_folder"`

	// Use asynchronous writing (default: true for files)
	// Significantly improves performance for file logging
	Async bool `toml:"async"`
}

// SyslogConfig contains syslog output settings
type SyslogConfig struct {
	// Network protocol (default: "udp")
	// Options: "tcp", "udp", "unixgram"
	Network string `toml:"network"`

	// Syslog server address (default: "localhost:514")
	// Examples: "localhost:514", "syslog.example.com:514"
	Address string `toml:"address"`

	// Hostname for syslog messages (default: system hostname)
	Hostname string `toml:"hostname"`

	// Syslog tag/program name (default: "etw_exporter")
	Tag string `toml:"tag"`

	// Message prefix marker (default: "@cee:")
	// Common values: "@cee:", ""
	Marker string `toml:"marker"`

	// Use asynchronous writing (default: true)
	Async bool `toml:"async"`
}

// EventlogConfig contains Windows Event Log settings
type EventlogConfig struct {
	// Event source name (default: "ETW_Exporter")
	Source string `toml:"source"`

	// Event ID for log entries (default: 1000)
	ID int `toml:"id"`

	// Target host (default: local machine)
	Host string `toml:"host"`

	// Use asynchronous writing (default: false)
	// Event log operations are typically synchronous
	Async bool `toml:"async"`
}

// DefaultConfig returns a configuration with sensible defaults
func DefaultConfig() *AppConfig {
	return &AppConfig{
		Server: ServerConfig{
			ListenAddress: ":9189",
			MetricsPath:   "/metrics",
		},
		Collectors: CollectorConfig{
			DiskIO: DiskIOConfig{
				Enabled:       true,
				TrackDiskInfo: true,
			},
			Thread: ThreadConfig{
				Enabled:         true,
				ContextSwitches: true,
			},
		},
		Logging: LoggingConfig{
			Defaults: LogDefaults{
				Level:        "info",
				Caller:       1,
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
					Enabled: true,
					File: &FileConfig{
						Filename:     "logs/app.log",
						MaxSize:      104857600, // 100MB
						MaxBackups:   7,
						TimeFormat:   "2006-01-02T15-04-05",
						LocalTime:    true,
						HostName:     true,
						ProcessID:    true,
						EnsureFolder: true,
						Async:        true,
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
	if !c.Collectors.DiskIO.Enabled && !c.Collectors.Thread.Enabled {
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
