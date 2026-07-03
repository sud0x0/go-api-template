package middleware

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestSecurityHeaders verifies every documented header is set on every response.
func TestSecurityHeaders(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := SecurityHeaders(next)

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	handler.ServeHTTP(rr, req)

	cases := []struct {
		header string
		want   string
	}{
		{"Content-Security-Policy", "default-src 'none'; frame-ancestors 'none'"},
		{"X-Content-Type-Options", "nosniff"},
		{"Cache-Control", "no-store"},
		{"Referrer-Policy", "no-referrer"},
	}
	for _, c := range cases {
		t.Run(c.header, func(t *testing.T) {
			if got := rr.Header().Get(c.header); got != c.want {
				t.Errorf("%s: got %q, want %q", c.header, got, c.want)
			}
		})
	}
}

// TestSecurityHeaders_AppliedRegardlessOfStatus verifies the headers are set
// even when the downstream handler returns an error status.
func TestSecurityHeaders_AppliedRegardlessOfStatus(t *testing.T) {
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
	})
	handler := SecurityHeaders(next)

	rr := httptest.NewRecorder()
	handler.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/anything", nil))

	if got := rr.Header().Get("Content-Security-Policy"); got == "" {
		t.Error("CSP header missing on 5xx response")
	}
}
