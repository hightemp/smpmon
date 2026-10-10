package ui

import (
	"fmt"
	"slices"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/hightemp/smpmon/internal/chart"
	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/config"
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
func collectorsFor(patterns []string) []string {
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
			if strings.HasPrefix(p, "cpu.temp") {
				add("sensors")
			}
		case "sensors":
			add("sensors")
		case "gpu":
			add("gpu")
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
