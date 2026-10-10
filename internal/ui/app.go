// Package ui implements the terminal user interface.
package ui

import (
	"cmp"
	"context"
	"fmt"
	"os"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"

	"github.com/hightemp/smpmon/internal/chart"
	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/config"
	"github.com/hightemp/smpmon/internal/layout"
	"github.com/hightemp/smpmon/internal/metrics"
	"github.com/hightemp/smpmon/internal/source"
)

// Options configures the UI.
type Options struct {
	Config     *config.Config
	ConfigPath string // watched for changes when set
	Source     source.Source
	Procs      collect.ProcessController
	Dashboard  string // dashboard to open; empty uses the config default
}

// dashboard is an instantiated config dashboard.
type dashboard struct {
	name    string
	title   string
	cfg     *config.Dashboard
	widgets map[*config.Node]Widget
	root    *config.Node // layout for the current terminal size
	places  []layout.Placement
}

// Model is the root Bubble Tea model.
type Model struct {
	opts   Options
	cfg    *config.Config
	ctx    Context
	border string

	dashboards []*dashboard
	active     int
	focus      int // index into the active dashboard's placements
	zoom       bool
	paused     bool
	frozen     string // last frame while paused

	dialog   dialog
	status   string
	statusAt time.Time

	width, height int
	storeCh       <-chan struct{}
	configCh      chan struct{}
	unsupSig      string // unsupported collectors at the last layout
	cancelWatch   context.CancelFunc
	hostname      string
}

type storeMsg struct{}

type configMsg struct {
	cfg *config.Config
	err error
}

// New builds the model. It returns an error if a widget can't be created
// (e.g. invalid options).
func New(opts Options) (*Model, error) {
	m := &Model{opts: opts, configCh: make(chan struct{}, 1)}
	m.hostname, _ = os.Hostname()
	if err := m.applyConfig(opts.Config); err != nil {
		return nil, err
	}
	start := cmp.Or(opts.Dashboard, opts.Config.General.DefaultDashboard)
	if start != "" {
		i := slices.IndexFunc(m.dashboards, func(d *dashboard) bool { return d.name == start })
		if i < 0 {
			return nil, fmt.Errorf("unknown dashboard %q (available: %s)", start, strings.Join(m.dashboardNames(), ", "))
		}
		m.active = i
	}
	ch, _ := opts.Source.Store().Subscribe()
	m.storeCh = ch
	return m, nil
}

func (m *Model) dashboardNames() []string {
	names := make([]string, len(m.dashboards))
	for i, d := range m.dashboards {
		names[i] = d.name
	}
	return names
}

// applyConfig (re)builds dashboards and widgets from cfg.
func (m *Model) applyConfig(cfg *config.Config) error {
	theme, err := LookupTheme(cfg.General.Theme)
	if err != nil {
		return err
	}
	mode, err := chart.ParseMode(cfg.General.GraphMode)
	if err != nil {
		return err
	}
	var dbs []*dashboard
	for _, name := range cfg.Tabs() {
		dc, _ := cfg.Dashboards.Get(name)
		d := &dashboard{name: name, title: cmp.Or(dc.Title, name), cfg: dc, widgets: map[*config.Node]Widget{}}
		layouts := []*config.Node{dc.Layout}
		for _, v := range dc.Variants {
			layouts = append(layouts, v.Layout)
		}
		for _, l := range layouts {
			for _, n := range l.Widgets() {
				w, err := newWidget(n)
				if err != nil {
					return fmt.Errorf("dashboard %q: %w", name, err)
				}
				d.widgets[n] = w
			}
		}
		dbs = append(dbs, d)
	}

	prev := ""
	if m.active < len(m.dashboards) {
		prev = m.dashboards[m.active].name
	}
	m.cfg = cfg
	m.dashboards = dbs
	m.border = cfg.General.Border
	m.ctx = Context{
		Store:     m.opts.Source.Store(),
		Registry:  metrics.DefaultRegistry,
		Theme:     theme,
		GraphMode: mode,
		Interval:  m.opts.Source.Interval(),
		Procs:     m.opts.Procs,
		NumCPU:    runtime.NumCPU(),
	}
	m.active = max(slices.IndexFunc(dbs, func(d *dashboard) bool { return d.name == prev }), 0)
	m.relayout()
	return nil
}

