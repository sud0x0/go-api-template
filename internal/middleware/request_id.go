package middleware

import (
	"net/http"

	chimw "github.com/go-chi/chi/v5/middleware"
)

// maxRequestIDLen bounds an accepted inbound request ID. 64 bytes is comfortably
// longer than chi's own generated IDs and long enough for the common formats a
// caller might send (UUID, ULID), while short enough that a hostile header cannot
// bloat every log line and every outbound X-Request-ID it is propagated onto.
const maxRequestIDLen = 64

// SanitizeRequestID must be registered IMMEDIATELY BEFORE chi's RequestID
// middleware.
//
// chi's RequestID adopts an inbound X-Request-Id header VERBATIM, with no
// validation (see the vendored middleware: it does `r.Header.Get` and uses the
// value as-is when non-empty). That value is then trusted for log correlation
// AND propagated onto outbound calls by internal/httpclient. An unvalidated,
// client-settable header is therefore an injection vector: 1 KB of garbage bloats
// every log line the request emits, and control characters (CR/LF, ANSI escapes)
// can forge or corrupt downstream log entries and headers.
//
// This wrapper bounds that trust. An inbound X-Request-Id is KEPT only when it is
// a sane token (see validRequestID); otherwise the header is stripped so chi's
// RequestID generates a fresh, trusted ID. A well-formed inbound ID is preserved
// so genuine cross-service correlation still works.
func SanitizeRequestID(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if id := r.Header.Get(chimw.RequestIDHeader); id != "" && !validRequestID(id) {
			// Strip the untrusted header; chi's RequestID then mints its own ID.
			r.Header.Del(chimw.RequestIDHeader)
		}
		next.ServeHTTP(w, r)
	})
}

// validRequestID reports whether id is a safe inbound request-id token: non-empty,
// at most maxRequestIDLen bytes, and drawn only from [A-Za-z0-9._-]. The charset
// deliberately excludes whitespace and control characters (no log forging) and
// any byte unsafe to reflect back into a header or a structured log field. It is
// applied ONLY to inbound values; chi's own generated IDs (which contain '/') are
// trusted and never re-validated here.
func validRequestID(id string) bool {
	if len(id) == 0 || len(id) > maxRequestIDLen {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		switch {
		case c >= 'a' && c <= 'z',
			c >= 'A' && c <= 'Z',
			c >= '0' && c <= '9',
			c == '.', c == '_', c == '-':
			// allowed
		default:
			return false
		}
	}
	return true
}
