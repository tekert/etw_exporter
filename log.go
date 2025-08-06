// log.go
package main

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/phuslu/log"
	"github.com/tekert/golang-etw/etw"
)

var (
	// Module-specific fast loggers for hot paths
	// These use IOWriter (JSON) for maximum performance in event processing
	diskIOLogger log.Logger
	threadLogger log.Logger
	eventLogger  log.Logger
)

// parseLogLevel converts string log level to log.Level
func parseLogLevel(levelStr string) log.Level {
	switch levelStr {
	case "trace":
		return log.TraceLevel
	case "debug":
		return log.DebugLevel
	case "info":
		return log.InfoLevel
	case "warn", "warning":
		return log.WarnLevel
	case "error":
		return log.ErrorLevel
	case "fatal":
		return log.FatalLevel
	default:
		return log.InfoLevel
	}
}

// parseTimeLocation parses time location string
func parseTimeLocation(location string) *time.Location {
	switch location {
	case "Local":
		return time.Local
	case "UTC":
		return time.UTC
	default:
		if loc, err := time.LoadLocation(location); err == nil {
			return loc
		}
		return time.Local
	}
}

// createConsoleWriter creates a console writer based on configuration
func createConsoleWriter(config *ConsoleConfig) (log.Writer, error) {
	var baseWriter io.Writer
	switch config.Writer {
	case "stdout":
		baseWriter = os.Stdout
	case "stderr":
		baseWriter = os.Stderr
	default:
		baseWriter = os.Stderr
	}

	var writer log.Writer

	if config.FastIO {
		// Use fast IOWriter for JSON output
		writer = &log.IOWriter{Writer: baseWriter}
	} else {
		// Use ConsoleWriter for formatted output
		consoleWriter := &log.ConsoleWriter{
			ColorOutput:    config.ColorOutput,
			QuoteString:    config.QuoteString,
			EndWithMessage: true,
			Writer:         baseWriter,
		}

		// Set formatter based on format
		switch config.Format {
		case "json":
			// JSON format using ConsoleWriter for proper formatting
			consoleWriter.Formatter = func(w io.Writer, a *log.FormatterArgs) (int, error) {
				return fmt.Fprintf(w, `{"time":%q,"level":%q,"caller":%q,"goid":%q,"message":%q}`+"\n",
					a.Time, a.Level, a.Caller, a.Goid, a.Message)
			}
			writer = consoleWriter
		case "logfmt":
			// Logfmt format
			consoleWriter.Formatter = log.LogfmtFormatter{TimeField: "time"}.Formatter
			writer = consoleWriter
		case "glog":
			// Glog format
			consoleWriter.Formatter = func(w io.Writer, a *log.FormatterArgs) (int, error) {
				level := 'I'
				if len(a.Level) > 0 {
					level = rune(a.Level[0] - 32) // Convert to uppercase
				}
				return fmt.Fprintf(w, "%c%s %s %s] %s\n", level, a.Time, a.Goid, a.Caller, a.Message)
			}
			writer = consoleWriter
		case "auto":
			fallthrough
		default:
			// Default colorized console format
			writer = consoleWriter
		}
	}

	if config.Async {
		return &log.AsyncWriter{
			ChannelSize: 4096,
			Writer:      writer,
		}, nil
	}
	return writer, nil
}

// createFileWriter creates a file writer based on configuration
func createFileWriter(config *FileConfig) (log.Writer, error) {
	// Ensure directory exists if requested
	if config.EnsureFolder {
		dir := filepath.Dir(config.Filename)
		if err := os.MkdirAll(dir, 0755); err != nil {
			return nil, err
		}
	}

	baseWriter := &log.FileWriter{
		Filename:     config.Filename,
		FileMode:     0644, // Fixed mode for Windows
		MaxSize:      config.MaxSize,
		MaxBackups:   config.MaxBackups,
		TimeFormat:   config.TimeFormat,
		LocalTime:    config.LocalTime,
		HostName:     config.HostName,
		ProcessID:    config.ProcessID,
		EnsureFolder: config.EnsureFolder,
	}

	if config.Async {
		return &log.AsyncWriter{
			ChannelSize: 4096,
			Writer:      baseWriter,
		}, nil
	}
	return baseWriter, nil
}

// createSyslogWriter creates a syslog writer based on configuration
func createSyslogWriter(config *SyslogConfig) (log.Writer, error) {
	baseWriter := &log.SyslogWriter{
		Network:  config.Network,
		Address:  config.Address,
		Hostname: config.Hostname,
		Tag:      config.Tag,
		Marker:   config.Marker,
	}

	if config.Async {
		return &log.AsyncWriter{
			ChannelSize: 4096,
			Writer:      baseWriter,
		}, nil
	}
	return baseWriter, nil
}

