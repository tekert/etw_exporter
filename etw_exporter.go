package main

import (
	"context"
	"fmt"
	"net/http"
	_ "net/http/pprof" // For pprof server
	"os"
	"os/signal"
	"syscall"
	"time"

	plog "github.com/phuslu/log"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"

	"etw_exporter/internal/config"
	"etw_exporter/internal/debug"
	etwmain "etw_exporter/internal/etw"
	"etw_exporter/internal/etw/watcher"
	"etw_exporter/internal/kernel/statemanager"
)

// ETWExporter manages the lifecycle of the application.
type ETWExporter struct {
	config            *config.AppConfig
	etwSessionManager *etwmain.SessionManager
	httpServer        *http.Server
	eventHandler      *etwmain.EventHandler
	log               plog.Logger
}

// NewETWExporter creates and initializes a new ETWExporter instance.
func NewETWExporter(config *config.AppConfig) (*ETWExporter, error) {
	exporter := &ETWExporter{
		config: config,
	}

	exporter.log = plog.DefaultLogger // main app uses default logger
	exporter.log.Info().
		Str("version", version).
		Str("listen_address", config.Server.ListenAddress).
		Str("metrics_path", config.Server.MetricsPath).
		Msg("Starting ETW Exporter")

	exporter.setupETW()
	exporter.setupHTTPServer()

	// Register ETW statistics collector
	statsCollector := etwmain.NewETWStatsCollector(exporter.etwSessionManager, exporter.eventHandler)
	prometheus.MustRegister(statsCollector)
	exporter.log.Info().Msg("ETW statistics collector enabled and registered with Prometheus")

	return exporter, nil
}

// setupETW initializes the ETW session manager and event handlers.
func (e *ETWExporter) setupETW() {

	e.config.Collectors.RequiresProcessManager = etwmain.IsProcessManagerNeeded(&e.config.Collectors)
	stateManager := statemanager.GetGlobalStateManager()
	stateManager.ApplyConfig(&e.config.Collectors)
	e.log.Debug().Msg("Global state manager configured")

	e.log.Debug().Msg("- Event handler creation started")
	e.eventHandler = etwmain.NewEventHandler(e.config)
	e.log.Debug().Msg("- Event handler created")

	e.log.Debug().Msg("- ETW session manager creation started")
	e.etwSessionManager = etwmain.NewSessionManager(e.eventHandler, e.config)
	e.log.Debug().Msg("- ETW session manager created")

	// If the session watcher is enabled, create it and register its routes with the event handler.
	if e.config.SessionWatcher.Enabled {
		sessionWatcher := watcher.New(e.etwSessionManager, e.config)
		e.eventHandler.RegisterWatcherRoutes(sessionWatcher)
	}

	// Log enabled providers in the config
	enabledGroups := e.etwSessionManager.GetEnabledProviderGroups()
	e.log.Info().Strs("provider_groups", enabledGroups).Msg("Enabled provider groups")
	e.log.Debug().Int("provider_count", len(enabledGroups)).Msg("Provider group count")
}

// setupHTTPServer configures the HTTP server for metrics and pprof.
func (e *ETWExporter) setupHTTPServer() {
	e.log.Debug().Str("metrics_path", e.config.Server.MetricsPath).Msg("Setting up HTTP handlers")
	mux := http.NewServeMux()

	metricsHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 1. Aggregate all hot-path metrics into a warm-path snapshot.
		// This is done once per scrape to provide a consistent view for all collectors.
		statemanager.GetGlobalStateManager().AggregateMetrics()

		// 2. Serve the metrics to the client using the standard Prometheus handler.
		// All collectors will now read from the fresh snapshot created above.
		promhttp.Handler().ServeHTTP(w, r)

		// 3. Now that the scrape is fully complete and the response has been sent,
		// trigger the coordinated cleanup of terminated entities.
		statemanager.GetGlobalStateManager().PostScrapeCleanup()
	})
	mux.Handle(e.config.Server.MetricsPath, metricsHandler)

	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`<html>
            <head><title>ETW Exporter</title></head>
            <body>
            <h1>ETW Exporter v` + version + ` </h1>
            <p><a href="` + e.config.Server.MetricsPath + `">Metrics</a></p>
            <p><a href="/debug/state">Debug State</a></p>
            </body>
            </html>`))
	})

	// Register debug handlers if enabled.
	if e.config.Server.DebugEnabled {
		sm := statemanager.GetGlobalStateManager()
		debug.RegisterHandlers(mux, sm)
		e.log.Info().Msg("Debug state endpoint enabled at /debug/state")
	}

	e.httpServer = &http.Server{
		Addr:    e.config.Server.ListenAddress,
		Handler: mux,
	}
}

// Run starts all services and waits for a shutdown signal.
func (e *ETWExporter) Run() error {
	// Create a context that we can stop to trigger a graceful shutdown.
	ctx, stop := context.WithCancel(context.Background())
	defer stop()

	// Listen for OS signals in a separate goroutine.
	go func() {
		sigChan := make(chan os.Signal, 1)
		signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
		<-sigChan
		e.log.Info().Msg("! Received OS shutdown signal, shutting down gracefully...")
		stop()
	}()

	if e.config.Server.PprofEnabled {
		go func() {
			// Recover from panics in this goroutine to trigger a graceful shutdown.
			defer func() {
				if r := recover(); r != nil {
					e.log.Error().Interface("panic", r).
						Msg("Panic recovered in pprof server, initiating shutdown")
					stop()
				}
			}()
			e.log.Info().Msg("Starting pprof HTTP server on localhost:6060")
			// pprof registers its handlers on http.DefaultServeMux
			if err := http.ListenAndServe("localhost:6060", nil); err != nil {
				e.log.Error().Err(err).Msg("pprof server failed")
			}
		}()
	}

	e.log.Debug().Msg("Starting ETW trace session...")
	if err := e.etwSessionManager.Start(); err != nil {
		return fmt.Errorf("failed to start ETW session: %w", err)
	}
	e.log.Info().Msg("ETW session started successfully")

	go func() {
		// Recover from panics in this goroutine to trigger a graceful shutdown.
		defer func() {
			if r := recover(); r != nil {
				e.log.Error().Interface("panic", r).Msg("Panic recovered in HTTP server, initiating shutdown")
				stop()
			}
		}()
		e.log.Info().Str("address", e.config.Server.ListenAddress).Msg("Starting HTTP server")
		if err := e.httpServer.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			e.log.Error().Err(err).Msg("❌ Failed to start HTTP server")
			stop() // Trigger shutdown on server error
		}
	}()

	e.log.Info().Msg("ETW Exporter is ready and collecting events...")

	// Block until a shutdown is triggered (from OS signal, panic, or other error).
	<-ctx.Done()
	e.log.Info().Msg("! Shutdown initiated...")

	// --- Graceful shutdown sequence ---

	httpCtx, cancelhttp := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancelhttp()

	if err := e.httpServer.Shutdown(httpCtx); err != nil {
		e.log.Error().Err(err).Msg("❌ Error shutting down HTTP server")
	} else {
		e.log.Debug().Msg("HTTP server shut down cleanly")
	}

	// Stop the ETW session as the final step.
	if err := e.etwSessionManager.Stop(); err != nil {
		e.log.Error().Err(err).Msg("Error stopping ETW session")
	} else {
		e.log.Info().Msg("ETW session stopped successfully")
	}

	e.log.Info().Msg("ETW Exporter stopped gracefully")
	return nil
}
