package ui

import (
	"errors"
	"os"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hightemp/smpmon/internal/chart"
	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/config"
	"github.com/hightemp/smpmon/internal/metrics"
)

// seriesPalette colors multiple series in one graph.
var seriesPalette = []chart.RGB{
	hex("#5fafff"), hex("#ff875f"), hex("#87d787"), hex("#d787d7"),
	hex("#ffd75f"), hex("#5fd7d7"), hex("#ff5f87"), hex("#afaf87"),
}

func parseSelectors(patterns []string) ([]metrics.Selector, error) {
	if len(patterns) == 0 {
		return nil, errors.New("options.metrics: at least one metric selector is required")
	}
	out := make([]metrics.Selector, len(patterns))
	for i, p := range patterns {
		s, err := metrics.ParseSelector(p)
		if err != nil {
			return nil, err
		}
		out[i] = s
	}
	return out, nil
}

func expand(ctx *Context, sels []metrics.Selector, limit int) []metrics.Key {
	var keys []metrics.Key
	for _, s := range sels {
		keys = append(keys, ctx.Store.Keys(s)...)
		if limit > 0 && len(keys) >= limit {
			return keys[:limit]
		}
	}
	return keys
}

func formatKey(ctx *Context, k metrics.Key, v float64) string {
	return metrics.Format(v, ctx.Registry.UnitOf(k))
}

// ---- graph ----

type graphOptions struct {
	Metrics  []string      `yaml:"metrics"`
	Window   time.Duration `yaml:"window"` // time span to show; default fits the width
	Mode     string        `yaml:"mode"`
	Style    string        `yaml:"style"` // area or line; default area for one series
	Scale    string        `yaml:"scale"` // linear or log
	Min      float64       `yaml:"min"`
	Max      float64       `yaml:"max"`
	Gradient string        `yaml:"gradient"` // theme gradient for a single series
	Legend   *bool         `yaml:"legend"`
	Limit    int           `yaml:"limit"` // max series (default 8)
}

type graphWidget struct {
	opts graphOptions
	sels []metrics.Selector
	bufs [][]float64
	tmp  []float64

	// With {gpu} in the metrics the selectors follow the GPU chosen with
	// gpu_next; selGPU is the choice sels were built for.
	perGPU bool
	selGPU int
}

// gpuPlaceholder in a metric stands for the chosen GPU, or all of them.
const gpuPlaceholder = "{gpu}"

func withGPU(patterns []string, gpu int) []string {
	v := "*"
	if gpu >= 0 {
		v = strconv.Itoa(gpu)
	}
	out := make([]string, len(patterns))
	for i, p := range patterns {
		out[i] = strings.ReplaceAll(p, gpuPlaceholder, v)
	}
	return out
}

func init() {
	register("graph", func(n *config.Node) (Widget, error) {
		w := &graphWidget{opts: graphOptions{Limit: 8}}
		if err := n.DecodeOptions(&w.opts); err != nil {
			return nil, err
		}
		w.perGPU = slices.ContainsFunc(w.opts.Metrics, func(m string) bool { return strings.Contains(m, gpuPlaceholder) })
		w.selGPU = -1
		var err error
		w.sels, err = parseSelectors(withGPU(w.opts.Metrics, -1))
		return w, err
	})
}

func (g *graphWidget) Kind() string         { return "graph" }
func (g *graphWidget) Collectors() []string { return collectorsFor(withGPU(g.opts.Metrics, -1)) }
func (g *graphWidget) Title(ctx *Context) (string, string) {
	return "graph", fg(ctx.Theme.Dim, strings.Join(withGPU(g.opts.Metrics, ctx.GPU), " "))
}

