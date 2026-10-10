package remote

import (
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"time"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/metrics"
	"github.com/hightemp/smpmon/internal/source"
)

// Dialer opens a byte stream to an agent.
type Dialer func(ctx context.Context) (io.ReadWriteCloser, error)

// ClientOptions configures a Client.
type ClientOptions struct {
	Name     string // host name shown in the UI
	Dial     Dialer
	Token    string // for TLS agents
	Interval time.Duration
	Store    metrics.StoreOptions
	// HelloTimeout bounds the wait for the agent's hello (SSH login and a
	// cold start can take a few seconds).
	HelloTimeout time.Duration
}

// Client is a source.Source that shows another machine's metrics.
type Client struct {
	opts  ClientOptions
	store *metrics.Store

	mu       sync.Mutex
	interval time.Duration
	demand   []string // nil = all
	conn     *Conn
	info     source.ConnInfo
	hello    *Hello
	status   []source.CollectorStatus
	pending  map[int64]chan *Result
	pingSent map[int64]time.Time
	nextID   int64
}

// NewClient creates a client; call Run to connect.
func NewClient(opts ClientOptions) *Client {
	if opts.Interval <= 0 {
		opts.Interval = time.Second
	}
	if opts.HelloTimeout <= 0 {
		opts.HelloTimeout = 20 * time.Second
	}
	return &Client{
		opts:     opts,
		store:    metrics.NewStore(opts.Store),
		interval: opts.Interval,
		demand:   []string{},
		pending:  map[int64]chan *Result{},
		pingSent: map[int64]time.Time{},
		info:     source.ConnInfo{State: source.Connecting},
	}
}

// Name implements source.Source.
func (c *Client) Name() string { return c.opts.Name }

// Store implements source.Source.
func (c *Client) Store() *metrics.Store { return c.store }

// Interval implements source.Source.
func (c *Client) Interval() time.Duration {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.interval
}

// SetInterval implements source.Source.
func (c *Client) SetInterval(d time.Duration) {
	c.mu.Lock()
	c.interval = max(d, source.MinInterval)
	c.mu.Unlock()
	c.subscribe()
}

// SetDemand implements source.Source.
func (c *Client) SetDemand(names []string) {
	c.mu.Lock()
	if names == nil {
		c.demand = nil
	} else {
		c.demand = slices.Clone(names)
	}
	c.mu.Unlock()
	c.subscribe()
}

// Status implements source.Source.
func (c *Client) Status() []source.CollectorStatus {
	c.mu.Lock()
	defer c.mu.Unlock()
	return slices.Clone(c.status)
}

// Conn implements source.Remote.
func (c *Client) Conn() source.ConnInfo {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.info
}

func (c *Client) subscribe() {
	c.mu.Lock()
	conn := c.conn
	m := &Message{Type: TypeSubscribe, IntervalMS: c.interval.Milliseconds()}
	if c.demand == nil {
		m.All = true
	} else {
		m.Collectors = slices.Clone(c.demand)
	}
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Send(m)
	}
}

func (c *Client) setState(st source.ConnState, err error) {
	c.mu.Lock()
	c.info.State, c.info.Err = st, err
	c.mu.Unlock()
}

// Run connects and reconnects with backoff until ctx is cancelled.
func (c *Client) Run(ctx context.Context) error {
	go c.pruneLoop(ctx)
	backoff := time.Second
	for ctx.Err() == nil {
		start := time.Now()
		err := c.session(ctx)
		if ctx.Err() != nil {
			break
		}
		c.setState(source.Offline, err)
		if time.Since(start) > time.Minute {
			backoff = time.Second // the session was healthy for a while
		}
		select {
		case <-ctx.Done():
		case <-time.After(backoff):
		}
		backoff = min(backoff*2, 30*time.Second)
		c.setState(source.Connecting, err)
	}
	return nil
}

