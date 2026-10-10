package gops

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"time"

	"github.com/shirou/gopsutil/v4/sensors"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/metrics"
)

// Sensors reads temperatures through gopsutil (SMC on macOS, WMI on
// Windows, which often needs administrator rights).
type Sensors struct{}

// Name implements collect.Collector.
func (s *Sensors) Name() string { return "sensors" }

// DefaultInterval implements collect.DefaultIntervaler.
func (s *Sensors) DefaultInterval() time.Duration { return 2 * time.Second }

// Init implements collect.Collector.
func (s *Sensors) Init() error {
	temps, err := sensors.TemperaturesWithContext(context.Background())
	if len(temps) == 0 {
		return fmt.Errorf("no temperature sensors (%w): %w", err, collect.ErrUnsupported)
	}
	return nil
}

// cpuSensor reports whether a sensor name looks like a CPU temperature.
func cpuSensor(name string) bool {
	n := strings.ToLower(name)
	for _, s := range []string{"cpu", "package", "tdie", "tctl", "tc0p", "tc0d", "pmu tdie", "core"} {
		if strings.Contains(n, s) {
			return true
		}
	}
	return false
}

// Collect implements collect.Collector.
func (s *Sensors) Collect(ctx context.Context, sm *metrics.Sample) error {
	temps, err := sensors.TemperaturesWithContext(ctx)
	if len(temps) == 0 {
		return err // partial results come with an error and are still useful
	}
	out := make([]collect.Sensor, 0, len(temps))
	var cpuMax float64
	cpuFound := false
	for _, t := range temps {
		if t.Temperature <= 0 || t.Temperature > 150 { // bogus readings
			continue
		}
		chip, label := "smc", t.SensorKey
		if c, l, ok := strings.Cut(t.SensorKey, "_"); ok && c != "" && l != "" {
			chip, label = c, l
		}
		sm.Add(metrics.NewKey("sensors", chip, label, "temp"), t.Temperature)
		out = append(out, collect.Sensor{
			Chip: chip, Label: label, Kind: collect.SensorTemp, Value: t.Temperature, High: t.High, Crit: t.Critical,
		})
		if cpuSensor(t.SensorKey) {
			cpuMax, cpuFound = max(cpuMax, t.Temperature), true
		}
	}
	if cpuFound {
		sm.Add("cpu.temp", cpuMax)
	}
	slices.SortFunc(out, func(a, b collect.Sensor) int {
		if c := strings.Compare(a.Chip, b.Chip); c != 0 {
			return c
		}
		return collect.NaturalCompare(a.Label, b.Label)
	})
	sm.SetTable(collect.TableSensors, out)
	return nil
}
