// log.go
package main

import (
	"bytes"
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
	// Module-specific loggers
	modDiskIOLogger  log.Logger // Disk I/O collector logger
	modThreadLogger  log.Logger // ThreadCS collector logger
	modHandlerLogger log.Logger // Event handler logger
	modProcessLogger log.Logger // Process tracker logger
	modSessionLogger log.Logger // ETW session manager logger
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

// mapTimeFormat maps string time format to log.TimeFormat
func mapTimeFormat(format string) string {
	switch format {
	case "Unix":
		return log.TimeFormatUnix
	case "UnixMs":
		return log.TimeFormatUnixMs
	default:
		return format
	}
}

// GlogFormatter implements a glog-style text format.
type GlogFormatter struct{}

// Formatter builds the log entry in glog format.
// This implementation uses a buffer for high performance, avoiding fmt.Fprintf.
func (f GlogFormatter) Formatter(w io.Writer, a *log.FormatterArgs) (int, error) {
	// Use a buffer to build the string efficiently.
	// This is much faster than fmt.Fprintf.
	var buf bytes.Buffer

	// Level (e.g., 'I' for info)
	if len(a.Level) > 0 {
		buf.WriteByte(a.Level[0] - 32) // Uppercase first letter
	} else {
		buf.WriteByte('?')
	}

	// Time, Goid, Caller
	buf.WriteString(a.Time)
	buf.WriteByte(' ')
	buf.WriteString(a.Goid)
	buf.WriteByte(' ')
	buf.WriteString(a.Caller)
	buf.WriteString("] ")

	// Message
	buf.WriteString(a.Message)
	buf.WriteByte('\n')

	return w.Write(buf.Bytes())
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
		case "logfmt":
			// Logfmt format
			consoleWriter.Formatter = log.LogfmtFormatter{TimeField: "time"}.Formatter
			writer = consoleWriter
		case "glog":
			// Glog format
			consoleWriter.Formatter = GlogFormatter{}.Formatter
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
	} else if !config.FastIO {
		// If not async and not FastIO, we are using the complex ConsoleWriter.
		// Wrap it in a mutex to make it thread-safe, preventing crashes
		// TODO: (report the author). see if can isolate the problem
		// It seems if a cgo goroutine writes to the console while another goroutine
		// is writing, it can cause a crash, this fixes that, or async.
		writer = &safeWriter{w: writer}
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
		FileMode:     0644,                         // Fixed mode for Windows
		MaxSize:      config.MaxSize * 1024 * 1024, // Convert MB to bytes
		MaxBackups:   config.MaxBackups,
		TimeFormat:   mapTimeFormat(config.TimeFormat),
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

		writer, err := createWriter(output)
		if err != nil {
			return nil, err
		}
		if writer != nil {
			writers = append(writers, writer)
		}
	}

	if len(writers) == 0 {
		// Fallback to stderr if no writers are configured
		return &log.IOWriter{Writer: os.Stderr}, nil
	}

	if len(writers) == 1 {
		// Single writer - no need for multi-writer wrapper
		return writers[0], nil
	}

	// Multiple writers - use phuslu/log's MultiEntryWriter
	multiWriter := log.MultiEntryWriter(writers)
	return &multiWriter, nil
}

// safeWriter is a simple log.Writer wrapper that ensures thread-safety via a mutex.
type safeWriter struct {
	mu sync.Mutex
	w  log.Writer
}

// WriteEntry implements the log.Writer interface by calling the wrapped
// writer's WriteEntry method under a lock.
func (sw *safeWriter) WriteEntry(e *log.Entry) (int, error) {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	return sw.w.WriteEntry(e)
}

// Close implements io.Closer to pass the close call to the underlying writer if it's a closer.
func (sw *safeWriter) Close() error {
	sw.mu.Lock()
	defer sw.mu.Unlock()
	if closer, ok := sw.w.(io.Closer); ok {
		return closer.Close()
	}
	return nil
}

func createLogger(config LoggingConfig, writer log.Writer, contextStr string) log.Logger {
	return log.Logger{
		Level:        parseLogLevel(config.Defaults.Level),
		Caller:       0, // Disable caller for performance
		TimeField:    config.Defaults.TimeField,
		TimeFormat:   mapTimeFormat(config.Defaults.TimeFormat),
		TimeLocation: parseTimeLocation(config.Defaults.TimeLocation),
		Writer:       writer,
		Context:      log.NewContext(nil).Str("module", contextStr).Value(),
	}
}

// ConfigureLogging configures the global logger and module-specific loggers
func ConfigureLogging(config LoggingConfig) error {
	// Create a multi-writer that handles all configured outputs
	multiWriter, err := createMultiWriter(config.Outputs)
	if err != nil {
		return err
	}

	// Configure the default logger (used by main application)
	log.DefaultLogger = log.Logger{
		Level:        parseLogLevel(config.Defaults.Level),
		Caller:       config.Defaults.Caller,
		TimeField:    config.Defaults.TimeField,
		TimeFormat:   mapTimeFormat(config.Defaults.TimeFormat),
		TimeLocation: parseTimeLocation(config.Defaults.TimeLocation),
		Writer:       multiWriter,
	}

	// Configure module-specific loggers using the same multi-writer
	// This allows all modules to use configured console/file outputs
	modDiskIOLogger = createLogger(config, multiWriter, "diskio")
	modThreadLogger = createLogger(config, multiWriter, "threadcs")
	modHandlerLogger = createLogger(config, multiWriter, "app-event-handler")
	modProcessLogger = createLogger(config, multiWriter, "app-process-tracker")
	modSessionLogger = createLogger(config, multiWriter, "app-etw-session")

	// Configure ETW library logging
	if err := ConfigureETWLibraryLogger(config.LibLevel, multiWriter); err != nil {
		return fmt.Errorf("failed to configure ETW library logging: %w", err)
	}

	return nil
}

func GetDiskIOLogger() log.Logger {
	return modDiskIOLogger
}

func GetThreadLogger() log.Logger {
	return modThreadLogger
}

func GetProcessLogger() log.Logger {
	return modProcessLogger
}

func GetSessionLogger() log.Logger {
	return modSessionLogger
}

func GetEventLogger() log.Logger {
	return modHandlerLogger
}

// ConfigureETWLibraryLogger configures the ETW library logger with a separate configuration
func ConfigureETWLibraryLogger(level string, sharedMultiWriter log.Writer) error {
	etwLogger := log.Logger{
		Level:   parseLogLevel(level),
		Caller:  0, // ETW library manages its own caller info
		Writer:  sharedMultiWriter,
		Context: log.NewContext(nil).Str("source", "etw-lib").Value(),
	}

	etw.SetLogger(&etwLogger)
	return nil
}
