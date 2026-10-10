package ui

import (
	"cmp"
	"fmt"
	"slices"
	"strconv"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/hightemp/smpmon/internal/chart"
	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/config"
	"github.com/hightemp/smpmon/internal/keymap"
	"github.com/hightemp/smpmon/internal/metrics"
)

// ---- gpu ----

type gpuOptions struct {
	Index *int   `yaml:"index"` // show only this GPU
	Mode  string `yaml:"mode"`
}

type gpuWidget struct {
	opts gpuOptions
	buf  []float64
}

func init() {
	register("gpu", func(n *config.Node) (Widget, error) {
		w := &gpuWidget{}
		return w, n.DecodeOptions(&w.opts)
	})
}

func (g *gpuWidget) Kind() string         { return "gpu" }
func (g *gpuWidget) Collectors() []string { return []string{"gpu"} }

func (g *gpuWidget) gpus(ctx *Context) []collect.GPU {
	all, _ := metrics.TableAs[[]collect.GPU](ctx.Store, collect.TableGPUs)
	if g.opts.Index == nil {
		return all
	}
	for _, x := range all {
		if x.Index == *g.opts.Index {
			return []collect.GPU{x}
		}
	}
	return nil
}

func (g *gpuWidget) Title(ctx *Context) (string, string) {
	gs := g.gpus(ctx)
	if len(gs) == 1 {
		return "gpu", fg(ctx.Theme.Dim, gs[0].Name)
	}
	return "gpu", fg(ctx.Theme.Dim, fmt.Sprintf("%d GPUs", len(gs)))
}

func (g *gpuWidget) Render(ctx *Context, w, h int) []string {
	t := ctx.Theme
	gs := g.gpus(ctx)
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
		out = append(out, g.renderOne(ctx, gpu, w, rows, len(gs) > 1)...)
	}
	return block(out, w, h)
}

