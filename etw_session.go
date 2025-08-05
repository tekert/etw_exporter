package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/tekert/golang-etw/etw"
)

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
	config          *AppCollectorConfig

	running bool
}

// NewSessionManager creates a new ETW session manager
func NewSessionManager(eventHandler *EventHandler, config *AppCollectorConfig) *SessionManager {
	ctx, cancel := context.WithCancel(context.Background())

	// Set debug level for ETW tracing
	etw.SetTraceLevel()

	return &SessionManager{
		ctx:          ctx,
		cancel:       cancel,
		eventHandler: eventHandler,
		config:       config,
	}
}

// Start initializes and starts the ETW sessions
func (s *SessionManager) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("session manager already running")
	}

	// Get SystemConfig events first if using manifest providers
	// SystemConfig events only arrive when kernel sessions are stopped
	// On Windows 10 SDK build 20348+ this is not needed, we can use System Config Provider
	// and SYSTEM_CONFIG_KW_STORAGE, but it's not available on older systems
	// So we use this workaround to capture SystemConfig events
	if err := s.captureSystemConfigEvents(); err != nil {
		return fmt.Errorf("failed to capture SystemConfig events: %w", err)
	}

	// Create sessions based on enabled provider groups
	if err := s.setupSessions(); err != nil {
		return fmt.Errorf("failed to setup sessions: %w", err)
	}

	// Setup consumer with callbacks
	if err := s.setupConsumer(); err != nil {
		return fmt.Errorf("failed to setup consumer: %w", err)
	}

	// Start the consumer
	if err := s.consumer.Start(); err != nil {
		return fmt.Errorf("failed to start consumer: %w", err)
	}

	s.running = true
	return nil
}

// captureSystemConfigEvents starts a temporary kernel session to capture SystemConfig events
// These events are only emitted when a kernel session is stopped, so we need this special handling
func (s *SessionManager) captureSystemConfigEvents() error {
	// Create a temporary kernel session to get SystemConfig events
	tempKernelSession := etw.NewKernelRealTimeSession(etw.EVENT_TRACE_FLAG_PROCESS)

	if err := tempKernelSession.Start(); err != nil {
		return fmt.Errorf("failed to start temporary kernel session for SystemConfig: %w", err)
	}

	// Create a temporary consumer to capture SystemConfig events
	tempConsumer := etw.NewConsumer(s.ctx).FromSessions(tempKernelSession)

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
				if s.config.DiskIO.Enabled && s.eventHandler.diskCollector != nil {
					return s.eventHandler.diskCollector.HandleSystemConfigPhyDisk(helper)
				}
			case 12: // EVENT_TRACE_TYPE_CONFIG_LOGICALDISK
				if s.config.DiskIO.Enabled && s.eventHandler.diskCollector != nil {
					return s.eventHandler.diskCollector.HandleSystemConfigLogDisk(helper)
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
			s.manifestSession.GetRundownEvents(&provider)
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
		//s.kernelSession.GetRundownEvents(nil)
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

// EnableProviderGroup enables a specific provider group
func (s *SessionManager) EnableProviderGroup(groupName string) error {
	// Update configuration to enable the group
	switch groupName {
	case "disk_io":
		s.config.DiskIO.Enabled = true
		return nil
	case "context_switch":
		s.config.ContextSwitch.Enabled = true
		return nil
	default:
		return fmt.Errorf("provider group '%s' not found", groupName)
	}
}

// GetEnabledProviderGroups returns the names of enabled provider groups
func (s *SessionManager) GetEnabledProviderGroups() []string {
	var enabled []string
	if s.config.DiskIO.Enabled {
		enabled = append(enabled, "disk_io")
	}
	if s.config.ContextSwitch.Enabled {
		enabled = append(enabled, "context_switch")
	}
	return enabled
}
