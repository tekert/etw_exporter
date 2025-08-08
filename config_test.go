package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// TestDefaultConfig tests that DefaultConfig returns expected values
func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

	// Test server defaults
	if config.Server.ListenAddress != ":9189" {
		t.Errorf("Expected ListenAddress ':9189', got '%s'", config.Server.ListenAddress)
	}
	if config.Server.MetricsPath != "/metrics" {
		t.Errorf("Expected MetricsPath '/metrics', got '%s'", config.Server.MetricsPath)
	}

	// Test collector defaults
	if !config.Collectors.DiskIO.Enabled {
		t.Error("Expected DiskIO to be enabled by default")
	}
	if !config.Collectors.DiskIO.TrackDiskInfo {
		t.Error("Expected TrackDiskInfo to be enabled by default")
	}
	if !config.Collectors.ThreadCS.Enabled {
		t.Error("Expected ThreadCS to be enabled by default")
	}

	// Test logging defaults
	if config.Logging.Defaults.Level != "info" {
		t.Errorf("Expected default level 'info', got '%s'", config.Logging.Defaults.Level)
	}
	if config.Logging.Defaults.Caller != 0 {
		t.Errorf("Expected default caller 0, got %d", config.Logging.Defaults.Caller)
	}
	if config.Logging.Defaults.TimeField != "time" {
		t.Errorf("Expected default time_field 'time', got '%s'", config.Logging.Defaults.TimeField)
	}
	if config.Logging.Defaults.TimeFormat != "" {
		t.Errorf("Expected default time_format '', got '%s'", config.Logging.Defaults.TimeFormat)
	}
	if config.Logging.Defaults.TimeLocation != "Local" {
		t.Errorf("Expected default time_location 'Local', got '%s'", config.Logging.Defaults.TimeLocation)
	}
	if config.Logging.LibLevel != "warn" {
		t.Errorf("Expected default lib_level 'warn', got '%s'", config.Logging.LibLevel)
	}

	// Test that we have expected outputs
	if len(config.Logging.Outputs) != 4 {
		t.Errorf("Expected 4 default outputs, got %d", len(config.Logging.Outputs))
	}

	// Test console output defaults
	var consoleOutput *LogOutput
	for i := range config.Logging.Outputs {
		if config.Logging.Outputs[i].Type == "console" {
			consoleOutput = &config.Logging.Outputs[i]
			break
		}
	}
	if consoleOutput == nil {
		t.Fatal("Console output not found in defaults")
	}
	if !consoleOutput.Enabled {
		t.Error("Console output should be enabled by default")
	}
	if consoleOutput.Console == nil {
		t.Fatal("Console config should not be nil")
	}
	if consoleOutput.Console.FastIO {
		t.Error("Console FastIO should be false by default")
	}
	if consoleOutput.Console.Format != "auto" {
		t.Errorf("Expected console format 'auto', got '%s'", consoleOutput.Console.Format)
	}
	if !consoleOutput.Console.ColorOutput {
		t.Error("Console ColorOutput should be true by default")
	}
	if !consoleOutput.Console.QuoteString {
		t.Error("Console QuoteString should be true by default")
	}
	if consoleOutput.Console.Writer != "stderr" {
		t.Errorf("Expected console writer 'stderr', got '%s'", consoleOutput.Console.Writer)
	}
	if consoleOutput.Console.Async {
		t.Error("Console Async should be false by default")
	}

	// Test file output defaults
	var fileOutput *LogOutput
	for i := range config.Logging.Outputs {
		if config.Logging.Outputs[i].Type == "file" {
			fileOutput = &config.Logging.Outputs[i]
			break
		}
	}
	if fileOutput == nil {
		t.Fatal("File output not found in defaults")
	}
	if fileOutput.Enabled {
		t.Error("File output should be disabled by default")
	}
	if fileOutput.File == nil {
		t.Fatal("File config should not be nil")
	}
	if fileOutput.File.Filename != "logs/app.log" {
		t.Errorf("Expected file filename 'logs/app.log', got '%s'", fileOutput.File.Filename)
	}
	if fileOutput.File.MaxSize != 10 {
		t.Errorf("Expected file max_size 10, got %d", fileOutput.File.MaxSize)
	}
	if fileOutput.File.MaxBackups != 7 {
		t.Errorf("Expected file max_backups 7, got %d", fileOutput.File.MaxBackups)
	}
	if fileOutput.File.TimeFormat != "2006-01-02T15-04-05" {
		t.Errorf("Expected file time_format '2006-01-02T15-04-05', got '%s'", fileOutput.File.TimeFormat)
	}
	if !fileOutput.File.LocalTime {
		t.Error("File LocalTime should be true by default")
	}
	if !fileOutput.File.HostName {
		t.Error("File HostName should be true by default")
	}
	if !fileOutput.File.ProcessID {
		t.Error("File ProcessID should be true by default")
	}
	if !fileOutput.File.EnsureFolder {
		t.Error("File EnsureFolder should be true by default")
	}
	if !fileOutput.File.Async {
		t.Error("File Async should be true by default")
	}

	// Test syslog output defaults
	var syslogOutput *LogOutput
	for i := range config.Logging.Outputs {
		if config.Logging.Outputs[i].Type == "syslog" {
			syslogOutput = &config.Logging.Outputs[i]
			break
		}
	}
	if syslogOutput == nil {
		t.Fatal("Syslog output not found in defaults")
	}
	if syslogOutput.Enabled {
		t.Error("Syslog output should be disabled by default")
	}
	if syslogOutput.Syslog == nil {
		t.Fatal("Syslog config should not be nil")
	}
	if syslogOutput.Syslog.Network != "udp" {
		t.Errorf("Expected syslog network 'udp', got '%s'", syslogOutput.Syslog.Network)
	}
	if syslogOutput.Syslog.Address != "localhost:514" {
		t.Errorf("Expected syslog address 'localhost:514', got '%s'", syslogOutput.Syslog.Address)
	}
	if syslogOutput.Syslog.Tag != "etw_exporter" {
		t.Errorf("Expected syslog tag 'etw_exporter', got '%s'", syslogOutput.Syslog.Tag)
	}
	if syslogOutput.Syslog.Hostname != "" {
		t.Errorf("Expected syslog hostname '', got '%s'", syslogOutput.Syslog.Hostname)
	}
	if syslogOutput.Syslog.Marker != "@cee:" {
		t.Errorf("Expected syslog marker '@cee:', got '%s'", syslogOutput.Syslog.Marker)
	}
	if !syslogOutput.Syslog.Async {
		t.Error("Syslog Async should be true by default")
	}

	// Test eventlog output defaults
	var eventlogOutput *LogOutput
	for i := range config.Logging.Outputs {
		if config.Logging.Outputs[i].Type == "eventlog" {
			eventlogOutput = &config.Logging.Outputs[i]
			break
		}
	}
	if eventlogOutput == nil {
		t.Fatal("Eventlog output not found in defaults")
	}
	if eventlogOutput.Enabled {
		t.Error("Eventlog output should be disabled by default")
	}
	if eventlogOutput.Eventlog == nil {
		t.Fatal("Eventlog config should not be nil")
	}
	if eventlogOutput.Eventlog.Source != "ETW Exporter" {
		t.Errorf("Expected eventlog source 'ETW Exporter', got '%s'", eventlogOutput.Eventlog.Source)
	}
	if eventlogOutput.Eventlog.ID != 1000 {
		t.Errorf("Expected eventlog ID 1000, got %d", eventlogOutput.Eventlog.ID)
	}
	if eventlogOutput.Eventlog.Host != "localhost" {
		t.Errorf("Expected eventlog host 'localhost', got '%s'", eventlogOutput.Eventlog.Host)
	}
	if eventlogOutput.Eventlog.Async {
		t.Error("Eventlog Async should be false by default")
	}
}

