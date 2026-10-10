//go:build linux

package linux

import (
	"context"
	"math"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/metrics"
)

// fixture is a fake /proc + /sys tree.
type fixture struct {
	t  *testing.T
	fs FS
}

func newFixture(t *testing.T) *fixture {
	dir := t.TempDir()
	return &fixture{t: t, fs: FS{Proc: filepath.Join(dir, "proc"), Sys: filepath.Join(dir, "sys")}}
}

func (f *fixture) write(root, rel, content string) {
	f.t.Helper()
	p := filepath.Join(root, rel)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		f.t.Fatal(err)
	}
	if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
		f.t.Fatal(err)
	}
}

func (f *fixture) proc(rel, content string) { f.write(f.fs.Proc, rel, content) }
func (f *fixture) sys(rel, content string)  { f.write(f.fs.Sys, rel, content) }

// collect runs c once at time t and returns the resulting sample.
func collectAt(t *testing.T, c collect.Collector, at time.Time) metrics.Sample {
	t.Helper()
	s := metrics.Sample{Time: at}
	if err := c.Collect(context.Background(), &s); err != nil {
		t.Fatalf("%s: %v", c.Name(), err)
	}
	return s
}

func value(t *testing.T, s metrics.Sample, k metrics.Key) float64 {
	t.Helper()
	for _, p := range s.Points {
		if p.Key == k {
			return p.Value
		}
	}
	t.Fatalf("no point %s in %v", k, s.Points)
	return 0
}

func near(a, b float64) bool { return math.Abs(a-b) < 1e-6 }

const statHeader = "btime 1700000000\n"

func TestCPU(t *testing.T) {
	f := newFixture(t)
	f.proc("cpuinfo", "processor\t: 0\nmodel name\t: Test CPU\nphysical id\t: 0\ncore id\t: 0\ncpu MHz\t\t: 1000.0\n\n"+
		"processor\t: 1\nmodel name\t: Test CPU\nphysical id\t: 0\ncore id\t: 1\ncpu MHz\t\t: 3000.0\n")
	f.proc("stat", "cpu  100 0 100 800 0 0 0 0 0 0\ncpu0 50 0 50 400 0 0 0 0 0 0\ncpu1 50 0 50 400 0 0 0 0 0 0\n"+statHeader)

	c := NewCPU(f.fs)
	if err := c.Init(); err != nil {
		t.Fatal(err)
	}
	if c.info.Model != "Test CPU" || c.info.Cores != 2 || c.info.Threads != 2 {
		t.Fatalf("info %+v", c.info)
	}
	t0 := time.Unix(1000, 0)
	first := collectAt(t, c, t0)
	if _, ok := first.Tables[collect.TableCPUInfo]; !ok {
		t.Fatal("cpu info table missing")
	}

	// cpu0: +100 busy of +200 total => 50%; cpu1: +0 busy of +100 => 0%.
	f.proc("stat", "cpu  200 0 100 900 100 0 0 0 0 0\ncpu0 150 0 50 500 0 0 0 0 0 0\ncpu1 50 0 50 400 100 0 0 0 0 0\n"+statHeader)
	s := collectAt(t, c, t0.Add(time.Second))
	if v := value(t, s, "cpu.core.0.usage"); !near(v, 50) {
		t.Errorf("core0 %v", v)
	}
	if v := value(t, s, "cpu.core.1.usage"); !near(v, 0) {
		t.Errorf("core1 %v", v)
	}
	// total: +100 user, +100 idle, +100 iowait of 300 => 33.3% busy, 33.3% iowait
	if v := value(t, s, keyCPUUsage); !near(v, 100.0/3) {
		t.Errorf("total %v", v)
	}
	if v := value(t, s, keyCPUIOWait); !near(v, 100.0/3) {
		t.Errorf("iowait %v", v)
	}
	// No cpufreq in sys: falls back to cpuinfo MHz.
	if v := value(t, s, "cpu.core.1.freq"); v != 3e9 {
		t.Errorf("freq %v", v)
	}
	if v := value(t, s, keyCPUFreq); v != 2e9 {
		t.Errorf("avg freq %v", v)
	}

	f.sys("devices/system/cpu/cpu0/cpufreq/scaling_cur_freq", "2500000\n")
	s = collectAt(t, c, t0.Add(2*time.Second))
	if v := value(t, s, "cpu.core.0.freq"); v != 2.5e9 {
		t.Errorf("cpufreq %v", v)
	}
}

func TestLoad(t *testing.T) {
	f := newFixture(t)
	f.proc("loadavg", "0.50 1.25 2.00 2/345 6789\n")
	f.proc("uptime", "12345.67 54321.00\n")
	f.proc("stat", statHeader)
	f.proc("sys/kernel/osrelease", "6.1.0-test\n")
	l := NewLoad(f.fs)
	if err := l.Init(); err != nil {
		t.Fatal(err)
	}
	s := collectAt(t, l, time.Now())
	if value(t, s, keyLoad5) != 1.25 || value(t, s, keyUptime) != 12345.67 {
		t.Fatalf("points %v", s.Points)
	}
	info := s.Tables[collect.TableSystem].(collect.SystemInfo)
	if info.Kernel != "6.1.0-test" || info.BootTime.Unix() != 1700000000 {
		t.Fatalf("info %+v", info)
	}
}

