package logger

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"os"
	"strings"
	"testing"
	"time"
)

// testLogger builds a SlogLogger that writes JSON to buf. Same shape as
// production NewLogger but with an injected writer so the test can
// inspect the emitted output directly.
func testLogger(buf *bytes.Buffer) Logger {
	h := slog.NewJSONHandler(buf, &slog.HandlerOptions{Level: slog.LevelDebug})
	return newSlogLogger("test-svc", h)
}

func decodeLines(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	out := make([]map[string]any, len(lines))
	for i, line := range lines {
		var entry map[string]any
		if err := json.Unmarshal([]byte(line), &entry); err != nil {
			t.Fatalf("line %d: invalid JSON: %v\n%s", i, err, line)
		}
		out[i] = entry
	}
	return out
}

// TestSlogLogger_BaseAttrsOnEveryLine verifies the service / version /
// commit attrs are emitted on every log line, regardless of the call
// site.
func TestSlogLogger_BaseAttrsOnEveryLine(t *testing.T) {
	buf := &bytes.Buffer{}
	l := testLogger(buf)
	l.LogInfo("a")
	l.LogError(errors.New("e1"), errors.New("e2"))
	l.LogDebug("d")

	for i, entry := range decodeLines(t, buf) {
		for _, key := range []string{"time", "level", "msg", "service", "version", "commit"} {
			if _, ok := entry[key]; !ok {
				t.Errorf("line %d (%v): missing %q in %v", i, entry["msg"], key, entry)
			}
		}
		if entry["service"] != "test-svc" {
			t.Errorf("line %d: service = %v, want test-svc", i, entry["service"])
		}
	}
}

// TestSlogLogger_WithRequestContext_BindsAllAttrsOnEveryLine is the
// contract test for the refactor: every line emitted by a logger
// derived from WithRequestContext must carry the request_id, method,
// path, ip, and user_id, not just the "request completed" line.
func TestSlogLogger_WithRequestContext_BindsAllAttrsOnEveryLine(t *testing.T) {
	buf := &bytes.Buffer{}
	scoped := testLogger(buf).WithRequestContext(RequestContext{
		RequestID: "req-abc",
		Method:    "GET",
		Path:      "/api/v1/logs/123",
		RemoteIP:  "203.0.113.5:1234",
		UserID:    "u-42",
	})

	scoped.LogInfo("creating log entry")
	scoped.LogError(errors.New("database error"), errors.New("getLog abc: pq: connection refused"))
	scoped.LogDebug("debug detail")
	scoped.LogWarn("slow request detected") // appended last so the error-line index below is stable

	entries := decodeLines(t, buf)
	if len(entries) != 4 {
		t.Fatalf("expected 4 log lines, got %d", len(entries))
	}

	want := map[string]string{
		"request_id": "req-abc",
		"method":     "GET",
		"path":       "/api/v1/logs/123",
		"ip":         "203.0.113.5:1234",
		"user_id":    "u-42",
	}
	for i, entry := range entries {
		for k, v := range want {
			got, ok := entry[k]
			if !ok {
				t.Errorf("line %d (%v): missing %q", i, entry["msg"], k)
				continue
			}
			if got != v {
				t.Errorf("line %d: %s = %v, want %v", i, k, got, v)
			}
		}
	}

	// The LogError line additionally carries error and actual_error.
	errLine := entries[1]
	if errLine["error"] != "database error" {
		t.Errorf("error = %v, want 'database error'", errLine["error"])
	}
	if errLine["actual_error"] != "getLog abc: pq: connection refused" {
		t.Errorf("actual_error = %v", errLine["actual_error"])
	}
}

// TestSlogLogger_WithRequestContext_EmptyFieldsOmitted verifies that
// blank fields don't pollute the JSON shape with empty strings.
// A pre-auth caller (no UserID) and a non-HTTP caller (no Method) both
// produce clean output.
func TestSlogLogger_WithRequestContext_EmptyFieldsOmitted(t *testing.T) {
	buf := &bytes.Buffer{}
	scoped := testLogger(buf).WithRequestContext(RequestContext{
		RequestID: "req-1",
		Method:    "POST",
		// Path, RemoteIP, UserID intentionally empty.
	})
	scoped.LogInfo("test")

	entry := decodeLines(t, buf)[0]
	for _, mustBe := range []string{"request_id", "method"} {
		if _, ok := entry[mustBe]; !ok {
			t.Errorf("expected %q to be present", mustBe)
		}
	}
	for _, mustNotBe := range []string{"path", "ip", "user_id", "trace_id", "span_id"} {
		if _, ok := entry[mustNotBe]; ok {
			t.Errorf("%q should be omitted when empty, got %v", mustNotBe, entry[mustNotBe])
		}
	}
}