// TestLoadConfigNonExistent tests loading a non-existent config file returns defaults
func TestLoadConfigNonExistent(t *testing.T) {
	config, err := LoadConfig("nonexistent.toml")
	if err != nil {
		t.Fatalf("Expected no error for non-existent config, got: %v", err)
	}

	// Should be equivalent to defaults
	defaultConfig := DefaultConfig()
	if config.Server.ListenAddress != defaultConfig.Server.ListenAddress {
		t.Error("Non-existent config should return defaults")
	}
}

// TestLoadConfigEmpty tests loading with empty path returns defaults
func TestLoadConfigEmpty(t *testing.T) {
	config, err := LoadConfig("")
	if err != nil {
		t.Fatalf("Expected no error for empty config path, got: %v", err)
	}

	// Should be equivalent to defaults
	defaultConfig := DefaultConfig()
	if config.Server.ListenAddress != defaultConfig.Server.ListenAddress {
		t.Error("Empty config path should return defaults")
	}
}

// TestLoadConfigValid tests loading a valid config file
func TestLoadConfigValid(t *testing.T) {
	// Create a temporary config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "test_config.toml")

	configContent := `
[server]
listen_address = ":8080"
metrics_path = "/test_metrics"

[collectors]
[collectors.disk_io]
enabled = false
track_disk_info = false

[collectors.threadcs]
enabled = false

[logging]
lib_level = "debug"

[logging.defaults]
level = "debug"
caller = 1
time_field = "timestamp"
time_format = "Unix"
time_location = "UTC"

[[logging.outputs]]
type = "console"
enabled = true

[logging.outputs.console]
fast_io = true
format = "json"
color_output = false
quote_string = false
writer = "stdout"
async = true
`

	err := os.WriteFile(configPath, []byte(configContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create test config file: %v", err)
	}

	config, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("Expected no error loading valid config, got: %v", err)
	}

	// Debug: Print the actual loaded config
	t.Logf("Loaded lib_level: %s", config.Logging.LibLevel)
	t.Logf("Loaded level: %s", config.Logging.Defaults.Level)

	// Test that values were loaded correctly
	if config.Server.ListenAddress != ":8080" {
		t.Errorf("Expected ListenAddress ':8080', got '%s'", config.Server.ListenAddress)
	}
	if config.Server.MetricsPath != "/test_metrics" {
		t.Errorf("Expected MetricsPath '/test_metrics', got '%s'", config.Server.MetricsPath)
	}

	if config.Collectors.DiskIO.Enabled {
		t.Error("Expected DiskIO to be disabled")
	}
	if config.Collectors.DiskIO.TrackDiskInfo {
		t.Error("Expected TrackDiskInfo to be disabled")
	}
	if config.Collectors.ThreadCS.Enabled {
		t.Error("Expected ThreadCS to be disabled")
	}

	if config.Logging.Defaults.Level != "debug" {
		t.Errorf("Expected level 'debug', got '%s'", config.Logging.Defaults.Level)
	}
	if config.Logging.Defaults.Caller != 1 {
		t.Errorf("Expected caller 1, got %d", config.Logging.Defaults.Caller)
	}
	if config.Logging.Defaults.TimeField != "timestamp" {
		t.Errorf("Expected time_field 'timestamp', got '%s'", config.Logging.Defaults.TimeField)
	}
	if config.Logging.Defaults.TimeFormat != "Unix" {
		t.Errorf("Expected time_format 'Unix', got '%s'", config.Logging.Defaults.TimeFormat)
	}
	if config.Logging.Defaults.TimeLocation != "UTC" {
		t.Errorf("Expected time_location 'UTC', got '%s'", config.Logging.Defaults.TimeLocation)
	}
	if config.Logging.LibLevel != "debug" {
		t.Errorf("Expected lib_level 'debug', got '%s'", config.Logging.LibLevel)
	}

	// Test console output was loaded correctly
	if len(config.Logging.Outputs) < 1 {
		t.Fatal("Expected at least 1 output")
	}

	consoleOutput := &config.Logging.Outputs[0]
	if consoleOutput.Type != "console" {
		t.Errorf("Expected first output type 'console', got '%s'", consoleOutput.Type)
	}
	if !consoleOutput.Enabled {
		t.Error("Expected console output to be enabled")
	}
	if consoleOutput.Console == nil {
		t.Fatal("Console config should not be nil")
	}
	if !consoleOutput.Console.FastIO {
		t.Error("Expected console FastIO to be true")
	}
	if consoleOutput.Console.Format != "json" {
		t.Errorf("Expected console format 'json', got '%s'", consoleOutput.Console.Format)
	}
	if consoleOutput.Console.ColorOutput {
		t.Error("Expected console ColorOutput to be false")
	}
	if consoleOutput.Console.QuoteString {
		t.Error("Expected console QuoteString to be false")
	}
	if consoleOutput.Console.Writer != "stdout" {
		t.Errorf("Expected console writer 'stdout', got '%s'", consoleOutput.Console.Writer)
	}
	if !consoleOutput.Console.Async {
		t.Error("Expected console Async to be true")
	}
}

