// main.go
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	_ "net/http/pprof"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/phuslu/log"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var (
	version = "0.1.0"
)

func main() {
	var (
		listenAddress = flag.String("web.listen-address", ":9189", "Address to listen on for web interface and telemetry.")
		metricsPath   = flag.String("web.telemetry-path", "/metrics", "Path under which to expose metrics.")
		configPath    = flag.String("config", "", "Path to configuration file (optional).")
	)
	flag.Parse()

	// Load configuration
	config, err := LoadConfig(*configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to load configuration: %v\n", err)
	}

	// TODO: config
	// go build .
	//
	// go tool pprof -output=".\pprof\profile.pb.gz" etw_exporter.exe http://localhost:6060/debug/pprof/profile?seconds=30
	// go tool pprof -http=:8080 -source_path=".\" etw_exporter.exe ".\pprof\profile.pb.gz"
	// or
	// go tool pprof -http=:8080 -source_path=".\" etw_exporter.exe http://localhost:6060/debug/pprof/profile?seconds=30
	// go tool pprof -http=:8080 -source_path=".\" etw_exporter.exe http://localhost:6060/debug/pprof/heap?seconds=30
	// Start pprof HTTP server on a separate goroutine
	go func() {
		log.Info().Msg("Starting pprof HTTP server on :6060")
		http.ListenAndServe("localhost:6060", nil)
	}()

	// Configure loggers based on configuration
	if err := configureLoggers(config); err != nil {
		fmt.Fprintf(os.Stderr, "Failed to configure loggers: %v\n", err)
		os.Exit(1)
	}

	// Override with command line flags if provided
	if flag.Lookup("web.listen-address").Value.String() != ":9189" {
		config.Server.ListenAddress = *listenAddress
	}
	if flag.Lookup("web.telemetry-path").Value.String() != "/metrics" {
		config.Server.MetricsPath = *metricsPath
	}

	// Validate configuration
	if err := config.Validate(); err != nil {
		log.Fatal().Err(err).Msg("❌ Invalid configuration")
	}

	log.Info().
		Str("version", version).
		Bool("disk_io_enabled", config.Collectors.DiskIO.Enabled).
		Bool("disk_io_track_info", config.Collectors.DiskIO.TrackDiskInfo).
		Bool("threadcs_enabled", config.Collectors.ThreadCS.Enabled).
		Str("listen_address", config.Server.ListenAddress).
		Str("metrics_path", config.Server.MetricsPath).
		Msg("Starting ETW Exporter")

	// Create context for graceful shutdown
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Set up signal handling
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		<-sigChan
		cancel()
	}()

	// Initialize metrics
	InitMetrics()
	log.Debug().Msg("- Metrics initialized")

	// Create event handler with configuration
	eventHandler := NewEventHandler(GetMetrics(), &config.Collectors)
	log.Debug().Msg("- Event handler created")

	// Set up ETW session
	etwSession := NewSessionManager(eventHandler, &config.Collectors)
	defer etwSession.Stop()
	log.Debug().Msg("- ETW session manager created")

	// Log enabled providers in the config
	enabledGroups := etwSession.GetEnabledProviderGroups()
	log.Info().Strs("provider_groups", enabledGroups).Msg("Enabled provider groups")
	log.Debug().Int("provider_count", len(enabledGroups)).Msg("Provider group count")

	// Start the ETW session
	log.Info().Msg("🔄 Starting ETW trace session...")
	if err := etwSession.Start(); err != nil {
		log.Fatal().Err(err).Msg("❌ Failed to start ETW session")
	}
	log.Debug().Msg("ETW session started successfully")

	// Set up HTTP server for Prometheus metrics
	log.Debug().Str("metrics_path", config.Server.MetricsPath).Msg("🌐 Setting up HTTP handlers")
	http.Handle(config.Server.MetricsPath, promhttp.Handler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>
            <head><title>ETW Exporter</title></head>
            <body>
            <h1>ETW Exporter v` + version + ` </h1>
            <p><a href="` + config.Server.MetricsPath + `">Metrics</a></p>
            </body>
            </html>`))
	})

	log.Info().Str("address", config.Server.ListenAddress).Msg("🌐 Starting HTTP server")
	srv := &http.Server{Addr: config.Server.ListenAddress}
	go func() {
		log.Trace().Msg("- HTTP server goroutine started")
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatal().Err(err).Msg("❌ Failed to start HTTP server")
		}
	}()

	log.Info().Msg("ETW Exporter is ready and collecting events...")

	// Wait for context cancellation
	<-ctx.Done()
	log.Info().Msg("🛑 Received shutdown signal, shutting down gracefully...")

	// Start graceful shutdown
	shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer shutdownCancel()

	log.Debug().Msg("🔌 Shutting down HTTP server...")
	if err := srv.Shutdown(shutdownCtx); err != nil {
		log.Error().Err(err).Msg("❌ Error shutting down HTTP server")
	} else {
		log.Debug().Msg("HTTP server shut down cleanly")
	}

	log.Info().Msg("ETW Exporter stopped gracefully")
}
