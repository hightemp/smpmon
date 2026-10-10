package ui

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"go.yaml.in/yaml/v3"

	"github.com/hightemp/smpmon/internal/collect/platform"
	"github.com/hightemp/smpmon/internal/config"
	"github.com/hightemp/smpmon/internal/metrics"
	"github.com/hightemp/smpmon/internal/source"
)

// liveSource collects real metrics for a moment so widgets have data.
func liveSource(t *testing.T) *source.Local {
	t.Helper()
	cs := platform.Collectors()
	if len(cs) == 0 {
		t.Skip("no collectors on this platform")
	}
	src := source.NewLocal(source.LocalOptions{Interval: 200 * time.Millisecond, Store: metrics.DefaultStoreOptions()}, cs)
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	go func() { _ = src.Run(ctx) }()
	deadline := time.Now().Add(5 * time.Second)
	for !src.Store().Has("cpu.total.usage") || !src.Store().Has("proc.count") {
		if time.Now().After(deadline) {
			t.Fatal("no data collected")
		}
		time.Sleep(50 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)
	return src
}

func newTestModel(t *testing.T, src source.Source, w, h int) *Model {
	t.Helper()
	m, err := New(Options{Config: config.Default(), Source: src, Procs: platform.ProcessController()})
	if err != nil {
		t.Fatal(err)
	}
	m.Update(tea.WindowSizeMsg{Width: w, Height: h})
	return m
}

func viewLines(t *testing.T, m *Model) []string {
	t.Helper()
	return strings.Split(m.View().Content, "\n")
}

func checkFrame(t *testing.T, name string, lines []string, w, h int) {
	t.Helper()
	if len(lines) != h {
		t.Errorf("%s: %d lines, want %d", name, len(lines), h)
	}
	for i, l := range lines {
		if got := ansi.StringWidth(l); got != w {
			t.Errorf("%s: line %d is %d cells wide, want %d: %q", name, i, got, w, ansi.Strip(l))
			return
		}
	}
	if os.Getenv("SMPMON_DUMP") != "" {
		fmt.Printf("===== %s (%dx%d)\n", name, w, h)
		for _, l := range lines {
			fmt.Println(ansi.Strip(l))
		}
	}
}

func key(s string) tea.KeyPressMsg {
	switch s {
	case "tab":
		return tea.KeyPressMsg{Code: tea.KeyTab}
	case "enter":
		return tea.KeyPressMsg{Code: tea.KeyEnter}
	case "esc":
		return tea.KeyPressMsg{Code: tea.KeyEscape}
	case "down":
		return tea.KeyPressMsg{Code: tea.KeyDown}
	case "right":
		return tea.KeyPressMsg{Code: tea.KeyRight}
	}
	r := []rune(s)[0]
	return tea.KeyPressMsg{Code: r, Text: s}
}

func TestDashboardsRender(t *testing.T) {
	src := liveSource(t)
	for _, size := range [][2]int{{80, 24}, {120, 40}, {200, 55}} {
		m := newTestModel(t, src, size[0], size[1])
		for i, name := range m.dashboardNames() {
			m.Update(key(fmt.Sprint(i + 1)))
			checkFrame(t, fmt.Sprintf("%s@%dx%d", name, size[0], size[1]), viewLines(t, m), size[0], size[1])
		}
	}
}

func TestInteractions(t *testing.T) {
	src := liveSource(t)
	m := newTestModel(t, src, 140, 45)
	if m.focusedWidget().Kind() != "proc" {
		t.Fatalf("default focus on %s", m.focusedWidget().Kind())
	}
	steps := []struct {
		name string
		keys []string
	}{
		{"select+details", []string{"down", "down", "enter"}},
		{"sort+tree", []string{"esc", "right", "r", "t"}},
		{"filter", []string{"f", "s", "h", "enter"}},
		{"kill dialog", []string{"k"}},
		{"kill cancelled", []string{"n"}},
		{"signal dialog", []string{"s"}},
		{"signal cancelled", []string{"q"}},
		{"zoom", []string{"z"}},
		{"unzoom+help", []string{"esc", "?"}},
		{"help closed+graph mode", []string{"x", "g"}},
		{"focus next", []string{"tab"}},
	}
	for _, s := range steps {
		for _, k := range s.keys {
			_, cmd := m.Update(key(k))
			// Run commands that open dialogs or set the status synchronously.
			if cmd != nil {
				if msg := cmd(); msg != nil {
					switch msg.(type) {
					case openDialogMsg, statusMsg:
						m.Update(msg)
					}
				}
			}
		}
		checkFrame(t, s.name, viewLines(t, m), 140, 45)
	}
	if m.dialog != nil {
		t.Fatal("dialog still open")
	}
	w := m.current().widgets[m.current().places[m.defaultFocus()].Node].(*procWidget)
	if w.filter != "sh" || !w.tree || w.desc {
		t.Fatalf("proc state filter=%q tree=%v desc=%v", w.filter, w.tree, w.desc)
	}
}

func TestMouse(t *testing.T) {
	src := liveSource(t)
	m := newTestModel(t, src, 120, 40)
	// Clicking the second tab switches dashboards.
	x := width(m.tabLabels()[0]) + 1
	m.Update(tea.MouseClickMsg{X: x, Y: 0, Button: tea.MouseLeft})
	if m.active != 1 {
		t.Fatalf("active %d", m.active)
	}
	// Clicking inside a widget focuses it.
	m.Update(key("1"))
	p := m.current().places[0]
	m.Update(tea.MouseClickMsg{X: p.Rect.X + 2, Y: p.Rect.Y + 2, Button: tea.MouseLeft})
	if m.focus != 0 {
		t.Fatalf("focus %d", m.focus)
	}
}

func TestEveryWidgetTiny(t *testing.T) {
	src := liveSource(t)
	ctx := &Context{Store: src.Store(), Registry: metrics.DefaultRegistry, Theme: themes["default"], Interval: time.Second, NumCPU: 4, Now: time.Now()}
	for _, name := range WidgetNames() {
		var n config.Node
		n.Widget = name
		switch name {
		case "graph", "table":
			n.Options.Kind = 0
		}
		opts := map[string]string{
			"graph": "metrics: [cpu.total.usage, mem.used_percent]",
			"table": "metrics: ['mem.*']",
			"gauge": "metric: cpu.total.usage",
			"stat":  "metric: mem.used",
		}
		if o, ok := opts[name]; ok {
			if err := yamlNode(o, &n); err != nil {
				t.Fatal(err)
			}
		}
		w, err := newWidget(&n)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		for _, size := range [][2]int{{1, 1}, {5, 2}, {20, 3}, {40, 10}} {
			lines := w.Render(ctx, size[0], size[1])
			checkFrame(t, fmt.Sprintf("%s@%v", name, size), lines, size[0], size[1])
		}
	}
}

func yamlNode(src string, n *config.Node) error {
	return yaml.Unmarshal([]byte(src), &n.Options)
}

func TestWidth(t *testing.T) {
	cases := []string{
		"plain", fg(hex("#ff0000"), "red") + " x", "╭─ cpu ─╮ ⣿⡇ ▁▂█ ■ ↑↓ é", "日本語", "emoji 👍 ok",
		bg(hex("#000000"), "a") + "\x1b[1mb\x1b[22m", "",
	}
	for _, c := range cases {
		if got, want := width(c), ansi.StringWidth(c); got != want {
			t.Errorf("width(%q) = %d, want %d", c, got, want)
		}
	}
}

func TestTopBarDegrades(t *testing.T) {
	src := liveSource(t)
	m := newTestModel(t, src, 90, 30)
	m.hostname = strings.Repeat("h", 40)
	m.setStatus("a long status message that will not fit next to the tabs")
	m.ctx.Now = time.Now()
	bar := ansi.Strip(m.topBar())
	if width(bar) != 90 {
		t.Fatalf("width %d: %q", width(bar), bar)
	}
	if !strings.Contains(bar, m.ctx.Now.Format("15:04")) || !strings.Contains(bar, "a long") {
		t.Fatalf("clock or status missing: %q", bar)
	}
	m.setStatus("")
	m.hostname = "box"
	if bar := ansi.Strip(m.topBar()); !strings.Contains(bar, "help") || !strings.Contains(bar, "box") {
		t.Fatalf("help should fit without status: %q", bar)
	}
}

func TestPruneUnsupported(t *testing.T) {
	var root config.Node
	if err := yaml.Unmarshal([]byte("row:\n  - widget: battery\n  - col:\n      - widget: gpu\n      - widget: cpu\n"), &root); err != nil {
		t.Fatal(err)
	}
	widgets := map[*config.Node]Widget{}
	for _, n := range root.Widgets() {
		w, err := newWidget(n)
		if err != nil {
			t.Fatal(err)
		}
		widgets[n] = w
	}
	p := pruneUnsupported(&root, widgets, map[string]bool{"battery": true, "gpu": true})
	got := p.Widgets()
	if len(got) != 1 || got[0].Widget != "cpu" {
		t.Fatalf("kept %v", got)
	}
	if len(root.Widgets()) != 3 {
		t.Fatal("original layout modified")
	}
	// cpu also uses sensors; it stays while cpu itself is supported.
	if p := pruneUnsupported(&root, widgets, map[string]bool{"sensors": true}); len(p.Widgets()) != 3 {
		t.Fatal("widget with a supported collector was removed")
	}
	if pruneUnsupported(&root, widgets, map[string]bool{"battery": true, "gpu": true, "cpu": true, "load": true, "sensors": true}) != nil {
		t.Fatal("expected nil when nothing is left")
	}
}

func TestUserThemes(t *testing.T) {
	dir := t.TempDir()
	write := func(name, data string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(data), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("ocean.yaml", "inherit: nord\ncolors:\n  fg: \"#112233\"\ngradients:\n  cpu: [\"#000000\", \"#ffffff\"]\nboxes:\n  proc: \"#abcdef\"\n")
	write("deep.yaml", "inherit: ocean\ncolors:\n  dim: \"#010101\"\n")
	write("broken.yaml", "colours:\n  fg: red\n")
	t.Cleanup(func() { _ = LoadUserThemes(t.TempDir()) })

	err := LoadUserThemes(dir)
	if err == nil || !strings.Contains(err.Error(), "broken.yaml") || !strings.Contains(err.Error(), "line 1") {
		t.Fatalf("expected error for broken.yaml with a line number, got %v", err)
	}
	deep, err := LookupTheme("deep")
	if err != nil {
		t.Fatal(err)
	}
	nord := themes["nord"]
	if deep.Fg != hex("#112233") || deep.Dim != hex("#010101") || deep.Title != nord.Title ||
		deep.CPU.At(1) != hex("#ffffff") || deep.BoxColor("proc") != hex("#abcdef") {
		t.Fatalf("deep %+v", deep)
	}
	if nord.Fg == hex("#112233") || nord.Boxes["proc"] == hex("#abcdef") {
		t.Fatal("built-in theme was modified")
	}
	if !slices.Contains(ThemeNames(), "deep") {
		t.Fatal("user theme not listed")
	}

	// Export round trip.
	data, err := ExportTheme(themes["gruvbox"])
	if err != nil {
		t.Fatal(err)
	}
	write2 := t.TempDir()
	if err := os.WriteFile(filepath.Join(write2, "copy.yaml"), data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := LoadUserThemes(write2); err != nil {
		t.Fatal(err)
	}
	cp, err := LookupTheme("copy")
	if err != nil {
		t.Fatal(err)
	}
	if cp.Fg != themes["gruvbox"].Fg || len(cp.CPU) != len(themes["gruvbox"].CPU) {
		t.Fatal("export round trip changed the theme")
	}

	for _, bad := range []string{
		"colors:\n  fgg: \"#fff\"\n",
		"colors:\n  fg: nothex\n",
		"inherit: nope\n",
		"inherit: self\n",
		"gradients:\n  cpu: []\n",
	} {
		d := t.TempDir()
		if err := os.WriteFile(filepath.Join(d, "self.yaml"), []byte(bad), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := LoadUserThemes(d); err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
}

func TestThemeDialog(t *testing.T) {
	src := liveSource(t)
	m := newTestModel(t, src, 100, 30)
	m.Update(key("T"))
	if m.dialog == nil {
		t.Fatal("no dialog")
	}
	_, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	_, cmd = m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("no command")
	}
	m.Update(cmd())
	if m.ctx.Theme.Name == "default" {
		t.Fatal("theme not changed")
	}
	checkFrame(t, "after theme change", viewLines(t, m), 100, 30)
}
