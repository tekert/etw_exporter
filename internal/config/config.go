package config

import (
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"

	"github.com/BurntSushi/toml"
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

	// Session Watcher configuration
	SessionWatcher SessionWatcherConfig `toml:"session_watcher"`

	// Logging configuration
	Logging LoggingConfig `toml:"logging"`
}

// ServerConfig contains HTTP server settings
type ServerConfig struct {
	// Listen address (default: ":9189")
	ListenAddress string `toml:"listen_address"`

	// Metrics endpoint path (default: "/metrics")
	MetricsPath string `toml:"metrics_path"`

	// Enable pprof endpoint for debugging (default: true)
	PprofEnabled bool `toml:"pprof_enabled"`
}

// SessionWatcherConfig contains settings for monitoring and restarting ETW sessions.
type SessionWatcherConfig struct {
	// Enable the session watcher to automatically restart sessions if they are stopped externally.
	Enabled bool `toml:"enabled"`

	// Automatically restart the NT Kernel Logger session if it stops.
	RestartKernelSession bool `toml:"restart_kernel_session"`

	// Automatically restart the main exporter (manifest) session if it stops.
	RestartExporterSession bool `toml:"restart_exporter_session"`
}

// ProcessFilterConfig contains settings for filtering metrics by process.
type ProcessFilterConfig struct {
	// Enable process filtering (default: false). If false, metrics for all processes are collected.
	Enabled bool `toml:"enabled"`

	// List of regular expressions to match against process names (e.g., "svchost.exe", "sqlservr.*").
	// If enabled, only metrics for processes whose names match one of these patterns will be collected.
	// The format is Go's standard regular expression syntax.
	IncludeNames []string `toml:"include_names"`
}

// ProcessConfig contains settings for the process metadata collector.
type ProcessConfig struct {
	// Enable process metrics (default: true). Provides a mapping from process_start_key to process name and ID.
	Enabled bool `toml:"enabled"`
}

// CollectorConfig defines which collectors are enabled and their settings
type CollectorConfig struct {
	// ProcessFilter configuration
	ProcessFilter ProcessFilterConfig `toml:"process_filter"`

	// Process collector configuration
	Process ProcessConfig `toml:"process"`

	// Disk I/O collector configuration
	DiskIO DiskIOConfig `toml:"disk_io"`

	// ThreadCS collector configuration
	ThreadCS ThreadCSConfig `toml:"threadcs"`

	// PerfInfo collector configuration
	PerfInfo PerfInfoConfig `toml:"perfinfo"`

	// Network collector configuration
	Network NetworkConfig `toml:"network"`

	// Memory collector configuration
	Memory MemoryConfig `toml:"memory"`

	// Registry collector configuration
	Registry RegistryConfig `toml:"registry"`
}

// DiskIOConfig contains disk I/O collector settings
type DiskIOConfig struct {
	// Enable disk I/O event collection (default: true)
	Enabled bool `toml:"enabled"`
}

// ThreadCSConfig contains thread context switch collector settings
type ThreadCSConfig struct {
	// Enable thread context switch event collection (default: true)
	Enabled bool `toml:"enabled"`
}

// PerfInfoConfig contains interrupt latency collector settings
type PerfInfoConfig struct {
	// Enable interrupt latency event collection (default: true)
	// This enables the System-Wide Latency Metrics (core metrics always on)
	Enabled bool `toml:"enabled"`

	// Enable per-driver latency metrics (default: false, adds per-driver labels)
	EnablePerDriver bool `toml:"enable_per_driver"`

	// Enable per-CPU metrics (default: false, can be high cardinality)
	EnablePerCPU bool `toml:"enable_per_cpu"`

	// Enable SMI gap detection using SampledProfile events (default: false).
	// This requires enabling the 'PROFILE' kernel group, which has a minor performance impact.
	EnableSMIDetection bool `toml:"enable_smi_detection"`
}

// NetworkConfig contains network collector settings
type NetworkConfig struct {
	// Enable network event collection (default: false)
	Enabled bool `toml:"enabled"`

	// Enable connection health metrics (default: false, adds connection tracking)
	ConnectionHealth bool `toml:"enable_connection_stats"`

	// Enable protocol-specific metrics (default: false, adds protocol labels)
	ByProtocol bool `toml:"enable_by_protocol"`

	// Enable retransmission rate tracking (default: false, adds retransmission metrics)
	RetransmissionRate bool `toml:"enable_retrasmission_rate"`
}

// MemoryConfig contains memory collector settings
type MemoryConfig struct {
	// Enable memory event collection (default: true)
	Enabled bool `toml:"enabled"`

	// Enable per-process hard page fault metrics (default: true)
	EnablePerProcess bool `toml:"enable_per_process"`
}

