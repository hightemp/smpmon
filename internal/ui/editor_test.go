package ui

import (
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"

	"github.com/hightemp/smpmon/internal/collect/platform"
	"github.com/hightemp/smpmon/internal/config"
	"github.com/hightemp/smpmon/internal/layout"
)

func editModel(t *testing.T, savePath string) *Model {
	t.Helper()
	src := liveSource(t)
	m, err := New(Options{Config: config.Default(), Source: src, Procs: platform.ProcessController(), SavePath: savePath})
	if err != nil {
		t.Fatal(err)
	}
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 44})
	m.Update(key("e"))
	if m.edit == nil {
		t.Fatal("not in edit mode")
	}
	return m
}

func press(m *Model, keys ...string) {
	for _, k := range keys {
		m.Update(key(k))
	}
}

// pick chooses an entry in the open choose dialog by its first word.
func pick(t *testing.T, m *Model, name string) {
	t.Helper()
	d, ok := m.dialog.(*chooseDialog)
	if !ok {
		t.Fatalf("no choose dialog, got %T", m.dialog)
	}
	i := slices.IndexFunc(d.options, func(o string) bool { return strings.Fields(o)[0] == name })
	if i < 0 {
		t.Fatalf("%s not in %v", name, d.options)
	}
	d.sel = i
	m.Update(key("enter"))
}

func TestEditorOperations(t *testing.T) {
	m := editModel(t, "")
	e := m.edit
	if got := e.selected(); got == nil || got.Widget != "proc" {
		t.Fatalf("initial selection %+v", got)
	}
	countWidgets := func() int { return len(e.root().Widgets()) }
	before := countWidgets()

	// Split right with a gauge: proc's parent is a row, so it becomes a sibling.
	press(m, "s")
	pick(t, m, "gauge")
	if countWidgets() != before+1 || e.selected().Widget != "gauge" {
		t.Fatalf("split: %d widgets, selected %+v", countWidgets(), e.selected())
	}
	if _, ok := e.d.widgets[e.selected()]; !ok {
		t.Fatal("no widget instance for the new node")
	}
	checkFrame(t, "after split", viewLines(t, m), 140, 44)

	// Split below: creates a column around the gauge.
	press(m, "v")
	pick(t, m, "stat")
	parent := e.root().At(e.sel[:len(e.sel)-1])
	if parent.Dir != config.DirCol || len(parent.Children) != 2 || parent.Children[0].Widget != "gauge" {
		t.Fatalf("split below: %+v", parent)
	}

	// Undo twice, redo once.
	press(m, "u", "u")
	if countWidgets() != before {
		t.Fatalf("undo: %d widgets", countWidgets())
	}
	press(m, "U")
	if countWidgets() != before+1 {
		t.Fatalf("redo: %d widgets", countWidgets())
	}

	// Select the gauge again (redo restored it), delete it.
	for i, p := range layout.Compute(e.root(), m.layoutRect()) {
		if p.Node.Widget == "gauge" {
			e.sel = slices.Clone(p.Path)
			_ = i
		}
	}
	press(m, "d")
	if countWidgets() != before {
		t.Fatalf("delete: %d", countWidgets())
	}

	// Grow the selection: weight goes up.
	w0 := e.selected().Weight
	press(m, "+")
	if e.selected().Weight <= w0 {
		t.Fatalf("grow: %v -> %v", w0, e.selected().Weight)
	}

	// Move and rotate.
	sel := slices.Clone(e.sel)
	press(m, "<")
	if slices.Equal(sel, e.sel) && sel[len(sel)-1] != 0 {
		t.Fatal("move did nothing")
	}
	press(m, "[")
	dir := e.selected().Dir
	press(m, "r")
	if e.selected().Dir == dir {
		t.Fatal("rotate did nothing")
	}
	checkFrame(t, "after edits", viewLines(t, m), 140, 44)

	// The root can't be deleted.
	e.sel = nil
	press(m, "d")
	if !strings.Contains(m.status, "root") {
		t.Fatalf("status %q", m.status)
	}

	// Leaving keeps the changes in memory.
	press(m, "esc")
	if m.edit != nil || !strings.Contains(m.status, "unsaved") {
		t.Fatalf("leave: edit=%v status=%q", m.edit, m.status)
	}
	checkFrame(t, "after leaving", viewLines(t, m), 140, 44)
}

