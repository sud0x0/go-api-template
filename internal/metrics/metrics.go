package metrics

import (
	"net/http"
	"strconv"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// Metrics holds all Prometheus metrics for the application.
type Metrics struct {
	// HTTP layer
	requestsTotal    *prometheus.CounterVec
	requestDuration  *prometheus.HistogramVec
	requestsInFlight prometheus.Gauge
	responseSize     *prometheus.HistogramVec

	// Database layer (populated by the QueryTracer wired into pgxpool)
	queryDuration *prometheus.HistogramVec
	queryErrors   *prometheus.CounterVec

	// API error breakdown by feature/error_type — populated by handlers
	// through the HandlerMetrics interface.
	apiErrors *prometheus.CounterVec
}

// New creates and registers all Prometheus metrics on the default registry.
func New() *Metrics {
	m := &Metrics{
		requestsTotal: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "http_requests_total",
				Help: "Total number of HTTP requests by method, path, and status code.",
			},
			[]string{"method", "path", "status"},
		),
		requestDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "http_request_duration_seconds",
				Help:    "HTTP request duration in seconds.",
				Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
			},
			[]string{"method", "path", "status"},
		),
		requestsInFlight: prometheus.NewGauge(
			prometheus.GaugeOpts{
				Name: "http_requests_in_flight",
				Help: "Current number of HTTP requests being processed.",
			},
		),
		responseSize: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "http_response_size_bytes",
				Help:    "HTTP response size in bytes.",
				Buckets: []float64{100, 1000, 10000, 100000, 1000000},
			},
			[]string{"method", "path", "status"},
		),

		queryDuration: prometheus.NewHistogramVec(
			prometheus.HistogramOpts{
				Name:    "db_query_duration_seconds",
				Help:    "Database query duration in seconds, labelled by SQL operation keyword.",
				Buckets: []float64{.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5},
			},
			[]string{"operation"},
		),
		queryErrors: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "db_query_errors_total",
				Help: "Database query errors, labelled by SQL operation keyword. pgx.ErrNoRows is excluded.",
			},
			[]string{"operation"},
		),

		apiErrors: prometheus.NewCounterVec(
			prometheus.CounterOpts{
				Name: "api_errors_total",
				Help: "API errors emitted by handlers, labelled by feature and a bounded error_type.",
			},
			[]string{"feature", "error_type"},
		),
	}

	prometheus.MustRegister(
		m.requestsTotal,
		m.requestDuration,
		m.requestsInFlight,
		m.responseSize,
		m.queryDuration,
		m.queryErrors,
		m.apiErrors,
	)

	return m
}

// QueryTracer returns a pgx.QueryTracer that records into this Metrics
// instance's queryDuration and queryErrors vectors. Wire the returned
// value into pgxpool.Config.ConnConfig.Tracer (db.New does this).
func (m *Metrics) QueryTracer() pgx.QueryTracer {
	return &QueryTracer{
		duration: m.queryDuration,
		errors:   m.queryErrors,
	}
}

// IncAPIError increments the api_errors_total counter. The label set is
// bounded: feature names are package-level constants (e.g. "log") and
// error_type values come from the package's ErrType* constants. Never
// pass strings derived from err.Error() here.
func (m *Metrics) IncAPIError(feature, errorType string) {
	m.apiErrors.WithLabelValues(feature, errorType).Inc()
}

// Handler returns the Prometheus HTTP handler for the /metrics endpoint.
func Handler() http.Handler {
	return promhttp.Handler()
}

// Middleware returns HTTP middleware that records Prometheus metrics per
// request. The path label uses chi's matched route pattern (or "unmatched"
// for 404s) so dynamic URL segments do not create unbounded label values.
//
// A single deferred block performs all observations so the metrics are
// recorded even when the handler panics. If the handler panics, the
// recovered value is re-thrown so chi's Recoverer (registered upstream
// in the chain) remains responsible for writing the 500 response and
// logging the stack — this middleware is purely an observer.
//
// Note on /metrics: requests to /metrics are recorded like any other
// request. There is no recursion: incrementing a counter while serving a
// scrape only changes what the NEXT scrape sees. The one visible side
// effect is that http_requests_total{path="/metrics"} grows and each
// scrape adds one observation to the duration and size histograms.
func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		m.requestsInFlight.Inc()
		wrapped := chimw.NewWrapResponseWriter(w, r.ProtoMajor)

		defer func() {
			m.requestsInFlight.Dec()

			rec := recover()
			statusCode := wrapped.Status()
			switch {
			case statusCode == 0 && rec != nil:
				// Handler panicked before writing a status — record 500.
				statusCode = http.StatusInternalServerError
			case statusCode == 0:
				// Handler returned without explicit WriteHeader → implicit 200.
				statusCode = http.StatusOK
			}
			// If a status was explicitly written before the panic, we
			// keep that status as the label — that's what the client
			// would see if the body completed.

			path := routeLabel(r)
			status := strconv.Itoa(statusCode)
			duration := time.Since(start).Seconds()
			m.requestsTotal.WithLabelValues(r.Method, path, status).Inc()
			m.requestDuration.WithLabelValues(r.Method, path, status).Observe(duration)
			m.responseSize.WithLabelValues(r.Method, path, status).Observe(float64(wrapped.BytesWritten()))

			if rec != nil {
				// Re-panic so chi.Recoverer upstream handles the 500.
				panic(rec)
			}
		}()

		next.ServeHTTP(wrapped, r)
	})
}

// routeLabel returns chi's matched route pattern for matched requests, and
// the literal "unmatched" for unmatched (404) requests. This bounds label
// cardinality so dynamic URL segments do not explode the metric series.
func routeLabel(r *http.Request) string {
	rctx := chi.RouteContext(r.Context())
	if rctx == nil {
		return "unmatched"
	}
	if p := rctx.RoutePattern(); p != "" {
		return p
	}
	return "unmatched"
}
