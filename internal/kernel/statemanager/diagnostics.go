package statemanager

import (
	"fmt"
	"path/filepath"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"etw_exporter/internal/logger"

	"github.com/tekert/goetw/logsampler/adapters/phusluadapter"
)

// ConsoleSample holds a snapshot of statistics for a single reporting interval.
type ConsoleSample struct {
	Timestamp        time.Time
	ThreadsAdded     uint64
	ProcessesAdded   uint64
	ServicesEnriched uint64
}

// Diagnostics holds all diagnostic counters for the application.
type Diagnostics struct {
	// Hot-path atomic counters
	threadsAdded     atomic.Uint64
	processesAdded   atomic.Uint64
	servicesEnriched atomic.Uint64
	newProcesses     *sync.Map // Stores PID -> Name for new processes in the current interval.

	// History for time-windowed analysis on the debug page
	Mu             sync.Mutex
	ConsoleHistory []ConsoleSample

	log *phusluadapter.SampledLogger
}

// GlobalDiagnostics is the singleton instance for all diagnostic data.
var GlobalDiagnostics = &Diagnostics{
	newProcesses:   new(sync.Map),
	ConsoleHistory: make([]ConsoleSample, 0, 800),
}

// Init initializes the diagnostics package.
func (d *Diagnostics) Init() {
	d.log = logger.NewSampledLoggerCtx("diagnostics")
	go d.startHistoryPruner()
}

// startHistoryPruner runs a background goroutine to periodically clean out old data.
func (d *Diagnostics) startHistoryPruner() {
	ticker := time.NewTicker(5 * time.Minute)
	defer ticker.Stop()

	for range ticker.C {
		cutoff := time.Now().Add(-1 * time.Hour)
		d.Mu.Lock()
		n := 0
		for _, s := range d.ConsoleHistory {
			if s.Timestamp.After(cutoff) {
				d.ConsoleHistory[n] = s
				n++
			}
		}
		d.ConsoleHistory = d.ConsoleHistory[:n]
		d.Mu.Unlock()
	}
}

// --- Public Recording Functions (called from hot path) ---

func (d *Diagnostics) RecordProcessAdded(pid uint32, name string) {
	d.processesAdded.Add(1)
	d.newProcesses.Store(pid, name)
}
func (d *Diagnostics) RecordThreadAdded() {
	d.threadsAdded.Add(1)
}
func (d *Diagnostics) RecordServiceEnrichedAPI() {
	d.servicesEnriched.Add(1)
}

// ReportTrackedProcToConsole logs the current statistics for the interval.
func (d *Diagnostics) ReportTrackedProcToConsole(totalTrackedProcesses int) {
	s := ConsoleSample{
		Timestamp:        time.Now(),
		ThreadsAdded:     d.threadsAdded.Swap(0),
		ProcessesAdded:   d.processesAdded.Swap(0),
		ServicesEnriched: d.servicesEnriched.Swap(0),
	}

	d.Mu.Lock()
	d.ConsoleHistory = append(d.ConsoleHistory, s)
	d.Mu.Unlock()

	oldMap := d.newProcesses
	d.newProcesses = new(sync.Map)
	var processes []string
	oldMap.Range(func(key, value any) bool {
		if pid, ok := key.(uint32); ok {
			if name, ok := value.(string); ok {
				processes = append(processes, fmt.Sprintf("%d-%s", pid, filepath.Base(name)))
			}
		}
		return true
	})
	sort.Strings(processes)

	if s.ThreadsAdded == 0 && s.ProcessesAdded == 0 && s.ServicesEnriched == 0 {
		return
	}

	d.log.Debug().
		Int("total_tracked_processes", totalTrackedProcesses).
		Uint64("new_procs", s.ProcessesAdded).
		Uint64("new_threads", s.ThreadsAdded).
		Uint64("services_enriched_api", s.ServicesEnriched).
		Strs("new_processes_in_interval", processes).
		Msg("State Manager Diagnostics (per scrape interval)")
}
