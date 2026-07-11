package middleware

import (
	"context"
	"net/http"
	"sync"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/otel/trace"

	"github.com/sud0x0/go-api-template/internal/shared/logger"
)

// mutableLogger is a logger.Logger whose delegate can be swapped in place after
// the request-scoped logger is installed. It exists so a POST-AUTH enricher
// (BindUserID) can add user_id to the SAME logger this middleware uses for its
// "request completed" line and that downstream handlers read via
// logger.FromContext. The enrichment runs on a child context this middleware
// can't see, so a plain value in the context would leave the completion line
// un-enriched; a shared pointer whose delegate is swapped is observed by both.
// The mutex keeps it safe if a handler logs from a spawned goroutine under -race.
type mutableLogger struct {
	mu       sync.Mutex
	delegate logger.Logger
}

func (m *mutableLogger) current() logger.Logger { m.mu.Lock(); defer m.mu.Unlock(); return m.delegate }
func (m *mutableLogger) swap(l logger.Logger)   { m.mu.Lock(); defer m.mu.Unlock(); m.delegate = l }

func (m *mutableLogger) LogError(simplified, actual error) { m.current().LogError(simplified, actual) }
func (m *mutableLogger) LogWarn(msg string, args ...any)   { m.current().LogWarn(msg, args...) }
func (m *mutableLogger) LogInfo(msg string, args ...any)   { m.current().LogInfo(msg, args...) }
func (m *mutableLogger) LogDebug(msg string, args ...any)  { m.current().LogDebug(msg, args...) }
func (m *mutableLogger) WithRequestContext(rc logger.RequestContext) logger.Logger {
	return m.current().WithRequestContext(rc)
}

// BindUserID enriches the request-scoped logger with the authenticated user_id
// so every log line for an authenticated request — the handler's lines AND the
// "request completed" summary — carries it. It is a no-op when no user is set
// (unauthenticated / auth disabled) or when RequestLogger did not install a
// mutable holder. Called by the authentication middleware once it has resolved
// the internal user id.
func BindUserID(ctx context.Context, userID string) {
	if userID == "" {
		return
	}
	if h, ok := ctx.Value(logger.LoggerContextKey).(*mutableLogger); ok {
		h.swap(h.current().WithRequestContext(logger.RequestContext{UserID: userID}))
	}
}

// RequestLogger returns middleware that builds a per-request logger
// pre-bound with request_id, method, path, and client IP, stores it on
// the request context, and emits a "request completed" line at the end
// of the request with the response status and duration.
//
// Because the per-request fields are bound onto the logger via slog's
// .With() inside WithRequestContext, every log line emitted during the
// request (including LogError calls from inside handlers) carries
// them automatically. A single log line is enough to identify the
// request, the route, and the client; incident response is one query.
//
// RealIP MUST run before this middleware so r.RemoteAddr is the
// resolved client IP, not the upstream proxy. See cmd/api/main.go.
//
// PRIVACY: the bound `ip` field is a client IP address, which is PERSONAL DATA
// under GDPR/CCPA. It is logged here because it is essential for incident
// response and abuse investigation, but a fork with strict data-retention or
// minimisation rules should truncate it (e.g. drop the last octet / interface
// id) or omit it entirely, and set a short retention on the log store. Change
// the RemoteIP field passed below to adjust.
//
// It also reads the active OTel span from the request context and binds
// trace_id/span_id onto the logger, so every log line in the request correlates
// to its trace. The span is created by otelhttp, which wraps outside chi, so it
// is already on the context here. This is the whole log-correlation surface: the
// bound fields cover every request-path log without widening the Logger
// interface with a ctx parameter.
func RequestLogger(log logger.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Only bind trace_id/span_id when the span context is valid. With
			// telemetry disabled the span is a no-op and IDs are all-zero, which
			// must not appear in the output.
			var traceID, spanID string
			if sc := trace.SpanContextFromContext(r.Context()); sc.IsValid() {
				traceID = sc.TraceID().String()
				spanID = sc.SpanID().String()
			}

			base := log.WithRequestContext(logger.RequestContext{
				RequestID: chimw.GetReqID(r.Context()),
				Method:    r.Method,
				Path:      r.URL.Path,
				RemoteIP:  r.RemoteAddr,
				TraceID:   traceID,
				SpanID:    spanID,
			})
			// Install a mutable holder rather than the bound logger directly, so a
			// post-auth BindUserID call can enrich the SAME logger this middleware's
			// completion line uses and that handlers read via logger.FromContext.
			// user_id is unknown here (auth runs later, inside /api/v1); binding it
			// pre-auth is impossible, so the holder lets it be added in place.
			reqLogger := &mutableLogger{delegate: base}
			ctx := context.WithValue(r.Context(), logger.LoggerContextKey, reqLogger)

			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()
			next.ServeHTTP(ww, r.WithContext(ctx))
			duration := time.Since(start)

			// Method, path, ip, and (post-auth) user_id are already bound on the
			// logger, so the "request completed" line only needs the end-of-request
			// fields. Reading through the holder picks up any BindUserID enrichment.
			reqLogger.LogInfo("request completed",
				"status", ww.Status(),
				"duration_ms", duration.Milliseconds(),
			)

			if duration > 5*time.Second {
				reqLogger.LogWarn("slow request detected",
					"status", ww.Status(),
					"duration_ms", duration.Milliseconds(),
				)
			}
		})
	}
}
