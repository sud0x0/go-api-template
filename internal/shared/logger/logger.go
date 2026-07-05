// Package logger is the canonical structured-logging interface for this
// codebase. The exported Logger type abstracts the underlying log/slog
// implementation so callers can stay on a small surface (LogError /
// LogWarn / LogInfo / LogDebug / WithRequestContext) and tests can swap in a
// recording fake without touching slog.
//
// Per-request attributes (request_id, method, path, ip, user_id) are
// bound onto the underlying *slog.Logger via slog's idiomatic .With()
// inside WithRequestContext. Every subsequent LogError / LogInfo /
// LogDebug call automatically emits them, so a single line is enough to
// identify the request, the route, and the client — incident response
// is one log query, not two.
package logger

import (
	"context"
	"log/slog"
	"os"
	"time"

	"github.com/sud0x0/go-api-template/internal/version"
)

// contextKey is an unexported type for context keys in this package.
type contextKey string

// LoggerContextKey is the context key for storing the request-scoped logger.
const LoggerContextKey contextKey = "logger"

// RequestContext is the per-request information bound onto a logger via
// WithRequestContext. Empty fields are omitted from the output — so a
// pre-auth call site can leave UserID blank and a non-HTTP caller can
// leave everything but RequestID blank.
type RequestContext struct {
	RequestID string
	Method    string
	Path      string
	RemoteIP  string
	UserID    string // populated after auth middleware runs; empty pre-auth
	// TraceID and SpanID correlate this request's log lines to its trace. They
	// are the hex-encoded OTel IDs of the active span, extracted by the request
	// logger middleware from the request context (the middleware owns the OTel
	// dependency so this package stays OTel-free). Both are empty when there is
	// no valid span (telemetry disabled, or a non-request log) and empty IDs
	// are omitted so a zero ID never appears in the output.
	TraceID string
	SpanID  string
}

// Logger is the canonical logging interface used across all packages.
//
// Implementations bind structured attributes once via WithRequestContext
// and emit them on every subsequent LogError / LogInfo / LogDebug call,
// so the call site at the leaf doesn't have to know what to thread.
type Logger interface {
	LogError(simplifiedError, actualError error)
	LogWarn(message string, args ...any)
	LogInfo(message string, args ...any)
	LogDebug(message string, args ...any)
	WithRequestContext(rc RequestContext) Logger
}

// FromContext retrieves the request-scoped logger from context.
// Falls back to the provided default logger if none is present.
func FromContext(ctx context.Context, defaultLogger Logger) Logger {
	if l, ok := ctx.Value(LoggerContextKey).(Logger); ok {
		return l
	}
	return defaultLogger
}

// SlogLogger implements Logger using slog. The struct holds a single
// *slog.Logger; WithRequestContext returns a new SlogLogger whose
// underlying *slog.Logger has the request attributes pre-bound via
// slog.Logger.With.
type SlogLogger struct {
	logger *slog.Logger
}

// NewLogger creates a Logger configured with the given log level and
// bound to a service identity. Every log line emitted by the returned
// logger carries service, version, and commit attributes, so during
// rolling deploys every line can be traced to the exact build that
// emitted it. The version and commit come from internal/version, which
// GoReleaser populates via -ldflags at build time.
//
// Log levels:
//   - "development" / "dev"   — shows all logs including debug
//   - "production"  / "prod"  — shows info, warnings, and errors
//   - "quiet"                 — shows only warnings and errors
//   - "silent"   / "errors"   — shows only errors
func NewLogger(logLevel, serviceName string) Logger {
	return newSlogLogger(serviceName, slogHandlerForLevel(logLevel, os.Stdout))
}

// newSlogLogger wraps a slog.Handler in a SlogLogger. It is the seam
// tests use to swap the underlying handler for one backed by a
// bytes.Buffer so they can inspect the emitted JSON directly.
//
// It deliberately does NOT call slog.SetDefault — a constructor must not mutate
// global state. main installs the default once via SetDefaultSlog.
func newSlogLogger(serviceName string, h slog.Handler) Logger {
	handler := h.WithAttrs([]slog.Attr{
		slog.String("service", serviceName),
		slog.String("version", version.Version),
		slog.String("commit", version.GitCommit),
	})
	sl := slog.New(handler)
	return &SlogLogger{logger: sl}
}

