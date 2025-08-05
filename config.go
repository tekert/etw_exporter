package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
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
	DiskIO        DiskIOConfig        `json:"disk_io"`
	ContextSwitch ContextSwitchConfig `json:"context_switch"`
	// Future collector configs can be added here:
	// Network NetworkConfig `json:"network"`
	// Memory  MemoryConfig  `json:"memory"`
	// CPU     CPUConfig     `json:"cpu"`
}

// DiskIOConfig contains disk I/O collector settings
type DiskIOConfig struct {
	Enabled          bool `json:"enabled"`
	TrackDiskInfo    bool `json:"track_disk_info"`    // Track SystemConfig disk info
	TrackFileMapping bool `json:"track_file_mapping"` // Process->File mapping (heavy)
}

// ContextSwitchConfig contains context switch collector settings
type ContextSwitchConfig struct {
	Enabled bool `json:"enabled"`
}

// GlobalConfig contains global application settings
type GlobalConfig struct {
	LogLevel     string `json:"log_level"`
	BufferSize   int    `json:"buffer_size"`
	FlushTimeout string `json:"flush_timeout"`
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
				Enabled:          true,
				TrackDiskInfo:    true,
				TrackFileMapping: false, // Heavy metric - disabled by default
			},
			ContextSwitch: ContextSwitchConfig{
				Enabled: false, // Disabled by default (high volume)
			},
		},
		Global: GlobalConfig{
			LogLevel:     "info",
			BufferSize:   64, // TODO
			FlushTimeout: "5s", // TODO
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

	if _, err := os.Stat(configPath); os.IsNotExist(err) {
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
func SaveConfig(config *AppConfig, configPath string) error {
	// Create directory if it doesn't exist
	dir := filepath.Dir(configPath)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	// Marshal to JSON
	data, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return fmt.Errorf("failed to marshal config: %w", err)
	}

	// Write to file
	if err := os.WriteFile(configPath, data, 0644); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
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
	if !c.Collectors.DiskIO.Enabled && !c.Collectors.ContextSwitch.Enabled {
		return fmt.Errorf("at least one collector must be enabled")
	}

	return nil
}
