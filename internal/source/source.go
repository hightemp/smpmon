// Package source provides metric sources. A Source owns a metrics.Store and
// keeps it filled; the UI only ever reads from the store, so local, remote and
// historical data look the same to widgets.
package source

import (
	"context"
	"time"

	"github.com/hightemp/smpmon/internal/metrics"
)

// Source is a stream of metrics for one host.
type Source interface {
	// Name identifies the source ("local" or a remote host name).
	Name() string
	// Store returns the store the source writes into.
	Store() *metrics.Store
	// Run collects until ctx is cancelled.
	Run(ctx context.Context) error
	// SetDemand enables exactly the named collectors; nil enables all.
	SetDemand(names []string)
	// SetInterval changes the base collection interval.
	SetInterval(d time.Duration)
	// Interval returns the base collection interval.
	Interval() time.Duration
	// Status reports per-collector health.
	Status() []CollectorStatus
}

// State of a collector.
type State uint8

// Collector states.
const (
	StateIdle        State = iota // not started yet or not in demand
	StateOK                       // last run succeeded
	StateError                    // last run failed
	StateUnsupported              // Init reported ErrUnsupported
)

func (s State) String() string {
	return [...]string{"idle", "ok", "error", "unsupported"}[s]
}

// CollectorStatus is a health snapshot of one collector.
type CollectorStatus struct {
	Name     string
	State    State
	Err      error
	LastRun  time.Time
	Duration time.Duration
	Interval time.Duration
	Enabled  bool
}

// ConnState is the state of a remote source's connection.
type ConnState uint8

// Connection states.
const (
	Connecting ConnState = iota
	Online
	Offline
)

func (s ConnState) String() string {
	return [...]string{"connecting", "online", "offline"}[s]
}

// ConnInfo describes a remote source's connection.
type ConnInfo struct {
	State    ConnState
	Err      error
	Latency  time.Duration
	LastData time.Time
	Hostname string // reported by the agent
	OS, Arch string
	Agent    string // agent's smpmon version
}

// Remote is implemented by sources that read from another machine.
type Remote interface {
	Source
	Conn() ConnInfo
}

// ParseState parses a State name.
func ParseState(s string) State {
	for i, n := range [...]string{"idle", "ok", "error", "unsupported"} {
		if n == s {
			return State(i)
		}
	}
	return StateIdle
}
