package middleware

import (
	"context"
	"net/http"
	"time"

	chimw "github.com/go-chi/chi/v5/middleware"

	"github.com/sud0x0/go-api-template/internal/shared/logger"
)

// RequestLogger returns middleware that builds a per-request logger
// pre-bound with request_id, method, path, and client IP, stores it on
// the request context, and emits a "request completed" line at the end
// of the request with the response status and duration.
//
// Because the per-request fields are bound onto the logger via slog's
// .With() inside WithRequestContext, every log line emitted during the
// request — including LogError calls from inside handlers — carries
// them automatically. A single log line is enough to identify the
// request, the route, and the client; incident response is one query.
//
// RealIP MUST run before this middleware so r.RemoteAddr is the
// resolved client IP, not the upstream proxy. See cmd/api/main.go.
func RequestLogger(log logger.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			reqLogger := log.WithRequestContext(logger.RequestContext{
				RequestID: chimw.GetReqID(r.Context()),
				Method:    r.Method,
				Path:      r.URL.Path,
				RemoteIP:  r.RemoteAddr,
			})
			ctx := context.WithValue(r.Context(), logger.LoggerContextKey, reqLogger)

			ww := chimw.NewWrapResponseWriter(w, r.ProtoMajor)
			start := time.Now()
			next.ServeHTTP(ww, r.WithContext(ctx))
			duration := time.Since(start)

			// Method, path, and ip are already bound on reqLogger, so the
			// "request completed" line only needs the end-of-request fields.
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
