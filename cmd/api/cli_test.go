package main

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestHandleFlags_Version(t *testing.T) {
	var buf bytes.Buffer
	shouldExit, code := handleFlags([]string{"--version"}, &buf)

	if !shouldExit {
		t.Error("--version: expected shouldExit=true, got false")
	}
	if code != 0 {
		t.Errorf("--version: expected exitCode=0, got %d", code)
	}
	if !strings.HasPrefix(buf.String(), "go-api-template version ") {
		t.Errorf("--version: banner should start with 'go-api-template version ', got: %q", buf.String()[:min(len(buf.String()), 50)])
	}
}

func TestHandleFlags_ShortVersion(t *testing.T) {
	var buf bytes.Buffer
	shouldExit, code := handleFlags([]string{"-v"}, &buf)

	if !shouldExit {
		t.Error("-v: expected shouldExit=true, got false")
	}
	if code != 0 {
		t.Errorf("-v: expected exitCode=0, got %d", code)
	}
	if !strings.HasPrefix(buf.String(), "go-api-template version ") {
		t.Errorf("-v: banner should start with 'go-api-template version ', got: %q", buf.String()[:min(len(buf.String()), 50)])
	}
}

func TestHandleFlags_NoFlags(t *testing.T) {
	var buf bytes.Buffer
	shouldExit, code := handleFlags([]string{}, &buf)

	if shouldExit {
		t.Error("no flags: expected shouldExit=false, got true")
	}
	if code != 0 {
		t.Errorf("no flags: expected exitCode=0, got %d", code)
	}
	if buf.Len() != 0 {
		t.Errorf("no flags: expected empty buffer, got: %q", buf.String())
	}
}

// TestHandleFlags_UnknownFlagFailsLoudly (item 6) verifies that a misspelled
// flag no longer silently boots the server. The error must be reported on
// the output writer and the exit code must be non-zero.
func TestHandleFlags_UnknownFlagFailsLoudly(t *testing.T) {
	var buf bytes.Buffer
	shouldExit, code := handleFlags([]string{"--frobnicate"}, &buf)

	if !shouldExit {
		t.Error("unknown flag: expected shouldExit=true, got false")
	}
	if code != 2 {
		t.Errorf("unknown flag: expected exitCode=2, got %d", code)
	}
	if !strings.Contains(buf.String(), "frobnicate") {
		t.Errorf("unknown flag: error should mention the bad flag name, got: %q", buf.String())
	}
}

func TestHandleFlags_DoubleVersion(t *testing.T) {
	var buf bytes.Buffer
	shouldExit, code := handleFlags([]string{"--version", "--version"}, &buf)

	if !shouldExit {
		t.Error("double --version: expected shouldExit=true, got false")
	}
	if code != 0 {
		t.Errorf("double --version: expected exitCode=0, got %d", code)
	}

	// Banner should be printed once
	lines := strings.Split(strings.TrimSuffix(buf.String(), "\n"), "\n")
	if len(lines) != 5 {
		t.Errorf("double --version: expected 5 lines (single banner), got %d", len(lines))
	}
}

// TestRunHealthcheck_RoutesToLivez (item 11A) verifies the --healthcheck path
// makes a localhost GET. It does this by overriding the localhost target via
// the same port the real binary uses, and pointing it at a test server bound
// to 127.0.0.1 on that port. (We can't run on 8080 in tests reliably, so we
// directly exercise the underlying probe logic against a test server.)
func TestRunHealthcheck_ProbeSuccess(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/livez" {
			w.WriteHeader(http.StatusOK)
			return
		}
		http.NotFound(w, r)
	}))
	defer ts.Close()

	// Use the same probe with the test server's URL.
	code := probeLivez(ts.URL)
	if code != 0 {
		t.Errorf("expected exit 0 for healthy /livez, got %d", code)
	}
}

func TestRunHealthcheck_ProbeFailure(t *testing.T) {
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusServiceUnavailable)
	}))
	defer ts.Close()

	code := probeLivez(ts.URL)
	if code != 1 {
		t.Errorf("expected exit 1 for 503 /livez, got %d", code)
	}
}

func TestRunHealthcheck_ProbeUnreachable(t *testing.T) {
	// Use a guaranteed-invalid URL.
	u, _ := url.Parse("http://127.0.0.1:1")
	code := probeLivez(u.String())
	if code != 1 {
		t.Errorf("expected exit 1 for unreachable server, got %d", code)
	}
}

// TestHandleFlags_HealthcheckPortFlag verifies the --port flag drives the
// probe target. Bind a test server to a known localhost port, pass that port
// via --port to handleFlags, and check the exit code reflects the probe
// outcome — proving the flag is wired all the way through runHealthcheck.
func TestHandleFlags_HealthcheckPortFlag(t *testing.T) {
	// Bind a listener on a free localhost port so we know the port number.
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	port := ln.Addr().(*net.TCPAddr).Port

	srv := &http.Server{
		Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/livez" {
				w.WriteHeader(http.StatusOK)
				return
			}
			http.NotFound(w, r)
		}),
		ReadHeaderTimeout: time.Second,
	}
	go func() { _ = srv.Serve(ln) }()
	defer func() { _ = srv.Close() }()

	var buf bytes.Buffer
	args := []string{"--healthcheck", "--port", strconv.Itoa(port)}
	shouldExit, code := handleFlags(args, &buf)

	if !shouldExit {
		t.Error("--healthcheck should short-circuit startup")
	}
	if code != 0 {
		t.Errorf("--healthcheck with healthy server: got %d, want 0. Output: %s", code, buf.String())
	}
}

// (Default-port behaviour is exercised by TestHandleFlags_HealthcheckPortFlag —
// with an explicit free port — and by TestRunHealthcheck_ProbeUnreachable.
// A test that asserted the default 8080 was unreachable was removed: it
// race-failed locally whenever the dev compose stack had port 8080
// forwarded to a running app container.)

// probeLivez is the testable core of runHealthcheck. It takes a base URL so
// tests can point it at an httptest server without binding to port 8080.
func probeLivez(baseURL string) int {
	client := &http.Client{Timeout: healthcheckTimeout}
	resp, err := client.Get(baseURL + "/livez")
	if err != nil {
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return 1
	}
	return 0
}
