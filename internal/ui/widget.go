package ui

import (
	"fmt"
	"reflect"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/hightemp/smpmon/internal/chart"
	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/config"
	"github.com/hightemp/smpmon/internal/keymap"
	"github.com/hightemp/smpmon/internal/metrics"
)

// Context is what widgets need to render and act. It is shared by all
// widgets and refreshed by the app before every frame.
type Context struct {
	Store     *metrics.Store
	Registry  *metrics.Registry
	Theme     *Theme
	GraphMode chart.Mode
	Interval  time.Duration
	Now       time.Time
	Procs     collect.ProcessController // nil if unsupported
	NumCPU    int
	Keys      *keymap.Keymap
	Host      string       // name of the host being shown
	Hosts     []HostSource // all hosts, for the overview
	// History is set while Store holds a recorded time window instead of
	// live data: graphs then fit the whole window into their width.
	History bool
}

// series reads up to n values of k for a graph n slots wide.
func (c *Context) series(k metrics.Key, buf []float64, n int) []float64 {
	if !c.History {
		return c.Store.Series(k, buf, n)
	}
	return chart.Resample(buf[:0], c.Store.Series(k, nil, 0), n, chart.AggMax)
}

// action resolves a key press to an action in the given scopes.
func (c *Context) action(k tea.KeyPressMsg, scopes ...keymap.Scope) keymap.Action {
	a, _ := c.Keys.Match(k.String(), scopes...)
	return a
}

// keyHint formats the first key of an action with a label for footers.
func keyHint(ctx *Context, a keymap.Action, label string) string {
	k := ctx.Keys.First(a)
	if k == "" {
		return ""
	}
	return hint(ctx, k, label)
}

// joinHints joins non-empty hints with spaces.
func joinHints(hints ...string) string {
	var out []string
	for _, h := range hints {
		if h != "" {
			out = append(out, h)
		}
	}
	return strings.Join(out, " ")
}

// Widget is a dashboard tile. Render is called with the inner size (inside
// the frame) and must return exactly h lines of w cells.
type Widget interface {
	// Kind is the widget type name from the config.
	Kind() string
	// Title returns the frame title and an optional right-aligned info text.
	Title(ctx *Context) (title, info string)
	// Render draws the content area.
	Render(ctx *Context, w, h int) []string
	// Collectors lists the collectors whose data the widget shows.
	Collectors() []string
}

// Interactive widgets accept input while focused.
type Interactive interface {
	Widget
	// HandleKey processes a key press. It returns false if the key was not
	// used so the app can apply global bindings.
	HandleKey(ctx *Context, k tea.KeyPressMsg) (bool, tea.Cmd)
	// HandleMouse processes a mouse event at (x, y) relative to the content area.
	HandleMouse(ctx *Context, m tea.MouseMsg, x, y int) tea.Cmd
	// Footer returns key hints shown on the bottom border while focused.
	Footer(ctx *Context) string
	// Capturing reports whether the widget wants all keys (e.g. while typing
	// a filter), suppressing global bindings.
	Capturing() bool
}

// factory creates a widget from its layout node.
type factory func(n *config.Node) (Widget, error)

var factories = map[string]factory{}

func register(name string, f factory) { factories[name] = f }

// widgetMeta describes a widget type for the layout editor.
type widgetMeta struct {
	help    string
	options reflect.Type // the options struct
	example string       // options YAML for a new instance, if required
}

var metas = map[string]widgetMeta{
	"cpu":     {"CPU graph and per-core usage", reflect.TypeFor[cpuOptions](), ""},
	"mem":     {"memory and swap", reflect.TypeFor[memOptions](), ""},
	"disks":   {"filesystems and disk I/O", reflect.TypeFor[disksOptions](), ""},
	"net":     {"network throughput", reflect.TypeFor[netOptions](), ""},
	"proc":    {"process table", reflect.TypeFor[procOptions](), ""},
	"gpu":     {"GPU usage", reflect.TypeFor[gpuOptions](), ""},
	"sensors": {"temperatures and fans", reflect.TypeFor[sensorsOptions](), ""},
	"battery": {"battery charge", reflect.TypeFor[struct{}](), ""},
	"docker":  {"containers", reflect.TypeFor[dockerOptions](), ""},
	"graph":   {"graph of any metrics", reflect.TypeFor[graphOptions](), "metrics: [cpu.total.usage]"},
	"gauge":   {"bar for one metric", reflect.TypeFor[gaugeOptions](), "metric: cpu.total.usage"},
	"stat":    {"big number with sparkline", reflect.TypeFor[statOptions](), "metric: mem.used_percent"},
	"table":   {"table of metrics", reflect.TypeFor[tableOptions](), "metrics: ['mem.*']"},
	"text":    {"text with host info", reflect.TypeFor[textOptions](), ""},
	"hosts":   {"overview of all hosts", reflect.TypeFor[struct{}](), ""},
}

// KnownWidget reports whether a widget type exists.
func KnownWidget(name string) bool {
	_, ok := factories[name]
	return ok
}

// WidgetNames lists all widget types.
func WidgetNames() []string {
	names := make([]string, 0, len(factories))
	for n := range factories {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

func newWidget(n *config.Node) (Widget, error) {
	f, ok := factories[n.Widget]
	if !ok {
		return nil, fmt.Errorf("line %d: unknown widget %q", n.Line, n.Widget)
	}
	w, err := f(n)
	if err != nil {
		return nil, fmt.Errorf("line %d: %w", n.Line, err)
	}
	return w, nil
}

// collectorsFor maps metric selectors to the collectors producing them.
func collectorsFor(patterns []string) []string { return collect.CollectorsFor(patterns) }

// graphMode returns the widget's graph mode override or the global one.
func graphMode(ctx *Context, override string) chart.Mode {
	if m, err := chart.ParseMode(override); err == nil {
		return m
	}
	return ctx.GraphMode
}

// windowSamples returns how many samples cover window at the context interval.
func windowSamples(ctx *Context, window time.Duration) int {
	if window <= 0 || ctx.Interval <= 0 {
		return 0
	}
	return int(window / ctx.Interval)
}

// CheckConfig validates what only the UI knows: theme names and widget
// options. It complements config.Config.Validate.
func CheckConfig(cfg *config.Config) error {
	if err := cfg.Validate(KnownWidget); err != nil {
		return err
	}
	if _, err := LookupTheme(cfg.General.Theme); err != nil {
		return fmt.Errorf("general.theme: %w", err)
	}
	for _, name := range cfg.Dashboards.Names {
		db, _ := cfg.Dashboards.Get(name)
		layouts := []*config.Node{db.Layout}
		for _, v := range db.Variants {
			layouts = append(layouts, v.Layout)
		}
		for _, l := range layouts {
			for _, n := range l.Widgets() {
				if _, err := newWidget(n); err != nil {
					return fmt.Errorf("dashboard %q: %w", name, err)
				}
			}
		}
	}
	return nil
}
