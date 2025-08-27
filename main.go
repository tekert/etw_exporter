// main.go
package main

//go:generate go run . -generate-config config.example.toml

import (
	"fmt"
	"os"

	"etw_exporter/internal/config"
	"etw_exporter/internal/logger"

	"github.com/phuslu/log"
)

var (
	version = "0.3.2-dev"
)

// pprof comments for reference
// go build .
//
// go tool pprof -output=".\pprof\profile.pb.gz" etw_exporter.exe http://localhost:6060/debug/pprof/profile?seconds=30
// go tool pprof -http=:8080 -source_path=".\" etw_exporter.exe ".\pprof\profile.pb.gz"
// or
// cpu> go tool pprof -http=:8080 -source_path=".\" etw_exporter.exe http://localhost:6060/debug/pprof/profile?seconds=30
// mem> go tool pprof -http=:8080 -source_path=".\" etw_exporter.exe http://localhost:6060/debug/pprof/heap?seconds=30

// TODO(tekert):
//  - Memory collector
//  - Skip MOF Opcodes we dont need,
//      and also Manifest provider using IDFilter on EnableProvider [DONE].
//  - Security provider? and that's it for now.
//  - reformat CONFIG.md explanations, more compact, less text.
//  - Reopen NT Kernel Logger if closed by another app, do config.
//
// link to https://learn.microsoft.com/en-us/windows-hardware/test/wpt/cpu-analysis (good article)

func main() {
	config, err := config.NewConfig()
	if err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
	}
	if config == nil {
		// This happens when --generate-config is used and succeeds.
		return
	}

	// Configure main application logging
	if err := logger.ConfigureLogging(config.Logging); err != nil {
		fmt.Fprintf(os.Stderr, "Configuration error: %v\n", err)
		os.Exit(1)
	}

	// Create and run the application
	exporter, err := NewETWExporter(config)
	if err != nil {
		log.Fatal().Err(err).Msg("❌ Failed to initialize application")
		os.Exit(1)
	}

	if err := exporter.Run(); err != nil {
		log.Fatal().Err(err).Msg("❌ Application run failed")
		os.Exit(1)
	}
}