func (m *Model) current() *dashboard { return m.dashboards[m.active] }

// relayout recomputes placements for the active dashboard and updates which
// collectors run.
func (m *Model) relayout() {
	if len(m.dashboards) == 0 {
		return
	}
	d := m.current()
	d.root = d.cfg.LayoutFor(m.width, m.height-1)
	unsup := m.unsupported()
	m.unsupSig = fmt.Sprint(unsup)
	if pruned := pruneUnsupported(d.root, d.widgets, unsup); pruned != nil {
		d.root = pruned
	}
	d.places = layout.Compute(d.root, layout.Rect{X: 0, Y: 1, W: m.width, H: m.height - 1})
	if m.focus >= len(d.places) {
		m.focus = 0
	}
	if m.focus == 0 {
		m.focus = m.defaultFocus()
	}
	if m.zoom && m.focusedWidget() == nil {
		m.zoom = false
	}
	m.updateDemand()
}

// unsupported returns the collectors that can't run on this machine.
func (m *Model) unsupported() map[string]bool {
	out := map[string]bool{}
	for _, st := range m.opts.Source.Status() {
		if st.State == source.StateUnsupported {
			out[st.Name] = true
		}
	}
	return out
}

// pruneUnsupported returns a copy of the layout without widgets whose data
// sources are all unavailable here (a battery widget on a desktop, a GPU
// widget without a GPU), so their space goes to the others. It returns nil
// if nothing would be left.
func pruneUnsupported(n *config.Node, widgets map[*config.Node]Widget, unsup map[string]bool) *config.Node {
	if n.Dir == config.DirNone {
		cs := widgets[n].Collectors()
		if len(cs) == 0 {
			return n
		}
		for _, c := range cs {
			if !unsup[c] {
				return n
			}
		}
		return nil
	}
	cp := *n
	cp.Children = nil
	for _, c := range n.Children {
		if pc := pruneUnsupported(c, widgets, unsup); pc != nil {
			cp.Children = append(cp.Children, pc)
		}
	}
	if len(cp.Children) == 0 {
		return nil
	}
	return &cp
}

func (m *Model) defaultFocus() int {
	d := m.current()
	first := -1
	for i, p := range d.places {
		w := d.widgets[p.Node]
		if w.Kind() == "proc" {
			return i
		}
		if _, ok := w.(Interactive); ok && first < 0 {
			first = i
		}
	}
	return max(first, 0)
}

func (m *Model) updateDemand() {
	var names []string
	for _, p := range m.current().places {
		cs := m.current().widgets[p.Node].Collectors()
		if cs == nil {
			m.opts.Source.SetDemand(nil)
			return
		}
		for _, c := range cs {
			if !slices.Contains(names, c) {
				names = append(names, c)
			}
		}
	}
	if len(m.current().places) == 0 {
		return // keep collecting until the first layout
	}
	m.opts.Source.SetDemand(names)
}

func (m *Model) focusedWidget() Widget {
	d := m.current()
	if m.focus < len(d.places) {
		return d.widgets[d.places[m.focus].Node]
	}
	return nil
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.waitStore()}
	if m.opts.ConfigPath != "" {
		ctx, cancel := context.WithCancel(context.Background())
		m.cancelWatch = cancel
		cmds = append(cmds, m.watchConfig(ctx))
	}
	return tea.Batch(cmds...)
}

// waitStore waits for the next store write. Writes from different collectors
// arrive close together, so a short delay coalesces them into one redraw.
func (m *Model) waitStore() tea.Cmd {
	ch := m.storeCh
	return func() tea.Msg {
		<-ch
		time.Sleep(30 * time.Millisecond)
		select {
		case <-ch:
		default:
		}
		return storeMsg{}
	}
}

func (m *Model) watchConfig(ctx context.Context) tea.Cmd {
	path, ch := m.opts.ConfigPath, m.configCh
	if err := config.Watch(ctx, path, func() {
		select {
		case ch <- struct{}{}:
		default:
		}
	}); err != nil {
		return statusCmd("config watch: " + err.Error())
	}
	return m.waitConfig()
}

func (m *Model) waitConfig() tea.Cmd {
	path, ch := m.opts.ConfigPath, m.configCh
	return func() tea.Msg {
		<-ch
		cfg, err := config.Load(path, true)
		if err == nil {
			err = CheckConfig(cfg)
		}
		return configMsg{cfg: cfg, err: err}
	}
}