func (g *graphWidget) Render(ctx *Context, w, h int) []string {
	t := ctx.Theme
	if g.perGPU && ctx.GPU != g.selGPU {
		if sels, err := parseSelectors(withGPU(g.opts.Metrics, ctx.GPU)); err == nil {
			g.sels, g.selGPU = sels, ctx.GPU
		}
	}
	keys := expand(ctx, g.sels, g.opts.Limit)
	if len(keys) == 0 {
		return block([]string{fg(t.Dim, "no data for "+strings.Join(withGPU(g.opts.Metrics, ctx.GPU), ", "))}, w, h)
	}
	legend := (g.opts.Legend == nil && len(keys) > 1) || (g.opts.Legend != nil && *g.opts.Legend)
	legendRows := 0
	if legend && h > 3 {
		legendRows = min(len(keys), max(h/3, 1))
	}
	gr := chart.Graph{Width: w, Height: h - legendRows, Mode: graphMode(ctx, g.opts.Mode), Min: g.opts.Min, Max: g.opts.Max}
	if gr.Max <= gr.Min {
		gr.Max = sharedMax(ctx, keys)
	}
	if g.opts.Scale == "log" {
		gr.Scale = chart.ScaleLog
	}
	if g.opts.Style == "line" || (g.opts.Style == "" && len(keys) > 1) {
		gr.Style = chart.StyleLine
	}
	samples := windowSamples(ctx, g.opts.Window)
	for len(g.bufs) < len(keys) {
		g.bufs = append(g.bufs, nil)
	}
	for i, k := range keys {
		var vals []float64
		if samples > gr.Slots() && !ctx.History {
			g.tmp = ctx.Store.Series(k, g.tmp[:0], samples)
			vals = chart.Resample(g.bufs[i][:0], g.tmp, gr.Slots(), chart.AggMax)
		} else {
			vals = ctx.series(k, g.bufs[i][:0], gr.Slots())
		}
		g.bufs[i] = vals
		grd := chart.Gradient{seriesPalette[i%len(seriesPalette)]}
		if len(keys) == 1 {
			grd = t.Gradient(g.opts.Gradient)
		}
		gr.Series = append(gr.Series, chart.Series{Values: vals, Gradient: grd})
	}
	if gr.Max <= gr.Min { // auto scale with headroom so flat lines don't fill the graph
		_, hi := gr.Range()
		gr.Max = gr.Min + (hi-gr.Min)*1.25
	}
	out := gr.Render().Lines()
	for i := 0; i < legendRows; i++ {
		k := keys[i]
		line := fg(seriesPalette[i%len(seriesPalette)], "■ ") + fg(t.Fg, string(k))
		if v, ok := ctx.Store.Last(k); ok {
			line = spread(line, fg(t.Title, formatKey(ctx, k, v)), w)
		}
		out = append(out, line)
	}
	return block(out, w, h)
}

// sharedMax returns the natural maximum shared by all keys (e.g. 100 for
// percentages), or 0 if they have none.
func sharedMax(ctx *Context, keys []metrics.Key) float64 {
	hi := 0.0
	for i, k := range keys {
		d := ctx.Registry.Lookup(k)
		if d == nil || d.Max <= 0 || (i > 0 && d.Max != hi) {
			return 0
		}
		hi = d.Max
	}
	return hi
}

// ---- gauge ----

type gaugeOptions struct {
	Metric   string  `yaml:"metric"`
	Max      float64 `yaml:"max"` // default from the metric (100 for percentages)
	Gradient string  `yaml:"gradient"`
}

type gaugeWidget struct {
	opts gaugeOptions
	key  metrics.Key
}

func init() {
	register("gauge", func(n *config.Node) (Widget, error) {
		w := &gaugeWidget{}
		if err := n.DecodeOptions(&w.opts); err != nil {
			return nil, err
		}
		if w.opts.Metric == "" {
			return nil, errors.New("options.metric is required")
		}
		w.key = metrics.Key(w.opts.Metric)
		return w, nil
	})
}

func (g *gaugeWidget) Kind() string         { return "gauge" }
func (g *gaugeWidget) Collectors() []string { return collectorsFor([]string{g.opts.Metric}) }
func (g *gaugeWidget) Title(*Context) (string, string) {
	return g.opts.Metric, ""
}

func maxFor(ctx *Context, k metrics.Key, override float64) float64 {
	if override > 0 {
		return override
	}
	if d := ctx.Registry.Lookup(k); d != nil && d.Max > 0 {
		return d.Max
	}
	return 0
}

func (g *gaugeWidget) Render(ctx *Context, w, h int) []string {
	t := ctx.Theme
	v, ok := ctx.Store.Last(g.key)
	if !ok {
		return block([]string{fg(t.Dim, "no data")}, w, h)
	}
	hi := maxFor(ctx, g.key, g.opts.Max)
	f := 0.0
	if hi > 0 {
		f = v / hi
	}
	grd := t.Gradient(g.opts.Gradient)
	val := fg(grd.At(f), bold(formatKey(ctx, g.key, v)))
	meter := chart.Meter{Width: w, Gradient: grd, Empty: t.MeterEmpty}.Render(f)
	var out []string
	pad := max((h-2)/2, 0)
	for range pad {
		out = append(out, "")
	}
	out = append(out, alignRight(val, (w+width(val))/2), meter)
	return block(out, w, h)
}

// ---- stat ----

type statOptions struct {
	Metric    string `yaml:"metric"`
	Sparkline *bool  `yaml:"sparkline"`
	Gradient  string `yaml:"gradient"`
}

type statWidget struct {
	opts statOptions
	key  metrics.Key
	buf  []float64
}

func init() {
	register("stat", func(n *config.Node) (Widget, error) {
		w := &statWidget{}
		if err := n.DecodeOptions(&w.opts); err != nil {
			return nil, err
		}
		if w.opts.Metric == "" {
			return nil, errors.New("options.metric is required")
		}
		w.key = metrics.Key(w.opts.Metric)
		return w, nil
	})
}

