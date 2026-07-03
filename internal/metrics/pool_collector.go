package metrics

import (
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/prometheus/client_golang/prometheus"
)

// PoolStats is a stable, testable snapshot of the values the PoolCollector
// emits. pgxpool.Stat is opaque (its fields are unexported) so the collector
// reads through this intermediate struct rather than the pgx type directly —
// production wires a real *pgxpool.Pool via NewPoolCollector, while tests
// can drive the collector with any PoolStatSource.
type PoolStats struct {
	AcquiredConns        int32
	IdleConns            int32
	TotalConns           int32
	MaxConns             int32
	AcquireCount         int64
	AcquireDuration      time.Duration
	EmptyAcquireCount    int64
	CanceledAcquireCount int64
}

// PoolStatSource produces a PoolStats snapshot. The PoolCollector calls
// PoolStats() exactly once per scrape — there is no background polling,
// no ticker, no goroutine. The stats are read at the moment Prometheus
// scrapes /metrics.
type PoolStatSource interface {
	PoolStats() PoolStats
}

// PoolCollector implements prometheus.Collector by reading PoolStats at
// scrape time. Register the instance with prometheus.MustRegister after
// the database pool has been created (see cmd/api/main.go).
type PoolCollector struct {
	src PoolStatSource

	acquiredConns        *prometheus.Desc
	idleConns            *prometheus.Desc
	totalConns           *prometheus.Desc
	maxConns             *prometheus.Desc
	acquireCount         *prometheus.Desc
	acquireDuration      *prometheus.Desc
	emptyAcquireCount    *prometheus.Desc
	canceledAcquireCount *prometheus.Desc
}

// NewPoolCollector wraps a live *pgxpool.Pool.
func NewPoolCollector(pool *pgxpool.Pool) *PoolCollector {
	return newPoolCollector(&pgxPoolAdapter{p: pool})
}

// newPoolCollector is the testable constructor — any PoolStatSource works.
func newPoolCollector(src PoolStatSource) *PoolCollector {
	return &PoolCollector{
		src: src,
		acquiredConns: prometheus.NewDesc(
			"db_pool_acquired_conns",
			"Number of connections currently acquired (in use).",
			nil, nil),
		idleConns: prometheus.NewDesc(
			"db_pool_idle_conns",
			"Number of currently idle connections in the pool.",
			nil, nil),
		totalConns: prometheus.NewDesc(
			"db_pool_total_conns",
			"Total number of connections currently in the pool (idle plus acquired).",
			nil, nil),
		maxConns: prometheus.NewDesc(
			"db_pool_max_conns",
			"Maximum pool size as configured at startup.",
			nil, nil),
		acquireCount: prometheus.NewDesc(
			"db_pool_acquire_count_total",
			"Cumulative number of successful acquisitions from the pool since startup.",
			nil, nil),
		acquireDuration: prometheus.NewDesc(
			"db_pool_acquire_duration_seconds_total",
			"Cumulative time spent acquiring connections, in seconds, since startup.",
			nil, nil),
		emptyAcquireCount: prometheus.NewDesc(
			"db_pool_empty_acquire_count_total",
			"Cumulative count of acquisitions that had to wait because the pool was empty.",
			nil, nil),
		canceledAcquireCount: prometheus.NewDesc(
			"db_pool_canceled_acquire_count_total",
			"Cumulative count of acquisitions cancelled before completion (typically by context cancellation).",
			nil, nil),
	}
}

// Describe implements prometheus.Collector.
func (c *PoolCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.acquiredConns
	ch <- c.idleConns
	ch <- c.totalConns
	ch <- c.maxConns
	ch <- c.acquireCount
	ch <- c.acquireDuration
	ch <- c.emptyAcquireCount
	ch <- c.canceledAcquireCount
}

// Collect implements prometheus.Collector. It is invoked on every scrape.
// Stats are read from the pool inside this call — there is no background
// state to keep in sync.
func (c *PoolCollector) Collect(ch chan<- prometheus.Metric) {
	s := c.src.PoolStats()
	ch <- prometheus.MustNewConstMetric(c.acquiredConns, prometheus.GaugeValue, float64(s.AcquiredConns))
	ch <- prometheus.MustNewConstMetric(c.idleConns, prometheus.GaugeValue, float64(s.IdleConns))
	ch <- prometheus.MustNewConstMetric(c.totalConns, prometheus.GaugeValue, float64(s.TotalConns))
	ch <- prometheus.MustNewConstMetric(c.maxConns, prometheus.GaugeValue, float64(s.MaxConns))
	ch <- prometheus.MustNewConstMetric(c.acquireCount, prometheus.CounterValue, float64(s.AcquireCount))
	ch <- prometheus.MustNewConstMetric(c.acquireDuration, prometheus.CounterValue, s.AcquireDuration.Seconds())
	ch <- prometheus.MustNewConstMetric(c.emptyAcquireCount, prometheus.CounterValue, float64(s.EmptyAcquireCount))
	ch <- prometheus.MustNewConstMetric(c.canceledAcquireCount, prometheus.CounterValue, float64(s.CanceledAcquireCount))
}

// pgxPoolAdapter adapts a *pgxpool.Pool to the PoolStatSource interface.
type pgxPoolAdapter struct {
	p *pgxpool.Pool
}

func (a *pgxPoolAdapter) PoolStats() PoolStats {
	s := a.p.Stat()
	return PoolStats{
		AcquiredConns:        s.AcquiredConns(),
		IdleConns:            s.IdleConns(),
		TotalConns:           s.TotalConns(),
		MaxConns:             s.MaxConns(),
		AcquireCount:         s.AcquireCount(),
		AcquireDuration:      s.AcquireDuration(),
		EmptyAcquireCount:    s.EmptyAcquireCount(),
		CanceledAcquireCount: s.CanceledAcquireCount(),
	}
}
