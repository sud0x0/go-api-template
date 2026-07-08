package middleware

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	chimw "github.com/go-chi/chi/v5/middleware"
)

// TestSanitizeRequestID verifies the inbound X-Request-Id trust boundary: a
// well-formed inbound ID is preserved (so cross-service correlation works), and a
// hostile or oversized one is stripped so chi mints a fresh, trusted ID. The
// wrapper is exercised in front of chi's real RequestID middleware, exactly as
// wired in main.go, and the effective ID is read back via chimw.GetReqID.
func TestSanitizeRequestID(t *testing.T) {
	cases := []struct {
		name      string
		inbound   string
		preserved bool // true → the inbound value must survive verbatim
	}{
		{name: "well-formed token preserved", inbound: "abc-123_ID.0", preserved: true},
		{name: "uuid preserved", inbound: "550e8400-e29b-41d4-a716-446655440000", preserved: true},
		{name: "exactly 64 chars preserved", inbound: strings.Repeat("a", 64), preserved: true},
		{name: "65 chars regenerated", inbound: strings.Repeat("a", 65), preserved: false},
		{name: "1KB garbage regenerated", inbound: strings.Repeat("x", 1024), preserved: false},
		{name: "CRLF control chars regenerated", inbound: "abc\r\ndef", preserved: false},
		{name: "whitespace regenerated", inbound: "has space", preserved: false},
		{name: "slash regenerated", inbound: "host/prefix-000001", preserved: false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var got string
			// SanitizeRequestID → chi.RequestID → handler, mirroring main.go's order.
			h := SanitizeRequestID(chimw.RequestID(http.HandlerFunc(
				func(_ http.ResponseWriter, r *http.Request) {
					got = chimw.GetReqID(r.Context())
				})))

			req := httptest.NewRequest(http.MethodGet, "/", nil)
			req.Header.Set(chimw.RequestIDHeader, c.inbound)
			h.ServeHTTP(httptest.NewRecorder(), req)

			if c.preserved {
				if got != c.inbound {
					t.Errorf("well-formed inbound ID must be preserved: got %q, want %q", got, c.inbound)
				}
				return
			}
			// Regenerated: the hostile value must NOT survive, and a fresh non-empty
			// ID must have been minted in its place.
			if got == c.inbound {
				t.Errorf("hostile inbound ID must be regenerated, but survived: %q", got)
			}
			if got == "" {
				t.Error("a fresh request ID must be minted when the inbound one is rejected")
			}
		})
	}
}

// TestValidRequestID unit-tests the charset/length predicate directly.
func TestValidRequestID(t *testing.T) {
	valid := []string{"a", "A1._-", strings.Repeat("z", maxRequestIDLen)}
	invalid := []string{"", strings.Repeat("z", maxRequestIDLen+1), "a b", "a/b", "a\tb", "a\nb", "café"}
	for _, s := range valid {
		if !validRequestID(s) {
			t.Errorf("validRequestID(%q) = false, want true", s)
		}
	}
	for _, s := range invalid {
		if validRequestID(s) {
			t.Errorf("validRequestID(%q) = true, want false", s)
		}
	}
}
