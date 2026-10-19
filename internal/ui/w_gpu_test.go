package ui

import (
	"context"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/metrics"
	"github.com/hightemp/smpmon/internal/source"
)

// fakeSource is a source with a fixed store and collector states.
type fakeSource struct {
	store  *metrics.Store
	status []source.CollectorStatus
	demand []string
}

func (f *fakeSource) Name() string                     { return "local" }
func (f *fakeSource) Store() *metrics.Store            { return f.store }
func (f *fakeSource) Run(ctx context.Context) error    { <-ctx.Done(); return nil }
func (f *fakeSource) SetDemand(names []string)         { f.demand = names }
func (f *fakeSource) SetInterval(time.Duration)        {}
func (f *fakeSource) Interval() time.Duration          { return time.Second }
func (f *fakeSource) Status() []source.CollectorStatus { return f.status }

// gpuStore holds two GPUs with a minute of history and their processes.
func gpuStore() *metrics.Store {
	st := metrics.NewStore(metrics.DefaultStoreOptions())
	now := time.Now()
	a, b := collect.UnknownGPU(), collect.UnknownGPU()
	a.Index, a.Name, a.Vendor, a.Util, a.MemUsed, a.MemTotal = 0, "Card Alpha", "nvidia", 40, 4<<30, 12<<30
	a.Temp, a.Power, a.PowerLimit, a.PState, a.PCIeGen, a.PCIeWidth = 65, 150, 200, 2, 4, 16
	a.Enc, a.Dec, a.Throttle = 5, 0, []string{"power cap"}
	b.Index, b.Name, b.Vendor, b.Util, b.MemUsed, b.MemTotal, b.Temp = 1, "Card Beta", "amd", 90, 1<<30, 8<<30, 70
	for i := 60; i >= 0; i-- {
		s := metrics.Sample{Time: now.Add(-time.Duration(i) * time.Second)}
		for _, g := range []collect.GPU{a, b} {
			k := metrics.NewKey("gpu", string(rune('0'+g.Index)))
			s.Add(k.Child("util"), g.Util-float64(i%10))
			s.Add(k.Child("mem_used_percent"), 100*float64(g.MemUsed)/float64(g.MemTotal))
			s.Add(k.Child("temp"), g.Temp)
		}
		s.Add("gpu.0.power", 150)
		s.Add("gpu.0.power_limit", 200)
		if i == 0 {
			s.SetTable(collect.TableGPUs, []collect.GPU{a, b})
			s.SetTable(collect.TableGPUProcs, []collect.GPUProcess{
				{PID: 100, GPU: 0, Type: "G", Name: "Xorg", MemUsed: 500 << 20, Util: 3, Enc: 0, Dec: 0},
				{PID: 200, GPU: 0, Type: "C", Name: "trainer", MemUsed: 3 << 30, Util: 35, Enc: -1, Dec: -1},
				{PID: 200, GPU: 1, Type: "G", Name: "trainer", MemUsed: 1 << 30, Util: 80, Enc: -1, Dec: -1},
				{PID: 300, GPU: 1, Type: "G", Name: "game", MemUsed: 100 << 20, Util: 10, Enc: -1, Dec: -1},
			})
			s.SetTable(collect.TableProcesses, []collect.Process{
				{PID: 100, Name: "Xorg", Cmdline: "/usr/lib/xorg/Xorg vt2", User: "root", State: 'S'},
				{PID: 200, Name: "python3", Cmdline: "python3 train.py", User: "alice", State: 'R', CPU: 50},
				{PID: 999, Name: "bash", Cmdline: "bash", User: "alice", State: 'S'},
			})
		}
		st.Write(s)
	}
	return st
}

func plainFrame(t *testing.T, m *Model, w, h int) string {
	t.Helper()
	lines := viewLines(t, m)
	checkFrame(t, "gpu", lines, w, h)
	return ansi.Strip(strings.Join(lines, "\n"))
}

