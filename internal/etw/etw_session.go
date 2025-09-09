package etwmain

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"github.com/phuslu/log"
	"github.com/tekert/goetw/etw"

	"etw_exporter/internal/config"
	"etw_exporter/internal/logger"

	"golang.org/x/sys/windows"
)

// TODO: for win11 use the startkey from the manifest, if is mof, use from the state manager.

var (
	EtwExporterGuid = etw.MustParseGUID("{c47d5c06-89e7-11f0-bbb7-6045cb9e9a44}")
)

// SessionManager orchestrates the entire lifecycle of ETW (Event Tracing for Windows)
// data collection.
type SessionManager struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.RWMutex

	consumer        *etw.Consumer
	manifestSession *etw.RealTimeSession
	kernelSession   *etw.RealTimeSession // Aditional session needed for win10 and below
	eventHandler    *EventHandler
	config          *config.CollectorConfig
	appConfig       *config.AppConfig // Add AppConfig to access SessionWatcher settings
	log             log.Logger        // Session manager logger

	running            bool
	isStopping         atomic.Bool // Flag to indicate the manager is in the process of shutting down.
	restartingSessions sync.Map    // Tracks sessions currently being restarted to prevent race conditions.
}

// NewSessionManager creates and initializes a new ETW session manager.
func NewSessionManager(eventHandler *EventHandler, appConfig *config.AppConfig) *SessionManager {
	ctx, cancel := context.WithCancel(context.Background())

	manager := &SessionManager{
		ctx:          ctx,
		cancel:       cancel,
		eventHandler: eventHandler,
		config:       &appConfig.Collectors,
		appConfig:    appConfig,
		log:          logger.NewLoggerWithContext("etw_session"),
	}

	manager.restartingSessions = sync.Map{}
	manager.log.Debug().Msg("SessionManager created")
	return manager
}

// Start initializes and starts the ETW sessions
func (s *SessionManager) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("session manager already running")
	}

	s.log.Info().Msg("Starting ETW session manager...")

	// Initialize all enabled provider groups first (calling Init on each)
	if err := InitProviders(s.config); err != nil {
		err = fmt.Errorf("failed to initialize provider groups: %w", err)
		s.log.Error().Err(err).Msg("Error when initializing provider groups")
	}
	s.log.Debug().Msg("Provider groups initialized")

	// Get SystemConfig events first if using manifest providers
	// SystemConfig events only arrive when kernel sessions are stopped
	// On Windows 10 SDK build 20348+ this is not needed, we can use System Config Provider
	// and SYSTEM_CONFIG_KW_STORAGE, but it's not available on older systems
	// So we use this workaround to capture SystemConfig events
	s.log.Debug().Msg("Capturing SystemConfig events...")
	if err := s.captureNtSystemConfigEvents(); err != nil {
		return fmt.Errorf("failed to capture SystemConfig events: %w", err)
	}
	s.log.Debug().Msg("SystemConfig events captured")

	// Create sessions based on enabled provider groups
	s.log.Debug().Msg("Setting up ETW sessions...")
	if err := s.setupSessions(); err != nil {
		return fmt.Errorf("failed to setup sessions: %w", err)
	}
	s.log.Debug().Msg("ETW sessions setup complete")

	// Setup consumer with callbacks
	s.log.Debug().Msg("Setting up ETW consumer...")
	if err := s.setupConsumer(); err != nil {
		return fmt.Errorf("failed to setup consumer: %w", err)
	}
	s.log.Debug().Msg("ETW consumer setup complete")

	// Start the consumer
	s.log.Debug().Msg("Starting ETW consumer...")
	if err := s.consumer.Start(); err != nil {
		return fmt.Errorf("failed to start consumer: %w", err)
	}
	s.log.Debug().Msg("ETW consumer started")

	// Start periodic cleanup of stale processes in the background, but only if
	// a manifest session is active, as it's required for rundown events.
	if s.manifestSession != nil {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.startStaleProcessCleanup()
		}()
	}

	// Start the safety net cleanup routine to prevent memory leaks if scrapes stop.
	// These are mark for deletion that are kept until scrape that must be deleted if no scrape happens.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.startScrapeSafetyNet()
	}()

	s.isStopping.Store(false) // Ensure shutdown flag is cleared on start
	s.running = true
	s.log.Info().Msg("✅ ETW session/s started successfully")
	return nil
}

