package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/BurntSushi/toml"
)

// TestDefaultConfig tests that DefaultConfig returns expected core values
func TestDefaultConfig(t *testing.T) {
	config := DefaultConfig()

	// Test core server defaults
	if config.Server.ListenAddress != ":9189" {
		t.Errorf("Expected ListenAddress ':9189', got '%s'", config.Server.ListenAddress)
	}
	if config.Server.MetricsPath != "/metrics" {
		t.Errorf("Expected MetricsPath '/metrics', got '%s'", config.Server.MetricsPath)
	}

	// Test collector defaults
	if !config.Collectors.DiskIO.Enabled || !config.Collectors.ThreadCS.Enabled {
		t.Error("Expected both collectors to be enabled by default")
	}

	// Test logging defaults
	if config.Logging.Defaults.Level != "info" || config.Logging.LibLevel != "warn" {
		t.Errorf("Expected log levels info/warn, got %s/%s", config.Logging.Defaults.Level, config.Logging.LibLevel)
	}

	// Test that we have all 4 output types and only console is enabled
	if len(config.Logging.Outputs) != 4 {
		t.Errorf("Expected 4 outputs, got %d", len(config.Logging.Outputs))
	}
	expectedTypes := []string{"console", "file", "syslog", "eventlog"}
	for i, output := range config.Logging.Outputs {
		if output.Type != expectedTypes[i] {
			t.Errorf("Expected output[%d] type %s, got %s", i, expectedTypes[i], output.Type)
		}
		expectedEnabled := (i == 0) // Only console enabled
		if output.Enabled != expectedEnabled {
			t.Errorf("Expected output[%d] enabled=%t, got %t", i, expectedEnabled, output.Enabled)
		}
	}
}

// TestLoadConfig tests loading configurations with fallbacks and validation
func TestLoadConfig(t *testing.T) {
	tests := []struct {
		name       string
		configTOML string
		setupFunc  func() string // Returns config path
		expectErr  bool
	}{
		{
			name: "non-existent file returns defaults",
			setupFunc: func() string {
				return "nonexistent.toml"
			},
			expectErr: false,
		},
		{
			name: "empty path returns defaults",
			setupFunc: func() string {
				return ""
			},
			expectErr: false,
		},
		{
			name: "valid config loads correctly",
			configTOML: `
[server]
listen_address = ":8080"
metrics_path = "/test"

[collectors.disk_io]
enabled = false

[logging.defaults]
level = "debug"

[[logging.outputs]]
type = "console"
enabled = true
`,
			expectErr: false,
		},
		{
			name: "invalid TOML returns error",
			configTOML: `
[server]
listen_address = ":8080"
invalid_syntax [
`,
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var configPath string

			if tt.configTOML != "" {
				// Create temporary config file
				tmpDir := t.TempDir()
				configPath = filepath.Join(tmpDir, "test.toml")
				err := os.WriteFile(configPath, []byte(tt.configTOML), 0644)
				if err != nil {
					t.Fatalf("Failed to create test config: %v", err)
				}
			} else if tt.setupFunc != nil {
				configPath = tt.setupFunc()
			}

			config, err := LoadConfig(configPath)

			if tt.expectErr {
				if err == nil {
					t.Fatal("Expected error but got none")
				}
				return
			}

			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}

			// For valid configs, verify they loaded correctly
			if tt.name == "valid config loads correctly" {
				if config.Server.ListenAddress != ":8080" {
					t.Errorf("Expected :8080, got %s", config.Server.ListenAddress)
				}
				if config.Collectors.DiskIO.Enabled {
					t.Error("Expected DiskIO to be disabled")
				}
				if config.Logging.Defaults.Level != "debug" {
					t.Errorf("Expected debug level, got %s", config.Logging.Defaults.Level)
				}
			}

			// All valid configs should pass validation
			if err := config.Validate(); err != nil {
				t.Errorf("Config validation failed: %v", err)
			}
		})
	}
}

// TestSaveConfig tests saving configurations
func TestSaveConfig(t *testing.T) {
	t.Run("save and load roundtrip", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "subdir", "test.toml")

		original := DefaultConfig()
		original.Server.ListenAddress = ":7777"
		original.Logging.Defaults.Level = "debug"

		// Save config
		err := SaveConfig(configPath, original)
		if err != nil {
			t.Fatalf("Failed to save: %v", err)
		}

		// Load it back
		loaded, err := LoadConfig(configPath)
		if err != nil {
			t.Fatalf("Failed to load: %v", err)
		}

		// Verify key values
		if loaded.Server.ListenAddress != ":7777" {
			t.Errorf("Expected :7777, got %s", loaded.Server.ListenAddress)
		}
		if loaded.Logging.Defaults.Level != "debug" {
			t.Errorf("Expected debug, got %s", loaded.Logging.Defaults.Level)
		}
	})

	t.Run("invalid path", func(t *testing.T) {
		config := DefaultConfig()
		err := SaveConfig("\x00invalid", config)
		if err == nil {
			t.Error("Expected error for invalid path")
		}
	})
}