func TestMem(t *testing.T) {
	f := newFixture(t)
	f.proc("meminfo", `MemTotal:       16000000 kB
MemFree:         2000000 kB
MemAvailable:    8000000 kB
Buffers:          500000 kB
Cached:          4000000 kB
SReclaimable:     500000 kB
SwapTotal:       4000000 kB
SwapFree:        3000000 kB
`)
	m := NewMem(f.fs)
	s := collectAt(t, m, time.Now())
	const k = 1024
	if v := value(t, s, keyMemUsed); v != 8000000*k {
		t.Errorf("used %v", v)
	}
	if v := value(t, s, keyMemCached); v != 4500000*k {
		t.Errorf("cached %v", v)
	}
	if v := value(t, s, keyMemUsedPct); v != 50 {
		t.Errorf("used%% %v", v)
	}
	if v := value(t, s, keySwapUsedPct); v != 25 {
		t.Errorf("swap%% %v", v)
	}

	// ZFS ARC above c_min counts as available.
	f.proc("spl/kstat/zfs/arcstats", "name type data\nc_min 4 1048576000\nsize 4 3145728000\n")
	s = collectAt(t, m, time.Now())
	if v := value(t, s, keyMemAvailable); v != 8000000*k+2097152000 {
		t.Errorf("available with arc %v", v)
	}
}

func TestMounts(t *testing.T) {
	f := newFixture(t)
	f.proc("self/mountinfo", `22 1 259:2 / / rw,relatime shared:1 - ext4 /dev/nvme0n1p2 rw
23 22 0:21 / /proc rw - proc proc rw
24 22 259:1 / /boot/efi rw - vfat /dev/nvme0n1p1 rw
25 22 259:2 /home /srv/home rw - ext4 /dev/nvme0n1p2 rw
26 22 7:0 / /snap/core/1 ro - squashfs /dev/loop0 ro
27 22 0:50 / /mnt/my\040share rw - nfs4 server:/export rw
28 22 0:22 / /run rw - tmpfs tmpfs rw
`)
	d := NewDisks(f.fs)
	ms, err := d.mounts()
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, m := range ms {
		got = append(got, m.Mount)
	}
	want := []string{"/", "/boot/efi", "/mnt/my share"}
	if len(got) != len(want) {
		t.Fatalf("mounts %q", got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("mounts %q want %q", got, want)
		}
	}
	if ms[0].IODevice != "nvme0n1p2" || ms[0].FSType != "ext4" {
		t.Fatalf("root %+v", ms[0])
	}
}

func TestDiskIO(t *testing.T) {
	f := newFixture(t)
	line := func(reads, sr, writes, sw, ms string) string {
		return " 259 0 nvme0n1 " + reads + " 0 " + sr + " 0 " + writes + " 0 " + sw + " 0 0 " + ms + " 0 0 0 0 0\n" +
			"   7 0 loop0 1 0 1 0 1 0 1 0 0 1 0 0 0 0 0\n"
	}
	f.proc("diskstats", line("100", "1000", "50", "2000", "100"))
	d := NewDisks(f.fs)
	t0 := time.Unix(1000, 0)
	if ios, _ := d.diskIO(t0); len(ios) != 0 {
		t.Fatalf("first call should have no rates: %v", ios)
	}
	f.proc("diskstats", line("300", "3048", "150", "6096", "600"))
	ios, err := d.diskIO(t0.Add(2 * time.Second))
	if err != nil || len(ios) != 1 {
		t.Fatalf("ios %v %v", ios, err)
	}
	io := ios[0]
	if io.Name != "nvme0n1" || io.ReadIOPS != 100 || io.WriteIOPS != 50 ||
		io.ReadBps != 1024*512 || io.WriteBps != 2048*512 || io.Util != 25 {
		t.Fatalf("io %+v", io)
	}
}

func TestNet(t *testing.T) {
	f := newFixture(t)
	dev := func(rx, tx string) string {
		return "Inter-|   Receive  |  Transmit\n face |bytes packets errs drop fifo frame compressed multicast|bytes packets errs drop fifo colls carrier compressed\n" +
			"    lo: 100 1 0 0 0 0 0 0 100 1 0 0 0 0 0 0\n" +
			"  eth0: " + rx + " 10 0 0 0 0 0 0 " + tx + " 5 0 0 0 0 0 0\n"
	}
	f.proc("net/dev", dev("1000", "500"))
	f.proc("net/route", "Iface\tDestination\tGateway\neth0\t00000000\t0102A8C0\n")
	f.sys("class/net/eth0/operstate", "up\n")
	f.sys("class/net/eth0/speed", "1000\n")
	n := NewNet(f.fs)
	t0 := time.Unix(1000, 0)
	collectAt(t, n, t0)
	f.proc("net/dev", dev("3000", "1500"))
	s := collectAt(t, n, t0.Add(2*time.Second))
	if v := value(t, s, "net.eth0.rx_bps"); v != 1000 {
		t.Errorf("rx %v", v)
	}
	if v := value(t, s, "net.eth0.tx_bps"); v != 500 {
		t.Errorf("tx %v", v)
	}
	ifaces := s.Tables[collect.TableInterfaces].([]collect.NetInterface)
	var eth collect.NetInterface
	for _, i := range ifaces {
		if i.Name == "eth0" {
			eth = i
		}
	}
	if !eth.Up || !eth.Default || eth.SpeedMbps != 1000 || eth.RxTotal != 3000 {
		t.Fatalf("eth0 %+v", eth)
	}
}

