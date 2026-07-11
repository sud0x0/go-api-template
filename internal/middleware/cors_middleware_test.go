package middleware

import (
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"
)

func newRecorderRequest(origin string) (*httptest.ResponseRecorder, *http.Request) {
	req := httptest.NewRequest(http.MethodGet, "/anything", nil)
	if origin != "" {
		req.Header.Set("Origin", origin)
	}
	return httptest.NewRecorder(), req
}

// TestCORS_VaryOrigin_AlwaysSet (item 11) verifies the Vary: Origin header
// is set on every response touched by the middleware (allowed OR rejected)
// so shared caches do not poison.
func TestCORS_VaryOrigin_AlwaysSet(t *testing.T) {
	mw := CORS(CORSConfig{AllowedOrigins: []string{"http://allowed.example"}})
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	handler := mw(next)

	t.Run("allowed origin", func(t *testing.T) {
		rr, req := newRecorderRequest("http://allowed.example")
		handler.ServeHTTP(rr, req)
		if got := rr.Header().Values("Vary"); !contains(got, "Origin") {
			t.Errorf("missing Vary: Origin (got %v)", got)
		}
	})

	t.Run("rejected origin", func(t *testing.T) {
		rr, req := newRecorderRequest("http://rejected.example")
		handler.ServeHTTP(rr, req)
		if got := rr.Header().Values("Vary"); !contains(got, "Origin") {
			t.Errorf("missing Vary: Origin (got %v)", got)
		}
	})

	t.Run("no origin", func(t *testing.T) {
		rr, req := newRecorderRequest("")
		handler.ServeHTTP(rr, req)
		if got := rr.Header().Values("Vary"); !contains(got, "Origin") {
			t.Errorf("missing Vary: Origin (got %v)", got)
		}
	})
}

// TestCORS_AllowCredentials_Configurable (item 13) verifies that the
// Allow-Credentials header is only emitted when the config enables it.
func TestCORS_AllowCredentials_Configurable(t *testing.T) {
	allowed := []string{"http://allowed.example"}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	t.Run("default false omits header", func(t *testing.T) {
		mw := CORS(CORSConfig{AllowedOrigins: allowed})
		rr, req := newRecorderRequest("http://allowed.example")
		mw(next).ServeHTTP(rr, req)
		if got := rr.Header().Get("Access-Control-Allow-Credentials"); got != "" {
			t.Errorf("expected no Allow-Credentials header, got %q", got)
		}
	})

	t.Run("explicit true sets header", func(t *testing.T) {
		mw := CORS(CORSConfig{AllowedOrigins: allowed, AllowCredentials: true})
		rr, req := newRecorderRequest("http://allowed.example")
		mw(next).ServeHTTP(rr, req)
		if got := rr.Header().Get("Access-Control-Allow-Credentials"); got != "true" {
			t.Errorf("expected Allow-Credentials: true, got %q", got)
		}
	})
}

func TestCORS_AllowedOriginReflected(t *testing.T) {
	mw := CORS(CORSConfig{AllowedOrigins: []string{"http://allowed.example"}})
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	rr, req := newRecorderRequest("http://allowed.example")
	mw(next).ServeHTTP(rr, req)

	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "http://allowed.example" {
		t.Errorf("expected reflected origin, got %q", got)
	}
}

func TestCORS_RejectedOriginNotReflected(t *testing.T) {
	mw := CORS(CORSConfig{AllowedOrigins: []string{"http://allowed.example"}})
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	rr, req := newRecorderRequest("http://rejected.example")
	mw(next).ServeHTTP(rr, req)

	if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "" {
		t.Errorf("rejected origin should not be reflected, got %q", got)
	}
}

// TestCORS_Preflight verifies the tightened preflight handling: only a genuine
// preflight (OPTIONS + Access-Control-Request-Method) is short-circuited 204,
// the announced request headers are reflected, and the request method/headers
// are added to Vary.
func TestCORS_Preflight(t *testing.T) {
	allowed := "http://allowed.example"
	mw := CORS(CORSConfig{AllowedOrigins: []string{allowed}})
	// A next handler that flags if it was reached (a genuine preflight must NOT
	// reach it; a bare OPTIONS must).
	var reached bool
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		reached = true
		w.WriteHeader(http.StatusTeapot)
	})

	preflight := func(origin, method, reqHeaders string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(http.MethodOptions, "/anything", nil)
		if origin != "" {
			req.Header.Set("Origin", origin)
		}
		if method != "" {
			req.Header.Set("Access-Control-Request-Method", method)
		}
		if reqHeaders != "" {
			req.Header.Set("Access-Control-Request-Headers", reqHeaders)
		}
		rr := httptest.NewRecorder()
		reached = false
		mw(next).ServeHTTP(rr, req)
		return rr
	}

	t.Run("genuine preflight reflects requested headers and 204s", func(t *testing.T) {
		rr := preflight(allowed, http.MethodPost, "X-Custom, Content-Type")
		if reached {
			t.Error("a genuine preflight must be short-circuited, not passed to the handler")
		}
		if rr.Code != http.StatusNoContent {
			t.Errorf("preflight status: got %d want 204", rr.Code)
		}
		if got := rr.Header().Get("Access-Control-Allow-Headers"); got != "X-Custom, Content-Type" {
			t.Errorf("Allow-Headers should reflect the requested headers, got %q", got)
		}
		if got := rr.Header().Get("Access-Control-Allow-Origin"); got != allowed {
			t.Errorf("Allow-Origin: got %q want %q", got, allowed)
		}
		vary := rr.Header().Values("Vary")
		for _, want := range []string{"Origin", "Access-Control-Request-Method", "Access-Control-Request-Headers"} {
			if !contains(vary, want) {
				t.Errorf("Vary should include %q, got %v", want, vary)
			}
		}
	})

	t.Run("preflight without announced headers uses the default set", func(t *testing.T) {
		rr := preflight(allowed, http.MethodPost, "")
		if got := rr.Header().Get("Access-Control-Allow-Headers"); got != defaultAllowHeaders {
			t.Errorf("Allow-Headers fallback: got %q want %q", got, defaultAllowHeaders)
		}
	})

	t.Run("preflight from a disallowed origin gets 204 with no CORS headers", func(t *testing.T) {
		rr := preflight("http://evil.example", http.MethodPost, "X-Custom")
		if rr.Code != http.StatusNoContent {
			t.Errorf("status: got %d want 204", rr.Code)
		}
		if got := rr.Header().Get("Access-Control-Allow-Origin"); got != "" {
			t.Errorf("disallowed origin must not be reflected, got %q", got)
		}
	})

	t.Run("bare OPTIONS (no preflight header) is NOT swallowed", func(t *testing.T) {
		rr := preflight(allowed, "", "")
		if !reached {
			t.Error("a bare OPTIONS without Access-Control-Request-Method must pass through to the handler")
		}
		if rr.Code != http.StatusTeapot {
			t.Errorf("bare OPTIONS should reach the handler (418), got %d", rr.Code)
		}
	})
}

func contains(haystack []string, needle string) bool {
	return slices.Contains(haystack, needle)
}
