//go:build linux

package linux

import (
	"bytes"
	"context"
	"net"
	"os"
	"syscall"
	"time"
	"unsafe"

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

	// Link attributes (addresses, state, speed, physical) change rarely and
	// cost a netlink dump or several sysfs reads per interface, so they are
	// refreshed every attrsTTL for all interfaces at once.
	attrs   map[string]linkAttrs
	attrsAt time.Time
}

type linkAttrs struct {
	addrs     []string
	up        bool
	speedMbps int
	physical  bool
}

const attrsTTL = 10 * time.Second

// NewNet creates a Net collector.
func NewNet(fs FS) *Net {
	return &Net{fs: fs, prev: make(map[string]netCounters), keys: make(map[string]netKeys), attrs: make(map[string]linkAttrs)}
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
	var addrs map[string][]string
	if s.Time.Sub(n.attrsAt) >= attrsTTL {
		clear(n.attrs)
		n.attrsAt = s.Time
		addrs = interfaceAddrs()
	}

	var ifaces []collect.NetInterface
	var total netCounters
	var totalRx, totalTx float64
	physical := 0
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
		attrs, cached := n.attrs[iface]
		if !cached {
			attrs = n.linkAttrs(iface, ni.Loopback)
			attrs.addrs = addrs[iface]
			n.attrs[iface] = attrs
		}
		ni.Up, ni.SpeedMbps, ni.Addrs = attrs.up, attrs.speedMbps, attrs.addrs

		// Totals only count physical NICs: bridges, veths and tunnels carry
		// the same traffic again.
		isPhysical := attrs.physical
		if isPhysical {
			physical++
			total.rxBytes += cur.rxBytes
			total.txBytes += cur.txBytes
		}

		k := n.keysFor(iface)
		s.Add(k.rxTotal, float64(cur.rxBytes))
		s.Add(k.txTotal, float64(cur.txBytes))
		if !first && hadPrev {
			ni.RxBps = rate(cur.rxBytes, prev.rxBytes, elapsed)
			ni.TxBps = rate(cur.txBytes, prev.txBytes, elapsed)
			s.Add(k.rxBps, ni.RxBps)
			s.Add(k.txBps, ni.TxBps)
			if isPhysical {
				totalRx += ni.RxBps
				totalTx += ni.TxBps
			}
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
	if physical > 0 {
		k := n.keysFor("total")
		s.Add(k.rxTotal, float64(total.rxBytes))
		s.Add(k.txTotal, float64(total.txBytes))
		if !first {
			s.Add(k.rxBps, totalRx)
			s.Add(k.txBps, totalTx)
		}
	}
	s.SetTable(collect.TableInterfaces, ifaces)
	return nil
}

// linkAttrs reads link state from sysfs. An interface backed by a device is
// a real NIC rather than a bridge, veth, tunnel or loopback.
func (n *Net) linkAttrs(iface string, loopback bool) linkAttrs {
	var a linkAttrs
	if state, err := readString(n.fs.sys("class/net", iface, "operstate")); err == nil {
		a.up = state == "up" || (loopback && state == "unknown")
	}
	if speed, err := readUint(n.fs.sys("class/net", iface, "speed")); err == nil && speed < 1<<31 {
		a.speedMbps = int(speed)
	}
	_, err := os.Stat(n.fs.sys("class/net", iface, "device"))
	a.physical = err == nil
	return a
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

// interfaceAddrs returns the addresses of all interfaces keyed by name using
// two netlink dumps. net.Interface.Addrs dumps every address of the system
// for each call, which is quadratic on hosts with many container interfaces.
func interfaceAddrs() map[string][]string {
	ifs, err := net.Interfaces()
	if err != nil {
		return nil
	}
	names := make(map[int]string, len(ifs))
	for _, i := range ifs {
		names[i.Index] = i.Name
	}
	rib, err := syscall.NetlinkRIB(syscall.RTM_GETADDR, syscall.AF_UNSPEC)
	if err != nil {
		return nil
	}
	msgs, err := syscall.ParseNetlinkMessage(rib)
	if err != nil {
		return nil
	}
	out := make(map[string][]string, len(ifs))
	for _, m := range msgs {
		if m.Header.Type != syscall.RTM_NEWADDR || len(m.Data) < syscall.SizeofIfAddrmsg {
			continue
		}
		ifa := (*syscall.IfAddrmsg)(unsafe.Pointer(&m.Data[0]))
		attrs, err := syscall.ParseNetlinkRouteAttr(&m)
		if err != nil {
			continue
		}
		var ip net.IP
		for _, a := range attrs {
			// IFA_LOCAL is the interface's own address on point-to-point
			// links, where IFA_ADDRESS is the peer.
			if a.Attr.Type == syscall.IFA_LOCAL || (a.Attr.Type == syscall.IFA_ADDRESS && ip == nil) {
				ip = net.IP(a.Value)
			}
		}
		if ip == nil {
			continue
		}
		bits := 32
		if ifa.Family == syscall.AF_INET6 {
			bits = 128
		}
		name := names[int(ifa.Index)]
		ipn := net.IPNet{IP: ip, Mask: net.CIDRMask(int(ifa.Prefixlen), bits)}
		out[name] = append(out[name], ipn.String())
	}
	return out
}
