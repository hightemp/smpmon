//go:build linux

package linux

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/metrics"
)

// sensorInput is one hwmon *_input file with its static attributes.
type sensorInput struct {
	path  string
	s     collect.Sensor // static fields; Value is filled per sample
	key   metrics.Key
	scale float64
	pkg   int // CPU package of a coretemp chip
}

// Sensors reads temperatures and fan speeds from /sys/class/hwmon.
type Sensors struct {
	fs      FS
	r       reader
	inputs  []sensorInput
	cpuTemp []int // indexes of inputs that make up cpu.temp
	cores   []coreTemp
	scanned time.Time
}

// sensorsRescan is how often the hwmon tree is re-read for new devices.
const sensorsRescan = time.Minute

// NewSensors creates a Sensors collector.
func NewSensors(fs FS) *Sensors { return &Sensors{fs: fs} }

// Name implements collect.Collector.
func (s *Sensors) Name() string { return "sensors" }

// DefaultInterval implements collect.DefaultIntervaler.
func (s *Sensors) DefaultInterval() time.Duration { return 2 * time.Second }

// Init implements collect.Collector.
func (s *Sensors) Init() error {
	if err := s.scan(); err != nil {
		return err
	}
	s.scanned = time.Now()
	if len(s.inputs) == 0 {
		return fmt.Errorf("no hwmon sensors: %w", collect.ErrUnsupported)
	}
	return nil
}

// cpuChips lists hwmon drivers of CPU temperature sensors and the labels to
// use, best first.
var cpuChips = []struct {
	chip   string
	labels []string
}{
	{"coretemp", []string{"Package id"}},
	{"k10temp", []string{"Tdie", "Tctl"}},
	{"zenpower", []string{"Tdie", "Tctl"}},
	{"cpu_thermal", nil},
	{"soc_thermal", nil},
}

func (s *Sensors) scan() error {
	dir := s.fs.sys("class/hwmon")
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("%s: %w", dir, collect.ErrUnsupported)
		}
		return err
	}
	names := make([]string, 0, len(entries))
	for _, e := range entries {
		names = append(names, e.Name())
	}
	slices.SortFunc(names, func(a, b string) int { return metrics.CompareKeys(metrics.Key(a), metrics.Key(b)) })

	var inputs []sensorInput
	seen := map[string]int{}
	for _, hw := range names {
		base := filepath.Join(dir, hw)
		chip, err := readString(filepath.Join(base, "name"))
		if err != nil {
			continue
		}
		seen[chip]++
		pkg := seen[chip] - 1
		if seen[chip] > 1 {
			chip = chip + "-" + strconv.Itoa(seen[chip]-1)
		}
		// coretemp.N is the chip of CPU package N.
		if dev, err := os.Readlink(filepath.Join(base, "device")); err == nil {
			if n, ok := strings.CutPrefix(filepath.Base(dev), "coretemp."); ok {
				if v, err := strconv.Atoi(n); err == nil {
					pkg = v
				}
			}
		}
		files, _ := os.ReadDir(base)
		for _, f := range files {
			n := f.Name()
			var kind collect.SensorKind
			var prefix string
			switch {
			case strings.HasPrefix(n, "temp") && strings.HasSuffix(n, "_input"):
				kind, prefix = collect.SensorTemp, strings.TrimSuffix(n, "_input")
			case strings.HasPrefix(n, "fan") && strings.HasSuffix(n, "_input"):
				kind, prefix = collect.SensorFan, strings.TrimSuffix(n, "_input")
			default:
				continue
			}
			label, err := readString(filepath.Join(base, prefix+"_label"))
			if err != nil || label == "" {
				label = prefix
			}
			in := sensorInput{
				path: filepath.Join(base, n),
				s:    collect.Sensor{Chip: chip, Label: label, Kind: kind},
				pkg:  pkg,
			}
			if kind == collect.SensorTemp {
				in.scale = 1000 // millidegrees
				in.key = metrics.NewKey("sensors", chip, label, "temp")
				if v, err := readInt(filepath.Join(base, prefix+"_max")); err == nil && v > 0 {
					in.s.High = float64(v) / 1000
				}
				if v, err := readInt(filepath.Join(base, prefix+"_crit")); err == nil && v > 0 {
					in.s.Crit = float64(v) / 1000
				}
			} else {
				in.scale = 1
				in.key = metrics.NewKey("sensors", chip, label, "fan")
			}
			inputs = append(inputs, in)
		}
	}
	slices.SortStableFunc(inputs, func(a, b sensorInput) int {
		if c := strings.Compare(a.s.Chip, b.s.Chip); c != 0 {
			return c
		}
		return collect.NaturalCompare(a.s.Label, b.s.Label)
	})
	s.inputs = inputs
	s.cpuTemp = pickCPUTemp(inputs)
	s.cores = s.coreTemps(inputs)
	return nil
}

// coreTemp is the sensor of one logical CPU.
type coreTemp struct {
	key   metrics.Key // cpu.core.<n>.temp
	input int
}

