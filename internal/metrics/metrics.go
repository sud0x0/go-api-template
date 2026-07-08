// Package metrics defines the process's OpenTelemetry (OTel) instrumentation:
// HTTP, connection-pool, query-tracer, and the bounded api.errors metrics.
// Instruments are created from the global OTel MeterProvider (installed by
// internal/observability from main) and exported over OTLP to a Collector - the
// app no longer serves a /metrics scrape endpoint. Every attribute value comes
// from a constant or a chi route pattern, never from user input, so cardinality
// stays bounded (see .claude/rules/security.md rule 7).
package metrics

import (
	"context"
	"fmt"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	chimw "github.com/go-chi/chi/v5/middleware"
	"github.com/jackc/pgx/v5"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
)

// instrumentationName is the OTel meter scope for every instrument in this
// package. It identifies which library produced the telemetry.
const instrumentationName = "github.com/sud0x0/go-api-template/internal/metrics"

// Instrument names. Where OpenTelemetry semantic conventions (semconv, spec
// 1.41.0 - the highest package shipped in otel core v1.44.0, at or below the
// targeted 1.43.0) define a standard name and unit for an equivalent signal we
// PREFER it: http.server.request.duration, http.server.active_requests,
// http.server.response.body.size (httpconv) and db.client.operation.duration
// (dbconv). The request COUNT, the DB error counter, api.errors and the
// db.pool.* stats have no semconv equivalent, so they keep descriptive
// dotted-namespace names of our own. The Collector's Prometheus exporter
// lower-snakes these (e.g. api.errors -> api_errors_total) for scraping.
const (
	metricRequestCount     = "http.server.request.count"
	metricRequestDuration  = "http.server.request.duration"
	metricRequestsInFlight = "http.server.active_requests"
	metricResponseSize     = "http.server.response.body.size"

	metricQueryDuration = "db.client.operation.duration"
	metricQueryErrors   = "db.client.operation.errors"

	metricAPIErrors = "api.errors"
)

// Attribute keys. HTTP and DB keys follow semconv (httpconv/dbconv). Feature
// and error_type are this template's own bounded application labels. Every
// value assigned to these keys is a constant, a chi route pattern, or an HTTP
// status code - never a user-supplied string.
const (
	attrHTTPMethod  = "http.request.method"
	attrHTTPRoute   = "http.route"
	attrHTTPStatus  = "http.response.status_code"
	attrDBOperation = "db.operation.name"
	attrFeature     = "feature"
	attrErrorType   = "error_type"
)

// Metrics holds all OTel instruments for the application.
type Metrics struct {
	// meter is retained so async pool instruments can be registered after the
	// database pool exists (see RegisterPoolCollector).
	meter metric.Meter

	// HTTP layer
	requestsTotal    metric.Int64Counter
	requestDuration  metric.Float64Histogram
	requestsInFlight metric.Int64UpDownCounter
	responseSize     metric.Int64Histogram

	// Database layer (populated by the QueryTracer wired into pgxpool)
	queryDuration metric.Float64Histogram
	queryErrors   metric.Int64Counter

	// API error breakdown by feature/error_type - populated by handlers
	// through the HandlerMetrics interface.
	apiErrors metric.Int64Counter
}

// New creates all instruments from the global OTel MeterProvider. Call it after
// internal/observability has installed the providers (main does this). When
// telemetry is disabled the global provider is a no-op, so every instrument is
// a no-op and this still succeeds - the app boots with or without a Collector.
//
// Unlike the previous Prometheus constructor this is NOT call-once: OTel dedupes
// instruments by name within a meter, so a second call would not panic. We still
// construct it exactly once from main (see .claude/rules/decisions.md #8).
func New() *Metrics {
	return newWithMeter(otel.Meter(instrumentationName))
}

// newWithMeter is the testable constructor: tests pass a meter backed by a
// manual reader so they can collect and assert on the emitted data points.
func newWithMeter(meter metric.Meter) *Metrics {
	return &Metrics{
		meter: meter,
		requestsTotal: must(meter.Int64Counter(
			metricRequestCount,
			metric.WithUnit("{request}"),
			metric.WithDescription("Total number of HTTP requests by method, route, and status code."),
		)),
		requestDuration: must(meter.Float64Histogram(
			metricRequestDuration,
			metric.WithUnit("s"),
			metric.WithDescription("HTTP request duration in seconds."),
			metric.WithExplicitBucketBoundaries(.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10),
		)),
		requestsInFlight: must(meter.Int64UpDownCounter(
			metricRequestsInFlight,
			metric.WithUnit("{request}"),
			metric.WithDescription("Current number of HTTP requests being processed."),
		)),
		responseSize: must(meter.Int64Histogram(
			metricResponseSize,
			metric.WithUnit("By"),
			metric.WithDescription("HTTP response body size in bytes."),
			metric.WithExplicitBucketBoundaries(100, 1000, 10000, 100000, 1000000),
		)),

		queryDuration: must(meter.Float64Histogram(
			metricQueryDuration,
			metric.WithUnit("s"),
			metric.WithDescription("Database query duration in seconds, labelled by SQL operation keyword."),
			metric.WithExplicitBucketBoundaries(.001, .005, .01, .025, .05, .1, .25, .5, 1, 2.5),
		)),
		queryErrors: must(meter.Int64Counter(
			metricQueryErrors,
			metric.WithUnit("{error}"),
			metric.WithDescription("Database query errors, labelled by SQL operation keyword. pgx.ErrNoRows is excluded."),
		)),

		apiErrors: must(meter.Int64Counter(
			metricAPIErrors,
			metric.WithUnit("{error}"),
			metric.WithDescription("API errors emitted by handlers, labelled by feature and a bounded error_type."),
		)),
	}
}

