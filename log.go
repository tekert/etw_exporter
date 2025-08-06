// log.go
package main

import (
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/phuslu/log"
	"github.com/tekert/golang-etw/etw"
)

var (
// Main application logger - uses log.DefaultLogger
// This avoids the need for plog prefix throughout the codebase
// Module-specific loggers with prefixes will be created from this
)

// LogOutputType defines the type of log output
type LogOutputType string

const (
	LogOutputIO       LogOutputType = "io"
	LogOutputConsole  LogOutputType = "console"
	LogOutputFile     LogOutputType = "file"
	LogOutputSyslog   LogOutputType = "syslog"
	LogOutputEventlog LogOutputType = "eventlog"
	LogOutputMulti    LogOutputType = "multi"
)

// LogGlobalConfig contains global logger settings
type LogGlobalConfig struct {
	Level        string `json:"level"`        // Log level: trace, debug, info, warn, error, fatal
	Caller       int    `json:"caller"`       // Caller information depth (0=disabled, 1=enabled, -1=full path)
	TimeField    string `json:"timeField"`    // Time field name in output (default: "time")
	TimeFormat   string `json:"timeFormat"`   // Time format (empty=RFC3339 with milliseconds)
	TimeLocation string `json:"timeLocation"` // Time location (default: "Local")
}

// LogIOConfig contains IO writer configuration
type LogIOConfig struct {
	Writer string `json:"writer"` // "stdout", "stderr"
	Async  bool   `json:"async"`  // Use async writer
}

// LogConsoleConfig contains console writer configuration
// WARNING: Do not use ConsoleWriter on critical path of high concurrency applications!
// ConsoleWriter should NOT be used in event processing hot paths due to performance impact.
type LogConsoleConfig struct {
	ColorOutput    bool   `json:"colorOutput"`    // Enable colorized output
	QuoteString    bool   `json:"quoteString"`    // Quote string values
	EndWithMessage bool   `json:"endWithMessage"` // Output message at end of line
	Format         string `json:"format"`         // "json" or custom format
	Writer         string `json:"writer"`         // "stdout", "stderr"
	Async          bool   `json:"async"`          // Use async writer
}

// LogFileConfig contains file writer configuration
type LogFileConfig struct {
	Filename     string `json:"filename"`     // Log file path
	FileMode     int    `json:"fileMode"`     // File permissions (e.g., 0644)
	MaxSize      int64  `json:"maxSize"`      // Maximum file size in bytes
	MaxBackups   int    `json:"maxBackups"`   // Maximum number of backup files
	TimeFormat   string `json:"timeFormat"`   // Time format for rotation
	LocalTime    bool   `json:"localTime"`    // Use local time for timestamps
	HostName     bool   `json:"hostName"`     // Include hostname in filename
	ProcessID    bool   `json:"processID"`    // Include process ID in filename
	EnsureFolder bool   `json:"ensureFolder"` // Create directory if needed
	Async        bool   `json:"async"`        // Use async writer
}

// LogSyslogConfig contains syslog writer configuration
type LogSyslogConfig struct {
	Network  string `json:"network"`  // "tcp", "udp", "unixgram"
	Address  string `json:"address"`  // Address (e.g., "localhost:514")
	Hostname string `json:"hostname"` // Hostname for syslog
	Tag      string `json:"tag"`      // Syslog tag
	Marker   string `json:"marker"`   // Marker prefix
	Async    bool   `json:"async"`    // Use async writer
}

// LogEventlogConfig contains Windows event log configuration
type LogEventlogConfig struct {
	Source string `json:"source"` // Event source name
	ID     int    `json:"id"`     // Event ID
	Host   string `json:"host"`   // Host name
	Async  bool   `json:"async"`  // Use async writer
}

// LogMultiConfig contains multi-level writer configuration
type LogMultiConfig struct {
	InfoWriter    string `json:"infoWriter"`    // Writer type for info level
	WarnWriter    string `json:"warnWriter"`    // Writer type for warn level
	ErrorWriter   string `json:"errorWriter"`   // Writer type for error level
	ConsoleWriter string `json:"consoleWriter"` // Console writer type
	ConsoleLevel  string `json:"consoleLevel"`  // Minimum level for console
}

// LogOutputConfig represents a single output configuration
type LogOutputConfig struct {
	Type    LogOutputType   `json:"type"`    // Output type
	Enabled bool            `json:"enabled"` // Whether this output is enabled
	Config  json.RawMessage `json:"config"`  // Configuration specific to the output type
}

