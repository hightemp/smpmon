package ui

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/hightemp/smpmon/internal/chart"
	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/config"
	"github.com/hightemp/smpmon/internal/keymap"
	"github.com/hightemp/smpmon/internal/metrics"
)

type procOptions struct {
	Columns []string `yaml:"columns"`
	Sort    struct {
		By   string `yaml:"by"`
		Desc *bool  `yaml:"desc"`
	} `yaml:"sort"`
	Tree   bool   `yaml:"tree"`
	Filter string `yaml:"filter"`
	// PerCore shows CPU as a share of one core (top style) instead of the
	// whole machine (btop style).
	PerCore bool `yaml:"per_core"`
}

// procColumn describes one table column.
type procColumn struct {
	key    string
	header string
	width  int  // 0 = flexible (takes the remaining space)
	right  bool // right-aligned
	value  func(p *collect.Process, w *procWidget, ctx *Context) string
	cmp    func(a, b *collect.Process) int
}

var procColumns = map[string]procColumn{
	"pid": {key: "pid", header: "Pid", width: 7, right: true,
		value: func(p *collect.Process, _ *procWidget, _ *Context) string { return strconv.Itoa(int(p.PID)) },
		cmp:   func(a, b *collect.Process) int { return cmp.Compare(a.PID, b.PID) }},
	"ppid": {key: "ppid", header: "PPid", width: 7, right: true,
		value: func(p *collect.Process, _ *procWidget, _ *Context) string { return strconv.Itoa(int(p.PPID)) },
		cmp:   func(a, b *collect.Process) int { return cmp.Compare(a.PPID, b.PPID) }},
	"name": {key: "name", header: "Program", width: 16,
		value: func(p *collect.Process, _ *procWidget, _ *Context) string { return p.Name },
		cmp:   func(a, b *collect.Process) int { return cmp.Compare(strings.ToLower(a.Name), strings.ToLower(b.Name)) }},
	"cmd": {key: "cmd", header: "Command", width: 0,
		value: func(p *collect.Process, _ *procWidget, _ *Context) string { return p.Cmdline },
		cmp:   func(a, b *collect.Process) int { return cmp.Compare(a.Cmdline, b.Cmdline) }},
	"user": {key: "user", header: "User", width: 10,
		value: func(p *collect.Process, _ *procWidget, _ *Context) string { return p.User },
		cmp:   func(a, b *collect.Process) int { return cmp.Compare(a.User, b.User) }},
	"state": {key: "state", header: "S", width: 1,
		value: func(p *collect.Process, _ *procWidget, _ *Context) string { return string(rune(p.State)) },
		cmp:   func(a, b *collect.Process) int { return cmp.Compare(a.State, b.State) }},
	"threads": {key: "threads", header: "Thr", width: 4, right: true,
		value: func(p *collect.Process, _ *procWidget, _ *Context) string { return strconv.Itoa(int(p.Threads)) },
		cmp:   func(a, b *collect.Process) int { return cmp.Compare(a.Threads, b.Threads) }},
	"nice": {key: "nice", header: "NI", width: 3, right: true,
		value: func(p *collect.Process, _ *procWidget, _ *Context) string { return strconv.Itoa(int(p.Nice)) },
		cmp:   func(a, b *collect.Process) int { return cmp.Compare(a.Nice, b.Nice) }},
	"cpu": {key: "cpu", header: "Cpu%", width: 6, right: true,
		value: func(p *collect.Process, w *procWidget, ctx *Context) string {
			return strconv.FormatFloat(w.cpu(ctx, p), 'f', 1, 64)
		},
		cmp: func(a, b *collect.Process) int { return cmp.Compare(a.CPU, b.CPU) }},
	"mem": {key: "mem", header: "Mem%", width: 5, right: true,
		value: func(p *collect.Process, _ *procWidget, _ *Context) string {
			return strconv.FormatFloat(p.Mem, 'f', 1, 64)
		},
		cmp: func(a, b *collect.Process) int { return cmp.Compare(a.RSS, b.RSS) }},
	"rss": {key: "rss", header: "MemB", width: 9, right: true,
		value: func(p *collect.Process, _ *procWidget, _ *Context) string { return metrics.FormatBytes(float64(p.RSS)) },
		cmp:   func(a, b *collect.Process) int { return cmp.Compare(a.RSS, b.RSS) }},
	"vms": {key: "vms", header: "Virt", width: 9, right: true,
		value: func(p *collect.Process, _ *procWidget, _ *Context) string { return metrics.FormatBytes(float64(p.VMS)) },
		cmp:   func(a, b *collect.Process) int { return cmp.Compare(a.VMS, b.VMS) }},
	"read": {key: "read", header: "Read/s", width: 10, right: true,
		value: func(p *collect.Process, _ *procWidget, _ *Context) string { return metrics.FormatBytes(p.ReadBps) },
		cmp:   func(a, b *collect.Process) int { return cmp.Compare(a.ReadBps, b.ReadBps) }},
	"write": {key: "write", header: "Write/s", width: 10, right: true,
		value: func(p *collect.Process, _ *procWidget, _ *Context) string { return metrics.FormatBytes(p.WriteBps) },
		cmp:   func(a, b *collect.Process) int { return cmp.Compare(a.WriteBps, b.WriteBps) }},
	"time": {key: "time", header: "Time", width: 9, right: true,
		value: func(p *collect.Process, _ *procWidget, _ *Context) string {
			return metrics.FormatDuration(p.CPUTime.Seconds())
		},
		cmp: func(a, b *collect.Process) int { return cmp.Compare(a.CPUTime, b.CPUTime) }},
	"start": {key: "start", header: "Start", width: 8, right: true,
		value: func(p *collect.Process, _ *procWidget, ctx *Context) string {
			if ctx.Now.Sub(p.StartTime) < 24*time.Hour {
				return p.StartTime.Format("15:04:05")
			}
			return p.StartTime.Format("Jan 02")
		},
		cmp: func(a, b *collect.Process) int { return a.StartTime.Compare(b.StartTime) }},
}