// Update implements tea.Model.
func (m *Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.frozen = ""
		m.relayout()
		return m, nil

	case storeMsg:
		// Collectors report "unsupported" asynchronously after start.
		if fmt.Sprint(m.unsupported()) != m.unsupSig {
			m.relayout()
		}
		return m, m.waitStore()

	case configMsg:
		if msg.err != nil {
			m.setStatus("config not reloaded: " + firstLine(msg.err.Error()))
		} else if err := m.applyConfig(msg.cfg); err != nil {
			m.setStatus("config not reloaded: " + firstLine(err.Error()))
		} else {
			m.opts.Source.SetInterval(msg.cfg.General.Interval)
			m.ctx.Interval = msg.cfg.General.Interval
			m.setStatus("config reloaded")
		}
		m.frozen = ""
		return m, m.waitConfig()

	case statusMsg:
		m.setStatus(msg.text)
		return m, nil

	case openDialogMsg:
		m.dialog = msg.d
		m.frozen = ""
		return m, nil

	case tea.KeyPressMsg:
		m.frozen = ""
		return m, m.handleKey(msg)

	case tea.MouseMsg:
		m.frozen = ""
		return m, m.handleMouse(msg)
	}
	return m, nil
}

func firstLine(s string) string {
	l, _, _ := strings.Cut(s, "\n")
	return l
}

func (m *Model) setStatus(s string) {
	m.status, m.statusAt = s, time.Now()
}

func (m *Model) handleKey(k tea.KeyPressMsg) tea.Cmd {
	if m.dialog != nil {
		done, cmd := m.dialog.key(k)
		if done {
			m.dialog = nil
		}
		return cmd
	}
	switch k.String() {
	case "ctrl+c":
		return m.quit()
	case "esc":
		if m.zoom {
			m.zoom = false
			return nil
		}
	}
	if w, ok := m.focusedWidget().(Interactive); ok {
		if handled, cmd := w.HandleKey(&m.ctx, k); handled || w.Capturing() {
			return cmd
		}
	}
	switch s := k.String(); s {
	case "q":
		return m.quit()
	case "tab":
		m.cycleFocus(1)
	case "shift+tab":
		m.cycleFocus(-1)
	case "z":
		m.zoom = !m.zoom
	case "esc":
		m.zoom = false
	case "p":
		m.paused = !m.paused
		m.frozen = ""
	case "+", "=":
		m.changeInterval(1)
	case "-", "_":
		m.changeInterval(-1)
	case "g":
		m.ctx.GraphMode = (m.ctx.GraphMode + 1) % 3
		m.setStatus("graph mode: " + m.ctx.GraphMode.String())
	case "?", "h", "f1":
		m.dialog = m.helpDialog()
	default:
		if len(s) == 1 && s[0] >= '1' && s[0] <= '9' {
			m.switchDashboard(int(s[0] - '1'))
		}
	}
	return nil
}

func (m *Model) quit() tea.Cmd {
	if m.cancelWatch != nil {
		m.cancelWatch()
	}
	return tea.Quit
}

func (m *Model) switchDashboard(i int) {
	if i < 0 || i >= len(m.dashboards) || i == m.active {
		return
	}
	m.active, m.focus, m.zoom = i, 0, false
	m.relayout()
}

func (m *Model) cycleFocus(dir int) {
	n := len(m.current().places)
	if n == 0 {
		return
	}
	m.focus = ((m.focus+dir)%n + n) % n
}

var intervalSteps = []time.Duration{
	100 * time.Millisecond, 250 * time.Millisecond, 500 * time.Millisecond,
	time.Second, 2 * time.Second, 3 * time.Second, 5 * time.Second, 10 * time.Second,
}

func (m *Model) changeInterval(dir int) {
	cur := m.opts.Source.Interval()
	i, _ := slices.BinarySearch(intervalSteps, cur)
	i = max(min(i+dir, len(intervalSteps)-1), 0)
	m.opts.Source.SetInterval(intervalSteps[i])
	m.ctx.Interval = intervalSteps[i]
	m.setStatus("interval " + intervalSteps[i].String())
}