// TestLoadConfigInvalid tests loading an invalid config file returns an error
func TestLoadConfigInvalid(t *testing.T) {
	// Create a temporary invalid config file
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "invalid_config.toml")

	invalidContent := `
[server]
listen_address = ":8080"
invalid_toml_syntax [
`

	err := os.WriteFile(configPath, []byte(invalidContent), 0644)
	if err != nil {
		t.Fatalf("Failed to create invalid config file: %v", err)
	}

	_, err = LoadConfig(configPath)
	if err == nil {
		t.Fatal("Expected error loading invalid config, got nil")
	}

	if !strings.Contains(err.Error(), "failed to parse config file") {
		t.Errorf("Expected parse error message, got: %v", err)
	}
}

// TestSaveConfig tests saving a config to a file
func TestSaveConfig(t *testing.T) {
	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "subdir", "test_save.toml")

	config := DefaultConfig()
	config.Server.ListenAddress = ":7777"
	config.Server.MetricsPath = "/save_test"

	err := SaveConfig(configPath, config)
	if err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	// Verify file was created
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		t.Fatal("Config file was not created")
	}

	// Load it back and verify
	loadedConfig, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("Failed to load saved config: %v", err)
	}

	if loadedConfig.Server.ListenAddress != ":7777" {
		t.Errorf("Expected loaded ListenAddress ':7777', got '%s'", loadedConfig.Server.ListenAddress)
	}
	if loadedConfig.Server.MetricsPath != "/save_test" {
		t.Errorf("Expected loaded MetricsPath '/save_test', got '%s'", loadedConfig.Server.MetricsPath)
	}
}

