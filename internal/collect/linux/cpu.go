//go:build linux

package linux

import (
	"bytes"
	"context"
	"os"
	"runtime"
	"strconv"
	"strings"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/metrics"
)

// cpuTimes holds the jiffy counters of one /proc/stat cpu line.
type cpuTimes struct {
	user, nice, system, idle, iowait, irq, softirq, steal uint64
}

func (c cpuTimes) total() uint64 {
	return c.user + c.nice + c.system + c.idle + c.iowait + c.irq + c.softirq + c.steal
}

// CPU collects total and per-core CPU usage and frequencies.
type CPU struct {
	fs    FS
	r     reader
	prev  map[int]cpuTimes // -1 is the aggregate line
	keys  []coreKeys
	info  collect.CPUInfo
	first bool
}

type coreKeys struct {
	usage, freq metrics.Key
}

var (
	keyCPUUsage  = metrics.Key("cpu.total.usage")
	keyCPUUser   = metrics.Key("cpu.total.user")
	keyCPUSystem = metrics.Key("cpu.total.system")
	keyCPUIOWait = metrics.Key("cpu.total.iowait")
	keyCPUSteal  = metrics.Key("cpu.total.steal")
	keyCPUFreq   = metrics.Key("cpu.freq")
)

// NewCPU creates a CPU collector.
func NewCPU(fs FS) *CPU {
	return &CPU{fs: fs, prev: make(map[int]cpuTimes), first: true}
}

// Name implements collect.Collector.
func (c *CPU) Name() string { return "cpu" }

// Init implements collect.Collector.
func (c *CPU) Init() error {
	if _, err := os.Stat(c.fs.proc("stat")); err != nil {
		return err
	}
	c.info = readCPUInfo(c.fs)
	return nil
}

func (c *CPU) coreKeys(n int) coreKeys {
	for len(c.keys) <= n {
		i := strconv.Itoa(len(c.keys))
		c.keys = append(c.keys, coreKeys{
			usage: metrics.NewKey("cpu", "core", i, "usage"),
			freq:  metrics.NewKey("cpu", "core", i, "freq"),
		})
	}
	return c.keys[n]
}

// Collect implements collect.Collector.
func (c *CPU) Collect(_ context.Context, s *metrics.Sample) error {
	b, err := c.r.read(c.fs.proc("stat"))
	if err != nil {
		return err
	}
	lines(b, func(l []byte) {
		if !bytes.HasPrefix(l, []byte("cpu")) {
			return
		}
		f := bytes.Fields(l)
		if len(f) < 9 {
			return
		}
		idx := -1
		if len(f[0]) > 3 {
			idx = int(parseUint(f[0][3:]))
		}
		cur := cpuTimes{
			user: parseUint(f[1]), nice: parseUint(f[2]), system: parseUint(f[3]), idle: parseUint(f[4]),
			iowait: parseUint(f[5]), irq: parseUint(f[6]), softirq: parseUint(f[7]), steal: parseUint(f[8]),
		}
		prev, ok := c.prev[idx]
		c.prev[idx] = cur
		if !ok {
			return
		}
		total := float64(delta(cur.total(), prev.total()))
		if total == 0 {
			return
		}
		pct := func(cur, prev uint64) float64 { return 100 * float64(delta(cur, prev)) / total }
		busy := 100 - pct(cur.idle+cur.iowait, prev.idle+prev.iowait)
		if idx < 0 {
			s.Add(keyCPUUsage, busy)
			s.Add(keyCPUUser, pct(cur.user+cur.nice, prev.user+prev.nice))
			s.Add(keyCPUSystem, pct(cur.system+cur.irq+cur.softirq, prev.system+prev.irq+prev.softirq))
			s.Add(keyCPUIOWait, pct(cur.iowait, prev.iowait))
			s.Add(keyCPUSteal, pct(cur.steal, prev.steal))
			return
		}
		s.Add(c.coreKeys(idx).usage, busy)
	})

	c.collectFreq(s)
	if c.first {
		c.first = false
		s.SetTable(collect.TableCPUInfo, c.info)
	}
	return nil
}

