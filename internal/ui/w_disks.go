package ui

import (
	"slices"

	"github.com/hightemp/smpmon/internal/chart"
	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/config"
	"github.com/hightemp/smpmon/internal/metrics"
)

type disksOptions struct {
	// Mounts limits the list to these mount points, in this order.
	Mounts []string `yaml:"mounts"`
	// IO shows read/write rates under each filesystem (default true).
	IO *bool `yaml:"io"`
	// Style is graphs (I/O history per filesystem and a total I/O graph in
	// spare rows) or meters (I/O as numbers).
	Style string `yaml:"style"`
	Mode  string `yaml:"mode"`
}

type disksWidget struct {
	opts       disksOptions
	bufs       map[metrics.Key][]float64
	rsum, wsum []float64
	tmp        []float64
}

func init() {
	register("disks", func(n *config.Node) (Widget, error) {
		w := &disksWidget{bufs: map[metrics.Key][]float64{}}
		if err := n.DecodeOptions(&w.opts); err != nil {
			return nil, err
		}
		return w, checkStyle(w.opts.Style)
	})
}

func (d *disksWidget) Kind() string         { return "disks" }
func (d *disksWidget) Collectors() []string { return []string{"disks"} }

func (d *disksWidget) Title(ctx *Context) (string, string) {
	ios, _ := metrics.TableAs[[]collect.DiskIO](ctx.Store, collect.TableDisks)
	var r, w float64
	for _, io := range ios {
		if isWholeDisk(io.Name, ios) {
			r += io.ReadBps
			w += io.WriteBps
		}
	}
	return "disks", fg(ctx.Theme.Dim, "r "+metrics.FormatRate(r)+" w "+metrics.FormatRate(w))
}

// isWholeDisk reports whether name is not a partition of another listed
// device (sda1 of sda, nvme0n1p2 of nvme0n1), to avoid double counting.
func isWholeDisk(name string, all []collect.DiskIO) bool {
	for _, o := range all {
		if o.Name != name && len(name) > len(o.Name) && name[:len(o.Name)] == o.Name {
			return false
		}
	}
	return true
}

func (d *disksWidget) filesystems(ctx *Context) []collect.Filesystem {
	fss, _ := metrics.TableAs[[]collect.Filesystem](ctx.Store, collect.TableFilesystems)
	if len(d.opts.Mounts) == 0 {
		return fss
	}
	var out []collect.Filesystem
	for _, m := range d.opts.Mounts {
		if i := slices.IndexFunc(fss, func(f collect.Filesystem) bool { return f.Mount == m }); i >= 0 {
			out = append(out, fss[i])
		}
	}
	return out
}

func (d *disksWidget) Render(ctx *Context, w, h int) []string {
	t := ctx.Theme
	fss := d.filesystems(ctx)
	ios, _ := metrics.TableAs[[]collect.DiskIO](ctx.Store, collect.TableDisks)
	showIO := d.opts.IO == nil || *d.opts.IO
	per := 2
	if showIO && h >= len(fss)*3 {
		per = 3
	}
	var out []string
	for _, fs := range fss {
		if len(out)+per > h && len(out) > 0 {
			break
		}
		f := fs.UsedPercent / 100
		out = append(out, spread(fg(t.Title, fs.Mount),
			fg(t.Dim, metrics.FormatBytes(float64(fs.Used))+" / "+metrics.FormatBytes(float64(fs.Total))), w))
		meterW := w - 7
		line := ""
		if meterW >= 4 {
			line = chart.Meter{Width: meterW, Gradient: t.Disk, Empty: t.MeterEmpty}.Render(f) + " "
		}
		out = append(out, line+fg(t.Disk.At(f), alignRight(metrics.FormatPercent(fs.UsedPercent), 6)))
		if per == 3 {
			io := ""
			if i := slices.IndexFunc(ios, func(x collect.DiskIO) bool { return x.Name == fs.IODevice }); i >= 0 {
				io = d.ioLine(ctx, ios[i], w)
			}
			out = append(out, io)
		}
	}
	if len(fss) == 0 {
		out = append(out, fg(t.Dim, "no filesystems"))
	}
	if spare := h - len(out); d.opts.Style != "meters" && showIO && spare >= 4 && len(ios) > 0 {
		out = append(out, "")
		out = append(out, d.totalGraph(ctx, ios, w, spare-1)...)
	}
	return block(out, w, h)
}

