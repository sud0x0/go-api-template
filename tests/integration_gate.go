//go:build ignore

// Command integration-gate reads `go test -json` from stdin and enforces that
// integration tests actually RAN. `go test` exits 0 when every test t.Skip()s
// (which is what the integration suite does when the DB/OPA stack env is absent),
// so `make verify` would otherwise print a green "✓ verify passed" checkmark
// having verified nothing. This gate fails loudly instead.
//
//	go test -p 1 -tags integration -json ./... | go run tests/integration_gate.go
//
// Exit codes: 0 = at least one test ran and none failed; 1 = a test failed;
// 2 = zero tests ran (all skipped / none selected) → NOT verified.
package main

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
)

// integrationSuffix identifies an integration test. `go test -tags integration
// ./...` runs the WHOLE suite (unit + integration), so counting every test would
// always see the unit tests run and never detect that the integration tests
// themselves skipped. This repo's integration test functions are all named
// `Test..._Integration` (verified across the //go:build integration files), so
// the gate counts ONLY those. A new integration test MUST follow that convention
// or the gate will not see it.
const integrationSuffix = "_Integration"

type gateEvent struct {
	Action string `json:"Action"`
	Test   string `json:"Test"`
}

// gate tallies pass/fail/skip events for INTEGRATION tests only (see
// integrationSuffix) from a `go test -json` stream and returns the process exit
// code plus a human summary.
func gate(in io.Reader) (code int, summary string) {
	sc := bufio.NewScanner(in)
	sc.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	var pass, fail, skip int
	for sc.Scan() {
		var e gateEvent
		if err := json.Unmarshal(sc.Bytes(), &e); err != nil {
			continue
		}
		if e.Test == "" { // ignore package-level events
			continue
		}
		// Consider only integration tests: match the TOP-LEVEL test name (a
		// subtest event is "TestX_Integration/case", so cut at the first '/').
		topLevel, _, _ := strings.Cut(e.Test, "/")
		if !strings.HasSuffix(topLevel, integrationSuffix) {
			continue
		}
		switch e.Action {
		case "pass":
			pass++
		case "fail":
			fail++
		case "skip":
			skip++
		}
	}
	ran := pass + fail
	base := fmt.Sprintf("integration: %d ran (%d passed, %d failed), %d skipped", ran, pass, fail, skip)
	switch {
	case fail > 0:
		return 1, base + "\nintegration tests FAILED"
	case ran == 0:
		return 2, base + "\n!!! integration tests SKIPPED — NOT verified (is the DB/OPA stack up? run `make run`) !!!"
	default:
		return 0, base + "\nintegration tests verified"
	}
}

func main() {
	code, summary := gate(os.Stdin)
	fmt.Fprintln(os.Stderr, summary)
	os.Exit(code)
}
