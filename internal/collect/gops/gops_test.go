package gops

import (
	"context"
	"runtime"
	"testing"
	"time"

	"github.com/shirou/gopsutil/v4/cpu"
	"github.com/shirou/gopsutil/v4/disk"
	"github.com/shirou/gopsutil/v4/process"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/metrics"
)

// TestLive runs every collector twice against the real system. On CI it
// runs on Linux, macOS and Windows.
func TestLive(t *testing.T) {
	want := map[string][]metrics.Key{
		"cpu":   {"cpu.total.usage"},
		"load":  {"system.uptime"},
		"mem":   {"mem.total", "mem.used_percent"},
		"procs": {"proc.count"},
	}
	tables := map[string]string{
		"disks": collect.TableFilesystems,
		"net":   collect.TableInterfaces,
		"procs": collect.TableProcesses,
	}
	for _, c := range Collectors() {
		if err := c.Init(); err != nil {
			t.Fatalf("%s init: %v", c.Name(), err)
		}
		t0 := time.Now()
		s := metrics.Sample{Time: t0}
		if err := c.Collect(context.Background(), &s); err != nil {
			t.Fatalf("%s: %v", c.Name(), err)
		}
		gap := 300 * time.Millisecond
		if c.Name() == "cpu" {
			gap = time.Second // CPU tick counters update coarsely on some systems
		}
		time.Sleep(gap)
		s = metrics.Sample{Time: time.Now()}
		start := time.Now()
		if err := c.Collect(context.Background(), &s); err != nil {
			t.Fatalf("%s: %v", c.Name(), err)
		}
		took := time.Since(start)
		got := map[metrics.Key]float64{}
		for _, p := range s.Points {
			got[p.Key] = p.Value
		}
		for _, k := range want[c.Name()] {
			if _, ok := got[k]; !ok {
				t.Errorf("%s: missing %s", c.Name(), k)
			}
		}
		if name, ok := tables[c.Name()]; ok {
			if _, ok := s.Tables[name]; !ok {
				t.Errorf("%s: missing table %s", c.Name(), name)
			}
		}
		switch c.Name() {
		case "procs":
			procs := s.Tables[collect.TableProcesses].([]collect.Process)
			if len(procs) < 3 {
				t.Errorf("only %d processes", len(procs))
			}
			self := false
			for _, p := range procs {
				if int(p.PID) == pidSelf() && p.Name != "" && p.RSS > 0 && p.Threads > 0 && !p.StartTime.IsZero() {
					self = true
				}
			}
			if !self {
				t.Errorf("own process not found with name, RSS, threads and start time")
			}
		case "disks":
			if fss := s.Tables[collect.TableFilesystems].([]collect.Filesystem); len(fss) == 0 {
				t.Errorf("no filesystems")
			}
		}
		t.Logf("%s: %d points in %v", c.Name(), len(s.Points), took)
	}
}

func TestUsage(t *testing.T) {
	prev := cpu.TimesStat{CPU: "cpu-total", User: 10, System: 10, Idle: 80}
	cur := cpu.TimesStat{CPU: "cpu-total", User: 30, System: 10, Idle: 100}
	if v, ok := usage(cur, prev); !ok || v != 50 {
		t.Fatalf("usage %v %v", v, ok)
	}
	if _, ok := usage(prev, prev); ok {
		t.Fatal("no elapsed time must not produce a value")
	}
}

func TestIODevice(t *testing.T) {
	io := map[string]disk.IOCountersStat{"disk3": {}, "sda1": {}, "nvme0n1p2": {}}
	cases := map[string]string{
		"/dev/disk3s1s1": "disk3",
		"/dev/sda1":      "sda1",
		"/dev/nvme0n1p2": "nvme0n1p2",
		"server:/export": "",
	}
	if runtime.GOOS == "windows" {
		t.Skip("device names differ on Windows")
	}
	for dev, want := range cases {
		if got := ioDevice(dev, io); got != want {
			t.Errorf("ioDevice(%q) = %q, want %q", dev, got, want)
		}
	}
}

func TestHelpers(t *testing.T) {
	if stateLetter([]string{process.Running}) != 'R' || stateLetter(nil) != '?' {
		t.Fatal("stateLetter")
	}
	for name, want := range map[string]bool{"en0": false, "eth0": false, "utun3": true, "vEthernet (WSL)": true, "lo0": true} {
		if isVirtual(name) != want {
			t.Errorf("isVirtual(%q) != %v", name, want)
		}
	}
	if rate(10, 20, time.Second) != 0 || rate(30, 10, 2*time.Second) != 10 {
		t.Fatal("rate")
	}
}