// captureNtSystemConfigEvents starts and stops a temporary kernel session to capture
// the initial state of system components like physical and logical disks.
// These configuration events are only emitted when a kernel logger session stops.
// This function is a workaround for older Windows versions where a dedicated
// System Config provider is not available.
func (s *SessionManager) captureNtSystemConfigEvents() error {
	s.log.Trace().Msg("Creating temporary kernel session for SystemConfig events")

	// Create a temporary kernel session to get SystemConfig events
	tempKernelSession := etw.NewKernelRealTimeSession(etw.EVENT_TRACE_FLAG_PROCESS)

	if err := tempKernelSession.Start(); err != nil {
		return fmt.Errorf("failed to start temporary kernel session for SystemConfig: %w", err)
	}
	s.log.Trace().Msg("Temporary kernel session started")

	// Create a temporary consumer to capture SystemConfig events
	tempConsumer := etw.NewConsumer(s.ctx).FromSessions(tempKernelSession)
	s.log.Trace().Msg("Temporary consumer created")

	// Set up callback to use the main event router, which will dispatch
	// SystemConfig events to the registered SystemConfigHandler.
	tempConsumer.EventPreparedCallback = s.eventHandler.EventPreparedCallback

	// Start the temporary consumer
	if err := tempConsumer.Start(); err != nil {
		tempKernelSession.Stop()
		return fmt.Errorf("failed to start temporary consumer for SystemConfig: %w", err)
	}

	// Wait a bit for events to be captured before stopping
	time.Sleep(100 * time.Millisecond)

	// Stop the kernel session to trigger SystemConfig events
	if err := tempKernelSession.Stop(); err != nil {
		tempConsumer.Stop()
		return fmt.Errorf("failed to stop temporary kernel session for SystemConfig: %w", err)
	}

	// Stop the temporary consumer
	if err := tempConsumer.Stop(); err != nil {
		return fmt.Errorf("failed to stop temporary consumer for SystemConfig: %w", err)
	}

	return nil
}

// isSystemProviderSupported checks if the OS version is sufficient to support System Providers.
// This feature was introduced in Windows 10 SDK build 20348 (which corresponds to
// Windows Server 2022 and Windows 11). A simple major version check for >= 10 and
// build number >= 20348 is a reliable way to detect this.
func isSystemProviderSupported() bool {
	v := windows.RtlGetVersion()
	// Windows 10 is Major version 10. Windows 11 reports as Major version 10 for compatibility.
	// We must check the build number.
	return v.MajorVersion >= 10 && v.BuildNumber >= 20348
}