// TestSlogLogger_WithRequestContext_TraceCorrelation verifies the trace
// correlation contract: when TraceID/SpanID are bound they appear on every
// log line, and when they are empty neither attribute is emitted (a zero ID
// must never surface).
func TestSlogLogger_WithRequestContext_TraceCorrelation(t *testing.T) {
	t.Run("present when set", func(t *testing.T) {
		buf := &bytes.Buffer{}
		scoped := testLogger(buf).WithRequestContext(RequestContext{
			RequestID: "req-1",
			TraceID:   "4bf92f3577b34da6a3ce929d0e0e4736",
			SpanID:    "00f067aa0ba902b7",
		})
		scoped.LogInfo("work")

		entry := decodeLines(t, buf)[0]
		if entry["trace_id"] != "4bf92f3577b34da6a3ce929d0e0e4736" {
			t.Errorf("trace_id = %v, want the bound value", entry["trace_id"])
		}
		if entry["span_id"] != "00f067aa0ba902b7" {
			t.Errorf("span_id = %v, want the bound value", entry["span_id"])
		}
	})

	t.Run("absent when empty", func(t *testing.T) {
		buf := &bytes.Buffer{}
		scoped := testLogger(buf).WithRequestContext(RequestContext{RequestID: "req-1"})
		scoped.LogInfo("work")

		entry := decodeLines(t, buf)[0]
		for _, k := range []string{"trace_id", "span_id"} {
			if _, ok := entry[k]; ok {
				t.Errorf("%q should be omitted when empty, got %v", k, entry[k])
			}
		}
	})
}

// TestSlogLogger_WithRequestContext_EmptyReturnsSelf verifies the
// no-op path: WithRequestContext({}) returns the receiver unchanged so
// no allocation happens for non-HTTP-derived loggers.
func TestSlogLogger_WithRequestContext_EmptyReturnsSelf(t *testing.T) {
	buf := &bytes.Buffer{}
	base := testLogger(buf)
	got := base.WithRequestContext(RequestContext{})
	if got != base {
		t.Errorf("empty RequestContext should return the receiver unchanged; got new instance")
	}
}

// TestSlogLogger_LogWarn_LevelGating verifies LogWarn is emitted at the levels
// an operator expects and suppressed where it should be, exercising the real
// slogHandlerForLevel mapping: "quiet" (warnings + errors) and "production"
// (info + warnings + errors) emit it; "silent" (errors only) suppresses it.
// This closes the gap the doc comment promised but nothing could satisfy:
// before LogWarn, "quiet" had no emittable warning level so it showed only
// errors. Emitted lines must carry the WARN level label.
func TestSlogLogger_LogWarn_LevelGating(t *testing.T) {
	cases := []struct {
		level    string
		wantEmit bool
	}{
		{"quiet", true},
		{"production", true},
		{"silent", false},
	}
	for _, tc := range cases {
		t.Run(tc.level, func(t *testing.T) {
			f, err := os.CreateTemp(t.TempDir(), "log-*.json")
			if err != nil {
				t.Fatalf("CreateTemp: %v", err)
			}
			defer func() { _ = f.Close() }()

			// Drive the production level-mapping path (slogHandlerForLevel),
			// not a hand-picked slog.Level, so the test pins the mapping too.
			l := newSlogLogger("test-svc", slogHandlerForLevel(tc.level, f))
			l.LogWarn("be careful")

			data, err := os.ReadFile(f.Name())
			if err != nil {
				t.Fatalf("ReadFile: %v", err)
			}
			emitted := strings.Contains(string(data), "be careful")
			if emitted != tc.wantEmit {
				t.Errorf("level %q: LogWarn emitted=%v, want %v (output: %q)", tc.level, emitted, tc.wantEmit, data)
			}
			if tc.wantEmit && !strings.Contains(string(data), `"level":"WARN"`) {
				t.Errorf("level %q: expected a WARN-level line, got: %q", tc.level, data)
			}
		})
	}
}

// TestSlogLogger_TimeAttrIsRFC3339Millis verifies the time field is rendered in
// millisecond-precision RFC3339 (timeFormat). It wires the EXACT production
// ReplaceAttr (replaceTimeAttr) over a buffer so the assertion tracks production.
func TestSlogLogger_TimeAttrIsRFC3339Millis(t *testing.T) {
	buf := &bytes.Buffer{}
	h := slog.NewJSONHandler(buf, &slog.HandlerOptions{
		Level:       slog.LevelDebug,
		ReplaceAttr: replaceTimeAttr,
	})
	l := newSlogLogger("test", h)
	l.LogInfo("hi")

	entry := decodeLines(t, buf)[0]
	timeStr, ok := entry["time"].(string)
	if !ok {
		t.Fatalf("time was not a string: %v", entry["time"])
	}
	// It must parse under the millisecond RFC3339 layout AND round-trip to the
	// same string — proving the exact precision (a second-only value would not
	// re-render with the ".000" fraction).
	parsed, err := time.Parse(timeFormat, timeStr)
	if err != nil {
		t.Fatalf("time %q is not %s-formatted: %v", timeStr, timeFormat, err)
	}
	if got := parsed.Format(timeFormat); got != timeStr {
		t.Errorf("time %q does not round-trip through the millisecond layout (got %q)", timeStr, got)
	}
	if !strings.Contains(timeStr, "T") || !strings.Contains(timeStr, ".") {
		t.Errorf("time %q is not millisecond-precision RFC3339", timeStr)
	}
}
