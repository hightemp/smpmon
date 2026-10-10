package ui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/hightemp/smpmon/internal/chart"
	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/config"
	"github.com/hightemp/smpmon/internal/keymap"
	"github.com/hightemp/smpmon/internal/metrics"
)

type netOptions struct {
	// Interface to show; "auto" picks the default-route interface.
	Interface string `yaml:"interface"`
	// Scale is "auto" (linear, max of the window) or "log".
	Scale string `yaml:"scale"`
	Mode  string `yaml:"mode"`
}

type netWidget struct {
	opts  netOptions
	iface string // current interface; empty until resolved
	rx    []float64
	tx    []float64
}

func init() {
	register("net", func(n *config.Node) (Widget, error) {
		w := &netWidget{opts: netOptions{Interface: "auto", Scale: "auto"}}
		if err := n.DecodeOptions(&w.opts); err != nil {
			return nil, err
		}
		if w.opts.Interface != "auto" {
			w.iface = w.opts.Interface
		}
		return w, nil
	})
}

func (n *netWidget) Kind() string         { return "net" }
func (n *netWidget) Collectors() []string { return []string{"net"} }

func (n *netWidget) interfaces(ctx *Context) []collect.NetInterface {
	ifs, _ := metrics.TableAs[[]collect.NetInterface](ctx.Store, collect.TableInterfaces)
	return ifs
}

// candidates returns interfaces worth cycling through: up, non-loopback,
// skipping container veths and bridges when something better exists.
func candidates(ifs []collect.NetInterface) []string {
	var main, other []string
	for _, i := range ifs {
		switch {
		case i.Loopback || !i.Up:
		case strings.HasPrefix(i.Name, "veth") || strings.HasPrefix(i.Name, "br-") || i.Name == "docker0":
			other = append(other, i.Name)
		default:
			main = append(main, i.Name)
		}
	}
	return append(main, other...)
}

func (n *netWidget) resolve(ctx *Context) string {
	if n.iface != "" {
		return n.iface
	}
	ifs := n.interfaces(ctx)
	for _, i := range ifs {
		if i.Default {
			n.iface = i.Name
			return n.iface
		}
	}
	if c := candidates(ifs); len(c) > 0 {
		n.iface = c[0]
	}
	return n.iface
}

func (n *netWidget) cycle(ctx *Context, dir int) {
	c := candidates(n.interfaces(ctx))
	if len(c) == 0 {
		return
	}
	i := slices.Index(c, n.resolve(ctx))
	n.iface = c[((i+dir)%len(c)+len(c))%len(c)]
}

func (n *netWidget) Title(ctx *Context) (string, string) {
	return "net", fg(ctx.Theme.Fg, n.resolve(ctx))
}

func (n *netWidget) Render(ctx *Context, w, h int) []string {
	t := ctx.Theme
	iface := n.resolve(ctx)
	if iface == "" || h < 2 {
		return block([]string{fg(t.Dim, "no interface")}, w, h)
	}
	base := metrics.NewKey("net", iface)
	scale := chart.ScaleLinear
	if n.opts.Scale == "log" {
		scale = chart.ScaleLog
	}
	mode := graphMode(ctx, n.opts.Mode)
	top := (h + 1) / 2
	rxG := chart.Graph{Width: w, Height: top, Mode: mode, Scale: scale,
		Series: []chart.Series{{Gradient: t.NetRx}}}
	txG := chart.Graph{Width: w, Height: h - top, Mode: mode, Scale: scale, Invert: true,
		Series: []chart.Series{{Gradient: t.NetTx}}}
	n.rx = ctx.series(base.Child("rx_bps"), n.rx[:0], rxG.Slots())
	n.tx = ctx.series(base.Child("tx_bps"), n.tx[:0], txG.Slots())
	rxG.Series[0].Values, txG.Series[0].Values = n.rx, n.tx

	c := chart.NewCanvas(w, h)
	rxG.Draw(c, 0, 0)
	txG.Draw(c, 0, top)

	var rxTotal, txTotal float64
	if v, ok := ctx.Store.Last(base.Child("rx_total")); ok {
		rxTotal = v
	}
	if v, ok := ctx.Store.Last(base.Child("tx_total")); ok {
		txTotal = v
	}
	_, rxMax := rxG.Range()
	_, txMax := txG.Range()
	label := func(arrow string, cur, peak, total float64) string {
		return arrow + " " + metrics.FormatRate(cur) + "  top " + metrics.FormatRate(peak) + "  Σ " + metrics.FormatBytes(total)
	}
	rxNow, txNow := last(n.rx), last(n.tx)
	c.Text(1, 0, label("▼", rxNow, rxMax, rxTotal), t.Title)
	c.Text(1, h-1, label("▲", txNow, txMax, txTotal), t.Title)
	return c.Lines()
}

func last(v []float64) float64 {
	if len(v) == 0 {
		return 0
	}
	return v[len(v)-1]
}

// HandleKey implements Interactive: b/n cycle interfaces.
func (n *netWidget) HandleKey(ctx *Context, k tea.KeyPressMsg) (bool, tea.Cmd) {
	switch ctx.action(k, keymap.Nav, keymap.Net) {
	case "net_next", "right":
		n.cycle(ctx, 1)
	case "net_prev", "left":
		n.cycle(ctx, -1)
	default:
		return false, nil
	}
	return true, nil
}

// HandleMouse implements Interactive: the wheel cycles interfaces.
func (n *netWidget) HandleMouse(ctx *Context, m tea.MouseMsg, _, _ int) tea.Cmd {
	if wm, ok := m.(tea.MouseWheelMsg); ok {
		if wm.Button == tea.MouseWheelUp {
			n.cycle(ctx, -1)
		} else {
			n.cycle(ctx, 1)
		}
	}
	return nil
}

// Footer implements Interactive.
func (n *netWidget) Footer(ctx *Context) string {
	return joinHints(keyHint(ctx, "net_prev", "prev"), keyHint(ctx, "net_next", "next"))
}

// Capturing implements Interactive.
func (n *netWidget) Capturing() bool { return false }

// hint formats a key hint like "‹n› next".
func hint(ctx *Context, key, label string) string {
	return fg(ctx.Theme.Hotkey, key) + fg(ctx.Theme.Dim, " "+label)
}
