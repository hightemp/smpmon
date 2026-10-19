package ui

import (
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/hightemp/smpmon/internal/chart"
	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/config"
	"github.com/hightemp/smpmon/internal/metrics"
)

type gpuOptions struct {
	Index *int   `yaml:"index"` // show only this GPU
	Mode  string `yaml:"mode"`
	// View is meters (bars and a usage graph) or graphs (nvtop style: a
	// usage and memory graph per card with the readings above it).
	View string `yaml:"view"`
}

type gpuWidget struct {
	opts        gpuOptions
	buf, memBuf []float64
}

func init() {
	register("gpu", func(n *config.Node) (Widget, error) {
		w := &gpuWidget{}
		if err := n.DecodeOptions(&w.opts); err != nil {
			return nil, err
		}
		if !slices.Contains([]string{"", "meters", "graphs"}, w.opts.View) {
			return nil, fmt.Errorf("options.view: want meters or graphs, got %q", w.opts.View)
		}
		return w, nil
	})
}

func (g *gpuWidget) Kind() string         { return "gpu" }
func (g *gpuWidget) Collectors() []string { return []string{"gpu"} }

// pickGPUs returns the cards to show: the configured index, else the one
// chosen with gpu_next, else all.
func pickGPUs(ctx *Context, index *int) (shown, all []collect.GPU) {
	all, _ = metrics.TableAs[[]collect.GPU](ctx.Store, collect.TableGPUs)
	want := ctx.GPU
	if index != nil {
		want = *index
	}
	if want >= 0 {
		for _, x := range all {
			if x.Index == want {
				return []collect.GPU{x}, all
			}
		}
		if index != nil {
			return nil, all
		}
	}
	return all, all
}

func (g *gpuWidget) Title(ctx *Context) (string, string) {
	gs, all := pickGPUs(ctx, g.opts.Index)
	t := ctx.Theme
	switch {
	case len(gs) == 1 && len(all) > 1 && g.opts.Index == nil:
		return "gpu", fg(t.Dim, fmt.Sprintf("%d of %d ", gs[0].Index+1, len(all))) + keyHint(ctx, "gpu_next", "next")
	case len(gs) == 1 && g.opts.View == "graphs":
		return "gpu", "" // the name is in the headline
	case len(gs) == 1:
		return "gpu", fg(t.Dim, gs[0].Name)
	case len(gs) > 1:
		return "gpu", fg(t.Dim, fmt.Sprintf("%d GPUs ", len(gs))) + keyHint(ctx, "gpu_next", "pick")
	}
	return "gpu", ""
}

func (g *gpuWidget) Render(ctx *Context, w, h int) []string {
	t := ctx.Theme
	gs, _ := pickGPUs(ctx, g.opts.Index)
	if len(gs) == 0 {
		return block([]string{fg(t.Dim, "no GPU found")}, w, h)
	}
	per := max(h/len(gs), 1)
	var out []string
	for i, gpu := range gs {
		rows := per
		if i == len(gs)-1 {
			rows = h - len(out)
		}
		if g.opts.View == "graphs" && rows >= 4 {
			out = append(out, g.renderGraphs(ctx, gpu, w, rows)...)
		} else {
			out = append(out, g.renderMeters(ctx, gpu, w, rows, len(gs) > 1)...)
		}
	}
	return block(out, w, h)
}

// gpuHeadline is the name with link, P-state and throttling.
func gpuHeadline(ctx *Context, gpu collect.GPU, w int) string {
	t := ctx.Theme
	parts := []string{fg(t.Title, bold(fmt.Sprintf("%d %s", gpu.Index, gpu.Name)))}
	if gpu.PCIeGen > 0 && gpu.PCIeWidth > 0 {
		parts = append(parts, fg(t.Dim, fmt.Sprintf("PCIe Gen%d x%d", gpu.PCIeGen, gpu.PCIeWidth)))
	}
	if gpu.PState >= 0 {
		parts = append(parts, fg(t.Dim, "P"+strconv.Itoa(gpu.PState)))
	}
	right := ""
	if slowed := slowdowns(gpu.Throttle); len(slowed) > 0 {
		right = fg(t.Warn, "⚠ "+strings.Join(slowed, ", "))
	}
	return spread(fitEllipsis(strings.Join(parts, "  "), w-width(right)-1), right, w)
}

