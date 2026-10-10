//go:build darwin

package gops

import (
	"context"
	"fmt"
	"sync"
	"unsafe"

	"github.com/ebitengine/purego"
	"golang.org/x/sys/unix"
)

// On macOS gopsutil spawns ps(1) for every process status and repeats
// sysctl and proc_pidinfo calls per field, which takes seconds for a few
// hundred processes. This lister uses one sysctl for the whole table and
// two libproc calls per process instead.

const (
	procPIDTaskInfo = 4 // PROC_PIDTASKINFO
	rusageInfoV2    = 2 // RUSAGE_INFO_V2
)

// procTaskInfo mirrors struct proc_taskinfo.
type procTaskInfo struct {
	VirtualSize      uint64
	ResidentSize     uint64
	TotalUser        uint64
	TotalSystem      uint64
	ThreadsUser      uint64
	ThreadsSystem    uint64
	Policy           int32
	Faults           int32
	Pageins          int32
	CowFaults        int32
	MessagesSent     int32
	MessagesReceived int32
	SyscallsMach     int32
	SyscallsUnix     int32
	Csw              int32
	Threadnum        int32
	Numrunning       int32
	Priority         int32
}

// rusageInfo mirrors struct rusage_info_v2.
type rusageInfo struct {
	UUID                [16]byte
	UserTime            uint64
	SystemTime          uint64
	PkgIdleWkups        uint64
	InterruptWkups      uint64
	Pageins             uint64
	WiredSize           uint64
	ResidentSize        uint64
	PhysFootprint       uint64
	ProcStartAbstime    uint64
	ProcExitAbstime     uint64
	ChildUserTime       uint64
	ChildSystemTime     uint64
	ChildPkgIdleWkups   uint64
	ChildInterruptWkups uint64
	ChildPageins        uint64
	ChildElapsedAbstime uint64
	DiskioBytesRead     uint64
	DiskioBytesWritten  uint64
}

var (
	libOnce          sync.Once
	libErr           error
	procPidInfo      func(pid, flavor int32, arg uint64, buf unsafe.Pointer, size int32) int32
	procPidRusage    func(pid, flavor int32, buf unsafe.Pointer) int32
	machTimebaseInfo func(info unsafe.Pointer) int32
	machToSeconds    float64
)

func loadLibSystem() error {
	libOnce.Do(func() {
		h, err := purego.Dlopen("/usr/lib/libSystem.B.dylib", purego.RTLD_LAZY|purego.RTLD_GLOBAL)
		if err != nil {
			libErr = fmt.Errorf("dlopen libSystem: %w", err)
			return
		}
		purego.RegisterLibFunc(&procPidInfo, h, "proc_pidinfo")
		purego.RegisterLibFunc(&procPidRusage, h, "proc_pid_rusage")
		purego.RegisterLibFunc(&machTimebaseInfo, h, "mach_timebase_info")
		var tb struct{ Numer, Denom uint32 }
		machTimebaseInfo(unsafe.Pointer(&tb))
		if tb.Denom == 0 {
			tb.Numer, tb.Denom = 1, 1
		}
		machToSeconds = float64(tb.Numer) / float64(tb.Denom) / 1e9
	})
	return libErr
}

// darwinStates maps p_stat (SIDL, SRUN, SSLEEP, SSTOP, SZOMB) to letters.
var darwinStates = [...]byte{'?', 'I', 'R', 'S', 'T', 'Z'}

func listDarwin(ctx context.Context) ([]rawProc, error) {
	if err := loadLibSystem(); err != nil {
		return nil, err
	}
	kps, err := unix.SysctlKinfoProcSlice("kern.proc.all")
	if err != nil {
		return nil, err
	}
	out := make([]rawProc, 0, len(kps))
	for i := range kps {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		k := &kps[i]
		r := rawProc{
			pid:     k.Proc.P_pid,
			ppid:    k.Eproc.Ppid,
			name:    unix.ByteSliceToString(k.Proc.P_comm[:]),
			uid:     k.Eproc.Ucred.Uid,
			hasUID:  true,
			nice:    int32(k.Proc.P_nice),
			created: k.Proc.P_starttime.Sec*1000 + int64(k.Proc.P_starttime.Usec)/1000,
			state:   '?',
		}
		if s := int(k.Proc.P_stat); s >= 0 && s < len(darwinStates) {
			r.state = darwinStates[s]
		}
		var ti procTaskInfo
		if procPidInfo(r.pid, procPIDTaskInfo, 0, unsafe.Pointer(&ti), int32(unsafe.Sizeof(ti))) == int32(unsafe.Sizeof(ti)) {
			r.rss, r.vms, r.threads = ti.ResidentSize, ti.VirtualSize, ti.Threadnum
			r.cpuSeconds = float64(ti.TotalUser+ti.TotalSystem) * machToSeconds
			r.hasTask = true
		}
		var ru rusageInfo
		if procPidRusage(r.pid, rusageInfoV2, unsafe.Pointer(&ru)) == 0 {
			r.readBytes, r.writeBytes, r.hasIO = ru.DiskioBytesRead, ru.DiskioBytesWritten, true
		}
		out = append(out, r)
	}
	return out, nil
}

func init() { fastList = listDarwin }
