package middleware

import (
	"net/http"
	"slices"
)

// CORSConfig configures the CORS middleware.
type CORSConfig struct {
	AllowedOrigins   []string
	AllowCredentials bool
}

// CORS returns middleware that handles cross-origin resource sharing.
//
// Vary: Origin is set on every response (allowed or not) to prevent shared
// caches (CDNs, reverse proxies) from serving a response with one Origin's
// Access-Control-Allow-Origin header to a different origin's request.
//
// Access-Control-Allow-Credentials is only emitted when the configuration
// enables it. Sending it by default is unsafe for APIs that don't use
// credentialed requests and unnecessary for server-to-server callers.
//
// Never include "*" in production AllowedOrigins. Always specify explicit
// origins. If your API has no browser clients (server-to-server only),
// remove this middleware from the chain entirely.
func CORS(cfg CORSConfig) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			// Always advertise that the response varies with Origin,
			// even on rejected origins, so caches do not poison.
			w.Header().Add("Vary", "Origin")

			origin := r.Header.Get("Origin")
			if origin != "" && isAllowed(origin, cfg.AllowedOrigins) {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				w.Header().Set("Access-Control-Allow-Methods", "GET, POST, PUT, DELETE, OPTIONS")
				w.Header().Set("Access-Control-Allow-Headers", "Authorization, Content-Type, X-Request-ID")
				if cfg.AllowCredentials {
					w.Header().Set("Access-Control-Allow-Credentials", "true")
				}
				w.Header().Set("Access-Control-Max-Age", "3600")
			}

			// Handle preflight.
			if r.Method == http.MethodOptions {
				w.WriteHeader(http.StatusNoContent)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func isAllowed(origin string, allowed []string) bool {
	return slices.Contains(allowed, origin)
}