// slowdowns filters throttle reasons to the ones that hold the card back.
func slowdowns(reasons []string) []string {
	var out []string
	for _, r := range reasons {
		switch r {
		case "idle", "app clocks", "display clocks", "sync boost":
		default:
			out = append(out, r)
		}
	}
	return out
}

// gpuReadings formats the current values in a line.
func gpuReadings(ctx *Context, gpu collect.GPU) []string {
	t := ctx.Theme
	var info []string
	if gpu.Temp >= 0 {
		info = append(info, fg(t.Temp.At(gpu.Temp/100), metrics.Format(gpu.Temp, metrics.UnitCelsius)))
	}
	if gpu.Power >= 0 {
		p := strconv.FormatFloat(gpu.Power, 'f', 0, 64) + " W"
		if gpu.PowerLimit > 0 {
			p += "/" + strconv.FormatFloat(gpu.PowerLimit, 'f', 0, 64) + " W"
		}
		info = append(info, fg(t.Fg, p))
	}
	if gpu.Clock >= 0 {
		info = append(info, fg(t.Dim, "core ")+fg(t.Fg, metrics.FormatHertz(gpu.Clock)))
	}
	if gpu.MemClock >= 0 {
		info = append(info, fg(t.Dim, "mem ")+fg(t.Fg, metrics.FormatHertz(gpu.MemClock)))
	}
	if gpu.Fan >= 0 {
		info = append(info, fg(t.Dim, "fan ")+fg(t.Fg, metrics.FormatPercent(gpu.Fan)))
	}
	if gpu.Enc >= 0 {
		info = append(info, fg(t.Dim, "enc ")+fg(t.Fg, metrics.FormatPercent(gpu.Enc)))
	}
	if gpu.Dec >= 0 {
		info = append(info, fg(t.Dim, "dec ")+fg(t.Fg, metrics.FormatPercent(gpu.Dec)))
	}
	return info
}

// joinFit joins parts with sep, dropping those that don't fit from the end.
func joinFit(parts []string, sep string, w int) string {
	out := ""
	for _, p := range parts {
		next := p
		if out != "" {
			next = out + sep + p
		}
		if width(next) > w {
			break
		}
		out = next
	}
	return out
}

// renderGraphs draws one card nvtop style: headline, readings, then a GPU
// usage graph and a memory graph, side by side or, when narrow, stacked.
func (g *gpuWidget) renderGraphs(ctx *Context, gpu collect.GPU, w, h int) []string {
	t := ctx.Theme
	out := []string{gpuHeadline(ctx, gpu, w)}
	if info := gpuReadings(ctx, gpu); len(info) > 0 {
		out = append(out, joinFit(info, "  ", w))
	}
	gh := h - len(out)
	idx := strconv.Itoa(gpu.Index)
	gpuLabel := fg(t.Title, "GPU ")
	if gpu.Util >= 0 {
		gpuLabel += fg(t.Fg, metrics.FormatPercent(gpu.Util))
	}
	memLabel := fg(t.Title, "VRAM ")
	if gpu.MemTotal > 0 {
		memLabel += fg(t.Fg, metrics.FormatBytes(float64(gpu.MemUsed))+"/"+metrics.FormatBytes(float64(gpu.MemTotal)))
	}
	graph := func(key string, buf *[]float64, grad chart.Gradient, label string, gw, gh int) []string {
		gr := chart.Graph{Width: gw, Height: gh, Mode: graphMode(ctx, g.opts.Mode), Max: 100}
		*buf = ctx.series(metrics.NewKey("gpu", idx, key), (*buf)[:0], gr.Slots())
		gr.Series = []chart.Series{{Values: *buf, Gradient: grad}}
		lines := gr.Render().Lines()
		// The label replaces the left part of the top row; the graph shows
		// through to its right.
		if lw := width(label); lw+1 < gw {
			lines[0] = label + " " + cutLeft(lines[0], lw+1)
		} else {
			lines[0] = fitEllipsis(label, gw)
		}
		return lines
	}
	// Stacked graphs need two rows each to be more than their labels.
	if w >= 80 || gh < 4 {
		lw := (w - 1) / 2
		left := graph("util", &g.buf, t.CPU, gpuLabel, lw, gh)
		right := graph("mem_used_percent", &g.memBuf, t.Mem, memLabel, w-lw-1, gh)
		for i := range left {
			out = append(out, left[i]+" "+right[i])
		}
	} else {
		top := gh / 2
		out = append(out, graph("util", &g.buf, t.CPU, gpuLabel, w, top)...)
		out = append(out, graph("mem_used_percent", &g.memBuf, t.Mem, memLabel, w, gh-top)...)
	}
	return block(out, w, h)
}