// TestSaveConfigInvalidPath tests saving to an invalid path
func TestSaveConfigInvalidPath(t *testing.T) {
	config := DefaultConfig()

	// Try to save to a path with invalid characters (Windows-specific)
	invalidPath := "C:\\invalid\x00path\\config.toml"

	err := SaveConfig(invalidPath, config)
	if err == nil {
		// If the invalid path test doesn't work on this system,
		// try a different approach - create a readonly directory
		tmpDir := t.TempDir()
		readonlyDir := filepath.Join(tmpDir, "readonly")
		os.MkdirAll(readonlyDir, 0444) // Read-only permissions

		invalidPath2 := filepath.Join(readonlyDir, "config.toml")
		err2 := SaveConfig(invalidPath2, config)
		if err2 == nil {
			t.Skip("Unable to create test conditions for invalid save path on this system")
		}
	}
}

// TestConfigValidation tests the Validate method
func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name      string
		setupFunc func(*AppConfig)
		expectErr bool
		errorMsg  string
	}{
		{
			name:      "valid config",
			setupFunc: func(c *AppConfig) {}, // No changes, use defaults
			expectErr: false,
		},
		{
			name: "empty listen address",
			setupFunc: func(c *AppConfig) {
				c.Server.ListenAddress = ""
			},
			expectErr: true,
			errorMsg:  "server.listen_address cannot be empty",
		},
		{
			name: "empty metrics path",
			setupFunc: func(c *AppConfig) {
				c.Server.MetricsPath = ""
			},
			expectErr: true,
			errorMsg:  "server.metrics_path cannot be empty",
		},
		{
			name: "no collectors enabled",
			setupFunc: func(c *AppConfig) {
				c.Collectors.DiskIO.Enabled = false
				c.Collectors.ThreadCS.Enabled = false
			},
			expectErr: true,
			errorMsg:  "at least one collector must be enabled",
		},
		{
			name: "no logging outputs enabled",
			setupFunc: func(c *AppConfig) {
				for i := range c.Logging.Outputs {
					c.Logging.Outputs[i].Enabled = false
				}
			},
			expectErr: true,
			errorMsg:  "at least one logging output must be enabled",
		},
		{
			name: "only disk_io enabled",
			setupFunc: func(c *AppConfig) {
				c.Collectors.DiskIO.Enabled = true
				c.Collectors.ThreadCS.Enabled = false
			},
			expectErr: false,
		},
		{
			name: "only threadcs enabled",
			setupFunc: func(c *AppConfig) {
				c.Collectors.DiskIO.Enabled = false
				c.Collectors.ThreadCS.Enabled = true
			},
			expectErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := DefaultConfig()
			tt.setupFunc(config)

			err := config.Validate()
			if tt.expectErr {
				if err == nil {
					t.Errorf("Expected error for %s, got nil", tt.name)
				} else if !strings.Contains(err.Error(), tt.errorMsg) {
					t.Errorf("Expected error message to contain '%s', got: %v", tt.errorMsg, err)
				}
			} else {
				if err != nil {
					t.Errorf("Expected no error for %s, got: %v", tt.name, err)
				}
			}
		})
	}
}

