//go:build linux

package linux

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/metrics"
)

// statfsTimeout bounds statfs on network filesystems that may hang.
const statfsTimeout = 500 * time.Millisecond

// realNetFS lists filesystem types without a /dev source that are still
// worth showing.
var realNetFS = map[string]bool{
	"zfs": true, "nfs": true, "nfs4": true, "cifs": true, "smb3": true,
	"fuse.sshfs": true, "9p": true, "virtiofs": true, "ceph": true, "fuse.rclone": true,
}

type diskCounters struct {
	reads, writes, sectorsRead, sectorsWritten, ioMillis uint64
}

type diskKeys struct {
	readBps, writeBps, readIOPS, writeIOPS, util metrics.Key
}

type fsKeys struct {
	total, used, free, usedPct metrics.Key
}

// Disks collects mounted filesystem usage and block device I/O.
type Disks struct {
	fs     FS
	r      reader
	prev   map[string]diskCounters
	prevAt time.Time
	dkeys  map[string]diskKeys
	fkeys  map[string]fsKeys

	// statfs may block forever on a dead network mount; such mounts are
	// skipped until the pending call returns.
	stuckMu sync.Mutex
	stuck   map[string]bool
}

// NewDisks creates a Disks collector.
func NewDisks(fs FS) *Disks {
	return &Disks{
		fs:    fs,
		prev:  make(map[string]diskCounters),
		dkeys: make(map[string]diskKeys),
		fkeys: make(map[string]fsKeys),
		stuck: make(map[string]bool),
	}
}

// Name implements collect.Collector.
func (d *Disks) Name() string { return "disks" }

// Init implements collect.Collector.
func (d *Disks) Init() error {
	_, err := os.Stat(d.fs.proc("self/mountinfo"))
	return err
}

// Collect implements collect.Collector.
func (d *Disks) Collect(ctx context.Context, s *metrics.Sample) error {
	mounts, err := d.mounts()
	if err != nil {
		return err
	}
	fss := make([]collect.Filesystem, 0, len(mounts))
	for _, m := range mounts {
		if ctx.Err() != nil {
			break
		}
		st, ok := d.statfs(m.Mount)
		if !ok || st.Blocks == 0 {
			continue
		}
		bsize := uint64(st.Bsize)
		total := st.Blocks * bsize
		free := st.Bavail * bsize
		used := (st.Blocks - st.Bfree) * bsize
		m.Total, m.Used, m.Free = total, used, free
		if used+free > 0 {
			m.UsedPercent = 100 * float64(used) / float64(used+free)
		}
		if st.Files > 0 {
			m.InodesUsedPercent = 100 * float64(st.Files-st.Ffree) / float64(st.Files)
		}
		k := d.fsKeys(m.Mount)
		s.Add(k.total, float64(m.Total))
		s.Add(k.used, float64(m.Used))
		s.Add(k.free, float64(m.Free))
		s.Add(k.usedPct, m.UsedPercent)
		fss = append(fss, m)
	}
	s.SetTable(collect.TableFilesystems, fss)

	ios, err := d.diskIO(s.Time)
	if err == nil {
		for _, io := range ios {
			k := d.diskKeys(io.Name)
			s.Add(k.readBps, io.ReadBps)
			s.Add(k.writeBps, io.WriteBps)
			s.Add(k.readIOPS, io.ReadIOPS)
			s.Add(k.writeIOPS, io.WriteIOPS)
			s.Add(k.util, io.Util)
		}
		s.SetTable(collect.TableDisks, ios)
	}
	return nil
}

func (d *Disks) fsKeys(mount string) fsKeys {
	k, ok := d.fkeys[mount]
	if !ok {
		base := metrics.NewKey("disk", "fs", mount)
		k = fsKeys{base.Child("total"), base.Child("used"), base.Child("free"), base.Child("used_percent")}
		d.fkeys[mount] = k
	}
	return k
}

func (d *Disks) diskKeys(dev string) diskKeys {
	k, ok := d.dkeys[dev]
	if !ok {
		base := metrics.NewKey("disk", "io", dev)
		k = diskKeys{base.Child("read_bps"), base.Child("write_bps"), base.Child("read_iops"), base.Child("write_iops"), base.Child("util")}
		d.dkeys[dev] = k
	}
	return k
}