func (m *Model) handleMouse(msg tea.MouseMsg) tea.Cmd {
	if m.dialog != nil {
		return nil
	}
	mo := msg.Mouse()
	if mo.Y == 0 {
		if _, ok := msg.(tea.MouseClickMsg); ok {
			if i := m.tabAt(mo.X); i >= 0 {
				m.switchDashboard(i)
			}
		}
		return nil
	}
	d := m.current()
	idx := -1
	var rect layout.Rect
	if m.zoom {
		idx, rect = m.focus, m.zoomRect()
	} else {
		for i, p := range d.places {
			if p.Rect.Contains(mo.X, mo.Y) {
				idx, rect = i, p.Rect
				break
			}
		}
	}
	if idx < 0 {
		return nil
	}
	if _, ok := msg.(tea.MouseClickMsg); ok {
		m.focus = idx
	}
	w, ok := d.widgets[d.places[idx].Node].(Interactive)
	if !ok {
		return nil
	}
	ox, oy := 1, 1
	if m.border == "none" {
		ox, oy = 0, 0
	}
	return w.HandleMouse(&m.ctx, msg, mo.X-rect.X-ox, mo.Y-rect.Y-oy)
}

func (m *Model) zoomRect() layout.Rect {
	return layout.Rect{X: 0, Y: 1, W: m.width, H: m.height - 1}
}

// View implements tea.Model.
func (m *Model) View() tea.View {
	var v tea.View
	v.AltScreen = true
	v.WindowTitle = "smpmon"
	if m.cfg.General.Mouse {
		v.MouseMode = tea.MouseModeCellMotion
	}
	if m.width <= 0 || m.height <= 0 {
		return v
	}
	if m.paused && m.frozen != "" {
		v.SetContent(m.frozen)
		return v
	}
	m.ctx.Now = time.Now()
	content := m.render()
	if m.paused {
		m.frozen = content
	}
	v.SetContent(content)
	return v
}

func (m *Model) render() string {
	screen := make([][]segment, m.height)
	screen[0] = []segment{{0, m.topBar()}}
	if m.height > 1 {
		d := m.current()
		if m.zoom {
			r := m.zoomRect()
			m.drawWidget(screen, d.widgets[d.places[m.focus].Node], d.places[m.focus].Node, r, true)
		} else {
			for i, p := range d.places {
				m.drawWidget(screen, d.widgets[p.Node], p.Node, p.Rect, i == m.focus)
			}
		}
	}
	rows := make([]string, m.height)
	for y, segs := range screen {
		slices.SortFunc(segs, func(a, b segment) int { return a.x - b.x })
		var sb strings.Builder
		x := 0
		for _, s := range segs {
			if s.x > x {
				sb.WriteString(strings.Repeat(" ", s.x-x))
				x = s.x
			}
			sb.WriteString(s.s)
			x += width(s.s)
		}
		rows[y] = fit(sb.String(), m.width)
	}
	base := strings.Join(rows, "\n")
	if m.dialog == nil {
		return base
	}
	box := renderDialog(&m.ctx, m.dialog, m.width, m.height)
	bw, bh := 0, len(box)
	for _, l := range box {
		bw = max(bw, width(l))
	}
	dlg := lipgloss.NewLayer(strings.Join(box, "\n")).X(max((m.width-bw)/2, 0)).Y(max((m.height-bh)/2, 0)).Z(1)
	return lipgloss.NewCompositor(lipgloss.NewLayer(base), dlg).Render()
}

type segment struct {
	x int
	s string
}

func (m *Model) drawWidget(screen [][]segment, w Widget, n *config.Node, r layout.Rect, focused bool) {
	t := m.ctx.Theme
	title, info := w.Title(&m.ctx)
	if n.Title != "" {
		title = n.Title
	}
	color := t.BoxColor(w.Kind())
	titleColor := t.Title
	if focused {
		color = t.Title
		titleColor = t.Hotkey
	}
	f := frame{style: m.border, color: color, info: info}
	if title != "" {
		f.title = fg(titleColor, bold(title))
	}
	if iw, ok := w.(Interactive); ok && focused {
		f.footer = iw.Footer(&m.ctx)
	}
	cw, ch := f.inner(r.W, r.H)
	var content []string
	if cw > 0 && ch > 0 {
		content = w.Render(&m.ctx, cw, ch)
	}
	for i, l := range f.render(r.W, r.H, content) {
		if y := r.Y + i; y < len(screen) {
			screen[y] = append(screen[y], segment{r.X, l})
		}
	}
}