// SetDefaultSlog installs l as slog's process-wide default (slog.SetDefault) so
// third-party code that logs through the slog package default routes through our
// handler. Call this exactly once from main, after constructing the app logger —
// constructors intentionally do not mutate this global. A non-SlogLogger
// implementation (e.g. a test fake) is a no-op.
func SetDefaultSlog(l Logger) {
	if sl, ok := l.(*SlogLogger); ok {
		slog.SetDefault(sl.logger)
	}
}

// slogHandlerForLevel builds the JSON handler used in production with
// our RFC3339 time formatting.
func slogHandlerForLevel(logLevel string, w *os.File) slog.Handler {
	var level slog.Level
	switch logLevel {
	case "development", "dev":
		level = slog.LevelDebug
	case "production", "prod":
		level = slog.LevelInfo
	case "quiet":
		level = slog.LevelWarn
	case "silent", "errors":
		level = slog.LevelError
	default:
		level = slog.LevelInfo
	}
	opts := &slog.HandlerOptions{
		Level: level,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				if t, ok := a.Value.Any().(time.Time); ok {
					return slog.String(a.Key, t.Format(time.RFC3339))
				}
			}
			return a
		},
	}
	return slog.NewJSONHandler(w, opts)
}

// WithRequestContext returns a new logger with the per-request
// attributes pre-bound onto every emitted log line. Empty fields are
// not added — slog would otherwise emit them as empty strings, which
// pollutes the log shape.
func (l *SlogLogger) WithRequestContext(rc RequestContext) Logger {
	var attrs []any
	if rc.RequestID != "" {
		attrs = append(attrs, slog.String("request_id", rc.RequestID))
	}
	if rc.Method != "" {
		attrs = append(attrs, slog.String("method", rc.Method))
	}
	if rc.Path != "" {
		attrs = append(attrs, slog.String("path", rc.Path))
	}
	if rc.RemoteIP != "" {
		attrs = append(attrs, slog.String("ip", rc.RemoteIP))
	}
	if rc.UserID != "" {
		attrs = append(attrs, slog.String("user_id", rc.UserID))
	}
	if rc.TraceID != "" {
		attrs = append(attrs, slog.String("trace_id", rc.TraceID))
	}
	if rc.SpanID != "" {
		attrs = append(attrs, slog.String("span_id", rc.SpanID))
	}
	if len(attrs) == 0 {
		return l
	}
	return &SlogLogger{logger: l.logger.With(attrs...)}
}

// LogError emits an error event. simplifiedError is the sanitised
// message safe to surface (typically a sentinel like ErrDatabase);
// actualError is the underlying cause for diagnostics. Both end up as
// JSON fields alongside whatever per-request attributes are bound.
func (l *SlogLogger) LogError(simplifiedError error, actualError error) {
	attrs := []any{slog.String("error", simplifiedError.Error())}
	if actualError != nil {
		attrs = append(attrs, slog.String("actual_error", actualError.Error()))
	}
	l.logger.Error("error occurred", attrs...)
}

// LogWarn emits a warning event with optional key-value attributes. It is the
// level an operator selects ("quiet") to see warnings AND errors but not info —
// so warnings must be emittable at warn level, not faked as info (which "quiet"
// drops).
func (l *SlogLogger) LogWarn(message string, args ...any) {
	l.logger.Warn(message, args...)
}

// LogInfo emits an info event with optional key-value attributes.
func (l *SlogLogger) LogInfo(message string, args ...any) {
	l.logger.Info(message, args...)
}

// LogDebug emits a debug event with optional key-value attributes, symmetric
// with LogInfo.
func (l *SlogLogger) LogDebug(message string, args ...any) {
	l.logger.Debug(message, args...)
}
