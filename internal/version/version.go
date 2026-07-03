// Package version exposes build-time and runtime version metadata.
//
// Version, GitCommit, and BuildDate are overridden at build time via
// the linker:
//
//	go build -ldflags "-X github.com/sud0x0/go-api-template/internal/version.Version=<v> \
//	                   -X github.com/sud0x0/go-api-template/internal/version.GitCommit=<sha> \
//	                   -X github.com/sud0x0/go-api-template/internal/version.BuildDate=<iso8601>" \
//	         ./cmd/api
//
// If built without ldflags (for example, a direct `go run`), the three
// build-time values fall back to "dev" / "unknown" sentinels.
package version

import (
	"fmt"
	"runtime"
)

// These three are set by -ldflags at build time.
var (
	Version   = "dev"
	GitCommit = "unknown"
	BuildDate = "unknown"
)

// Info bundles the values for formatted display.
type Info struct {
	Version   string
	GitCommit string
	BuildDate string
	GoVersion string
	OSArch    string
}

// Current returns the current build's version information.
func Current() Info {
	return Info{
		Version:   Version,
		GitCommit: GitCommit,
		BuildDate: BuildDate,
		GoVersion: runtime.Version(),
		OSArch:    runtime.GOOS + "/" + runtime.GOARCH,
	}
}

// Banner returns a multi-line, human-readable version banner in the
// style of common Go CLI tools. The first line is the app name and
// version; subsequent lines are indented metadata.
func (i Info) Banner(appName string) string {
	return fmt.Sprintf(
		"%s version %s\n"+
			"  Git commit: %s\n"+
			"  Build date: %s\n"+
			"  Go version: %s\n"+
			"  OS/Arch:    %s\n",
		appName, i.Version, i.GitCommit, i.BuildDate, i.GoVersion, i.OSArch,
	)
}
