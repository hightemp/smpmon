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
