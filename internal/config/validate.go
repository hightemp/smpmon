package config

import (
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/hightemp/smpmon/internal/chart"
	"github.com/hightemp/smpmon/internal/keymap"
	"github.com/hightemp/smpmon/internal/metrics"
)

// ColorModes lists the valid values of general.color.
var ColorModes = []string{"auto", "truecolor", "256", "16", "none"}

// BorderStyles lists the valid values of general.border.
var BorderStyles = []string{"rounded", "single", "double", "thick", "none"}

// Validate checks semantic rules that decoding cannot. knownWidget reports
// whether a widget type exists; nil skips that check. All problems are
// returned together.
func (c *Config) Validate(knownWidget func(string) bool) error {
	var errs []error
	add := func(format string, args ...any) { errs = append(errs, fmt.Errorf(format, args...)) }

	g := c.General
	if g.Interval < MinInterval {
		add("general.interval: %v is below the minimum of %v", g.Interval, MinInterval)
	}
	if _, err := chart.ParseMode(g.GraphMode); err != nil {
		add("general.graph_mode: %v", err)
	}
	if g.Color != "" && !slices.Contains(ColorModes, g.Color) {
		add("general.color: unknown mode %q (want one of %v)", g.Color, ColorModes)
	}
	if !slices.Contains(BorderStyles, g.Border) {
		add("general.border: unknown style %q (want one of %v)", g.Border, BorderStyles)
	}
	if _, err := keymap.Build(c.Keys.Preset, c.Keys.Bind); err != nil {
		add("keys: %w", err)
	}
	if h := c.History; h.Enabled {
		for _, m := range h.Metrics {
			if _, err := metricsSelector(m); err != nil {
				add("history.metrics: %v", err)
			}
		}
		if h.MaxSizeMB < 0 {
			add("history.max_size_mb must not be negative")
		}
	}
	for _, name := range c.HostNames() {
		h := c.Hosts[name]
		switch {
		case name == "local":
			add("hosts: %q is reserved for this machine", name)
		case (h.SSH == "") == (h.URL == ""):
			add("hosts.%s: set exactly one of ssh or url", name)
		case h.URL != "" && !strings.HasPrefix(h.URL, "tls://") && !strings.HasPrefix(h.URL, "tcp://"):
			add("hosts.%s.url: want tls://host:port", name)
		case h.URL != "" && h.TokenFile == "":
			add("hosts.%s: token_file is required for network agents", name)
		}
	}
	for name, cc := range c.Collectors {
		if cc.Interval != 0 && cc.Interval < MinInterval {
			add("collectors.%s.interval: %v is below the minimum of %v", name, cc.Interval, MinInterval)
		}
	}

	if len(c.Dashboards.Names) == 0 {
		add("dashboards: at least one dashboard is required")
	}
	for _, name := range c.General.Dashboards {
		if _, ok := c.Dashboards.Get(name); !ok {
			add("general.dashboards: unknown dashboard %q", name)
		}
	}
	if g.DefaultDashboard != "" && !slices.Contains(c.Tabs(), g.DefaultDashboard) {
		add("general.default_dashboard: %q is not one of the shown dashboards %v", g.DefaultDashboard, c.Tabs())
	}
	for _, name := range c.Dashboards.Names {
		db := c.Dashboards.ByName[name]
		layouts := []*Node{db.Layout}
		for _, v := range db.Variants {
			layouts = append(layouts, v.Layout)
		}
		for _, l := range layouts {
			for _, w := range l.Widgets() {
				if knownWidget != nil && !knownWidget(w.Widget) {
					add("dashboard %q, line %d: unknown widget %q", name, w.Line, w.Widget)
				}
				if w.Source != "" && w.Source != "local" {
					if _, ok := c.Hosts[w.Source]; !ok {
						add("dashboard %q, line %d: source %q is not in hosts", name, w.Line, w.Source)
					}
				}
				if w.Size.Kind == SizePercent && w.Size.Value == 0 {
					add("dashboard %q, line %d: size 0%% hides the widget", name, w.Line)
				}
			}
		}
	}
	return errors.Join(errs...)
}

func metricsSelector(s string) (metrics.Selector, error) { return metrics.ParseSelector(s) }