// createEventlogWriter creates an eventlog writer based on configuration
func createEventlogWriter(config *EventlogConfig) (log.Writer, error) {
	baseWriter := &log.EventlogWriter{
		Source: config.Source,
		ID:     uintptr(config.ID),
		Host:   config.Host,
	}

	if config.Async {
		return &log.AsyncWriter{
			ChannelSize: 4096,
			Writer:      baseWriter,
		}, nil
	}
	return baseWriter, nil
}

// createWriter creates a log.Writer based on the output configuration
func createWriter(output LogOutput) (log.Writer, error) {
	if !output.Enabled {
		return nil, nil
	}

	switch output.Type {
	case "console":
		if output.Console == nil {
			return nil, fmt.Errorf("console output missing console configuration")
		}
		return createConsoleWriter(output.Console)

	case "file":
		if output.File == nil {
			return nil, fmt.Errorf("file output missing file configuration")
		}
		return createFileWriter(output.File)

	case "syslog":
		if output.Syslog == nil {
			return nil, fmt.Errorf("syslog output missing syslog configuration")
		}
		return createSyslogWriter(output.Syslog)

	case "eventlog":
		if output.Eventlog == nil {
			return nil, fmt.Errorf("eventlog output missing eventlog configuration")
		}
		return createEventlogWriter(output.Eventlog)

	default:
		return nil, fmt.Errorf("unknown output type: %s", output.Type)
	}
}

// createMultiWriter creates a multi-writer that outputs to multiple destinations
func createMultiWriter(outputs []LogOutput) (log.Writer, error) {
	var writers []log.Writer

	for _, output := range outputs {
		if !output.Enabled {
			continue
		}

		if output.Type == "console" {
			// Always use the shared synchronized console writer to prevent race conditions
			if sharedConsoleWriter != nil {
				writers = append(writers, sharedConsoleWriter)
			}
		} else {
			// Non-console writers don't need synchronization
			writer, err := createWriter(output)
			if err != nil {
				return nil, err
			}
			if writer != nil {
				writers = append(writers, writer)
			}
		}
	}

	if len(writers) == 0 {
		// Fallback to shared console writer if no writers are configured
		if sharedConsoleWriter != nil {
			return sharedConsoleWriter, nil
		}
		// If shared writer not available yet, create a default one
		return &SyncConsoleWriter{
			writer: &log.IOWriter{Writer: os.Stderr},
		}, nil
	}

	if len(writers) == 1 {
		// Single writer - no need for multi-writer wrapper
		return writers[0], nil
	}

	// Multiple writers - use phuslu/log's MultiEntryWriter
	multiWriter := log.MultiEntryWriter(writers)
	return &multiWriter, nil
}

// SyncConsoleWriter is a thread-safe console writer implementation
// This ensures all console output is properly synchronized to prevent race conditions
// The phuslu/log ConsoleWriter is NOT thread-safe, so we must serialize access
type SyncConsoleWriter struct {
	writer log.Writer
	mu     sync.Mutex
}

func (w *SyncConsoleWriter) WriteEntry(e *log.Entry) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.writer.WriteEntry(e)
}

// Shared synchronized console writer to prevent race conditions across all loggers
var sharedConsoleWriter *SyncConsoleWriter

