package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/phuslu/log"
	"github.com/tekert/goetw/etw"
)

// TODO(tekert): reopen NT Kernel Logger session if it closes, just a goroutine that checks every 5 seconds
// TODO(tekert): If in windows 11, use System Config Provider instead of NT Kernel Logger
// TODO(tekert): Explain that in Windows 10 only 1 NT Kernel Logger session can be active at a time.

// SessionManager manages ETW sessions and event consumption
type SessionManager struct {
	ctx    context.Context
	cancel context.CancelFunc
	wg     sync.WaitGroup
	mu     sync.RWMutex

	consumer        *etw.Consumer
	manifestSession *etw.RealTimeSession
	kernelSession   *etw.RealTimeSession
	eventHandler    *EventHandler
	config          *CollectorConfig
	log             log.Logger // Session manager logger

	running bool
}

// NewSessionManager creates a new ETW session manager
func NewSessionManager(eventHandler *EventHandler, config *CollectorConfig) *SessionManager {
	ctx, cancel := context.WithCancel(context.Background())

	manager := &SessionManager{
		ctx:          ctx,
		cancel:       cancel,
		eventHandler: eventHandler,
		config:       config,
		log:          GetSessionLogger(),
	}

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

	// Ensure we have the necessary privileges for kernel sessions
	// etw.EVENT_TRACE_FLAG_PROFILE provider.
	// ! TESTING
	if s.config.PerfInfo.Enabled {
		etw.EnableProfilingPrivileges()
		s.log.Warn().Msg("SE_SYSTEM_PROFILE_NAME Privilege enabled for profiling")
	}

	s.log.Info().Msg("Starting ETW session manager...")

	// Get SystemConfig events first if using manifest providers
	// SystemConfig events only arrive when kernel sessions are stopped
	// On Windows 10 SDK build 20348+ this is not needed, we can use System Config Provider
	// and SYSTEM_CONFIG_KW_STORAGE, but it's not available on older systems
	// So we use this workaround to capture SystemConfig events
	if s.config.DiskIO.TrackDiskInfo {
		s.log.Debug().Msg("Capturing SystemConfig events...")
		if err := s.captureNtSystemConfigEvents(); err != nil {
			return fmt.Errorf("failed to capture SystemConfig events: %w", err)
		}
		s.log.Debug().Msg("SystemConfig events captured")
	}

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

	s.running = true
	s.log.Info().Msg("✅ ETW session/s started successfully")
	return nil
}

// captureNtSystemConfigEvents starts a temporary kernel session to capture SystemConfig events
// These events are only emitted when a nt kernel session is stopped, so we need this special handling
// This is a workaround for Windows 10 and below where System Config manifest Provider is not available
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

	// Set up callback to capture SystemConfig events
	tempConsumer.EventPreparedCallback = func(helper *etw.EventRecordHelper) error {
		defer helper.Skip()

		eventRecord := helper.EventRec
		providerGUID := eventRecord.EventHeader.ProviderId
		eventType := eventRecord.EventHeader.EventDescriptor.Opcode

		// Only process SystemConfig events
		if providerGUID.Equals(SystemConfigGUID) {
			switch eventType {
			case 11: // EVENT_TRACE_TYPE_CONFIG_PHYSICALDISK
				if s.config.DiskIO.Enabled && s.eventHandler.diskHandler != nil {
					return s.eventHandler.diskHandler.HandleSystemConfigPhyDisk(helper)
				}
			case 12: // EVENT_TRACE_TYPE_CONFIG_LOGICALDISK
				if s.config.DiskIO.Enabled && s.eventHandler.diskHandler != nil {
					return s.eventHandler.diskHandler.HandleSystemConfigLogDisk(helper)
				}
			}
		}
		return nil
	}

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

// setupSessions creates the required ETW sessions
func (s *SessionManager) setupSessions() error {
	var sessions []etw.Session

	// Setup manifest providers session if needed
	manifestProviders := GetEnabledManifestProviders(s.config)
	if len(manifestProviders) > 0 {
		s.manifestSession = etw.NewRealTimeSession("etw_exporter")
		for _, provider := range manifestProviders {
			if err := s.manifestSession.EnableProvider(provider); err != nil {
				return fmt.Errorf("failed to enable provider %s: %w", provider.Name, err)
			}
			// force rundown events for manifest providers
			// This ensures we get initial state for providers that support rundown
			s.manifestSession.GetRundownEvents(&provider.GUID)
		}
		sessions = append(sessions, s.manifestSession)
	}

	// Setup kernel session if needed
	kernelFlags := GetEnabledKernelFlags(s.config)
	if kernelFlags != 0 {
		s.kernelSession = etw.NewKernelRealTimeSession(kernelFlags)
		// IMPORTANT: Kernel sessions must be explicitly started (unlike manifest providers)
		if err := s.kernelSession.Start(); err != nil {
			return fmt.Errorf("failed to start kernel session: %w", err)
		}
		//s.kernelSession.GetRundownEvents(etw.SystemConfigGuid)
		sessions = append(sessions, s.kernelSession)
	}

	// Create consumer from sessions
	s.consumer = etw.NewConsumer(s.ctx).FromSessions(sessions...)

	return nil
}

// setupConsumer configures the consumer callbacks
func (s *SessionManager) setupConsumer() error {
	// Set up callbacks using the event handler
	s.consumer.EventRecordCallback = s.eventHandler.EventRecordCallback
	s.consumer.EventRecordHelperCallback = s.eventHandler.EventRecordHelperCallback
	s.consumer.EventPreparedCallback = s.eventHandler.EventPreparedCallback
	s.consumer.EventCallback = s.eventHandler.EventCallback

	return nil
}

// Stop gracefully stops the session manager
func (s *SessionManager) Stop() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if !s.running {
		return nil
	}

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

	// Stop consumer last
	if s.consumer != nil {
		if err := s.consumer.Stop(); err != nil {
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

// startStaleProcessCleanup runs a periodic task to remove stale process entries.
func (s *SessionManager) startStaleProcessCleanup() {
	//	const cleanupInterval = 5 * time.Minute
	const cleanupInterval = 5 * time.Second
	// max age must be slightly longer than the interval to avoid race conditions
	const processMaxAge = cleanupInterval + 10*time.Second
	// Time to wait for the OS to process the rundown request and emit events
	const rundownWait = 5 * time.Second

	log.Info().
		Dur("interval", cleanupInterval).
		Dur("max_age", processMaxAge).
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
			pc := GetGlobalProcessCollector()
			cleanedCount := pc.CleanupStaleProcesses(processMaxAge)
			if cleanedCount > 0 {
				log.Info().Int("count", cleanedCount).Msg("Cleaned up stale processes")
			}

		case <-s.ctx.Done():
			log.Debug().Msg("Stopping stale process cleanup routine")
			return
		}
	}
}
