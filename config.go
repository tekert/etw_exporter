package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/phuslu/log"
)

// AppConfig represents the complete application configuration
type AppConfig struct {
	// Server configuration
	Server ServerConfig `json:"server"`

	// Collector configurations
	Collectors AppCollectorConfig `json:"collectors"`

	// Global settings
	Global GlobalConfig `json:"global"`
}

// ServerConfig contains HTTP server settings
type ServerConfig struct {
	ListenAddress string `json:"listen_address"`
	MetricsPath   string `json:"metrics_path"`
}

// AppCollectorConfig defines which collectors are enabled and their settings
type AppCollectorConfig struct {
	DiskIO DiskIOConfig `json:"disk_io"`
	Thread ThreadConfig `json:"thread"`
	// Future collector configs can be added here:
	// Network NetworkConfig `json:"network"`
	// Memory  MemoryConfig  `json:"memory"`
	// CPU     CPUConfig     `json:"cpu"`
}

// DiskIOConfig contains disk I/O collector settings
type DiskIOConfig struct {
	Enabled       bool `json:"enabled"`
	TrackDiskInfo bool `json:"track_disk_info"` // Track SystemConfig disk info
}

// ThreadConfig contains thread collector settings
type ThreadConfig struct {
	Enabled         bool `json:"enabled"`
	ContextSwitches bool `json:"context_switches"` // Track context switches
}

// GlobalConfig contains global application settings
type GlobalConfig struct {
	// Legacy logging configuration (deprecated, use Logging instead)
	LogLevel    string `json:"log_level,omitempty"`     // Deprecated: Use Logging.Global.Level
	LibLogLevel string `json:"lib_log_level,omitempty"` // Deprecated: Use Logging.LibLevel
	LogFormat   string `json:"log_format,omitempty"`    // Deprecated: Use Logging outputs
	LogColors   bool   `json:"log_colors,omitempty"`    // Deprecated: Use Logging outputs
	LogFile     string `json:"log_file,omitempty"`      // Deprecated: Use Logging outputs

	// New structured logging configuration
	Logging *LoggingConfig `json:"logging,omitempty"` // New structured logging config
}

// LoggingConfig contains the complete logging configuration
type LoggingConfig struct {
	Global   LogGlobalConfig   `json:"global"`   // Global logger settings
	Outputs  []LogOutputConfig `json:"outputs"`  // Output configurations
	LibLevel string            `json:"libLevel"` // ETW library log level
}

// DefaultConfig returns a configuration with sensible defaults
func DefaultConfig() *AppConfig {
	return &AppConfig{
		Server: ServerConfig{
			ListenAddress: ":9189",
			MetricsPath:   "/metrics",
		},
		Collectors: AppCollectorConfig{
			DiskIO: DiskIOConfig{
				Enabled:       true,
				TrackDiskInfo: true,
			},
			Thread: ThreadConfig{
				Enabled:         true,
				ContextSwitches: true, // Track context switches
			},
		},
		Global: GlobalConfig{
			// Legacy configuration for backward compatibility
			LogLevel:    "info",
			LibLogLevel: "warn",
			LogFormat:   "console",
			LogColors:   true,
			LogFile:     "",

			// New structured logging configuration
			Logging: &LoggingConfig{
				Global: LogGlobalConfig{
					Level:        "info",
					Caller:       0,
					TimeField:    "",
					TimeFormat:   "",
					TimeLocation: "Local",
				},
				Outputs: []LogOutputConfig{
					{
						Type:    LogOutputIO,
						Enabled: true,
						Config:  json.RawMessage(`{"writer": "stderr", "async": false}`),
					},
				},
				LibLevel: "warn",
			},
		},
	}
}

