package source

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"runtime/debug"
	"sync"
	"time"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/metrics"
)

// MinInterval is the smallest allowed collection interval.
const MinInterval = 100 * time.Millisecond

// collectTimeout is the minimum time a single collection may take.
const collectTimeout = 10 * time.Second

// LocalOptions configures a Local source.
type LocalOptions struct {
	// Interval is the base collection interval (default 1s).
	Interval time.Duration
	// Intervals overrides the interval of individual collectors by name.
	Intervals map[string]time.Duration
	// Store configures the underlying store.
	Store metrics.StoreOptions
}

// Local collects metrics of the machine it runs on.
type Local struct {
	store      *metrics.Store
	collectors []collect.Collector

	mu        sync.Mutex
	interval  time.Duration
	overrides map[string]time.Duration
	demand    map[string]bool // nil means every collector is wanted
	status    map[string]*CollectorStatus
	wake      map[string]chan struct{}
}

// NewLocal creates a Local source running the given collectors. Pass
// collect.Platform() for the default set.
func NewLocal(opts LocalOptions, collectors []collect.Collector) *Local {
	if opts.Interval <= 0 {
		opts.Interval = time.Second
	}
	l := &Local{
		store:      metrics.NewStore(opts.Store),
		collectors: collectors,
		interval:   max(opts.Interval, MinInterval),
		overrides:  make(map[string]time.Duration),
		status:     make(map[string]*CollectorStatus),
		wake:       make(map[string]chan struct{}),
	}
	for name, d := range opts.Intervals {
		l.overrides[name] = max(d, MinInterval)
	}
	for _, c := range collectors {
		l.status[c.Name()] = &CollectorStatus{Name: c.Name(), Enabled: true}
		l.wake[c.Name()] = make(chan struct{}, 1)
	}
	return l
}

// Name implements Source.
func (l *Local) Name() string { return "local" }

// Store implements Source.
func (l *Local) Store() *metrics.Store { return l.store }

// Interval implements Source.
func (l *Local) Interval() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.interval
}

// SetInterval implements Source. Per-collector overrides are kept.
func (l *Local) SetInterval(d time.Duration) {
	l.mu.Lock()
	l.interval = max(d, MinInterval)
	l.mu.Unlock()
	l.wakeAll()
}

// SetDemand implements Source.
func (l *Local) SetDemand(names []string) {
	l.mu.Lock()
	if names == nil {
		l.demand = nil
	} else {
		l.demand = make(map[string]bool, len(names))
		for _, n := range names {
			l.demand[n] = true
		}
	}
	l.mu.Unlock()
	l.wakeAll()
}

// Status implements Source. Results follow the collector order.
func (l *Local) Status() []CollectorStatus {
	l.mu.Lock()
	defer l.mu.Unlock()
	out := make([]CollectorStatus, 0, len(l.collectors))
	for _, c := range l.collectors {
		out = append(out, *l.status[c.Name()])
	}
	return out
}

// Run starts one goroutine per collector and blocks until ctx is cancelled.
func (l *Local) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	for _, c := range l.collectors {
		wg.Add(1)
		go func() {
			defer wg.Done()
			l.runCollector(ctx, c)
		}()
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		l.pruneLoop(ctx)
	}()
	wg.Wait()
	return nil
}

func (l *Local) wakeAll() {
	for _, ch := range l.wake {
		select {
		case ch <- struct{}{}:
		default:
		}
	}
}

// schedule returns whether the collector is wanted and its interval.
func (l *Local) schedule(name string) (bool, time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()
	enabled := l.demand == nil || l.demand[name]
	l.status[name].Enabled = enabled
	if d, ok := l.overrides[name]; ok {
		return enabled, d
	}
	return enabled, l.interval
}

