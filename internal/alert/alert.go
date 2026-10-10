// Package alert evaluates threshold rules against metric stores and sends
// notifications when they fire and resolve.
package alert

import (
	"context"
	"fmt"
	"log/slog"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hightemp/smpmon/internal/metrics"
)

// Level of an alert.
type Level string

// Levels.
const (
	Warn Level = "warn"
	Crit Level = "crit"
)

// Rule is a compiled alert rule.
type Rule struct {
	Name       string
	Expr       string
	Selector   metrics.Selector
	Op         string
	Threshold  float64
	For        time.Duration
	Level      Level
	Hysteresis float64
	Cooldown   time.Duration
	Hosts      []string // empty = all hosts
	Notify     []string // tui, desktop, exec, webhook
	Exec       string
	Webhook    string
}

var exprRe = regexp.MustCompile(`^\s*(\S+)\s*(>=|<=|==|!=|>|<)\s*(-?[0-9.]+)\s*([a-zA-Z%]*)\s*$`)

var suffixes = map[string]float64{
	"": 1, "%": 1, "k": 1e3, "m": 1e6, "g": 1e9, "t": 1e12,
	"ki": 1 << 10, "mi": 1 << 20, "gi": 1 << 30, "ti": 1 << 40,
	"kb": 1e3, "mb": 1e6, "gb": 1e9, "tb": 1e12,
	"kib": 1 << 10, "mib": 1 << 20, "gib": 1 << 30, "tib": 1 << 40,
}

// ParseExpr parses "selector op number[suffix]", e.g. "mem.used > 8GiB".
func ParseExpr(expr string) (metrics.Selector, string, float64, error) {
	m := exprRe.FindStringSubmatch(expr)
	if m == nil {
		return metrics.Selector{}, "", 0, fmt.Errorf("expression %q: want <metric> <op> <number>, e.g. cpu.total.usage > 90", expr)
	}
	sel, err := metrics.ParseSelector(m[1])
	if err != nil {
		return metrics.Selector{}, "", 0, err
	}
	v, err := strconv.ParseFloat(m[3], 64)
	if err != nil {
		return metrics.Selector{}, "", 0, fmt.Errorf("expression %q: %w", expr, err)
	}
	mult, ok := suffixes[strings.ToLower(m[4])]
	if !ok {
		return metrics.Selector{}, "", 0, fmt.Errorf("expression %q: unknown suffix %q", expr, m[4])
	}
	return sel, m[2], v * mult, nil
}

// holds reports whether v satisfies the condition. Once firing, the alert
// only resolves when the value has moved hysteresis past the threshold.
func (r *Rule) holds(v float64, firing bool) bool {
	t := r.Threshold
	if firing {
		switch r.Op {
		case ">", ">=":
			t -= r.Hysteresis
		case "<", "<=":
			t += r.Hysteresis
		}
	}
	switch r.Op {
	case ">":
		return v > t
	case ">=":
		return v >= t
	case "<":
		return v < t
	case "<=":
		return v <= t
	case "==":
		return v == t
	case "!=":
		return v != t
	}
	return false
}

// State of an alert instance.
type State uint8

// States.
const (
	Inactive State = iota
	Pending
	Firing
)

// Event is a change of an alert instance.
type Event struct {
	Time     time.Time
	Rule     *Rule
	Host     string
	Key      metrics.Key
	Value    float64
	Resolved bool
}

// Summary formats the event for messages.
func (e Event) Summary() string {
	unit := metrics.DefaultRegistry.UnitOf(e.Key)
	val := metrics.Format(e.Value, unit)
	thr := metrics.Format(e.Rule.Threshold, unit)
	host := ""
	if e.Host != "" && e.Host != "local" {
		host = " on " + e.Host
	}
	if e.Resolved {
		return fmt.Sprintf("resolved: %s%s: %s is %s", e.Rule.Name, host, e.Key, val)
	}
	return fmt.Sprintf("%s%s: %s is %s (%s %s)", e.Rule.Name, host, e.Key, val, e.Rule.Op, thr)
}

// Active is a currently firing alert.
type Active struct {
	Rule  *Rule
	Host  string
	Key   metrics.Key
	Value float64
	Since time.Time
}

type instance struct {
	state     State
	since     time.Time // when the condition started holding
	firedAt   time.Time
	resolved  time.Time
	lastValue float64
}

type hostStore struct {
	name  string
	store *metrics.Store
}

// Engine evaluates rules every second.
type Engine struct {
	rules    []*Rule
	notifier Notifier

	mu        sync.Mutex
	hosts     []hostStore
	instances map[string]*instance
	events    []Event // most recent last
	seq       uint64  // number of events ever produced
}

// Notifier sends notifications other than the TUI's.
type Notifier interface {
	Notify(ctx context.Context, e Event)
}

const maxEvents = 200

// NewEngine creates an engine.
func NewEngine(rules []*Rule, n Notifier) *Engine {
	return &Engine{rules: rules, notifier: n, instances: map[string]*instance{}}
}

