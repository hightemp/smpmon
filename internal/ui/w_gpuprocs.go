package ui

import (
	"cmp"
	"slices"
	"strconv"
	"strings"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/metrics"
)

// gpuProcInfo is a process's use of all GPUs.
type gpuProcInfo struct {
	gpus           []int
	typ            string
	util, enc, dec float64 // negative if unknown
	mem            uint64
}

var defaultGPUProcColumns = []string{"pid", "user", "gpu", "gpu_type", "gpu_util", "vram", "gpu_enc", "gpu_dec", "cpu", "rss", "cmd"}

func init() {
	pct := func(v float64) string {
		if v < 0 {
			return "-"
		}
		return strconv.FormatFloat(v, 'f', 0, 64) + "%"
	}
	gpuCol := func(key, header string, w int, value func(i *gpuProcInfo) string, less func(a, b *gpuProcInfo) int) {
		procColumns[key] = procColumn{key: key, header: header, width: w, right: true,
			value: func(p *collect.Process, pw *procWidget, _ *Context) string { return value(pw.gpuOf(p)) },
			wcmp: func(pw *procWidget, a, b *collect.Process) int {
				return less(pw.gpuOf(a), pw.gpuOf(b))
			}}
	}
	gpuCol("gpu", "GPU", 3, func(i *gpuProcInfo) string {
		s := make([]string, len(i.gpus))
		for k, g := range i.gpus {
			s[k] = strconv.Itoa(g)
		}
		return strings.Join(s, ",")
	}, func(a, b *gpuProcInfo) int { return slices.Compare(a.gpus, b.gpus) })
	gpuCol("gpu_type", "Type", 4, func(i *gpuProcInfo) string { return i.typ },
		func(a, b *gpuProcInfo) int { return cmp.Compare(a.typ, b.typ) })
	gpuCol("gpu_util", "GPU%", 5, func(i *gpuProcInfo) string { return pct(i.util) },
		func(a, b *gpuProcInfo) int { return cmp.Compare(a.util, b.util) })
	gpuCol("vram", "VRAM", 9, func(i *gpuProcInfo) string {
		if i.mem == 0 && len(i.gpus) == 0 {
			return ""
		}
		return metrics.FormatBytes(float64(i.mem))
	}, func(a, b *gpuProcInfo) int { return cmp.Compare(a.mem, b.mem) })
	gpuCol("gpu_enc", "Enc", 4, func(i *gpuProcInfo) string { return pct(i.enc) },
		func(a, b *gpuProcInfo) int { return cmp.Compare(a.enc, b.enc) })
	gpuCol("gpu_dec", "Dec", 4, func(i *gpuProcInfo) string { return pct(i.dec) },
		func(a, b *gpuProcInfo) int { return cmp.Compare(a.dec, b.dec) })
}

var noGPUInfo = &gpuProcInfo{util: -1, enc: -1, dec: -1}

// countGPUProcs counts the processes on the shown GPUs.
func countGPUProcs(ctx *Context) int {
	gps, _ := metrics.TableAs[[]collect.GPUProcess](ctx.Store, collect.TableGPUProcs)
	pids := map[int32]bool{}
	for _, gp := range gps {
		if ctx.GPU < 0 || gp.GPU == ctx.GPU {
			pids[gp.PID] = true
		}
	}
	return len(pids)
}

// gpuOf returns p's GPU usage.
func (w *procWidget) gpuOf(p *collect.Process) *gpuProcInfo {
	if i, ok := w.gpuInfo[p.PID]; ok {
		return i
	}
	return noGPUInfo
}

// sumPct adds percentages; unknown (negative) plus unknown stays unknown.
// A process on two GPUs can exceed 100%, like CPU% over several cores.
func sumPct(a, b float64) float64 {
	if a < 0 {
		return b
	}
	if b < 0 {
		return a
	}
	return a + b
}

// gpuProcesses returns the processes using a GPU, joined with the process
// table for user, command line and CPU. Processes the table doesn't have
// (another PID namespace, a history window holding only the top ones) are
// shown with what the driver reports.
func (w *procWidget) gpuProcesses(ctx *Context, procs []collect.Process) []collect.Process {
	gps, _ := metrics.TableAs[[]collect.GPUProcess](ctx.Store, collect.TableGPUProcs)
	w.gpuInfo = make(map[int32]*gpuProcInfo, len(gps))
	names := map[int32]string{}
	for _, gp := range gps {
		if ctx.GPU >= 0 && gp.GPU != ctx.GPU {
			continue
		}
		i := w.gpuInfo[gp.PID]
		if i == nil {
			i = &gpuProcInfo{util: -1, enc: -1, dec: -1}
			w.gpuInfo[gp.PID] = i
		}
		if !slices.Contains(i.gpus, gp.GPU) {
			i.gpus = append(i.gpus, gp.GPU)
			slices.Sort(i.gpus)
		}
		switch {
		case i.typ == "" || i.typ == gp.Type:
			i.typ = gp.Type
		case gp.Type != "":
			i.typ = "C+G"
		}
		i.util, i.enc, i.dec = sumPct(i.util, gp.Util), sumPct(i.enc, gp.Enc), sumPct(i.dec, gp.Dec)
		i.mem += gp.MemUsed
		if gp.Name != "" {
			names[gp.PID] = gp.Name
		}
	}
	out := make([]collect.Process, 0, len(w.gpuInfo))
	found := make(map[int32]bool, len(w.gpuInfo))
	for _, p := range procs {
		if _, ok := w.gpuInfo[p.PID]; ok {
			out = append(out, p)
			found[p.PID] = true
		}
	}
	for pid := range w.gpuInfo {
		if !found[pid] {
			out = append(out, collect.Process{PID: pid, Name: names[pid], Cmdline: names[pid]})
		}
	}
	return out
}