// LogConfig holds the complete logging configuration
type LogConfig struct {
	Global  LogGlobalConfig   `json:"global"`  // Global logger settings
	Outputs []LogOutputConfig `json:"outputs"` // Output configurations
}

// DefaultLogConfig returns the default logging configuration
func DefaultLogConfig() LogConfig {
	return LogConfig{
		Global: LogGlobalConfig{
			Level:        "info",
			Caller:       0,
			TimeField:    "",
			TimeFormat:   "",
			TimeLocation: "Local",
		},
		Outputs: []LogOutputConfig{
			{
				Type:    LogOutputIO,
				Enabled: true,
				Config:  json.RawMessage(`{"writer": "stderr", "async": false}`),
			},
		},
	}
}

// parseOutputConfig parses the configuration for a specific output type
func parseOutputConfig(outputType LogOutputType, config json.RawMessage) (interface{}, error) {
	switch outputType {
	case LogOutputIO:
		var cfg LogIOConfig
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, err
		}
		return cfg, nil
	case LogOutputConsole:
		var cfg LogConsoleConfig
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, err
		}
		return cfg, nil
	case LogOutputFile:
		var cfg LogFileConfig
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, err
		}
		return cfg, nil
	case LogOutputSyslog:
		var cfg LogSyslogConfig
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, err
		}
		return cfg, nil
	case LogOutputEventlog:
		var cfg LogEventlogConfig
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, err
		}
		return cfg, nil
	case LogOutputMulti:
		var cfg LogMultiConfig
		if err := json.Unmarshal(config, &cfg); err != nil {
			return nil, err
		}
		return cfg, nil
	default:
		return nil, nil
	}
}

// createWriter creates a log.Writer based on the output configuration
func createWriter(outputConfig LogOutputConfig) (log.Writer, error) {
	if !outputConfig.Enabled {
		return nil, nil
	}

	parsedConfig, err := parseOutputConfig(outputConfig.Type, outputConfig.Config)
	if err != nil {
		return nil, err
	}

	switch outputConfig.Type {
	case LogOutputIO:
		cfg := parsedConfig.(LogIOConfig)
		var writer io.Writer
		switch cfg.Writer {
		case "stdout":
			writer = os.Stdout
		case "stderr":
			writer = os.Stderr
		default:
			writer = os.Stderr
		}

		baseWriter := &log.IOWriter{Writer: writer}
		if cfg.Async {
			return &log.AsyncWriter{
				ChannelSize: 4096,
				Writer:      baseWriter,
			}, nil
		}
		return baseWriter, nil

	case LogOutputConsole:
		// WARNING: ConsoleWriter should not be used in high-performance hot paths!
		cfg := parsedConfig.(LogConsoleConfig)
		var writer io.Writer
		switch cfg.Writer {
		case "stdout":
			writer = os.Stdout
		case "stderr":
			writer = os.Stderr
		default:
			writer = os.Stderr
		}

		baseWriter := &log.ConsoleWriter{
			ColorOutput:    cfg.ColorOutput,
			QuoteString:    cfg.QuoteString,
			EndWithMessage: cfg.EndWithMessage,
			Writer:         writer,
		}

		if cfg.Async {
			return &log.AsyncWriter{
				ChannelSize: 4096,
				Writer:      baseWriter,
			}, nil
		}
		return baseWriter, nil

	case LogOutputFile:
		cfg := parsedConfig.(LogFileConfig)

		// Ensure directory exists if requested
		if cfg.EnsureFolder {
			dir := filepath.Dir(cfg.Filename)
			if err := os.MkdirAll(dir, 0755); err != nil {
				return nil, err
			}
		}

		baseWriter := &log.FileWriter{
			Filename:     cfg.Filename,
			FileMode:     os.FileMode(cfg.FileMode),
			MaxSize:      cfg.MaxSize,
			MaxBackups:   cfg.MaxBackups,
			TimeFormat:   cfg.TimeFormat,
			LocalTime:    cfg.LocalTime,
			HostName:     cfg.HostName,
			ProcessID:    cfg.ProcessID,
			EnsureFolder: cfg.EnsureFolder,
		}

		if cfg.Async {
			return &log.AsyncWriter{
				ChannelSize: 4096,
				Writer:      baseWriter,
			}, nil
		}
		return baseWriter, nil

	case LogOutputSyslog:
		cfg := parsedConfig.(LogSyslogConfig)
		baseWriter := &log.SyslogWriter{
			Network:  cfg.Network,
			Address:  cfg.Address,
			Hostname: cfg.Hostname,
			Tag:      cfg.Tag,
			Marker:   cfg.Marker,
		}

		if cfg.Async {
			return &log.AsyncWriter{
				ChannelSize: 4096,
				Writer:      baseWriter,
			}, nil
		}
		return baseWriter, nil

	case LogOutputEventlog:
		cfg := parsedConfig.(LogEventlogConfig)
		baseWriter := &log.EventlogWriter{
			Source: cfg.Source,
			ID:     uintptr(cfg.ID),
			Host:   cfg.Host,
		}

		if cfg.Async {
			return &log.AsyncWriter{
				ChannelSize: 4096,
				Writer:      baseWriter,
			}, nil
		}
		return baseWriter, nil

	case LogOutputMulti:
		// TODO(tekert): Implement multi-level writer or not
		// {
		// 	"type": "multi",
		// 	"enabled": false,
		// 	"config": {
		// 		"infoWriter": "io",
		// 		"warnWriter": "file",
		// 		"errorWriter": "file",
		// 		"consoleWriter": "console",
		// 		"consoleLevel": "warn"
		// }
		// Multi-level writer requires reference to other outputs
		// This is more complex and would need additional implementation
		return nil, nil

	default:
		return nil, nil
	}
}