// Rules returns the rules.
func (e *Engine) Rules() []*Rule { return e.rules }

// AddHost registers a host's store for evaluation.
func (e *Engine) AddHost(name string, st *metrics.Store) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.hosts = append(e.hosts, hostStore{name, st})
}

// Run evaluates every second until ctx is cancelled.
func (e *Engine) Run(ctx context.Context) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			for _, ev := range e.Evaluate(now) {
				slog.Info("alert", "event", ev.Summary())
				if e.notifier != nil {
					go e.notifier.Notify(ctx, ev)
				}
			}
		}
	}
}

// Evaluate checks all rules at now and returns the resulting events.
func (e *Engine) Evaluate(now time.Time) []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []Event
	seen := map[string]bool{}
	for _, h := range e.hosts {
		for _, r := range e.rules {
			if len(r.Hosts) > 0 && !slices.Contains(r.Hosts, h.name) {
				continue
			}
			for _, k := range h.store.Keys(r.Selector) {
				id := h.name + "\x00" + r.Name + "\x00" + string(k)
				seen[id] = true // a stale series still exists: don't resolve it
				t, v, ok := h.store.LastAt(k)
				if !ok || math.IsNaN(v) || now.Sub(t) > time.Minute {
					continue // stale data neither fires nor resolves
				}
				inst := e.instances[id]
				if inst == nil {
					inst = &instance{}
					e.instances[id] = inst
				}
				inst.lastValue = v
				if ev, ok := step(inst, r, h.name, k, v, now); ok {
					out = append(out, ev)
				}
			}
		}
	}
	// Series that disappeared (e.g. an unmounted disk) resolve.
	for id, inst := range e.instances {
		if !seen[id] && inst.state == Firing {
			parts := strings.SplitN(id, "\x00", 3)
			if r := e.rule(parts[1]); r != nil {
				inst.state, inst.resolved = Inactive, now
				out = append(out, Event{Time: now, Rule: r, Host: parts[0], Key: metrics.Key(parts[2]), Value: inst.lastValue, Resolved: true})
			}
		}
	}
	for _, ev := range out {
		e.events = append(e.events, ev)
		e.seq++
	}
	if len(e.events) > maxEvents {
		e.events = slices.Clone(e.events[len(e.events)-maxEvents:])
	}
	return out
}

func step(inst *instance, r *Rule, host string, k metrics.Key, v float64, now time.Time) (Event, bool) {
	holds := r.holds(v, inst.state == Firing)
	switch inst.state {
	case Inactive:
		if !holds {
			return Event{}, false
		}
		if !inst.resolved.IsZero() && now.Sub(inst.resolved) < r.Cooldown {
			return Event{}, false
		}
		inst.state, inst.since = Pending, now
		fallthrough
	case Pending:
		if !holds {
			inst.state = Inactive
			return Event{}, false
		}
		if now.Sub(inst.since) >= r.For {
			inst.state, inst.firedAt = Firing, now
			return Event{Time: now, Rule: r, Host: host, Key: k, Value: v}, true
		}
	case Firing:
		if !holds {
			inst.state, inst.resolved = Inactive, now
			return Event{Time: now, Rule: r, Host: host, Key: k, Value: v, Resolved: true}, true
		}
	}
	return Event{}, false
}

func (e *Engine) rule(name string) *Rule {
	for _, r := range e.rules {
		if r.Name == name {
			return r
		}
	}
	return nil
}

// Active returns the firing alerts, most severe and oldest first.
func (e *Engine) Active() []Active {
	e.mu.Lock()
	defer e.mu.Unlock()
	var out []Active
	for id, inst := range e.instances {
		if inst.state != Firing {
			continue
		}
		parts := strings.SplitN(id, "\x00", 3)
		if r := e.rule(parts[1]); r != nil {
			out = append(out, Active{Rule: r, Host: parts[0], Key: metrics.Key(parts[2]), Value: inst.lastValue, Since: inst.firedAt})
		}
	}
	slices.SortFunc(out, func(a, b Active) int {
		if a.Rule.Level != b.Rule.Level {
			if a.Rule.Level == Crit {
				return -1
			}
			return 1
		}
		return a.Since.Compare(b.Since)
	})
	return out
}

// Events returns events after sequence number since and the current
// sequence number, so callers can poll for new events.
func (e *Engine) Events(since uint64) ([]Event, uint64) {
	e.mu.Lock()
	defer e.mu.Unlock()
	n := int(e.seq - since)
	if n <= 0 {
		return nil, e.seq
	}
	n = min(n, len(e.events))
	return slices.Clone(e.events[len(e.events)-n:]), e.seq
}

// Recent returns up to n recent events, newest first.
func (e *Engine) Recent(n int) []Event {
	e.mu.Lock()
	defer e.mu.Unlock()
	out := make([]Event, 0, min(n, len(e.events)))
	for i := len(e.events) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, e.events[i])
	}
	return out
}
