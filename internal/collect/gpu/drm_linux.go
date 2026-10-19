//go:build linux

package gpu

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/hightemp/smpmon/internal/collect"
)

type drmReader interface {
	processes(now time.Time) []collect.GPUProcess
}

// drm reads per-process usage of AMD and Intel cards from the DRM client
// statistics the kernel exposes in /proc/<pid>/fdinfo (amdgpu, i915, xe).
// Finding the DRM file descriptors means reading every process's fd
// links, so that sweep runs only every rescanEvery; in between only the
// known descriptors are read.
type drm struct {
	proc    string         // /proc
	pci     map[string]int // PCI address -> GPU index
	fds     map[int32][]string
	scanned time.Time
	prev    map[drmClientID]drmClient
}

const rescanEvery = 10 * time.Second

type drmClientID struct {
	pdev string
	id   uint64
}

// drmClient is one DRM client's counters.
type drmClient struct {
	at       time.Time
	engines  map[string]float64 // busy ns, or cycles for xe
	totals   map[string]float64 // xe: total cycles per engine
	capacity map[string]float64 // i915: engines per class
	mem      uint64
	sysMem   uint64
}

func newDRM(r sysfsReader, first int) drmReader {
	s, ok := r.(*sysfs)
	if !ok {
		return nil
	}
	d := &drm{proc: "/proc", pci: map[string]int{}, prev: map[drmClientID]drmClient{}}
	for i, c := range s.cards {
		if c.pci != "" {
			d.pci[c.pci] = first + i
		}
	}
	if len(d.pci) == 0 {
		return nil
	}
	return d
}

// scan finds the descriptors that point at /dev/dri.
func (d *drm) scan() {
	d.fds = map[int32][]string{}
	entries, err := os.ReadDir(d.proc)
	if err != nil {
		return
	}
	for _, e := range entries {
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		dir := filepath.Join(d.proc, e.Name(), "fd")
		fds, err := os.ReadDir(dir) // fails for other users' processes unless root
		if err != nil {
			continue
		}
		for _, fd := range fds {
			if target, err := os.Readlink(filepath.Join(dir, fd.Name())); err == nil && strings.HasPrefix(target, "/dev/dri/") {
				d.fds[int32(pid)] = append(d.fds[int32(pid)], fd.Name())
			}
		}
	}
}

func (d *drm) processes(now time.Time) []collect.GPUProcess {
	if d.fds == nil || now.Sub(d.scanned) >= rescanEvery {
		d.scan()
		d.scanned = now
	}
	type key struct {
		pid int32
		gpu int
	}
	agg := map[key]*collect.GPUProcess{}
	var order []key
	seen := map[drmClientID]bool{}
	next := map[drmClientID]drmClient{}
	for pid, fds := range d.fds {
		alive := false
		for _, fd := range fds {
			b, err := os.ReadFile(filepath.Join(d.proc, strconv.Itoa(int(pid)), "fdinfo", fd))
			if err != nil {
				continue
			}
			alive = true
			id, c, ok := parseFdinfo(b)
			if !ok || seen[id] { // the same client is often open through several fds
				continue
			}
			gpu, known := d.pci[id.pdev]
			if !known {
				continue
			}
			seen[id] = true
			c.at = now
			next[id] = c
			k := key{pid, gpu}
			p := agg[k]
			if p == nil {
				p = &collect.GPUProcess{PID: pid, GPU: gpu, Type: "G", Util: -1, Enc: -1, Dec: -1}
				agg[k] = p
				order = append(order, k)
			}
			mem := c.mem
			if mem == 0 {
				mem = c.sysMem // integrated GPUs use system memory
			}
			p.MemUsed += mem
			if prev, ok := d.prev[id]; ok {
				addUsage(p, prev, c)
			}
		}
		if !alive {
			delete(d.fds, pid)
		}
	}
	d.prev = next
	out := make([]collect.GPUProcess, 0, len(order))
	for _, k := range order {
		out = append(out, *agg[k])
	}
	sortProcs(out)
	return out
}

