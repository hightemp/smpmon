package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/hightemp/smpmon/internal/alert"
	"github.com/hightemp/smpmon/internal/collect/platform"
	"github.com/hightemp/smpmon/internal/config"
)

func TestAlertsInUI(t *testing.T) {
	src := liveSource(t)
	cfg, err := config.Parse([]byte(`
alerts:
  - name: always
    expr: "cpu.total.usage >= 0"
    level: crit
general:
  dashboards: [btop, watch]
dashboards:
  watch:
    layout:
      widget: alerts
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckConfig(cfg); err != nil {
		t.Fatal(err)
	}
	rules, _ := alert.Compile(cfg.Alerts)
	eng := alert.NewEngine(rules, nil)
	eng.AddHost("local", src.Store())
	m, err := New(Options{Config: cfg, Source: src, Procs: platform.ProcessController(), Alerts: eng})
	if err != nil {
		t.Fatal(err)
	}
	m.Update(tea.WindowSizeMsg{Width: 120, Height: 40})
	eng.Evaluate(time.Now())
	m.Update(heartbeatMsg{})
	if !strings.Contains(m.status, "always") {
		t.Fatalf("no toast: %q", m.status)
	}
	lines := viewLines(t, m)
	checkFrame(t, "alert btop", lines, 120, 40)
	if !strings.Contains(ansi.Strip(lines[0]), "⚠ 1") {
		t.Fatalf("no badge: %q", ansi.Strip(lines[0]))
	}
	// The cpu widget's border uses the critical color.
	if !strings.Contains(lines[1], "38;2;"+rgbParams(m.ctx.Theme.Crit)) {
		t.Fatal("cpu widget not highlighted")
	}
	m.Update(key("2"))
	frame := ansi.Strip(strings.Join(viewLines(t, m), "\n"))
	if !strings.Contains(frame, "always") || !strings.Contains(frame, "1 firing") || !strings.Contains(frame, "Recent") {
		t.Fatalf("alerts widget:\n%s", frame)
	}

	// Bad rules are reported by CheckConfig.
	bad, _ := config.Parse([]byte("alerts:\n  - name: x\n    expr: nonsense\n"))
	if err := CheckConfig(bad); err == nil {
		t.Fatal("bad rule accepted")
	}
}
