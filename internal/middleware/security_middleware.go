package middleware

import "net/http"

// SecurityHeaders returns middleware that sets security headers on all responses.
//
// Headers set:
//   - Content-Security-Policy: default-src 'none'; frame-ancestors 'none' — strict CSP for JSON APIs
//   - X-Content-Type-Options: nosniff — prevents MIME-sniffing
//   - Cache-Control: no-store — prevents caching of authenticated responses
//   - Referrer-Policy: no-referrer — prevents URL/token leakage via Referer header
//
// Content-Type is set per-response in handler methods (e.g. responseJSON).
func SecurityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		h := w.Header()
		h.Set("Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'")
		h.Set("X-Content-Type-Options", "nosniff")
		h.Set("Cache-Control", "no-store")
		h.Set("Referrer-Policy", "no-referrer")
		next.ServeHTTP(w, r)
	})
}
