package remote

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"os"
	"runtime"
	"sync"
	"time"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/metrics"
	"github.com/hightemp/smpmon/internal/source"
)

// AgentOptions configures a session served by the agent.
type AgentOptions struct {
	// Collectors returns fresh collectors for the session.
	Collectors func() []collect.Collector
	// Procs performs process actions; nil disables them.
	Procs collect.ProcessController
	// Control allows process actions without a token (SSH sessions).
	Control bool
	// ReadToken and ControlToken, if set, require the client to
	// authenticate; ControlToken also allows process actions.
	ReadToken, ControlToken string
	// Version is the smpmon version reported in the hello.
	Version string
	// Intervals overrides collector intervals.
	Intervals map[string]time.Duration
}

// Serve runs one agent session on rw until the client disconnects or ctx
// is cancelled. Nothing is collected until the client subscribes.
func Serve(ctx context.Context, rw io.ReadWriteCloser, opts AgentOptions) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	go func() { // unblock reads when the session ends
		<-ctx.Done()
		_ = rw.Close()
	}()

	host, _ := os.Hostname()
	needToken := opts.ReadToken != "" || opts.ControlToken != ""
	hello := Hello{Smpmon: opts.Version, Hostname: host, OS: runtime.GOOS, Arch: runtime.GOARCH, NeedsToken: needToken}
	control := opts.Control && opts.Procs != nil
	if opts.Procs != nil {
		for _, s := range opts.Procs.Signals() {
			hello.Signals = append(hello.Signals, SignalInfo{s.Name, s.Number})
		}
	}
	hello.Control = control
	conn, err := AgentConn(rw, hello)
	if err != nil {
		return err
	}

	if needToken {
		m, err := conn.Recv()
		if err != nil {
			return err
		}
		switch {
		case m.Type == TypeAuth && opts.ControlToken != "" && tokenEqual(m.Token, opts.ControlToken):
			control = opts.Procs != nil
		case m.Type == TypeAuth && opts.ReadToken != "" && tokenEqual(m.Token, opts.ReadToken):
			control = false
		default:
			time.Sleep(time.Second) // slow down guessing
			_ = conn.Send(&Message{Type: TypeError, Error: "authentication failed"})
			return errors.New("authentication failed")
		}
		if err := conn.Send(&Message{Type: TypeWelcome, Control: control}); err != nil {
			return err
		}
	}

	// The first send error ends the session; collectors send concurrently
	// with the receive loop below, which reports it.
	var sendMu sync.Mutex
	var sendErr error
	send := func(m *Message) {
		if err := conn.Send(m); err != nil {
			sendMu.Lock()
			if sendErr == nil {
				sendErr = err
				cancel()
			}
			sendMu.Unlock()
		}
	}
	firstSendErr := func() error {
		sendMu.Lock()
		defer sendMu.Unlock()
		return sendErr
	}

	local := source.NewLocal(source.LocalOptions{
		Intervals: opts.Intervals,
		Store:     metrics.StoreOptions{Capacity: 2}, // the client keeps history
		OnSample: func(name string, s metrics.Sample) {
			ws, err := encodeSample(name, s)
			if err != nil {
				slog.Warn("encode sample", "err", err)
				return
			}
			send(&Message{Type: TypeSample, Sample: ws})
		},
	}, opts.Collectors())
	local.SetDemand([]string{}) // idle until the client subscribes
	go func() { _ = local.Run(ctx) }()
	go statusLoop(ctx, local, send)

	for {
		m, err := conn.Recv()
		if err != nil {
			if err := firstSendErr(); err != nil {
				return err
			}
			if errors.Is(err, io.EOF) || ctx.Err() != nil {
				return nil
			}
			return err
		}
		switch m.Type {
		case TypeSubscribe:
			if m.IntervalMS > 0 {
				local.SetInterval(time.Duration(m.IntervalMS) * time.Millisecond)
			}
			if m.All {
				local.SetDemand(nil)
			} else {
				local.SetDemand(append([]string{}, m.Collectors...))
			}
		case TypePing:
			send(&Message{Type: TypePong, ID: m.ID})
		case TypeAction:
			res := &Result{}
			if err := runAction(m.Action, control, opts.Procs); err != nil {
				res.Error = err.Error()
			}
			send(&Message{Type: TypeResult, ID: m.ID, Result: res})
		}
	}
}

func tokenEqual(a, b string) bool {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b)) == 1
}

func runAction(a *Action, control bool, procs collect.ProcessController) error {
	if !control || procs == nil {
		return errors.New("process actions are not allowed on this agent")
	}
	if a == nil {
		return errors.New("empty action")
	}
	switch a.Kind {
	case "signal":
		return procs.Signal(a.PID, a.Value)
	case "nice":
		return procs.SetNice(a.PID, a.Value)
	}
	return fmt.Errorf("unknown action %q", a.Kind)
}

// statusLoop reports collector states whenever they change.
func statusLoop(ctx context.Context, local *source.Local, send func(*Message)) {
	t := time.NewTicker(time.Second)
	defer t.Stop()
	var last string
	for {
		var states []CollectorState
		for _, st := range local.Status() {
			cs := CollectorState{Name: st.Name, State: st.State.String()}
			if st.Err != nil {
				cs.Error = st.Err.Error()
			}
			states = append(states, cs)
		}
		if b, _ := json.Marshal(states); string(b) != last {
			last = string(b)
			send(&Message{Type: TypeStatus, Status: states})
		}
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
	}
}

func encodeSample(collector string, s metrics.Sample) (*WireSample, error) {
	ws := &WireSample{Collector: collector, Time: s.Time.UnixMilli()}
	for _, p := range s.Points {
		if math.IsNaN(p.Value) || math.IsInf(p.Value, 0) {
			continue // not representable in JSON
		}
		ws.Keys = append(ws.Keys, string(p.Key))
		ws.Values = append(ws.Values, p.Value)
	}
	if len(s.Tables) > 0 {
		ws.Tables = make(map[string]json.RawMessage, len(s.Tables))
		for name, t := range s.Tables {
			raw, err := json.Marshal(t)
			if err != nil {
				return nil, fmt.Errorf("table %s: %w", name, err)
			}
			ws.Tables[name] = raw
		}
	}
	return ws, nil
}

// decodeSample converts a wire sample back into a metrics.Sample.
func decodeSample(ws *WireSample) metrics.Sample {
	s := metrics.Sample{Time: time.UnixMilli(ws.Time)}
	n := min(len(ws.Keys), len(ws.Values))
	s.Points = make([]metrics.Point, n)
	for i := 0; i < n; i++ {
		s.Points[i] = metrics.Point{Key: metrics.Key(ws.Keys[i]), Value: ws.Values[i]}
	}
	for name, raw := range ws.Tables {
		v, err := collect.DecodeTable(name, raw)
		if err != nil {
			continue // a newer agent may send tables we don't know
		}
		s.SetTable(name, v)
	}
	return s
}
