package ui

import (
	"fmt"
	"math"
	"regexp"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"
	"go.yaml.in/yaml/v3"

	"github.com/hightemp/smpmon/internal/config"
	"github.com/hightemp/smpmon/internal/keymap"
	"github.com/hightemp/smpmon/internal/layout"
)

// editor is the state of the layout editor for one dashboard layout.
type editor struct {
	d     *dashboard
	slot  **config.Node // the edited layout: the default or a variant's
	sel   []int         // path of the selected node
	undo  []*config.Node
	redo  []*config.Node
	dirty bool
	drag  *dragState
}

// dragState tracks a border being dragged with the mouse.
type dragState struct {
	container []int // path of the container
	index     int   // border between children index and index+1
	pushed    bool  // undo snapshot taken
}

func (e *editor) root() *config.Node     { return *e.slot }
func (e *editor) selected() *config.Node { return e.root().At(e.sel) }

// snapshot saves the layout for undo before a change.
func (e *editor) snapshot() {
	e.undo = append(e.undo, e.root().Clone())
	if len(e.undo) > 100 {
		e.undo = e.undo[1:]
	}
	e.redo = nil
	e.dirty = true
}

// startEdit enters the layout editor on the current dashboard.
func (m *Model) startEdit() {
	d := m.current()
	slot := &d.cfg.Layout
	for i := range d.cfg.Variants {
		if d.cfg.Variants[i].Layout == d.cfg.LayoutFor(m.width, m.height-1) {
			slot = &d.cfg.Variants[i].Layout
			break
		}
	}
	e := &editor{d: d, slot: slot}
	m.edit, m.zoom = e, false
	m.relayout() // without pruning, so paths match the config tree
	if m.focus < len(d.places) {
		e.sel = slices.Clone(d.places[m.focus].Path)
	}
	where := "default layout"
	if slot != &d.cfg.Layout {
		where = "layout for this terminal size"
	}
	m.setStatus("editing the " + where + " of " + d.name)
}

func (m *Model) stopEdit() {
	if m.edit.dirty {
		m.setStatus("unsaved layout changes (e then ctrl+s to save)")
	}
	m.edit = nil
	m.relayout()
}

// apply runs a change to the layout with undo support. If the result can't
// be instantiated, the change is rolled back.
func (m *Model) apply(change func(e *editor) error) {
	e := m.edit
	e.snapshot()
	if err := change(e); err == nil {
		// Widgets of the changed subtree are re-created so new options and
		// types take effect (and are validated).
		if sel := e.selected(); sel != nil {
			for _, n := range sel.Widgets() {
				delete(e.d.widgets, n)
			}
		}
		err = m.syncWidgets(e.d)
		if err == nil {
			m.relayout()
			return
		}
		m.setStatus(err.Error())
	} else {
		m.setStatus(err.Error())
	}
	// Roll back.
	*e.slot = e.undo[len(e.undo)-1]
	e.undo = e.undo[:len(e.undo)-1]
	_ = m.syncWidgets(e.d)
	m.relayout()
}

// syncWidgets creates widgets for nodes that don't have one yet and drops
// widgets of removed nodes.
func (m *Model) syncWidgets(d *dashboard) error {
	live := map[*config.Node]bool{}
	layouts := []*config.Node{d.cfg.Layout}
	for _, v := range d.cfg.Variants {
		layouts = append(layouts, v.Layout)
	}
	for _, l := range layouts {
		for _, n := range l.Widgets() {
			live[n] = true
			if _, ok := d.widgets[n]; ok {
				continue
			}
			w, err := newWidget(n)
			if err != nil {
				return err
			}
			d.widgets[n] = w
		}
	}
	for n := range d.widgets {
		if !live[n] {
			delete(d.widgets, n)
		}
	}
	return nil
}

// newLeaf creates a widget node with the type's example options.
func newLeaf(kind string) *config.Node {
	n := &config.Node{Widget: kind, Weight: 1}
	if ex := metas[kind].example; ex != "" {
		_ = yaml.Unmarshal([]byte(ex), &n.Options)
		if n.Options.Kind == yaml.DocumentNode && len(n.Options.Content) > 0 {
			n.Options = *n.Options.Content[0]
		}
	}
	return n
}