var defaultProcColumns = []string{"pid", "name", "cmd", "threads", "user", "rss", "cpu"}

// procRow is a process as displayed, with its tree prefix.
type procRow struct {
	p      *collect.Process
	prefix string
}

type procWidget struct {
	opts    procOptions
	cols    []procColumn
	sortBy  string
	desc    bool
	tree    bool
	perCore bool

	filter    string
	filtering bool // typing into the filter

	selPID  int32 // 0 = no selection
	selIdx  int
	offset  int
	details bool

	collapsed map[int32]bool
	rows      []procRow
	listH     int // rows of the list in the last render
	listY     int // y of the first list row in the content area
	colX      []int

	cpuBuf, memBuf []float64
}

func init() {
	register("proc", func(n *config.Node) (Widget, error) {
		w := &procWidget{collapsed: map[int32]bool{}}
		if err := n.DecodeOptions(&w.opts); err != nil {
			return nil, err
		}
		names := w.opts.Columns
		if len(names) == 0 {
			names = defaultProcColumns
		}
		for _, c := range names {
			col, ok := procColumns[c]
			if !ok {
				return nil, fmt.Errorf("options.columns: unknown column %q (valid: %s)", c, strings.Join(procColumnNames(), ", "))
			}
			w.cols = append(w.cols, col)
		}
		w.sortBy = cmp.Or(w.opts.Sort.By, "cpu")
		if _, ok := procColumns[w.sortBy]; !ok {
			return nil, fmt.Errorf("options.sort.by: unknown column %q", w.sortBy)
		}
		w.desc = w.opts.Sort.Desc == nil || *w.opts.Sort.Desc
		w.tree, w.perCore, w.filter = w.opts.Tree, w.opts.PerCore, w.opts.Filter
		return w, nil
	})
}

func procColumnNames() []string {
	names := make([]string, 0, len(procColumns))
	for k := range procColumns {
		names = append(names, k)
	}
	slices.Sort(names)
	return names
}

func (w *procWidget) Kind() string         { return "proc" }
func (w *procWidget) Collectors() []string { return []string{"procs"} }
func (w *procWidget) Capturing() bool      { return w.filtering }

func (w *procWidget) cpu(ctx *Context, p *collect.Process) float64 {
	if w.perCore || ctx.NumCPU <= 0 {
		return p.CPU
	}
	return p.CPU / float64(ctx.NumCPU)
}