// LoadConfig loads configuration from a JSON file, falling back to defaults
func LoadConfig(configPath string) (*AppConfig, error) {
	config := DefaultConfig()

	// If no config file specified or doesn't exist, use defaults
	if configPath == "" {
		return config, nil
	}

	if _, err := os.Stat(configPath); errors.Is(err, fs.ErrNotExist) {
		return config, nil
	}

	// Read the config file
	data, err := os.ReadFile(configPath)
	if err != nil {
		return nil, fmt.Errorf("failed to read config file %s: %w", configPath, err)
	}

	// Parse JSON
	if err := json.Unmarshal(data, config); err != nil {
		return nil, fmt.Errorf("failed to parse config file %s: %w", configPath, err)
	}

	return config, nil
}

// SaveConfig saves the configuration to a JSON file
func SaveConfig(configPath string, config *AppConfig) error {
	// Ensure directory exists
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create directory %s: %w", dir, err)
	}

	// Marshal to JSON with indentation for readability
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Write to file
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file %s: %w", configPath, err)
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

	return nil
}

// GetLoggingConfig returns the effective logging configuration, handling backward compatibility
func (gc *GlobalConfig) GetLoggingConfig() LoggingConfig {
	// If new logging config is present, use it
	if gc.Logging != nil {
		return *gc.Logging
	}

	// Convert legacy configuration to new format
	var outputs []LogOutputConfig

	// Determine output type based on legacy config
	if gc.LogFile != "" {
		// File output
		config := map[string]interface{}{
			"filename":     gc.LogFile,
			"fileMode":     0644,
			"maxSize":      104857600, // 100MB
			"maxBackups":   7,
			"timeFormat":   "2006-01-02T15-04-05",
			"localTime":    true,
			"hostName":     false,
			"processID":    true,
			"ensureFolder": true,
			"async":        true,
		}
		configJSON, _ := json.Marshal(config)
		outputs = append(outputs, LogOutputConfig{
			Type:    LogOutputFile,
			Enabled: true,
			Config:  json.RawMessage(configJSON),
		})
	} else if gc.LogFormat == "console" {
		// Console output
		config := map[string]interface{}{
			"colorOutput":    gc.LogColors,
			"quoteString":    true,
			"endWithMessage": true,
			"format":         "json",
			"writer":         "stderr",
			"async":          false,
		}
		configJSON, _ := json.Marshal(config)
		outputs = append(outputs, LogOutputConfig{
			Type:    LogOutputConsole,
			Enabled: true,
			Config:  json.RawMessage(configJSON),
		})
	} else {
		// Default to IO output
		config := map[string]any{
			"writer": "stderr",
			"async":  false,
		}
		configJSON, _ := json.Marshal(config)
		outputs = append(outputs, LogOutputConfig{
			Type:    LogOutputIO,
			Enabled: true,
			Config:  json.RawMessage(configJSON),
		})
	}

	return LoggingConfig{
		Global: LogGlobalConfig{
			Level:        gc.LogLevel,
			Caller:       1,
			TimeField:    "",
			TimeFormat:   "",
			TimeLocation: "Local",
		},
		Outputs:  outputs,
		LibLevel: gc.LibLogLevel,
	}
}

// configureLoggers sets up the application and library loggers based on configuration
func configureLoggers(config *AppConfig) error {
	loggingConfig := config.Global.GetLoggingConfig()

	// Configure main application logging
	if err := ConfigureLogging(LogConfig{
		Global:  loggingConfig.Global,
		Outputs: loggingConfig.Outputs,
	}); err != nil {
		return fmt.Errorf("failed to configure application logging: %w", err)
	}

	// Configure ETW library logging (use same outputs but potentially different level)
	if err := ConfigureETWLibraryLogger(loggingConfig.LibLevel, loggingConfig.Outputs); err != nil {
		return fmt.Errorf("failed to configure ETW library logging: %w", err)
	}

	// Log configuration summary using the new logger
	log.Info().
		Str("app_level", loggingConfig.Global.Level).
		Str("lib_level", loggingConfig.LibLevel).
		Int("outputs", len(loggingConfig.Outputs)).
		Msg("🔧 Loggers configured")

	return nil
}