// must panics if instrument creation fails. Instrument creation only fails on a
// malformed static name - a programming error caught by tests, not a runtime
// condition - so panicking keeps New's signature clean at the call site.
func must[T any](v T, err error) T {
	if err != nil {
		panic(fmt.Sprintf("metrics: instrument creation failed: %v", err))
	}
	return v
}

// QueryTracer returns a pgx.QueryTracer that records into this Metrics
// instance's query duration and error instruments. Wire the returned value into
// pgxpool.Config.ConnConfig.Tracer (db.New does this).
func (m *Metrics) QueryTracer() pgx.QueryTracer {
	return &QueryTracer{
		duration: m.queryDuration,
		errors:   m.queryErrors,
	}
}

// IncAPIError increments the api.errors counter. The attribute set is bounded:
// feature names are package-level constants (e.g. "log") and error_type values
// come from the package's ErrType* constants. Never pass strings derived from
// err.Error() here.
func (m *Metrics) IncAPIError(feature, errorType string) {
	m.apiErrors.Add(context.Background(), 1, metric.WithAttributes(
		attribute.String(attrFeature, feature),
		attribute.String(attrErrorType, errorType),
	))
}

// Middleware returns HTTP middleware that records OTel metrics per request. The
// http.route attribute uses chi's matched route pattern (or "unmatched" for
// 404s) so dynamic URL segments do not create unbounded attribute values.
//
// A single deferred block performs all observations so the metrics are recorded
// even when the handler panics. If the handler panics, the recovered value is
// re-thrown so chi's Recoverer (registered upstream in the chain) remains
// responsible for writing the 500 response and logging the stack - this
// middleware is purely an observer.
//
// The request context is passed to every Add/Record call so the SDK can attach
// exemplars linking a data point back to the active trace span.
func (m *Metrics) Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ctx := r.Context()
		start := time.Now()
		// active_requests is keyed by method (semconv). Normalise once against the
		// bounded allow-list so an arbitrary method token cannot explode the series;
		// the matching -1 in the deferred block reuses this SAME value, so the
		// up-down counter still nets out.
		method := normalizeMethod(r.Method)
		methodAttr := metric.WithAttributes(attribute.String(attrHTTPMethod, method))
		m.requestsInFlight.Add(ctx, 1, methodAttr)
		wrapped := chimw.NewWrapResponseWriter(w, r.ProtoMajor)

		defer func() {
			m.requestsInFlight.Add(ctx, -1, methodAttr)

			rec := recover()
			statusCode := wrapped.Status()
			switch {
			case statusCode == 0 && rec != nil:
				// Handler panicked before writing a status - record 500.
				statusCode = http.StatusInternalServerError
			case statusCode == 0:
				// Handler returned without explicit WriteHeader → implicit 200.
				statusCode = http.StatusOK
			}
			// If a status was explicitly written before the panic, we keep that
			// status as the attribute - that's what the client would see if the
			// body completed.

			attrs := metric.WithAttributes(
				attribute.String(attrHTTPMethod, method),
				attribute.String(attrHTTPRoute, routeLabel(r)),
				attribute.Int(attrHTTPStatus, statusCode),
			)
			m.requestsTotal.Add(ctx, 1, attrs)
			m.requestDuration.Record(ctx, time.Since(start).Seconds(), attrs)
			m.responseSize.Record(ctx, int64(wrapped.BytesWritten()), attrs)

			if rec != nil {
				// Re-panic so chi.Recoverer upstream handles the 500.
				panic(rec)
			}
		}()

		next.ServeHTTP(wrapped, r)
	})
}

// methodOther is the OTel semconv sentinel recorded for any HTTP method token
// outside the known set. net/http accepts arbitrary method tokens on the request
// line, so recording r.Method verbatim would let a client mint unbounded
// http.request.method time-series (a cardinality DoS) - the exact thing the
// package doc promises does not happen. See semconv http.request.method.
const methodOther = "_OTHER"

// knownMethods is the fixed allow-list of HTTP methods recorded verbatim; any
// other token normalises to methodOther. It is the RFC 9110 method set plus
// PATCH (RFC 5789), which is also OTel semconv's http.request.method known set.
var knownMethods = map[string]struct{}{
	http.MethodGet:     {},
	http.MethodHead:    {},
	http.MethodPost:    {},
	http.MethodPut:     {},
	http.MethodPatch:   {},
	http.MethodDelete:  {},
	http.MethodConnect: {},
	http.MethodOptions: {},
	http.MethodTrace:   {},
}

// normalizeMethod bounds the http.request.method attribute: a known method is
// returned as-is, anything else becomes methodOther. It is called ONCE per
// request so the in-flight +1/-1 pair and the terminal attrs record the
// identical value and the up-down counter still nets to zero.
func normalizeMethod(method string) string {
	if _, ok := knownMethods[method]; ok {
		return method
	}
	return methodOther
}

// routeLabel returns chi's matched route pattern for matched requests, and the
// literal "unmatched" for unmatched (404) requests. This bounds attribute
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
