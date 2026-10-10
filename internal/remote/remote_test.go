package remote

import (
	"context"
	"errors"
	"io"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/metrics"
	"github.com/hightemp/smpmon/internal/source"
)

type fakeCollector struct {
	name  string
	calls atomic.Int64
}

func (f *fakeCollector) Name() string { return f.name }
func (f *fakeCollector) Init() error  { return nil }
func (f *fakeCollector) Collect(_ context.Context, s *metrics.Sample) error {
	n := f.calls.Add(1)
	s.Add(metrics.NewKey(f.name, "n"), float64(n))
	s.SetTable(collect.TableProcesses, []collect.Process{{PID: 42, Name: f.name}})
	return nil
}

type fakeProcs struct {
	mu      sync.Mutex
	signals []int
}

func (p *fakeProcs) Signals() []collect.SignalInfo {
	return []collect.SignalInfo{{Name: "SIGTERM", Number: 15}}
}
func (p *fakeProcs) Signal(pid int32, sig int) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if pid == 0 {
		return errors.New("no such process")
	}
	p.signals = append(p.signals, sig)
	return nil
}
func (p *fakeProcs) SetNice(int32, int) error { return nil }

func waitFor(t *testing.T, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for !cond() {
		if time.Now().After(deadline) {
			t.Fatalf("timeout waiting for %s", what)
		}
		time.Sleep(10 * time.Millisecond)
	}
}

// pipeDialer serves an agent session on the other end of a loopback TCP
// connection for every dial. (net.Pipe is unbuffered, which deadlocks when
// both sides write at once; real transports have buffers.)
func pipeDialer(opts AgentOptions, dials *atomic.Int64, sessions chan<- net.Conn) Dialer {
	return func(ctx context.Context) (io.ReadWriteCloser, error) {
		dials.Add(1)
		l, err := net.Listen("tcp", "127.0.0.1:0")
		if err != nil {
			return nil, err
		}
		defer func() { _ = l.Close() }()
		accepted := make(chan net.Conn, 1)
		go func() {
			a, err := l.Accept()
			if err != nil {
				close(accepted)
				return
			}
			accepted <- a
		}()
		b, err := net.Dial("tcp", l.Addr().String())
		if err != nil {
			return nil, err
		}
		a, ok := <-accepted
		if !ok {
			return nil, errors.New("accept failed")
		}
		if sessions != nil {
			sessions <- a
		}
		go func() { _ = Serve(ctx, a, opts) }()
		return b, nil
	}
}

func TestAgentClient(t *testing.T) {
	cpu, mem := &fakeCollector{name: "cpu"}, &fakeCollector{name: "mem"}
	procs := &fakeProcs{}
	var dials atomic.Int64
	opts := AgentOptions{
		Collectors: func() []collect.Collector { return []collect.Collector{cpu, mem} },
		Procs:      procs, Control: true, Version: "test",
	}
	c := NewClient(ClientOptions{Name: "srv", Dial: pipeDialer(opts, &dials, nil), Interval: 100 * time.Millisecond})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Run(ctx) }()

	waitFor(t, "online", func() bool { return c.Conn().State == source.Online })
	if info := c.Conn(); info.Agent != "test" || info.Hostname == "" {
		t.Fatalf("info %+v", info)
	}
	// Nothing is collected before a subscription with collectors.
	time.Sleep(300 * time.Millisecond)
	if cpu.calls.Load() != 0 {
		t.Fatal("agent collected without demand")
	}
	c.SetDemand([]string{"cpu"})
	waitFor(t, "cpu data", func() bool { return c.Store().Has("cpu.n") })
	if mem.calls.Load() != 0 {
		t.Fatal("mem collected without demand")
	}
	procsTable, ok := metrics.TableAs[[]collect.Process](c.Store(), collect.TableProcesses)
	if !ok || procsTable[0].PID != 42 {
		t.Fatalf("table %v %v", procsTable, ok)
	}
	waitFor(t, "status", func() bool { return len(c.Status()) == 2 })
	waitFor(t, "latency", func() bool { return c.Conn().Latency > 0 })

	// Process actions round-trip.
	pc := c.ProcessController()
	if sigs := pc.Signals(); len(sigs) != 1 || sigs[0].Name != "SIGTERM" {
		t.Fatalf("signals %v", sigs)
	}
	if err := pc.Signal(7, 15); err != nil {
		t.Fatal(err)
	}
	if err := pc.Signal(0, 15); err == nil || !strings.Contains(err.Error(), "no such process") {
		t.Fatalf("remote error not propagated: %v", err)
	}
	procs.mu.Lock()
	if len(procs.signals) != 1 {
		t.Fatalf("signals sent %v", procs.signals)
	}
	procs.mu.Unlock()

	// Subscribing to everything turns mem on.
	c.SetDemand(nil)
	waitFor(t, "mem data", func() bool { return c.Store().Has("mem.n") })
}

