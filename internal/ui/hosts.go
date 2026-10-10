package ui

import (
	"fmt"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/config"
	"github.com/hightemp/smpmon/internal/history"
	"github.com/hightemp/smpmon/internal/source"
)

// HostSource is a machine the UI can show: this one or a remote host.
type HostSource struct {
	Name   string
	Source source.Source
	Procs  collect.ProcessController // nil if process actions are unsupported
	// HistoryDir holds the host's recorded history; empty if not recorded.
	HistoryDir string
	Recorder   *history.Recorder
	// Keep lists collectors that run regardless of the visible widgets, so
	// history has no gaps.
	Keep []string
}

// conn returns the connection state of a remote host; local hosts are
// always online.
func (h HostSource) conn() (source.ConnInfo, bool) {
	if r, ok := h.Source.(source.Remote); ok {
		return r.Conn(), true
	}
	return source.ConnInfo{State: source.Online}, false
}

// hostsCollectors are what the hosts overview shows for every host.
var hostsCollectors = []string{"cpu", "load", "mem", "disks", "net"}

func (m *Model) hostIndex(name string) int {
	return slices.IndexFunc(m.hosts, func(h HostSource) bool { return h.Name == name })
}

// ctxFor returns the rendering context for a widget: the current host, or
// the host named in the node's `source`.
func (m *Model) ctxFor(n *config.Node) (*Context, bool) {
	if n.Source == "" || n.Source == m.hosts[m.cur].Name {
		return &m.ctx, true
	}
	i := m.hostIndex(n.Source)
	if i < 0 {
		return &m.ctx, false
	}
	c := m.ctx
	c.Store, c.Procs, c.Host = m.hosts[i].Source.Store(), m.hosts[i].Procs, m.hosts[i].Name
	return &c, true
}

// setHost switches the whole UI to host i.
func (m *Model) setHost(i int) {
	if i < 0 || i >= len(m.hosts) {
		return
	}
	m.leaveHistory()
	m.cur = i
	h := m.hosts[i]
	m.ctx.Store, m.ctx.Procs, m.ctx.Host = h.Source.Store(), h.Procs, h.Name
	m.frozen = ""
	m.relayout()
	m.setStatus("host: " + h.Name)
}

type switchHostMsg struct{ name string }

// updateDemand tells every host which collectors the visible widgets need.
func (m *Model) updateDemand() {
	d := m.current()
	if len(d.places) == 0 {
		return // keep collecting until the first layout
	}
	need := make([][]string, len(m.hosts))
	all := make([]bool, len(m.hosts))
	add := func(i int, cs []string) {
		if cs == nil {
			all[i] = true
			return
		}
		for _, c := range cs {
			if !slices.Contains(need[i], c) {
				need[i] = append(need[i], c)
			}
		}
	}
	for _, p := range d.places {
		w := d.widgets[p.Node]
		if _, ok := w.(*hostsWidget); ok {
			for i := range m.hosts {
				add(i, hostsCollectors)
			}
			continue
		}
		i := m.cur
		if p.Node.Source != "" {
			if j := m.hostIndex(p.Node.Source); j >= 0 {
				i = j
			}
		}
		add(i, w.Collectors())
	}
	for i, h := range m.hosts {
		if len(h.Keep) > 0 {
			add(i, h.Keep)
		}
		switch {
		case all[i]:
			h.Source.SetDemand(nil)
		case need[i] == nil:
			h.Source.SetDemand([]string{})
		default:
			h.Source.SetDemand(need[i])
		}
	}
}

func (m *Model) hostDialog() dialog {
	opts := make([]string, len(m.hosts))
	for i, h := range m.hosts {
		info, remote := h.conn()
		desc := "this machine"
		if remote {
			desc = info.State.String()
			if info.State == source.Online {
				desc = fmt.Sprintf("online  %s %s/%s  %s", info.Hostname, info.OS, info.Arch, formatLatency(info.Latency))
			} else if info.Err != nil {
				desc += "  " + firstLine(info.Err.Error())
			}
		}
		opts[i] = fmt.Sprintf("%-14s %s", h.Name, desc)
	}
	return &chooseDialog{heading: "Host", options: opts, sel: m.cur, onPick: func(i int) tea.Cmd {
		m.setHost(i)
		return nil
	}}
}

// hostBadge renders the current host and its state for the top bar.
func (m *Model) hostBadge() string {
	if len(m.hosts) < 2 {
		return ""
	}
	t := m.ctx.Theme
	h := m.hosts[m.cur]
	info, remote := h.conn()
	dot := fg(t.CPU.At(0), "●")
	state := ""
	switch info.State {
	case source.Connecting:
		dot = fg(t.Warn, "●")
		state = fg(t.Warn, " connecting")
	case source.Offline:
		dot = fg(t.Crit, "●")
		state = fg(t.Crit, " offline")
		if !info.LastData.IsZero() {
			state += fg(t.Dim, " "+time.Since(info.LastData).Round(time.Second).String())
		}
	}
	if !remote {
		dot = fg(t.Dim, "●")
	}
	return dot + " " + fg(t.Title, bold(h.Name)) + state
}

func formatLatency(d time.Duration) string {
	if d < time.Millisecond {
		return "<1ms"
	}
	return d.Round(time.Millisecond).String()
}
