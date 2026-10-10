// Package gops implements collectors on top of gopsutil. It is used on
// macOS, Windows and other non-Linux systems, and builds everywhere so it
// can be tested on any OS. Metric keys and tables match the Linux collectors.
package gops

import (
	"context"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/host"
	"github.com/shirou/gopsutil/v4/load"
	"github.com/shirou/gopsutil/v4/mem"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/metrics"
)

// Collectors returns all gopsutil-based collectors.
func Collectors() []collect.Collector {
	return []collect.Collector{&CPU{}, &Load{}, &Mem{}, NewDisks(), NewNet(), NewProcs(), &Sensors{}, &Battery{}}
}

func delta(cur, prev uint64) uint64 {
	if cur < prev {
		return 0
	}
	return cur - prev
}

func rate(cur, prev uint64, elapsed time.Duration) float64 {
	if elapsed <= 0 {
		return 0
	}
	return float64(delta(cur, prev)) / elapsed.Seconds()
}

// ---- cpu ----

// CPU collects total and per-core usage.
type CPU struct {
	prevTotal cpu.TimesStat
	prevCores []cpu.TimesStat
	keys      []metrics.Key
	info      collect.CPUInfo
	mhz       float64
	sent      bool
}

// Name implements collect.Collector.
func (c *CPU) Name() string { return "cpu" }

// Init implements collect.Collector.
func (c *CPU) Init() error {
	c.info.Threads = runtime.NumCPU()
	if n, err := cpu.Counts(false); err == nil {
		c.info.Cores = n
	}
	if infos, err := cpu.Info(); err == nil && len(infos) > 0 {
		c.info.Model = strings.TrimSpace(infos[0].ModelName)
		c.mhz = infos[0].Mhz
	}
	_, err := cpu.Times(false)
	return err
}

func busyIdle(t cpu.TimesStat) (busy, idle float64) {
	idle = t.Idle + t.Iowait
	busy = t.User + t.Nice + t.System + t.Irq + t.Softirq + t.Steal
	return busy, idle
}

// usage returns busy percent between two samples.
func usage(cur, prev cpu.TimesStat) (float64, bool) {
	cb, ci := busyIdle(cur)
	pb, pi := busyIdle(prev)
	total := (cb + ci) - (pb + pi)
	if total <= 0 {
		return 0, false
	}
	return min(max(100*(cb-pb)/total, 0), 100), true
}

// Collect implements collect.Collector.
func (c *CPU) Collect(ctx context.Context, s *metrics.Sample) error {
	total, err := cpu.TimesWithContext(ctx, false)
	if err != nil {
		return err
	}
	cores, err := cpu.TimesWithContext(ctx, true)
	if err != nil {
		return err
	}
	if len(total) > 0 {
		cur := total[0]
		if v, ok := usage(cur, c.prevTotal); ok && c.prevTotal.CPU != "" {
			s.Add("cpu.total.usage", v)
			b, i := busyIdle(cur)
			pb, pi := busyIdle(c.prevTotal)
			span := (b + i) - (pb + pi)
			pct := func(cur, prev float64) float64 { return min(max(100*(cur-prev)/span, 0), 100) }
			s.Add("cpu.total.user", pct(cur.User+cur.Nice, c.prevTotal.User+c.prevTotal.Nice))
			s.Add("cpu.total.system", pct(cur.System+cur.Irq+cur.Softirq, c.prevTotal.System+c.prevTotal.Irq+c.prevTotal.Softirq))
			s.Add("cpu.total.iowait", pct(cur.Iowait, c.prevTotal.Iowait))
			s.Add("cpu.total.steal", pct(cur.Steal, c.prevTotal.Steal))
		}
		c.prevTotal = cur
	}
	for len(c.keys) < len(cores) {
		c.keys = append(c.keys, metrics.NewKey("cpu", "core", strconv.Itoa(len(c.keys)), "usage"))
	}
	if len(c.prevCores) == len(cores) {
		for i := range cores {
			if v, ok := usage(cores[i], c.prevCores[i]); ok {
				s.Add(c.keys[i], v)
			}
		}
	}
	c.prevCores = slices.Clone(cores)
	if c.mhz > 0 {
		s.Add("cpu.freq", c.mhz*1e6)
	}
	if !c.sent {
		c.sent = true
		s.SetTable(collect.TableCPUInfo, c.info)
	}
	return nil
}

// ---- load ----

// Load collects load averages (emulated on Windows) and uptime.
type Load struct {
	sent bool
}

// Name implements collect.Collector.
func (l *Load) Name() string { return "load" }

// Init implements collect.Collector.
func (l *Load) Init() error {
	_, err := host.Uptime()
	return err
}

// Collect implements collect.Collector.
func (l *Load) Collect(ctx context.Context, s *metrics.Sample) error {
	if avg, err := load.AvgWithContext(ctx); err == nil {
		s.Add("load.1", avg.Load1)
		s.Add("load.5", avg.Load5)
		s.Add("load.15", avg.Load15)
	}
	up, err := host.UptimeWithContext(ctx)
	if err != nil {
		return err
	}
	s.Add("system.uptime", float64(up))
	if !l.sent {
		l.sent = true
		info := collect.SystemInfo{OS: runtime.GOOS}
		if hi, err := host.InfoWithContext(ctx); err == nil {
			info.Hostname = hi.Hostname
			info.OS = strings.TrimSpace(hi.Platform + " " + hi.PlatformVersion)
			info.Kernel = hi.KernelVersion
			info.BootTime = time.Unix(int64(hi.BootTime), 0)
		}
		s.SetTable(collect.TableSystem, info)
	}
	return nil
}

// ---- mem ----

// Mem collects memory and swap usage.
type Mem struct{}

// Name implements collect.Collector.
func (m *Mem) Name() string { return "mem" }

// Init implements collect.Collector.
func (m *Mem) Init() error {
	_, err := mem.VirtualMemory()
	return err
}

// Collect implements collect.Collector.
func (m *Mem) Collect(ctx context.Context, s *metrics.Sample) error {
	vm, err := mem.VirtualMemoryWithContext(ctx)
	if err != nil {
		return err
	}
	used := vm.Total - min(vm.Available, vm.Total)
	s.Add("mem.total", float64(vm.Total))
	s.Add("mem.used", float64(used))
	s.Add("mem.available", float64(vm.Available))
	s.Add("mem.free", float64(vm.Free))
	s.Add("mem.cached", float64(vm.Cached))
	s.Add("mem.buffers", float64(vm.Buffers))
	if vm.Total > 0 {
		s.Add("mem.used_percent", 100*float64(used)/float64(vm.Total))
	}
	if sw, err := mem.SwapMemoryWithContext(ctx); err == nil {
		s.Add("swap.total", float64(sw.Total))
		s.Add("swap.used", float64(sw.Used))
		s.Add("swap.free", float64(sw.Free))
		pct := 0.0
		if sw.Total > 0 {
			pct = 100 * float64(sw.Used) / float64(sw.Total)
		}
		s.Add("swap.used_percent", pct)
	}
	return nil
}