func TestGPUDashboard(t *testing.T) {
	src := &fakeSource{store: gpuStore()}
	m := newTestModel(t, src, 140, 46)
	m.switchDashboard(slices.Index(m.dashboardNames(), "gpu"))
	frame := plainFrame(t, m, 140, 46)
	for _, want := range []string{"0 Card Alpha", "PCIe Gen4 x16", "P2", "⚠ power cap", "1 Card Beta",
		"VRAM 4.00 GiB/12.0 GiB", "gpu procs", "python3 train.py", "game", "Temperature"} {
		if !strings.Contains(frame, want) {
			t.Errorf("missing %q in\n%s", want, frame)
		}
	}
	// pid 200 uses both GPUs: one row, memory summed, type combined.
	for _, l := range strings.Split(frame, "\n") {
		if strings.Contains(l, "train.py") && (!strings.Contains(l, "0,1") || !strings.Contains(l, "C+G") || !strings.Contains(l, "4.00 GiB")) {
			t.Errorf("trainer row %q", l)
		}
	}
	if strings.Contains(frame, "bash") {
		t.Error("process without GPU listed")
	}
	if !slices.Contains(src.demand, "gpu_procs") || !slices.Contains(src.demand, "gpu_pcie") {
		t.Errorf("demand %v", src.demand)
	}

	// u picks the first card: the other one and its processes disappear,
	// graphs follow.
	m.Update(key("u"))
	frame = plainFrame(t, m, 140, 46)
	if !strings.Contains(frame, "Card Alpha") || strings.Contains(frame, "Card Beta") || strings.Contains(frame, "game") {
		t.Errorf("after u:\n%s", frame)
	}
	if !strings.Contains(frame, "gpu.0.temp") {
		t.Errorf("temperature graph not on gpu 0:\n%s", frame)
	}
	m.Update(key("u"))
	frame = plainFrame(t, m, 140, 46)
	if strings.Contains(frame, "Card Alpha") || !strings.Contains(frame, "game") || strings.Contains(frame, "Xorg vt2") {
		t.Errorf("after second u:\n%s", frame)
	}
	m.Update(key("u"))
	if m.ctx.GPU != -1 || !strings.Contains(plainFrame(t, m, 140, 46), "Card Alpha") {
		t.Error("third u should show all GPUs")
	}

	// GPU usage and memory get separate graphs: side by side when wide,
	// stacked when narrow.
	for _, l := range strings.Split(frame, "\n") {
		if strings.Contains(l, "GPU 40.0%") && !strings.Contains(l, "VRAM 4.00 GiB") {
			t.Errorf("wide: GPU and VRAM graphs not side by side: %q", l)
		}
	}
	m.Update(tea.WindowSizeMsg{Width: 70, Height: 40})
	m.Update(key("u")) // one card, so it has room for stacked graphs
	narrow := strings.Split(plainFrame(t, m, 70, 40), "\n")
	m.Update(key("u"))
	m.Update(key("u"))
	gpuRow := slices.IndexFunc(narrow, func(l string) bool { return strings.Contains(l, "GPU 40.0%") })
	memRow := slices.IndexFunc(narrow, func(l string) bool { return strings.Contains(l, "VRAM 4.00 GiB") })
	if gpuRow < 0 || memRow <= gpuRow+1 {
		t.Errorf("narrow: GPU graph at row %d, VRAM graph at row %d", gpuRow, memRow)
	}

	// Two cards in a short widget: side by side even when narrow.
	for _, l := range strings.Split(plainFrame(t, m, 70, 40), "\n") {
		if strings.Contains(l, "GPU 40.0%") && !strings.Contains(l, "VRAM") {
			t.Errorf("narrow two cards: %q", l)
		}
	}

	// Small terminals use the variant without detail graphs.
	m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	frame = plainFrame(t, m, 100, 30)
	if strings.Contains(frame, "Temperature") || !strings.Contains(frame, "gpu procs") {
		t.Errorf("small:\n%s", frame)
	}
}

func TestGPUTabHidden(t *testing.T) {
	src := &fakeSource{store: metrics.NewStore(metrics.DefaultStoreOptions())}
	for _, c := range []string{"gpu", "gpu_pcie", "gpu_procs"} {
		src.status = append(src.status, source.CollectorStatus{Name: c, State: source.StateUnsupported})
	}
	m := newTestModel(t, src, 160, 40)
	labels, tabs := m.tabLabels()
	bar := strings.Join(labels, "")
	if strings.Contains(bar, "GPU") || len(tabs) != len(m.dashboards)-1 {
		t.Fatalf("tabs %q", bar)
	}
	// Numbers follow the visible tabs: 7 is now Containers.
	m.Update(key("7"))
	if m.current().name != "containers" {
		t.Fatalf("7 went to %s", m.current().name)
	}
}
