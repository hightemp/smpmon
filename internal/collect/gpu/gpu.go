// Package gpu collects GPU metrics: NVIDIA through nvidia-smi on every OS,
// AMD and Intel through sysfs on Linux.
//
// The data comes from three collectors sharing one set of devices: "gpu"
// for card metrics, "gpu_pcie" for PCIe throughput and "gpu_procs" for
// processes. The last two cost noticeably more (nvidia-smi dmon and pmon,
// a /proc sweep for AMD and Intel), so they only run while shown.
package gpu

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/metrics"
)

// devices are the GPUs found on this machine.
type devices struct {
	once  sync.Once
	err   error
	nv    *nvidia
	sysfs sysfsReader // nil on non-Linux or without AMD/Intel cards
	drm   drmReader   // processes on AMD/Intel cards, nil if unsupported

	nvCount int // NVIDIA cards come first; sysfs cards are numbered after them
}

func (d *devices) init() error {
	d.once.Do(func() {
		nv, err := newNVIDIA(time.Second)
		if err == nil {
			d.nv = nv
			d.nvCount = len(nv.gpus)
		} else {
			slog.Debug("no nvidia gpu", "err", err)
		}
		d.sysfs = newSysfs()
		if d.sysfs != nil {
			d.drm = newDRM(d.sysfs, d.nvCount)
		}
		if d.nv == nil && d.sysfs == nil {
			d.err = fmt.Errorf("no supported GPU: %w", collect.ErrUnsupported)
		}
	})
	return d.err
}

func (d *devices) gpus(withPCIe bool) ([]collect.GPU, error) {
	var gpus []collect.GPU
	if d.nv != nil {
		gpus = d.nv.snapshot(withPCIe)
	}
	if d.sysfs != nil {
		more, err := d.sysfs.read(d.nvCount)
		if err != nil && len(gpus) == 0 {
			return nil, err
		}
		gpus = append(gpus, more...)
	}
	if len(gpus) == 0 {
		return nil, errors.New("no GPU data yet")
	}
	return gpus, nil
}

func (d *devices) close() {
	if d.nv != nil {
		d.nv.close()
	}
}

// Collectors returns the GPU collectors.
func Collectors() []collect.Collector {
	d := &devices{}
	return []collect.Collector{
		&Collector{dev: d, keys: map[int]gpuKeys{}},
		&PCIeCollector{dev: d},
		&ProcCollector{dev: d},
	}
}

// Collector reads card metrics.
type Collector struct {
	dev  *devices
	keys map[int]gpuKeys
}

type gpuKeys struct {
	util, memUtil, memUsed, memTotal, memPct, temp, power, powerLimit, clock, memClock, fan,
	enc, dec, pstate, throttled metrics.Key
}

// Name implements collect.Collector.
func (c *Collector) Name() string { return "gpu" }

// DefaultInterval implements collect.DefaultIntervaler.
func (c *Collector) DefaultInterval() time.Duration { return time.Second }

// Init implements collect.Collector.
func (c *Collector) Init() error { return c.dev.init() }

// Close implements collect.Closer.
func (c *Collector) Close() error {
	c.dev.close()
	return nil
}

func (c *Collector) keysFor(i int) gpuKeys {
	k, ok := c.keys[i]
	if !ok {
		b := metrics.NewKey("gpu", strconv.Itoa(i))
		k = gpuKeys{
			b.Child("util"), b.Child("mem_util"), b.Child("mem_used"), b.Child("mem_total"),
			b.Child("mem_used_percent"), b.Child("temp"), b.Child("power"), b.Child("power_limit"),
			b.Child("clock"), b.Child("mem_clock"), b.Child("fan"),
			b.Child("enc"), b.Child("dec"), b.Child("pstate"), b.Child("throttled"),
		}
		c.keys[i] = k
	}
	return k
}

