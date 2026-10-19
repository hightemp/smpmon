// Package export exposes metrics to other tools: a Prometheus endpoint and
// a JSON stream.
package export

import (
	"fmt"
	"io"
	"math"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/hightemp/smpmon/internal/metrics"
)

// Host is a store to export with its name.
type Host struct {
	Name  string
	Store *metrics.Store
}

// mapping turns keys matching Pattern into a metric name and labels.
// Name may reference key segments as {N}; Labels maps label names to
// segment indexes.
type mapping struct {
	Pattern string
	Name    string
	Labels  map[string]int
	sel     metrics.Selector
}

// mappings, most specific first. Keys that match none are exported with a
// name derived from the key.
var mappings = []mapping{
	{Pattern: "cpu.total.*", Name: "cpu_{2}"},
	{Pattern: "cpu.core.*.*", Name: "cpu_core_{3}", Labels: map[string]int{"core": 2}},
	{Pattern: "cpu.freq", Name: "cpu_frequency"},
	{Pattern: "cpu.temp", Name: "cpu_temperature"},
	{Pattern: "load.*", Name: "load", Labels: map[string]int{"period": 1}},
	{Pattern: "system.uptime", Name: "uptime"},
	{Pattern: "mem.*", Name: "memory_{1}"},
	{Pattern: "swap.*", Name: "swap_{1}"},
	{Pattern: "disk.fs.*.*", Name: "filesystem_{3}", Labels: map[string]int{"mount": 2}},
	{Pattern: "disk.io.*.*", Name: "disk_{3}", Labels: map[string]int{"device": 2}},
	{Pattern: "net.total.*", Name: "network_total_{2}"},
	{Pattern: "net.*.*", Name: "network_{2}", Labels: map[string]int{"interface": 1}},
	{Pattern: "proc.count", Name: "processes"},
	{Pattern: "proc.threads", Name: "threads"},
	{Pattern: "proc.running", Name: "processes_running"},
	{Pattern: "proc.*.*", Name: ""}, // per-process series are not exported
	{Pattern: "sensors.*.*.temp", Name: "sensor_temperature", Labels: map[string]int{"chip": 1, "sensor": 2}},
	{Pattern: "sensors.*.*.fan", Name: "sensor_fan", Labels: map[string]int{"chip": 1, "sensor": 2}},
	{Pattern: "gpu.*.*", Name: "gpu_{2}", Labels: map[string]int{"gpu": 1}},
	{Pattern: "battery.*", Name: "battery_{1}"},
	{Pattern: "docker.*.*", Name: "container_{2}", Labels: map[string]int{"container": 1}},
}

var compileOnce sync.Once

func compiled() []mapping {
	compileOnce.Do(func() {
		for i := range mappings {
			mappings[i].sel = metrics.MustSelector(mappings[i].Pattern)
		}
	})
	return mappings
}

// unitSuffix follows the Prometheus naming conventions.
var unitSuffix = map[metrics.Unit]string{
	metrics.UnitPercent:     "_percent",
	metrics.UnitBytes:       "_bytes",
	metrics.UnitBytesPerSec: "_bytes_per_second",
	metrics.UnitCelsius:     "_celsius",
	metrics.UnitHertz:       "_hertz",
	metrics.UnitSeconds:     "_seconds",
	metrics.UnitPerSec:      "_per_second",
	metrics.UnitWatts:       "_watts",
	metrics.UnitRPM:         "_rpm",
}

// abbreviations in key names spelled out for metric names.
var abbreviations = strings.NewReplacer("_pps", "_packets", "_errs", "_errors", "_iops", "_operations",
	"gpu_enc", "gpu_encoder", "gpu_dec", "gpu_decoder")

// sample is one exported value.
type sample struct {
	name   string
	labels [][2]string
	value  float64
	help   string
}

func sanitize(s string) string {
	var b strings.Builder
	for i, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r == '_', r >= '0' && r <= '9' && i > 0:
			b.WriteRune(r)
		default:
			b.WriteByte('_')
		}
	}
	return b.String()
}

