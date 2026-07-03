package metrics

import (
	"context"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
)

// queryTracerCtxKey carries the per-query start time and operation label
// from TraceQueryStart to TraceQueryEnd. Per-query state lives on the
// context, not the tracer struct, so the tracer is concurrency-safe across
// any number of in-flight queries.
type queryTracerCtxKey struct{}

type queryCtxValue struct {
	start time.Time
	op    string
}

// QueryTracer implements pgx.QueryTracer to record query duration and
// errors. Wire it into the pool via pgxpool.Config.ConnConfig.Tracer.
// Concurrency-safe: no mutable fields besides the pre-allocated metrics
// vectors.
type QueryTracer struct {
	duration *prometheus.HistogramVec
	errors   *prometheus.CounterVec
}

// TraceQueryStart records the start time and the operation label on the
// returned context. The returned context is given to TraceQueryEnd.
func (t *QueryTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	return context.WithValue(ctx, queryTracerCtxKey{}, queryCtxValue{
		start: time.Now(),
		op:    extractOperation(data.SQL),
	})
}

// TraceQueryEnd observes the duration and increments the error counter
// for non-no-rows errors. pgx.ErrNoRows is excluded because it is a
// normal outcome of QueryRow when no row matches, not a database error.
func (t *QueryTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	v, ok := ctx.Value(queryTracerCtxKey{}).(queryCtxValue)
	if !ok {
		// TraceQueryStart did not run with this tracer on this context
		// (e.g. ctx was replaced upstream); nothing to record.
		return
	}
	t.duration.WithLabelValues(v.op).Observe(time.Since(v.start).Seconds())
	if data.Err != nil && !errors.Is(data.Err, pgx.ErrNoRows) {
		t.errors.WithLabelValues(v.op).Inc()
	}
}

// knownOperations is the BOUNDED label set for the operation label. Any
// SQL keyword outside this set is mapped to "OTHER" so cardinality stays
// fixed regardless of what queries the application runs. Never derive
// operation labels from raw SQL text, table names, or arguments.
var knownOperations = map[string]struct{}{
	"SELECT":   {},
	"INSERT":   {},
	"UPDATE":   {},
	"DELETE":   {},
	"BEGIN":    {},
	"COMMIT":   {},
	"ROLLBACK": {},
	// WITH is the leading keyword of a CTE query (`WITH x AS (...) SELECT ...`);
	// including it keeps CTEs out of the OTHER bucket. Still bounded — one fixed
	// label, not derived from the CTE body.
	"WITH": {},
}

// extractOperation finds the first SQL keyword of a query, after skipping
// leading whitespace and any combination of single-line (--) and block
// (/* */) comments, uppercases it, and returns it if it matches the
// bounded set — otherwise "OTHER".
func extractOperation(sql string) string {
	stripped := stripLeadingNoise(sql)
	word := strings.ToUpper(firstWord(stripped))
	if _, ok := knownOperations[word]; ok {
		return word
	}
	return "OTHER"
}

func stripLeadingNoise(sql string) string {
	for {
		sql = strings.TrimLeft(sql, " \t\n\r")
		switch {
		case strings.HasPrefix(sql, "--"):
			if i := strings.IndexAny(sql, "\n\r"); i >= 0 {
				sql = sql[i+1:]
			} else {
				return ""
			}
		case strings.HasPrefix(sql, "/*"):
			if end := strings.Index(sql, "*/"); end >= 0 {
				sql = sql[end+2:]
			} else {
				// Unterminated block comment — give up.
				return ""
			}
		default:
			return sql
		}
	}
}

func firstWord(s string) string {
	for i, r := range s {
		switch r {
		case ' ', '\t', '\n', '\r', '(', ';', ',':
			return s[:i]
		}
	}
	return s
}
