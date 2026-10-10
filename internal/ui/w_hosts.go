package ui

import (
	"fmt"
	"strconv"

	tea "charm.land/bubbletea/v2"

	"github.com/hightemp/smpmon/internal/chart"
	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/config"
	"github.com/hightemp/smpmon/internal/keymap"
	"github.com/hightemp/smpmon/internal/metrics"
	"github.com/hightemp/smpmon/internal/source"
)

// hostsWidget shows a card per host. It reads every host's store directly,
// so its context's Store is ignored.
type hostsWidget struct {
	sel        int
	cols       int
	cardW      int
	cardH      int
	cardsShown int
}

func init() {
	register("hosts", func(n *config.Node) (Widget, error) {
		return &hostsWidget{}, n.DecodeOptions(&struct{}{})
	})
}

func (h *hostsWidget) Kind() string { return "hosts" }

// Collectors is handled specially by Model.updateDemand: the overview needs
// data from every host.
func (h *hostsWidget) Collectors() []string { return hostsCollectors }

func (h *hostsWidget) Title(ctx *Context) (string, string) {
	online := 0
	for _, hs := range ctx.Hosts {
		if info, _ := hs.conn(); info.State == source.Online {
			online++
		}
	}
	return "hosts", fg(ctx.Theme.Dim, fmt.Sprintf("%d of %d online", online, len(ctx.Hosts)))
}

const minCardW, cardH = 34, 8

func (h *hostsWidget) Render(ctx *Context, w, ht int) []string {
	n := len(ctx.Hosts)
	if n == 0 {
		return block(nil, w, ht)
	}
	h.cols = max(min(w/minCardW, n), 1)
	h.cardW = w / h.cols
	h.cardH = cardH
	rows := max(ht/cardH, 1)
	h.cardsShown = min(n, rows*h.cols)
	h.sel = min(h.sel, n-1)
	// Scroll by rows so the selection is visible.
	firstRow := max(h.sel/h.cols-rows+1, 0)

	out := make([]string, 0, ht)
	for r := 0; r < rows; r++ {
		lines := make([]string, cardH)
		for c := 0; c < h.cols; c++ {
			i := (firstRow+r)*h.cols + c
			cw := h.cardW
			if c == h.cols-1 {
				cw = w - h.cardW*(h.cols-1)
			}
			var card []string
			if i < n {
				card = h.card(ctx, ctx.Hosts[i], i == h.sel, i == hostIndexIn(ctx), cw)
			} else {
				card = block(nil, cw, cardH)
			}
			for y := range lines {
				lines[y] += card[y]
			}
		}
		out = append(out, lines...)
	}
	return block(out, w, ht)
}

func hostIndexIn(ctx *Context) int {
	for i, h := range ctx.Hosts {
		if h.Name == ctx.Host {
			return i
		}
	}
	return -1
}