// convert maps a key to a Prometheus sample; ok is false for keys that are
// not exported.
func convert(k metrics.Key, v float64) (sample, bool) {
	segs := k.Segments()
	reg := metrics.DefaultRegistry.Lookup(k)
	s := sample{value: v}
	matched := false
	for _, m := range compiled() {
		if !m.sel.Match(k) {
			continue
		}
		if m.Name == "" {
			return sample{}, false
		}
		name := m.Name
		for i, seg := range segs {
			name = strings.ReplaceAll(name, "{"+strconv.Itoa(i)+"}", seg)
		}
		s.name = name
		for label, idx := range m.Labels {
			s.labels = append(s.labels, [2]string{label, segs[idx]})
		}
		matched = true
		break
	}
	if !matched {
		s.name = strings.Join(segs, "_")
	}
	s.name = abbreviations.Replace(s.name)
	if reg != nil {
		s.help = reg.Help
		suffix := unitSuffix[reg.Unit]
		name := strings.TrimSuffix(s.name, "_percent")
		name = strings.TrimSuffix(name, "_bps")
		s.name = name + suffix
		if reg.Unit == metrics.UnitBytes && strings.HasSuffix(name, "_total") {
			s.name = strings.TrimSuffix(name, "_total") + "_bytes_total"
		}
	}
	s.name = "smpmon_" + sanitize(s.name)
	slices.SortFunc(s.labels, func(a, b [2]string) int { return strings.Compare(a[0], b[0]) })
	return s, true
}

func escapeLabel(v string) string {
	return strings.NewReplacer(`\`, `\\`, "\n", `\n`, `"`, `\"`).Replace(v)
}

// Write renders the hosts' current values in the Prometheus text format.
// sels limits the exported keys; empty means all.
func Write(w io.Writer, hosts []Host, sels []metrics.Selector) error {
	type family struct {
		help    string
		samples []sample
	}
	families := map[string]*family{}
	for _, h := range hosts {
		for _, k := range h.Store.Keys(metrics.MustSelector("**")) {
			if len(sels) > 0 && !slices.ContainsFunc(sels, func(s metrics.Selector) bool { return s.Match(k) }) {
				continue
			}
			v, ok := h.Store.Last(k)
			if !ok || math.IsNaN(v) || math.IsInf(v, 0) {
				continue
			}
			s, ok := convert(k, v)
			if !ok {
				continue
			}
			if len(hosts) > 1 || h.Name != "local" {
				s.labels = append([][2]string{{"host", h.Name}}, s.labels...)
			}
			f := families[s.name]
			if f == nil {
				f = &family{help: s.help}
				families[s.name] = f
			}
			f.samples = append(f.samples, s)
		}
	}
	names := make([]string, 0, len(families))
	for n := range families {
		names = append(names, n)
	}
	slices.Sort(names)
	var b strings.Builder
	for _, n := range names {
		f := families[n]
		if f.help != "" {
			fmt.Fprintf(&b, "# HELP %s %s\n", n, f.help)
		}
		typ := "gauge"
		if strings.HasSuffix(n, "_total") {
			typ = "counter"
		}
		fmt.Fprintf(&b, "# TYPE %s %s\n", n, typ)
		for _, s := range f.samples {
			b.WriteString(n)
			if len(s.labels) > 0 {
				b.WriteByte('{')
				for i, l := range s.labels {
					if i > 0 {
						b.WriteByte(',')
					}
					fmt.Fprintf(&b, `%s="%s"`, l[0], escapeLabel(l[1]))
				}
				b.WriteByte('}')
			}
			b.WriteByte(' ')
			b.WriteString(strconv.FormatFloat(s.value, 'g', -1, 64))
			b.WriteByte('\n')
		}
	}
	_, err := io.WriteString(w, b.String())
	return err
}

// Handler serves /metrics.
func Handler(hosts func() []Host, sels []metrics.Selector) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/metrics", func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		if err := Write(w, hosts(), sels); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		}
	})
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		_, _ = io.WriteString(w, "smpmon exporter: see /metrics\n")
	})
	return mux
}
