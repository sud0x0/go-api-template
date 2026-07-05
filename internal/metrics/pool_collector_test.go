package metrics

import (
	"testing"
	"time"
)

// fakePoolSource is a mutable PoolStatSource for tests.
type fakePoolSource struct {
	stats PoolStats
}

func (f *fakePoolSource) PoolStats() PoolStats { return f.stats }

// TestPoolCollector_EmitsAllEightMetricFamilies verifies registerPoolStats
// produces all eight db.pool.* instruments with the correct values when
// collected through a ManualReader.
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
	m, reader := newForTest(t)
	if err := m.registerPoolStats(src); err != nil {
		t.Fatalf("registerPoolStats: %v", err)
	}

	rm := collect(t, reader)

	// Gauges.
	if v := gaugeInt64(t, rm, metricPoolAcquiredConns); v != 7 {
		t.Errorf("%s: got %v, want 7", metricPoolAcquiredConns, v)
	}
	if v := gaugeInt64(t, rm, metricPoolIdleConns); v != 3 {
		t.Errorf("%s: got %v, want 3", metricPoolIdleConns, v)
	}
	if v := gaugeInt64(t, rm, metricPoolTotalConns); v != 10 {
		t.Errorf("%s: got %v, want 10", metricPoolTotalConns, v)
	}
	if v := gaugeInt64(t, rm, metricPoolMaxConns); v != 100 {
		t.Errorf("%s: got %v, want 100", metricPoolMaxConns, v)
	}

	// Counters.
	if v := sumInt64(t, rm, metricPoolAcquireCount); v != 42 {
		t.Errorf("%s: got %v, want 42", metricPoolAcquireCount, v)
	}
	if v := sumFloat64(t, rm, metricPoolAcquireDuration); v != 1.5 {
		t.Errorf("%s: got %v, want 1.5", metricPoolAcquireDuration, v)
	}
	if v := sumInt64(t, rm, metricPoolEmptyAcquire); v != 5 {
		t.Errorf("%s: got %v, want 5", metricPoolEmptyAcquire, v)
	}
	if v := sumInt64(t, rm, metricPoolCanceledAcquire); v != 2 {
		t.Errorf("%s: got %v, want 2", metricPoolCanceledAcquire, v)
	}
}

// TestPoolCollector_ReadsAtEachScrape verifies the collect-on-scrape promise:
// mutating the source's stats between collections shows up immediately, no
// background poll needed - the callback reads src.PoolStats() each time.
func TestPoolCollector_ReadsAtEachScrape(t *testing.T) {
	src := &fakePoolSource{stats: PoolStats{AcquiredConns: 1, MaxConns: 100}}
	m, reader := newForTest(t)
	if err := m.registerPoolStats(src); err != nil {
		t.Fatalf("registerPoolStats: %v", err)
	}

	if v := gaugeInt64(t, collect(t, reader), metricPoolAcquiredConns); v != 1 {
		t.Errorf("first collection: got %v, want 1", v)
	}

	src.stats.AcquiredConns = 5
	if v := gaugeInt64(t, collect(t, reader), metricPoolAcquiredConns); v != 5 {
		t.Errorf("second collection: got %v, want 5", v)
	}
}