// tabLabels returns the tab bar labels. When the full labels would leave
// less than minRight cells for the right side of the bar, inactive tabs
// show only their number.
func (m *Model) tabLabels() []string {
	const minRight = 12
	labels := make([]string, len(m.dashboards))
	total := 0
	for i, d := range m.dashboards {
		labels[i] = " " + strconv.Itoa(i+1) + " " + d.title + " "
		total += width(labels[i])
	}
	if total+minRight <= m.width {
		return labels
	}
	for i := range labels {
		if i != m.active {
			labels[i] = " " + strconv.Itoa(i+1) + " "
		}
	}
	return labels
}

// tabAt returns the dashboard index of the tab at column x, or -1.
func (m *Model) tabAt(x int) int {
	pos := 0
	for i, l := range m.tabLabels() {
		w := width(l)
		if x >= pos && x < pos+w {
			return i
		}
		pos += w
	}
	return -1
}

func (m *Model) topBar() string {
	t := m.ctx.Theme
	var left strings.Builder
	for i, l := range m.tabLabels() {
		if i == m.active {
			left.WriteString(bg(t.Selected, fg(t.SelectedFg, bold(l))))
		} else {
			left.WriteString(fg(t.Dim, l))
		}
	}
	sep := fg(t.Dim, " │ ")
	status := ""
	if m.status != "" && time.Since(m.statusAt) < 5*time.Second {
		status = fg(t.Warn, m.status)
	}
	if m.paused {
		status = strings.TrimPrefix(status+sep+fg(t.Crit, bold("PAUSED")), sep)
	}
	// Optional parts, dropped from the front when space runs out.
	optional := []string{
		fg(t.Hotkey, "?") + fg(t.Dim, " help"),
		fg(t.Fg, m.hostname),
		fg(t.Dim, m.ctx.Interval.String()),
	}
	clock := fg(t.Title, m.ctx.Now.Format("15:04:05"))
	room := m.width - width(left.String()) - 1
	for {
		parts := []string{}
		if status != "" {
			parts = append(parts, status)
		}
		for i := len(optional) - 1; i >= 0; i-- {
			parts = append(parts, optional[i])
		}
		parts = append(parts, clock)
		right := strings.Join(parts, sep)
		if width(right) <= room || (len(optional) == 0 && status == "") {
			return spread(left.String(), right, m.width)
		}
		if len(optional) > 0 {
			optional = optional[1:]
			continue
		}
		// Only status and clock left: shorten the status.
		budget := room - width(sep+clock)
		if budget < 4 {
			status = ""
			continue
		}
		status = fitEllipsis(status, budget)
		return spread(left.String(), strings.TrimRight(status, " ")+sep+clock, m.width)
	}
}

func (m *Model) helpDialog() dialog {
	return &helpDialog{sections: []helpSection{
		{"Global", [][2]string{
			{"1-9", "switch dashboard"},
			{"tab / shift+tab", "focus next/previous widget"},
			{"z / esc", "zoom focused widget"},
			{"+ / -", "change update interval"},
			{"g", "cycle graph mode (braille/block/ascii)"},
			{"p", "pause display"},
			{"q / ctrl+c", "quit"},
		}},
		{"Processes", [][2]string{
			{"↑↓ pgup pgdn", "select"},
			{"← →  r", "sort column, reverse"},
			{"f or /", "filter"},
			{"t / space", "tree view / collapse"},
			{"enter", "details"},
			{"k / K / s", "SIGTERM / SIGKILL / choose signal"},
			{"i", "renice"},
			{"c", "CPU per core / total"},
		}},
		{"Network", [][2]string{{"b / n", "previous / next interface"}}},
	}}
}

// Run starts the UI and blocks until the user quits.
func Run(ctx context.Context, opts Options) error {
	m, err := New(opts)
	if err != nil {
		return err
	}
	// Data changes once per interval, so a lower frame rate saves wakeups
	// without making input feel slower.
	_, err = tea.NewProgram(m, tea.WithContext(ctx), tea.WithFPS(30)).Run()
	return err
}