// TestCompleteLoggingConfigurations tests all possible logging output configurations
func TestCompleteLoggingConfigurations(t *testing.T) {
	testCases := []struct {
		name        string
		configTOML  string
		expectError bool
	}{
		{
			name: "console_fast_io_json",
			configTOML: `
[server]
listen_address = ":9189"
metrics_path = "/metrics"

[collectors.disk_io]
enabled = true

[logging]
[logging.defaults]
level = "trace"

[[logging.outputs]]
type = "console"
enabled = true

[logging.outputs.console]
fast_io = true
format = "json"
writer = "stdout"
async = false
`,
			expectError: false,
		},
		{
			name: "console_all_formats",
			configTOML: `
[server]
listen_address = ":9189"
metrics_path = "/metrics"

[collectors.disk_io]
enabled = true

[logging]
[logging.defaults]
level = "debug"
caller = -1
time_field = "ts"
time_format = "UnixMs"
time_location = "UTC"

[[logging.outputs]]
type = "console"
enabled = true

[logging.outputs.console]
fast_io = false
format = "logfmt"
color_output = false
quote_string = false
writer = "stderr"
async = true
`,
			expectError: false,
		},
		{
			name: "file_configuration",
			configTOML: `
[server]
listen_address = ":9189"
metrics_path = "/metrics"

[collectors.threadcs]
enabled = true

[logging]
[logging.defaults]
level = "warn"

[[logging.outputs]]
type = "file"
enabled = true

[logging.outputs.file]
filename = "test_logs/test.log"
max_size = 50
max_backups = 10
time_format = "Unix"
local_time = false
host_name = false
process_id = false
ensure_folder = true
async = false
`,
			expectError: false,
		},
		{
			name: "syslog_tcp_configuration",
			configTOML: `
[server]
listen_address = ":9189"
metrics_path = "/metrics"

[collectors.disk_io]
enabled = true

[logging]
[logging.defaults]
level = "error"

[[logging.outputs]]
type = "syslog"
enabled = true

[logging.outputs.syslog]
network = "tcp"
address = "syslog.example.com:514"
hostname = "test-host"
tag = "etw_test"
marker = ""
async = false
`,
			expectError: false,
		},
		{
			name: "syslog_unixgram_configuration",
			configTOML: `
[server]
listen_address = ":9189"
metrics_path = "/metrics"

[collectors.disk_io]
enabled = true

[logging]
[[logging.outputs]]
type = "syslog"
enabled = true

[logging.outputs.syslog]
network = "unixgram"
address = "/dev/log"
hostname = ""
tag = "etw_exporter"
marker = "@cee:"
async = true
`,
			expectError: false,
		},
		{
			name: "eventlog_configuration",
			configTOML: `
[server]
listen_address = ":9189"
metrics_path = "/metrics"

[collectors.threadcs]
enabled = true

[logging]
[logging.defaults]
level = "fatal"

[[logging.outputs]]
type = "eventlog"
enabled = true

[logging.outputs.eventlog]
source = "Custom_ETW_Exporter"
id = 2000
host = "remote-host"
async = true
`,
			expectError: false,
		},
		{
			name: "multiple_outputs_mixed",
			configTOML: `
[server]
listen_address = ":9189"
metrics_path = "/metrics"

[collectors.disk_io]
enabled = true
track_disk_info = false

[collectors.threadcs]
enabled = true

[logging]
[logging.defaults]
level = "info"
caller = 1
time_field = "time"
time_format = "2006-01-02T15:04:05.000Z"
time_location = "America/New_York"

lib_level = "trace"

[[logging.outputs]]
type = "console"
enabled = true

[logging.outputs.console]
fast_io = false
format = "glog"
color_output = true
quote_string = true
writer = "stderr"
async = false

[[logging.outputs]]
type = "file"
enabled = true

[logging.outputs.file]
filename = "logs/mixed.log"
max_size = 25
max_backups = 5
time_format = "2006-01-02T15-04-05"
local_time = true
host_name = true
process_id = true
ensure_folder = true
async = true

[[logging.outputs]]
type = "syslog"
enabled = false

[logging.outputs.syslog]
network = "udp"
address = "localhost:514"
hostname = ""
tag = "etw_exporter"
marker = "@cee:"
async = true
`,
			expectError: false,
		},
		{
			name: "all_log_levels",
			configTOML: `
[server]
listen_address = ":9189"
metrics_path = "/metrics"

[collectors.disk_io]
enabled = true

[logging]
[logging.defaults]
level = "trace"

lib_level = "fatal"

[[logging.outputs]]
type = "console"
enabled = true
`,
			expectError: false,
		},
		{
			name: "missing_file_config",
			configTOML: `
[server]
listen_address = ":9189"
metrics_path = "/metrics"

[collectors.disk_io]
enabled = true

[logging]
[[logging.outputs]]
type = "file"
enabled = true
# Missing [logging.outputs.file] section
`,
			expectError: false, // Should work with defaults from TOML decoder
		},
		{
			name: "custom_time_zones",
			configTOML: `
[server]
listen_address = ":9189"
metrics_path = "/metrics"

[collectors.disk_io]
enabled = true

[logging]
[logging.defaults]
time_location = "Europe/London"

[[logging.outputs]]
type = "console"
enabled = true
`,
			expectError: false,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			// Create temporary config file
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "test.toml")

			err := os.WriteFile(configPath, []byte(tc.configTOML), 0644)
			if err != nil {
				t.Fatalf("Failed to write test config: %v", err)
			}

			// Load config
			config, err := LoadConfig(configPath)
			if tc.expectError {
				if err == nil {
					t.Errorf("Expected error for %s, got nil", tc.name)
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error loading config for %s: %v", tc.name, err)
			}

			// Validate the configuration
			err = config.Validate()
			if err != nil {
				t.Errorf("Configuration validation failed for %s: %v", tc.name, err)
			}

			// Test that the config can be marshaled back to TOML
			_, err = toml.Marshal(config)
			if err != nil {
				t.Errorf("Failed to marshal config back to TOML for %s: %v", tc.name, err)
			}
		})
	}
}