// RestartSession attempts to restart a stopped ETW session in a separate goroutine.
// It implements a retry mechanism with backoff and a locking mechanism to prevent
// concurrent restart attempts for the same session.
func (s *SessionManager) RestartSession(sessionGUID etw.GUID) {
	go func() {
		// Immediately ignore events if we are in the process of a planned shutdown.
		if s.isStopping.Load() {
			s.log.Debug().Str("guid", sessionGUID.String()).Msg("Ignoring session stop event during shutdown.")
			return
		}

		s.mu.RLock()
		if !s.running {
			s.mu.RUnlock()
			s.log.Warn().Str("guid", sessionGUID.String()).Msg("Session restart requested, but manager is not running. Ignoring.")
			return
		}

		var sessionToRestart *etw.RealTimeSession
		var sessionName string

		isKernel := s.kernelSession != nil && sessionGUID == *etw.SystemTraceControlGuid
		isManifest := s.manifestSession != nil && sessionGUID == *EtwExporterGuid

		if !isKernel && !isManifest {
			s.mu.RUnlock()
			s.log.Warn().Str("guid", sessionGUID.String()).Msg("Restart requested for an unknown or unmanaged session GUID.")
			return
		}

		if isKernel {
			sessionToRestart = s.kernelSession
			sessionName = s.kernelSession.TraceName()
		} else {
			sessionToRestart = s.manifestSession
			sessionName = s.manifestSession.TraceName()
		}
		s.mu.RUnlock()

		// ! TODO: refactor the retry mechanism and maybe use goetw StoppedTraces() channel and the Context to
		// !  with this, So in case we lose events we can get a signal that the consumer lost the session.

		// This loop ensures that only one goroutine can attempt a restart for a given session at a time.
		// If a restart is already in progress, this goroutine will wait, and then re-check the session's health
		// after the other restart attempt has finished.
		for {
			if _, alreadyRestarting := s.restartingSessions.LoadOrStore(sessionName, true); !alreadyRestarting {
				// We've acquired the "lock". Break the loop to proceed with the restart attempt.
				break
			}

			// A restart is already in progress. Wait for a moment.
			s.log.Debug().Str("session", sessionName).Msg("Another restart is in progress, waiting...")
			time.Sleep(500 * time.Millisecond) // Wait for the other operation to complete.

			// After waiting, check if the session is now running. If it is, the other restart was successful,
			// and our work is done.
			if s.consumer != nil && s.consumer.IsTraceRunning(sessionName) {
				s.log.Debug().Str("session", sessionName).Msg("Session is now running after waiting; aborting redundant restart.")
				return
			}

			// If the session is still not running, the other restart might have failed or finished.
			// The loop will continue, and we'll try to acquire the lock again.
		}
		// We now hold the "lock" for this session. Ensure it's released on exit.
		defer s.restartingSessions.Delete(sessionName)

		// Poll for a short duration to confirm the trace has actually stopped. This handles
		// the race condition where we receive the stop event before the ProcessTrace
		// goroutine has fully exited.
		const pollInterval = 250 * time.Millisecond
		const maxWait = 2 * time.Second
		waited := 0 * time.Millisecond
		for s.consumer != nil && s.consumer.IsTraceRunning(sessionName) && waited < maxWait {
			time.Sleep(pollInterval)
			waited += pollInterval
		}

		// After waiting, if the trace is still running, it was a stale "echo" event
		// from a previous successful restart. The session is healthy, so we ignore it.
		if s.consumer != nil && s.consumer.IsTraceRunning(sessionName) {
			s.log.Debug().Str("session", sessionName).Msg("Ignoring stop event as trace is still running after wait period; assuming healthy.")
			return
		}

		s.log.Warn().Str("session", sessionName).Msg("Session stopped externally. Attempting to restart...")

		// Retry logic
		for i := 0; i < 10; i++ { // Try up to 10 times
			err := s.consumer.RestartSessions(sessionToRestart)

			if err == nil {
				s.log.Info().Str("session", sessionName).Msg("Session restarted successfully.")
				return
			}

			s.log.Error().Err(err).Str("session", sessionName).Int("attempt", i+1).
				Msg("Failed to restart session.")

			var waitTime time.Duration
			if i < 3 {
				waitTime = 1 * time.Second
			} else {
				waitTime = 10 * time.Second
			}

			select {
			case <-time.After(waitTime):
			// continue loop
			case <-s.ctx.Done():
				s.log.Info().Str("session", sessionName).
					Msg("Session manager is shutting down, aborting restart attempt.")
				return
			}
		}
		s.log.Error().Str("session", sessionName).Msg("Failed to restart session after multiple attempts.")
	}()
}

