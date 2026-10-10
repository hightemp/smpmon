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
}

type disksWidget struct {
	opts disksOptions
}

func init() {
	register("disks", func(n *config.Node) (Widget, error) {
		w := &disksWidget{}
		return w, n.DecodeOptions(&w.opts)
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
				x := ios[i]
				io = fg(t.Dim, "r ") + fg(t.Fg, metrics.FormatRate(x.ReadBps)) +
					fg(t.Dim, "  w ") + fg(t.Fg, metrics.FormatRate(x.WriteBps)) +
					fg(t.Dim, "  busy ") + fg(t.Disk.At(x.Util/100), metrics.FormatPercent(x.Util))
			}
			out = append(out, io)
		}
	}
	if len(fss) == 0 {
		out = append(out, fg(t.Dim, "no filesystems"))
	}
	return block(out, w, h)
}
