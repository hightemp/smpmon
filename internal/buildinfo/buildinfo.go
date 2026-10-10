// Package buildinfo exposes version metadata injected at build time via ldflags.
package buildinfo

import (
	"fmt"
	"runtime"
)

// These are overridden with -ldflags "-X github.com/hightemp/smpmon/internal/buildinfo.Version=...".
var (
	Version = "dev"
	Commit  = "none"
	Date    = "unknown"
)

// String returns a human-readable one-line version description.
func String() string {
	return fmt.Sprintf("smpmon %s (commit %s, built %s, %s %s/%s)",
		Version, Commit, Date, runtime.Version(), runtime.GOOS, runtime.GOARCH)
}