func (h *hostsWidget) card(ctx *Context, hs HostSource, selected, current bool, w int) []string {
	t := ctx.Theme
	st := hs.Source.Store()
	info, remote := hs.conn()
	color := t.Border
	if selected {
		color = t.Hotkey
	}
	title := fg(t.Title, bold(hs.Name))
	if current {
		title += fg(t.Dim, " (shown)")
	}
	state := fg(t.CPU.At(0), "online")
	switch info.State {
	case source.Connecting:
		state = fg(t.Warn, "connecting")
	case source.Offline:
		state = fg(t.Crit, "offline")
	}
	if remote && info.State == source.Online && info.Latency > 0 {
		state += fg(t.Dim, " "+formatLatency(info.Latency))
	}
	if !remote {
		state = fg(t.Dim, "local")
	}
	f := frame{style: "rounded", color: color, title: title, info: state}
	iw, _ := f.inner(w, cardH)

	meter := func(label string, pct float64, ok bool, grad chart.Gradient, right string) string {
		if !ok {
			return fg(t.Dim, fmt.Sprintf("%-5s", label)+"–")
		}
		mw := iw - 5 - 1 - 7
		line := fg(t.Title, fmt.Sprintf("%-5s", label))
		if mw >= 3 {
			line += chart.Meter{Width: mw, Gradient: grad, Empty: t.MeterEmpty}.Render(pct/100) + " "
		}
		if right == "" {
			right = metrics.FormatPercent(pct)
		}
		return line + fg(grad.At(pct/100), alignRight(right, 6))
	}
	cpu, okCPU := st.Last("cpu.total.usage")
	mem, okMem := st.Last("mem.used_percent")
	disk, okDisk := fullestDisk(st)
	lines := []string{
		meter("CPU", cpu, okCPU, t.CPU, ""),
		meter("MEM", mem, okMem, t.Mem, ""),
		meter("DISK", disk, okDisk, t.Disk, ""),
	}
	rx, okRx := st.Last("net.total.rx_bps")
	tx, _ := st.Last("net.total.tx_bps")
	if okRx {
		lines = append(lines, fg(t.Dim, "NET  ")+fg(t.NetRx.At(1), "▼ "+metrics.FormatRate(rx))+"  "+fg(t.NetTx.At(1), "▲ "+metrics.FormatRate(tx)))
	} else {
		lines = append(lines, fg(t.Dim, "NET  –"))
	}
	load, okLoad := st.Last("load.1")
	up, okUp := st.Last("system.uptime")
	var bottom string
	if okLoad {
		bottom = fg(t.Dim, "load ") + fg(t.Fg, strconv.FormatFloat(load, 'f', 2, 64))
	}
	if okUp {
		bottom += fg(t.Dim, "  up ") + fg(t.Fg, metrics.FormatDuration(up))
	}
	if info.State == source.Offline && info.Err != nil {
		bottom = fg(t.Crit, firstLine(info.Err.Error()))
	} else if sys, ok := metrics.TableAs[collect.SystemInfo](st, collect.TableSystem); ok && sys.OS != "" {
		bottom = spread(bottom, fg(t.Dim, sys.OS), iw)
	}
	lines = append(lines, bottom)
	return f.render(w, cardH, lines)
}

// fullestDisk returns the highest filesystem usage.
func fullestDisk(st *metrics.Store) (float64, bool) {
	fss, ok := metrics.TableAs[[]collect.Filesystem](st, collect.TableFilesystems)
	if !ok || len(fss) == 0 {
		return 0, false
	}
	hi := 0.0
	for _, f := range fss {
		hi = max(hi, f.UsedPercent)
	}
	return hi, true
}

// HandleKey implements Interactive.
func (h *hostsWidget) HandleKey(ctx *Context, k tea.KeyPressMsg) (bool, tea.Cmd) {
	n := len(ctx.Hosts)
	cols := max(h.cols, 1)
	switch ctx.action(k, keymap.Nav, keymap.Global) {
	case "left":
		h.sel = max(h.sel-1, 0)
	case "right":
		h.sel = min(h.sel+1, n-1)
	case "up":
		h.sel = max(h.sel-cols, 0)
	case "down":
		h.sel = min(h.sel+cols, n-1)
	default:
		if k.String() != "enter" {
			return false, nil
		}
		if h.sel < n {
			name := ctx.Hosts[h.sel].Name
			return true, func() tea.Msg { return switchHostMsg{name} }
		}
	}
	return true, nil
}

// HandleMouse implements Interactive: click selects, clicking the selected
// card switches to that host.
func (h *hostsWidget) HandleMouse(ctx *Context, m tea.MouseMsg, x, y int) tea.Cmd {
	click, ok := m.(tea.MouseClickMsg)
	if !ok || click.Button != tea.MouseLeft || h.cardW == 0 {
		return nil
	}
	i := (y/max(h.cardH, 1))*max(h.cols, 1) + x/h.cardW
	if i >= len(ctx.Hosts) {
		return nil
	}
	if i == h.sel {
		name := ctx.Hosts[i].Name
		return func() tea.Msg { return switchHostMsg{name} }
	}
	h.sel = i
	return nil
}

// Footer implements Interactive.
func (h *hostsWidget) Footer(ctx *Context) string {
	return joinHints(hint(ctx, "←↑↓→", "select"), hint(ctx, "enter", "show host"))
}

// Capturing implements Interactive.
func (h *hostsWidget) Capturing() bool { return false }
