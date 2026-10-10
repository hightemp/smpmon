package collect

import (
	"strings"
	"time"
)

// Sanitize replaces control characters (newlines, tabs, escapes) with
// spaces so process names and command lines cannot break the terminal UI.
func Sanitize(s string) string {
	if !strings.ContainsFunc(s, isControl) {
		return s
	}
	return strings.Map(func(r rune) rune {
		if isControl(r) {
			return ' '
		}
		return r
	}, s)
}

func isControl(r rune) bool { return r < 0x20 || r == 0x7f || (r >= 0x80 && r < 0xa0) }

// Table names used in metrics.Sample.Tables. Values stored under these names
// are treated as immutable once written.
const (
	TableCPUInfo     = "cpu.info"    // CPUInfo
	TableProcesses   = "procs"       // []Process
	TableFilesystems = "disk.fs"     // []Filesystem
	TableDisks       = "disk.io"     // []DiskIO
	TableInterfaces  = "net.ifaces"  // []NetInterface
	TableSystem      = "system.info" // SystemInfo
)

// CPUInfo describes the processor.
type CPUInfo struct {
	Model   string
	Cores   int // physical cores (0 if unknown)
	Threads int // logical CPUs
}

// SystemInfo describes the host.
type SystemInfo struct {
	Hostname string
	OS       string
	Kernel   string
	BootTime time.Time
}

// Process is a snapshot of one process.
type Process struct {
	PID     int32
	PPID    int32
	Name    string
	Cmdline string
	User    string
	State   byte // R, S, D, Z, T, ...
	Threads int32
	Nice    int32
	// CPU is the share of one core in percent (100 = one core fully busy),
	// like top/htop. Divide by the number of CPUs for a whole-machine share.
	CPU       float64
	Mem       float64 // percent of total memory
	RSS       uint64  // bytes
	VMS       uint64  // bytes
	ReadBps   float64 // bytes/s, 0 if unavailable
	WriteBps  float64
	StartTime time.Time
	CPUTime   time.Duration // total user+system time
}

// Filesystem is a mounted filesystem.
type Filesystem struct {
	Mount             string
	Device            string // mount source, e.g. /dev/nvme0n1p2
	FSType            string
	IODevice          string // block device name in disk.io, e.g. nvme0n1p2 or dm-0
	Total             uint64
	Used              uint64
	Free              uint64 // available to unprivileged users
	UsedPercent       float64
	InodesUsedPercent float64
}

// DiskIO holds I/O rates of a block device.
type DiskIO struct {
	Name      string
	ReadBps   float64
	WriteBps  float64
	ReadIOPS  float64
	WriteIOPS float64
	Util      float64 // percent of time busy
}

// NetInterface is a network interface snapshot.
type NetInterface struct {
	Name      string
	Up        bool
	Default   bool // carries the default route
	Loopback  bool
	Addrs     []string
	SpeedMbps int // link speed, 0 if unknown
	RxBps     float64
	TxBps     float64
	RxTotal   uint64 // bytes since boot
	TxTotal   uint64
}