func (g *gpuWidget) renderOne(ctx *Context, gpu collect.GPU, w, h int, showName bool) []string {
	t := ctx.Theme
	var out []string
	if showName {
		out = append(out, fg(t.Title, fitEllipsis(fmt.Sprintf("%d %s", gpu.Index, gpu.Name), w)))
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
	if len(info) > 0 {
		out = append(out, strings.Join(info, "  "))
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

// ---- sensors ----

type sensorsOptions struct {
	Filter string `yaml:"filter"` // substring of chip or label
	Fans   *bool  `yaml:"fans"`   // include fans (default true)
}

type sensorsWidget struct {
	opts   sensorsOptions
	offset int
}

func init() {
	register("sensors", func(n *config.Node) (Widget, error) {
		w := &sensorsWidget{}
		return w, n.DecodeOptions(&w.opts)
	})
}

func (s *sensorsWidget) Kind() string         { return "sensors" }
func (s *sensorsWidget) Collectors() []string { return []string{"sensors"} }
func (s *sensorsWidget) Title(ctx *Context) (string, string) {
	if v, ok := ctx.Store.Last("cpu.temp"); ok {
		return "sensors", fg(ctx.Theme.Dim, "cpu ") + fg(ctx.Theme.Temp.At(v/100), metrics.Format(v, metrics.UnitCelsius))
	}
	return "sensors", ""
}

func (s *sensorsWidget) rows(ctx *Context) []collect.Sensor {
	all, _ := metrics.TableAs[[]collect.Sensor](ctx.Store, collect.TableSensors)
	f := strings.ToLower(s.opts.Filter)
	var out []collect.Sensor
	for _, x := range all {
		if x.Kind == collect.SensorFan && s.opts.Fans != nil && !*s.opts.Fans {
			continue
		}
		if f != "" && !strings.Contains(strings.ToLower(x.Chip+" "+x.Label), f) {
			continue
		}
		out = append(out, x)
	}
	return out
}

func (s *sensorsWidget) Render(ctx *Context, w, h int) []string {
	t := ctx.Theme
	rows := s.rows(ctx)
	if len(rows) == 0 {
		return block([]string{fg(t.Dim, "no sensors")}, w, h)
	}
	s.offset = max(min(s.offset, len(rows)-h), 0)
	labelW := 0
	for _, x := range rows {
		labelW = max(labelW, width(x.Label))
	}
	labelW = min(labelW, max(w/3, 8))
	var out []string
	lastChip := ""
	for _, x := range rows[s.offset:] {
		if len(out) >= h {
			break
		}
		chip := ""
		if x.Chip != lastChip {
			chip, lastChip = x.Chip, x.Chip
		}
		label := fg(t.Dim, fit(fitEllipsis(chip, 10), 10)) + " " + fg(t.Fg, fitEllipsis(x.Label, labelW))
		var value string
		if x.Kind == collect.SensorFan {
			value = fg(t.Fg, metrics.Format(x.Value, metrics.UnitRPM))
		} else {
			hi := cmp.Or(x.Crit, x.High, 100)
			color := t.Temp.At(x.Value / hi)
			if x.Crit > 0 && x.Value >= x.Crit {
				color = t.Crit
			} else if x.High > 0 && x.Value >= x.High {
				color = t.Warn
			}
			value = fg(color, metrics.Format(x.Value, metrics.UnitCelsius))
			if mw := w - width(label) - 8 - 2; mw >= 6 {
				meter := chart.Meter{Width: min(mw, 20), Gradient: t.Temp, Empty: t.MeterEmpty}.Render(x.Value / hi)
				value = meter + " " + alignRight(value, 6)
			}
		}
		out = append(out, spread(label, value, w))
	}
	return block(out, w, h)
}

// HandleKey implements Interactive: scroll.
func (s *sensorsWidget) HandleKey(ctx *Context, k tea.KeyPressMsg) (bool, tea.Cmd) {
	switch ctx.action(k, keymap.Nav) {
	case "up":
		s.offset = max(s.offset-1, 0)
	case "down":
		s.offset++
	default:
		return false, nil
	}
	return true, nil
}

// HandleMouse implements Interactive.
func (s *sensorsWidget) HandleMouse(_ *Context, m tea.MouseMsg, _, _ int) tea.Cmd {
	if wm, ok := m.(tea.MouseWheelMsg); ok {
		if wm.Button == tea.MouseWheelUp {
			s.offset = max(s.offset-1, 0)
		} else {
			s.offset++
		}
	}
	return nil
}

// Footer implements Interactive.
func (s *sensorsWidget) Footer(ctx *Context) string { return hint(ctx, "↑↓", "scroll") }

// Capturing implements Interactive.
func (s *sensorsWidget) Capturing() bool { return false }

// ---- battery ----

type batteryWidget struct{}

func init() {
	register("battery", func(n *config.Node) (Widget, error) {
		return &batteryWidget{}, n.DecodeOptions(&struct{}{})
	})
}

func (b *batteryWidget) Kind() string         { return "battery" }
func (b *batteryWidget) Collectors() []string { return []string{"battery"} }
func (b *batteryWidget) Title(*Context) (string, string) {
	return "battery", ""
}

func (b *batteryWidget) Render(ctx *Context, w, h int) []string {
	t := ctx.Theme
	bats, _ := metrics.TableAs[[]collect.Battery](ctx.Store, collect.TableBatteries)
	if len(bats) == 0 {
		return block([]string{fg(t.Dim, "no battery")}, w, h)
	}
	var out []string
	for _, bat := range bats {
		f := bat.Percent / 100
		// Low charge is the alarming end, so the gradient runs backwards.
		color := t.CPU.At(1 - f)
		head := fg(color, bold(metrics.FormatPercent(bat.Percent))) + " " + fg(t.Fg, bat.Status)
		var extra []string
		if bat.TimeLeft > 0 {
			verb := "left"
			if bat.Status == "charging" {
				verb = "to full"
			}
			extra = append(extra, metrics.FormatDuration(bat.TimeLeft.Seconds())+" "+verb)
		}
		if bat.Power > 0 {
			extra = append(extra, strconv.FormatFloat(bat.Power, 'f', 1, 64)+" W")
		}
		out = append(out, spread(head, fg(t.Dim, strings.Join(extra, "  ")), w))
		meter := chart.Meter{Width: w, Gradient: chart.Gradient{t.CPU.At(1), t.CPU.At(0.5), t.CPU.At(0)}, Empty: t.MeterEmpty}
		out = append(out, meter.Render(f))
		if len(bats) > 1 {
			out[len(out)-2] = fg(t.Dim, bat.Name+" ") + out[len(out)-2]
		}
	}
	return block(out, w, h)
}

// ---- docker ----

type dockerOptions struct {
	All  bool   `yaml:"all"`  // include stopped containers
	Sort string `yaml:"sort"` // cpu, mem, name, net
}

type dockerWidget struct {
	opts   dockerOptions
	offset int
}

func init() {
	register("docker", func(n *config.Node) (Widget, error) {
		w := &dockerWidget{opts: dockerOptions{Sort: "cpu"}}
		if err := n.DecodeOptions(&w.opts); err != nil {
			return nil, err
		}
		if !slices.Contains(dockerSorts, w.opts.Sort) {
			return nil, fmt.Errorf("options.sort: %q is not one of %v", w.opts.Sort, dockerSorts)
		}
		return w, nil
	})
}

var dockerSorts = []string{"cpu", "mem", "net", "name"}

func (d *dockerWidget) Kind() string         { return "docker" }
func (d *dockerWidget) Collectors() []string { return []string{"docker"} }

func (d *dockerWidget) containers(ctx *Context) []collect.Container {
	all, _ := metrics.TableAs[[]collect.Container](ctx.Store, collect.TableContainers)
	out := make([]collect.Container, 0, len(all))
	for _, c := range all {
		if d.opts.All || c.State == "running" {
			out = append(out, c)
		}
	}
	slices.SortStableFunc(out, func(a, b collect.Container) int {
		switch d.opts.Sort {
		case "mem":
			return cmp.Compare(b.MemUsed, a.MemUsed)
		case "net":
			return cmp.Compare(b.NetRx+b.NetTx, a.NetRx+a.NetTx)
		case "name":
			return strings.Compare(a.Name, b.Name)
		}
		return cmp.Compare(b.CPU, a.CPU)
	})
	return out
}

func (d *dockerWidget) Title(ctx *Context) (string, string) {
	all, _ := metrics.TableAs[[]collect.Container](ctx.Store, collect.TableContainers)
	running := 0
	for _, c := range all {
		if c.State == "running" {
			running++
		}
	}
	return "docker", fg(ctx.Theme.Dim, fmt.Sprintf("%d running / %d", running, len(all))+"  sort ") + fg(ctx.Theme.Fg, d.opts.Sort)
}

func (d *dockerWidget) Render(ctx *Context, w, h int) []string {
	t := ctx.Theme
	cs := d.containers(ctx)
	if len(cs) == 0 {
		return block([]string{fg(t.Dim, "no containers")}, w, h)
	}
	// Columns: name (flex) state cpu mem net-rx net-tx
	const fixed = 9 + 7 + 10 + 11 + 11
	nameW := max(w-fixed, 8)
	header := fg(t.Title, fit("Name", nameW)+alignRight("State", 9)+alignRight("Cpu%", 7)+
		alignRight("Mem", 10)+alignRight("Rx/s", 11)+alignRight("Tx/s", 11))
	out := []string{header}
	d.offset = max(min(d.offset, len(cs)-(h-1)), 0)
	for _, c := range cs[d.offset:] {
		if len(out) >= h {
			break
		}
		cpuColor := t.Proc.At(c.CPU / 100 / float64(max(ctx.NumCPU, 1)))
		state := fg(t.Dim, alignRight(c.State, 9))
		if c.State == "running" {
			state = fg(t.Fg, alignRight(c.State, 9))
		}
		line := fg(t.Fg, fitEllipsis(c.Name, nameW)) + state
		if c.State == "running" {
			line += fg(cpuColor, alignRight(strconv.FormatFloat(c.CPU, 'f', 1, 64), 7)) +
				fg(t.Fg, alignRight(metrics.FormatBytes(float64(c.MemUsed)), 10)) +
				fg(t.Dim, alignRight(metrics.FormatBytes(c.NetRx), 11)) +
				fg(t.Dim, alignRight(metrics.FormatBytes(c.NetTx), 11))
		}
		out = append(out, line)
	}
	return block(out, w, h)
}

// HandleKey implements Interactive.
func (d *dockerWidget) HandleKey(ctx *Context, k tea.KeyPressMsg) (bool, tea.Cmd) {
	switch a := ctx.action(k, keymap.Nav, keymap.Docker); a {
	case "up":
		d.offset = max(d.offset-1, 0)
	case "down":
		d.offset++
	case "docker_all":
		d.opts.All = !d.opts.All
	case "left", "right":
		i := slices.Index(dockerSorts, d.opts.Sort)
		if a == "right" {
			i = (i + 1) % len(dockerSorts)
		} else {
			i = (i - 1 + len(dockerSorts)) % len(dockerSorts)
		}
		d.opts.Sort = dockerSorts[i]
	default:
		return false, nil
	}
	return true, nil
}

// HandleMouse implements Interactive.
func (d *dockerWidget) HandleMouse(_ *Context, m tea.MouseMsg, _, _ int) tea.Cmd {
	if wm, ok := m.(tea.MouseWheelMsg); ok {
		if wm.Button == tea.MouseWheelUp {
			d.offset = max(d.offset-1, 0)
		} else {
			d.offset++
		}
	}
	return nil
}

// Footer implements Interactive.
func (d *dockerWidget) Footer(ctx *Context) string {
	return joinHints(hint(ctx, "←→", "sort"), keyHint(ctx, "docker_all", "all"), hint(ctx, "↑↓", "scroll"))
}

// Capturing implements Interactive.
func (d *dockerWidget) Capturing() bool { return false }