// ioLine renders "r ⣀⣤⣶ 1.2 MiB/s  w ⣀⣀ 0 B/s" (graphs) or the rates and
// busy time (meters, or when too narrow for graphs).
func (d *disksWidget) ioLine(ctx *Context, x collect.DiskIO, w int) string {
	t := ctx.Theme
	const rateW = 11
	gw := (w-2*(2+rateW)-2)/2 - 1
	if d.opts.Style == "meters" || gw < 4 {
		return fg(t.Dim, "r ") + fg(t.Fg, metrics.FormatRate(x.ReadBps)) +
			fg(t.Dim, "  w ") + fg(t.Fg, metrics.FormatRate(x.WriteBps)) +
			fg(t.Dim, "  busy ") + fg(t.Disk.At(x.Util/100), metrics.FormatPercent(x.Util))
	}
	base := metrics.NewKey("disk", "io", x.Name)
	part := func(label string, key metrics.Key, v float64, grad chart.Gradient) string {
		g := mini(ctx, d.bufs, key, gw, 1, d.opts.Mode, 0, 0, grad)[0]
		return fg(t.Dim, label+" ") + g + fg(t.Fg, alignRight(metrics.FormatRate(v), rateW))
	}
	return part("r", base.Child("read_bps"), x.ReadBps, t.NetRx) + "  " + part("w", base.Child("write_bps"), x.WriteBps, t.NetTx)
}

// totalGraph draws reads of all whole disks upward and writes downward.
func (d *disksWidget) totalGraph(ctx *Context, ios []collect.DiskIO, w, h int) []string {
	t := ctx.Theme
	mode := graphMode(ctx, d.opts.Mode)
	top := (h + 1) / 2
	rg := chart.Graph{Width: w, Height: top, Mode: mode, Series: []chart.Series{{Gradient: t.NetRx}}}
	wg := chart.Graph{Width: w, Height: h - top, Mode: mode, Invert: true, Series: []chart.Series{{Gradient: t.NetTx}}}
	d.rsum, d.wsum = d.rsum[:0], d.wsum[:0]
	var rNow, wNow float64
	for _, x := range ios {
		if !isWholeDisk(x.Name, ios) {
			continue
		}
		base := metrics.NewKey("disk", "io", x.Name)
		d.tmp = ctx.series(base.Child("read_bps"), d.tmp[:0], rg.Slots())
		d.rsum = addSeries(d.rsum, d.tmp)
		d.tmp = ctx.series(base.Child("write_bps"), d.tmp[:0], wg.Slots())
		d.wsum = addSeries(d.wsum, d.tmp)
		rNow, wNow = rNow+x.ReadBps, wNow+x.WriteBps
	}
	rg.Series[0].Values, wg.Series[0].Values = d.rsum, d.wsum
	c := chart.NewCanvas(w, h)
	rg.Draw(c, 0, 0)
	wg.Draw(c, 0, top)
	c.Text(0, 0, "▲ read "+metrics.FormatRate(rNow), t.Title)
	c.Text(0, h-1, "▼ write "+metrics.FormatRate(wNow), t.Title)
	return c.Lines()
}

// addSeries adds b to sum element-wise, aligning the newest values.
func addSeries(sum, b []float64) []float64 {
	if n := len(b); n > len(sum) {
		old := len(sum)
		if cap(sum) >= n {
			sum = sum[:n]
		} else {
			sum = append(make([]float64, 0, n), sum...)[:n]
		}
		copy(sum[n-old:], sum[:old]) // shift right: older values are on the left
		clear(sum[:n-old])
	}
	off := len(sum) - len(b)
	for i, v := range b {
		sum[off+i] += v
	}
	return sum
}