func (w *procWidget) Title(ctx *Context) (string, string) {
	t := ctx.Theme
	info := ""
	if w.filter != "" || w.filtering {
		cursor := ""
		if w.filtering {
			cursor = "█"
		}
		info = fg(t.Hotkey, "filter: ") + fg(t.Title, w.filter+cursor) + " "
	}
	if w.tree {
		info += fg(t.Dim, "tree ")
	}
	dir := "↓"
	if !w.desc {
		dir = "↑"
	}
	info += fg(t.Dim, "sort ") + fg(t.Fg, procColumns[w.sortBy].header+dir)
	return "proc", info
}

func (w *procWidget) Footer(ctx *Context) string {
	if w.filtering {
		return hint(ctx, "enter", "apply") + " " + hint(ctx, "esc", "clear")
	}
	return joinHints(
		keyHint(ctx, "proc_filter", "filter"), hint(ctx, "←→", "sort"), keyHint(ctx, "proc_reverse", "rev"),
		keyHint(ctx, "proc_tree", "tree"), keyHint(ctx, "proc_kill", "kill"), keyHint(ctx, "proc_signal", "signal"),
		keyHint(ctx, "proc_renice", "nice"), keyHint(ctx, "proc_details", "info"),
	)
}

// visible builds the displayed rows: filtered, sorted or as a tree.
func (w *procWidget) visible(ctx *Context) []procRow {
	procs, _ := metrics.TableAs[[]collect.Process](ctx.Store, collect.TableProcesses)
	list := make([]*collect.Process, 0, len(procs))
	f := strings.ToLower(w.filter)
	for i := range procs {
		p := &procs[i]
		if f != "" && !matchesFilter(p, f) {
			continue
		}
		list = append(list, p)
	}
	col := procColumns[w.sortBy]
	less := func(a, b *collect.Process) int {
		c := col.cmp(a, b)
		if c == 0 {
			c = cmp.Compare(a.PID, b.PID)
			if w.desc {
				c = -c // keep pid ascending as tie-break
			}
		}
		if w.desc {
			return -c
		}
		return c
	}
	if !w.tree || f != "" {
		slices.SortFunc(list, less)
		rows := make([]procRow, len(list))
		for i, p := range list {
			rows[i] = procRow{p: p}
		}
		return rows
	}
	return w.treeRows(list, less)
}

func matchesFilter(p *collect.Process, f string) bool {
	return strings.Contains(strings.ToLower(p.Name), f) ||
		strings.Contains(strings.ToLower(p.Cmdline), f) ||
		strings.Contains(strings.ToLower(p.User), f) ||
		strconv.Itoa(int(p.PID)) == f
}

func (w *procWidget) treeRows(list []*collect.Process, less func(a, b *collect.Process) int) []procRow {
	byPID := make(map[int32]*collect.Process, len(list))
	for _, p := range list {
		byPID[p.PID] = p
	}
	children := map[int32][]*collect.Process{}
	var roots []*collect.Process
	for _, p := range list {
		if _, ok := byPID[p.PPID]; ok && p.PPID != p.PID {
			children[p.PPID] = append(children[p.PPID], p)
		} else {
			roots = append(roots, p)
		}
	}
	slices.SortFunc(roots, less)
	rows := make([]procRow, 0, len(list))
	var walk func(p *collect.Process, indent string, lastChild, root bool)
	walk = func(p *collect.Process, indent string, lastChild, root bool) {
		prefix := ""
		childIndent := indent
		if !root {
			branch := "├─"
			if lastChild {
				branch = "└─"
			}
			prefix = indent + branch
			if lastChild {
				childIndent = indent + "  "
			} else {
				childIndent = indent + "│ "
			}
		}
		kids := children[p.PID]
		if len(kids) > 0 && w.collapsed[p.PID] {
			prefix += "+"
		}
		rows = append(rows, procRow{p: p, prefix: prefix})
		if w.collapsed[p.PID] {
			return
		}
		slices.SortFunc(kids, less)
		for i, k := range kids {
			walk(k, childIndent, i == len(kids)-1, false)
		}
	}
	for _, r := range roots {
		walk(r, "", true, true)
	}
	return rows
}

