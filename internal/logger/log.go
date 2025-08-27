// log.go
package logger

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"etw_exporter/internal/config"

	"github.com/phuslu/log"
	"github.com/tekert/goetw/etw"
	"github.com/tekert/goetw/logsampler"
	"github.com/tekert/goetw/logsampler/logadapters"
)

// NOTE: use example: log = logger.NewSampledLoggerWithContext("event-processor")

var (
	// mainSampler holds the global sampler for the application's hot-paths.
	mainSampler logsampler.Sampler
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
func createConsoleWriter(config *config.ConsoleConfig) (log.Writer, error) {
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

	// TODO: (report the author). see if can isolate the problem
	// TODO: Delete this, not needed, is a problem with the library
	// https://github.com/phuslu/log/issues/105

	if config.Async {
		return &log.AsyncWriter{
			ChannelSize: 4096,
			Writer:      writer,
		}, nil
	}
	return writer, nil
}

// createFileWriter creates a file writer based on configuration
func createFileWriter(config *config.FileConfig) (log.Writer, error) {
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
func createSyslogWriter(config *config.SyslogConfig) (log.Writer, error) {
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
func createEventlogWriter(config *config.EventlogConfig) (log.Writer, error) {
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
func createWriter(output config.LogOutput) (log.Writer, error) {
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
func createMultiWriter(outputs []config.LogOutput) (log.Writer, error) {
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

// ConfigureLogging configures the global DefaultLogger with user configuration
func ConfigureLogging(config config.LoggingConfig) error {
	// Create a multi-writer that handles all configured outputs
	multiWriter, err := createMultiWriter(config.Outputs)
	if err != nil {
		return err
	}

	// Configure the default logger (used by main application and as base for component loggers)
	log.DefaultLogger = log.Logger{
		Level:        parseLogLevel(config.Defaults.Level),
		Caller:       config.Defaults.Caller,
		TimeField:    config.Defaults.TimeField,
		TimeFormat:   mapTimeFormat(config.Defaults.TimeFormat),
		TimeLocation: parseTimeLocation(config.Defaults.TimeLocation),
		Writer:       multiWriter,
	}

	// Create and configure the application's main sampler.
	backoffConfig := logsampler.BackoffConfig{
		InitialInterval: 1 * time.Second,
		MaxInterval:     1 * time.Hour,
		Factor:          1.5,
		ResetInterval:   10 * time.Minute,
	}
	// The sampler needs a logger to report summaries. We'll use the default logger.
	reporter := &logadapters.SummaryReporter{Logger: &log.DefaultLogger}
	mainSampler = logsampler.NewDeduplicatingSampler(backoffConfig, reporter) // uses extra goroutine
	//mainSampler = logsampler.NewEventDrivenSampler(backoffConfig, reporter) // no goroutines

	// Configure ETW library logging
	if err := ConfigureETWLibraryLogger(config.LibLevel, multiWriter); err != nil {
		return fmt.Errorf("failed to configure ETW library logging: %w", err)
	}

	log.Info().
		Str("app_level", config.Defaults.Level).
		Str("lib_level", config.LibLevel).
		Int("outputs", len(config.Outputs)).
		Msg("Loggers configured")

	return nil
}

// NewLoggerWithContext creates a new logger by copying the global DefaultLogger
// (which contains all user configuration) and adding component-specific context.
// This should be called after ConfigureLogging has been called to ensure
// the DefaultLogger is properly configured.
func NewLoggerWithContext(component string) log.Logger {
	// Create a copy to avoid modifying the original logger
	bl := &log.DefaultLogger
	return log.Logger{
		Level:        bl.Level,
		Caller:       0, // Disable caller for component loggers to avoid confusion
		TimeField:    bl.TimeField,
		TimeFormat:   bl.TimeFormat,
		TimeLocation: bl.TimeLocation,
		Writer:       bl.Writer,
		Context:      log.NewContext(bl.Context).Str("component", component).Value(),
	}
}

// NewSampledLoggerCtx creates a new sampled logger for a specific component.
// It uses the globally configured sampler.
func NewSampledLoggerCtx(component string) *logadapters.SampledLogger {
	// Create a base logger for the component, similar to NewLoggerWithContext.
	bl := &log.DefaultLogger
	componentLogger := &log.Logger{
		Level:        bl.Level,
		Caller:       0, // Disable caller for component loggers to avoid confusion
		TimeField:    bl.TimeField,
		TimeFormat:   bl.TimeFormat,
		TimeLocation: bl.TimeLocation,
		Writer:       bl.Writer,
		Context:      log.NewContext(bl.Context).Str("component", component).Value(),
	}

	// Wrap the component logger with the sampler adapter.
	return logadapters.NewSampledLogger(componentLogger, mainSampler)
}

// ConfigureETWLibraryLogger configures the ETW library logger with a separate configuration
func ConfigureETWLibraryLogger(level string, sharedMultiWriter log.Writer) error {
	lm := etw.GetLogManager()
	ctx := log.NewContext(nil).Str("source", "etw-lib").Value()
	levels := map[etw.LoggerName]log.Level{
		etw.ConsumerLogger: parseLogLevel(level),
		etw.SessionLogger:  parseLogLevel(level),
		etw.DefaultLogger:  parseLogLevel(level),
	}
	lm.SetLogLevels(levels)
	lm.SetBaseContext(ctx)
	lm.SetWriter(sharedMultiWriter)

	//etw.SetLogger(&etwLogger)
	return nil
}