func (s *statWidget) Kind() string         { return "stat" }
func (s *statWidget) Collectors() []string { return collectorsFor([]string{s.opts.Metric}) }
func (s *statWidget) Title(*Context) (string, string) {
	return s.opts.Metric, ""
}

func (s *statWidget) Render(ctx *Context, w, h int) []string {
	t := ctx.Theme
	v, ok := ctx.Store.Last(s.key)
	if !ok {
		return block([]string{fg(t.Dim, "no data")}, w, h)
	}
	hi := maxFor(ctx, s.key, 0)
	grd := t.Gradient(s.opts.Gradient)
	f := 0.5
	if hi > 0 {
		f = v / hi
	}
	text := fg(grd.At(f), bold(formatKey(ctx, s.key, v)))
	out := []string{alignRight(text, (w+width(text))/2)}
	if (s.opts.Sparkline == nil || *s.opts.Sparkline) && h >= 2 {
		s.buf = ctx.series(s.key, s.buf[:0], w)
		out = append(out, chart.Sparkline(s.buf, w, 0, hi, grd))
	}
	return block(out, w, h)
}

// ---- table ----

type tableOptions struct {
	Metrics []string `yaml:"metrics"`
}

type tableWidget struct {
	opts tableOptions
	sels []metrics.Selector
}

func init() {
	register("table", func(n *config.Node) (Widget, error) {
		w := &tableWidget{}
		if err := n.DecodeOptions(&w.opts); err != nil {
			return nil, err
		}
		var err error
		w.sels, err = parseSelectors(w.opts.Metrics)
		return w, err
	})
}

func (tw *tableWidget) Kind() string         { return "table" }
func (tw *tableWidget) Collectors() []string { return collectorsFor(tw.opts.Metrics) }
func (tw *tableWidget) Title(*Context) (string, string) {
	return "table", ""
}

func (tw *tableWidget) Render(ctx *Context, w, h int) []string {
	t := ctx.Theme
	keys := expand(ctx, tw.sels, h)
	prefix := commonPrefix(keys)
	var out []string
	for _, k := range keys {
		v, _ := ctx.Store.Last(k)
		name := strings.TrimPrefix(string(k), prefix)
		out = append(out, spread(fg(t.Fg, name), fg(t.Title, formatKey(ctx, k, v)), w))
	}
	if len(keys) == 0 {
		out = append(out, fg(t.Dim, "no data"))
	}
	return block(out, w, h)
}

// commonPrefix returns the shared leading key segments (with trailing dot).
func commonPrefix(keys []metrics.Key) string {
	if len(keys) < 2 {
		return ""
	}
	p := string(keys[0])
	for _, k := range keys[1:] {
		for !strings.HasPrefix(string(k), p) {
			i := strings.LastIndex(strings.TrimSuffix(p, "."), ".")
			if i < 0 {
				return ""
			}
			p = p[:i+1]
		}
	}
	if !strings.HasSuffix(p, ".") {
		if i := strings.LastIndex(p, "."); i >= 0 {
			return p[:i+1]
		}
		return ""
	}
	return p
}

// ---- text ----

type textOptions struct {
	// Text may contain {hostname}, {os}, {kernel}, {uptime}, {time} and {date}.
	Text  string `yaml:"text"`
	Align string `yaml:"align"` // left, center or right
}

type textWidget struct {
	opts textOptions
}

func init() {
	register("text", func(n *config.Node) (Widget, error) {
		w := &textWidget{opts: textOptions{Text: "{hostname}  {os}  up {uptime}", Align: "center"}}
		return w, n.DecodeOptions(&w.opts)
	})
}

func (tw *textWidget) Kind() string                    { return "text" }
func (tw *textWidget) Collectors() []string            { return []string{"load"} }
func (tw *textWidget) Title(*Context) (string, string) { return "", "" }

func (tw *textWidget) Render(ctx *Context, w, h int) []string {
	info, _ := metrics.TableAs[collect.SystemInfo](ctx.Store, collect.TableSystem)
	host := info.Hostname
	if host == "" {
		host, _ = os.Hostname()
	}
	up, _ := ctx.Store.Last("system.uptime")
	r := strings.NewReplacer(
		"{hostname}", host, "{os}", info.OS, "{kernel}", info.Kernel,
		"{uptime}", metrics.FormatDuration(up),
		"{time}", ctx.Now.Format("15:04:05"), "{date}", ctx.Now.Format("2006-01-02"),
	)
	var out []string
	for _, l := range strings.Split(r.Replace(tw.opts.Text), "\n") {
		l = fg(ctx.Theme.Fg, l)
		switch tw.opts.Align {
		case "right":
			l = alignRight(l, w)
		case "center":
			l = alignRight(l, (w+width(l))/2)
		}
		out = append(out, l)
	}
	return block(out, w, h)
}
