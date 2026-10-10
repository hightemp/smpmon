package gops

import (
	"context"
	"runtime"
	"strconv"
	"time"

	"github.com/shirou/gopsutil/v4/mem"
	"github.com/shirou/gopsutil/v4/process"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/metrics"
)

// procState caches what does not change during a process' life.
type procState struct {
	p       *process.Process
	created int64
	name    string
	cmdline string
	user    string
	ppid    int32
	cpuTime float64 // user+system seconds at the previous sample
	rb, wb  uint64
	cpuKey  metrics.Key
	memKey  metrics.Key
	seen    bool
}

// Procs collects the process list.
type Procs struct {
	prev   map[int32]*procState
	prevAt time.Time
}

// NewProcs creates a Procs collector.
func NewProcs() *Procs { return &Procs{prev: map[int32]*procState{}} }

// Name implements collect.Collector.
func (p *Procs) Name() string { return "procs" }

// DefaultInterval implements collect.DefaultIntervaler.
func (p *Procs) DefaultInterval() time.Duration { return 2 * time.Second }

// Init implements collect.Collector.
func (p *Procs) Init() error {
	_, err := process.Pids()
	return err
}

// stateLetter maps gopsutil status names to ps-style letters.
func stateLetter(st []string) byte {
	if len(st) == 0 {
		return '?'
	}
	switch st[0] {
	case process.Running:
		return 'R'
	case process.Sleep:
		return 'S'
	case process.Stop:
		return 'T'
	case process.Idle:
		return 'I'
	case process.Zombie:
		return 'Z'
	case process.Wait:
		return 'D'
	case process.Lock:
		return 'L'
	}
	return '?'
}

// Collect implements collect.Collector.
func (p *Procs) Collect(ctx context.Context, s *metrics.Sample) error {
	pids, err := process.PidsWithContext(ctx)
	if err != nil {
		return err
	}
	var memTotal uint64
	if vm, err := mem.VirtualMemoryWithContext(ctx); err == nil {
		memTotal = vm.Total
	}
	elapsed := s.Time.Sub(p.prevAt)
	first := p.prevAt.IsZero()
	p.prevAt = s.Time
	for _, st := range p.prev {
		st.seen = false
	}

	procs := make([]collect.Process, 0, len(pids))
	var threads int
	for _, pid := range pids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		pr, ok := p.read(ctx, pid, memTotal, elapsed, first)
		if !ok {
			continue
		}
		threads += int(pr.Threads)
		st := p.prev[pid]
		s.Add(st.cpuKey, pr.CPU)
		s.Add(st.memKey, pr.Mem)
		procs = append(procs, pr)
	}
	for pid, st := range p.prev {
		if !st.seen {
			delete(p.prev, pid)
		}
	}
	s.Add("proc.count", float64(len(procs)))
	s.Add("proc.threads", float64(threads))
	s.SetTable(collect.TableProcesses, procs)
	return nil
}

func (p *Procs) read(ctx context.Context, pid int32, memTotal uint64, elapsed time.Duration, first bool) (collect.Process, bool) {
	st, known := p.prev[pid]
	if !known {
		proc, err := process.NewProcessWithContext(ctx, pid)
		if err != nil {
			return collect.Process{}, false
		}
		st = &procState{p: proc}
	}
	created, err := st.p.CreateTimeWithContext(ctx)
	if err != nil && runtime.GOOS != "windows" { // pid 0/4 on Windows have no create time
		return collect.Process{}, false
	}
	if known && created != st.created { // pid reused
		proc, err := process.NewProcessWithContext(ctx, pid)
		if err != nil {
			return collect.Process{}, false
		}
		st, known = &procState{p: proc}, false
	}
	if !known {
		st.created = created
		st.name, _ = st.p.NameWithContext(ctx)
		st.cmdline, _ = st.p.CmdlineWithContext(ctx)
		if st.cmdline == "" {
			st.cmdline = "[" + st.name + "]"
		}
		st.name, st.cmdline = collect.Sanitize(st.name), collect.Sanitize(st.cmdline)
		st.user, _ = st.p.UsernameWithContext(ctx)
		st.ppid, _ = st.p.PpidWithContext(ctx)
		id := strconv.Itoa(int(pid))
		st.cpuKey = metrics.NewKey("proc", id, "cpu")
		st.memKey = metrics.NewKey("proc", id, "mem")
		p.prev[pid] = st
	}
	st.seen = true

	pr := collect.Process{
		PID: pid, PPID: st.ppid, Name: st.name, Cmdline: st.cmdline, User: st.user,
		StartTime: time.UnixMilli(st.created),
	}
	if mi, err := st.p.MemoryInfoWithContext(ctx); err == nil {
		pr.RSS, pr.VMS = mi.RSS, mi.VMS
		if memTotal > 0 {
			pr.Mem = 100 * float64(mi.RSS) / float64(memTotal)
		}
	}
	if n, err := st.p.NumThreadsWithContext(ctx); err == nil {
		pr.Threads = n
	}
	if n, err := st.p.NiceWithContext(ctx); err == nil {
		pr.Nice = n
	}
	if status, err := st.p.StatusWithContext(ctx); err == nil {
		pr.State = stateLetter(status)
	} else {
		pr.State = 'R'
	}
	if t, err := st.p.TimesWithContext(ctx); err == nil {
		total := t.User + t.System
		pr.CPUTime = time.Duration(total * float64(time.Second))
		if known && !first && elapsed > 0 {
			pr.CPU = max(100*(total-st.cpuTime)/elapsed.Seconds(), 0)
		}
		st.cpuTime = total
	}
	if io, err := st.p.IOCountersWithContext(ctx); err == nil {
		rb, wb := io.ReadBytes, io.WriteBytes
		if rb == 0 && wb == 0 {
			rb, wb = io.DiskReadBytes, io.DiskWriteBytes // macOS
		}
		if known && !first {
			pr.ReadBps = rate(rb, st.rb, elapsed)
			pr.WriteBps = rate(wb, st.wb, elapsed)
		}
		st.rb, st.wb = rb, wb
	}
	return pr, true
}
