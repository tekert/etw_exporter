// main.go
package main

import (
	"context"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/prometheus/client_golang/prometheus/promhttp"
)

var version = "0.1.0"

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
		log.Fatalf("Failed to load configuration: %v", err)
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
		log.Fatalf("Invalid configuration: %v", err)
	}

	log.Printf("Starting ETW Exporter v%s with config: DiskIO=%t", version, config.Collectors.DiskIO.Enabled)

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

	// Create event handler with configuration
	eventHandler := NewEventHandler(GetMetrics(), &config.Collectors)

	// Set up ETW session
	etwSession := NewSessionManager(eventHandler, &config.Collectors)
	defer etwSession.Stop()

	// Enable provider groups for our collectors
	// For now, we only have the disk_io collector
	if err := etwSession.EnableProviderGroup("disk_io"); err != nil {
		log.Fatalf("Failed to enable disk_io provider group: %v", err)
	}

	// Log enabled providers
	log.Printf("Enabled provider groups: %v", etwSession.GetEnabledProviderGroups())

	// Start the ETW session
	if err := etwSession.Start(); err != nil {
		log.Fatalf("Failed to start ETW session: %v", err)
	}

	// Set up HTTP server for Prometheus metrics
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

	log.Printf("Starting server on %s", config.Server.ListenAddress)
	srv := &http.Server{Addr: config.Server.ListenAddress}
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			log.Fatalf("Failed to start HTTP server: %v", err)
		}
	}()

	// Wait for context cancellation
	<-ctx.Done()
	if err := srv.Shutdown(context.Background()); err != nil {
		log.Printf("Error shutting down HTTP server: %v", err)
	}
	log.Println("ETW Exporter stopped gracefully")
}