// moveLayout copies size attributes from src to dst and resets src.
func moveLayout(dst, src *config.Node) {
	dst.Size, dst.Weight, dst.Min, dst.Max = src.Size, src.Weight, src.Min, src.Max
	src.Size, src.Weight, src.Min, src.Max = config.Size{}, 1, 0, 0
}

// split puts a new widget next to the selection in direction dir. If the
// parent already runs in that direction the widget becomes a sibling
// instead of creating a nested container.
func (m *Model) split(kind string, dir config.Direction) {
	m.apply(func(e *editor) error {
		leaf := newLeaf(kind)
		if len(e.sel) > 0 {
			parentPath := e.sel[:len(e.sel)-1]
			parent := e.root().At(parentPath)
			if parent.Dir == dir {
				i := e.sel[len(e.sel)-1] + 1
				parent.Children = slices.Insert(parent.Children, i, leaf)
				e.sel = append(slices.Clone(parentPath), i)
				return nil
			}
		}
		node := e.selected()
		wrapped := *node
		box := &config.Node{Dir: dir, Weight: 1}
		moveLayout(box, &wrapped)
		box.Children = []*config.Node{&wrapped, leaf}
		*node = *box
		e.sel = append(slices.Clone(e.sel), 1)
		return nil
	})
}

// addAfter inserts a widget after the selection, or as the last child of a
// selected container.
func (m *Model) addAfter(kind string) {
	sel := m.edit.selected()
	switch {
	case sel.Dir != config.DirNone:
		m.apply(func(e *editor) error {
			n := e.selected()
			n.Children = append(n.Children, newLeaf(kind))
			e.sel = append(slices.Clone(e.sel), len(n.Children)-1)
			return nil
		})
	case len(m.edit.sel) == 0:
		m.split(kind, config.DirRow)
	default:
		parent := m.edit.root().At(m.edit.sel[:len(m.edit.sel)-1])
		m.split(kind, parent.Dir)
	}
}

func (m *Model) deleteSelected() {
	if len(m.edit.sel) == 0 {
		m.setStatus("the root can't be deleted; change its type instead")
		return
	}
	m.apply(func(e *editor) error {
		parentPath := e.sel[:len(e.sel)-1]
		parent := e.root().At(parentPath)
		i := e.sel[len(e.sel)-1]
		parent.Children = slices.Delete(parent.Children, i, i+1)
		e.sel = slices.Clone(parentPath)
		if len(parent.Children) == 1 { // a container with one child is pointless
			only := parent.Children[0]
			moveLayout(only, parent)
			*parent = *only
			return nil
		}
		e.sel = append(e.sel, min(i, len(parent.Children)-1))
		return nil
	})
}

func (m *Model) moveSelected(delta int) {
	e := m.edit
	if len(e.sel) == 0 {
		return
	}
	parent := e.root().At(e.sel[:len(e.sel)-1])
	i := e.sel[len(e.sel)-1]
	j := i + delta
	if j < 0 || j >= len(parent.Children) {
		return
	}
	m.apply(func(e *editor) error {
		p := e.root().At(e.sel[:len(e.sel)-1])
		p.Children[i], p.Children[j] = p.Children[j], p.Children[i]
		e.sel[len(e.sel)-1] = j
		return nil
	})
}

// resize grows or shrinks the selection in its parent: weights by 25%,
// fixed sizes by one cell, percentages by 5 points.
func (m *Model) resize(dir float64) {
	m.apply(func(e *editor) error {
		n := e.selected()
		switch n.Size.Kind {
		case config.SizeFixed:
			n.Size.Value = max(n.Size.Value+dir, 1)
		case config.SizePercent:
			n.Size.Value = min(max(n.Size.Value+5*dir, 5), 95)
		default:
			n.Weight = max(math.Round((n.Weight*(1+0.25*dir))*100)/100, 0.1)
		}
		return nil
	})
}

func (m *Model) rotate() {
	m.apply(func(e *editor) error {
		path := e.sel
		if e.selected().Dir == config.DirNone {
			if len(path) == 0 {
				return fmt.Errorf("select a container to rotate")
			}
			path = path[:len(path)-1]
		}
		n := e.root().At(path)
		if n.Dir == config.DirRow {
			n.Dir = config.DirCol
		} else {
			n.Dir = config.DirRow
		}
		return nil
	})
}

