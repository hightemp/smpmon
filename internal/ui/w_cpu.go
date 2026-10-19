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
	// Style of the per-core panel: graphs (usage and temperature history
	// per core, like btop) or meters. Graphs fall back to meters when
	// there is no room.
	Style string `yaml:"style"`
}

type cpuWidget struct {
	opts  cpuOptions
	buf   []float64
	cores []metrics.Key
	bufs  map[metrics.Key][]float64 // mini graph buffers
}

func init() {
	register("cpu", func(n *config.Node) (Widget, error) {
		w := &cpuWidget{bufs: map[metrics.Key][]float64{}}
		if err := n.DecodeOptions(&w.opts); err != nil {
			return nil, err
		}
		return w, checkStyle(w.opts.Style)
	})
}

// checkStyle validates the style option of widgets with mini graphs.
func checkStyle(s string) error {
	if s != "" && s != "graphs" && s != "meters" {
		return fmt.Errorf("options.style: want graphs or meters, got %q", s)
	}
	return nil
}

// mini renders a one-or-more-row graph of k's recent values.
func mini(ctx *Context, bufs map[metrics.Key][]float64, k metrics.Key, w, h int, mode string, lo, hi float64, grad chart.Gradient) []string {
	m := graphMode(ctx, mode)
	slots := w
	if m == chart.ModeBraille {
		slots *= 2
	}
	b := ctx.series(k, bufs[k][:0], slots)
	bufs[k] = b
	return chart.Mini(b, w, h, m, lo, hi, grad)
}

var (
	selCores    = metrics.MustSelector("cpu.core.*.usage")
	keyCPUTotal = metrics.Key("cpu.total.usage")
	keyCPUFreq  = metrics.Key("cpu.freq")
)

func (c *cpuWidget) Kind() string         { return "cpu" }
func (c *cpuWidget) Collectors() []string { return []string{"cpu", "load", "sensors"} }

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
	info = fg(ctx.Theme.Dim, info)
	if v, ok := ctx.Store.Last("cpu.temp"); ok {
		info += " " + fg(ctx.Theme.Temp.At(v/100), metrics.Format(v, metrics.UnitCelsius))
	}
	return "cpu", info
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

// coreLayout is one way to draw a core entry, from richest to plainest.
type coreLayout struct {
	graphW, tempGraphW int  // 0 = no graph
	temp               bool // show the temperature
	meterW             int  // meter instead of a usage graph
}

// width of an entry: label 4, graph or meter, percent 5, temperature 5,
// temperature graph with a space before it, and a separator.
func (l coreLayout) width() int {
	w := 4 + l.graphW + l.meterW + 5 + 1
	if l.temp {
		w += 5
	}
	if l.tempGraphW > 0 {
		w += 1 + l.tempGraphW
	}
	return w
}

func (c *cpuWidget) layouts(temps bool) []coreLayout {
	var out []coreLayout
	if c.opts.Style != "meters" {
		if temps {
			out = append(out, coreLayout{graphW: 10, temp: true, tempGraphW: 6}, coreLayout{graphW: 8, temp: true, tempGraphW: 4},
				coreLayout{graphW: 8, temp: true})
		}
		out = append(out, coreLayout{graphW: 10}, coreLayout{graphW: 6})
	}
	return append(out, coreLayout{meterW: 6}, coreLayout{meterW: 4})
}

