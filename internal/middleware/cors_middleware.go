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

// defaultAllowHeaders is the Access-Control-Allow-Headers value returned on a
// preflight that announced NO Access-Control-Request-Headers. When the browser
// does announce headers we reflect exactly those instead (see CORS).
const defaultAllowHeaders = "Authorization, Content-Type, X-Request-ID"

// allowMethods is the Access-Control-Allow-Methods value for an allowed preflight.
const allowMethods = "GET, POST, PUT, DELETE, OPTIONS"

// CORS returns middleware that handles cross-origin resource sharing.
//
// Vary: Origin is set on every response (allowed or not) to prevent shared
// caches (CDNs, reverse proxies) from serving a response with one Origin's
// Access-Control-Allow-Origin header to a different origin's request.
//
// A CORS PREFLIGHT is specifically an OPTIONS request carrying an
// Access-Control-Request-Method header. Only such a request is short-circuited
// with 204; a bare OPTIONS (no preflight header) is NOT swallowed — it may be a
// legitimate application OPTIONS request — and falls through to the handler. On a
// preflight for an allowed origin the middleware REFLECTS the headers the browser
// announced in Access-Control-Request-Headers (falling back to a default set)
// and adds Access-Control-Request-Method/-Headers to Vary so caches key on them.
// The preflight-only headers (Allow-Methods/-Headers, Max-Age) are sent only on
// the preflight response, where they are meaningful; the actual cross-origin
// response carries just Allow-Origin (and Allow-Credentials when enabled).
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
			originAllowed := origin != "" && isAllowed(origin, cfg.AllowedOrigins)

			// A genuine preflight is OPTIONS + Access-Control-Request-Method.
			if r.Method == http.MethodOptions && r.Header.Get("Access-Control-Request-Method") != "" {
				// The preflight decision varies with the requested method/headers,
				// so caches must key on them too.
				w.Header().Add("Vary", "Access-Control-Request-Method")
				w.Header().Add("Vary", "Access-Control-Request-Headers")

				if originAllowed {
					w.Header().Set("Access-Control-Allow-Origin", origin)
					w.Header().Set("Access-Control-Allow-Methods", allowMethods)
					// Reflect exactly the headers the browser announced it will send,
					// so the preflight approves that set (and no more). Fall back to a
					// default set when the browser announced none.
					if reqHeaders := r.Header.Get("Access-Control-Request-Headers"); reqHeaders != "" {
						w.Header().Set("Access-Control-Allow-Headers", reqHeaders)
					} else {
						w.Header().Set("Access-Control-Allow-Headers", defaultAllowHeaders)
					}
					if cfg.AllowCredentials {
						w.Header().Set("Access-Control-Allow-Credentials", "true")
					}
					w.Header().Set("Access-Control-Max-Age", "3600")
				}
				// A preflight is terminal: a disallowed origin simply gets a 204 with
				// no CORS headers, which the browser treats as a rejection.
				w.WriteHeader(http.StatusNoContent)
				return
			}

			// Actual request (or a bare, non-preflight OPTIONS): attach the
			// cross-origin response headers for an allowed origin and pass through.
			if originAllowed {
				w.Header().Set("Access-Control-Allow-Origin", origin)
				if cfg.AllowCredentials {
					w.Header().Set("Access-Control-Allow-Credentials", "true")
				}
			}

			next.ServeHTTP(w, r)
		})
	}
}

func isAllowed(origin string, allowed []string) bool {
	return slices.Contains(allowed, origin)
}
