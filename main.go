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
	)
	flag.Parse()

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

	// Initialize the global disk collector
	diskCollector = NewDiskIOCollector()

	// Set up ETW session
	etwSession := NewSessionManager()
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
	http.Handle(*metricsPath, promhttp.Handler())
	http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html>
            <head><title>ETW Exporter</title></head>
            <body>
            <h1>ETW Exporter v` + version + ` </h1>
            <p><a href="` + *metricsPath + `">Metrics</a></p>
            </body>
            </html>`))
	})

	log.Printf("Starting server on %s", *listenAddress)
	srv := &http.Server{Addr: *listenAddress}
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