// statfs calls statfs with a timeout so a hung network mount cannot block
// the collector.
func (d *Disks) statfs(path string) (syscall.Statfs_t, bool) {
	d.stuckMu.Lock()
	if d.stuck[path] {
		d.stuckMu.Unlock()
		return syscall.Statfs_t{}, false
	}
	d.stuckMu.Unlock()

	type result struct {
		st  syscall.Statfs_t
		err error
	}
	ch := make(chan result, 1)
	go func() {
		var st syscall.Statfs_t
		err := syscall.Statfs(path, &st)
		ch <- result{st, err}
		d.stuckMu.Lock()
		delete(d.stuck, path)
		d.stuckMu.Unlock()
	}()
	select {
	case r := <-ch:
		return r.st, r.err == nil
	case <-time.After(statfsTimeout):
		d.stuckMu.Lock()
		d.stuck[path] = true
		d.stuckMu.Unlock()
		return syscall.Statfs_t{}, false
	}
}

// mounts parses mountinfo and returns real filesystems, one per device.
func (d *Disks) mounts() ([]collect.Filesystem, error) {
	b, err := d.r.read(d.fs.proc("self/mountinfo"))
	if err != nil {
		return nil, err
	}
	byDev := map[string]collect.Filesystem{}
	var order []string
	lines(b, func(l []byte) {
		// 36 35 98:0 /mnt1 /mnt2 rw,noatime master:1 - ext3 /dev/root rw,errors=continue
		pre, post, ok := bytes.Cut(l, []byte(" - "))
		if !ok {
			return
		}
		pf, qf := bytes.Fields(pre), bytes.Fields(post)
		if len(pf) < 5 || len(qf) < 2 {
			return
		}
		devID := string(pf[2])
		mount := unescapeMount(string(pf[4]))
		fstype, source := string(qf[0]), unescapeMount(string(qf[1]))
		if !isRealFS(fstype, source) {
			return
		}
		fs := collect.Filesystem{Mount: mount, Device: source, FSType: fstype, IODevice: d.ioDevice(source)}
		if prev, seen := byDev[devID]; seen {
			// Bind mounts and btrfs subvolumes share a device: keep the
			// shortest mount point.
			if len(mount) < len(prev.Mount) {
				byDev[devID] = fs
			}
			return
		}
		byDev[devID] = fs
		order = append(order, devID)
	})
	out := make([]collect.Filesystem, 0, len(order))
	for _, id := range order {
		out = append(out, byDev[id])
	}
	slices.SortStableFunc(out, func(a, b collect.Filesystem) int { return strings.Compare(a.Mount, b.Mount) })
	return out, nil
}

func isRealFS(fstype, source string) bool {
	if fstype == "squashfs" || strings.HasPrefix(source, "/dev/loop") {
		return false // snaps and other read-only images
	}
	return strings.HasPrefix(source, "/dev/") || realNetFS[fstype]
}

// ioDevice maps a mount source to its block device name in /proc/diskstats.
func (d *Disks) ioDevice(source string) string {
	if !strings.HasPrefix(source, "/dev/") {
		return ""
	}
	if p, err := filepath.EvalSymlinks(source); err == nil {
		source = p
	}
	return filepath.Base(source)
}

// unescapeMount decodes octal escapes (\040 for space) used in mountinfo.
func unescapeMount(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) {
			if n, err := strconv.ParseUint(s[i+1:i+4], 8, 8); err == nil {
				b.WriteByte(byte(n))
				i += 3
				continue
			}
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// diskIO reads /proc/diskstats and returns rates since the previous call.
func (d *Disks) diskIO(now time.Time) ([]collect.DiskIO, error) {
	b, err := d.r.read(d.fs.proc("diskstats"))
	if err != nil {
		return nil, err
	}
	elapsed := now.Sub(d.prevAt)
	first := d.prevAt.IsZero()
	d.prevAt = now
	var out []collect.DiskIO
	seen := map[string]bool{}
	lines(b, func(l []byte) {
		f := bytes.Fields(l)
		if len(f) < 14 {
			return
		}
		name := string(f[2])
		if strings.HasPrefix(name, "loop") || strings.HasPrefix(name, "ram") {
			return
		}
		cur := diskCounters{
			reads: parseUint(f[3]), sectorsRead: parseUint(f[5]),
			writes: parseUint(f[7]), sectorsWritten: parseUint(f[9]),
			ioMillis: parseUint(f[12]),
		}
		seen[name] = true
		prev, ok := d.prev[name]
		d.prev[name] = cur
		if first || !ok {
			return
		}
		io := collect.DiskIO{
			Name:      name,
			ReadBps:   rate(cur.sectorsRead, prev.sectorsRead, elapsed) * 512,
			WriteBps:  rate(cur.sectorsWritten, prev.sectorsWritten, elapsed) * 512,
			ReadIOPS:  rate(cur.reads, prev.reads, elapsed),
			WriteIOPS: rate(cur.writes, prev.writes, elapsed),
			Util:      min(100, rate(cur.ioMillis, prev.ioMillis, elapsed)/10),
		}
		out = append(out, io)
	})
	for name := range d.prev {
		if !seen[name] {
			delete(d.prev, name)
		}
	}
	return out, nil
}