func TestEditorOptionsForm(t *testing.T) {
	m := editModel(t, "")
	e := m.edit
	press(m, "o")
	f, ok := m.dialog.(*formDialog)
	if !ok {
		t.Fatalf("dialog %T", m.dialog)
	}
	checkFrame(t, "form", viewLines(t, m), 140, 44)
	field := func(label string) int {
		i := slices.IndexFunc(f.fields, func(fl formField) bool { return fl.label == label })
		if i < 0 {
			t.Fatalf("no field %s", label)
		}
		return i
	}
	edit := func(label, value string) {
		f.sel = field(label)
		m.Update(key("enter"))
		in, ok := m.dialog.(*inputDialog)
		if !ok {
			t.Fatalf("%s: dialog %T", label, m.dialog)
		}
		in.value = value
		m.Update(key("enter"))
		if m.dialog != f {
			t.Fatalf("%s: form not reopened", label)
		}
	}
	edit("title", "Tasks")
	edit("columns", "pid, name, cpu")
	edit("sort.by", "mem")
	edit("size", "40%")
	n := e.selected()
	if n.Title != "Tasks" || n.Size.Value != 40 {
		t.Fatalf("node %+v", n)
	}
	w := e.d.widgets[n].(*procWidget)
	if len(w.cols) != 3 || w.sortBy != "mem" {
		t.Fatalf("widget not rebuilt: cols %d sort %s", len(w.cols), w.sortBy)
	}
	// Toggle a bool option.
	f.sel = field("tree")
	m.Update(key("enter"))
	if !e.d.widgets[e.selected()].(*procWidget).tree {
		t.Fatal("tree not toggled")
	}
	// An invalid value is rolled back.
	edit("sort.by", "bogus")
	if got := e.d.widgets[e.selected()].(*procWidget).sortBy; got != "mem" || !strings.Contains(m.status, "bogus") {
		t.Fatalf("invalid value applied: %s, status %q", got, m.status)
	}
	// Clearing a value removes it.
	edit("columns", "")
	if _, ok := getOption(e.selected(), []string{"columns"}); ok {
		t.Fatal("columns not removed")
	}
	press(m, "esc")
	if m.dialog != nil {
		t.Fatal("form not closed")
	}
}

func TestEditorSaveAndNewDashboard(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	m := editModel(t, path)
	press(m, "s")
	pick(t, m, "text")
	press(m, "ctrl+s")
	if !strings.Contains(m.status, "saved") || m.edit.dirty {
		t.Fatalf("save: %q", m.status)
	}
	cfg, err := config.Load(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckConfig(cfg); err != nil {
		t.Fatal(err)
	}
	db, _ := cfg.Dashboards.Get("btop")
	if !slices.ContainsFunc(db.Layout.Widgets(), func(n *config.Node) bool { return n.Widget == "text" }) {
		t.Fatal("saved layout lacks the new widget")
	}

	// New dashboard.
	press(m, "n")
	in := m.dialog.(*inputDialog)
	in.value = "mine"
	m.Update(key("enter"))
	if m.current().name != "mine" || m.edit == nil || m.edit.d.name != "mine" {
		t.Fatalf("new dashboard: %s", m.current().name)
	}
	press(m, "ctrl+s")
	cfg, err = config.Load(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := cfg.Dashboards.Get("mine"); !ok || !slices.Contains(cfg.Tabs(), "mine") {
		t.Fatalf("new dashboard not saved: %v", cfg.Tabs())
	}
	// Invalid names are rejected.
	press(m, "n")
	m.dialog.(*inputDialog).value = "Bad Name"
	_, cmd := m.Update(key("enter"))
	if cmd == nil || !strings.Contains(cmd().(statusMsg).text, "names") {
		t.Fatal("bad name accepted")
	}
}

func TestEditorMouseDrag(t *testing.T) {
	m := editModel(t, "")
	e := m.edit
	// btop: col [cpu size 35%, row [...]]. The border between them is the
	// bottom edge of cpu.
	var cpu layout.Placement
	for _, p := range layout.Compute(e.root(), m.layoutRect()) {
		if p.Node.Widget == "cpu" {
			cpu = p
		}
	}
	y := cpu.Rect.Y + cpu.Rect.H - 1
	m.Update(tea.MouseClickMsg{X: 10, Y: y, Button: tea.MouseLeft})
	if e.drag == nil {
		t.Fatal("no drag started on the border")
	}
	m.Update(tea.MouseMotionMsg{X: 10, Y: y + 5, Button: tea.MouseLeft})
	m.Update(tea.MouseReleaseMsg{X: 10, Y: y + 5, Button: tea.MouseLeft})
	if cpu.Node.Size.Kind != config.SizePercent || cpu.Node.Size.Value <= 35 {
		t.Fatalf("cpu size %+v", cpu.Node.Size)
	}
	if !e.dirty || len(e.undo) != 1 {
		t.Fatalf("dirty %v undo %d", e.dirty, len(e.undo))
	}
	// A click inside a widget selects it.
	m.Update(tea.MouseClickMsg{X: 5, Y: cpu.Rect.Y + 2, Button: tea.MouseLeft})
	if e.selected() != cpu.Node {
		t.Fatal("click did not select cpu")
	}
}
