package metrics

import (
	"context"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"go.opentelemetry.io/otel/metric"
)

// Pool-stat instrument names. These are this template's own db.pool.* signals -
// semconv's db.client.connection.* model collapses used/idle into one metric
// with a state attribute and has no equivalent for the acquire counters, so
// keeping the eight flat names preserves the exact signals the old Prometheus
// collector emitted.
const (
	metricPoolAcquiredConns   = "db.pool.acquired_conns"
	metricPoolIdleConns       = "db.pool.idle_conns"
	metricPoolTotalConns      = "db.pool.total_conns"
	metricPoolMaxConns        = "db.pool.max_conns"
	metricPoolAcquireCount    = "db.pool.acquire_count"
	metricPoolAcquireDuration = "db.pool.acquire_duration_seconds"
	metricPoolEmptyAcquire    = "db.pool.empty_acquire_count"
	metricPoolCanceledAcquire = "db.pool.canceled_acquire_count"
)

// PoolStats is a stable, testable snapshot of the values the pool instruments
// emit. pgxpool.Stat is opaque (its fields are unexported) so the collector
// reads through this intermediate struct rather than the pgx type directly -
// production wires a real *pgxpool.Pool via RegisterPoolCollector, while tests
// can drive registerPoolStats with any PoolStatSource.
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

// PoolStatSource produces a PoolStats snapshot. The async callback calls
// PoolStats() exactly once per collection - there is no background polling, no
// ticker, no goroutine. The stats are read at the moment the OTel SDK's periodic
// reader collects, preserving the original read-at-scrape design.
type PoolStatSource interface {
	PoolStats() PoolStats
}

// RegisterPoolCollector wires a live *pgxpool.Pool's stats onto async OTel
// instruments. Call it once after the pool has been created (see
// cmd/api/main.go). Returns an error only if instrument registration fails.
func (m *Metrics) RegisterPoolCollector(pool *pgxpool.Pool) error {
	return m.registerPoolStats(&pgxPoolAdapter{p: pool})
}

// registerPoolStats is the testable core: it registers one callback that reads
// src.PoolStats() a single time per collection and observes every instrument
// from that one snapshot, so all eight values are internally consistent.
func (m *Metrics) registerPoolStats(src PoolStatSource) error {
	acquired, err := m.meter.Int64ObservableGauge(metricPoolAcquiredConns,
		metric.WithDescription("Number of connections currently acquired (in use)."))
	if err != nil {
		return err
	}
	idle, err := m.meter.Int64ObservableGauge(metricPoolIdleConns,
		metric.WithDescription("Number of currently idle connections in the pool."))
	if err != nil {
		return err
	}
	total, err := m.meter.Int64ObservableGauge(metricPoolTotalConns,
		metric.WithDescription("Total number of connections currently in the pool (idle plus acquired)."))
	if err != nil {
		return err
	}
	maxConns, err := m.meter.Int64ObservableGauge(metricPoolMaxConns,
		metric.WithDescription("Maximum pool size as configured at startup."))
	if err != nil {
		return err
	}
	acquireCount, err := m.meter.Int64ObservableCounter(metricPoolAcquireCount,
		metric.WithDescription("Cumulative number of successful acquisitions from the pool since startup."))
	if err != nil {
		return err
	}
	acquireDuration, err := m.meter.Float64ObservableCounter(metricPoolAcquireDuration,
		metric.WithUnit("s"),
		metric.WithDescription("Cumulative time spent acquiring connections, in seconds, since startup."))
	if err != nil {
		return err
	}
	emptyAcquire, err := m.meter.Int64ObservableCounter(metricPoolEmptyAcquire,
		metric.WithDescription("Cumulative count of acquisitions that had to wait because the pool was empty."))
	if err != nil {
		return err
	}
	canceledAcquire, err := m.meter.Int64ObservableCounter(metricPoolCanceledAcquire,
		metric.WithDescription("Cumulative count of acquisitions cancelled before completion (typically by context cancellation)."))
	if err != nil {
		return err
	}

	_, err = m.meter.RegisterCallback(
		func(_ context.Context, o metric.Observer) error {
			s := src.PoolStats()
			o.ObserveInt64(acquired, int64(s.AcquiredConns))
			o.ObserveInt64(idle, int64(s.IdleConns))
			o.ObserveInt64(total, int64(s.TotalConns))
			o.ObserveInt64(maxConns, int64(s.MaxConns))
			o.ObserveInt64(acquireCount, s.AcquireCount)
			o.ObserveFloat64(acquireDuration, s.AcquireDuration.Seconds())
			o.ObserveInt64(emptyAcquire, s.EmptyAcquireCount)
			o.ObserveInt64(canceledAcquire, s.CanceledAcquireCount)
			return nil
		},
		acquired, idle, total, maxConns,
		acquireCount, acquireDuration, emptyAcquire, canceledAcquire,
	)
	return err
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