// coreTemps maps logical CPUs to per-core sensors: coretemp's "Core N"
// is the core with core_id N in its package (threads of a core share it);
// k10temp's "TccdN" covers the CPUs of the Nth L3 cache (CCD), if the
// numbers match. Other chips have no per-core sensors.
func (s *Sensors) coreTemps(inputs []sensorInput) []coreTemp {
	type cpu struct{ n, core, pkg, l3 int }
	var cpus []cpu
	dirs, _ := filepath.Glob(s.fs.sys("devices/system/cpu/cpu[0-9]*"))
	for _, d := range dirs {
		n, err := strconv.Atoi(strings.TrimPrefix(filepath.Base(d), "cpu"))
		if err != nil {
			continue
		}
		c := cpu{n: n, core: -1, l3: -1}
		if v, err := readInt(filepath.Join(d, "topology/core_id")); err == nil {
			c.core = int(v)
		}
		if v, err := readInt(filepath.Join(d, "topology/physical_package_id")); err == nil {
			c.pkg = int(v)
		}
		if v, err := readInt(filepath.Join(d, "cache/index3/id")); err == nil {
			c.l3 = int(v)
		}
		cpus = append(cpus, c)
	}
	slices.SortFunc(cpus, func(a, b cpu) int { return a.n - b.n })
	key := func(n int) metrics.Key { return metrics.NewKey("cpu", "core", strconv.Itoa(n), "temp") }

	var out []coreTemp
	for _, c := range cpus {
		want := "Core " + strconv.Itoa(c.core)
		for i, in := range inputs {
			chip, _, _ := strings.Cut(in.s.Chip, "-")
			if chip == "coretemp" && in.pkg == c.pkg && in.s.Label == want {
				out = append(out, coreTemp{key(c.n), i})
				break
			}
		}
	}
	if len(out) > 0 {
		return out
	}

	var ccds []int // inputs of Tccd1, Tccd2, ...
	for i, in := range inputs {
		chip, _, _ := strings.Cut(in.s.Chip, "-")
		if (chip == "k10temp" || chip == "zenpower") && strings.HasPrefix(in.s.Label, "Tccd") {
			ccds = append(ccds, i)
		}
	}
	var l3s []int
	for _, c := range cpus {
		if c.l3 >= 0 && !slices.Contains(l3s, c.l3) {
			l3s = append(l3s, c.l3)
		}
	}
	slices.Sort(l3s)
	if len(ccds) == 0 || len(ccds) != len(l3s) {
		return nil
	}
	for _, c := range cpus {
		if i := slices.Index(l3s, c.l3); i >= 0 {
			out = append(out, coreTemp{key(c.n), ccds[i]})
		}
	}
	return out
}

// pickCPUTemp returns the inputs that represent the CPU temperature.
func pickCPUTemp(inputs []sensorInput) []int {
	for _, c := range cpuChips {
		for _, label := range append(c.labels, "") {
			var idx []int
			for i, in := range inputs {
				chip, _, _ := strings.Cut(in.s.Chip, "-")
				if chip == c.chip && in.s.Kind == collect.SensorTemp &&
					(label == "" || strings.HasPrefix(in.s.Label, label)) {
					idx = append(idx, i)
				}
			}
			if len(idx) > 0 {
				return idx
			}
			if len(c.labels) == 0 {
				break
			}
		}
	}
	return nil
}

func readInt(path string) (int64, error) {
	v, err := readString(path)
	if err != nil {
		return 0, err
	}
	return strconv.ParseInt(v, 10, 64)
}

// Collect implements collect.Collector.
func (s *Sensors) Collect(_ context.Context, sm *metrics.Sample) error {
	if sm.Time.Sub(s.scanned) >= sensorsRescan {
		if err := s.scan(); err != nil {
			return err
		}
		s.scanned = sm.Time
	}
	out := make([]collect.Sensor, 0, len(s.inputs))
	values := make([]float64, len(s.inputs))
	for i, in := range s.inputs {
		b, err := s.r.read(in.path)
		if err != nil {
			values[i] = -1
			continue
		}
		v := float64(parseInt(trimSpace(b))) / in.scale
		values[i] = v
		sm.Add(in.key, v)
		row := in.s
		row.Value = v
		out = append(out, row)
	}
	var cpuMax float64
	found := false
	for _, i := range s.cpuTemp {
		if values[i] >= 0 {
			cpuMax, found = max(cpuMax, values[i]), true
		}
	}
	if found {
		sm.Add("cpu.temp", cpuMax)
	}
	for _, c := range s.cores {
		if v := values[c.input]; v >= 0 {
			sm.Add(c.key, v)
		}
	}
	sm.SetTable(collect.TableSensors, out)
	return nil
}

func trimSpace(b []byte) []byte {
	for len(b) > 0 && (b[len(b)-1] == '\n' || b[len(b)-1] == ' ') {
		b = b[:len(b)-1]
	}
	return b
}
