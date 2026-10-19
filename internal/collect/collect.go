// Package collect defines the Collector interface together with the
// platform-neutral table types and metric descriptions that collectors
// produce. Implementations live in OS-specific subpackages; see
// collect/platform for the set used on the current OS.
package collect

import (
	"context"
	"errors"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/hightemp/smpmon/internal/metrics"
)

// ErrUnsupported is returned by Init when a collector cannot work on this
// platform or machine (no GPU, no battery, missing permissions, ...).
var ErrUnsupported = errors.New("not supported on this system")

// Collector gathers one group of metrics. Implementations keep state between
// calls (e.g. previous counters to compute rates) and are called from a single
// goroutine, so they need no internal locking.
type Collector interface {
	// Name is a stable identifier used in config and status, e.g. "cpu".
	Name() string
	// Init prepares the collector. Returning an error wrapping ErrUnsupported
	// disables it without being reported as a failure.
	Init() error
	// Collect appends points and tables to s.
	Collect(ctx context.Context, s *metrics.Sample) error
}

// DefaultIntervaler can be implemented by collectors that should run at a
// different default rate than the global interval (e.g. slow sensors).
type DefaultIntervaler interface {
	DefaultInterval() time.Duration
}

// SignalInfo names a signal that can be sent to a process.
type SignalInfo struct {
	Name   string
	Number int
}

// ProcessController performs actions on processes.
type ProcessController interface {
	// Signals lists the signals offered in the UI; the first is the default
	// for "terminate" and the second for "kill".
	Signals() []SignalInfo
	Signal(pid int32, sig int) error
	// SetNice changes the scheduling priority (-20..19 on Unix).
	SetNice(pid int32, nice int) error
}

// Closer can be implemented by collectors that hold resources such as child
// processes; Close is called when the source stops.
type Closer interface {
	Close() error
}

// CollectorsFor maps metric selectors to the collectors producing them.
func CollectorsFor(patterns []string) []string {
	var out []string
	add := func(c string) {
		if !slices.Contains(out, c) {
			out = append(out, c)
		}
	}
	for _, p := range patterns {
		first, _, _ := strings.Cut(p, ".")
		switch first {
		case "cpu":
			add("cpu")
			if last := p[strings.LastIndexByte(p, '.')+1:]; last == "**" || matchGlob(last, "temp") {
				add("sensors")
			}
		case "sensors":
			add("sensors")
		case "gpu":
			add("gpu")
			if last := p[strings.LastIndexByte(p, '.')+1:]; last == "**" || matchGlob(last, "pcie_rx") {
				add("gpu_pcie")
			}
		case "battery":
			add("battery")
		case "docker":
			add("docker")
		case "load", "system":
			add("load")
		case "mem", "swap":
			add("mem")
		case "disk":
			add("disks")
		case "net":
			add("net")
		case "proc":
			add("procs")
		default: // wildcard or unknown prefix: everything may be needed
			return nil
		}
	}
	return out
}

// matchGlob matches a selector segment glob against a name.
func matchGlob(pattern, name string) bool {
	ok, err := path.Match(pattern, name)
	return ok && err == nil
}