// collectFreq reads current per-core frequencies from cpufreq, falling back
// to /proc/cpuinfo when cpufreq is unavailable (VMs, some ARM boards).
func (c *CPU) collectFreq(s *metrics.Sample) {
	var sum float64
	var n int
	for i := 0; i < len(c.keys); i++ {
		khz, err := readUint(c.fs.sys("devices/system/cpu", "cpu"+strconv.Itoa(i), "cpufreq/scaling_cur_freq"))
		if err != nil {
			continue
		}
		hz := float64(khz) * 1000
		s.Add(c.keys[i].freq, hz)
		sum += hz
		n++
	}
	if n == 0 {
		b, err := c.r.read(c.fs.proc("cpuinfo"))
		if err != nil {
			return
		}
		core := 0
		lines(b, func(l []byte) {
			if v, ok := bytes.CutPrefix(l, []byte("cpu MHz")); ok {
				_, mhz, _ := bytes.Cut(v, []byte(":"))
				f, err := strconv.ParseFloat(string(bytes.TrimSpace(mhz)), 64)
				if err == nil {
					hz := f * 1e6
					if core < len(c.keys) {
						s.Add(c.keys[core].freq, hz)
					}
					sum += hz
					n++
				}
				core++
			}
		})
	}
	if n > 0 {
		s.Add(keyCPUFreq, sum/float64(n))
	}
}

// readCPUInfo extracts the model name and core counts from /proc/cpuinfo.
func readCPUInfo(fs FS) collect.CPUInfo {
	info := collect.CPUInfo{Threads: runtime.NumCPU()}
	b, err := os.ReadFile(fs.proc("cpuinfo"))
	if err != nil {
		return info
	}
	cores := map[string]bool{}
	var physID string
	threads := 0
	lines(b, func(l []byte) {
		k, v, ok := bytes.Cut(l, []byte(":"))
		if !ok {
			return
		}
		key := string(bytes.TrimSpace(k))
		val := strings.TrimSpace(string(v))
		switch key {
		case "processor":
			threads++
		case "model name", "Model", "Hardware":
			if info.Model == "" {
				info.Model = val
			}
		case "physical id":
			physID = val
		case "core id":
			cores[physID+"/"+val] = true
		}
	})
	if threads > 0 {
		info.Threads = threads
	}
	info.Cores = len(cores)
	return info
}

// Load collects load averages and uptime.
type Load struct {
	fs   FS
	r    reader
	sent bool
}

var (
	keyLoad1  = metrics.Key("load.1")
	keyLoad5  = metrics.Key("load.5")
	keyLoad15 = metrics.Key("load.15")
	keyUptime = metrics.Key("system.uptime")
)

// NewLoad creates a Load collector.
func NewLoad(fs FS) *Load { return &Load{fs: fs} }

// Name implements collect.Collector.
func (l *Load) Name() string { return "load" }

// Init implements collect.Collector.
func (l *Load) Init() error {
	_, err := os.Stat(l.fs.proc("loadavg"))
	return err
}

// Collect implements collect.Collector.
func (l *Load) Collect(_ context.Context, s *metrics.Sample) error {
	b, err := l.r.read(l.fs.proc("loadavg"))
	if err != nil {
		return err
	}
	f := bytes.Fields(b)
	if len(f) >= 3 {
		for i, k := range []metrics.Key{keyLoad1, keyLoad5, keyLoad15} {
			if v, err := strconv.ParseFloat(string(f[i]), 64); err == nil {
				s.Add(k, v)
			}
		}
	}
	if b, err := l.r.read(l.fs.proc("uptime")); err == nil {
		if f := bytes.Fields(b); len(f) > 0 {
			if v, err := strconv.ParseFloat(string(f[0]), 64); err == nil {
				s.Add(keyUptime, v)
			}
		}
	}
	if !l.sent {
		l.sent = true
		s.SetTable(collect.TableSystem, systemInfo(l.fs))
	}
	return nil
}

func systemInfo(fs FS) collect.SystemInfo {
	info := collect.SystemInfo{OS: "Linux"}
	info.Hostname, _ = os.Hostname()
	info.Kernel, _ = readString(fs.proc("sys/kernel/osrelease"))
	if b, err := os.ReadFile("/etc/os-release"); err == nil {
		lines(b, func(l []byte) {
			if v, ok := bytes.CutPrefix(l, []byte("PRETTY_NAME=")); ok {
				info.OS = strings.Trim(string(v), `"`)
			}
		})
	}
	info.BootTime, _ = bootTime(fs)
	return info
}
