package metrics

import (
	"testing"
	"time"

	"github.com/prometheus/client_golang/prometheus"
	dto "github.com/prometheus/client_model/go"
)

// fakePoolSource is a mutable PoolStatSource for tests.
type fakePoolSource struct {
	stats PoolStats
}

func (f *fakePoolSource) PoolStats() PoolStats { return f.stats }

// TestPoolCollector_EmitsAllEightMetricFamilies verifies the collector's
// Describe and Collect produce all eight families with the correct values
// when scraped through a registry.
func TestPoolCollector_EmitsAllEightMetricFamilies(t *testing.T) {
	src := &fakePoolSource{
		stats: PoolStats{
			AcquiredConns:        7,
			IdleConns:            3,
			TotalConns:           10,
			MaxConns:             100,
			AcquireCount:         42,
			AcquireDuration:      1500 * time.Millisecond,
			EmptyAcquireCount:    5,
			CanceledAcquireCount: 2,
		},
	}
	c := newPoolCollector(src)

	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}

	// Index by metric family name.
	byName := map[string]*dto.MetricFamily{}
	for _, mf := range mfs {
		byName[mf.GetName()] = mf
	}

	// Each family must exist with exactly one Metric (no labels), with the
	// expected numeric value. Gauge vs Counter is distinguished by which
	// field on dto.Metric is populated.
	type want struct {
		kind  string // "gauge" or "counter"
		value float64
	}
	expected := map[string]want{
		"db_pool_acquired_conns":                 {"gauge", 7},
		"db_pool_idle_conns":                     {"gauge", 3},
		"db_pool_total_conns":                    {"gauge", 10},
		"db_pool_max_conns":                      {"gauge", 100},
		"db_pool_acquire_count_total":            {"counter", 42},
		"db_pool_acquire_duration_seconds_total": {"counter", 1.5},
		"db_pool_empty_acquire_count_total":      {"counter", 5},
		"db_pool_canceled_acquire_count_total":   {"counter", 2},
	}
	for name, w := range expected {
		mf, ok := byName[name]
		if !ok {
			t.Errorf("missing metric family %q", name)
			continue
		}
		if len(mf.Metric) != 1 {
			t.Errorf("%s: expected 1 metric, got %d", name, len(mf.Metric))
			continue
		}
		m := mf.Metric[0]
		var got float64
		switch w.kind {
		case "gauge":
			if m.Gauge == nil {
				t.Errorf("%s: expected gauge, got %T", name, m)
				continue
			}
			got = m.Gauge.GetValue()
		case "counter":
			if m.Counter == nil {
				t.Errorf("%s: expected counter, got %T", name, m)
				continue
			}
			got = m.Counter.GetValue()
		}
		if got != w.value {
			t.Errorf("%s: got %v, want %v", name, got, w.value)
		}
	}
}

// TestPoolCollector_ReadsAtEachScrape verifies the collect-on-scrape
// promise: mutating the source's stats between scrapes shows up
// immediately, no background poll needed.
func TestPoolCollector_ReadsAtEachScrape(t *testing.T) {
	src := &fakePoolSource{stats: PoolStats{AcquiredConns: 1, MaxConns: 100}}
	c := newPoolCollector(src)
	reg := prometheus.NewRegistry()
	reg.MustRegister(c)

	if v := acquiredConnsValue(t, reg); v != 1 {
		t.Errorf("first scrape: got %v, want 1", v)
	}

	src.stats.AcquiredConns = 5
	if v := acquiredConnsValue(t, reg); v != 5 {
		t.Errorf("second scrape: got %v, want 5", v)
	}
}

func acquiredConnsValue(t *testing.T, reg *prometheus.Registry) float64 {
	t.Helper()
	mfs, err := reg.Gather()
	if err != nil {
		t.Fatalf("Gather: %v", err)
	}
	for _, mf := range mfs {
		if mf.GetName() != "db_pool_acquired_conns" {
			continue
		}
		if len(mf.Metric) != 1 || mf.Metric[0].Gauge == nil {
			t.Fatalf("unexpected shape for acquired_conns: %+v", mf)
		}
		return mf.Metric[0].Gauge.GetValue()
	}
	t.Fatal("db_pool_acquired_conns not in gather")
	return 0
}
