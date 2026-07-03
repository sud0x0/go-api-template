package metrics

import (
	"context"
	"errors"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"
)

// TestExtractOperation covers the bounded-cardinality rules: known SQL
// keywords map to themselves (uppercased), everything else maps to OTHER.
func TestExtractOperation(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"SELECT", "SELECT * FROM x", "SELECT"},
		{"lowercase", "select * from x", "SELECT"},
		{"leading whitespace", "   \t\nSELECT 1", "SELECT"},
		{"trailing semicolon", "SELECT;", "SELECT"},
		{"paren after keyword", "SELECT(", "SELECT"},
		{"single-line comment", "-- hi\nSELECT 1", "SELECT"},
		{"block comment", "/* hi */SELECT 1", "SELECT"},
		{"comment then whitespace", "/* hi */\n SELECT", "SELECT"},
		{"mixed comments", "/* outer */ -- inner\nUPDATE x SET a=1", "UPDATE"},
		{"INSERT", "INSERT INTO x VALUES (1)", "INSERT"},
		{"UPDATE", "UPDATE x SET y = 1", "UPDATE"},
		{"DELETE", "DELETE FROM x", "DELETE"},
		{"BEGIN", "BEGIN", "BEGIN"},
		{"COMMIT", "COMMIT", "COMMIT"},
		{"ROLLBACK", "ROLLBACK", "ROLLBACK"},
		{"unknown EXPLAIN", "EXPLAIN ANALYZE SELECT 1", "OTHER"},
		{"CTE WITH", "WITH x AS (...) SELECT", "WITH"},
		{"CTE WITH lowercase", "with recent as (select 1) select * from recent", "WITH"},
		{"CREATE", "CREATE TABLE x ()", "OTHER"},
		{"empty", "", "OTHER"},
		{"whitespace only", "   \n\t   ", "OTHER"},
		{"only single-line comment", "-- just a comment", "OTHER"},
		{"unterminated block comment", "/* hello", "OTHER"},
		{"non-SQL gibberish", "????", "OTHER"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := extractOperation(c.in)
			if got != c.want {
				t.Errorf("extractOperation(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}
}

func newTracerForTest() (*QueryTracer, *prometheus.HistogramVec, *prometheus.CounterVec) {
	d := prometheus.NewHistogramVec(
		prometheus.HistogramOpts{Name: "query_duration_seconds_test", Help: "x"},
		[]string{"operation"},
	)
	e := prometheus.NewCounterVec(
		prometheus.CounterOpts{Name: "query_errors_total_test", Help: "x"},
		[]string{"operation"},
	)
	return &QueryTracer{duration: d, errors: e}, d, e
}

// TestQueryTracer_NoRowsNotCountedAsError verifies the explicit carve-out:
// pgx.ErrNoRows is a normal QueryRow outcome, not a database error.
func TestQueryTracer_NoRowsNotCountedAsError(t *testing.T) {
	tracer, _, errCV := newTracerForTest()

	ctx := tracer.TraceQueryStart(context.Background(), nil,
		pgx.TraceQueryStartData{SQL: "SELECT * FROM x WHERE id = $1"})
	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: pgx.ErrNoRows})

	if v := testutil.ToFloat64(errCV.WithLabelValues("SELECT")); v != 0 {
		t.Errorf("ErrNoRows should not increment error counter, got %v", v)
	}
}

// TestQueryTracer_RealErrorCounted verifies that a non-no-rows error
// increments the error counter labelled with the operation keyword.
func TestQueryTracer_RealErrorCounted(t *testing.T) {
	tracer, _, errCV := newTracerForTest()

	ctx := tracer.TraceQueryStart(context.Background(), nil,
		pgx.TraceQueryStartData{SQL: "INSERT INTO x VALUES ($1)"})
	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{Err: errors.New("connection reset")})

	if v := testutil.ToFloat64(errCV.WithLabelValues("INSERT")); v != 1 {
		t.Errorf("expected 1 INSERT error, got %v", v)
	}
}

// TestQueryTracer_DurationRecorded verifies the duration histogram is
// observed even on a successful query.
func TestQueryTracer_DurationRecorded(t *testing.T) {
	tracer, durCV, _ := newTracerForTest()

	ctx := tracer.TraceQueryStart(context.Background(), nil,
		pgx.TraceQueryStartData{SQL: "SELECT 1"})
	tracer.TraceQueryEnd(ctx, nil, pgx.TraceQueryEndData{})

	if got := testutil.CollectAndCount(durCV); got != 1 {
		t.Errorf("expected 1 observation in duration histogram, got %d", got)
	}
}

// TestQueryTracer_NoCtxValueIsSafe verifies that calling TraceQueryEnd
// without a corresponding TraceQueryStart does not panic (defensive
// behaviour for unusual contexts).
func TestQueryTracer_NoCtxValueIsSafe(t *testing.T) {
	tracer, _, _ := newTracerForTest()
	// Should be a no-op, not a panic.
	tracer.TraceQueryEnd(context.Background(), nil, pgx.TraceQueryEndData{Err: errors.New("x")})
}
