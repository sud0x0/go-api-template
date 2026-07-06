package main

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/go-chi/httprate"

	"github.com/sud0x0/go-api-template/internal/middleware"
	"github.com/sud0x0/go-api-template/internal/shared"
)

// newTestPublicRouter assembles a public router with the same envelope
// fallbacks main() wires (registerEnvelopeFallbacks) plus a couple of the cheap
// middlewares from the real stack, and one sample route that only accepts GET,
// so a wrong-method request to it yields a 405.
func newTestPublicRouter() chi.Router {
	r := chi.NewRouter()
	r.Use(chimw.RequestID)
	r.Use(middleware.SecurityHeaders)
	registerEnvelopeFallbacks(r)
	r.Get("/api/v1/logs", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	return r
}

// TestPublicRouter_NotFoundEnvelope verifies an unmatched path returns the JSON
// error envelope (not chi's plain-text 404).
func TestPublicRouter_NotFoundEnvelope(t *testing.T) {
	r := newTestPublicRouter()

	req := httptest.NewRequest(http.MethodGet, "/nope/does-not-exist", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusNotFound {
		t.Errorf("status: got %d want 404. Body: %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: got %q want application/json", ct)
	}
	var env shared.ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("body is not a JSON envelope: %v\nbody: %s", err, rr.Body.String())
	}
	if env.ErrorType != shared.ErrTypeNotFound {
		t.Errorf("error field: got %q want %q", env.ErrorType, shared.ErrTypeNotFound)
	}
	if env.Message == "" {
		t.Error("message field should be non-empty")
	}
}

// TestPublicRouter_MethodNotAllowedEnvelope verifies a wrong method on a known
// path returns the JSON error envelope 405 (not chi's bare 405).
func TestPublicRouter_MethodNotAllowedEnvelope(t *testing.T) {
	r := newTestPublicRouter()

	req := httptest.NewRequest(http.MethodDelete, "/api/v1/logs", nil)
	rr := httptest.NewRecorder()
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusMethodNotAllowed {
		t.Errorf("status: got %d want 405. Body: %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("Content-Type: got %q want application/json", ct)
	}
	var env shared.ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("body is not a JSON envelope: %v\nbody: %s", err, rr.Body.String())
	}
	if env.ErrorType != shared.ErrTypeMethodNotAllowed {
		t.Errorf("error field: got %q want %q", env.ErrorType, shared.ErrTypeMethodNotAllowed)
	}
}

// TestPublicRouter_RateLimiterExemptsHealth verifies the Task 4 structure: the
// app-level limiter applies to business routes (a chi Group) but NOT to the
// health endpoints registered outside that group, so liveness/readiness probes
// are never 429'd into a restart loop.
func TestPublicRouter_RateLimiterExemptsHealth(t *testing.T) {
	limiter := httprate.Limit(1, time.Minute, httprate.WithKeyByIP(),
		httprate.WithLimitHandler(func(w http.ResponseWriter, _ *http.Request) {
			shared.WriteJSONError(w, http.StatusTooManyRequests, shared.ErrTypeRateLimited, "rate limit exceeded; retry later")
		}))

	r := chi.NewRouter()
	registerEnvelopeFallbacks(r)
	// Health endpoint on the root router, OUTSIDE the limited group.
	r.Get("/livez", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	// Business routes inside the limited group, mirroring main()'s wiring.
	r.Group(func(r chi.Router) {
		r.Use(limiter)
		r.Get("/api/v1/logs", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusOK) })
	})

	do := func(method, path string) *httptest.ResponseRecorder {
		req := httptest.NewRequest(method, path, nil)
		req.RemoteAddr = "203.0.113.7:5555" // stable client IP across calls
		rr := httptest.NewRecorder()
		r.ServeHTTP(rr, req)
		return rr
	}

	// Repeated /livez probes from the same IP all succeed (exempt).
	for i := range 3 {
		if rr := do(http.MethodGet, "/livez"); rr.Code != http.StatusOK {
			t.Errorf("/livez probe %d: got %d want 200 (must be exempt from the limiter)", i, rr.Code)
		}
	}

	// First /api/v1 request allowed (RPM=1), second 429 in the envelope.
	if rr := do(http.MethodGet, "/api/v1/logs"); rr.Code != http.StatusOK {
		t.Fatalf("first /api/v1/logs: got %d want 200. Body: %s", rr.Code, rr.Body.String())
	}
	rr := do(http.MethodGet, "/api/v1/logs")
	if rr.Code != http.StatusTooManyRequests {
		t.Fatalf("second /api/v1/logs: got %d want 429. Body: %s", rr.Code, rr.Body.String())
	}
	if ct := rr.Header().Get("Content-Type"); ct != "application/json" {
		t.Errorf("429 Content-Type: got %q want application/json", ct)
	}
	var env shared.ErrorResponse
	if err := json.Unmarshal(rr.Body.Bytes(), &env); err != nil {
		t.Fatalf("429 body is not a JSON envelope: %v\nbody: %s", err, rr.Body.String())
	}
	if env.ErrorType != shared.ErrTypeRateLimited {
		t.Errorf("429 error field: got %q want %q", env.ErrorType, shared.ErrTypeRateLimited)
	}
}
