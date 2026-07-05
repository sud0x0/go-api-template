package metrics

import (
	"context"
	"testing"

	"go.opentelemetry.io/otel/attribute"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/metric/metricdata"
)

// newForTest builds a Metrics backed by an in-memory ManualReader so each test
// collects and asserts on the emitted OTel data points in isolation - the
// equivalent of the old private-Prometheus-registry helper.
func newForTest(t *testing.T) (*Metrics, *sdkmetric.ManualReader) {
	t.Helper()
	reader := sdkmetric.NewManualReader()
	provider := sdkmetric.NewMeterProvider(sdkmetric.WithReader(reader))
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	return newWithMeter(provider.Meter("test")), reader
}

// collect drains the reader into a ResourceMetrics snapshot.
func collect(t *testing.T, reader *sdkmetric.ManualReader) metricdata.ResourceMetrics {
	t.Helper()
	var rm metricdata.ResourceMetrics
	if err := reader.Collect(context.Background(), &rm); err != nil {
		t.Fatalf("collect: %v", err)
	}
	return rm
}

func findMetric(t *testing.T, rm metricdata.ResourceMetrics, name string) metricdata.Metrics {
	t.Helper()
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return m
			}
		}
	}
	t.Fatalf("metric %q not found in collected data", name)
	return metricdata.Metrics{}
}

func metricExists(rm metricdata.ResourceMetrics, name string) bool {
	for _, sm := range rm.ScopeMetrics {
		for _, m := range sm.Metrics {
			if m.Name == name {
				return true
			}
		}
	}
	return false
}

// attrsContain reports whether set carries every key/value in want. Used to
// assert a data point is labelled with the expected bounded attributes.
func attrsContain(set attribute.Set, want ...attribute.KeyValue) bool {
	for _, kv := range want {
		v, ok := set.Value(kv.Key)
		if !ok || v != kv.Value {
			return false
		}
	}
	return true
}

// sumInt64 sums the value of the int64 Sum data points matching want (covers
// both monotonic counters and up-down counters).
func sumInt64(t *testing.T, rm metricdata.ResourceMetrics, name string, want ...attribute.KeyValue) int64 {
	t.Helper()
	m := findMetric(t, rm, name)
	s, ok := m.Data.(metricdata.Sum[int64])
	if !ok {
		t.Fatalf("metric %q is %T, want Sum[int64]", name, m.Data)
	}
	var total int64
	for _, dp := range s.DataPoints {
		if attrsContain(dp.Attributes, want...) {
			total += dp.Value
		}
	}
	return total
}

// sumFloat64 sums the value of the float64 Sum data points matching want.
func sumFloat64(t *testing.T, rm metricdata.ResourceMetrics, name string, want ...attribute.KeyValue) float64 {
	t.Helper()
	m := findMetric(t, rm, name)
	s, ok := m.Data.(metricdata.Sum[float64])
	if !ok {
		t.Fatalf("metric %q is %T, want Sum[float64]", name, m.Data)
	}
	var total float64
	for _, dp := range s.DataPoints {
		if attrsContain(dp.Attributes, want...) {
			total += dp.Value
		}
	}
	return total
}

// gaugeInt64 sums the value of the int64 Gauge data points matching want.
func gaugeInt64(t *testing.T, rm metricdata.ResourceMetrics, name string, want ...attribute.KeyValue) int64 {
	t.Helper()
	m := findMetric(t, rm, name)
	g, ok := m.Data.(metricdata.Gauge[int64])
	if !ok {
		t.Fatalf("metric %q is %T, want Gauge[int64]", name, m.Data)
	}
	var total int64
	for _, dp := range g.DataPoints {
		if attrsContain(dp.Attributes, want...) {
			total += dp.Value
		}
	}
	return total
}

// histogramCount returns the total observation count across the histogram data
// points matching want. Handles both float64 (durations) and int64 (byte
// sizes) histograms.
func histogramCount(t *testing.T, rm metricdata.ResourceMetrics, name string, want ...attribute.KeyValue) uint64 {
	t.Helper()
	m := findMetric(t, rm, name)
	var count uint64
	switch h := m.Data.(type) {
	case metricdata.Histogram[float64]:
		for _, dp := range h.DataPoints {
			if attrsContain(dp.Attributes, want...) {
				count += dp.Count
			}
		}
	case metricdata.Histogram[int64]:
		for _, dp := range h.DataPoints {
			if attrsContain(dp.Attributes, want...) {
				count += dp.Count
			}
		}
	default:
		t.Fatalf("metric %q is %T, want Histogram", name, m.Data)
	}
	return count
}
