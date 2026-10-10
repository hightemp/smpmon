package gops

import (
	"context"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/shirou/gopsutil/v4/disk"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/metrics"
)

const (
	usageTimeout = 500 * time.Millisecond
	mountsTTL    = 10 * time.Second
)

// Disks collects filesystem usage and disk I/O.
type Disks struct {
	prev   map[string]disk.IOCountersStat
	prevAt time.Time

	parts   []disk.PartitionStat
	partsAt time.Time

	stuckMu sync.Mutex
	stuck   map[string]bool
}

// NewDisks creates a Disks collector.
func NewDisks() *Disks {
	return &Disks{prev: map[string]disk.IOCountersStat{}, stuck: map[string]bool{}}
}

// Name implements collect.Collector.
func (d *Disks) Name() string { return "disks" }

// Init implements collect.Collector.
func (d *Disks) Init() error {
	_, err := disk.Partitions(false)
	return err
}

// skipMount filters system volumes that only duplicate the root disk.
func skipMount(p disk.PartitionStat) bool {
	if runtime.GOOS == "darwin" {
		// APFS exposes VM, Preboot, Update, ... as separate volumes of the
		// same container; only the user data volume is interesting.
		if strings.HasPrefix(p.Mountpoint, "/System/Volumes/") && p.Mountpoint != "/System/Volumes/Data" {
			return true
		}
		return p.Fstype == "devfs" || p.Fstype == "autofs" || strings.HasPrefix(p.Mountpoint, "/Library/Developer/CoreSimulator")
	}
	return p.Fstype == "squashfs" || strings.HasPrefix(p.Device, "/dev/loop")
}

// usage calls disk.Usage with a timeout so a dead network mount can't block.
func (d *Disks) usage(ctx context.Context, path string) (*disk.UsageStat, bool) {
	d.stuckMu.Lock()
	if d.stuck[path] {
		d.stuckMu.Unlock()
		return nil, false
	}
	d.stuckMu.Unlock()
	ch := make(chan *disk.UsageStat, 1)
	go func() {
		u, err := disk.UsageWithContext(ctx, path)
		if err != nil {
			u = nil
		}
		ch <- u
		d.stuckMu.Lock()
		delete(d.stuck, path)
		d.stuckMu.Unlock()
	}()
	select {
	case u := <-ch:
		return u, u != nil
	case <-time.After(usageTimeout):
		d.stuckMu.Lock()
		d.stuck[path] = true
		d.stuckMu.Unlock()
		return nil, false
	}
}

// ioDevice maps a partition device to its key in disk.IOCounters:
// "C:" on Windows, "disk3" for /dev/disk3s1s1 on macOS, "sda1" elsewhere.
func ioDevice(dev string, io map[string]disk.IOCountersStat) string {
	name := strings.TrimPrefix(dev, "/dev/")
	if runtime.GOOS == "windows" {
		name = strings.TrimSuffix(filepath.VolumeName(dev), `\`)
	}
	for name != "" {
		if _, ok := io[name]; ok {
			return name
		}
		i := strings.LastIndexByte(name, 's')
		if i <= 0 {
			break
		}
		name = name[:i]
	}
	return ""
}

// Collect implements collect.Collector.
func (d *Disks) Collect(ctx context.Context, s *metrics.Sample) error {
	if d.parts == nil || s.Time.Sub(d.partsAt) >= mountsTTL {
		parts, err := disk.PartitionsWithContext(ctx, false)
		if err != nil {
			return err
		}
		d.parts, d.partsAt = parts, s.Time
	}
	io, _ := disk.IOCountersWithContext(ctx)

	var fss []collect.Filesystem
	seen := map[string]bool{}
	for _, p := range d.parts {
		if skipMount(p) || seen[p.Mountpoint] {
			continue
		}
		seen[p.Mountpoint] = true
		u, ok := d.usage(ctx, p.Mountpoint)
		if !ok || u.Total == 0 {
			continue
		}
		fs := collect.Filesystem{
			Mount: p.Mountpoint, Device: p.Device, FSType: p.Fstype, IODevice: ioDevice(p.Device, io),
			Total: u.Total, Used: u.Used, Free: u.Free, UsedPercent: u.UsedPercent,
			InodesUsedPercent: u.InodesUsedPercent,
		}
		base := metrics.NewKey("disk", "fs", fs.Mount)
		s.Add(base.Child("total"), float64(fs.Total))
		s.Add(base.Child("used"), float64(fs.Used))
		s.Add(base.Child("free"), float64(fs.Free))
		s.Add(base.Child("used_percent"), fs.UsedPercent)
		fss = append(fss, fs)
	}
	slices.SortFunc(fss, func(a, b collect.Filesystem) int { return strings.Compare(a.Mount, b.Mount) })
	s.SetTable(collect.TableFilesystems, fss)

	elapsed := s.Time.Sub(d.prevAt)
	first := d.prevAt.IsZero()
	d.prevAt = s.Time
	var ios []collect.DiskIO
	for name, cur := range io {
		prev, ok := d.prev[name]
		if first || !ok {
			continue
		}
		x := collect.DiskIO{
			Name:      name,
			ReadBps:   rate(cur.ReadBytes, prev.ReadBytes, elapsed),
			WriteBps:  rate(cur.WriteBytes, prev.WriteBytes, elapsed),
			ReadIOPS:  rate(cur.ReadCount, prev.ReadCount, elapsed),
			WriteIOPS: rate(cur.WriteCount, prev.WriteCount, elapsed),
			Util:      min(100, rate(cur.IoTime, prev.IoTime, elapsed)/10),
		}
		base := metrics.NewKey("disk", "io", name)
		s.Add(base.Child("read_bps"), x.ReadBps)
		s.Add(base.Child("write_bps"), x.WriteBps)
		s.Add(base.Child("read_iops"), x.ReadIOPS)
		s.Add(base.Child("write_iops"), x.WriteIOPS)
		s.Add(base.Child("util"), x.Util)
		ios = append(ios, x)
	}
	d.prev = io
	slices.SortFunc(ios, func(a, b collect.DiskIO) int { return strings.Compare(a.Name, b.Name) })
	s.SetTable(collect.TableDisks, ios)
	return nil
}