// TestTOMLRoundTrip tests that a config can be saved and loaded without data loss
func TestTOMLRoundTrip(t *testing.T) {
	originalConfig := DefaultConfig()

	// Modify some values to test serialization
	originalConfig.Server.ListenAddress = ":8888"
	originalConfig.Collectors.DiskIO.TrackDiskInfo = false
	originalConfig.Logging.Defaults.Level = "debug"
	originalConfig.Logging.Defaults.Caller = -1
	originalConfig.Logging.Defaults.TimeFormat = "UnixMs"
	originalConfig.Logging.LibLevel = "trace"

	tmpDir := t.TempDir()
	configPath := filepath.Join(tmpDir, "roundtrip.toml")

	// Save config
	err := SaveConfig(configPath, originalConfig)
	if err != nil {
		t.Fatalf("Failed to save config: %v", err)
	}

	// Load config back
	loadedConfig, err := LoadConfig(configPath)
	if err != nil {
		t.Fatalf("Failed to load config: %v", err)
	}

	// Compare key values
	if originalConfig.Server.ListenAddress != loadedConfig.Server.ListenAddress {
		t.Errorf("ListenAddress mismatch: original=%s, loaded=%s",
			originalConfig.Server.ListenAddress, loadedConfig.Server.ListenAddress)
	}
	if originalConfig.Collectors.DiskIO.TrackDiskInfo != loadedConfig.Collectors.DiskIO.TrackDiskInfo {
		t.Errorf("TrackDiskInfo mismatch: original=%t, loaded=%t",
			originalConfig.Collectors.DiskIO.TrackDiskInfo, loadedConfig.Collectors.DiskIO.TrackDiskInfo)
	}
	if originalConfig.Logging.Defaults.Level != loadedConfig.Logging.Defaults.Level {
		t.Errorf("Logging level mismatch: original=%s, loaded=%s",
			originalConfig.Logging.Defaults.Level, loadedConfig.Logging.Defaults.Level)
	}
	if originalConfig.Logging.Defaults.Caller != loadedConfig.Logging.Defaults.Caller {
		t.Errorf("Logging caller mismatch: original=%d, loaded=%d",
			originalConfig.Logging.Defaults.Caller, loadedConfig.Logging.Defaults.Caller)
	}
	if originalConfig.Logging.Defaults.TimeFormat != loadedConfig.Logging.Defaults.TimeFormat {
		t.Errorf("Logging time format mismatch: original=%s, loaded=%s",
			originalConfig.Logging.Defaults.TimeFormat, loadedConfig.Logging.Defaults.TimeFormat)
	}
	if originalConfig.Logging.LibLevel != loadedConfig.Logging.LibLevel {
		t.Errorf("Lib level mismatch: original=%s, loaded=%s",
			originalConfig.Logging.LibLevel, loadedConfig.Logging.LibLevel)
	}
}

