package ui

import (
	"fmt"
	"slices"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/hightemp/smpmon/internal/history"
	"github.com/hightemp/smpmon/internal/metrics"
)

// histView is a recorded time window shown instead of live data.
type histView struct {
	at     time.Time     // end of the window
	window time.Duration // length of the window
	tier   history.Tier
	empty  bool
}

var histWindows = []time.Duration{10 * time.Minute, time.Hour, 6 * time.Hour, 24 * time.Hour, 7 * 24 * time.Hour}

type histMsg struct {
	host   int
	at     time.Time
	window time.Duration
	tier   history.Tier
	store  *metrics.Store
	empty  bool
}

// histStep moves the window by half its length; dir 0 only reloads.
func (m *Model) histStep(dir int) tea.Cmd {
	h := m.hosts[m.cur]
	if h.HistoryDir == "" {
		m.setStatus("history is off: set history.enabled in the config")
		return nil
	}
	view := m.hist
	if view == nil {
		view = &histView{at: time.Now(), window: 10 * time.Minute}
	}
	at := view.at.Add(time.Duration(dir) * view.window / 2)
	if dir > 0 && !at.Before(time.Now()) {
		m.leaveHistory()
		return nil
	}
	if oldest, ok := history.Oldest(h.HistoryDir); ok && at.Before(oldest) {
		at = oldest.Add(view.window)
	}
	return m.loadHistory(at, view.window)
}

func (m *Model) histCycleWindow() tea.Cmd {
	if m.hist == nil {
		return m.histStep(0)
	}
	i := slices.Index(histWindows, m.hist.window)
	return m.loadHistory(m.hist.at, histWindows[(i+1)%len(histWindows)])
}

// loadHistory reads the window in the background.
func (m *Model) loadHistory(at time.Time, window time.Duration) tea.Cmd {
	host := m.cur
	h := m.hosts[host]
	n := max(m.width*2, 120) // enough for a full-width braille graph
	if m.hist == nil {
		m.hist = &histView{at: at, window: window}
	}
	return func() tea.Msg {
		if h.Recorder != nil {
			_ = h.Recorder.Flush() // make the last seconds visible
		}
		from := at.Add(-window)
		tier, series := history.Query(h.HistoryDir, []metrics.Selector{metrics.MustSelector("**")}, from, at)
		tables, _, _ := history.TablesAt(h.HistoryDir, at)
		return histMsg{host: host, at: at, window: window, tier: tier,
			store: history.BuildStore(series, tables, from, at, n), empty: len(series) == 0}
	}
}

func (m *Model) applyHistory(msg histMsg) {
	if m.hist == nil || msg.host != m.cur {
		return // left history or switched hosts meanwhile
	}
	m.hist = &histView{at: msg.at, window: msg.window, tier: msg.tier, empty: msg.empty}
	m.ctx.Store, m.ctx.History = msg.store, true
	m.frozen = ""
}

func (m *Model) leaveHistory() {
	if m.hist == nil {
		return
	}
	m.hist = nil
	m.ctx.Store, m.ctx.History = m.hosts[m.cur].Source.Store(), false
	m.frozen = ""
	m.setStatus("live")
}

// histLabel describes the shown window for the top bar.
func (m *Model) histLabel() string {
	t := m.ctx.Theme
	h := m.hist
	from := h.at.Add(-h.window)
	layout := "15:04"
	if h.window >= 24*time.Hour || time.Since(from) > 20*time.Hour {
		layout = "Jan 02 15:04"
	}
	label := fmt.Sprintf("◀ HISTORY %s–%s", from.Format(layout), h.at.Format(layout))
	detail := fmt.Sprintf(" %s window", shortDuration(h.window))
	if h.tier != "" {
		detail += ", " + string(h.tier) + " points"
	}
	if h.empty {
		detail = " no data recorded here"
	}
	return fg(t.Warn, bold(label)) + fg(t.Dim, detail)
}

func shortDuration(d time.Duration) string {
	switch {
	case d >= 24*time.Hour && d%(24*time.Hour) == 0:
		return fmt.Sprintf("%dd", d/(24*time.Hour))
	case d >= time.Hour && d%time.Hour == 0:
		return fmt.Sprintf("%dh", d/time.Hour)
	}
	return fmt.Sprintf("%dm", d/time.Minute)
}