func (l *Local) defaultOverride(c collect.Collector) {
	di, ok := c.(collect.DefaultIntervaler)
	if !ok {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if _, set := l.overrides[c.Name()]; !set {
		l.overrides[c.Name()] = max(di.DefaultInterval(), MinInterval)
	}
}

func (l *Local) runCollector(ctx context.Context, c collect.Collector) {
	name := c.Name()
	if cl, ok := c.(collect.Closer); ok {
		defer func() {
			if err := cl.Close(); err != nil {
				slog.Debug("collector close", "collector", name, "err", err)
			}
		}()
	}
	if err := safeCall(c.Init); err != nil {
		state := StateError
		if errors.Is(err, collect.ErrUnsupported) {
			state = StateUnsupported
		} else {
			slog.Warn("collector init failed", "collector", name, "err", err)
		}
		l.setStatus(name, func(s *CollectorStatus) { s.State, s.Err = state, err })
		return
	}
	l.defaultOverride(c)

	// Runs are aligned to wall-clock multiples of the interval so collectors
	// with the same interval fire together and the UI redraws once per tick
	// instead of once per collector.
	var lastRun time.Time
	timer := time.NewTimer(0)
	defer timer.Stop()
	for {
		enabled, interval := l.schedule(name)
		now := time.Now()
		if enabled && (lastRun.IsZero() || !now.Before(nextTick(lastRun, interval))) {
			lastRun = now
			l.collectOnce(ctx, c, interval)
		}

		wait := interval
		if enabled {
			wait = max(time.Until(nextTick(lastRun, interval)), 0)
		}
		// Since Go 1.23 Reset discards any pending tick, so no draining is needed.
		timer.Reset(wait)
		select {
		case <-ctx.Done():
			return
		case <-l.wake[name]:
		case <-timer.C:
		}
	}
}

// nextTick returns the first multiple of interval after t, unless that is
// less than half an interval away (then the one after), so a run that
// happened just before a boundary is not immediately repeated.
func nextTick(t time.Time, interval time.Duration) time.Time {
	next := t.Truncate(interval).Add(interval)
	if next.Sub(t) < interval/2 {
		next = next.Add(interval)
	}
	return next
}

func (l *Local) collectOnce(ctx context.Context, c collect.Collector, interval time.Duration) {
	// The timeout only guards against hung collectors; a slow but working
	// one (a process sweep on macOS can take over a second) must finish.
	cctx, cancel := context.WithTimeout(ctx, max(2*interval, collectTimeout))
	defer cancel()
	sample := metrics.Sample{Time: time.Now()}
	start := time.Now()
	err := safeCall(func() error { return c.Collect(cctx, &sample) })
	dur := time.Since(start)
	if err == nil {
		l.store.Write(sample)
	} else if ctx.Err() == nil {
		slog.Debug("collect failed", "collector", c.Name(), "err", err)
	}
	l.setStatus(c.Name(), func(s *CollectorStatus) {
		s.LastRun, s.Duration, s.Interval, s.Err = start, dur, interval, err
		s.State = StateOK
		if err != nil {
			s.State = StateError
		}
	})
}

func (l *Local) setStatus(name string, f func(*CollectorStatus)) {
	l.mu.Lock()
	defer l.mu.Unlock()
	f(l.status[name])
}

// pruneLoop drops series that stopped being updated (exited processes,
// removed devices). A series is stale after missing several intervals.
func (l *Local) pruneLoop(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			l.store.Prune(now.Add(-l.staleAfter()))
		}
	}
}

func (l *Local) staleAfter() time.Duration {
	l.mu.Lock()
	defer l.mu.Unlock()
	longest := l.interval
	for _, d := range l.overrides {
		longest = max(longest, d)
	}
	return max(30*time.Second, 5*longest)
}

// safeCall runs f and converts a panic into an error so a buggy collector
// cannot take down the application.
func safeCall(f func() error) (err error) {
	defer func() {
		if r := recover(); r != nil {
			slog.Error("collector panic", "panic", r, "stack", string(debug.Stack()))
			err = fmt.Errorf("panic: %v", r)
		}
	}()
	return f()
}
