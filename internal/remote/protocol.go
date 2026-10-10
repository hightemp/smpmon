// Package remote implements remote monitoring: an agent that serves a host's
// metrics over a byte stream (an SSH session's stdio or a TLS connection)
// and a client Source that shows them in the UI.
//
// Protocol: the agent first writes one plain JSON line, its Hello. Plain
// text lets the client skip noise that login scripts print on SSH and give a
// clear error for incompatible versions. After that both sides exchange
// JSON messages through a deflate stream that is flushed after every
// message, so repeated keys compress well across messages.
package remote

import (
	"bufio"
	"bytes"
	"compress/flate"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sync"
	"time"
)

// ProtocolVersion is bumped on incompatible changes.
const ProtocolVersion = 1

// magic identifies the agent's hello line.
const magic = "smpmon-agent"

// Hello is sent by the agent when a session starts.
type Hello struct {
	Proto      string       `json:"proto"`
	Version    int          `json:"version"`
	Smpmon     string       `json:"smpmon"`
	Hostname   string       `json:"hostname"`
	OS         string       `json:"os"`
	Arch       string       `json:"arch"`
	Control    bool         `json:"control"` // kill/renice allowed
	Signals    []SignalInfo `json:"signals,omitempty"`
	NeedsToken bool         `json:"needs_token,omitempty"`
}

// SignalInfo mirrors collect.SignalInfo on the wire.
type SignalInfo struct {
	Name   string `json:"name"`
	Number int    `json:"number"`
}

// Message is the envelope of every message after the hello.
type Message struct {
	Type string `json:"t"`

	// client → agent
	Token      string   `json:"token,omitempty"`      // auth (TLS agent)
	Collectors []string `json:"collectors,omitempty"` // subscribe; nil = all
	All        bool     `json:"all,omitempty"`        // subscribe to everything
	IntervalMS int64    `json:"interval_ms,omitempty"`
	Action     *Action  `json:"action,omitempty"`

	// agent → client
	Sample *WireSample      `json:"sample,omitempty"`
	Status []CollectorState `json:"status,omitempty"`
	Result *Result          `json:"result,omitempty"`
	Error  string           `json:"error,omitempty"`

	// Control answers a successful auth: whether process actions are allowed.
	Control bool `json:"control,omitempty"`

	// both
	ID int64 `json:"id,omitempty"` // ping/pong and action correlation
}

// Message types.
const (
	TypeAuth      = "auth"
	TypeWelcome   = "welcome" // successful auth
	TypeSubscribe = "subscribe"
	TypeAction    = "action"
	TypePing      = "ping"
	TypeSample    = "sample"
	TypeStatus    = "status"
	TypeResult    = "result"
	TypePong      = "pong"
	TypeError     = "error"
)

// Action asks the agent to signal or renice a process.
type Action struct {
	Kind  string `json:"kind"` // "signal" or "nice"
	PID   int32  `json:"pid"`
	Value int    `json:"value"`
}

// Result answers an Action.
type Result struct {
	Error string `json:"error,omitempty"`
}

// WireSample is metrics.Sample on the wire, with points as parallel key and
// value arrays.
type WireSample struct {
	Collector string                     `json:"c"`
	Time      int64                      `json:"ts"` // unix ms
	Keys      []string                   `json:"k,omitempty"`
	Values    []float64                  `json:"v,omitempty"`
	Tables    map[string]json.RawMessage `json:"tb,omitempty"`
}

// CollectorState reports a collector's health.
type CollectorState struct {
	Name  string `json:"name"`
	State string `json:"state"`
	Error string `json:"error,omitempty"`
}

// Conn exchanges messages over a stream.
type Conn struct {
	rw io.ReadWriteCloser
	br *bufio.Reader

	wmu sync.Mutex
	fw  *flate.Writer
	enc *json.Encoder

	dec *json.Decoder
}

func newConn(rw io.ReadWriteCloser, br *bufio.Reader) (*Conn, error) {
	fw, err := flate.NewWriter(rw, flate.BestSpeed)
	if err != nil {
		return nil, err
	}
	return &Conn{rw: rw, br: br, fw: fw, enc: json.NewEncoder(fw), dec: json.NewDecoder(flate.NewReader(br))}, nil
}

// Send writes one message and flushes it.
func (c *Conn) Send(m *Message) error {
	c.wmu.Lock()
	defer c.wmu.Unlock()
	if err := c.enc.Encode(m); err != nil {
		return err
	}
	return c.fw.Flush()
}

// Recv reads one message.
func (c *Conn) Recv() (*Message, error) {
	var m Message
	if err := c.dec.Decode(&m); err != nil {
		return nil, err
	}
	return &m, nil
}

// Close closes the underlying stream.
func (c *Conn) Close() error { return c.rw.Close() }

// AgentConn writes the hello and returns the message stream (agent side).
func AgentConn(rw io.ReadWriteCloser, h Hello) (*Conn, error) {
	h.Proto, h.Version = magic, ProtocolVersion
	line, err := json.Marshal(h)
	if err != nil {
		return nil, err
	}
	if _, err := rw.Write(append(line, '\n')); err != nil {
		return nil, err
	}
	return newConn(rw, bufio.NewReader(rw))
}

// ErrNotAgent means the other side didn't answer like an smpmon agent.
var ErrNotAgent = errors.New("no smpmon agent answered")

// maxNoise bounds the output skipped before the hello (login banners).
const maxNoise = 64 << 10

// ClientConn reads the agent's hello and returns the message stream. Lines
// before the hello are skipped and returned as noise for error messages.
func ClientConn(rw io.ReadWriteCloser, timeout time.Duration) (*Conn, *Hello, error) {
	type res struct {
		h     *Hello
		br    *bufio.Reader
		noise string
		err   error
	}
	ch := make(chan res, 1)
	go func() {
		br := bufio.NewReader(rw)
		var noise bytes.Buffer
		for noise.Len() < maxNoise {
			line, err := br.ReadBytes('\n')
			if bytes.HasPrefix(bytes.TrimSpace(line), []byte(`{"proto":"`+magic+`"`)) {
				var h Hello
				if err := json.Unmarshal(line, &h); err != nil {
					ch <- res{err: err}
					return
				}
				ch <- res{h: &h, br: br}
				return
			}
			noise.Write(line)
			if err != nil {
				ch <- res{noise: noise.String(), err: err}
				return
			}
		}
		ch <- res{noise: noise.String(), err: ErrNotAgent}
	}()
	var r res
	select {
	case r = <-ch:
	case <-time.After(timeout):
		_ = rw.Close()
		return nil, nil, fmt.Errorf("%w within %v", ErrNotAgent, timeout)
	}
	if r.err != nil {
		if noise := lastLine(r.noise); noise != "" {
			return nil, nil, fmt.Errorf("%w: %s", ErrNotAgent, noise)
		}
		return nil, nil, fmt.Errorf("%w: %w", ErrNotAgent, r.err)
	}
	if r.h.Version != ProtocolVersion {
		return nil, r.h, fmt.Errorf("agent speaks protocol %d (smpmon %s), this client speaks %d: update smpmon on both sides",
			r.h.Version, r.h.Smpmon, ProtocolVersion)
	}
	c, err := newConn(rw, r.br)
	return c, r.h, err
}

func lastLine(s string) string {
	lines := bytes.Split(bytes.TrimSpace([]byte(s)), []byte("\n"))
	if len(lines) == 0 {
		return ""
	}
	return string(bytes.TrimSpace(lines[len(lines)-1]))
}