func (c *Client) session(ctx context.Context) error {
	rw, err := c.opts.Dial(ctx)
	if err != nil {
		return err
	}
	conn, hello, err := ClientConn(rw, c.opts.HelloTimeout)
	if err != nil {
		_ = rw.Close()
		return err
	}
	defer func() { _ = conn.Close() }()
	sctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() {
		<-sctx.Done()
		_ = conn.Close()
	}()

	if hello.NeedsToken {
		if err := conn.Send(&Message{Type: TypeAuth, Token: c.opts.Token}); err != nil {
			return err
		}
	}
	c.mu.Lock()
	c.conn, c.hello = conn, hello
	// Online is set on the first message: with a token the agent may still
	// reject us.
	c.info = source.ConnInfo{State: source.Connecting, Hostname: hello.Hostname, OS: hello.OS, Arch: hello.Arch,
		Agent: hello.Smpmon, LastData: c.info.LastData}
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		c.conn = nil
		for id, ch := range c.pending {
			close(ch)
			delete(c.pending, id)
		}
		c.mu.Unlock()
	}()
	c.subscribe()
	go c.pingLoop(sctx, conn)

	for {
		m, err := conn.Recv()
		if err != nil {
			if errors.Is(err, io.EOF) {
				return errors.New("connection closed")
			}
			return err
		}
		if m.Type != TypeError {
			c.mu.Lock()
			c.info.State = source.Online
			c.mu.Unlock()
		}
		switch m.Type {
		case TypeSample:
			if m.Sample == nil {
				continue
			}
			s := decodeSample(m.Sample)
			s.Time = time.Now() // the remote clock may differ from ours
			c.store.Write(s)
			c.mu.Lock()
			c.info.LastData = s.Time
			c.mu.Unlock()
		case TypeStatus:
			st := make([]source.CollectorStatus, 0, len(m.Status))
			for _, cs := range m.Status {
				s := source.CollectorStatus{Name: cs.Name, State: source.ParseState(cs.State)}
				if cs.Error != "" {
					s.Err = errors.New(cs.Error)
				}
				st = append(st, s)
			}
			c.mu.Lock()
			c.status = st
			c.mu.Unlock()
		case TypeResult:
			c.mu.Lock()
			ch, ok := c.pending[m.ID]
			delete(c.pending, m.ID)
			c.mu.Unlock()
			if ok && m.Result != nil {
				ch <- m.Result
				close(ch)
			}
		case TypeWelcome:
			c.mu.Lock()
			h := *c.hello
			h.Control = m.Control
			c.hello = &h
			c.mu.Unlock()
		case TypePong:
			c.mu.Lock()
			if sent, ok := c.pingSent[m.ID]; ok {
				c.info.Latency = max(time.Since(sent), time.Nanosecond) // non-zero once measured
				delete(c.pingSent, m.ID)
			}
			c.mu.Unlock()
		case TypeError:
			return fmt.Errorf("agent: %s", m.Error)
		}
	}
}

func (c *Client) pingLoop(ctx context.Context, conn *Conn) {
	t := time.NewTicker(5 * time.Second)
	defer t.Stop()
	for {
		c.mu.Lock()
		c.nextID++
		id := c.nextID
		c.pingSent[id] = time.Now()
		for k, sent := range c.pingSent { // forget unanswered pings
			if time.Since(sent) > time.Minute {
				delete(c.pingSent, k)
			}
		}
		c.mu.Unlock()
		if conn.Send(&Message{Type: TypePing, ID: id}) != nil {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func (c *Client) pruneLoop(ctx context.Context) {
	t := time.NewTicker(10 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case now := <-t.C:
			// Only prune while data flows; when offline the last values stay.
			if c.Conn().State == source.Online {
				c.store.Prune(now.Add(-max(30*time.Second, 5*c.Interval())))
			}
		}
	}
}

// ProcessController returns a controller that acts on the remote host. Its
// calls fail if the agent doesn't allow process actions.
func (c *Client) ProcessController() collect.ProcessController {
	return remoteProcs{c}
}

type remoteProcs struct{ c *Client }

func (r remoteProcs) Signals() []collect.SignalInfo {
	r.c.mu.Lock()
	defer r.c.mu.Unlock()
	if r.c.hello == nil {
		return nil
	}
	out := make([]collect.SignalInfo, len(r.c.hello.Signals))
	for i, s := range r.c.hello.Signals {
		out[i] = collect.SignalInfo{Name: s.Name, Number: s.Number}
	}
	return out
}

func (r remoteProcs) Signal(pid int32, sig int) error {
	return r.c.act(Action{Kind: "signal", PID: pid, Value: sig})
}

func (r remoteProcs) SetNice(pid int32, nice int) error {
	return r.c.act(Action{Kind: "nice", PID: pid, Value: nice})
}

func (c *Client) act(a Action) error {
	c.mu.Lock()
	conn := c.conn
	if conn == nil {
		c.mu.Unlock()
		return errors.New("host is offline")
	}
	if c.hello != nil && !c.hello.Control {
		c.mu.Unlock()
		return errors.New("this agent doesn't allow process actions")
	}
	c.nextID++
	id := c.nextID
	ch := make(chan *Result, 1)
	c.pending[id] = ch
	c.mu.Unlock()
	if err := conn.Send(&Message{Type: TypeAction, ID: id, Action: &a}); err != nil {
		return err
	}
	select {
	case res, ok := <-ch:
		if !ok {
			return errors.New("connection lost")
		}
		if res.Error != "" {
			return errors.New(res.Error)
		}
		return nil
	case <-time.After(10 * time.Second):
		c.mu.Lock()
		delete(c.pending, id)
		c.mu.Unlock()
		return errors.New("no answer from the agent")
	}
}
