package version

import (
	"runtime"
	"strings"
	"testing"
)

func TestCurrent(t *testing.T) {
	info := Current()

	// GoVersion must match runtime.Version()
	if info.GoVersion != runtime.Version() {
		t.Errorf("GoVersion = %q, want %q", info.GoVersion, runtime.Version())
	}

	// OSArch must match runtime.GOOS + "/" + runtime.GOARCH
	expectedOSArch := runtime.GOOS + "/" + runtime.GOARCH
	if info.OSArch != expectedOSArch {
		t.Errorf("OSArch = %q, want %q", info.OSArch, expectedOSArch)
	}

	// Version, GitCommit, BuildDate must be non-empty (either ldflag-injected or fallbacks)
	if info.Version == "" {
		t.Error("Version is empty")
	}
	if info.GitCommit == "" {
		t.Error("GitCommit is empty")
	}
	if info.BuildDate == "" {
		t.Error("BuildDate is empty")
	}
}

func TestBanner(t *testing.T) {
	info := Current()
	banner := info.Banner("go-api-template")

	// Must start with app name and version
	if !strings.HasPrefix(banner, "go-api-template version ") {
		t.Errorf("Banner does not start with 'go-api-template version ', got: %q", banner[:min(len(banner), 50)])
	}

	// Must contain expected metadata labels with exact spacing
	expectedLabels := []string{
		"Git commit: ",
		"Build date: ",
		"Go version: ",
		"OS/Arch:    ",
	}
	for _, label := range expectedLabels {
		if !strings.Contains(banner, label) {
			t.Errorf("Banner missing label %q", label)
		}
	}

	// Must end with a newline
	if !strings.HasSuffix(banner, "\n") {
		t.Error("Banner does not end with newline")
	}

	// Must have exactly 5 lines
	lines := strings.Split(strings.TrimSuffix(banner, "\n"), "\n")
	if len(lines) != 5 {
		t.Errorf("Banner has %d lines, want 5", len(lines))
	}
}

func TestBannerWithDifferentAppName(t *testing.T) {
	info := Current()
	banner := info.Banner("my-custom-app")

	if !strings.HasPrefix(banner, "my-custom-app version ") {
		t.Errorf("Banner does not use custom app name, got: %q", banner[:min(len(banner), 50)])
	}
}

func TestBannerDeterministic(t *testing.T) {
	info := Current()
	banner1 := info.Banner("go-api-template")
	banner2 := info.Banner("go-api-template")

	if banner1 != banner2 {
		t.Error("Banner is not deterministic: two calls returned different results")
	}
}