func (w *procWidget) Render(ctx *Context, width, h int) []string {
	t := ctx.Theme
	w.rows = w.visible(ctx)
	w.syncSelection()

	detailsH := 0
	if w.details && w.selPID != 0 && h >= 12 {
		detailsH = 6
	}
	w.listY = 1
	w.listH = max(h-1-detailsH, 0)
	w.clampScroll()

	// Column widths: fixed columns as defined, the flexible one takes the
	// rest; columns that don't fit are dropped from the right.
	widths := make([]int, len(w.cols))
	used := 0
	flex := -1
	for i, c := range w.cols {
		if c.width == 0 && flex < 0 {
			flex = i
			continue
		}
		cw := max(c.width, len(c.header))
		if used+cw+1 > width {
			widths[i] = -1
			continue
		}
		widths[i] = cw
		used += cw + 1
	}
	if flex >= 0 {
		widths[flex] = max(width-used-1, 0)
		if widths[flex] < 4 {
			widths[flex] = -1
		}
	}
	w.colX = make([]int, len(w.cols))

	cell := func(i int, s string) string {
		if w.cols[i].right {
			return alignRight(s, widths[i])
		}
		return fitEllipsis(s, widths[i])
	}
	var hdr strings.Builder
	x := 0
	for i, c := range w.cols {
		w.colX[i] = -1
		if widths[i] < 0 {
			continue
		}
		w.colX[i] = x
		label := c.header
		if c.key == w.sortBy {
			label = bold(label)
		}
		hdr.WriteString(cell(i, label) + " ")
		x += widths[i] + 1
	}
	out := []string{fg(t.Title, hdr.String())}

	for r := 0; r < w.listH; r++ {
		idx := w.offset + r
		if idx >= len(w.rows) {
			break
		}
		row := w.rows[idx]
		p := row.p
		selected := p.PID == w.selPID
		var b strings.Builder
		for i, c := range w.cols {
			if widths[i] < 0 {
				continue
			}
			v := c.value(p, w, ctx)
			if c.key == "name" || (c.key == "cmd" && !slices.ContainsFunc(w.cols, func(c procColumn) bool { return c.key == "name" })) {
				v = row.prefix + v
			}
			s := cell(i, v)
			switch {
			case selected:
			case c.key == "cpu":
				s = fg(t.Proc.At(w.cpu(ctx, p)/100), s)
			case c.key == "mem" || c.key == "rss":
				s = fg(t.Proc.At(p.Mem/100), s)
			case c.key == "cmd" || c.key == "pid":
				s = fg(t.Dim, s)
			default:
				s = fg(t.Fg, s)
			}
			b.WriteString(s + " ")
		}
		line := fit(b.String(), width)
		if selected {
			line = bg(t.Selected, fg(t.SelectedFg, line))
		}
		out = append(out, line)
	}
	out = block(out, width, h-detailsH)
	if detailsH > 0 {
		out = append(out, w.renderDetails(ctx, width, detailsH)...)
	}
	return out
}

func (w *procWidget) selected() *collect.Process {
	for _, r := range w.rows {
		if r.p.PID == w.selPID {
			return r.p
		}
	}
	return nil
}

func (w *procWidget) renderDetails(ctx *Context, width, h int) []string {
	t := ctx.Theme
	p := w.selected()
	if p == nil {
		return block(nil, width, h)
	}
	sep := fg(t.Border, strings.Repeat("─", width))
	head := fg(t.Title, bold(p.Name)) + fg(t.Dim, fmt.Sprintf("  pid %d  ppid %d  %s  state %c  threads %d  nice %d  started %s",
		p.PID, p.PPID, p.User, p.State, p.Threads, p.Nice, p.StartTime.Format("2006-01-02 15:04")))
	cmdline := fg(t.Fg, p.Cmdline)

	gw := (width - 1) / 2
	pid := strconv.Itoa(int(p.PID))
	w.cpuBuf = ctx.Store.Series(metrics.NewKey("proc", pid, "cpu"), w.cpuBuf[:0], gw*2)
	w.memBuf = ctx.Store.Series(metrics.NewKey("proc", pid, "mem"), w.memBuf[:0], gw*2)
	cpuG := chart.Graph{Width: gw, Height: 3, Mode: ctx.GraphMode, Series: []chart.Series{{Values: w.cpuBuf, Gradient: t.CPU}}}
	memG := chart.Graph{Width: width - gw - 1, Height: 3, Mode: ctx.GraphMode, Series: []chart.Series{{Values: w.memBuf, Gradient: t.Mem}}}
	cpuC, memC := cpuG.Render(), memG.Render()
	cpuC.Text(0, 0, fmt.Sprintf("cpu %.1f%%", w.cpu(ctx, p)), t.Title)
	memC.Text(0, 0, "mem "+metrics.FormatBytes(float64(p.RSS)), t.Title)
	cl, ml := cpuC.Lines(), memC.Lines()

	out := []string{sep, fitEllipsis(head, width), fitEllipsis(cmdline, width)}
	for i := range 3 {
		out = append(out, cl[i]+" "+ml[i])
	}
	return block(out, width, h)
}