// createMultiWriter creates a multi-writer that outputs to multiple destinations
func createMultiWriter(outputs []LogOutputConfig) (log.Writer, error) {
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
		return writers[0], nil
	}

	// Use MultiEntryWriter for multiple outputs
	multiWriter := log.MultiEntryWriter(writers)
	return &multiWriter, nil
}

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

// ConfigureLogging configures the global logger and module-specific loggers
func ConfigureLogging(config LogConfig) error {
	// Create the main writer from all enabled outputs
	writer, err := createMultiWriter(config.Outputs)
	if err != nil {
		return err
	}

	// Configure the default logger
	log.DefaultLogger = log.Logger{
		Level:        parseLogLevel(config.Global.Level),
		Caller:       config.Global.Caller,
		TimeField:    config.Global.TimeField,
		TimeFormat:   config.Global.TimeFormat,
		TimeLocation: parseTimeLocation(config.Global.TimeLocation),
		Writer:       writer,
	}

	return nil
}

// GetDiskIOLogger returns a logger with disk-io module context
// WARNING: If ConsoleWriter is configured, do NOT use this logger in the ETW event processing hot path!
func GetDiskIOLogger() log.Logger {
	return log.Logger{
		Level:        log.DefaultLogger.Level,
		Caller:       log.DefaultLogger.Caller,
		TimeField:    log.DefaultLogger.TimeField,
		TimeFormat:   log.DefaultLogger.TimeFormat,
		TimeLocation: log.DefaultLogger.TimeLocation,
		Writer:       log.DefaultLogger.Writer,
		Context:      log.NewContext(nil).Str("module", "disk-io").Value(),
	}
}

// GetThreadLogger returns a logger with thread module context
// WARNING: If ConsoleWriter is configured, do NOT use this logger in the ETW event processing hot path!
func GetThreadLogger() log.Logger {
	return log.Logger{
		Level:        log.DefaultLogger.Level,
		Caller:       log.DefaultLogger.Caller,
		TimeField:    log.DefaultLogger.TimeField,
		TimeFormat:   log.DefaultLogger.TimeFormat,
		TimeLocation: log.DefaultLogger.TimeLocation,
		Writer:       log.DefaultLogger.Writer,
		Context:      log.NewContext(nil).Str("module", "thread").Value(),
	}
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

// GetEventLogger returns a logger with events module context
// WARNING: If ConsoleWriter is configured, do NOT use this logger in the ETW event processing hot path!
// The event processing pipeline handles thousands of events and requires maximum performance.
func GetEventLogger() log.Logger {
	return log.Logger{
		Level:        log.DefaultLogger.Level,
		Caller:       log.DefaultLogger.Caller,
		TimeField:    log.DefaultLogger.TimeField,
		TimeFormat:   log.DefaultLogger.TimeFormat,
		TimeLocation: log.DefaultLogger.TimeLocation,
		Writer:       log.DefaultLogger.Writer,
		Context:      log.NewContext(nil).Str("module", "events").Value(),
	}
}

// ConfigureETWLibraryLogger configures the ETW library logger with a separate configuration
func ConfigureETWLibraryLogger(level string, outputs []LogOutputConfig) error {
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
