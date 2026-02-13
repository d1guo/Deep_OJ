package observability

import (
	"log/slog"
	"os"
)

var defaultLogger *slog.Logger

func init() {
	// Default to JSON handler for production-like environments
	// Level can be adjusted via env var in the future
	handler := slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	})
	defaultLogger = slog.New(handler)
	slog.SetDefault(defaultLogger)
}

// Logger returns the default logger
func Logger() *slog.Logger {
	return defaultLogger
}

// WithTraceID returns a logger with trace_id field if present in context
// Note: This requires the context to actually imply strict key usage,
// for now we'll assume the traceID is passed explicitly or we extract it if we had a middleware key.
// But to keep it simple, we will allow passing traceID directly or wrapping.
func LoggerWithTrace(traceID string) *slog.Logger {
	return defaultLogger.With("trace_id", traceID)
}

// Info logs at Info level
func Info(msg string, args ...any) {
	defaultLogger.Info(msg, args...)
}

// Error logs at Error level
func Error(msg string, args ...any) {
	defaultLogger.Error(msg, args...)
}

// Fatal logs at Error level and exits
func Fatal(msg string, args ...any) {
	defaultLogger.Error(msg, args...)
	os.Exit(1)
}