// setupSessions creates and configures the required ETW sessions based on the
// enabled collectors in the user's configuration. It intelligently chooses between
// modern System Providers (on Win11+) and legacy Kernel Flags (on Win10).
func (s *SessionManager) setupSessions() error {
	var sessions []etw.Session

	manifestProviders := GetEnabledManifestProviders(s.config)

	// If the session watcher is enabled, add its provider to the manifest session.
	if s.appConfig.SessionWatcher.Enabled {
		s.log.Info().Msg("Session watcher is enabled. Monitoring session status.")
		watcherProvider := etw.Provider{
			Name:            "Microsoft-Windows-Kernel-EventTracing",
			GUID:            *MicrosoftWindowsKernelEventTracingGUID,
			MatchAnyKeyword: 0x10, // ETW_KEYWORD_SESSION
			Filters: []etw.ProviderFilter{
				etw.NewEventIDFilter(true, 10, 11),
			},
		}
		manifestProviders = append(manifestProviders, watcherProvider)
	}

	if isSystemProviderSupported() {
		// --- Win11+ Path ---
		s.log.Info().Msg("Using System Providers for kernel events.")
		systemProviders := GetEnabledSystemProviders(s.config)
		manifestProviders := append(manifestProviders, systemProviders...)
		if len(manifestProviders) > 0 {
			// Mark the session as a "system logger"
			// to allow enabling System Providers.
			s.manifestSession = etw.NewSystemTraceSession("etw_exporter")
			s.manifestSession.SetGuid(*EtwExporterGuid)
		}
	} else {
		// --- Legacy (Win10) Path ---
		s.log.Warn().Msg("System Providers not supported on this OS version, falling back to NT Kernel Logger session for kernel events.")
		if len(manifestProviders) > 0 {
			s.manifestSession = etw.NewRealTimeSession("etw_exporter")
			s.manifestSession.SetGuid(*EtwExporterGuid)
		}
		// Setup additional kernel session
		kernelFlags := GetEnabledKernelFlags(s.config)
		if kernelFlags != 0 {
			s.kernelSession = etw.NewKernelRealTimeSession(kernelFlags)
			// guid is set to SystemTraceControlGuid: {9e814aad-3204-11d2-9a82-006008a86939}
			// IMPORTANT: Kernel sessions must be explicitly started (unlike manifest providers)
			if err := s.kernelSession.Start(); err != nil {
				return fmt.Errorf("failed to start kernel session: %w", err)
			}
			//s.kernelSession.GetRundownEvents(etw.SystemConfigGuid)
			sessions = append(sessions, s.kernelSession)
		}
	}

	for _, provider := range manifestProviders {
		if err := s.manifestSession.EnableProvider(provider); err != nil {
			return fmt.Errorf("failed to enable provider %s: %w", provider.Name, err)
		}
		s.log.Debug().Str("provider", provider.Name).Msg("Enabled provider in manifest session")
		// force rundown events for manifest providers
		// This ensures we get initial state for providers that support rundown
		s.manifestSession.GetRundownEvents(&provider.GUID)
	}
	sessions = append(sessions, s.manifestSession)

	if len(sessions) == 0 {
		s.log.Warn().Msg("No providers or kernel flags enabled, no sessions will be started.")
		return nil
	}

	// Create consumer from the configured sessions
	s.consumer = etw.NewConsumer(s.ctx).FromSessions(sessions...)
	return nil
}

// setupConsumer configures the ETW consumer by wiring its callbacks to the central
// EventHandler. This directs the flow of all captured events to the router.
func (s *SessionManager) setupConsumer() error {
	// Set up callbacks using the event handler
	s.consumer.EventRecordCallback = s.eventHandler.EventRecordCallback
	//s.consumer.EventRecordHelperCallback = s.eventHandler.EventRecordHelperCallback
	s.consumer.EventPreparedCallback = s.eventHandler.EventPreparedCallback
	s.consumer.EventCallback = s.eventHandler.EventCallback

	return nil
}

// StopSessions stops ONLY the ETW sessions without stopping the consumer.
func (s *SessionManager) stopSessions() error {
	// Stop sessions first
	if s.manifestSession != nil {
		if err := s.manifestSession.Stop(); err != nil {
			return fmt.Errorf("failed to stop manifest session: %w", err)
		}
	}

	if s.kernelSession != nil {
		if err := s.kernelSession.Stop(); err != nil {
			return fmt.Errorf("failed to stop kernel session: %w", err)
		}
	}
	return nil
}

// Stop gracefully stops the session manager
func (s *SessionManager) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return nil
	}
	s.isStopping.Store(true) // Signal that we are shutting down BEFORE stopping sessions.

	// Stop sessions first
	if err := s.stopSessions(); err != nil {
		return fmt.Errorf("failed to stop sessions: %w", err)
	}

	// Stop consumer last
	if s.consumer != nil {
		if err := s.consumer.StopWithTimeout(30 * time.Second); err != nil {
			return fmt.Errorf("failed to stop consumer: %w", err)
		}
	}

	// Wait for all goroutines to finish
	s.cancel()
	s.wg.Wait()

	s.running = false
	return nil
}

