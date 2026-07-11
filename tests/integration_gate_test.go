// Guards the integration gate (see integration_gate.go): the piece that stops
// `make verify` from printing a green checkmark when every integration test
// SKIPPED (no DB/OPA stack). Compiles the gate to a temp binary and feeds it
// crafted `go test -json` streams on stdin, asserting the exit code.
package main

import (
	"errors"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func buildGate(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test source file")
	}
	src := filepath.Join(filepath.Dir(thisFile), "integration_gate.go")
	bin := filepath.Join(t.TempDir(), "gate")
	if out, err := exec.Command("go", "build", "-o", bin, src).CombinedOutput(); err != nil {
		t.Fatalf("build gate: %v\n%s", err, out)
	}
	return bin
}

func runGate(t *testing.T, bin, stdin string) int {
	t.Helper()
	cmd := exec.Command(bin)
	cmd.Stdin = strings.NewReader(stdin)
	err := cmd.Run()
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	t.Fatalf("run gate: %v", err)
	return -1
}

func TestIntegrationGate(t *testing.T) {
	bin := buildGate(t)

	// An integration test that ran and passed.
	const passed = `{"Action":"run","Package":"p","Test":"TestA_Integration"}
{"Action":"pass","Package":"p","Test":"TestA_Integration","Elapsed":0.1}
`
	// Integration tests all skipped (no DB stack) → not verified.
	const allSkipped = `{"Action":"run","Package":"p","Test":"TestA_Integration"}
{"Action":"skip","Package":"p","Test":"TestA_Integration","Elapsed":0}
{"Action":"run","Package":"p","Test":"TestB_Integration"}
{"Action":"skip","Package":"p","Test":"TestB_Integration","Elapsed":0}
`
	const failed = `{"Action":"run","Package":"p","Test":"TestA_Integration"}
{"Action":"fail","Package":"p","Test":"TestA_Integration","Elapsed":0.1}
`
	// The realistic mixed stream: unit tests PASS but every integration test
	// SKIPPED. The gate must ignore the unit tests and report "not verified" —
	// this is the exact case the `-tags integration ./...` run produces with no DB.
	const unitPassIntegrationSkipped = `{"Action":"run","Package":"p","Test":"TestUnitThing"}
{"Action":"pass","Package":"p","Test":"TestUnitThing","Elapsed":0.01}
{"Action":"run","Package":"p","Test":"TestA_Integration"}
{"Action":"skip","Package":"p","Test":"TestA_Integration","Elapsed":0}
`
	// A subtest of an integration test counts via its top-level name.
	const subtestPassed = `{"Action":"run","Package":"p","Test":"TestA_Integration"}
{"Action":"run","Package":"p","Test":"TestA_Integration/case1"}
{"Action":"pass","Package":"p","Test":"TestA_Integration/case1","Elapsed":0.1}
{"Action":"pass","Package":"p","Test":"TestA_Integration","Elapsed":0.1}
`

	cases := []struct {
		name  string
		input string
		want  int
	}{
		{"integration passed", passed, 0},
		{"all integration skipped → not verified", allSkipped, 2},
		{"an integration failure", failed, 1},
		{"unit passed but integration skipped → not verified", unitPassIntegrationSkipped, 2},
		{"integration subtest passed", subtestPassed, 0},
		{"empty stream", "", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := runGate(t, bin, c.input); got != c.want {
				t.Errorf("gate exit: got %d, want %d", got, c.want)
			}
		})
	}
}
