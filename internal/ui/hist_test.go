package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/collect/platform"
	"github.com/hightemp/smpmon/internal/config"
	"github.com/hightemp/smpmon/internal/history"
	"github.com/hightemp/smpmon/internal/metrics"
)

// run executes a command synchronously and feeds its message back.
func run(m *Model, cmd tea.Cmd) {
	if cmd != nil {
		if msg := cmd(); msg != nil {
			m.Update(msg)
		}
	}
}

func TestHistoryMode(t *testing.T) {
	live := liveSource(t)
	dir := t.TempDir()

	// Record 20 minutes ending a minute ago: CPU ramps up, one process.
	rec := metrics.NewStore(metrics.DefaultStoreOptions())
	r := history.NewRecorder(rec, history.Options{Dir: dir, Metrics: []metrics.Selector{metrics.MustSelector("**")}, Processes: true})
	end := time.Now().Add(-time.Minute).Truncate(time.Second)
	for i := 0; i < 1200; i++ {
		at := end.Add(time.Duration(i-1200) * time.Second)
		s := metrics.Sample{Time: at}
		s.Add("cpu.total.usage", float64(i)/12)
		s.Add("mem.used_percent", 50)
		s.Add("mem.total", 8<<30)
		s.SetTable(collect.TableProcesses, []collect.Process{{PID: 99, Name: "recorded-proc", CPU: 42}})
		rec.Write(s)
		if err := r.Record(at); err != nil {
			t.Fatal(err)
		}
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}

	m, err := New(Options{Config: config.Default(), Hosts: []HostSource{
		{Name: "local", Source: live, Procs: platform.ProcessController(), HistoryDir: dir},
	}})
	if err != nil {
		t.Fatal(err)
	}
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 44})

	_, cmd := m.Update(key("["))
	run(m, cmd)
	if m.hist == nil || !m.ctx.History {
		t.Fatal("not in history mode")
	}
	lines := viewLines(t, m)
	checkFrame(t, "history", lines, 140, 44)
	frame := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(frame, "HISTORY") || !strings.Contains(frame, "recorded-proc") {
		t.Fatalf("history frame:\n%s", frame)
	}
	cpu, _ := m.ctx.Store.Last("cpu.total.usage")
	if cpu < 75 || cpu > 85 { // the window ends 5 minutes ago, where the ramp is at 80
		t.Fatalf("cpu at window end %v", cpu)
	}

	// w widens the window; [ moves back; ] eventually returns to live.
	_, cmd = m.Update(key("w"))
	run(m, cmd)
	if m.hist.window != time.Hour {
		t.Fatalf("window %v", m.hist.window)
	}
	checkFrame(t, "history 1h", viewLines(t, m), 140, 44)
	at := m.hist.at
	_, cmd = m.Update(key("["))
	run(m, cmd)
	if !m.hist.at.Before(at) {
		t.Fatal("did not move back")
	}
	for i := 0; i < 5 && m.hist != nil; i++ {
		_, cmd = m.Update(key("]"))
		run(m, cmd)
	}
	if m.hist != nil || m.ctx.History || m.ctx.Store != live.Store() {
		t.Fatal("did not return to live data")
	}

	// esc also leaves history.
	_, cmd = m.Update(key("["))
	run(m, cmd)
	m.Update(key("esc"))
	if m.hist != nil {
		t.Fatal("esc did not leave history")
	}

	// Without a history directory there is a hint instead.
	m.hosts[0].HistoryDir = ""
	m.Update(key("["))
	if m.hist != nil || !strings.Contains(m.status, "history is off") {
		t.Fatalf("status %q", m.status)
	}
}
