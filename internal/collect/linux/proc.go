//go:build linux

package linux

import (
	"bytes"
	"context"
	"errors"
	"os"
	"os/user"
	"runtime"
	"strconv"
	"syscall"
	"time"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/metrics"
)

type procState struct {
	start    uint64 // starttime in ticks; detects pid reuse
	ticks    uint64 // utime+stime
	rb, wb   uint64 // io read/write bytes
	ioDenied bool
	name     string
	cmdline  string
	user     string
	cpuKey   metrics.Key
	memKey   metrics.Key
	seen     bool
}

// Procs collects the process list.
type Procs struct {
	fs       FS
	r        reader
	prev     map[int32]*procState
	prevAt   time.Time
	users    map[uint32]string
	boot     time.Time
	pageSize uint64
	numCPU   int

	pids    []int32
	dirBuf  []byte
	fields  [24][]byte
	pathBuf []byte
}

var (
	keyProcCount   = metrics.Key("proc.count")
	keyProcThreads = metrics.Key("proc.threads")
	keyProcRunning = metrics.Key("proc.running")
)

// NewProcs creates a Procs collector.
func NewProcs(fs FS) *Procs {
	return &Procs{
		fs:       fs,
		prev:     make(map[int32]*procState),
		users:    make(map[uint32]string),
		pageSize: uint64(os.Getpagesize()),
		numCPU:   runtime.NumCPU(),
	}
}

// Name implements collect.Collector.
func (p *Procs) Name() string { return "procs" }

// DefaultInterval implements collect.DefaultIntervaler; walking /proc is the
// most expensive collection, so it runs less often by default.
func (p *Procs) DefaultInterval() time.Duration { return 2 * time.Second }

// Init implements collect.Collector.
func (p *Procs) Init() error {
	bt, err := bootTime(p.fs)
	if err != nil {
		return err
	}
	p.boot = bt
	return nil
}

// Collect implements collect.Collector.
func (p *Procs) Collect(ctx context.Context, s *metrics.Sample) error {
	memTotal := p.memTotal()
	var err error
	p.pids, p.dirBuf, err = listPIDs(p.fs.Proc, p.dirBuf, p.pids[:0])
	if err != nil {
		return err
	}

	elapsed := s.Time.Sub(p.prevAt)
	first := p.prevAt.IsZero()
	p.prevAt = s.Time
	for _, st := range p.prev {
		st.seen = false
	}

	procs := make([]collect.Process, 0, len(p.pids))
	var threads int
	for _, pid := range p.pids {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		pr, ok := p.readProcess(pid, memTotal, elapsed, first)
		if !ok {
			continue
		}
		threads += int(pr.Threads)
		st := p.prev[pr.PID]
		s.Add(st.cpuKey, pr.CPU)
		s.Add(st.memKey, pr.Mem)
		procs = append(procs, pr)
	}
	for pid, st := range p.prev {
		if !st.seen {
			delete(p.prev, pid)
		}
	}

	s.Add(keyProcCount, float64(len(procs)))
	s.Add(keyProcThreads, float64(threads))
	if running, ok := p.procsRunning(); ok {
		s.Add(keyProcRunning, float64(running))
	}
	s.SetTable(collect.TableProcesses, procs)
	return nil
}

// readProcess reads one /proc/<pid>. It returns false if the process
// vanished while being read.
func (p *Procs) readProcess(pid int32, memTotal uint64, elapsed time.Duration, first bool) (collect.Process, bool) {
	p.pathBuf = strconv.AppendInt(append(append(p.pathBuf[:0], p.fs.Proc...), '/'), int64(pid), 10)
	base := string(p.pathBuf)
	b, err := p.r.read(base + "/stat")
	if err != nil {
		return collect.Process{}, false
	}
	open := bytes.IndexByte(b, '(')
	end := bytes.LastIndexByte(b, ')')
	if open < 0 || end < open || end+2 > len(b) {
		return collect.Process{}, false
	}
	comm := b[open+1 : end]
	f := p.fields[:]
	if fields(b[end+2:], f) < 22 {
		return collect.Process{}, false
	}
	ticks := parseUint(f[11]) + parseUint(f[12])
	start := parseUint(f[19])

	st, known := p.prev[pid]
	if !known || st.start != start {
		st = &procState{
			start:  start,
			name:   collect.Sanitize(string(comm)),
			cpuKey: metrics.NewKey("proc", strconv.Itoa(int(pid)), "cpu"),
			memKey: metrics.NewKey("proc", strconv.Itoa(int(pid)), "mem"),
		}
		st.cmdline = p.cmdline(base, st.name)
		st.user = p.user(base)
		p.prev[pid] = st
		known = false
	}
	st.seen = true

	pr := collect.Process{
		PID:       pid,
		PPID:      int32(parseInt(f[1])),
		Name:      st.name,
		Cmdline:   st.cmdline,
		User:      st.user,
		State:     f[0][0],
		Nice:      int32(parseInt(f[16])),
		Threads:   int32(parseInt(f[17])),
		VMS:       parseUint(f[20]),
		RSS:       parseUint(f[21]) * p.pageSize,
		StartTime: p.boot.Add(time.Duration(start) * time.Second / clockTicks),
		CPUTime:   time.Duration(ticks) * time.Second / clockTicks,
	}
	if memTotal > 0 {
		pr.Mem = 100 * float64(pr.RSS) / float64(memTotal)
	}
	if known && !first && elapsed > 0 {
		pr.CPU = 100 * float64(delta(ticks, st.ticks)) / clockTicks / elapsed.Seconds()
	}
	st.ticks = ticks

	if !st.ioDenied {
		if rb, wb, err := p.io(base); err == nil {
			if known && !first {
				pr.ReadBps = rate(rb, st.rb, elapsed)
				pr.WriteBps = rate(wb, st.wb, elapsed)
			}
			st.rb, st.wb = rb, wb
		} else if errors.Is(err, os.ErrPermission) {
			st.ioDenied = true
		}
	}
	return pr, true
}

