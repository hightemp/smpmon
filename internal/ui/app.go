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
	"github.com/charmbracelet/colorprofile"

	"github.com/hightemp/smpmon/internal/chart"
	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/config"
	"github.com/hightemp/smpmon/internal/keymap"
	"github.com/hightemp/smpmon/internal/layout"
	"github.com/hightemp/smpmon/internal/metrics"
	"github.com/hightemp/smpmon/internal/source"
)

// Options configures the UI.
type Options struct {
	Config     *config.Config
	ConfigPath string // watched for changes when set
	SavePath   string // where the layout editor saves; may not exist yet
	// Hosts are the machines to show; the first is shown initially unless
	// Host names another. If empty, Source and Procs form a single host.
	Hosts     []HostSource
	Host      string
	Source    source.Source
	Procs     collect.ProcessController
	Dashboard string // dashboard to open; empty uses the config default
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
	edit     *editor // layout editor, nil when not editing
	status   string
	statusAt time.Time

	width, height int
	hosts         []HostSource
	cur           int           // index of the current host
	updates       chan struct{} // any store was written
	configCh      chan struct{}
	unsupSig      string // unsupported collectors at the last layout
	cancelWatch   context.CancelFunc
	hostname      string
}

type storeMsg struct{}

type heartbeatMsg struct{}

func heartbeat() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return heartbeatMsg{} })
}

type configMsg struct {
	cfg *config.Config
	err error
}

