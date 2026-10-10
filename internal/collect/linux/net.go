//go:build linux

package linux

import (
	"bytes"
	"context"
	"net"
	"os"
	"time"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/metrics"
)

type netCounters struct {
	rxBytes, rxPackets, rxErrs, txBytes, txPackets, txErrs uint64
}

type netKeys struct {
	rxBps, txBps, rxPps, txPps, rxErrs, txErrs, rxTotal, txTotal metrics.Key
}

// Net collects per-interface network throughput.
type Net struct {
	fs     FS
	r      reader
	prev   map[string]netCounters
	prevAt time.Time
	keys   map[string]netKeys

	// Interface addresses change rarely and cost a netlink round trip, so
	// they are refreshed every addrsTTL.
	addrs   map[string][]string
	addrsAt time.Time
}

const addrsTTL = 10 * time.Second

// NewNet creates a Net collector.
func NewNet(fs FS) *Net {
	return &Net{fs: fs, prev: make(map[string]netCounters), keys: make(map[string]netKeys), addrs: make(map[string][]string)}
}

// Name implements collect.Collector.
func (n *Net) Name() string { return "net" }

// Init implements collect.Collector.
func (n *Net) Init() error {
	_, err := os.Stat(n.fs.proc("net/dev"))
	return err
}

func (n *Net) keysFor(iface string) netKeys {
	k, ok := n.keys[iface]
	if !ok {
		b := metrics.NewKey("net", iface)
		k = netKeys{
			b.Child("rx_bps"), b.Child("tx_bps"), b.Child("rx_pps"), b.Child("tx_pps"),
			b.Child("rx_errs"), b.Child("tx_errs"), b.Child("rx_total"), b.Child("tx_total"),
		}
		n.keys[iface] = k
	}
	return k
}

// Collect implements collect.Collector.
func (n *Net) Collect(_ context.Context, s *metrics.Sample) error {
	b, err := n.r.read(n.fs.proc("net/dev"))
	if err != nil {
		return err
	}
	elapsed := s.Time.Sub(n.prevAt)
	first := n.prevAt.IsZero()
	n.prevAt = s.Time
	defRoute := n.defaultInterface()
	if s.Time.Sub(n.addrsAt) >= addrsTTL {
		clear(n.addrs)
		n.addrsAt = s.Time
	}

	var ifaces []collect.NetInterface
	seen := map[string]bool{}
	lines(b, func(l []byte) {
		name, rest, ok := bytes.Cut(l, []byte(":"))
		if !ok {
			return // header lines
		}
		f := bytes.Fields(rest)
		if len(f) < 16 {
			return
		}
		iface := string(bytes.TrimSpace(name))
		cur := netCounters{
			rxBytes: parseUint(f[0]), rxPackets: parseUint(f[1]), rxErrs: parseUint(f[2]),
			txBytes: parseUint(f[8]), txPackets: parseUint(f[9]), txErrs: parseUint(f[10]),
		}
		seen[iface] = true
		prev, hadPrev := n.prev[iface]
		n.prev[iface] = cur

		ni := collect.NetInterface{
			Name:     iface,
			Default:  iface == defRoute,
			Loopback: iface == "lo",
			RxTotal:  cur.rxBytes,
			TxTotal:  cur.txBytes,
		}
		if state, err := readString(n.fs.sys("class/net", iface, "operstate")); err == nil {
			ni.Up = state == "up" || (ni.Loopback && state == "unknown")
		}
		if speed, err := readUint(n.fs.sys("class/net", iface, "speed")); err == nil && speed < 1<<31 {
			ni.SpeedMbps = int(speed)
		}
		addrs, cached := n.addrs[iface]
		if !cached {
			addrs = interfaceAddrs(iface)
			n.addrs[iface] = addrs
		}
		ni.Addrs = addrs

		k := n.keysFor(iface)
		s.Add(k.rxTotal, float64(cur.rxBytes))
		s.Add(k.txTotal, float64(cur.txBytes))
		if !first && hadPrev {
			ni.RxBps = rate(cur.rxBytes, prev.rxBytes, elapsed)
			ni.TxBps = rate(cur.txBytes, prev.txBytes, elapsed)
			s.Add(k.rxBps, ni.RxBps)
			s.Add(k.txBps, ni.TxBps)
			s.Add(k.rxPps, rate(cur.rxPackets, prev.rxPackets, elapsed))
			s.Add(k.txPps, rate(cur.txPackets, prev.txPackets, elapsed))
			s.Add(k.rxErrs, rate(cur.rxErrs, prev.rxErrs, elapsed))
			s.Add(k.txErrs, rate(cur.txErrs, prev.txErrs, elapsed))
		}
		ifaces = append(ifaces, ni)
	})
	for name := range n.prev {
		if !seen[name] {
			delete(n.prev, name)
		}
	}
	s.SetTable(collect.TableInterfaces, ifaces)
	return nil
}

// defaultInterface returns the interface carrying the IPv4 default route.
func (n *Net) defaultInterface() string {
	b, err := os.ReadFile(n.fs.proc("net/route"))
	if err != nil {
		return ""
	}
	var iface string
	lines(b, func(l []byte) {
		f := bytes.Fields(l)
		if iface == "" && len(f) > 2 && string(f[1]) == "00000000" {
			iface = string(f[0])
		}
	})
	return iface
}

func interfaceAddrs(name string) []string {
	ifi, err := net.InterfaceByName(name)
	if err != nil {
		return nil
	}
	addrs, err := ifi.Addrs()
	if err != nil {
		return nil
	}
	out := make([]string, 0, len(addrs))
	for _, a := range addrs {
		out = append(out, a.String())
	}
	return out
}
