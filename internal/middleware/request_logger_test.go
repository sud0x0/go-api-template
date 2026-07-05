package middleware

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	chimw "github.com/go-chi/chi/v5/middleware"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"

	"github.com/sud0x0/go-api-template/internal/shared/logger"
)

// captureLogger records every call so tests can assert on what
// middleware did. Concurrency-safe so it survives -race.
type captureLogger struct {
	mu          sync.Mutex
	infoCalls   []infoCall
	boundCtx    logger.RequestContext
	boundCalled bool
}

type infoCall struct {
	message string
	args    []any
}

func (c *captureLogger) LogError(_, _ error)        {}
func (c *captureLogger) LogWarn(_ string, _ ...any) {}
func (c *captureLogger) LogInfo(message string, args ...any) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.infoCalls = append(c.infoCalls, infoCall{message: message, args: args})
}
func (c *captureLogger) LogDebug(_ string, _ ...any) {}
func (c *captureLogger) WithRequestContext(rc logger.RequestContext) logger.Logger {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.boundCtx = rc
	c.boundCalled = true
	return c
}

// TestRequestLogger_BindsRequestContextAndLogsCompletion verifies that
// the middleware:
//  1. Reads the chi request ID and builds a full RequestContext (id +
//     method + path + ip) when wrapping the logger.
//  2. Emits a "request completed" line at the end of the request.
//  3. Stores the request-scoped logger on the context so handlers can
//     fetch it via logger.FromContext.
func TestRequestLogger_BindsRequestContextAndLogsCompletion(t *testing.T) {
	cap := &captureLogger{}

	var loggerFromCtx logger.Logger
	next := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		loggerFromCtx = logger.FromContext(r.Context(), cap)
		w.WriteHeader(http.StatusOK)
	})
	mw := RequestLogger(cap)(next)

	// Chi's RequestID middleware writes into the request context; emulate
	// it by chaining chimw.RequestID outside RequestLogger.
	req := httptest.NewRequest(http.MethodGet, "/test", nil)
	req.RemoteAddr = "203.0.113.5:1234"
	rr := httptest.NewRecorder()

	chimw.RequestID(mw).ServeHTTP(rr, req)

	cap.mu.Lock()
	defer cap.mu.Unlock()

	if !cap.boundCalled {
		t.Fatal("WithRequestContext was not called — middleware did not wrap the logger")
	}
	if cap.boundCtx.RequestID == "" {
		t.Error("RequestID was not bound")
	}
	if cap.boundCtx.Method != http.MethodGet {
		t.Errorf("Method: got %q, want %q", cap.boundCtx.Method, http.MethodGet)
	}
	if cap.boundCtx.Path != "/test" {
		t.Errorf("Path: got %q, want /test", cap.boundCtx.Path)
	}
	if cap.boundCtx.RemoteIP != "203.0.113.5:1234" {
		t.Errorf("RemoteIP: got %q, want 203.0.113.5:1234", cap.boundCtx.RemoteIP)
	}

	if len(cap.infoCalls) == 0 {
		t.Fatal("no LogInfo calls recorded — expected at least 'request completed'")
	}
	found := false
	for _, ic := range cap.infoCalls {
		if ic.message == "request completed" {
			found = true
		}
	}
	if !found {
		t.Errorf("missing 'request completed' log line; got: %+v", cap.infoCalls)
	}

	if loggerFromCtx == nil {
		t.Error("handler could not retrieve request-scoped logger via logger.FromContext")
	}
}

