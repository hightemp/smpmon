package ui

import (
	"fmt"
	"time"

	"github.com/hightemp/smpmon/internal/alert"
	"github.com/hightemp/smpmon/internal/chart"
	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/config"
	"github.com/hightemp/smpmon/internal/metrics"
)

// alertsWidget lists firing alerts and recent events.
type alertsWidget struct{}

func init() {
	register("alerts", func(n *config.Node) (Widget, error) {
		return &alertsWidget{}, n.DecodeOptions(&struct{}{})
	})
}

func (a *alertsWidget) Kind() string         { return "alerts" }
func (a *alertsWidget) Collectors() []string { return []string{} }

func (a *alertsWidget) Title(ctx *Context) (string, string) {
	if ctx.Alerts == nil {
		return "alerts", ""
	}
	n := len(ctx.Alerts.Active())
	if n == 0 {
		return "alerts", fg(ctx.Theme.Dim, "all clear")
	}
	return "alerts", fg(ctx.Theme.Crit, fmt.Sprintf("%d firing", n))
}

func levelColor(t *Theme, l alert.Level) chart.RGB {
	if l == alert.Crit {
		return t.Crit
	}
	return t.Warn
}

func (a *alertsWidget) Render(ctx *Context, w, h int) []string {
	t := ctx.Theme
	if ctx.Alerts == nil {
		return block([]string{fg(t.Dim, "no alert rules: add them under alerts: in the config")}, w, h)
	}
	var out []string
	for _, act := range ctx.Alerts.Active() {
		unit := metrics.DefaultRegistry.UnitOf(act.Key)
		left := fg(levelColor(t, act.Rule.Level), bold(fmt.Sprintf("%-4s", act.Rule.Level))) + " " +
			fg(t.Title, act.Rule.Name) + fg(t.Dim, " "+act.Host+" ") + fg(t.Fg, string(act.Key))
		right := fg(levelColor(t, act.Rule.Level), metrics.Format(act.Value, unit)) +
			fg(t.Dim, " for "+metrics.FormatDuration(time.Since(act.Since).Seconds()))
		out = append(out, spread(left, right, w))
	}
	if len(out) == 0 {
		out = append(out, fg(t.CPU.At(0), "✓ no alerts firing"))
	}
	if room := h - len(out) - 2; room > 0 {
		out = append(out, "", fg(t.Title, "Recent"))
		for _, ev := range ctx.Alerts.Recent(room) {
			color := levelColor(t, ev.Rule.Level)
			if ev.Resolved {
				color = t.Dim
			}
			out = append(out, fg(t.Dim, ev.Time.Format("15:04:05")+" ")+fg(color, ev.Summary()))
		}
	}
	return block(out, w, h)
}

// alertedCollectors returns, per host, the collectors behind firing alerts
// and the worst level, so widgets showing that data can be highlighted.
func alertedCollectors(eng *alert.Engine) map[string]map[string]alert.Level {
	if eng == nil {
		return nil
	}
	out := map[string]map[string]alert.Level{}
	for _, act := range eng.Active() {
		if out[act.Host] == nil {
			out[act.Host] = map[string]alert.Level{}
		}
		for _, c := range collect.CollectorsFor([]string{string(act.Key)}) {
			if out[act.Host][c] != alert.Crit {
				out[act.Host][c] = act.Rule.Level
			}
		}
	}
	return out
}

// alertLevelFor returns the alert level for a widget on host, if any.
func alertLevelFor(alerted map[string]map[string]alert.Level, host string, w Widget) (alert.Level, bool) {
	cs := alerted[host]
	if len(cs) == 0 {
		return "", false
	}
	var lvl alert.Level
	found := false
	for _, c := range w.Collectors() {
		if l, ok := cs[c]; ok && (!found || l == alert.Crit) {
			lvl, found = l, true
		}
	}
	return lvl, found
}
