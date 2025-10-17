package etwmain

import (
	"context"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	plog "github.com/phuslu/log"
	"github.com/tekert/goetw/etw"

	"etw_exporter/internal/config"
	"etw_exporter/internal/etw/guids"
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
	log             plog.Logger       // Session manager logger

	running               bool
	isStopping            atomic.Bool // Flag to indicate the manager is in the process of shutting down.
	restartingSessions    sync.Map    // Tracks sessions currently being restarted to prevent race conditions.
	enabledProviderGroups []*ProviderGroup
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

	// Initialize the list of enabled providers based on the config.
	manager.initProviders()

	manager.restartingSessions = sync.Map{}
	manager.log.Debug().Msg("SessionManager created")
	return manager
}

// initProviders initializes the provider groups based on the configuration.
func (s *SessionManager) initProviders() {
	s.log.Debug().Msg("Initializing provider groups")
	s.enabledProviderGroups = GetEnabledProviders(s.config)

	var once bool
	for _, group := range s.enabledProviderGroups {
		// Ensure we have the necessary privileges for kernel sessions if PROFILE flag is set
		if group.KernelFlags&etw.EVENT_TRACE_FLAG_PROFILE != 0 && !once {
			etw.EnableProfilingPrivileges()
			s.log.Warn().Msg("SE_SYSTEM_PROFILE_NAME Privilege enabled for profiling")
			once = true
		}

		if err := group.init(); err != nil {
			s.log.Error().Err(err).Str("group", group.Name).Msg("ProviderGroup Init failed")
		}
	}
}

// Start initializes and starts the ETW sessions
func (s *SessionManager) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("session manager already running")
	}

	// NOTE: don't setup the temporal session here,
	// do it before http service start or there can be mem corruption.

	s.log.Info().Msg("Starting ETW session manager...")

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

	// Start the consumer-based trace monitor for robust session health checks.
	s.wg.Add(1)
	go func() {
		defer s.wg.Done()
		s.startTraceMonitor()
	}()

	// Priodic rundown for processes to handle any process close lost event.
	if s.kernelSession != nil {
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.startPeriodicProcessRundown()
		}()
	}

	s.isStopping.Store(false) // Ensure shutdown flag is cleared on start
	s.running = true
	s.log.Info().Msg("✅ ETW session/s started successfully")
	return nil
}

