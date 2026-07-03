// Package scripts hosts repo-utility scripts and their Go tests. The
// Go files here exist only so `go test ./...` can drive the shell
// scripts and assert their behaviour — the actual logic lives in
// extract-changelog.sh.
package scripts

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const fixture = "testdata/CHANGELOG.md"

// runScript invokes the shell script with args. Returns stdout, stderr,
// and the exit code. The script is invoked via `sh` so the test is
// shell-agnostic.
func runScript(t *testing.T, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	scriptPath, err := filepath.Abs("extract-changelog.sh")
	if err != nil {
		t.Fatalf("abs: %v", err)
	}
	cmd := exec.Command("sh", append([]string{scriptPath}, args...)...)
	var so, se bytes.Buffer
	cmd.Stdout = &so
	cmd.Stderr = &se
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return so.String(), se.String(), exitErr.ExitCode()
		}
		t.Fatalf("run script: %v\nstderr: %s", err, se.String())
	}
	return so.String(), se.String(), 0
}

func TestExtract_ExistingSection(t *testing.T) {
	stdout, stderr, code := runScript(t, "1.2.3", fixture)
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr)
	}
	// Expect headings and bullet contents from the 1.2.3 section.
	for _, want := range []string{"### Added", "new feature alpha", "### Fixed", "bug in the thing"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("missing %q in stdout:\n%s", want, stdout)
		}
	}
	// Must NOT include the next section's heading or its content.
	for _, unwanted := range []string{"## [1.2.0]", "Initial public release", "[Unreleased]"} {
		if strings.Contains(stdout, unwanted) {
			t.Errorf("body leaked %q from neighbouring section:\n%s", unwanted, stdout)
		}
	}
}

func TestExtract_VersionWithoutTrailingDate(t *testing.T) {
	// "1.2.0" exists in the fixture as `## [1.2.0] - 2026-01-01`.
	// Confirm the body extracts even with a trailing " - DATE".
	stdout, stderr, code := runScript(t, "1.2.0", fixture)
	if code != 0 {
		t.Fatalf("exit %d, stderr: %s", code, stderr)
	}
	if !strings.Contains(stdout, "Initial public release") {
		t.Errorf("missing expected content:\n%s", stdout)
	}
}

func TestExtract_MissingSection(t *testing.T) {
	_, stderr, code := runScript(t, "99.0.0", fixture)
	if code != 1 {
		t.Errorf("expected exit 1 for missing section, got %d", code)
	}
	if !strings.Contains(stderr, "not found") {
		t.Errorf("error should say 'not found', got: %s", stderr)
	}
}

func TestExtract_EmptySection(t *testing.T) {
	// [0.0.1] is the deliberately empty fixture section.
	_, stderr, code := runScript(t, "0.0.1", fixture)
	if code != 1 {
		t.Errorf("expected exit 1 for empty section, got %d", code)
	}
	if !strings.Contains(stderr, "no content") {
		t.Errorf("error should say 'no content', got: %s", stderr)
	}
}

// TestExtract_VersionPrefixMustNotFalseMatch verifies that asking for
// "1.2.3" does NOT accidentally match a heading like "## [1.2.31]" via
// loose prefix matching.
func TestExtract_VersionPrefixMustNotFalseMatch(t *testing.T) {
	// Build a temp changelog where "1.2.31" exists but "1.2.3" doesn't.
	tmp := t.TempDir()
	cl := filepath.Join(tmp, "CHANGELOG.md")
	content := []byte("# Changelog\n\n## [1.2.31] - 2026-02-01\n\nsome notes\n")
	if err := os.WriteFile(cl, content, 0o600); err != nil {
		t.Fatalf("write fixture: %v", err)
	}
	_, stderr, code := runScript(t, "1.2.3", cl)
	if code != 1 {
		t.Errorf("expected exit 1 (not found), got %d (stderr: %s)", code, stderr)
	}
}

func TestExtract_MissingFile(t *testing.T) {
	_, stderr, code := runScript(t, "1.0.0", "testdata/does-not-exist.md")
	if code != 2 {
		t.Errorf("expected exit 2 for missing file, got %d", code)
	}
	if !strings.Contains(stderr, "not found") {
		t.Errorf("error should say 'not found', got: %s", stderr)
	}
}

func TestExtract_UsageError(t *testing.T) {
	_, _, code := runScript(t)
	if code != 2 {
		t.Errorf("expected exit 2 with no args, got %d", code)
	}
}