// TestConfigEdgeCases tests edge cases and boundary conditions
func TestConfigEdgeCases(t *testing.T) {
	t.Run("zero_max_size", func(t *testing.T) {
		config := DefaultConfig()
		config.Logging.Outputs[1].File.MaxSize = 0

		err := config.Validate()
		if err != nil {
			t.Errorf("Config with zero max_size should be valid: %v", err)
		}
	})

	t.Run("zero_max_backups", func(t *testing.T) {
		config := DefaultConfig()
		config.Logging.Outputs[1].File.MaxBackups = 0

		err := config.Validate()
		if err != nil {
			t.Errorf("Config with zero max_backups should be valid: %v", err)
		}
	})

	t.Run("negative_caller", func(t *testing.T) {
		config := DefaultConfig()
		config.Logging.Defaults.Caller = -100

		err := config.Validate()
		if err != nil {
			t.Errorf("Config with negative caller should be valid: %v", err)
		}
	})

	t.Run("empty_time_field", func(t *testing.T) {
		config := DefaultConfig()
		config.Logging.Defaults.TimeField = ""

		err := config.Validate()
		if err != nil {
			t.Errorf("Config with empty time field should be valid: %v", err)
		}
	})

	t.Run("very_long_strings", func(t *testing.T) {
		longString := strings.Repeat("a", 10000)
		config := DefaultConfig()
		config.Server.ListenAddress = longString
		config.Server.MetricsPath = longString

		err := config.Validate()
		if err != nil {
			t.Errorf("Config with very long strings should be valid: %v", err)
		}
	})
}