// Wait waits for the session manager to complete
func (s *SessionManager) Wait() {
	if s.consumer != nil {
		s.consumer.Wait()
	}
}

// IsRunning returns whether the session manager is running
func (s *SessionManager) IsRunning() bool {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.running
}

// GetEnabledProviderGroups returns the names of enabled provider groups
func (s *SessionManager) GetEnabledProviderGroups() []string {
	var enabled []string
	for _, group := range AllProviderGroups {
		if group.IsEnabled(s.config) {
			enabled = append(enabled, group.Name)
		}
	}
	return enabled
}

// TriggerProcessRundown requests the ETW system to emit events for all currently
// running processes. This is used to refresh the state of the process collector.
func (s *SessionManager) TriggerProcessRundown() error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if s.manifestSession == nil {
		return fmt.Errorf("no manifest session available for process rundown")
	}
	if !s.running {
		return fmt.Errorf("cannot trigger process rundown: session is not running")
	}

	s.log.Debug().Msg("Triggering process rundown...")
	if err := s.manifestSession.GetRundownEvents(MicrosoftWindowsKernelProcessGUID); err != nil {
		s.log.Error().Err(err).Msg("Failed to trigger process rundown")
		return err
	}
	s.log.Debug().Msg("Process rundown triggered successfully")
	return nil
}

// startScrapeSafetyNet runs a periodic task to force-cleanup entries that have been
// marked for deletion for a very long time. This acts as a safety net to prevent
// unbounded memory growth in the terminated maps if Prometheus stops scraping.
func (s *SessionManager) startScrapeSafetyNet() {
	const checkInterval = 1 * time.Hour
	const hardCapMaxAge = 6 * time.Hour

	log.Info().
		Str("interval", checkInterval.String()).
		Str("max_age", hardCapMaxAge.String()).
		Msg("Starting scrape safety net routine")

	ticker := time.NewTicker(checkInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			sm := s.eventHandler.GetStateManager()
			sm.ForceCleanupOldEntries(hardCapMaxAge)
			log.Debug().Msg("Ran periodic safety net cleanup")

		case <-s.ctx.Done():
			log.Debug().Msg("Stopping scrape safety net routine")
			return
		}
	}
}

// startStaleProcessCleanup runs a periodic task to remove stale process entries.
func (s *SessionManager) startStaleProcessCleanup() {
	//	const cleanupInterval = 5 * time.Minute
	const cleanupInterval = 5 * time.Minute
	// max age must be slightly longer than the interval to avoid race conditions
	const processMaxAge = cleanupInterval + 10*time.Second
	// Time to wait for the OS to process the rundown request and emit events
	const rundownWait = 5 * time.Second

	log.Info().
		Str("interval", cleanupInterval.String()).
		Str("max_age", processMaxAge.String()).
		Msg("🧹 Starting stale process cleanup routine (ETW Rundown)")

	ticker := time.NewTicker(cleanupInterval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			// Only trigger rundown if the session is actually running.
			if !s.IsRunning() {
				continue
			}

			// 1. Trigger a process rundown to get events for all active processes.
			if err := s.TriggerProcessRundown(); err != nil {
				log.Error().Err(err).Msg("Skipping cleanup due to rundown failure")
				continue
			}

			// 2. Wait for a moment to allow the rundown events to be processed.
			log.Debug().Dur("wait_time", rundownWait).Msg("Waiting for rundown events to be processed")
			select {
			case <-time.After(rundownWait):
				// Wait finished, continue to cleanup.
			case <-s.ctx.Done():
				log.Debug().Msg("Shutdown signaled during rundown wait, stopping cleanup.")
				return // Exit the goroutine immediately.
			}

			// 3. Clean up processes that were not "seen" during the rundown.
			sm := s.eventHandler.GetStateManager()
			sm.CleanupStaleProcesses(processMaxAge)
			log.Debug().Msg("Ran stale process cleanup")

		case <-s.ctx.Done():
			log.Debug().Msg("Stopping stale process cleanup routine")
			return
		}
	}
}