func (c *cpuWidget) Render(ctx *Context, w, h int) []string {
	t := ctx.Theme
	showCores := c.opts.Cores == nil || *c.opts.Cores
	c.cores = ctx.Store.Keys(selCores)
	temps := false
	for _, k := range c.cores {
		if ctx.Store.Has(coreTempKey(k)) {
			temps = true
			break
		}
	}

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
	var lay coreLayout
	panelW := 22
	limit := w * 55 / 100
	if w < 50 {
		limit = w / 2 // narrow: the graph keeps half
	}
	if n > 0 {
		n = 0 // until a layout fits
		for _, l := range c.layouts(temps) {
			if pw := max(cols*l.width(), 22); pw <= limit {
				lay, panelW, n = l, pw, len(c.cores)
				break
			}
		}
		if n == 0 {
			cols = 0
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
	c.buf = ctx.series(keyCPUTotal, c.buf[:0], g.Slots())
	g.Series[0].Values = c.buf
	canvas := chart.NewCanvas(graphW, h)
	g.Draw(canvas, 0, 0)
	lines := canvas.Lines()

	if panelW <= 0 {
		return lines
	}
	panel := make([]string, h)
	panel[0] = c.totalLine(ctx, panelW)
	if n > 0 {
		perCol := (n + cols - 1) / cols // balanced columns
		for col := 0; col < cols; col++ {
			for r := 0; r < perCol; r++ {
				i := col*perCol + r
				if i >= n || r+1 >= h {
					continue
				}
				panel[r+1] += c.coreEntry(ctx, c.cores[i], lay) + " "
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

// coreTempKey returns cpu.core.<n>.temp for cpu.core.<n>.usage.
func coreTempKey(usage metrics.Key) metrics.Key {
	return metrics.NewKey("cpu", "core", usage.Segments()[2], "temp")
}

// Temperatures are graphed from tempLo, so the usual range fills the graph.
const tempLo, tempHi = 20.0, 100.0

// tempText formats a temperature in 5 cells, colored by heat.
func tempText(ctx *Context, v float64) string {
	return fg(ctx.Theme.Temp.At(v/100), alignRight(strconv.FormatFloat(v, 'f', 0, 64)+"°C", 5))
}

// coreEntry renders "C3 ⣀⣤⣶⣿ 42%  38°C ⣀⣀⣠" (or with a meter).
func (c *cpuWidget) coreEntry(ctx *Context, usage metrics.Key, l coreLayout) string {
	t := ctx.Theme
	v, _ := ctx.Store.Last(usage)
	out := fg(t.Title, fmt.Sprintf("%-4s", "C"+usage.Segments()[2]))
	if l.graphW > 0 {
		out += mini(ctx, c.bufs, usage, l.graphW, 1, c.opts.Mode, 0, 100, t.CPU)[0]
	} else {
		out += chart.Meter{Width: l.meterW, Gradient: t.CPU, Empty: t.MeterEmpty}.Render(v / 100)
	}
	out += fg(t.CPU.At(v/100), alignRight(fmt.Sprintf("%3.0f%%", v), 5))
	if l.temp {
		tk := coreTempKey(usage)
		if tv, ok := ctx.Store.Last(tk); ok {
			out += tempText(ctx, tv)
			if l.tempGraphW > 0 {
				out += " " + mini(ctx, c.bufs, tk, l.tempGraphW, 1, c.opts.Mode, tempLo, tempHi, t.Temp)[0]
			}
		} else {
			out += strings.Repeat(" ", l.width()-width(out)-1)
		}
	}
	return out
}

// totalLine renders "CPU ■■■■■■■■ 19% 38°C ⣀⣀⣠" across the panel.
func (c *cpuWidget) totalLine(ctx *Context, w int) string {
	t := ctx.Theme
	total, _ := ctx.Store.Last(keyCPUTotal)
	right := fg(t.CPU.At(total/100), alignRight(fmt.Sprintf("%3.0f%%", total), 5))
	if tv, ok := ctx.Store.Last("cpu.temp"); ok && w >= 30 {
		right += tempText(ctx, tv)
		if gw := min(6, w-30); c.opts.Style != "meters" && gw >= 3 {
			right += " " + mini(ctx, c.bufs, "cpu.temp", gw, 1, c.opts.Mode, tempLo, tempHi, t.Temp)[0]
		}
	}
	meterW := max(w-4-width(right), 1)
	return fg(t.Title, "CPU ") + chart.Meter{Width: meterW, Gradient: t.CPU, Empty: t.MeterEmpty}.Render(total/100) + right
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
