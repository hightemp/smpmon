package ui

import (
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"

	"github.com/hightemp/smpmon/internal/chart"
	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/config"
	"github.com/hightemp/smpmon/internal/metrics"
)

// historyStore holds a minute of CPU, memory, disk and process data.
func historyStore(withTemps bool) *metrics.Store {
	st := metrics.NewStore(metrics.DefaultStoreOptions())
	now := time.Now()
	for i := 60; i >= 0; i-- {
		s := metrics.Sample{Time: now.Add(-time.Duration(i) * time.Second)}
		wave := float64(i % 10 * 10)
		s.Add("cpu.total.usage", 30+wave/2)
		s.Add("cpu.temp", 55)
		for c := range 4 {
			s.Add(metrics.NewKey("cpu", "core", fmt.Sprint(c), "usage"), wave)
			if withTemps {
				s.Add(metrics.NewKey("cpu", "core", fmt.Sprint(c), "temp"), 40+float64(c))
			}
		}
		for k, v := range map[metrics.Key]float64{"mem.total": 16 << 30, "mem.used": 12 << 30, "mem.available": 4 << 30,
			"mem.cached": 3 << 30, "mem.free": 1 << 30, "swap.total": 8 << 30, "swap.used": 2 << 30} {
			s.Add(k, v)
		}
		s.Add("disk.io.sda.read_bps", 1e6*wave)
		s.Add("disk.io.sda.write_bps", 5e5)
		s.Add("proc.42.cpu", wave)
		if i == 0 {
			s.SetTable(collect.TableFilesystems, []collect.Filesystem{{Mount: "/", Total: 100 << 30, Used: 40 << 30, UsedPercent: 40, IODevice: "sda"}})
			s.SetTable(collect.TableDisks, []collect.DiskIO{{Name: "sda", ReadBps: 9e6, WriteBps: 5e5}})
			s.SetTable(collect.TableProcesses, []collect.Process{{PID: 42, Name: "worker", Cmdline: "worker --busy", User: "u", CPU: 90}})
		}
		st.Write(s)
	}
	return st
}

func renderWidget(t *testing.T, st *metrics.Store, kind, opts string, w, h int) string {
	t.Helper()
	n := config.Node{Widget: kind}
	if opts != "" {
		if err := yamlNode(opts, &n); err != nil {
			t.Fatal(err)
		}
	}
	wd, err := newWidget(&n)
	if err != nil {
		t.Fatal(err)
	}
	ctx := &Context{Store: st, Registry: metrics.DefaultRegistry, Theme: themes["default"], Interval: time.Second,
		NumCPU: 4, Now: time.Now(), GraphMode: chart.ModeBraille, GPU: -1}
	lines := wd.Render(ctx, w, h)
	checkFrame(t, fmt.Sprintf("%s %q %dx%d", kind, opts, w, h), lines, w, h)
	return ansi.Strip(strings.Join(lines, "\n"))
}

func hasBraille(s string) bool {
	return strings.ContainsFunc(s, func(r rune) bool { return r > 0x2800 && r <= 0x28ff })
}

func TestCPUCoreGraphs(t *testing.T) {
	st := historyStore(true)
	out := renderWidget(t, st, "cpu", "", 120, 12)
	core := lineWith(out, "C1 ")
	if !hasBraille(core) || !strings.Contains(core, "41°C") {
		t.Errorf("core line %q", core)
	}
	if total := lineWith(out, "CPU "); !strings.Contains(total, "55°C") {
		t.Errorf("total line %q", total)
	}

	// Narrow: temperatures go first, the usage graph stays.
	if core := lineWith(renderWidget(t, st, "cpu", "", 44, 12), "C1 "); !hasBraille(core) || strings.Contains(core, "°C") {
		t.Errorf("narrow core line %q", core)
	}
	if core := lineWith(renderWidget(t, st, "cpu", "style: meters", 120, 12), "C1 "); !strings.Contains(core, "■") || hasBraille(core) {
		t.Errorf("meters core line %q", core)
	}

	// Without per-core sensors there is no temperature column.
	out = renderWidget(t, historyStore(false), "cpu", "", 120, 12)
	if core := lineWith(out, "C1 "); strings.Contains(core, "°C") || !hasBraille(core) {
		t.Errorf("no temps: %q", core)
	}
	bad := yamlOpts(t, "style: bars")
	bad.Widget = "cpu"
	if _, err := newWidget(&bad); err == nil {
		t.Error("unknown style accepted")
	}
}

func yamlOpts(t *testing.T, s string) (n config.Node) {
	t.Helper()
	if err := yamlNode(s, &n); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestMemGraphs(t *testing.T) {
	st := historyStore(true)
	// Tall: a header line and graph rows per item.
	out := renderWidget(t, st, "mem", "", 60, 20)
	lines := strings.Split(out, "\n")
	i := slices.IndexFunc(lines, func(l string) bool { return strings.HasPrefix(l, "Used:") })
	if i < 0 || !strings.Contains(lines[i], "12.0 GiB") || !hasBraille(lines[i+1]) || !strings.Contains(lines[i+1], "75.0%") {
		t.Fatalf("tall:\n%s", out)
	}
	// Short: one line per item with an in-line graph.
	out = renderWidget(t, st, "mem", "", 60, 5)
	if l := lineWith(out, "Swap"); !hasBraille(l) || !strings.Contains(l, "25.0%") || !strings.Contains(l, "2.00 GiB") {
		t.Fatalf("short:\n%s", out)
	}
	out = renderWidget(t, st, "mem", "style: meters", 60, 8)
	if hasBraille(lineWith(out, "Used")) {
		t.Fatalf("meters:\n%s", out)
	}
}

func TestDiskGraphs(t *testing.T) {
	st := historyStore(true)
	out := renderWidget(t, st, "disks", "", 70, 12)
	io := lineWith(out, "r ")
	if !hasBraille(io) || !strings.Contains(io, "8.58 MiB/s") || !strings.Contains(io, "w ") {
		t.Errorf("io line %q", io)
	}
	if !strings.Contains(out, "▲ read") || !strings.Contains(out, "▼ write") {
		t.Errorf("no total graph:\n%s", out)
	}
	out = renderWidget(t, st, "disks", "style: meters", 70, 12)
	if !strings.Contains(out, "busy") || strings.Contains(out, "▲ read") {
		t.Errorf("meters:\n%s", out)
	}
}

func TestProcCPUGraph(t *testing.T) {
	st := historyStore(true)
	if row := lineWith(renderWidget(t, st, "proc", "", 120, 5), "worker"); !hasBraille(row) {
		t.Errorf("wide row %q", row)
	}
	// Narrow: the graph column goes before the command loses its room.
	if row := lineWith(renderWidget(t, st, "proc", "", 70, 5), "worker"); hasBraille(row) || !strings.Contains(row, "22.5") { // 90% of one core of four
		t.Errorf("narrow row %q", row)
	}
}

func lineWith(s, sub string) string {
	for _, l := range strings.Split(s, "\n") {
		if strings.Contains(l, sub) {
			return l
		}
	}
	return ""
}

func TestAddSeries(t *testing.T) {
	sum := addSeries(nil, []float64{1, 2})
	sum = addSeries(sum, []float64{10, 20, 30})
	sum = addSeries(sum, []float64{100})
	if !slices.Equal(sum, []float64{10, 21, 132}) {
		t.Fatalf("%v", sum)
	}
	reused := addSeries(sum[:0], []float64{5})
	if &reused[0] != &sum[0] || !slices.Equal(reused, []float64{5}) {
		t.Fatalf("buffer not reused: %v", reused)
	}
}
