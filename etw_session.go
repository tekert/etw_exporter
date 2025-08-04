package main

import (
	"context"
	"fmt"
	"sync"

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

	running bool
}

// NewSessionManager creates a new ETW session manager
func NewSessionManager() *SessionManager {
	ctx, cancel := context.WithCancel(context.Background())

	return &SessionManager{
		ctx:    ctx,
		cancel: cancel,
	}
}

// Start initializes and starts the ETW sessions
func (s *SessionManager) Start() error {
	s.mu.Lock()
	defer s.mu.Unlock()

	if s.running {
		return fmt.Errorf("session manager already running")
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

// setupSessions creates the required ETW sessions
func (s *SessionManager) setupSessions() error {
	var sessions []etw.Session

	// Setup manifest providers session if needed
	manifestProviders := GetEnabledManifestProviders()
	if len(manifestProviders) > 0 {
		s.manifestSession = etw.NewRealTimeSession("etw_exporter")

		for _, provider := range manifestProviders {
			if err := s.manifestSession.EnableProvider(provider); err != nil {
				return fmt.Errorf("failed to enable provider %s: %w", provider.Name, err)
			}
		}
		sessions = append(sessions, s.manifestSession)
	}

	// Setup kernel session if needed
	kernelFlags := GetEnabledKernelFlags()
	if kernelFlags != 0 {
		s.kernelSession = etw.NewKernelRealTimeSession(kernelFlags)
		sessions = append(sessions, s.kernelSession)
	}

	// Create consumer from sessions
	s.consumer = etw.NewConsumer(s.ctx).FromSessions(sessions...)

	return nil
}

// setupConsumer configures the consumer callbacks
func (s *SessionManager) setupConsumer() error {
	// Set up callbacks
	s.consumer.EventRecordCallback = handleEventRecord
	s.consumer.EventRecordHelperCallback = handleEventRecordHelper
	s.consumer.EventPreparedCallback = handlePreparedEvent
	s.consumer.EventCallback = handleEvent

	// NOTE: InitFilters is NOT called here because:
	// 1. We use custom callbacks that don't check c.Filter.Match(h)
	// 2. Only default callbacks (DefaultEventRecordHelperCallback) apply filters automatically
	// 3. Our custom callbacks handle filtering logic directly in handlePreparedEvent
	// 4. We only use NT Kernel Logger (no manifest providers to filter)
	// 5. Calling InitFilters would have no effect with our custom callback setup

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
	for _, group := range AllProviderGroups {
		if group.Name == groupName {
			group.Enabled = true
			return nil
		}
	}
	return fmt.Errorf("provider group '%s' not found", groupName)
}

// GetEnabledProviderGroups returns the names of enabled provider groups
func (s *SessionManager) GetEnabledProviderGroups() []string {
	var enabled []string
	for _, group := range AllProviderGroups {
		if group.Enabled {
			enabled = append(enabled, group.Name)
		}
	}
	return enabled
}