func (m *Model) changeType(kind string) {
	m.apply(func(e *editor) error {
		n := e.selected()
		fresh := newLeaf(kind)
		n.Dir, n.Children, n.Widget, n.Options = config.DirNone, nil, kind, fresh.Options
		return nil
	})
}

func (m *Model) undoEdit(redo bool) {
	e := m.edit
	from, to := &e.undo, &e.redo
	if redo {
		from, to = &e.redo, &e.undo
	}
	if len(*from) == 0 {
		return
	}
	*to = append(*to, e.root().Clone())
	*e.slot = (*from)[len(*from)-1]
	*from = (*from)[:len(*from)-1]
	e.dirty = true
	for len(e.sel) > 0 && e.root().At(e.sel) == nil {
		e.sel = e.sel[:len(e.sel)-1]
	}
	_ = m.syncWidgets(e.d)
	m.relayout()
}

func (m *Model) saveEdit() {
	if m.opts.SavePath == "" {
		m.setStatus("no config path to save to")
		return
	}
	var tabs []string
	if len(m.cfg.General.Dashboards) > 0 {
		tabs = m.cfg.General.Dashboards
	}
	if err := config.SaveDashboard(m.opts.SavePath, m.edit.d.name, m.edit.d.cfg, tabs); err != nil {
		m.setStatus("save failed: " + err.Error())
		return
	}
	m.edit.dirty = false
	m.setStatus("saved to " + m.opts.SavePath)
}

var dashboardNameRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]*$`)

func (m *Model) newDashboard(name string) tea.Cmd {
	name = strings.TrimSpace(name)
	if !dashboardNameRe.MatchString(name) {
		return statusCmd("dashboard names use a-z, 0-9, - and _")
	}
	if _, exists := m.cfg.Dashboards.Get(name); exists {
		return statusCmd("dashboard " + name + " already exists")
	}
	dc := &config.Dashboard{Title: name, Layout: newLeaf("cpu")}
	m.cfg.Dashboards.Set(name, dc)
	if len(m.cfg.General.Dashboards) > 0 {
		m.cfg.General.Dashboards = append(m.cfg.General.Dashboards, name)
	}
	d := &dashboard{name: name, title: name, cfg: dc, widgets: map[*config.Node]Widget{}}
	_ = m.syncWidgets(d)
	m.dashboards = append(m.dashboards, d)
	m.active, m.focus = len(m.dashboards)-1, 0
	m.startEdit()
	m.edit.dirty = true
	return statusCmd("new dashboard " + name + ": s/v split, t change type, ctrl+s save")
}

func (m *Model) widgetPalette(title string, pick func(kind string)) dialog {
	names := WidgetNames()
	opts := make([]string, len(names))
	for i, n := range names {
		opts[i] = fmt.Sprintf("%-8s %s", n, metas[n].help)
	}
	return &chooseDialog{heading: title, options: opts, onPick: func(i int) tea.Cmd {
		pick(names[i])
		return nil
	}}
}

// handleEditKey processes keys in edit mode.
func (m *Model) handleEditKey(k tea.KeyPressMsg) tea.Cmd {
	e := m.edit
	switch m.ctx.action(k, keymap.Nav, keymap.Editor, keymap.Global) {
	case "back", "edit":
		m.stopEdit()
	case "quit":
		if e.dirty {
			m.setStatus("unsaved layout changes: ctrl+s saves, esc leaves the editor")
			return nil
		}
		return m.quit()
	case "help":
		m.dialog = m.editHelp()
	case "up":
		m.selectNeighbor(layout.Up)
	case "down":
		m.selectNeighbor(layout.Down)
	case "left":
		m.selectNeighbor(layout.Left)
	case "right":
		m.selectNeighbor(layout.Right)
	case "edit_parent":
		if len(e.sel) > 0 {
			e.sel = e.sel[:len(e.sel)-1]
		}
	case "edit_child":
		if n := e.selected(); len(n.Children) > 0 {
			e.sel = append(e.sel, 0)
		}
	case "edit_split_h":
		m.dialog = m.widgetPalette("Add to the right", func(kind string) { m.split(kind, config.DirRow) })
	case "edit_split_v":
		m.dialog = m.widgetPalette("Add below", func(kind string) { m.split(kind, config.DirCol) })
	case "edit_add":
		m.dialog = m.widgetPalette("Add after", m.addAfter)
	case "edit_delete":
		m.deleteSelected()
	case "edit_move_prev":
		m.moveSelected(-1)
	case "edit_move_next":
		m.moveSelected(1)
	case "edit_grow":
		m.resize(1)
	case "edit_shrink":
		m.resize(-1)
	case "edit_rotate":
		m.rotate()
	case "edit_type":
		m.dialog = m.widgetPalette("Widget type", m.changeType)
	case "edit_options":
		m.dialog = m.optionsForm()
	case "edit_undo":
		m.undoEdit(false)
	case "edit_redo":
		m.undoEdit(true)
	case "edit_save":
		m.saveEdit()
	case "edit_new":
		m.dialog = &inputDialog{prompt: "Name of the new dashboard:", onSubmit: m.newDashboard}
	case "edit_title":
		d := e.d
		m.dialog = &inputDialog{prompt: "Dashboard title:", value: d.title, onSubmit: func(s string) tea.Cmd {
			s = strings.TrimSpace(s)
			if s == "" {
				return statusCmd("the title can't be empty")
			}
			d.title, d.cfg.Title, e.dirty = s, s, true
			return nil
		}}
	}
	return nil
}

// selectNeighbor moves the selection to the adjacent widget.
func (m *Model) selectNeighbor(dir layout.Direction) {
	e := m.edit
	all := layout.ComputeAll(e.root(), m.layoutRect())
	var from layout.Rect
	for _, p := range all {
		if slices.Equal(p.Path, e.sel) {
			from = p.Rect
		}
	}
	leaves := layout.Compute(e.root(), m.layoutRect())
	if i := layout.Neighbor(leaves, from, dir); i >= 0 {
		e.sel = slices.Clone(leaves[i].Path)
	}
}

func (m *Model) layoutRect() layout.Rect {
	return layout.Rect{X: 0, Y: 1, W: m.width, H: m.height - 1}
}

// inSelection reports whether the leaf at path is the selection or inside it.
func (e *editor) inSelection(path []int) bool {
	return len(path) >= len(e.sel) && slices.Equal(path[:len(e.sel)], e.sel)
}

// describe returns the selection as "col › row › proc" with its size.
func (e *editor) describe() string {
	var parts []string
	n := e.root()
	name := func(n *config.Node) string {
		switch n.Dir {
		case config.DirRow:
			return "row"
		case config.DirCol:
			return "col"
		}
		return n.Widget
	}
	parts = append(parts, name(n))
	for _, i := range e.sel {
		n = n.Children[i]
		parts = append(parts, name(n))
	}
	size := "weight " + strconv.FormatFloat(n.Weight, 'g', 4, 64)
	switch n.Size.Kind {
	case config.SizeFixed:
		size = fmt.Sprintf("size %d", int(n.Size.Value))
	case config.SizePercent:
		size = "size " + strconv.FormatFloat(n.Size.Value, 'g', 4, 64) + "%"
	}
	return strings.Join(parts, " › ") + "  (" + size + ")"
}

func (m *Model) editBar() string {
	t := m.ctx.Theme
	e := m.edit
	name := e.d.name
	if e.dirty {
		name += "*"
	}
	left := bg(t.Hotkey, fg(t.SelectedFg, bold(" EDIT "))) + " " + fg(t.Title, name) + fg(t.Dim, "  ") + fg(t.Fg, e.describe())
	k := m.ctx.Keys
	hints := joinHints(
		keyHint(&m.ctx, "edit_split_h", "split→"), keyHint(&m.ctx, "edit_split_v", "split↓"),
		keyHint(&m.ctx, "edit_delete", "del"), keyHint(&m.ctx, "edit_options", "options"),
		hint(&m.ctx, k.First("edit_save"), "save"), hint(&m.ctx, k.First("help"), "more"),
	)
	if status := m.statusText(); status != "" {
		hints = fg(t.Warn, status)
	}
	return spread(left, hints, m.width)
}

func (m *Model) editHelp() dialog {
	sec := helpSection{name: "Layout editor"}
	sec.keys = append(sec.keys, [2]string{"←↑→↓", "select the neighbouring widget"})
	for _, i := range keymap.Actions {
		if i.Scope == keymap.Editor {
			if l := m.ctx.Keys.Label(i.Action); l != "" {
				sec.keys = append(sec.keys, [2]string{l, i.Help})
			}
		}
	}
	sec.keys = append(sec.keys, [2]string{m.ctx.Keys.Label("back"), "leave the editor"},
		[2]string{"mouse", "click to select, drag a border to resize"})
	return &helpDialog{sections: []helpSection{sec}}
}

// ---- mouse ----

// borderAt finds the border between two siblings at (x, y): the right or
// bottom edge of child i or the left or top edge of child i+1. The
// innermost container wins.
func (m *Model) borderAt(x, y int) *dragState {
	var best *dragState
	bestDepth := -1
	for _, p := range layout.ComputeAll(m.edit.root(), m.layoutRect()) {
		n := p.Node
		if n.Dir == config.DirNone || !p.Rect.Contains(x, y) || len(p.Path) <= bestDepth {
			continue
		}
		pos := p.Rect.X
		coord := x
		if n.Dir == config.DirCol {
			pos, coord = p.Rect.Y, y
		}
		sizes := childSizesOf(n, p.Rect)
		for i := 0; i < len(sizes)-1; i++ {
			pos += sizes[i]
			if coord == pos-1 || coord == pos {
				best, bestDepth = &dragState{container: slices.Clone(p.Path), index: i}, len(p.Path)
				break
			}
		}
	}
	return best
}

func childSizesOf(n *config.Node, r layout.Rect) []int {
	total := r.W
	if n.Dir == config.DirCol {
		total = r.H
	}
	items := make([]layout.Item, len(n.Children))
	for i, c := range n.Children {
		items[i] = layout.Item{Size: c.Size, Weight: c.Weight, Min: c.Min, Max: c.Max}
	}
	return layout.Split(total, items)
}

// dragTo moves the dragged border so that child index ends at coordinate c.
func (m *Model) dragTo(x, y int) {
	e := m.edit
	ds := e.drag
	var rect layout.Rect
	for _, p := range layout.ComputeAll(e.root(), m.layoutRect()) {
		if slices.Equal(p.Path, ds.container) {
			rect = p.Rect
		}
	}
	n := e.root().At(ds.container)
	if n == nil || ds.index+1 >= len(n.Children) {
		return
	}
	sizes := childSizesOf(n, rect)
	start, total, coord := rect.X, rect.W, x
	if n.Dir == config.DirCol {
		start, total, coord = rect.Y, rect.H, y
	}
	for i := 0; i < ds.index; i++ {
		start += sizes[i]
	}
	pair := sizes[ds.index] + sizes[ds.index+1]
	first := min(max(coord-start+1, 3), pair-3)
	if pair < 6 || first == sizes[ds.index] {
		return
	}
	if !ds.pushed {
		e.snapshot()
		ds.pushed = true
	}
	a, b := n.Children[ds.index], n.Children[ds.index+1]
	setCells := func(c *config.Node, cells int) {
		switch c.Size.Kind {
		case config.SizeFixed:
			c.Size.Value = float64(cells)
		case config.SizePercent:
			c.Size.Value = math.Round(float64(cells)/float64(total)*1000) / 10
		}
	}
	if a.Size.Kind == config.SizeAuto && b.Size.Kind == config.SizeAuto {
		sum := a.Weight + b.Weight
		a.Weight = math.Round(sum*float64(first)/float64(pair)*100) / 100
		b.Weight = math.Round((sum-a.Weight)*100) / 100
	} else {
		setCells(a, first)
		setCells(b, pair-first)
	}
	e.dirty = true
	m.relayout()
}

func (m *Model) handleEditMouse(msg tea.MouseMsg) tea.Cmd {
	e := m.edit
	mo := msg.Mouse()
	switch msg.(type) {
	case tea.MouseClickMsg:
		if mo.Button != tea.MouseLeft {
			return nil
		}
		if ds := m.borderAt(mo.X, mo.Y); ds != nil {
			e.drag = ds
			return nil
		}
		for _, p := range layout.Compute(e.root(), m.layoutRect()) {
			if p.Rect.Contains(mo.X, mo.Y) {
				e.sel = slices.Clone(p.Path)
			}
		}
	case tea.MouseMotionMsg:
		if e.drag != nil {
			m.dragTo(mo.X, mo.Y)
		}
	case tea.MouseReleaseMsg:
		e.drag = nil
	}
	return nil
}
