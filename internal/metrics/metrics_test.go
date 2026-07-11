package metrics

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"go.opentelemetry.io/otel/attribute"
)

// These asserts target the OTel instruments through a ManualReader. They keep
// the original test INTENTS: bounded attributes (route pattern, not raw path),
// the panic path (still 500, in-flight restored, re-panic reaches Recoverer),
// and the metric/name coverage - just re-expressed against OTel data points.

// TestUnmatchedRoutesUseSameLabel verifies that two different 404 paths produce
// the same "unmatched" http.route attribute so cardinality stays bounded for
// unknown URLs.
func TestUnmatchedRoutesUseSameLabel(t *testing.T) {
	m, reader := newForTest(t)

	r := chi.NewRouter()
	r.Use(m.Middleware)
	r.Get("/known", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	hit := func(path string) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		r.ServeHTTP(rr, req)
	}

	hit("/totally/unknown/one")
	hit("/another/random/path")

	rm := collect(t, reader)
	count := sumInt64(t, rm, metricRequestCount,
		attribute.String(attrHTTPMethod, http.MethodGet),
		attribute.String(attrHTTPRoute, "unmatched"),
		attribute.Int(attrHTTPStatus, http.StatusNotFound),
	)
	if count < 2 {
		t.Errorf("expected at least 2 unmatched requests under http.route=unmatched, got %v", count)
	}
}

// TestMatchedRouteUsesPattern verifies the http.route attribute is the chi
// route pattern, not the literal URL.
func TestMatchedRouteUsesPattern(t *testing.T) {
	m, reader := newForTest(t)

	r := chi.NewRouter()
	r.Use(m.Middleware)
	r.Get("/logs/{id}", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})

	hit := func(path string) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodGet, path, nil)
		r.ServeHTTP(rr, req)
	}

	hit("/logs/550e8400-e29b-41d4-a716-446655440000")
	hit("/logs/660e8400-e29b-41d4-a716-446655440001")

	rm := collect(t, reader)
	count := sumInt64(t, rm, metricRequestCount,
		attribute.String(attrHTTPMethod, http.MethodGet),
		attribute.String(attrHTTPRoute, "/logs/{id}"),
		attribute.Int(attrHTTPStatus, http.StatusOK),
	)
	if count < 2 {
		t.Errorf("expected >=2 requests on route /logs/{id}, got %v", count)
	}
}

