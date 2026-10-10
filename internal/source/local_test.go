package source

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/metrics"
)

type fakeCollector struct {
	name    string
	initErr error
	panics  bool
	calls   atomic.Int64
}

func (f *fakeCollector) Name() string { return f.name }
func (f *fakeCollector) Init() error  { return f.initErr }
func (f *fakeCollector) Collect(_ context.Context, s *metrics.Sample) error {
	n := f.calls.Add(1)
	if f.panics {
		panic("boom")
	}
	s.Add(metrics.NewKey(f.name, "calls"), float64(n))
	return nil
}

func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatal("condition not met in time")
		}
		time.Sleep(5 * time.Millisecond)
	}
}

func statusOf(l *Local, name string) CollectorStatus {
	for _, s := range l.Status() {
		if s.Name == name {
			return s
		}
	}
	panic("no status for " + name)
}

func TestLocalRunsCollectors(t *testing.T) {
	ok := &fakeCollector{name: "ok"}
	unsupported := &fakeCollector{name: "gpu", initErr: fmt.Errorf("no gpu: %w", collect.ErrUnsupported)}
	broken := &fakeCollector{name: "broken", initErr: errors.New("permission denied")}
	panicky := &fakeCollector{name: "panicky", panics: true}

	l := NewLocal(LocalOptions{Interval: MinInterval}, []collect.Collector{ok, unsupported, broken, panicky})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { _ = l.Run(ctx); close(done) }()

	waitFor(t, func() bool { return ok.calls.Load() >= 3 })
	waitFor(t, func() bool { return statusOf(l, "panicky").State == StateError })

	if v, found := l.Store().Last("ok.calls"); !found || v < 1 {
		t.Fatalf("store value %v %v", v, found)
	}
	if s := statusOf(l, "gpu"); s.State != StateUnsupported {
		t.Fatalf("gpu state %v", s.State)
	}
	if s := statusOf(l, "broken"); s.State != StateError || s.Err == nil {
		t.Fatalf("broken state %v %v", s.State, s.Err)
	}
	if unsupported.calls.Load() != 0 {
		t.Fatal("unsupported collector was called")
	}

	cancel()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("Run did not stop")
	}
}

func TestLocalDemand(t *testing.T) {
	a := &fakeCollector{name: "a"}
	b := &fakeCollector{name: "b"}
	l := NewLocal(LocalOptions{Interval: MinInterval}, []collect.Collector{a, b})
	l.SetDemand([]string{"a"})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = l.Run(ctx) }()

	waitFor(t, func() bool { return a.calls.Load() >= 2 })
	if b.calls.Load() != 0 {
		t.Fatal("collector b ran without demand")
	}
	if statusOf(l, "b").Enabled {
		t.Fatal("b reported enabled")
	}

	l.SetDemand(nil)
	waitFor(t, func() bool { return b.calls.Load() >= 1 })
}

func TestLocalIntervalOverride(t *testing.T) {
	fast := &fakeCollector{name: "fast"}
	slow := &fakeCollector{name: "slow"}
	l := NewLocal(LocalOptions{
		Interval:  MinInterval,
		Intervals: map[string]time.Duration{"slow": time.Hour},
	}, []collect.Collector{fast, slow})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = l.Run(ctx) }()

	waitFor(t, func() bool { return fast.calls.Load() >= 4 })
	if n := slow.calls.Load(); n != 1 {
		t.Fatalf("slow ran %d times", n)
	}
	l.SetInterval(time.Millisecond)
	if l.Interval() != MinInterval {
		t.Fatalf("interval not clamped: %v", l.Interval())
	}
}
