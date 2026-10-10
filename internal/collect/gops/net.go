package gops

import (
	"context"
	"net/netip"
	"slices"
	"strings"
	"time"

	gnet "github.com/shirou/gopsutil/v4/net"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/metrics"
)

const ifaceTTL = 10 * time.Second

// virtualPrefixes are interface name prefixes of tunnels, bridges and
// hypervisor adapters, which repeat traffic of physical NICs.
var virtualPrefixes = []string{
	"lo", "utun", "awdl", "llw", "bridge", "gif", "stf", "anpi", "ap", "vmnet", "vboxnet",
	"docker", "veth", "br-", "virbr", "tun", "tap", "wg", "zt", "tailscale",
	"vEthernet", "Loopback", "isatap", "Teredo", "Bluetooth",
}

func isVirtual(name string) bool {
	for _, p := range virtualPrefixes {
		if strings.HasPrefix(name, p) {
			return true
		}
	}
	return false
}

// Net collects per-interface throughput.
type Net struct {
	prev   map[string]gnet.IOCountersStat
	prevAt time.Time

	ifaces   map[string]gnet.InterfaceStat
	ifacesAt time.Time
	def      string
}

// NewNet creates a Net collector.
func NewNet() *Net { return &Net{prev: map[string]gnet.IOCountersStat{}} }

// Name implements collect.Collector.
func (n *Net) Name() string { return "net" }

// Init implements collect.Collector.
func (n *Net) Init() error {
	_, err := gnet.IOCounters(true)
	return err
}

func (n *Net) refreshInterfaces(ctx context.Context) {
	list, err := gnet.InterfacesWithContext(ctx)
	if err != nil {
		return
	}
	n.ifaces = make(map[string]gnet.InterfaceStat, len(list))
	n.def = ""
	for _, i := range list {
		n.ifaces[i.Name] = i
		// Without a portable routing table, the default interface is the
		// first physical one that is up and has a global IPv4 address.
		if n.def == "" && slices.Contains(i.Flags, "up") && !isVirtual(i.Name) && hasGlobalIPv4(i) {
			n.def = i.Name
		}
	}
}

func hasGlobalIPv4(i gnet.InterfaceStat) bool {
	for _, a := range i.Addrs {
		p, err := netip.ParsePrefix(a.Addr)
		if err == nil && p.Addr().Is4() && p.Addr().IsGlobalUnicast() {
			return true
		}
	}
	return false
}

// Collect implements collect.Collector.
func (n *Net) Collect(ctx context.Context, s *metrics.Sample) error {
	counters, err := gnet.IOCountersWithContext(ctx, true)
	if err != nil {
		return err
	}
	if n.ifaces == nil || s.Time.Sub(n.ifacesAt) >= ifaceTTL {
		n.refreshInterfaces(ctx)
		n.ifacesAt = s.Time
	}
	elapsed := s.Time.Sub(n.prevAt)
	first := n.prevAt.IsZero()
	n.prevAt = s.Time

	var (
		out              []collect.NetInterface
		totRx, totTx     uint64
		totRxBps, totTxB float64
		physical         int
	)
	next := make(map[string]gnet.IOCountersStat, len(counters))
	for _, c := range counters {
		next[c.Name] = c
		info := n.ifaces[c.Name]
		ni := collect.NetInterface{
			Name:     c.Name,
			Up:       slices.Contains(info.Flags, "up"),
			Loopback: slices.Contains(info.Flags, "loopback"),
			Default:  c.Name == n.def,
			RxTotal:  c.BytesRecv,
			TxTotal:  c.BytesSent,
		}
		for _, a := range info.Addrs {
			ni.Addrs = append(ni.Addrs, a.Addr)
		}
		phys := !ni.Loopback && !isVirtual(c.Name)
		if phys {
			physical++
			totRx += c.BytesRecv
			totTx += c.BytesSent
		}
		base := metrics.NewKey("net", c.Name)
		s.Add(base.Child("rx_total"), float64(c.BytesRecv))
		s.Add(base.Child("tx_total"), float64(c.BytesSent))
		if prev, ok := n.prev[c.Name]; ok && !first {
			ni.RxBps = rate(c.BytesRecv, prev.BytesRecv, elapsed)
			ni.TxBps = rate(c.BytesSent, prev.BytesSent, elapsed)
			s.Add(base.Child("rx_bps"), ni.RxBps)
			s.Add(base.Child("tx_bps"), ni.TxBps)
			s.Add(base.Child("rx_pps"), rate(c.PacketsRecv, prev.PacketsRecv, elapsed))
			s.Add(base.Child("tx_pps"), rate(c.PacketsSent, prev.PacketsSent, elapsed))
			s.Add(base.Child("rx_errs"), rate(c.Errin, prev.Errin, elapsed))
			s.Add(base.Child("tx_errs"), rate(c.Errout, prev.Errout, elapsed))
			if phys {
				totRxBps += ni.RxBps
				totTxB += ni.TxBps
			}
		}
		out = append(out, ni)
	}
	n.prev = next
	if physical > 0 {
		s.Add("net.total.rx_total", float64(totRx))
		s.Add("net.total.tx_total", float64(totTx))
		if !first {
			s.Add("net.total.rx_bps", totRxBps)
			s.Add("net.total.tx_bps", totTxB)
		}
	}
	slices.SortFunc(out, func(a, b collect.NetInterface) int { return strings.Compare(a.Name, b.Name) })
	s.SetTable(collect.TableInterfaces, out)
	return nil
}
