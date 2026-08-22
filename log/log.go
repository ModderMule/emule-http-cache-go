// Package log provides structured, leveled logging built on Uber zap. It is a
// copy of the maxmind-proxy log package, which is itself the standalone
// equivalent of verified-gateway's: a small Logger interface decouples call
// sites from the backend, a viper-driven Configuration fans output across
// multiple sinks (console, rotating file, in-memory buffer), and two adapter
// seams let third-party libraries log through it.
//
// # Adapters
//
// Anything that wants an io.Writer (gin's output, the stdlib log package,
// http.Server.ErrorLog, ...) can use GetWriter(), which forwards each written
// line back into the Logger.
//
// Typed logging interfaces are adapted by embedding the third-party config and
// delegating to a Logger field. This package deliberately imports nothing but
// zap and viper; the seam is the Logger interface itself.
//
// # A note on chunk URLs
//
// A chunk URL is a bearer token: the 128-bit id in /v1/chunks/<id> is the only
// thing guarding that chunk's ciphertext. Nothing in this package may be pointed
// at a raw request URI. http_public's access log scrubs the id first.
package log

import (
	"github.com/pkg/errors"
	"github.com/spf13/viper"
)

// Fields is a set of structured key/value pairs attached to a Logger.
type Fields map[string]interface{}

// LoggerLevel is the minimum level a sink emits.
type LoggerLevel string

const (
	Debug LoggerLevel = "debug"
	Info  LoggerLevel = "info"
	Warn  LoggerLevel = "warn"
	Error LoggerLevel = "error"
	Fatal LoggerLevel = "fatal"
)

// LoggerInstance selects the backend implementation.
type LoggerInstance int

const InstanceZapLogger LoggerInstance = iota

// DefaultLogger is the backend used unless another is requested.
var DefaultLogger = InstanceZapLogger

var errInvalidLoggerInstance = errors.New("invalid logger instance")

// Logger is the minimal logging surface used across the app. WithFields returns
// a child logger that tags every line with the given fields.
type Logger interface {
	Debugf(format string, args ...interface{})
	Infof(format string, args ...interface{})
	Warnf(format string, args ...interface{})
	Errorf(format string, args ...interface{})
	Fatalf(format string, args ...interface{})
	Panicf(format string, args ...interface{})
	WithFields(keyValues Fields) Logger
}

// Configuration describes the enabled sinks and their formats/levels.
type Configuration struct {
	// console (stdout) logging
	EnableConsole     bool
	ConsoleJSONFormat bool
	ConsoleLevel      LoggerLevel
	Color             bool

	// rotating file logging
	EnableFile     bool
	FileJSONFormat bool
	FileLevel      LoggerLevel
	FileLocation   string

	// in-memory buffer (recent lines, e.g. for future introspection)
	EnableBuffer     bool
	BufferLines      int
	BufferJSONFormat bool
	BufferLevel      LoggerLevel

	// FilterPrefixes are line prefixes the io.Writer adapter silently drops.
	FilterPrefixes []string

	// AccessLog enables the per-request log line in http_public.
	AccessLog bool
}

// NewConfig builds a Configuration from viper. Console output is always on; the
// file and buffer sinks are opt-in. Keys are lowercase to match the proxy's
// config style (env overrides: log.level -> LOG_LEVEL, log.json -> LOG_JSON, ...).
func NewConfig(conf *viper.Viper) *Configuration {
	logLevel := NormalizeLogLevel(conf.GetString("log.level"))

	fileLocation := conf.GetString("log.file_location")
	if fileLocation == "" {
		fileLocation = "log.log"
	}
	bufferLines := conf.GetInt("log.buffer_lines")
	if bufferLines == 0 {
		bufferLines = 10000
	}

	return &Configuration{
		EnableConsole:     true,
		ConsoleJSONFormat: conf.GetBool("log.json"),
		Color:             conf.GetBool("log.color"),
		ConsoleLevel:      logLevel,

		EnableFile:     conf.GetBool("log.enable_file"),
		FileJSONFormat: conf.GetBool("log.json"),
		FileLevel:      logLevel,
		FileLocation:   fileLocation,

		EnableBuffer:     conf.GetBool("log.enable_buffer"),
		BufferLines:      bufferLines,
		BufferJSONFormat: true, // buffered lines are served as JSON
		BufferLevel:      logLevel,

		FilterPrefixes: conf.GetStringSlice("log.filter_prefixes"),

		// Absent means on: a cache with no request visibility at all is a
		// worse default than one that logs scrubbed paths.
		AccessLog: !conf.IsSet("log.access_log") || conf.GetBool("log.access_log"),
	}
}

// NormalizeLogLevel maps a config string to a LoggerLevel, defaulting to Info.
func NormalizeLogLevel(logLevel string) LoggerLevel {
	switch logLevel {
	case "debug":
		return Debug
	case "info":
		return Info
	case "warn":
		return Warn
	case "error":
		return Error
	case "fatal":
		return Fatal
	default:
		return Info
	}
}

// NewLogger constructs a Logger for the given backend and wires the package
// io.Writer adapter (GetWriter) to forward into it.
func NewLogger(config *Configuration, instance LoggerInstance) (Logger, error) {
	var logger Logger
	var err error

	switch instance {
	case InstanceZapLogger:
		logger, err = newZapLogger(*config)
		if err != nil {
			return nil, err
		}
	default:
		return nil, errInvalidLoggerInstance
	}

	writer = Writer{
		Filter: config.FilterPrefixes,
		log:    logger,
	}
	return logger, nil
}

// Package-level sinks populated by NewLogger.
var (
	buffer Buffer
	writer Writer
)

// GetBuffer returns the in-memory log buffer (populated only when the buffer
// sink is enabled).
func GetBuffer() *Buffer { return &buffer }

// GetWriter returns an io.Writer that forwards each written line into the
// configured Logger. Pass it to libraries that expect an io.Writer.
func GetWriter() *Writer { return &writer }