// TestRequestLogger_RequestCompletedDoesNotRepeatBoundAttrs verifies the
// optimisation: the "request completed" line should NOT pass method,
// path, or ip as args (they are already bound on the logger). Only
// status and duration_ms — values only known at the end.
func TestRequestLogger_RequestCompletedDoesNotRepeatBoundAttrs(t *testing.T) {
	cap := &captureLogger{}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mw := RequestLogger(cap)(next)

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	chimw.RequestID(mw).ServeHTTP(httptest.NewRecorder(), req)

	cap.mu.Lock()
	defer cap.mu.Unlock()

	var completed *infoCall
	for i := range cap.infoCalls {
		if cap.infoCalls[i].message == "request completed" {
			completed = &cap.infoCalls[i]
		}
	}
	if completed == nil {
		t.Fatal("no 'request completed' line")
	}

	// The args should be exactly: "status", N, "duration_ms", M
	// (4 elements; nothing about method/path/ip).
	wantKeys := map[string]bool{"status": true, "duration_ms": true}
	for i := 0; i < len(completed.args); i += 2 {
		key, ok := completed.args[i].(string)
		if !ok {
			t.Errorf("non-string key at arg %d: %v", i, completed.args[i])
			continue
		}
		if !wantKeys[key] {
			t.Errorf("unexpected key %q in 'request completed' line — should be bound on the logger, not repeated as an arg", key)
		}
	}
}

// TestRequestLogger_BindsTraceAndSpanIDsFromActiveSpan verifies that when the
// request context carries a valid OTel span, the middleware binds trace_id and
// span_id matching that span onto the request-scoped logger.
func TestRequestLogger_BindsTraceAndSpanIDsFromActiveSpan(t *testing.T) {
	cap := &captureLogger{}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mw := RequestLogger(cap)(next)

	// A real SDK tracer produces a sampled, valid span context (mirrors what
	// otelhttp puts on the context in production).
	tp := sdktrace.NewTracerProvider()
	t.Cleanup(func() { _ = tp.Shutdown(context.Background()) })
	ctx, span := tp.Tracer("test").Start(context.Background(), "op")
	defer span.End()
	sc := span.SpanContext()

	req := httptest.NewRequest(http.MethodGet, "/x", nil).WithContext(ctx)
	mw.ServeHTTP(httptest.NewRecorder(), req)

	cap.mu.Lock()
	defer cap.mu.Unlock()
	if got := cap.boundCtx.TraceID; got != sc.TraceID().String() {
		t.Errorf("TraceID: got %q, want %q", got, sc.TraceID().String())
	}
	if got := cap.boundCtx.SpanID; got != sc.SpanID().String() {
		t.Errorf("SpanID: got %q, want %q", got, sc.SpanID().String())
	}
}

// TestRequestLogger_NoSpanLeavesTraceIDsEmpty verifies that with no span on the
// context (telemetry disabled, or a non-instrumented path) neither trace_id nor
// span_id is bound, so a zero ID never appears in the output.
func TestRequestLogger_NoSpanLeavesTraceIDsEmpty(t *testing.T) {
	cap := &captureLogger{}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusOK)
	})
	mw := RequestLogger(cap)(next)

	req := httptest.NewRequest(http.MethodGet, "/x", nil)
	mw.ServeHTTP(httptest.NewRecorder(), req)

	cap.mu.Lock()
	defer cap.mu.Unlock()
	if cap.boundCtx.TraceID != "" {
		t.Errorf("TraceID should be empty with no span, got %q", cap.boundCtx.TraceID)
	}
	if cap.boundCtx.SpanID != "" {
		t.Errorf("SpanID should be empty with no span, got %q", cap.boundCtx.SpanID)
	}
}

// TestRequestLogger_DoesNotMaskHandlerErrors verifies the middleware does
// not swallow downstream behaviour: status and body pass through.
func TestRequestLogger_DoesNotMaskHandlerErrors(t *testing.T) {
	cap := &captureLogger{}
	next := http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusTeapot)
		_, _ = w.Write([]byte("teapot"))
	})
	mw := RequestLogger(cap)(next)

	rr := httptest.NewRecorder()
	mw.ServeHTTP(rr, httptest.NewRequest(http.MethodGet, "/x", nil))

	if rr.Code != http.StatusTeapot {
		t.Errorf("status: got %d, want 418", rr.Code)
	}
	if rr.Body.String() != "teapot" {
		t.Errorf("body: got %q, want %q", rr.Body.String(), "teapot")
	}
}
