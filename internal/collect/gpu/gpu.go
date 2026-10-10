// Package gpu collects GPU metrics: NVIDIA through nvidia-smi on every OS,
// AMD and Intel through sysfs on Linux.
package gpu

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"time"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/metrics"
)

// Collector reads all supported GPUs.
type Collector struct {
	nv    *nvidia
	sysfs sysfsReader // nil on non-Linux or without AMD/Intel cards
	keys  map[int]gpuKeys
}

type gpuKeys struct {
	util, memUtil, memUsed, memTotal, memPct, temp, power, powerLimit, clock, memClock, fan metrics.Key
}

// New creates the GPU collector.
func New() *Collector { return &Collector{keys: map[int]gpuKeys{}} }

// Name implements collect.Collector.
func (c *Collector) Name() string { return "gpu" }

// DefaultInterval implements collect.DefaultIntervaler.
func (c *Collector) DefaultInterval() time.Duration { return time.Second }

// Init implements collect.Collector.
func (c *Collector) Init() error {
	nv, err := newNVIDIA(time.Second)
	if err == nil {
		c.nv = nv
	} else {
		slog.Debug("no nvidia gpu", "err", err)
	}
	c.sysfs = newSysfs()
	if c.nv == nil && c.sysfs == nil {
		return fmt.Errorf("no supported GPU: %w", collect.ErrUnsupported)
	}
	return nil
}

// Close implements collect.Closer.
func (c *Collector) Close() error {
	if c.nv != nil {
		c.nv.close()
	}
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
		}
		c.keys[i] = k
	}
	return k
}

// Collect implements collect.Collector.
func (c *Collector) Collect(_ context.Context, s *metrics.Sample) error {
	var gpus []collect.GPU
	var procs []collect.GPUProcess
	if c.nv != nil {
		gpus, procs = c.nv.snapshot()
	}
	if c.sysfs != nil {
		more, err := c.sysfs.read(len(gpus))
		if err != nil && len(gpus) == 0 {
			return err
		}
		gpus = append(gpus, more...)
	}
	if len(gpus) == 0 {
		return errors.New("no GPU data yet")
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
		if g.MemTotal > 0 {
			s.Add(k.memUsed, float64(g.MemUsed))
			s.Add(k.memTotal, float64(g.MemTotal))
			s.Add(k.memPct, 100*float64(g.MemUsed)/float64(g.MemTotal))
		}
	}
	s.SetTable(collect.TableGPUs, gpus)
	s.SetTable(collect.TableGPUProcs, procs)
	return nil
}
