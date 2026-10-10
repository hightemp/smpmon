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

// NaturalCompare compares strings treating digit runs as numbers, so
// "Core 4" sorts before "Core 12".
func NaturalCompare(a, b string) int {
	for a != "" && b != "" {
		da, db := digitPrefix(a), digitPrefix(b)
		if da > 0 && db > 0 {
			na, nb := strings.TrimLeft(a[:da], "0"), strings.TrimLeft(b[:db], "0")
			if len(na) != len(nb) {
				return len(na) - len(nb)
			}
			if c := strings.Compare(na, nb); c != 0 {
				return c
			}
			a, b = a[da:], b[db:]
			continue
		}
		if a[0] != b[0] {
			return int(a[0]) - int(b[0])
		}
		a, b = a[1:], b[1:]
	}
	return len(a) - len(b)
}

func digitPrefix(s string) int {
	i := 0
	for i < len(s) && s[i] >= '0' && s[i] <= '9' {
		i++
	}
	return i
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

// More table names.
const (
	TableSensors    = "sensors"    // []Sensor
	TableGPUs       = "gpu"        // []GPU
	TableGPUProcs   = "gpu.procs"  // []GPUProcess
	TableBatteries  = "battery"    // []Battery
	TableContainers = "containers" // []Container
)

// SensorKind distinguishes temperature from fan sensors.
type SensorKind uint8

// Sensor kinds.
const (
	SensorTemp SensorKind = iota // °C
	SensorFan                    // RPM
)

// Sensor is one hardware sensor reading.
type Sensor struct {
	Chip  string // e.g. coretemp, nvme, k10temp
	Label string // e.g. "Package id 0", "Composite"
	Kind  SensorKind
	Value float64
	High  float64 // warning threshold, 0 if unknown
	Crit  float64 // critical threshold, 0 if unknown
}

// GPU is a graphics card snapshot. Unknown values are negative.
type GPU struct {
	Index      int
	Name       string
	Vendor     string  // nvidia, amd, intel
	Util       float64 // percent
	MemUtil    float64 // percent of memory bandwidth
	MemUsed    uint64
	MemTotal   uint64
	Temp       float64 // °C
	Power      float64 // W
	PowerLimit float64 // W
	Clock      float64 // Hz
	MemClock   float64 // Hz
	Fan        float64 // percent
}

// GPUProcess is a process using a GPU.
type GPUProcess struct {
	PID     int32
	GPU     int
	MemUsed uint64
}

// Battery is a battery snapshot.
type Battery struct {
	Name     string
	Percent  float64
	Status   string        // charging, discharging, full, not charging, unknown
	Power    float64       // W, positive while charging or discharging
	TimeLeft time.Duration // to empty or to full; 0 if unknown
}

// Container is a container snapshot.
type Container struct {
	ID       string
	Name     string
	Image    string
	State    string  // running, paused, exited, ...
	Status   string  // human readable, e.g. "Up 3 hours"
	CPU      float64 // percent of one core
	MemUsed  uint64
	MemLimit uint64
	NetRx    float64 // bytes/s
	NetTx    float64
	BlkRead  float64 // bytes/s
	BlkWrite float64
}
