package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestConfigData tests configuration data, defaults, edge cases, and validation
func TestConfigData(t *testing.T) {
	tests := []struct {
		name       string
		config     *AppConfig
		configTOML string
		setupFunc  func(*AppConfig)
		expectErr  bool
		validate   func(*testing.T, *AppConfig)
	}{
		{
			name:   "default config",
			config: DefaultConfig(),
			validate: func(t *testing.T, c *AppConfig) {
				if c.Server.ListenAddress != ":9189" {
					t.Errorf("Expected ListenAddress ':9189', got %s", c.Server.ListenAddress)
				}
				if c.Logging.Defaults.Level != "info" {
					t.Errorf("Expected default log level 'info', got %s", c.Logging.Defaults.Level)
				}
				if len(c.Logging.Outputs) != 4 {
					t.Errorf("Expected 4 outputs, got %d", len(c.Logging.Outputs))
				}
			},
		},
		{
			name: "custom logging config",
			configTOML: `
[logging.defaults]
level = "debug"

[[logging.outputs]]
type = "console"
enabled = true

[[logging.outputs]]
type = "file"
enabled = true
[logging.outputs.file]
filename = "app.log"
`,
			validate: func(t *testing.T, c *AppConfig) {
				if c.Logging.Defaults.Level != "debug" {
					t.Errorf("Expected debug level, got %s", c.Logging.Defaults.Level)
				}
				if len(c.Logging.Outputs) != 2 {
					t.Errorf("Expected 2 outputs, got %d", len(c.Logging.Outputs))
				}
				if c.Logging.Outputs[0].Type != "console" {
					t.Errorf("Expected first output 'console', got %s", c.Logging.Outputs[0].Type)
				}
			},
		},
		{
			name:   "invalid empty listen address",
			config: DefaultConfig(),
			setupFunc: func(c *AppConfig) {
				c.Server.ListenAddress = ""
			},
			expectErr: true,
		},
		{
			name:   "invalid no collectors enabled",
			config: DefaultConfig(),
			setupFunc: func(c *AppConfig) {
				c.Collectors.DiskIO.Enabled = false
				c.Collectors.ThreadCS.Enabled = false
			},
			expectErr: true,
		},
		{
			name:   "invalid no outputs enabled",
			config: DefaultConfig(),
			setupFunc: func(c *AppConfig) {
				for i := range c.Logging.Outputs {
					c.Logging.Outputs[i].Enabled = false
				}
			},
			expectErr: true,
		},
		{
			name: "valid custom server config",
			configTOML: `
[server]
listen_address = ":8080"
metrics_path = "/custom"

[collectors.disk_io]
enabled = false
track_disk_info = false
`,
			validate: func(t *testing.T, c *AppConfig) {
				if c.Server.ListenAddress != ":8080" {
					t.Errorf("Expected :8080, got %s", c.Server.ListenAddress)
				}
				if c.Server.MetricsPath != "/custom" {
					t.Errorf("Expected /custom, got %s", c.Server.MetricsPath)
				}
				if c.Collectors.DiskIO.Enabled {
					t.Error("Expected DiskIO to be disabled")
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var cfg *AppConfig

			// Get config from direct config, TOML, or setup function
			if tt.config != nil {
				cfg = tt.config
				if tt.setupFunc != nil {
					tt.setupFunc(cfg)
				}
			} else {
				tmpDir := t.TempDir()
				path := filepath.Join(tmpDir, "test.toml")
				os.WriteFile(path, []byte(tt.configTOML), 0644)
				var err error
				cfg, err = LoadConfig(path)
				if err != nil {
					t.Fatalf("Failed to load config: %v", err)
				}
			}

			// Test validation
			err := cfg.Validate()
			if tt.expectErr && err == nil {
				t.Error("Expected validation error but got none")
			} else if !tt.expectErr && err != nil {
				t.Errorf("Unexpected validation error: %v", err)
			}

			// Run custom validation if provided and config is valid
			if !tt.expectErr && tt.validate != nil {
				tt.validate(t, cfg)
			}
		})
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

// TestSaveConfig tests saving configurations

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