// syncSelection keeps the selection on the same pid while rows move, and
// on the same index when the selected process exits.
func (w *procWidget) syncSelection() {
	if w.selPID == 0 {
		return
	}
	for i, r := range w.rows {
		if r.p.PID == w.selPID {
			w.selIdx = i
			return
		}
	}
	if len(w.rows) == 0 {
		w.selPID = 0
		return
	}
	w.selIdx = min(w.selIdx, len(w.rows)-1)
	w.selPID = w.rows[w.selIdx].p.PID
}

func (w *procWidget) clampScroll() {
	if w.listH <= 0 {
		return
	}
	if w.selPID != 0 {
		if w.selIdx < w.offset {
			w.offset = w.selIdx
		}
		if w.selIdx >= w.offset+w.listH {
			w.offset = w.selIdx - w.listH + 1
		}
	}
	w.offset = max(min(w.offset, len(w.rows)-w.listH), 0)
}

func (w *procWidget) moveSelection(delta int) {
	if len(w.rows) == 0 {
		return
	}
	if w.selPID == 0 {
		w.selIdx = w.offset
		if delta < 0 {
			w.selIdx = min(w.offset+w.listH-1, len(w.rows)-1)
		}
	} else {
		w.selIdx = max(min(w.selIdx+delta, len(w.rows)-1), 0)
	}
	w.selPID = w.rows[w.selIdx].p.PID
	w.clampScroll()
}

func (w *procWidget) cycleSort(dir int) {
	i := slices.IndexFunc(w.cols, func(c procColumn) bool { return c.key == w.sortBy })
	if i < 0 {
		i = 0
	}
	i = ((i+dir)%len(w.cols) + len(w.cols)) % len(w.cols)
	w.sortBy = w.cols[i].key
}

// HandleKey implements Interactive.
func (w *procWidget) HandleKey(ctx *Context, k tea.KeyPressMsg) (bool, tea.Cmd) {
	if w.rows == nil { // no frame drawn yet
		w.rows = w.visible(ctx)
		w.syncSelection()
	}
	if w.filtering {
		switch k.String() {
		case "enter":
			w.filtering = false
		case "esc":
			w.filtering, w.filter = false, ""
		case "backspace":
			if r := []rune(w.filter); len(r) > 0 {
				w.filter = string(r[:len(r)-1])
			}
		case "ctrl+u":
			w.filter = ""
		default:
			if k.Text != "" {
				w.filter += k.Text
			}
		}
		w.offset = 0
		return true, nil
	}
	page := max(w.listH-1, 1)
	switch ctx.action(k, keymap.Nav, keymap.Proc, keymap.Global) {
	case "up":
		w.moveSelection(-1)
	case "down":
		w.moveSelection(1)
	case "page_up":
		w.moveSelection(-page)
	case "page_down":
		w.moveSelection(page)
	case "home":
		w.moveSelection(-len(w.rows))
	case "end":
		w.moveSelection(len(w.rows))
	case "left":
		w.cycleSort(-1)
	case "right":
		w.cycleSort(1)
	case "proc_reverse":
		w.desc = !w.desc
	case "proc_tree":
		w.tree = !w.tree
	case "proc_per_core":
		w.perCore = !w.perCore
	case "proc_filter":
		w.filtering = true
	case "proc_collapse":
		if w.tree && w.selPID != 0 {
			w.collapsed[w.selPID] = !w.collapsed[w.selPID]
		}
	case "proc_details":
		if w.selPID == 0 {
			w.moveSelection(0)
		}
		w.details = !w.details
	case "back":
		switch {
		case w.details:
			w.details = false
		case w.filter != "":
			w.filter = ""
		case w.selPID != 0:
			w.selPID = 0
		default:
			return false, nil
		}
	case "proc_kill":
		return true, w.signalCmd(ctx, 0)
	case "proc_kill_force":
		return true, w.signalCmd(ctx, 1)
	case "proc_signal":
		return true, w.chooseSignalCmd(ctx)
	case "proc_renice":
		return true, w.reniceCmd(ctx)
	default:
		return false, nil
	}
	return true, nil
}

