// This test guards the pretty test-runner's exit behaviour (see test_runner.go).
// The runner is a //go:build ignore standalone tool, so it is compiled to a temp
// binary and driven against throwaway modules. It must exit NON-ZERO on a tree
// that does not build or where no test ran — the bug being that it previously
// discarded build errors and printed "0 failed", exiting 0 on a broken tree.
package main

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"testing"
)

// buildRunner compiles test_runner.go (this dir) into a temp binary once.
func buildRunner(t *testing.T) string {
	t.Helper()
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate the test source file")
	}
	runnerSrc := filepath.Join(filepath.Dir(thisFile), "test_runner.go")
	bin := filepath.Join(t.TempDir(), "prettyrunner")
	if out, err := exec.Command("go", "build", "-o", bin, runnerSrc).CombinedOutput(); err != nil {
		t.Fatalf("build runner: %v\n%s", err, out)
	}
	return bin
}

// writeModule writes a throwaway module with the given files plus a go.mod.
func writeModule(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files["go.mod"] = "module runnertest\n\ngo 1.26\n"
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

// runRunnerIn runs the compiled runner with cwd=dir and returns its exit code.
func runRunnerIn(t *testing.T, bin, dir string) int {
	t.Helper()
	cmd := exec.Command(bin)
	cmd.Dir = dir
	err := cmd.Run()
	if err == nil {
		return 0
	}
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		return ee.ExitCode()
	}
	t.Fatalf("run runner: %v", err)
	return -1
}

func TestRunner_ExitsNonZeroOnBrokenBuild(t *testing.T) {
	bin := buildRunner(t)
	// A package that does not compile, plus a test that references it.
	dir := writeModule(t, map[string]string{
		"broken.go":      "package runnertest\n\nfunc F() int { return undefinedSymbol }\n",
		"broken_test.go": "package runnertest\n\nimport \"testing\"\n\nfunc TestF(t *testing.T) { _ = F() }\n",
	})
	if code := runRunnerIn(t, bin, dir); code == 0 {
		t.Error("runner exited 0 on a tree that does not compile; want non-zero (the original bug)")
	}
}

func TestRunner_ExitsNonZeroWhenNoTestsRan(t *testing.T) {
	bin := buildRunner(t)
	// A compiling module with NO test files: zero test events → not verified.
	dir := writeModule(t, map[string]string{
		"lib.go": "package runnertest\n\nfunc Add(a, b int) int { return a + b }\n",
	})
	if code := runRunnerIn(t, bin, dir); code == 0 {
		t.Error("runner exited 0 when no tests ran; want non-zero")
	}
}

func TestRunner_ExitsZeroOnGreenTree(t *testing.T) {
	bin := buildRunner(t)
	dir := writeModule(t, map[string]string{
		"ok_test.go": "package runnertest\n\nimport \"testing\"\n\nfunc TestOK(t *testing.T) {}\n",
	})
	if code := runRunnerIn(t, bin, dir); code != 0 {
		t.Errorf("runner exited %d on a passing tree; want 0", code)
	}
}