// Engine names per driver: amdgpu gfx/compute/dec/enc/enc_1/vcn..., i915
// render/copy/video/video-enhance, xe rcs/bcs/vcs/vecs/ccs.
func engineClass(e string) string {
	switch e {
	case "gfx", "render", "rcs":
		return "gfx"
	case "compute", "ccs":
		return "compute"
	case "dec", "video", "vcs", "vcn", "vcn_unified":
		return "dec"
	case "enc", "enc_1":
		return "enc"
	}
	return ""
}

// addUsage adds the busy share since prev to p.
func addUsage(p *collect.GPUProcess, prev, cur drmClient) {
	dt := float64(cur.at.Sub(prev.at).Nanoseconds())
	if dt <= 0 {
		return
	}
	var gfx, compute float64
	for eng, v := range cur.engines {
		class := engineClass(eng)
		if class == "" {
			continue
		}
		var pct float64
		if total, ok := cur.totals[eng]; ok { // xe: busy cycles out of total cycles
			dTotal := total - prev.totals[eng]
			if dTotal <= 0 {
				continue
			}
			pct = 100 * (v - prev.engines[eng]) / dTotal
		} else {
			pct = 100 * (v - prev.engines[eng]) / dt / max(cur.capacity[eng], 1)
		}
		pct = min(max(pct, 0), 100)
		switch class {
		case "gfx":
			gfx += pct
		case "compute":
			compute += pct
		case "dec":
			p.Dec = min(max(p.Dec, 0)+pct, 100)
		case "enc":
			p.Enc = min(max(p.Enc, 0)+pct, 100)
		}
	}
	p.Util = min(max(p.Util, 0)+gfx+compute, 100)
	if compute > 0 && gfx == 0 {
		p.Type = "C"
	}
}

// parseFdinfo parses the drm-* keys of one fdinfo file.
func parseFdinfo(b []byte) (drmClientID, drmClient, bool) {
	var id drmClientID
	c := drmClient{engines: map[string]float64{}}
	hasID := false
	sc := bufio.NewScanner(bytes.NewReader(b))
	for sc.Scan() {
		k, v, ok := strings.Cut(sc.Text(), ":")
		if !ok || !strings.HasPrefix(k, "drm-") {
			continue
		}
		v = strings.TrimSpace(v)
		switch {
		case k == "drm-pdev":
			id.pdev = v
		case k == "drm-client-id":
			n, err := strconv.ParseUint(v, 10, 64)
			id.id, hasID = n, err == nil
		case strings.HasPrefix(k, "drm-engine-capacity-"):
			if c.capacity == nil {
				c.capacity = map[string]float64{}
			}
			c.capacity[strings.TrimPrefix(k, "drm-engine-capacity-")] = num(v)
		case strings.HasPrefix(k, "drm-engine-"):
			c.engines[strings.TrimPrefix(k, "drm-engine-")] = num(strings.TrimSuffix(v, " ns"))
		case strings.HasPrefix(k, "drm-total-cycles-"):
			if c.totals == nil {
				c.totals = map[string]float64{}
			}
			c.totals[strings.TrimPrefix(k, "drm-total-cycles-")] = num(v)
		case strings.HasPrefix(k, "drm-cycles-"):
			c.engines[strings.TrimPrefix(k, "drm-cycles-")] = num(v)
		case k == "drm-memory-vram", strings.HasPrefix(k, "drm-resident-vram"), strings.HasPrefix(k, "drm-resident-local"):
			c.mem += parseSize(v)
		case k == "drm-memory-gtt", strings.HasPrefix(k, "drm-resident-system"), strings.HasPrefix(k, "drm-resident-gtt"):
			c.sysMem += parseSize(v)
		}
	}
	return id, c, hasID && id.pdev != ""
}

// parseSize parses "123 KiB" style sizes.
func parseSize(s string) uint64 {
	f := strings.Fields(s)
	if len(f) == 0 {
		return 0
	}
	v, err := strconv.ParseUint(f[0], 10, 64)
	if err != nil {
		return 0
	}
	if len(f) > 1 {
		switch f[1] {
		case "KiB":
			v <<= 10
		case "MiB":
			v <<= 20
		case "GiB":
			v <<= 30
		}
	}
	return v
}
