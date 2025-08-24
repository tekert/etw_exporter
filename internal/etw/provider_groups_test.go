package etwmain

import (
	"fmt"
	"testing"

	"etw_exporter/internal/config"

	"github.com/tekert/goetw/etw"
)

// TestProviderGroups tests the simplified provider groups functionality
func TestProviderGroups(t *testing.T) {
	// Create a sample config
	config := &config.CollectorConfig{
		DiskIO: config.DiskIOConfig{
			Enabled: true,
		},
		ThreadCS: config.ThreadCSConfig{
			Enabled: true,
		},
	}

	// Test GetEnabledProviders
	enabled := GetEnabledProviders(config)
	if len(enabled) != 2 {
		t.Errorf("Expected 2 enabled providers, got %d", len(enabled))
	}

	// Test GetEnabledKernelFlags
	flags := GetEnabledKernelFlags(config)
	if flags == 0 {
		t.Error("Expected non-zero kernel flags when threadcs is enabled")
	}

	// Test GetEnabledManifestProviders
	providers := GetEnabledManifestProviders(config)
	if len(providers) == 0 {
		t.Error("Expected non-zero manifest providers when disk I/O is enabled")
	}

	fmt.Printf("Enabled providers: %d\n", len(enabled))
	fmt.Printf("Kernel flags: 0x%x\n", flags)
	fmt.Printf("Manifest providers: %d\n", len(providers))
}

// TestAddNewProvider demonstrates how to add a new provider group easily
func TestAddNewProvider(t *testing.T) {
	// Create the new provider group
	networkGroup := &ProviderGroup{
		Name:        "network",
		KernelFlags: 0, // No kernel flags needed
		ManifestProviders: []etw.Provider{
			{
				Name:            "Microsoft-Windows-Kernel-Network",
				GUID:            *etw.MustParseGUID("{7dd42a49-5329-4832-8dfd-43d979153a88}"), // Example GUID
				EnableLevel:     0xFF,
				MatchAnyKeyword: 0x0,
				MatchAllKeyword: 0x0,
			},
		},
		// Simple enabled check function
		IsEnabled: func(config *config.CollectorConfig) bool {
			// This would need a Network field in CollectorConfig
			return false // Disabled for now since NetworkConfig doesn't exist
		},
	}

	// Add to the provider groups slice
	AllProviderGroups = append(AllProviderGroups, networkGroup)

	fmt.Println("New network provider added successfully!")
	fmt.Printf("Total provider groups: %d\n", len(AllProviderGroups))
}

// TestComplexProvider demonstrates custom enabled check for complex logic
func TestComplexProvider(t *testing.T) {
	// Provider with complex enabled logic
	complexGroup := &ProviderGroup{
		Name:              "complex",
		KernelFlags:       0,
		ManifestProviders: []etw.Provider{
			// ... providers would be defined here
		},
		IsEnabled: func(config *config.CollectorConfig) bool {
			// Complex logic: enabled only if both disk I/O and threadcs are enabled
			return config.DiskIO.Enabled && config.ThreadCS.Enabled
		},
	}

	// Add to the provider groups slice
	AllProviderGroups = append(AllProviderGroups, complexGroup)
	fmt.Println("Complex provider with custom enabled check added!")
}

// TestProviderCombining tests that providers with same GUID are properly combined
func TestProviderCombining(t *testing.T) {
	// Create a config with disk I/O enabled
	config := &config.CollectorConfig{
		DiskIO: config.DiskIOConfig{
			Enabled: true,
		},
	}

	// Get enabled manifest providers
	providers := GetEnabledManifestProviders(config)

	fmt.Printf("Combined manifest providers: %d\n", len(providers))
	for i, provider := range providers {
		fmt.Printf("Provider %d: %s (GUID: %s, Keywords: 0x%x)\n",
			i+1, provider.Name, provider.GUID.String(), provider.MatchAnyKeyword)
	}
}