func (p *Procs) cmdline(base, name string) string {
	b, err := p.r.read(base + "/cmdline")
	if err != nil || len(b) == 0 {
		return "[" + name + "]" // kernel thread or zombie
	}
	b = bytes.TrimRight(b, "\x00")
	return collect.Sanitize(string(bytes.ReplaceAll(b, []byte{0}, []byte{' '})))
}

func (p *Procs) user(base string) string {
	var st syscall.Stat_t
	if err := syscall.Stat(base, &st); err != nil {
		return ""
	}
	if name, ok := p.users[st.Uid]; ok {
		return name
	}
	uid := strconv.FormatUint(uint64(st.Uid), 10)
	name := uid
	if u, err := user.LookupId(uid); err == nil {
		name = u.Username
	}
	p.users[st.Uid] = name
	return name
}

func (p *Procs) io(base string) (rb, wb uint64, err error) {
	b, err := p.r.read(base + "/io")
	if err != nil {
		return 0, 0, err
	}
	lines(b, func(l []byte) {
		if v, ok := bytes.CutPrefix(l, []byte("read_bytes: ")); ok {
			rb = parseUint(v)
		} else if v, ok := bytes.CutPrefix(l, []byte("write_bytes: ")); ok {
			wb = parseUint(v)
		}
	})
	return rb, wb, nil
}

// procsRunning returns the number of runnable threads from /proc/stat. A
// process' own state only reflects its main thread, so counting 'R' states
// would miss most busy multi-threaded processes.
func (p *Procs) procsRunning() (uint64, bool) {
	b, err := p.r.read(p.fs.proc("stat"))
	if err != nil {
		return 0, false
	}
	var n uint64
	found := false
	lines(b, func(l []byte) {
		if v, ok := bytes.CutPrefix(l, []byte("procs_running ")); ok {
			n, found = parseUint(v), true
		}
	})
	return n, found
}

func (p *Procs) memTotal() uint64 {
	b, err := p.r.read(p.fs.proc("meminfo"))
	if err != nil {
		return 0
	}
	var total uint64
	lines(b, func(l []byte) {
		if v, ok := bytes.CutPrefix(l, []byte("MemTotal:")); ok {
			total = parseUint(bytes.TrimSpace(bytes.TrimSuffix(bytes.TrimSpace(v), []byte("kB")))) * 1024
		}
	})
	return total
}

// ProcessController sends signals and changes priorities of processes.
type ProcessController struct{}

// Signals implements collect.ProcessController.
func (ProcessController) Signals() []collect.SignalInfo {
	return []collect.SignalInfo{
		{Name: "SIGTERM", Number: int(syscall.SIGTERM)},
		{Name: "SIGKILL", Number: int(syscall.SIGKILL)},
		{Name: "SIGINT", Number: int(syscall.SIGINT)},
		{Name: "SIGHUP", Number: int(syscall.SIGHUP)},
		{Name: "SIGQUIT", Number: int(syscall.SIGQUIT)},
		{Name: "SIGSTOP", Number: int(syscall.SIGSTOP)},
		{Name: "SIGCONT", Number: int(syscall.SIGCONT)},
		{Name: "SIGUSR1", Number: int(syscall.SIGUSR1)},
		{Name: "SIGUSR2", Number: int(syscall.SIGUSR2)},
	}
}

// Signal implements collect.ProcessController.
func (ProcessController) Signal(pid int32, sig int) error {
	return syscall.Kill(int(pid), syscall.Signal(sig))
}

// SetNice implements collect.ProcessController.
func (ProcessController) SetNice(pid int32, nice int) error {
	return syscall.Setpriority(syscall.PRIO_PROCESS, int(pid), nice)
}