// ConfigureLogging configures the global logger and module-specific loggers
func ConfigureLogging(config LoggingConfig) error {
	// Create a single shared synchronized console writer to prevent race conditions
	// This ensures ALL console output goes through the same synchronized writer
	var consoleWriter log.Writer = &log.IOWriter{Writer: os.Stderr} // Default fallback
	for _, output := range config.Outputs {
		if output.Enabled && output.Type == "console" && output.Console != nil {
			if cw, err := createConsoleWriter(output.Console); err == nil {
				consoleWriter = cw
				break
			}
		}
	}

	sharedConsoleWriter = &SyncConsoleWriter{
		writer: consoleWriter,
	}

	// Create the main writer from all enabled outputs
	writer, err := createMultiWriter(config.Outputs)
	if err != nil {
		return err
	}

	// Configure the default logger
	log.DefaultLogger = log.Logger{
		Level:        parseLogLevel(config.Defaults.Level),
		Caller:       config.Defaults.Caller,
		TimeField:    config.Defaults.TimeField,
		TimeFormat:   config.Defaults.TimeFormat,
		TimeLocation: parseTimeLocation(config.Defaults.TimeLocation),
		Writer:       writer,
	}

	// Create fast writers for module loggers (hot path optimization)
	// These always use fast JSON output for performance, but still need console synchronization
	fastConsoleWriter := &SyncConsoleWriter{
		writer: &log.IOWriter{Writer: os.Stderr},
	}

	// Find file writers for fast loggers
	var fastWriters []log.Writer
	fastWriters = append(fastWriters, fastConsoleWriter)

	for _, output := range config.Outputs {
		if output.Enabled && output.Type == "file" && output.File != nil {
			if fw, err := createFileWriter(output.File); err == nil {
				fastWriters = append(fastWriters, fw)
			}
		}
	}

	var fastWriter log.Writer
	if len(fastWriters) == 1 {
		fastWriter = fastWriters[0]
	} else {
		multiWriter := log.MultiEntryWriter(fastWriters)
		fastWriter = &multiWriter
	}

	// Configure module-specific fast loggers
	diskIOLogger = log.Logger{
		Level:        parseLogLevel(config.Defaults.Level),
		Caller:       0, // Disable caller for performance
		TimeField:    config.Defaults.TimeField,
		TimeFormat:   config.Defaults.TimeFormat,
		TimeLocation: parseTimeLocation(config.Defaults.TimeLocation),
		Writer:       fastWriter,
		Context:      log.NewContext(nil).Str("module", "disk-io").Value(),
	}

	threadLogger = log.Logger{
		Level:        parseLogLevel(config.Defaults.Level),
		Caller:       0, // Disable caller for performance
		TimeField:    config.Defaults.TimeField,
		TimeFormat:   config.Defaults.TimeFormat,
		TimeLocation: parseTimeLocation(config.Defaults.TimeLocation),
		Writer:       fastWriter,
		Context:      log.NewContext(nil).Str("module", "thread").Value(),
	}

	eventLogger = log.Logger{
		Level:        parseLogLevel(config.Defaults.Level),
		Caller:       0, // Disable caller for performance
		TimeField:    config.Defaults.TimeField,
		TimeFormat:   config.Defaults.TimeFormat,
		TimeLocation: parseTimeLocation(config.Defaults.TimeLocation),
		Writer:       fastWriter,
		Context:      log.NewContext(nil).Str("module", "events").Value(),
	}

	// Configure ETW library logging
	if err := ConfigureETWLibraryLogger(config.LibLevel, config.Outputs); err != nil {
		return fmt.Errorf("failed to configure ETW library logging: %w", err)
	}

	return nil
}

// GetDiskIOLogger returns a high-performance logger for disk-io module
// This logger uses fast JSON output regardless of console configuration for optimal performance
func GetDiskIOLogger() log.Logger {
	return diskIOLogger
}

// GetThreadLogger returns a high-performance logger for thread module
// This logger uses fast JSON output regardless of console configuration for optimal performance
func GetThreadLogger() log.Logger {
	return threadLogger
}

// GetProcessLogger returns a logger with process module context
func GetProcessLogger() log.Logger {
	return log.Logger{
		Level:        log.DefaultLogger.Level,
		Caller:       log.DefaultLogger.Caller,
		TimeField:    log.DefaultLogger.TimeField,
		TimeFormat:   log.DefaultLogger.TimeFormat,
		TimeLocation: log.DefaultLogger.TimeLocation,
		Writer:       log.DefaultLogger.Writer,
		Context:      log.NewContext(nil).Str("module", "process").Value(),
	}
}

// GetSessionLogger returns a logger with session module context
func GetSessionLogger() log.Logger {
	return log.Logger{
		Level:        log.DefaultLogger.Level,
		Caller:       log.DefaultLogger.Caller,
		TimeField:    log.DefaultLogger.TimeField,
		TimeFormat:   log.DefaultLogger.TimeFormat,
		TimeLocation: log.DefaultLogger.TimeLocation,
		Writer:       log.DefaultLogger.Writer,
		Context:      log.NewContext(nil).Str("module", "session").Value(),
	}
}

// GetEventLogger returns a high-performance logger for events module
// This logger uses fast JSON output regardless of console configuration for optimal performance
func GetEventLogger() log.Logger {
	return eventLogger
}

// ConfigureETWLibraryLogger configures the ETW library logger with a separate configuration
func ConfigureETWLibraryLogger(level string, outputs []LogOutput) error {
	writer, err := createMultiWriter(outputs)
	if err != nil {
		return err
	}

	etwLogger := log.Logger{
		Level:   parseLogLevel(level),
		Caller:  0, // ETW library manages its own caller info
		Writer:  writer,
		Context: log.NewContext(nil).Str("source", "etw-lib").Value(),
	}

	etw.SetLogger(&etwLogger)
	return nil
}