// Collect implements collect.Collector.
func (c *Collector) Collect(_ context.Context, s *metrics.Sample) error {
	gpus, err := c.dev.gpus(false)
	if err != nil {
		return err
	}
	for _, g := range gpus {
		k := c.keysFor(g.Index)
		add := func(key metrics.Key, v float64) {
			if v >= 0 {
				s.Add(key, v)
			}
		}
		add(k.util, g.Util)
		add(k.memUtil, g.MemUtil)
		add(k.temp, g.Temp)
		add(k.power, g.Power)
		add(k.powerLimit, g.PowerLimit)
		add(k.clock, g.Clock)
		add(k.memClock, g.MemClock)
		add(k.fan, g.Fan)
		add(k.enc, g.Enc)
		add(k.dec, g.Dec)
		add(k.pstate, float64(g.PState))
		if g.Throttle != nil { // nil: the driver doesn't report reasons
			v := 0.0
			if isSlowed(g.Throttle) {
				v = 1
			}
			s.Add(k.throttled, v)
		}
		if g.MemTotal > 0 {
			s.Add(k.memUsed, float64(g.MemUsed))
			s.Add(k.memTotal, float64(g.MemTotal))
			s.Add(k.memPct, 100*float64(g.MemUsed)/float64(g.MemTotal))
		}
	}
	s.SetTable(collect.TableGPUs, gpus)
	return nil
}

// PCIeCollector reads PCIe throughput.
type PCIeCollector struct {
	dev  *devices
	keys map[int][2]metrics.Key
}

// Name implements collect.Collector.
func (c *PCIeCollector) Name() string { return "gpu_pcie" }

// DefaultInterval implements collect.DefaultIntervaler.
func (c *PCIeCollector) DefaultInterval() time.Duration { return time.Second }

// Init implements collect.Collector.
func (c *PCIeCollector) Init() error {
	if err := c.dev.init(); err != nil {
		return err
	}
	if c.dev.nv == nil {
		return fmt.Errorf("PCIe throughput needs an NVIDIA GPU: %w", collect.ErrUnsupported)
	}
	c.keys = map[int][2]metrics.Key{}
	return nil
}

// Collect implements collect.Collector.
func (c *PCIeCollector) Collect(_ context.Context, s *metrics.Sample) error {
	for _, g := range c.dev.nv.snapshot(true) {
		if g.PCIeRX < 0 {
			continue
		}
		k, ok := c.keys[g.Index]
		if !ok {
			b := metrics.NewKey("gpu", strconv.Itoa(g.Index))
			k = [2]metrics.Key{b.Child("pcie_rx"), b.Child("pcie_tx")}
			c.keys[g.Index] = k
		}
		s.Add(k[0], g.PCIeRX)
		s.Add(k[1], g.PCIeTX)
	}
	return nil
}

// ProcCollector lists processes using GPUs.
type ProcCollector struct {
	dev *devices
}

// Name implements collect.Collector.
func (c *ProcCollector) Name() string { return "gpu_procs" }

// DefaultInterval implements collect.DefaultIntervaler.
func (c *ProcCollector) DefaultInterval() time.Duration { return 2 * time.Second }

// Init implements collect.Collector.
func (c *ProcCollector) Init() error {
	if err := c.dev.init(); err != nil {
		return err
	}
	if c.dev.nv == nil && c.dev.drm == nil {
		return fmt.Errorf("GPU processes are not available here: %w", collect.ErrUnsupported)
	}
	return nil
}

// Collect implements collect.Collector.
func (c *ProcCollector) Collect(ctx context.Context, s *metrics.Sample) error {
	var procs []collect.GPUProcess
	if c.dev.nv != nil {
		procs = append(procs, c.dev.nv.processes(ctx)...)
	}
	if c.dev.drm != nil {
		procs = append(procs, c.dev.drm.processes(time.Now())...)
	}
	if procs == nil {
		procs = []collect.GPUProcess{}
	}
	s.SetTable(collect.TableGPUProcs, procs)
	return nil
}
