//go:build linux

package linux

import (
	"bytes"
	"context"
	"os"

	"github.com/hightemp/smpmon/internal/metrics"
)

// Mem collects memory and swap usage, including the ZFS ARC.
type Mem struct {
	fs FS
	r  reader
}

var (
	keyMemTotal     = metrics.Key("mem.total")
	keyMemUsed      = metrics.Key("mem.used")
	keyMemAvailable = metrics.Key("mem.available")
	keyMemFree      = metrics.Key("mem.free")
	keyMemCached    = metrics.Key("mem.cached")
	keyMemBuffers   = metrics.Key("mem.buffers")
	keyMemARC       = metrics.Key("mem.zfs_arc")
	keyMemUsedPct   = metrics.Key("mem.used_percent")
	keySwapTotal    = metrics.Key("swap.total")
	keySwapUsed     = metrics.Key("swap.used")
	keySwapFree     = metrics.Key("swap.free")
	keySwapUsedPct  = metrics.Key("swap.used_percent")
)

// NewMem creates a Mem collector.
func NewMem(fs FS) *Mem { return &Mem{fs: fs} }

// Name implements collect.Collector.
func (m *Mem) Name() string { return "mem" }

// Init implements collect.Collector.
func (m *Mem) Init() error {
	_, err := os.Stat(m.fs.proc("meminfo"))
	return err
}

// Collect implements collect.Collector.
func (m *Mem) Collect(_ context.Context, s *metrics.Sample) error {
	b, err := m.r.read(m.fs.proc("meminfo"))
	if err != nil {
		return err
	}
	var total, free, avail, buffers, cached, sreclaim, swapTotal, swapFree uint64
	hasAvail := false
	lines(b, func(l []byte) {
		k, v, ok := bytes.Cut(l, []byte(":"))
		if !ok {
			return
		}
		n := parseUint(bytes.TrimSpace(bytes.TrimSuffix(bytes.TrimSpace(v), []byte("kB")))) * 1024
		switch string(k) {
		case "MemTotal":
			total = n
		case "MemFree":
			free = n
		case "MemAvailable":
			avail, hasAvail = n, true
		case "Buffers":
			buffers = n
		case "Cached":
			cached = n
		case "SReclaimable":
			sreclaim = n
		case "SwapTotal":
			swapTotal = n
		case "SwapFree":
			swapFree = n
		}
	})
	if !hasAvail { // kernels before 3.14
		avail = free + buffers + cached
	}
	cached += sreclaim

	// The ZFS ARC is reported as used memory although it shrinks on demand,
	// so count its reclaimable part as cache/available like btop does.
	if arc, arcMin, ok := m.zfsARC(); ok {
		s.Add(keyMemARC, float64(arc))
		if arc > arcMin {
			avail += arc - arcMin
			cached += arc - arcMin
		}
	}
	avail = min(avail, total)
	used := total - avail

	s.Add(keyMemTotal, float64(total))
	s.Add(keyMemUsed, float64(used))
	s.Add(keyMemAvailable, float64(avail))
	s.Add(keyMemFree, float64(free))
	s.Add(keyMemCached, float64(cached))
	s.Add(keyMemBuffers, float64(buffers))
	if total > 0 {
		s.Add(keyMemUsedPct, 100*float64(used)/float64(total))
	}
	swapUsed := swapTotal - min(swapFree, swapTotal)
	s.Add(keySwapTotal, float64(swapTotal))
	s.Add(keySwapUsed, float64(swapUsed))
	s.Add(keySwapFree, float64(swapFree))
	if swapTotal > 0 {
		s.Add(keySwapUsedPct, 100*float64(swapUsed)/float64(swapTotal))
	} else {
		s.Add(keySwapUsedPct, 0)
	}
	return nil
}

// zfsARC returns the ARC size and its minimum size from arcstats.
func (m *Mem) zfsARC() (size, cmin uint64, ok bool) {
	b, err := m.r.read(m.fs.proc("spl/kstat/zfs/arcstats"))
	if err != nil {
		return 0, 0, false
	}
	lines(b, func(l []byte) {
		f := bytes.Fields(l)
		if len(f) != 3 {
			return
		}
		switch string(f[0]) {
		case "size":
			size, ok = parseUint(f[2]), true
		case "c_min":
			cmin = parseUint(f[2])
		}
	})
	return size, cmin, ok
}
