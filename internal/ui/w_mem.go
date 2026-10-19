package ui

import (
	"github.com/hightemp/smpmon/internal/chart"
	"github.com/hightemp/smpmon/internal/config"
	"github.com/hightemp/smpmon/internal/metrics"
)

type memOptions struct {
	Graph *bool  `yaml:"graph"` // meters style: graph used memory in spare rows (default true)
	Mode  string `yaml:"mode"`
	// Style is graphs (a history graph per row, like btop) or meters.
	Style string `yaml:"style"`
}

type memWidget struct {
	opts memOptions
	buf  []float64
	bufs map[metrics.Key][]float64
}

func init() {
	register("mem", func(n *config.Node) (Widget, error) {
		w := &memWidget{bufs: map[metrics.Key][]float64{}}
		if err := n.DecodeOptions(&w.opts); err != nil {
			return nil, err
		}
		return w, checkStyle(w.opts.Style)
	})
}

func (m *memWidget) Kind() string         { return "mem" }
func (m *memWidget) Collectors() []string { return []string{"mem"} }

func (m *memWidget) Title(ctx *Context) (string, string) {
	total, _ := ctx.Store.Last("mem.total")
	return "mem", fg(ctx.Theme.Dim, metrics.FormatBytes(total))
}

type memRow struct {
	label string
	key   metrics.Key
	grad  chart.Gradient
	total metrics.Key
}

func (m *memWidget) Render(ctx *Context, w, h int) []string {
	t := ctx.Theme
	rows := []memRow{
		{"Used", "mem.used", t.Mem, "mem.total"},
		{"Available", "mem.available", t.Free, "mem.total"},
		{"Cached", "mem.cached", t.Cached, "mem.total"},
		{"Free", "mem.free", t.Free, "mem.total"},
	}
	if st, ok := ctx.Store.Last("swap.total"); ok && st > 0 {
		rows = append(rows, memRow{"Swap", "swap.used", t.Swap, "swap.total"})
	}
	if _, ok := ctx.Store.Last("mem.zfs_arc"); ok {
		rows = append(rows, memRow{"ZFS ARC", "mem.zfs_arc", t.Cached, "mem.total"})
	}
	if len(rows) > h {
		rows = rows[:h]
	}
	if m.opts.Style != "meters" {
		if out := m.renderGraphs(ctx, rows, w, h); out != nil {
			return out
		}
	}

	labelW := 10
	valueW := 10
	meterW := w - labelW - valueW - 7 // percent 6 + space after the meter
	var out []string
	for _, r := range rows {
		v, _ := ctx.Store.Last(r.key)
		total, _ := ctx.Store.Last(r.total)
		f := 0.0
		if total > 0 {
			f = v / total
		}
		line := fg(t.Title, fit(r.label, labelW))
		if meterW >= 4 {
			line += chart.Meter{Width: meterW, Gradient: r.grad, Empty: t.MeterEmpty}.Render(f) + " "
		}
		line += fg(r.grad.At(f), alignRight(metrics.FormatPercent(f*100), 6)) +
			fg(t.Fg, alignRight(metrics.FormatBytes(v), valueW))
		out = append(out, line)
	}

	spare := h - len(out) - 1
	if (m.opts.Graph == nil || *m.opts.Graph) && spare >= 2 {
		g := chart.Graph{Width: w, Height: spare, Mode: graphMode(ctx, m.opts.Mode), Max: 100,
			Series: []chart.Series{{Gradient: t.Mem}}}
		m.buf = ctx.series("mem.used_percent", m.buf[:0], g.Slots())
		g.Series[0].Values = m.buf
		out = append(out, "")
		out = append(out, g.Render().Lines()...)
	}
	return block(out, w, h)
}

// renderGraphs draws each row as "Used:  22.4 GiB" over a history graph
// with the percentage at its right, using one to three graph rows as the
// height allows; with less room each row is a single line with an in-line
// graph. It returns nil when even that doesn't fit.
func (m *memWidget) renderGraphs(ctx *Context, rows []memRow, w, h int) []string {
	t := ctx.Theme
	const labelW, pctW, valueW = 10, 6, 10
	per := h / len(rows)
	inline := w - labelW - pctW - valueW - 1 // width of single-line graphs
	if per < 2 && inline < 4 {
		return nil
	}
	gh := min(per-1, 3)
	var out []string
	for _, r := range rows {
		v, _ := ctx.Store.Last(r.key)
		total, _ := ctx.Store.Last(r.total)
		f := 0.0
		if total > 0 {
			f = v / total
		}
		pct := fg(r.grad.At(f), alignRight(metrics.FormatPercent(f*100), pctW))
		value := fg(t.Fg, metrics.FormatBytes(v))
		if per < 2 {
			g := mini(ctx, m.bufs, r.key, inline, 1, m.opts.Mode, 0, total, r.grad)
			out = append(out, fg(t.Title, fit(r.label, labelW))+g[0]+" "+pct+alignRight(value, valueW))
			continue
		}
		out = append(out, spread(fg(t.Title, r.label+":"), value, w))
		g := mini(ctx, m.bufs, r.key, w-pctW, gh, m.opts.Mode, 0, total, r.grad)
		for i, l := range g {
			if i == 0 {
				l += pct
			}
			out = append(out, l)
		}
	}
	return block(out, w, h)
}
