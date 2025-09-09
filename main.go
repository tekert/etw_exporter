// main.go
package main

//go:generate go run . -generate-config config.example.toml

import (
	"fmt"
	"log"
	"os"

	"etw_exporter/internal/config"
	"etw_exporter/internal/logger"
)

var (
	version = "0.3.9"
)

// pprof comments for reference
// go build .
//
// go tool pprof -output=".\pprof\profile.pb.gz" etw_exporter.exe http://localhost:6060/debug/pprof/profile?seconds=30
// go tool pprof -http=:8080 -source_path=".\" etw_exporter.exe ".\pprof\profile.pb.gz"
// or
// cpu> go tool pprof -http=:8080 -source_path=".\" etw_exporter.exe http://localhost:6060/debug/pprof/profile?seconds=30
// mem> go tool pprof -http=:8080 -source_path=".\" etw_exporter.exe http://localhost:6060/debug/pprof/heap?seconds=30
//
// pgo> go tool pprof -proto -output=default.pgo etw_exporter.exe http://localhost:6060/debug/pprof/profile?seconds=300
//
// builds flags: -gcflags="all=-B" (bound check elimation) "-s -w" (strip symbols)

// TODO(tekert):
//  - Add win11 support with the new provider system. (goetw already supports it)
//  - Skip MOF Opcodes we dont need,
//      and also Manifest provider using IDFilter on EnableProvider [DONE].
//  - Security provider? and that's it for now.
//     https://techcommunity.microsoft.com/blog/windows-itpro-blog/new-security-capabilities-in-event-tracing-for-windows/3949941
//  - Reopen NT Kernel Logger if closed by another app, do config.
//  - user suplied exe names to expand metric to per process. ( Use: EventRecord.ExtProcessStartKey works on mof?)
//      for example the thread collector wait states.
//      that would filter the per process of the other metrics.
//  - Move Image events to image collector?
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
		log.Fatal("Failed to initialize application: ", err)
		os.Exit(1)
	}

	if err := exporter.Run(); err != nil {
		log.Fatal("Application run failed: ", err)
		os.Exit(1)
	}
}