// RegistryConfig contains registry collector settings
type RegistryConfig struct {
	// Enable registry event collection (default: false)
	Enabled bool `toml:"enabled"`

	// Enable per-process registry metrics (default: true)
	EnablePerProcess bool `toml:"enable_per_process"`
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
			PprofEnabled:  true,
		},
		Collectors: CollectorConfig{
			ProcessFilter: ProcessFilterConfig{
				Enabled:      false,
				IncludeNames: []string{},
			},
			Process: ProcessConfig{
				Enabled: true,
			},
			DiskIO: DiskIOConfig{
				Enabled: true,
			},
			ThreadCS: ThreadCSConfig{
				Enabled: false,
			},
			PerfInfo: PerfInfoConfig{
				Enabled:            false, // Core system-wide metrics enabled by default
				EnablePerDriver:    false, // Per-driver metrics disabled by default
				EnablePerCPU:       false, // Disabled by default to reduce cardinality
				EnableSMIDetection: false, // Disabled by default as it requires PROFILE kernel group
			},
			Network: NetworkConfig{
				Enabled:            false, // Network event collection disabled by default
				ConnectionHealth:   false, // Connection health metrics disabled by default
				ByProtocol:         false, // Protocol-specific metrics disabled by default
				RetransmissionRate: false, // Retransmission rate tracking disabled by default
			},
			Memory: MemoryConfig{
				Enabled:          true,
				EnablePerProcess: true,
			},
			Registry: RegistryConfig{
				Enabled:          false,
				EnablePerProcess: true,
			},
		},
		SessionWatcher: SessionWatcherConfig{
			Enabled:              true,
			RestartKernelSession: true,
			RestartExporterSession: true,
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
						Host:   "",    // localhost
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
		return config, fmt.Errorf("config file not found: %s", configPath)
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

	// Validate that at least one collector is enabled using reflection.
	// This automatically handles any new collectors added to CollectorConfig.
	v := reflect.ValueOf(c.Collectors)
	oneCollectorEnabled := false
	for i := 0; i < v.NumField(); i++ {
		// Get the 'Enabled' field from each collector's config struct
		enabledField := v.Field(i).FieldByName("Enabled")
		if enabledField.IsValid() && enabledField.Kind() == reflect.Bool {
			if enabledField.Bool() {
				oneCollectorEnabled = true
				break
			}
		}
	}
	if !oneCollectorEnabled {
		return fmt.Errorf("at least one collector must be enabled in the [collectors] section")
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

// Flags holds the command-line flags
type Flags struct {
	ListenAddress  string
	MetricsPath    string
	ConfigPath     string
	GenerateConfig string
}

// NewConfig creates a new configuration by parsing flags and loading the config file.
func NewConfig() (*AppConfig, error) {
	flags := &Flags{}

	// Define flags and bind them to the Flags struct
	flag.StringVar(&flags.ListenAddress,
		"web.listen-address",
		":9189",
		"Address to listen on for web interface and telemetry.")
	flag.StringVar(&flags.MetricsPath,
		"web.telemetry-path",
		"/metrics",
		"Path under which to expose metrics.")
	flag.StringVar(&flags.ConfigPath,
		"config",
		"",
		"Path to configuration file (optional).")
	flag.StringVar(&flags.GenerateConfig,
		"generate-config",
		"",
		"Generate example config file to specified path and exit.")
	flag.Parse()

	// Handle config generation and exit.
	// We return a special error to signal that the program should exit cleanly.
	if flags.GenerateConfig != "" {
		if err := GenerateExampleConfig(flags.GenerateConfig); err != nil {
			return nil, fmt.Errorf("error generating example config: %w", err)
		}
		fmt.Printf("Generated %s successfully\n", flags.GenerateConfig)
		return nil, nil // Signal clean exit
	}

	// Start with default config
	config := DefaultConfig()

	// Load configuration from file if a path is provided
	if flags.ConfigPath != "" {
		var err error
		config, err = LoadConfig(flags.ConfigPath)
		if err != nil {
			return nil, err
		}
	}

	// Override config with command-line flags if they were set by the user
	if isFlagPassed("web.listen-address") {
		config.Server.ListenAddress = flags.ListenAddress
	}
	if isFlagPassed("web.telemetry-path") {
		config.Server.MetricsPath = flags.MetricsPath
	}

	// Validate the final configuration
	if err := config.Validate(); err != nil {
		return nil, fmt.Errorf("invalid configuration: %w", err)
	}

	return config, nil
}

// isFlagPassed checks if a flag was explicitly set on the command line.
func isFlagPassed(name string) bool {
	found := false
	flag.Visit(func(f *flag.Flag) {
		if f.Name == name {
			found = true
		}
	})
	return found
}