// TestConfigValidation tests validation edge cases
func TestConfigValidation(t *testing.T) {
	tests := []struct {
		name      string
		setupFunc func(*AppConfig)
		expectErr bool
	}{
		{
			name:      "valid config",
			setupFunc: func(c *AppConfig) {},
			expectErr: false,
		},
		{
			name: "empty listen address",
			setupFunc: func(c *AppConfig) {
				c.Server.ListenAddress = ""
			},
			expectErr: true,
		},
		{
			name: "no collectors enabled",
			setupFunc: func(c *AppConfig) {
				c.Collectors.DiskIO.Enabled = false
				c.Collectors.ThreadCS.Enabled = false
			},
			expectErr: true,
		},
		{
			name: "no outputs enabled",
			setupFunc: func(c *AppConfig) {
				for i := range c.Logging.Outputs {
					c.Logging.Outputs[i].Enabled = false
				}
			},
			expectErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			config := DefaultConfig()
			tt.setupFunc(config)

			err := config.Validate()
			if tt.expectErr && err == nil {
				t.Error("Expected validation error")
			} else if !tt.expectErr && err != nil {
				t.Errorf("Unexpected validation error: %v", err)
			}
		})
	}
}

// TestLoggingConfig tests various logging configurations
func TestLoggingConfig(t *testing.T) {
	tests := []struct {
		name       string
		configTOML string
	}{
		{
			name: "console with json format",
			configTOML: `
[collectors.disk_io]
enabled = true

[logging.defaults]
level = "trace"
time_format = "Unix"

[[logging.outputs]]
type = "console"
enabled = true

[logging.outputs.console]
fast_io = true
format = "json"
writer = "stdout"
`,
		},
		{
			name: "file output",
			configTOML: `
[collectors.disk_io]
enabled = true

[[logging.outputs]]
type = "file"
enabled = true

[logging.outputs.file]
filename = "test.log"
max_size = 50
async = false
`,
		},
		{
			name: "multiple outputs",
			configTOML: `
[collectors.disk_io]
enabled = true

[logging.defaults]
level = "info"
caller = 1

[[logging.outputs]]
type = "console"
enabled = true

[[logging.outputs]]
type = "file"
enabled = true

[logging.outputs.file]
filename = "app.log"
`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			tmpDir := t.TempDir()
			configPath := filepath.Join(tmpDir, "test.toml")

			err := os.WriteFile(configPath, []byte(tt.configTOML), 0644)
			if err != nil {
				t.Fatalf("Failed to create config: %v", err)
			}

			config, err := LoadConfig(configPath)
			if err != nil {
				t.Fatalf("Failed to load config: %v", err)
			}

			if err := config.Validate(); err != nil {
				t.Errorf("Config validation failed: %v", err)
			}

			// Test TOML roundtrip
			_, err = toml.Marshal(config)
			if err != nil {
				t.Errorf("Failed to marshal config: %v", err)
			}
		})
	}
}

// TestConfigGenerator tests configuration generation
func TestConfigGenerator(t *testing.T) {
	t.Run("GenerateExampleConfig", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "example.toml")

		err := GenerateExampleConfig(configPath)
		if err != nil {
			t.Fatalf("Failed to generate config: %v", err)
		}

		// Verify file exists and is valid
		config, err := LoadConfig(configPath)
		if err != nil {
			t.Fatalf("Generated config is invalid: %v", err)
		}

		if err := config.Validate(); err != nil {
			t.Errorf("Generated config validation failed: %v", err)
		}

		// Verify it has expected header
		content, err := os.ReadFile(configPath)
		if err != nil {
			t.Fatalf("Failed to read generated file: %v", err)
		}

		contentStr := string(content)
		if !strings.Contains(contentStr, "ETW Exporter Example Configuration") {
			t.Error("Generated config missing expected header")
		}
	})

	t.Run("CLI generator", func(t *testing.T) {
		tmpDir := t.TempDir()
		configPath := filepath.Join(tmpDir, "cli_test.toml")

		cmd := exec.Command("go", "run", ".", "-generate-config", configPath)
		output, err := cmd.CombinedOutput()
		if err != nil {
			t.Fatalf("CLI generator failed: %v, output: %s", err, output)
		}

		// Verify generated file is valid
		config, err := LoadConfig(configPath)
		if err != nil {
			t.Fatalf("CLI generated config is invalid: %v", err)
		}

		if err := config.Validate(); err != nil {
			t.Errorf("CLI generated config validation failed: %v", err)
		}
	})
}