// TestMiddleware_RecordsPanickingRequests verifies that a request that panics
// is still recorded as 500, the in-flight up-down counter is restored to 0, and
// chi.Recoverer still produces the 500 response (re-panic flows up).
func TestMiddleware_RecordsPanickingRequests(t *testing.T) {
	m, reader := newForTest(t)

	r := chi.NewRouter()
	// Recoverer is registered first (outer) so it catches the metrics
	// middleware's re-panic - mirroring main.go's chain order.
	r.Use(chimw.Recoverer)
	r.Use(m.Middleware)
	r.Get("/boom", func(_ http.ResponseWriter, _ *http.Request) {
		panic("kaboom")
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/boom", nil)
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusInternalServerError {
		t.Errorf("response status: got %d want 500", rr.Code)
	}

	rm := collect(t, reader)
	count := sumInt64(t, rm, metricRequestCount,
		attribute.String(attrHTTPMethod, http.MethodGet),
		attribute.String(attrHTTPRoute, "/boom"),
		attribute.Int(attrHTTPStatus, http.StatusInternalServerError),
	)
	if count != 1 {
		t.Errorf("expected exactly 1 request recorded at status=500, got %v", count)
	}

	// The duration and size histograms must each carry exactly one observation
	// for this request.
	if got := histogramCount(t, rm, metricRequestDuration,
		attribute.Int(attrHTTPStatus, http.StatusInternalServerError)); got != 1 {
		t.Errorf("request duration: expected 1 observation, got %d", got)
	}
	if got := histogramCount(t, rm, metricResponseSize,
		attribute.Int(attrHTTPStatus, http.StatusInternalServerError)); got != 1 {
		t.Errorf("response size: expected 1 observation, got %d", got)
	}

	// In-flight up-down counter must net back to 0.
	if v := sumInt64(t, rm, metricRequestsInFlight); v != 0 {
		t.Errorf("in-flight counter not restored, got %v", v)
	}
}

// TestMiddleware_RecordsWrittenStatusOnPanic verifies the documented behaviour:
// if the handler wrote a status before panicking, record that status, not 500.
func TestMiddleware_RecordsWrittenStatusOnPanic(t *testing.T) {
	m, reader := newForTest(t)

	r := chi.NewRouter()
	r.Use(chimw.Recoverer)
	r.Use(m.Middleware)
	r.Get("/half-broken", func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
		panic("after 503")
	})

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/half-broken", nil)
	r.ServeHTTP(rr, req)

	rm := collect(t, reader)
	count := sumInt64(t, rm, metricRequestCount,
		attribute.String(attrHTTPMethod, http.MethodGet),
		attribute.String(attrHTTPRoute, "/half-broken"),
		attribute.Int(attrHTTPStatus, http.StatusServiceUnavailable),
	)
	if count != 1 {
		t.Errorf("expected 1 request recorded at status=503, got %v", count)
	}
}

// TestMiddleware_NormalisesMethodAttribute verifies the http.request.method
// attribute is bounded to the known-method allow-list: a known method is
// recorded verbatim, an arbitrary token collapses to the "_OTHER" sentinel, and
// the in-flight up-down counter nets to zero either way (so the normalised value
// is used symmetrically on the +1 and -1).
func TestMiddleware_NormalisesMethodAttribute(t *testing.T) {
	cases := []struct {
		name   string
		method string
		want   string
	}{
		{name: "known method recorded verbatim", method: http.MethodGet, want: http.MethodGet},
		{name: "arbitrary token collapses to _OTHER", method: "WEIRDVERB123", want: methodOther},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, reader := newForTest(t)

			r := chi.NewRouter()
			r.Use(m.Middleware)
			// Handle matches ALL methods, so even a non-standard token reaches a 200.
			r.Handle("/x", http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(http.StatusOK)
			}))

			rr := httptest.NewRecorder()
			req := httptest.NewRequest(c.method, "/x", nil)
			r.ServeHTTP(rr, req)

			rm := collect(t, reader)
			if v := sumInt64(t, rm, metricRequestCount,
				attribute.String(attrHTTPMethod, c.want)); v != 1 {
				t.Errorf("http.request.method=%q: got count %d, want 1", c.want, v)
			}
			// The raw token must NEVER appear as an attribute value when it is not a
			// known method (the cardinality-DoS guard).
			if c.method != c.want {
				if v := sumInt64(t, rm, metricRequestCount,
					attribute.String(attrHTTPMethod, c.method)); v != 0 {
					t.Errorf("raw method %q leaked as an attribute value (count %d)", c.method, v)
				}
			}
			// In-flight up-down counter must net back to 0 for the normalised value.
			if v := sumInt64(t, rm, metricRequestsInFlight); v != 0 {
				t.Errorf("in-flight counter not restored, got %d", v)
			}
		})
	}
}

// TestIncAPIError verifies the handler-facing counter records under bounded
// feature/error_type attributes.
func TestIncAPIError(t *testing.T) {
	m, reader := newForTest(t)

	ctx := context.Background()
	m.IncAPIError(ctx, "log", "database")
	m.IncAPIError(ctx, "log", "database")
	m.IncAPIError(ctx, "log", "validation")

	rm := collect(t, reader)
	if v := sumInt64(t, rm, metricAPIErrors,
		attribute.String(attrFeature, "log"),
		attribute.String(attrErrorType, "database")); v != 2 {
		t.Errorf("api.errors{feature=log,error_type=database}: got %v want 2", v)
	}
	if v := sumInt64(t, rm, metricAPIErrors,
		attribute.String(attrFeature, "log"),
		attribute.String(attrErrorType, "validation")); v != 1 {
		t.Errorf("api.errors{feature=log,error_type=validation}: got %v want 1", v)
	}
}
