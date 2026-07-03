package metrics

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
	"github.com/prometheus/client_golang/prometheus/testutil"
	dto "github.com/prometheus/client_model/go"
)

func findCounter(t *testing.T, vec *prometheus.CounterVec, labels prometheus.Labels) float64 {
	t.Helper()
	c, err := vec.GetMetricWith(labels)
	if err != nil {
		t.Fatalf("GetMetricWith %v: %v", labels, err)
	}
	var m dto.Metric
	if err := c.Write(&m); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if m.Counter == nil {
		return 0
	}
	return m.Counter.GetValue()
}

// TestUnmatchedRoutesUseSameLabel (item 15) verifies that two different
// 404 paths produce the same "unmatched" path label so cardinality stays
// bounded for unknown URLs.
func TestUnmatchedRoutesUseSameLabel(t *testing.T) {
	m := newForTest()

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

	count := findCounter(t, m.requestsTotal, prometheus.Labels{
		"method": http.MethodGet,
		"path":   "unmatched",
		"status": "404",
	})
	if count < 2 {
		t.Errorf("expected at least 2 unmatched requests under path=unmatched, got %v", count)
	}
}

// TestMatchedRouteUsesPattern verifies the path label is the chi route
// pattern, not the literal URL.
func TestMatchedRouteUsesPattern(t *testing.T) {
	m := newForTest()

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

	count := findCounter(t, m.requestsTotal, prometheus.Labels{
		"method": http.MethodGet,
		"path":   "/logs/{id}",
		"status": "200",
	})
	if count < 2 {
		t.Errorf("expected >=2 requests on pattern /logs/{id}, got %v", count)
	}
}

// TestMetricsEndpointIsRecorded (item 4) verifies that the previous
// "avoid recursion" skip is gone — a scrape to /metrics is recorded like
// any other request.
func TestMetricsEndpointIsRecorded(t *testing.T) {
	m := newForTest()

	r := chi.NewRouter()
	r.Use(m.Middleware)
	r.Handle("/metrics", promhttp.HandlerFor(prometheus.NewRegistry(), promhttp.HandlerOpts{}))

	rr := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	r.ServeHTTP(rr, req)

	if rr.Code != http.StatusOK {
		t.Fatalf("status: got %d want 200", rr.Code)
	}

	count := findCounter(t, m.requestsTotal, prometheus.Labels{
		"method": http.MethodGet,
		"path":   "/metrics",
		"status": "200",
	})
	if count < 1 {
		t.Errorf("expected /metrics scrape to be recorded, got count=%v", count)
	}
}

// TestMiddleware_RecordsPanickingRequests (item 5) verifies that a request
// that panics is still recorded as 500, the in-flight gauge is restored,
// and chi.Recoverer still produces the 500 response (re-panic flows up).
func TestMiddleware_RecordsPanickingRequests(t *testing.T) {
	m := newForTest()

	r := chi.NewRouter()
	// Recoverer is registered first (outer) so it catches the metrics
	// middleware's re-panic — mirroring main.go's chain order.
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

	count := findCounter(t, m.requestsTotal, prometheus.Labels{
		"method": http.MethodGet,
		"path":   "/boom",
		"status": "500",
	})
	if count != 1 {
		t.Errorf("expected exactly 1 request recorded at status=500, got %v", count)
	}

	// Each vec must have exactly one observation/value for this request.
	if got := testutil.CollectAndCount(m.requestDuration); got != 1 {
		t.Errorf("requestDuration: expected 1 series, got %d", got)
	}
	if got := testutil.CollectAndCount(m.responseSize); got != 1 {
		t.Errorf("responseSize: expected 1 series, got %d", got)
	}

	// In-flight gauge must be back to 0.
	if v := testutil.ToFloat64(m.requestsInFlight); v != 0 {
		t.Errorf("in-flight gauge not restored, got %v", v)
	}
}

// TestMiddleware_RecordsWrittenStatusOnPanic verifies the documented
// behaviour: if the handler wrote a status before panicking, record that
// status — not 500.
func TestMiddleware_RecordsWrittenStatusOnPanic(t *testing.T) {
	m := newForTest()

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

	count := findCounter(t, m.requestsTotal, prometheus.Labels{
		"method": http.MethodGet,
		"path":   "/half-broken",
		"status": "503",
	})
	if count != 1 {
		t.Errorf("expected 1 request recorded at status=503, got %v", count)
	}
}

// newForTest builds a Metrics instance with a private registry so each test
// gets an isolated counter set.
func newForTest() *Metrics {
	reg := prometheus.NewRegistry()
	m := &Metrics{
		requestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{Name: "http_requests_total_test", Help: "x"},
			[]string{"method", "path", "status"},
		),
		requestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{Name: "http_request_duration_seconds_test", Help: "x"},
			[]string{"method", "path", "status"},
		),
		requestsInFlight: prometheus.NewGauge(
			prometheus.GaugeOpts{Name: "http_requests_in_flight_test", Help: "x"},
		),
		responseSize: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{Name: "http_response_size_bytes_test", Help: "x"},
			[]string{"method", "path", "status"},
		),
		queryDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{Name: "db_query_duration_seconds_test", Help: "x"},
			[]string{"operation"},
		),
		queryErrors: prometheus.NewCounterVec(
			prometheus.CounterOpts{Name: "db_query_errors_total_test", Help: "x"},
			[]string{"operation"},
		),
		apiErrors: prometheus.NewCounterVec(
			prometheus.CounterOpts{Name: "api_errors_total_test", Help: "x"},
			[]string{"feature", "error_type"},
		),
	}
	reg.MustRegister(
		m.requestsTotal, m.requestDuration, m.requestsInFlight, m.responseSize,
		m.queryDuration, m.queryErrors, m.apiErrors,
	)
	return m
}