func TestReconnect(t *testing.T) {
	cpu := &fakeCollector{name: "cpu"}
	var dials atomic.Int64
	sessions := make(chan net.Conn, 4)
	opts := AgentOptions{Collectors: func() []collect.Collector { return []collect.Collector{cpu} }}
	c := NewClient(ClientOptions{Name: "srv", Dial: pipeDialer(opts, &dials, sessions), Interval: 100 * time.Millisecond})
	c.SetDemand([]string{"cpu"})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = c.Run(ctx) }()
	first := <-sessions
	waitFor(t, "data", func() bool { return c.Store().Has("cpu.n") })

	_ = first.Close() // the server goes away
	waitFor(t, "offline", func() bool { return c.Conn().State != source.Online })
	if c.Conn().Err == nil {
		t.Fatal("no error reported")
	}
	// Last values stay while offline.
	if !c.Store().Has("cpu.n") {
		t.Fatal("data dropped while offline")
	}
	waitFor(t, "reconnect", func() bool { return dials.Load() >= 2 && c.Conn().State == source.Online })
	n, _ := c.Store().Last("cpu.n")
	waitFor(t, "fresh data", func() bool { v, _ := c.Store().Last("cpu.n"); return v != n })
	// Actions are refused when the agent has no control.
	if err := c.ProcessController().Signal(1, 15); err == nil {
		t.Fatal("action allowed without control")
	}
}

func TestAuth(t *testing.T) {
	opts := AgentOptions{
		Collectors: func() []collect.Collector { return []collect.Collector{&fakeCollector{name: "cpu"}} },
		Procs:      &fakeProcs{}, ReadToken: "reader", ControlToken: "admin",
	}
	try := func(token string) (*Client, context.CancelFunc) {
		var dials atomic.Int64
		c := NewClient(ClientOptions{Name: "srv", Dial: pipeDialer(opts, &dials, nil), Token: token, Interval: 100 * time.Millisecond})
		c.SetDemand(nil)
		ctx, cancel := context.WithCancel(context.Background())
		go func() { _ = c.Run(ctx) }()
		return c, cancel
	}
	bad, cancel := try("wrong")
	waitFor(t, "auth failure", func() bool {
		err := bad.Conn().Err
		return err != nil && strings.Contains(err.Error(), "authentication failed")
	})
	cancel()

	reader, cancel := try("reader")
	defer cancel()
	waitFor(t, "reader data", func() bool { return reader.Store().Has("cpu.n") })
	if err := reader.ProcessController().Signal(1, 15); err == nil {
		t.Fatal("read token allowed an action")
	}

	admin, cancel2 := try("admin")
	defer cancel2()
	waitFor(t, "admin data", func() bool { return admin.Store().Has("cpu.n") })
	if err := admin.ProcessController().Signal(1, 15); err != nil {
		t.Fatalf("control token refused: %v", err)
	}
}

type rwc struct {
	io.Reader
	io.Writer
}

func (rwc) Close() error { return nil }

func TestHelloParsing(t *testing.T) {
	// Login noise before the hello is skipped.
	noisy := "Welcome to Ubuntu\nLast login: today\n" + `{"proto":"smpmon-agent","version":1,"hostname":"h"}` + "\n"
	_, h, err := ClientConn(rwc{strings.NewReader(noisy), io.Discard}, time.Second)
	if err != nil || h.Hostname != "h" {
		t.Fatalf("noisy hello: %v %v", h, err)
	}
	// A missing smpmon gives the shell's message.
	_, _, err = ClientConn(rwc{strings.NewReader("bash: smpmon: command not found\n"), io.Discard}, time.Second)
	if err == nil || !strings.Contains(err.Error(), "command not found") {
		t.Fatalf("missing agent: %v", err)
	}
	// Incompatible versions are explained.
	_, _, err = ClientConn(rwc{strings.NewReader(`{"proto":"smpmon-agent","version":99,"smpmon":"9.0.0"}` + "\n"), io.Discard}, time.Second)
	if err == nil || !strings.Contains(err.Error(), "protocol 99") {
		t.Fatalf("version mismatch: %v", err)
	}
	// Silence times out.
	pr, _ := io.Pipe()
	_, _, err = ClientConn(rwc{pr, io.Discard}, 100*time.Millisecond)
	if !errors.Is(err, ErrNotAgent) {
		t.Fatalf("timeout: %v", err)
	}
}

func TestSampleRoundTrip(t *testing.T) {
	var s metrics.Sample
	s.Time = time.UnixMilli(12345)
	s.Add("cpu.total.usage", 12.5)
	s.SetTable(collect.TableInterfaces, []collect.NetInterface{{Name: "eth0", Up: true, RxBps: 10}})
	ws, err := encodeSample("net", s)
	if err != nil {
		t.Fatal(err)
	}
	back := decodeSample(ws)
	if back.Points[0].Key != "cpu.total.usage" || back.Points[0].Value != 12.5 {
		t.Fatalf("points %+v", back.Points)
	}
	ifs := back.Tables[collect.TableInterfaces].([]collect.NetInterface)
	if ifs[0].Name != "eth0" || !ifs[0].Up {
		t.Fatalf("table %+v", ifs)
	}
}
