//go:build linux

package linux

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/metrics"
)

// Battery reads /sys/class/power_supply/BAT*.
type Battery struct {
	fs   FS
	bats []string
}

// NewBattery creates a Battery collector.
func NewBattery(fs FS) *Battery { return &Battery{fs: fs} }

// Name implements collect.Collector.
func (b *Battery) Name() string { return "battery" }

// DefaultInterval implements collect.DefaultIntervaler.
func (b *Battery) DefaultInterval() time.Duration { return 5 * time.Second }

// Init implements collect.Collector.
func (b *Battery) Init() error {
	dir := b.fs.sys("class/power_supply")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return fmt.Errorf("%s: %w", dir, collect.ErrUnsupported)
	}
	for _, e := range entries {
		if t, err := readString(filepath.Join(dir, e.Name(), "type")); err == nil && t == "Battery" {
			// Skip peripherals (mice, keyboards) that report a battery.
			if scope, err := readString(filepath.Join(dir, e.Name(), "scope")); err == nil && scope == "Device" {
				continue
			}
			b.bats = append(b.bats, e.Name())
		}
	}
	if len(b.bats) == 0 {
		return fmt.Errorf("no battery: %w", collect.ErrUnsupported)
	}
	return nil
}

// readBattery reads one battery. Drivers report either energy (µWh, µW) or
// charge (µAh, µA); both give the same ratios.
func (b *Battery) readBattery(name string) (collect.Battery, bool) {
	dir := b.fs.sys("class/power_supply", name)
	num := func(f string) (float64, bool) {
		v, err := readInt(filepath.Join(dir, f))
		return float64(v), err == nil
	}
	bat := collect.Battery{Name: name, Status: "unknown"}
	if st, err := readString(filepath.Join(dir, "status")); err == nil {
		bat.Status = strings.ToLower(st)
	}
	now, okNow := num("energy_now")
	full, okFull := num("energy_full")
	rate, okRate := num("power_now")
	energy := okNow && okFull
	if !energy {
		now, okNow = num("charge_now")
		full, okFull = num("charge_full")
		rate, okRate = num("current_now")
	}
	if c, ok := num("capacity"); ok {
		bat.Percent = c
	} else if okNow && okFull && full > 0 {
		bat.Percent = 100 * now / full
	} else {
		return bat, false
	}
	rate = max(rate, -rate) // some drivers report discharge as negative
	if okRate && rate > 0 {
		if energy {
			bat.Power = rate / 1e6
		} else if v, ok := num("voltage_now"); ok {
			bat.Power = rate / 1e6 * v / 1e6
		}
		if okNow && okFull {
			switch bat.Status {
			case "discharging":
				bat.TimeLeft = time.Duration(now / rate * float64(time.Hour))
			case "charging":
				bat.TimeLeft = time.Duration(max(full-now, 0) / rate * float64(time.Hour))
			}
		}
	}
	return bat, true
}

// Collect implements collect.Collector.
func (b *Battery) Collect(_ context.Context, s *metrics.Sample) error {
	var out []collect.Battery
	for _, name := range b.bats {
		if bat, ok := b.readBattery(name); ok {
			out = append(out, bat)
		}
	}
	addBatteryMetrics(s, out)
	return nil
}

// addBatteryMetrics publishes the first battery as battery.* metrics and
// all of them as a table.
func addBatteryMetrics(s *metrics.Sample, bats []collect.Battery) {
	if len(bats) > 0 {
		b := bats[0]
		s.Add("battery.percent", b.Percent)
		s.Add("battery.power", b.Power)
		s.Add("battery.time_left", b.TimeLeft.Seconds())
	}
	s.SetTable(collect.TableBatteries, bats)
}
