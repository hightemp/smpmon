package ui

import (
	"context"
	"io"
	"net"
	"slices"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/collect/platform"
	"github.com/hightemp/smpmon/internal/config"
	"github.com/hightemp/smpmon/internal/metrics"
	"github.com/hightemp/smpmon/internal/remote"
	"github.com/hightemp/smpmon/internal/source"
)

// remoteHost starts an in-process agent with this machine's collectors and
// a client connected to it over loopback TCP. Closing the returned listener
// side simulates the server going away.
func remoteHost(t *testing.T, name string) (*remote.Client, func()) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(cancel)
	var server net.Conn
	dial := func(ctx context.Context) (io.ReadWriteCloser, error) {
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		defer func() { _ = l.Close() }()
		go func() {
			a, err := l.Accept()
			if err != nil {
				return
			}
			server = a
			_ = remote.Serve(ctx, a, remote.AgentOptions{Collectors: platform.Collectors, Procs: platform.ProcessController(), Control: true, Version: "test"})
		}()
		return net.Dial("tcp", l.Addr().String())
	}
	c := remote.NewClient(remote.ClientOptions{Name: name, Dial: dial, Interval: 200 * time.Millisecond, Store: metrics.DefaultStoreOptions()})
	go func() { _ = c.Run(ctx) }()
	return c, func() {
		if server != nil {
			_ = server.Close()
		}
	}
}

func TestMultiHost(t *testing.T) {
	local := liveSource(t)
	srv, drop := remoteHost(t, "srv")
	cfg, err := config.Parse([]byte(`
general:
  dashboards: [btop, pair]
hosts:
  srv: {ssh: srv}
dashboards:
  pair:
    layout:
      row:
        - widget: cpu
        - widget: cpu
          source: srv
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := CheckConfig(cfg); err != nil {
		t.Fatal(err)
	}
	m, err := New(Options{Config: cfg, Hosts: []HostSource{
		{Name: "local", Source: local, Procs: platform.ProcessController()},
		{Name: "srv", Source: srv, Procs: srv.ProcessController()},
	}})
	if err != nil {
		t.Fatal(err)
	}
	m.Update(tea.WindowSizeMsg{Width: 140, Height: 44})
	if !slices.Contains(m.dashboardNames(), "hosts") {
		t.Fatalf("hosts dashboard not added: %v", m.dashboardNames())
	}
	waitFor(t, "remote online", func() bool { return srv.Conn().State == source.Online })

	// Switch with H.
	m.Update(key("H"))
	d, ok := m.dialog.(*chooseDialog)
	if !ok || len(d.options) != 2 {
		t.Fatalf("host dialog %T", m.dialog)
	}
	d.sel = 1
	m.Update(key("enter"))
	if m.hosts[m.cur].Name != "srv" || m.ctx.Store != srv.Store() {
		t.Fatal("host not switched")
	}
	waitFor(t, "remote processes", func() bool {
		_, ok := metrics.TableAs[[]collect.Process](srv.Store(), collect.TableProcesses)
		return ok
	})
	lines := viewLines(t, m)
	checkFrame(t, "remote btop", lines, 140, 44)
	if !strings.Contains(ansi.Strip(lines[0]), "srv") {
		t.Fatalf("top bar lacks host: %q", ansi.Strip(lines[0]))
	}
	// The kill dialog names the host.
	w := m.focusedWidget().(*procWidget)
	m.Update(key("down"))
	if _, cmd := m.Update(key("k")); cmd != nil {
		if msg, ok := cmd().(openDialogMsg); ok {
			if !strings.Contains(msg.d.(*confirmDialog).text, "on srv") {
				t.Fatalf("dialog text %q", msg.d.(*confirmDialog).text)
			}
		}
	}
	_ = w

	// Hosts overview: enter on the local card switches back.
	m.switchDashboard(slices.Index(m.dashboardNames(), "hosts"))
	waitFor(t, "remote cpu", func() bool { return srv.Store().Has("cpu.total.usage") })
	lines = viewLines(t, m)
	checkFrame(t, "hosts overview", lines, 140, 44)
	frame := ansi.Strip(strings.Join(lines, "\n"))
	if !strings.Contains(frame, "srv") || !strings.Contains(frame, "local") || !strings.Contains(frame, "CPU") {
		t.Fatalf("overview:\n%s", frame)
	}
	_, cmd := m.Update(key("enter"))
	if cmd == nil {
		t.Fatal("enter on a card did nothing")
	}
	m.Update(cmd())
	if m.hosts[m.cur].Name != "local" {
		t.Fatalf("now on %s", m.hosts[m.cur].Name)
	}

	// Mixed dashboard: the second cpu widget reads srv.
	m.switchDashboard(slices.Index(m.dashboardNames(), "pair"))
	lines = viewLines(t, m)
	checkFrame(t, "pair", lines, 140, 44)
	if !strings.Contains(ansi.Strip(strings.Join(lines, "\n")), "cpu @srv") {
		t.Fatal("source widget not labelled")
	}

	// The server goes away: the badge says offline, last data stays.
	m.setHost(1)
	drop()
	waitFor(t, "offline", func() bool { return srv.Conn().State != source.Online })
	m.Update(heartbeatMsg{})
	bar := ansi.Strip(viewLines(t, m)[0])
	if !strings.Contains(bar, "offline") && !strings.Contains(bar, "connecting") {
		t.Fatalf("bar %q", bar)
	}
	if !srv.Store().Has("cpu.total.usage") {
		t.Fatal("data dropped")
	}
}

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(10 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(20 * time.Millisecond)
	}
}