// renderMeters draws one card as bars, readings and a usage graph.
func (g *gpuWidget) renderMeters(ctx *Context, gpu collect.GPU, w, h int, showName bool) []string {
	t := ctx.Theme
	var out []string
	if showName {
		out = append(out, gpuHeadline(ctx, gpu, w))
	}
	bar := func(label string, f float64, right string, grad chart.Gradient) string {
		meterW := w - 6 - 1 - max(width(right), 6) - 1
		line := fg(t.Title, fmt.Sprintf("%-5s ", label))
		if meterW >= 3 {
			line += chart.Meter{Width: meterW, Gradient: grad, Empty: t.MeterEmpty}.Render(f) + " "
		}
		return line + fg(grad.At(f), alignRight(right, max(width(right), 6)))
	}
	if gpu.Util >= 0 {
		out = append(out, bar("GPU", gpu.Util/100, metrics.FormatPercent(gpu.Util), t.CPU))
	}
	if gpu.MemTotal > 0 {
		f := float64(gpu.MemUsed) / float64(gpu.MemTotal)
		out = append(out, bar("VRAM", f, metrics.FormatBytes(float64(gpu.MemUsed))+"/"+metrics.FormatBytes(float64(gpu.MemTotal)), t.Mem))
	}
	if info := gpuReadings(ctx, gpu); len(info) > 0 {
		out = append(out, joinFit(info, "  ", w))
	}
	if gh := h - len(out); gh >= 2 && gpu.Util >= 0 {
		gr := chart.Graph{Width: w, Height: gh, Mode: graphMode(ctx, g.opts.Mode), Max: 100,
			Series: []chart.Series{{Gradient: t.CPU}}}
		g.buf = ctx.series(metrics.NewKey("gpu", strconv.Itoa(gpu.Index), "util"), g.buf[:0], gr.Slots())
		gr.Series[0].Values = g.buf
		out = append(out, gr.Render().Lines()...)
	}
	return block(out, w, h)
}

// nextGPU cycles gpu_next: all cards, then each one, then all again.
func (m *Model) nextGPU() {
	gpus, _ := metrics.TableAs[[]collect.GPU](m.ctx.Store, collect.TableGPUs)
	if len(gpus) < 2 {
		m.gpuPick = 0
		m.ctx.GPU = -1
		m.setStatus("only one GPU here")
		return
	}
	cur := slices.IndexFunc(gpus, func(g collect.GPU) bool { return g.Index == m.ctx.GPU })
	if cur+1 >= len(gpus) {
		m.ctx.GPU = -1
		m.setStatus("showing all GPUs")
	} else {
		m.ctx.GPU = gpus[cur+1].Index
		m.setStatus(fmt.Sprintf("showing GPU %d: %s", gpus[cur+1].Index, gpus[cur+1].Name))
	}
	m.gpuPick = m.ctx.GPU + 1
	m.frozen = ""
}
