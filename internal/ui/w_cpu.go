package ui

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/hightemp/smpmon/internal/chart"
	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/config"
	"github.com/hightemp/smpmon/internal/metrics"
)

type cpuOptions struct {
	Cores *bool  `yaml:"cores"` // show per-core panel (default true)
	Mode  string `yaml:"mode"`  // graph mode override
}

type cpuWidget struct {
	opts  cpuOptions
	buf   []float64
	cores []metrics.Key
}

func init() {
	register("cpu", func(n *config.Node) (Widget, error) {
		w := &cpuWidget{}
		return w, n.DecodeOptions(&w.opts)
	})
}

var (
	selCores    = metrics.MustSelector("cpu.core.*.usage")
	keyCPUTotal = metrics.Key("cpu.total.usage")
	keyCPUFreq  = metrics.Key("cpu.freq")
)

func (c *cpuWidget) Kind() string         { return "cpu" }
func (c *cpuWidget) Collectors() []string { return []string{"cpu", "load"} }

func (c *cpuWidget) Title(ctx *Context) (string, string) {
	info := ""
	if ci, ok := metrics.TableAs[collect.CPUInfo](ctx.Store, collect.TableCPUInfo); ok {
		info = shortModel(ci.Model)
	}
	if f, ok := ctx.Store.Last(keyCPUFreq); ok {
		if info != "" {
			info += " "
		}
		info += metrics.FormatHertz(f)
	}
	return "cpu", fg(ctx.Theme.Dim, info)
}

// shortModel trims vendor noise from CPU model names.
func shortModel(m string) string {
	for _, junk := range []string{"(R)", "(TM)", "(tm)", " CPU", " Processor", "-Core", " with Radeon Graphics"} {
		m = strings.ReplaceAll(m, junk, "")
	}
	if i := strings.Index(m, " @ "); i >= 0 {
		m = m[:i]
	}
	m = strings.TrimPrefix(m, "13th Gen ")
	m = strings.TrimPrefix(m, "12th Gen ")
	return strings.Join(strings.Fields(m), " ")
}

const coreEntryMin = 14 // "C1  ■■■■ 100% "

func (c *cpuWidget) Render(ctx *Context, w, h int) []string {
	t := ctx.Theme
	showCores := c.opts.Cores == nil || *c.opts.Cores
	c.cores = ctx.Store.Keys(selCores)

	// Side panel: total, cores (in columns), load average.
	rows := max(h-2, 1)
	n := len(c.cores)
	if !showCores {
		n = 0
	}
	cols := 0
	if n > 0 {
		cols = (n + rows - 1) / rows
	}
	entry := 16
	panelW := max(cols*entry, 22)
	if panelW > w*55/100 { // cores don't fit comfortably: shrink meters, then drop
		entry = coreEntryMin
		panelW = max(cols*entry, 22)
		if panelW > w*55/100 {
			n, cols, panelW = 0, 0, 22
		}
	}
	if panelW > w/2 && w < 50 {
		panelW = 0 // too narrow: graph only
	}
	graphW := w - panelW
	if panelW > 0 {
		graphW-- // gap
	}

	g := chart.Graph{
		Width: graphW, Height: h, Mode: graphMode(ctx, c.opts.Mode), Max: 100,
		Series: []chart.Series{{Gradient: t.CPU}},
	}
	c.buf = ctx.Store.Series(keyCPUTotal, c.buf[:0], g.Slots())
	g.Series[0].Values = c.buf
	canvas := chart.NewCanvas(graphW, h)
	g.Draw(canvas, 0, 0)
	lines := canvas.Lines()

	if panelW <= 0 {
		return lines
	}
	panel := make([]string, h)
	total, _ := ctx.Store.Last(keyCPUTotal)
	meterW := panelW - 10
	panel[0] = c.coreLine(ctx, "CPU", total, meterW)
	if n > 0 {
		meterW = entry - 10 // label 4 + meter + percent 5 + separator 1
		for col := 0; col < cols; col++ {
			for r := 0; r < rows; r++ {
				i := col*rows + r
				if i >= n || r+1 >= h {
					continue
				}
				v, _ := ctx.Store.Last(c.cores[i])
				label := "C" + c.cores[i].Segments()[2]
				panel[r+1] += c.coreLine(ctx, label, v, meterW) + " "
			}
		}
	}
	if h >= 2 {
		panel[h-1] = c.loadLine(ctx, panelW)
	}

	out := make([]string, h)
	for i := range out {
		out[i] = lines[i] + " " + fit(panel[i], panelW)
	}
	return out
}

// coreLine renders "C3 ■■■■■ 42%".
func (c *cpuWidget) coreLine(ctx *Context, label string, v float64, meterW int) string {
	t := ctx.Theme
	m := chart.Meter{Width: max(meterW, 1), Gradient: t.CPU, Empty: t.MeterEmpty}
	pct := fmt.Sprintf("%3.0f%%", v)
	return fg(t.Title, fmt.Sprintf("%-4s", label)) + m.Render(v/100) + fg(t.CPU.At(v/100), alignRight(pct, 5))
}

func (c *cpuWidget) loadLine(ctx *Context, w int) string {
	var parts []string
	for _, k := range []metrics.Key{"load.1", "load.5", "load.15"} {
		if v, ok := ctx.Store.Last(k); ok {
			parts = append(parts, strconv.FormatFloat(v, 'f', 2, 64))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	up := ""
	if v, ok := ctx.Store.Last("system.uptime"); ok {
		up = "up " + metrics.FormatDuration(v)
	}
	return spread(fg(ctx.Theme.Dim, "load ")+fg(ctx.Theme.Fg, strings.Join(parts, " ")), fg(ctx.Theme.Dim, up), w)
}