// TestSpecialTimeFormats tests special time format handling
func TestSpecialTimeFormats(t *testing.T) {
	timeFormats := []string{
		"",                           // Default RFC3339 with milliseconds
		"Unix",                       // Unix timestamp
		"UnixMs",                     // Unix timestamp with milliseconds
		"2006-01-02T15:04:05",       // Custom format
		"2006-01-02 15:04:05.000",   // Custom format with milliseconds
		"15:04:05",                   // Time only
		"2006-01-02",                 // Date only
	}

	for _, format := range timeFormats {
		t.Run("time_format_"+format, func(t *testing.T) {
			config := DefaultConfig()
			config.Logging.Defaults.TimeFormat = format

			err := config.Validate()
			if err != nil {
				t.Errorf("Config with time format '%s' should be valid: %v", format, err)
			}

			// Test that it can be saved and loaded
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "time_format_test.toml")

			err = SaveConfig(configPath, config)
			if err != nil {
				t.Errorf("Failed to save config with time format '%s': %v", format, err)
			}

			loadedConfig, err := LoadConfig(configPath)
			if err != nil {
				t.Errorf("Failed to load config with time format '%s': %v", format, err)
			}

			if loadedConfig.Logging.Defaults.TimeFormat != format {
				t.Errorf("Time format not preserved: expected '%s', got '%s'",
					format, loadedConfig.Logging.Defaults.TimeFormat)
			}
		})
	}
}

// TestAllLogLevels tests all supported log levels
func TestAllLogLevels(t *testing.T) {
	logLevels := []string{
		"trace", "debug", "info", "warn", "error", "fatal",
		"warning", // Alias for warn
	}

	for _, level := range logLevels {
		t.Run("log_level_"+level, func(t *testing.T) {
			config := DefaultConfig()
			config.Logging.Defaults.Level = level
			config.Logging.LibLevel = level

			err := config.Validate()
			if err != nil {
				t.Errorf("Config with log level '%s' should be valid: %v", level, err)
			}
		})
	}
}

// TestTimeZones tests various time zone configurations
func TestTimeZones(t *testing.T) {
	timeZones := []string{
		"Local", "UTC",
		"America/New_York", "Europe/London", "Asia/Tokyo",
		"America/Los_Angeles", "Europe/Paris",
	}

	for _, tz := range timeZones {
		t.Run("timezone_"+strings.ReplaceAll(tz, "/", "_"), func(t *testing.T) {
			config := DefaultConfig()
			config.Logging.Defaults.TimeLocation = tz

			err := config.Validate()
			if err != nil {
				t.Errorf("Config with timezone '%s' should be valid: %v", tz, err)
			}
		})
	}
}

// BenchmarkDefaultConfig benchmarks the DefaultConfig function
func BenchmarkDefaultConfig(b *testing.B) {
	for i := 0; i < b.N; i++ {
		_ = DefaultConfig()
	}
}

// BenchmarkLoadConfig benchmarks loading a config file
func BenchmarkLoadConfig(b *testing.B) {
	tmpDir := b.TempDir()
	configPath := filepath.Join(tmpDir, "benchmark.toml")

	// Create a test config file
	config := DefaultConfig()
	err := SaveConfig(configPath, config)
	if err != nil {
		b.Fatalf("Failed to create benchmark config: %v", err)
	}

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		_, err := LoadConfig(configPath)
		if err != nil {
			b.Fatalf("Failed to load config: %v", err)
		}
	}
}

// BenchmarkValidateConfig benchmarks config validation
func BenchmarkValidateConfig(b *testing.B) {
	config := DefaultConfig()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		err := config.Validate()
		if err != nil {
			b.Fatalf("Config validation failed: %v", err)
		}
	}
}
