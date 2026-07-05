// Package observability wires the process's OpenTelemetry (OTel) providers.
// It builds the Resource (service identity), a TracerProvider and a
// MeterProvider that export over OTLP/HTTP to a Collector, and installs them as
// the OTel globals so internal/metrics and any instrumentation read them
// through otel.Meter / otel.Tracer.
//
// This is infrastructure, not a feature: it imports internal/config and
// internal/version but never a feature package. Logs are deliberately NOT
// handled here - they stay on stdlib slog JSON to stdout (the OTel Go logs SDK
// is beta, see .claude/rules/decisions.md). Correlation of a log line to its
// trace is done in the logger, which reads the active span from context.
package observability

import (
	"context"
	"errors"
	"fmt"

	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/exporters/otlp/otlpmetric/otlpmetrichttp"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	sdkmetric "go.opentelemetry.io/otel/sdk/metric"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.41.0"

	"github.com/sud0x0/go-api-template/internal/config"
	"github.com/sud0x0/go-api-template/internal/version"
)

// ShutdownFunc flushes and shuts down the providers Init installed. Call it
// during graceful shutdown, after the HTTP servers stop, so in-flight spans and
// the last metric interval are flushed before the process exits.
type ShutdownFunc func(context.Context) error

// Init installs the global OTel TracerProvider and MeterProvider from cfg and
// returns a single shutdown function for both.
//
// When cfg.OTELEnabled is false it installs nothing: the OTel globals remain the
// SDK's built-in no-ops, so every span and instrument is a no-op and the app
// boots and serves with no Collector present. The returned enabled flag lets
// main log one startup line stating which mode it is in.
func Init(ctx context.Context, cfg config.ObservabilityConfig) (shutdown ShutdownFunc, enabled bool, err error) {
	if !cfg.OTELEnabled {
		// No-op providers are already the global default, nothing to install.
		return func(context.Context) error { return nil }, false, nil
	}

	res, err := newResource(ctx, cfg.OTELServiceName)
	if err != nil {
		return nil, false, fmt.Errorf("build otel resource: %w", err)
	}

	// OTLP/HTTP (port 4318). A bare endpoint URL like http://otel-collector:4318
	// has the signal path (/v1/traces, /v1/metrics) appended by the exporter and
	// the http scheme selects an insecure connection.
	traceExp, err := otlptracehttp.New(ctx, otlptracehttp.WithEndpointURL(cfg.OTELExporterEndpoint))
	if err != nil {
		return nil, false, fmt.Errorf("otel trace exporter: %w", err)
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithResource(res),
		// Batch span processor: spans are queued and flushed in the background
		// rather than exported one-per-span on the request path.
		sdktrace.WithBatcher(traceExp),
	)

	metricExp, err := otlpmetrichttp.New(ctx, otlpmetrichttp.WithEndpointURL(cfg.OTELExporterEndpoint))
	if err != nil {
		// Roll back the trace provider we already built so we do not leak its
		// background batcher goroutine on a partial init failure.
		_ = tp.Shutdown(ctx)
		return nil, false, fmt.Errorf("otel metric exporter: %w", err)
	}
	mp := sdkmetric.NewMeterProvider(
		sdkmetric.WithResource(res),
		// Periodic reader: the SDK pushes metrics on an interval (default 60s).
		// Async instruments (the db.pool.* callbacks) are read at each push, so
		// there is still no background polling goroutine of our own.
		sdkmetric.WithReader(sdkmetric.NewPeriodicReader(metricExp)),
	)

	otel.SetTracerProvider(tp)
	otel.SetMeterProvider(mp)
	// W3C trace-context propagation so an inbound traceparent header continues an
	// upstream trace, and baggage for context propagation across services.
	otel.SetTextMapPropagator(propagation.NewCompositeTextMapPropagator(
		propagation.TraceContext{},
		propagation.Baggage{},
	))

	shutdown = func(ctx context.Context) error {
		// Join so a failure in one still attempts the other.
		return errors.Join(tp.Shutdown(ctx), mp.Shutdown(ctx))
	}
	return shutdown, true, nil
}

// newResource builds the OTel Resource describing this service: the semconv
// service.name and service.version, plus the build commit. version.Version and
// version.GitCommit come from internal/version (populated by GoReleaser at
// build time), so telemetry can be traced to the exact build that emitted it -
// the same identity the logger binds onto every log line.
func newResource(ctx context.Context, serviceName string) (*resource.Resource, error) {
	return resource.New(ctx,
		resource.WithAttributes(
			semconv.ServiceName(serviceName),
			semconv.ServiceVersion(version.Version),
			// No semconv key for VCS commit at this spec version, so use a
			// stable custom key mirroring the logger's "commit" attribute.
			attribute.String("service.commit", version.GitCommit),
		),
	)
}