// CaptureNtSystemConfigEvents starts and stops a temporary kernel session to capture
// the initial state of system components like physical and logical disks.
// These configuration events are only emitted when a kernel logger session stops.
// This function is a workaround for older Windows versions where a dedicated
// System Config provider is not available.
func (s *SessionManager) CaptureNtSystemConfigEvents() error {
	s.log.Debug().Msg("Creating temporary kernel session for SystemConfig events")

	// Create a temporary kernel session to get SystemConfig events
	//tempKernelSession := etw.NewKernelRealTimeSession(0)
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

// IsSystemProviderSupported checks if the OS version is sufficient to support System Providers.
// This feature was introduced in Windows 10 SDK build 20348 (which corresponds to
// Windows Server 2022 and Windows 11). A simple major version check for >= 10 and
// build number >= 20348 is a reliable way to detect this.
func IsSystemProviderSupported() bool {
	v := windows.RtlGetVersion()
	// Windows 10 is Major version 10. Windows 11 reports as Major version 10 for compatibility.
	// We must check the build number.
	return v.MajorVersion >= 10 && v.BuildNumber >= 20348
}

// IsNtKernelSessionInUse checks if any process is currently using the NT Kernel Logger session.
func (s *SessionManager) IsNtKernelSessionInUse() bool {
	return s.kernelSession.IsSessionActive() // used by another process.
}

// handleSessionStop is the centralized decision-making function for restarting a session.
// It checks the application's configuration to determine if a restart is permitted
// and under what conditions (e.g., not hijacking a session in use by another tool).
func (s *SessionManager) HandleSessionStop(sessionGUID etw.GUID) {
	// Check if the stopped session is one we manage and if restart is enabled.
	if sessionGUID == *etw.SystemTraceControlGuid {
		policy := s.appConfig.SessionWatcher.RestartKernelSession
		s.log.Debug().Str("policy", policy).Msg("Applying restart policy for NT Kernel Logger session.")

		switch policy {
		case "forced":
			s.restartSession(sessionGUID)
		case "enabled":
			if !s.IsNtKernelSessionInUse() {
				s.log.Info().Msg("NT Kernel session is not in use by another process. Proceeding with restart.")
				s.restartSession(sessionGUID)
			} else {
				s.log.Warn().Msg("NT Kernel session was stopped, but another process is now using it. Aborting restart to avoid hijacking.")
			}
		case "off":
			s.log.Info().Msg("NT Kernel session was stopped. Restart is disabled by policy 'off'.")
			// Do nothing.
		}
		return
	}

	if sessionGUID == *EtwExporterGuid {
		if s.appConfig.SessionWatcher.RestartExporterSession {
			s.log.Debug().Msg("Applying restart policy for exporter session.")
			s.restartSession(sessionGUID)
		} else {
			s.log.Info().Msg("Exporter session was stopped. Restart is disabled by policy.")
		}
	}
}

// restartSession attempts to restart a stopped ETW session in a separate goroutine.
// It implements a retry mechanism with backoff and a locking mechanism to prevent
// concurrent restart attempts for the same session.
func (s *SessionManager) restartSession(sessionGUID etw.GUID) {
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

		// TODO: refactor the retry mechanism and maybe use goetw StoppedTraces() channel and the Context to
		//   watch this, So in case we lose events we can get a signal that the consumer lost the session.

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
			time.Sleep(1 * time.Second) // Wait for the other operation to complete.

			// After waiting, check if the session is now running. If it is, the other restart was successful,
			// and our work is done.
			//if sessionToRestart.IsSessionActive() {
			if s.consumer != nil && s.consumer.IsTraceRunning(sessionName) {
				s.log.Debug().Str("session", sessionName).Msg("Session is now running after waiting; aborting redundant restart.")
				return
			}

			// If the session is still not running, the other restart might have failed or finished.
			// The loop will continue, and we'll try to acquire the lock again.
		}
		// We now hold the "lock" for this session. Ensure it's released on exit.
		defer s.restartingSessions.Delete(sessionName)

		s.log.Warn().Str("session", sessionName).Msg("Session stopped externally. Attempting to restart...")

		// Retry logic
		for i := range 10 { // Try up to 10 times
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

// startTraceMonitor runs a background goroutine that listens to the consumer's
// StoppedTraces channel. This provides a highly reliable, event-driven way to
// detect when a trace processing loop has terminated for any reason.
func (s *SessionManager) startTraceMonitor() {
	s.log.Info().Msg("Starting consumer trace monitor.")
	stoppedTraces := s.consumer.StoppedTraces()

	for {
		select {
		case stoppedTrace := <-stoppedTraces:
			// If a shutdown is in progress, this is an expected event.
			// We log it for debugging and ignore it to prevent a restart attempt.
			if s.isStopping.Load() {
				s.log.Debug().
					Str("trace_name", stoppedTrace.TraceName).
					Msg("Consumer stopped processing trace during planned shutdown. Ignoring.")
				continue
			}

			// A trace has stopped unexpectedly. We need to determine which session it belongs to
			// and trigger a restart.
			s.log.Warn().
				Str("trace_name", stoppedTrace.TraceName).
				Msg("Consumer stopped processing trace unexpectedly. Triggering restart.")

			s.mu.RLock()
			isKernel := s.kernelSession != nil && s.kernelSession.TraceName() == stoppedTrace.TraceName
			isManifest := s.manifestSession != nil && s.manifestSession.TraceName() == stoppedTrace.TraceName
			s.mu.RUnlock()

			var sessionGUID etw.GUID
			if isKernel {
				sessionGUID = *etw.SystemTraceControlGuid
			} else if isManifest {
				sessionGUID = *EtwExporterGuid
			} else {
				s.log.Error().
					Str("trace_name", stoppedTrace.TraceName).
					Msg("Stopped trace does not match any managed session.")
				continue // Continue loop
			}

			// Defer the restart decision to the centralized handler, which respects user configuration.
			s.HandleSessionStop(sessionGUID)

		case <-s.ctx.Done():
			s.log.Debug().Msg("Stopping consumer trace monitor.")
			return
		}
	}
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
			GUID:            *guids.MicrosoftWindowsKernelEventTracingGUID,
			MatchAnyKeyword: 0x10, // ETW_KEYWORD_SESSION
			Filters: []etw.ProviderFilter{
				etw.NewEventIDFilter(true, 10, 11), // Session start/stop events
			},
		}
		manifestProviders = append(manifestProviders, watcherProvider)
	}

	if IsSystemProviderSupported() {
		// --- Win11+ Path ---
		s.log.Info().Msg("Using System Providers for kernel events.")
		systemProviders := GetEnabledSystemProviders(s.config)
		manifestProviders := append(manifestProviders, systemProviders...)
		if len(manifestProviders) > 0 {
			// Mark the session as a "system logger"
			// to allow enabling System Providers.
			s.manifestSession = etw.NewSystemTraceSession("etw_exporter")
			s.manifestSession.SetGuid(*EtwExporterGuid)
			// props := s.manifestSession.TraceProperties()
			// props.BufferSize = 64 // (default is 64)
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
			props := s.kernelSession.TraceProperties()
			props.BufferSize = 64 // 64 KB (default is 64)
			//props.MinimumBuffers = 0
			// props.MaximumBuffers = 300
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
		if err := s.manifestSession.EnableProvider(provider); err != nil { // This starts the session if not already started
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
	s.consumer.EventRecordHelperCallback = nil //s.eventHandler.EventRecordHelperCallback
	s.consumer.EventPreparedCallback = s.eventHandler.EventPreparedCallback
	s.consumer.EventCallback = nil //s.eventHandler.EventCallback

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
	if !s.running {
		s.mu.Unlock()
		return nil
	}
	s.isStopping.Store(true) // Signal that we are shutting down BEFORE stopping sessions.
	s.running = false
	s.mu.Unlock() // Important: Unlock before waiting

	// Stop consumer first to stop unwanted errors while in shutdown
	// and also a rush of unnesesary events that would cause small delays when closing.
	if s.consumer != nil {
		if err := s.consumer.StopWithTimeout(10 * time.Second); err != nil {
			return fmt.Errorf("failed to stop consumer: %w", err)
		}
	}

	// Stop sessions last
	if err := s.stopSessions(); err != nil {
		s.log.Error().Err(err).Msg("Failed to stop ETW sessions cleanly")
		// Continue shutdown even if session stop fails
	}

	// Signal all background goroutines to exit
	s.cancel()
	// Wait for them to finish
	s.wg.Wait()

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
	for _, group := range s.enabledProviderGroups {
		enabled = append(enabled, group.Name)
	}
	return enabled
}

// startPeriodicProcessRundown runs a background task that periodically triggers a process
// rundown.
func (s *SessionManager) startPeriodicProcessRundown() {
    // The interval should be reasonably long to avoid unnecessary overhead.
    const rundownInterval = 5 * time.Minute

    s.log.Info().
        Str("interval", rundownInterval.String()).
        Msg("Starting periodic process rundown routine for state reconciliation")

    ticker := time.NewTicker(rundownInterval)
    defer ticker.Stop()
    for {
        select {
        case <-ticker.C:
            if !s.IsRunning() {
                continue
            }
            s.log.Debug().Msg("Triggering periodic process rundown for state reconciliation...")
            if err := s.TriggerProcessRundown(); err != nil {
                s.log.Error().Err(err).Msg("Periodic process rundown failed")
            }

        case <-s.ctx.Done():
            s.log.Debug().Msg("Stopping periodic process rundown routine")
            return
        }
    }
}

// TriggerProcessRundown requests the ETW system to emit events for all currently
// running processes.
func (s *SessionManager) TriggerProcessRundown() error {
	s.mu.RLock()
	defer s.mu.RUnlock()

	if !s.running {
		return fmt.Errorf("cannot trigger process rundown: session is not running")
	}

	var rundownTriggered bool
	var lastErr error

	// 1. Check for NT Kernel Logger (MOF) process provider.
	if s.kernelSession != nil {
		// Check the configured flags to see if process events are enabled.
		if (s.kernelSession.TraceProperties().EnableFlags & etw.EVENT_TRACE_FLAG_PROCESS) != 0 {
			s.log.Debug().Msg("Triggering process rundown via NT Kernel Logger restart...")
			err := s.kernelSession.RestartNtKernelProvider(etw.EVENT_TRACE_FLAG_PROCESS)
			if err != nil {
				s.log.Error().Err(err).Msg("Failed to trigger NT Kernel Logger process rundown")
				lastErr = err
			} else {
				s.log.Debug().Msg("NT Kernel Logger process rundown triggered successfully.")
				rundownTriggered = true
			}
		}
	}

	// 2. Check for Manifest-based and System process providers.
	if s.manifestSession != nil {
		for _, provider := range s.manifestSession.Providers() {
			// Check for Microsoft-Windows-Kernel-Process (Win10 Manifest)
			if provider.GUID == *guids.MicrosoftWindowsKernelProcessGUID {
				s.log.Debug().Msg("Triggering process rundown via Microsoft-Windows-Kernel-Process provider...")
				// NOTE: this does not trigger thread or image rundowns. Only process. (not useful)
				if err := s.manifestSession.GetRundownEvents(&provider.GUID); err != nil {
					s.log.Error().Err(err).Msg("Failed to trigger Microsoft-Windows-Kernel-Process rundown")
					lastErr = err
				} else {
					s.log.Debug().Msg("Microsoft-Windows-Kernel-Process rundown triggered successfully.")
					rundownTriggered = true
				}
			}

			// Check for SystemProcessProvider (Win11+)
			if provider.GUID == *etw.SystemProcessProviderGuid {
				s.log.Debug().Msg("Triggering process rundown via SystemProcessProvider...")
				// TODO: test this on Win11+
				if err := s.manifestSession.GetRundownEvents(&provider.GUID); err != nil {
					s.log.Error().Err(err).Msg("Failed to trigger SystemProcessProvider rundown")
					lastErr = err
				} else {
					s.log.Debug().Msg("SystemProcessProvider rundown triggered successfully.")
					rundownTriggered = true
				}
			}
		}
	}

	if !rundownTriggered {
		if lastErr != nil {
			return fmt.Errorf("failed to trigger any process rundown: %w", lastErr)
		}
		return fmt.Errorf("no active process provider found to trigger a rundown")
	}

	s.log.Info().Msg("Process rundown successfully triggered for active providers.")
	return nil
}
