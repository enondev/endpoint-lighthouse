// Package observability provides structured logging, Prometheus metrics, and
// OpenTelemetry tracing for the Endpoint Lighthouse API service.
package observability

import (
	"context"
	"log/slog"
	"os"
	"strings"
)

// InitLogger configures and returns a structured slog logger. Use JSON output
// in production and console (text) output locally.
//
// Callers should capture the returned logger and inject it into handlers and
// middleware rather than reaching for a package-global, which keeps loggers
// test-isolated and avoids cross-test state leakage.
func InitLogger(environment, level string) *slog.Logger {
	var handler slog.Handler
	opts := &slog.HandlerOptions{
		AddSource: false,
	}

	switch strings.ToLower(level) {
	case "debug":
		opts.Level = slog.LevelDebug
	case "warn", "warning":
		opts.Level = slog.LevelWarn
	case "error":
		opts.Level = slog.LevelError
	default:
		opts.Level = slog.LevelInfo
	}

	switch strings.ToLower(environment) {
	case "production", "prod":
		handler = slog.NewJSONHandler(os.Stdout, opts)
	default:
		handler = slog.NewTextHandler(os.Stdout, opts)
	}

	return slog.New(handler).With(slog.String("service", "api"))
}

// WithTrace returns slog attributes carrying trace context when available.
func WithTrace(ctx context.Context) []any {
	if traceID := traceIDFromCtx(ctx); traceID != "" {
		return []any{slog.String("trace_id", traceID)}
	}
	return nil
}
