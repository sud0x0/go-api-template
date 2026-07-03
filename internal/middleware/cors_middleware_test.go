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
// is set on every response touched by the middleware — allowed OR rejected —
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

func contains(haystack []string, needle string) bool {
	return slices.Contains(haystack, needle)
}