func TestProcs(t *testing.T) {
	f := newFixture(t)
	f.proc("stat", statHeader+"procs_running 3\n")
	f.proc("meminfo", "MemTotal: 1000000 kB\n")
	stat := func(utime string) string {
		// pid (comm) state ppid ... utime(14) stime(15) ... nice(19) threads(20) ... starttime(22) vsize rss
		return "42 (my (weird) proc) S 1 42 42 0 -1 4194304 0 0 0 0 " + utime + " 50 0 0 20 5 3 0 1000 4096000 256 0\n"
	}
	f.proc("42/stat", stat("100"))
	f.proc("42/cmdline", "/usr/bin/app\x00--flag\x00")
	f.proc("42/io", "rchar: 1\nread_bytes: 1000\nwrite_bytes: 2000\n")
	f.proc("7/stat", "7 (kworker/0:1) I 2 0 0 0 -1 0 0 0 0 0 0 0 0 0 20 0 1 0 5 0 0 0\n")
	f.proc("7/cmdline", "")
	f.proc("self", "")

	p := NewProcs(f.fs)
	if err := p.Init(); err != nil {
		t.Fatal(err)
	}
	t0 := time.Unix(1700001000, 0)
	collectAt(t, p, t0)

	f.proc("42/stat", stat("300")) // +200 ticks over 2s = 100% of one core
	f.proc("42/io", "read_bytes: 5000\nwrite_bytes: 2000\n")
	s := collectAt(t, p, t0.Add(2*time.Second))

	procs := s.Tables[collect.TableProcesses].([]collect.Process)
	if len(procs) != 2 {
		t.Fatalf("procs %+v", procs)
	}
	var app, kw collect.Process
	for _, pr := range procs {
		switch pr.PID {
		case 42:
			app = pr
		case 7:
			kw = pr
		}
	}
	if app.Name != "my (weird) proc" || app.Cmdline != "/usr/bin/app --flag" || app.PPID != 1 ||
		app.Nice != 5 || app.Threads != 3 || app.State != 'S' {
		t.Fatalf("app %+v", app)
	}
	if !near(app.CPU, 100) || app.ReadBps != 2000 || app.WriteBps != 0 {
		t.Fatalf("app rates cpu=%v r=%v w=%v", app.CPU, app.ReadBps, app.WriteBps)
	}
	if want := uint64(256 * os.Getpagesize()); app.RSS != want {
		t.Fatalf("rss %v want %v", app.RSS, want)
	}
	if app.StartTime.Unix() != 1700000010 {
		t.Fatalf("start %v", app.StartTime)
	}
	if kw.Cmdline != "[kworker/0:1]" {
		t.Fatalf("kworker cmdline %q", kw.Cmdline)
	}
	if v := value(t, s, "proc.42.cpu"); !near(v, 100) {
		t.Fatalf("proc.42.cpu %v", v)
	}
	if v := value(t, s, keyProcCount); v != 2 {
		t.Fatalf("count %v", v)
	}
	if v := value(t, s, keyProcRunning); v != 3 {
		t.Fatalf("running %v", v)
	}

	// Exited processes are forgotten.
	if err := os.RemoveAll(filepath.Join(f.fs.Proc, "7")); err != nil {
		t.Fatal(err)
	}
	collectAt(t, p, t0.Add(4*time.Second))
	if _, ok := p.prev[7]; ok {
		t.Fatal("stale pid kept")
	}
}

// TestLiveSystem runs every collector against the real /proc twice.
func TestLiveSystem(t *testing.T) {
	if _, err := os.Stat("/proc/stat"); err != nil {
		t.Skip("no /proc")
	}
	for _, c := range Collectors() {
		if err := c.Init(); err != nil {
			t.Fatalf("%s init: %v", c.Name(), err)
		}
		t0 := time.Now()
		collectAt(t, c, t0)
		s := collectAt(t, c, t0.Add(100*time.Millisecond))
		if len(s.Points) == 0 && len(s.Tables) == 0 {
			t.Errorf("%s produced nothing", c.Name())
		}
	}
}

func BenchmarkProcs(b *testing.B) {
	p := NewProcs(DefaultFS)
	if err := p.Init(); err != nil {
		b.Skip(err)
	}
	now := time.Now()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		s := metrics.Sample{Time: now.Add(time.Duration(i) * time.Second)}
		if err := p.Collect(context.Background(), &s); err != nil {
			b.Fatal(err)
		}
	}
}