// New builds the model. It returns an error if a widget can't be created
// (e.g. invalid options).
func New(opts Options) (*Model, error) {
	m := &Model{opts: opts, configCh: make(chan struct{}, 1), updates: make(chan struct{}, 1)}
	m.hostname, _ = os.Hostname()
	m.hosts = opts.Hosts
	if len(m.hosts) == 0 {
		m.hosts = []HostSource{{Name: "local", Source: opts.Source, Procs: opts.Procs}}
	}
	if opts.Host != "" {
		i := m.hostIndex(opts.Host)
		if i < 0 {
			return nil, fmt.Errorf("unknown host %q", opts.Host)
		}
		m.cur = i
	}
	for _, h := range m.hosts {
		ch, _ := h.Source.Store().Subscribe()
		go func() {
			for range ch {
				select {
				case m.updates <- struct{}{}:
				default:
				}
			}
		}()
	}
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
	keys, err := keymap.Build(cfg.Keys.Preset, cfg.Keys.Bind)
	if err != nil {
		return err
	}
	var dbs []*dashboard
	tabs := cfg.Tabs()
	// The hosts overview appears by itself once there are remote hosts.
	if _, ok := cfg.Dashboards.Get("hosts"); ok && len(m.hosts) > 1 && !slices.Contains(tabs, "hosts") {
		tabs = append(slices.Clone(tabs), "hosts")
	}
	for _, name := range tabs {
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
		Store:     m.hosts[m.cur].Source.Store(),
		Registry:  metrics.DefaultRegistry,
		Theme:     theme,
		GraphMode: mode,
		Interval:  m.hosts[m.cur].Source.Interval(),
		Procs:     m.hosts[m.cur].Procs,
		Host:      m.hosts[m.cur].Name,
		Hosts:     m.hosts,
		NumCPU:    runtime.NumCPU(),
		Keys:      keys,
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
	if m.edit != nil && m.edit.d == d {
		d.root = m.edit.root() // the editor shows every widget, paths must match the config
	} else if pruned := pruneUnsupported(d.root, d.widgets, unsup); pruned != nil {
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
	for _, st := range m.hosts[m.cur].Source.Status() {
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

func (m *Model) focusedWidget() Widget {
	d := m.current()
	if m.focus < len(d.places) {
		return d.widgets[d.places[m.focus].Node]
	}
	return nil
}

// Init implements tea.Model.
func (m *Model) Init() tea.Cmd {
	cmds := []tea.Cmd{m.waitStore(), heartbeat()}
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
	ch := m.updates
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
			if dir, derr := ThemesDir(); derr == nil {
				err = LoadUserThemes(dir)
			}
		}
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
		if m.edit != nil && m.edit.dirty {
			m.setStatus("config changed on disk; save (ctrl+s) or leave the editor to reload")
			return m, m.waitConfig()
		}
		var editing string
		var sel []int
		if m.edit != nil {
			editing, sel = m.edit.d.name, m.edit.sel
			m.edit = nil
		}
		defer func() {
			if editing != "" && m.current().name == editing {
				m.startEdit()
				if m.edit.root().At(sel) != nil {
					m.edit.sel = sel
				}
			}
		}()
		if msg.err != nil {
			m.setStatus("config not reloaded: " + firstLine(msg.err.Error()))
		} else if err := m.applyConfig(msg.cfg); err != nil {
			m.setStatus("config not reloaded: " + firstLine(err.Error()))
		} else {
			for _, h := range m.hosts {
				h.Source.SetInterval(msg.cfg.General.Interval)
			}
			m.ctx.Interval = msg.cfg.General.Interval
			m.setStatus("config reloaded")
		}
		m.frozen = ""
		return m, m.waitConfig()

	case statusMsg:
		m.setStatus(msg.text)
		return m, nil

	case switchHostMsg:
		m.setHost(m.hostIndex(msg.name))
		return m, nil

	case heartbeatMsg:
		// Redraw for the clock and connection states, and re-layout when
		// collectors report what's unsupported.
		if fmt.Sprint(m.unsupported()) != m.unsupSig {
			m.relayout()
		}
		return m, heartbeat()

	case themeMsg:
		if t, err := LookupTheme(msg.name); err == nil {
			m.ctx.Theme = t
			m.setStatus("theme: " + t.Name)
		}
		m.frozen = ""
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

// statusText returns the status message while it is fresh.
func (m *Model) statusText() string {
	if m.status != "" && time.Since(m.statusAt) < 5*time.Second {
		return m.status
	}
	return ""
}

func (m *Model) setStatus(s string) {
	m.status, m.statusAt = s, time.Now()
}

func (m *Model) handleKey(k tea.KeyPressMsg) tea.Cmd {
	if m.dialog != nil {
		d := m.dialog
		done, cmd := d.key(k)
		if done && m.dialog == d { // the callback may have opened another dialog
			m.dialog = nil
		}
		return cmd
	}
	if m.edit != nil {
		if k.String() == "ctrl+c" {
			return m.quit()
		}
		return m.handleEditKey(k)
	}
	a := m.ctx.action(k, keymap.Global)
	switch {
	case k.String() == "ctrl+c":
		return m.quit() // always available, whatever the bindings
	case a == "back" && m.zoom:
		m.zoom = false
		return nil
	}
	if w, ok := m.focusedWidget().(Interactive); ok {
		wctx, _ := m.ctxFor(m.current().places[m.focus].Node)
		if handled, cmd := w.HandleKey(wctx, k); handled || w.Capturing() {
			return cmd
		}
	}
	switch a {
	case "quit":
		return m.quit()
	case "focus_next":
		m.cycleFocus(1)
	case "focus_prev":
		m.cycleFocus(-1)
	case "zoom":
		m.zoom = !m.zoom
	case "back":
		m.zoom = false
	case "pause":
		m.paused = !m.paused
		m.frozen = ""
	case "interval_up":
		m.changeInterval(1)
	case "interval_down":
		m.changeInterval(-1)
	case "graph_mode":
		m.ctx.GraphMode = (m.ctx.GraphMode + 1) % 3
		m.setStatus("graph mode: " + m.ctx.GraphMode.String())
	case "help":
		m.dialog = m.helpDialog()
	case "theme":
		m.dialog = m.themeDialog()
	case "edit":
		m.startEdit()
	case "host":
		if len(m.hosts) > 1 {
			m.dialog = m.hostDialog()
		} else {
			m.setStatus("no remote hosts: add them under hosts: in the config or use smpmon connect")
		}
	default:
		if n, ok := strings.CutPrefix(string(a), "dashboard_"); ok {
			i, _ := strconv.Atoi(n)
			m.switchDashboard(i - 1)
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
	cur := m.hosts[m.cur].Source.Interval()
	i, _ := slices.BinarySearch(intervalSteps, cur)
	i = max(min(i+dir, len(intervalSteps)-1), 0)
	for _, h := range m.hosts {
		h.Source.SetInterval(intervalSteps[i])
	}
	m.ctx.Interval = intervalSteps[i]
	m.setStatus("interval " + intervalSteps[i].String())
}

func (m *Model) handleMouse(msg tea.MouseMsg) tea.Cmd {
	if m.dialog != nil {
		return nil
	}
	if m.edit != nil {
		return m.handleEditMouse(msg)
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
	wctx, _ := m.ctxFor(d.places[idx].Node)
	return w.HandleMouse(wctx, msg, mo.X-rect.X-ox, mo.Y-rect.Y-oy)
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
	if m.edit != nil {
		screen[0] = []segment{{0, m.editBar()}}
	} else {
		screen[0] = []segment{{0, m.topBar()}}
	}
	if m.height > 1 {
		d := m.current()
		if m.zoom {
			r := m.zoomRect()
			m.drawWidget(screen, d.widgets[d.places[m.focus].Node], d.places[m.focus].Node, r, true)
		} else {
			for i, p := range d.places {
				focused := i == m.focus
				if m.edit != nil {
					focused = m.edit.inSelection(p.Path)
				}
				m.drawWidget(screen, d.widgets[p.Node], p.Node, p.Rect, focused)
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
	wctx, _ := m.ctxFor(n)
	title, info := w.Title(wctx)
	if n.Title != "" {
		title = n.Title
	}
	if n.Source != "" && n.Source != m.hosts[m.cur].Name {
		title += " @" + n.Source
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
		f.footer = iw.Footer(wctx)
	}
	cw, ch := f.inner(r.W, r.H)
	var content []string
	if cw > 0 && ch > 0 {
		if ctx, ok := m.ctxFor(n); ok {
			content = w.Render(ctx, cw, ch)
		} else {
			content = block([]string{fg(t.Crit, "unknown host "+n.Source)}, cw, ch)
		}
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
	if s := m.statusText(); s != "" {
		status = fg(t.Warn, s)
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
	if b := m.hostBadge(); b != "" {
		// With remote hosts the badge replaces the local hostname and is
		// dropped last.
		optional = append([]string{optional[0]}, optional[2], b)
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

type themeMsg struct{ name string }

func (m *Model) themeDialog() dialog {
	names := ThemeNames()
	return &chooseDialog{
		heading: "Theme",
		options: names,
		sel:     max(slices.Index(names, m.ctx.Theme.Name), 0),
		onPick: func(i int) tea.Cmd {
			return func() tea.Msg { return themeMsg{names[i]} }
		},
	}
}

func (m *Model) helpDialog() dialog {
	names := map[keymap.Scope]string{
		keymap.Global: "Global", keymap.Nav: "Lists", keymap.Proc: "Processes",
		keymap.Net: "Network", keymap.Docker: "Docker",
	}
	var sections []helpSection
	for _, scope := range []keymap.Scope{keymap.Global, keymap.Nav, keymap.Proc, keymap.Net, keymap.Docker} {
		sec := helpSection{name: names[scope]}
		for _, i := range keymap.Actions {
			if i.Scope != scope || strings.HasPrefix(string(i.Action), "dashboard_") {
				continue
			}
			if label := m.ctx.Keys.Label(i.Action); label != "" {
				sec.keys = append(sec.keys, [2]string{label, i.Help})
			}
		}
		if scope == keymap.Global {
			first, last := m.ctx.Keys.First("dashboard_1"), m.ctx.Keys.First("dashboard_9")
			if first != "" {
				sec.keys = append(sec.keys, [2]string{first + "…" + last, "switch dashboard"})
			}
		}
		sections = append(sections, sec)
	}
	return &helpDialog{sections: sections}
}

// colorProfiles maps general.color to forced profiles; "auto" detects.
var colorProfiles = map[string]colorprofile.Profile{
	"truecolor": colorprofile.TrueColor,
	"256":       colorprofile.ANSI256,
	"16":        colorprofile.ANSI,
	"none":      colorprofile.Ascii,
}

// Run starts the UI and blocks until the user quits.
func Run(ctx context.Context, opts Options) error {
	m, err := New(opts)
	if err != nil {
		return err
	}
	// Data changes once per interval, so a lower frame rate saves wakeups
	// without making input feel slower.
	popts := []tea.ProgramOption{tea.WithContext(ctx), tea.WithFPS(30)}
	if p, ok := colorProfiles[opts.Config.General.Color]; ok {
		popts = append(popts, tea.WithColorProfile(p))
	}
	_, err = tea.NewProgram(m, popts...).Run()
	return err
}