// HandleMouse implements Interactive.
func (w *procWidget) HandleMouse(_ *Context, m tea.MouseMsg, x, y int) tea.Cmd {
	switch ev := m.(type) {
	case tea.MouseWheelMsg:
		switch ev.Button {
		case tea.MouseWheelUp:
			w.moveSelection(-3)
		case tea.MouseWheelDown:
			w.moveSelection(3)
		}
	case tea.MouseClickMsg:
		if ev.Button != tea.MouseLeft {
			return nil
		}
		if y == 0 { // header: sort by the clicked column
			for i := len(w.colX) - 1; i >= 0; i-- {
				if w.colX[i] >= 0 && x >= w.colX[i] {
					if w.sortBy == w.cols[i].key {
						w.desc = !w.desc
					} else {
						w.sortBy = w.cols[i].key
					}
					break
				}
			}
			return nil
		}
		idx := w.offset + y - w.listY
		if y >= w.listY && y < w.listY+w.listH && idx < len(w.rows) {
			if w.rows[idx].p.PID == w.selPID {
				w.details = !w.details
			}
			w.selIdx, w.selPID = idx, w.rows[idx].p.PID
		}
	}
	return nil
}

func (w *procWidget) target() (*collect.Process, bool) {
	p := w.selected()
	return p, p != nil
}

func (w *procWidget) signalCmd(ctx *Context, which int) tea.Cmd {
	p, ok := w.target()
	if !ok || ctx.Procs == nil {
		return statusCmd("select a process first (↑/↓)")
	}
	sigs := ctx.Procs.Signals()
	if which >= len(sigs) {
		return nil
	}
	sig := sigs[which]
	pid, name := p.PID, p.Name
	procs := ctx.Procs
	return dialogCmd(&confirmDialog{
		text: fmt.Sprintf("Send %s to %d (%s)?", sig.Name, pid, name),
		onYes: func() tea.Cmd {
			if err := procs.Signal(pid, sig.Number); err != nil {
				return statusCmd(fmt.Sprintf("%s %d: %v", sig.Name, pid, err))
			}
			return statusCmd(fmt.Sprintf("sent %s to %d (%s)", sig.Name, pid, name))
		},
	})
}

func (w *procWidget) chooseSignalCmd(ctx *Context) tea.Cmd {
	p, ok := w.target()
	if !ok || ctx.Procs == nil {
		return statusCmd("select a process first (↑/↓)")
	}
	sigs := ctx.Procs.Signals()
	opts := make([]string, len(sigs))
	for i, s := range sigs {
		opts[i] = fmt.Sprintf("%-8s %2d", s.Name, s.Number)
	}
	pid, name := p.PID, p.Name
	procs := ctx.Procs
	return dialogCmd(&chooseDialog{
		heading: fmt.Sprintf("Signal for %d (%s)", pid, name),
		options: opts,
		onPick: func(i int) tea.Cmd {
			if err := procs.Signal(pid, sigs[i].Number); err != nil {
				return statusCmd(fmt.Sprintf("%s %d: %v", sigs[i].Name, pid, err))
			}
			return statusCmd(fmt.Sprintf("sent %s to %d (%s)", sigs[i].Name, pid, name))
		},
	})
}

func (w *procWidget) reniceCmd(ctx *Context) tea.Cmd {
	p, ok := w.target()
	if !ok || ctx.Procs == nil {
		return statusCmd("select a process first (↑/↓)")
	}
	pid, name, cur := p.PID, p.Name, p.Nice
	procs := ctx.Procs
	return dialogCmd(&inputDialog{
		prompt: fmt.Sprintf("Nice value for %d (%s), now %d [-20..19]:", pid, name, cur),
		value:  strconv.Itoa(int(cur)),
		onSubmit: func(s string) tea.Cmd {
			n, err := strconv.Atoi(strings.TrimSpace(s))
			if err != nil || n < -20 || n > 19 {
				return statusCmd("nice must be an integer between -20 and 19")
			}
			if err := procs.SetNice(pid, n); err != nil {
				return statusCmd(fmt.Sprintf("renice %d: %v", pid, err))
			}
			return statusCmd(fmt.Sprintf("nice of %d (%s) set to %d", pid, name, n))
		},
	})
}
