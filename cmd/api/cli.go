package main

import (
	"flag"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/sud0x0/go-api-template/internal/config"
	"github.com/sud0x0/go-api-template/internal/version"
)

// healthcheckTimeout bounds the localhost probe. Anything longer than a few
// seconds means the app is wedged; the orchestrator should kill it.
const healthcheckTimeout = 2 * time.Second

// handleFlags parses CLI flags that should short-circuit startup.
//
// It is a pure function for testability: it takes the args slice and
// an output writer explicitly, so callers (main and tests) control
// both inputs. It returns a decision — (shouldExit, exitCode) — and
// does not call os.Exit itself. main() applies the decision.
//
// Current flags:
//
//	--version, -v       print version banner and exit 0
//	--healthcheck       probe /livez over localhost; exit 0 if healthy, 1 otherwise.
//	                    Used by container HEALTHCHECK directives.
//	--port <port>       only used with --healthcheck; localhost port to probe.
//	                    Defaults to $PORT (falling back to config.DefaultPort,
//	                    8080), so the probe targets the same port the server
//	                    binds without --port needing to be kept in sync. Pass
//	                    --port only to override.
//
// Unknown or misspelled flags fail loudly (exit 2) so a typo does not
// silently boot the server.
func handleFlags(args []string, out io.Writer) (shouldExit bool, exitCode int) {
	fs := flag.NewFlagSet("api", flag.ContinueOnError)
	// Capture flag-package error output (it prefixes the bad-flag message)
	// and re-emit through our writer so tests can assert on it.
	fs.SetOutput(out)

	var (
		showVersion bool
		healthcheck bool
		port        string
	)
	fs.BoolVar(&showVersion, "version", false, "print version information and exit")
	fs.BoolVar(&showVersion, "v", false, "print version information and exit (shorthand)")
	fs.BoolVar(&healthcheck, "healthcheck", false,
		"probe /livez on localhost; exit 0 if healthy, 1 otherwise (used by container HEALTHCHECK)")
	// Default the probe port to $PORT (or config.DefaultPort) so it tracks the
	// listener automatically. --port overrides.
	fs.StringVar(&port, "port", config.ResolvePort(),
		"with --healthcheck: localhost port to probe (defaults to $PORT, else 8080)")

	if err := fs.Parse(args); err != nil {
		// fs.Parse has already emitted the error to out.
		return true, 2
	}

	if showVersion {
		_, _ = fmt.Fprint(out, version.Current().Banner("go-api-template"))
		return true, 0
	}

	if healthcheck {
		return true, runHealthcheck(out, port)
	}

	return false, 0
}

// runHealthcheck performs a GET against http://127.0.0.1:<port>/livez and
// returns 0 if the server responds with 200, 1 otherwise.
func runHealthcheck(out io.Writer, port string) int {
	client := &http.Client{Timeout: healthcheckTimeout}
	url := "http://127.0.0.1:" + port + "/livez"
	resp, err := client.Get(url)
	if err != nil {
		_, _ = fmt.Fprintf(out, "healthcheck: %v\n", err)
		return 1
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		_, _ = fmt.Fprintf(out, "healthcheck: status %d\n", resp.StatusCode)
		return 1
	}
	return 0
}
