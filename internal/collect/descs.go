package collect

import "github.com/hightemp/smpmon/internal/metrics"

// Metric key layout shared by all platforms. Per-device segments (core index,
// mount point, interface, pid) are escaped with metrics.NewKey.
//
//	cpu.total.{usage,user,system,iowait,steal}   percent
//	cpu.core.<n>.usage                           percent
//	cpu.core.<n>.freq, cpu.freq                  Hz
//	load.{1,5,15}                                count
//	system.uptime                                seconds
//	mem.{total,used,available,free,cached,buffers,zfs_arc}  bytes
//	mem.used_percent, swap.used_percent          percent
//	swap.{total,used,free}                       bytes
//	disk.fs.<mount>.{total,used,free}            bytes
//	disk.fs.<mount>.used_percent                 percent
//	disk.io.<dev>.{read_bps,write_bps}           bytes/s
//	disk.io.<dev>.{read_iops,write_iops}         1/s
//	disk.io.<dev>.util                           percent
//	net.<if>.{rx_bps,tx_bps}                     bytes/s
//	net.<if>.{rx_pps,tx_pps,rx_errs,tx_errs}     1/s
//	net.<if>.{rx_total,tx_total}                 bytes
//	net.total.{rx_bps,tx_bps,rx_total,tx_total}  sums over physical NICs
//	proc.count, proc.threads, proc.running       count
//	proc.<pid>.{cpu,mem}                         percent
//	cpu.temp, sensors.<chip>.<label>.temp        °C
//	sensors.<chip>.<label>.fan                   RPM
//	gpu.<n>.{util,mem_util,mem_used_percent,fan} percent
//	gpu.<n>.{mem_used,mem_total}                 bytes
//	gpu.<n>.temp                                 °C
//	gpu.<n>.{power,power_limit}                  W
//	gpu.<n>.{clock,mem_clock}                    Hz
//	battery.{percent}, battery.power, battery.time_left  %, W, s
//	docker.<name>.{cpu,mem_percent}              percent
//	docker.<name>.{mem_used,mem_limit}           bytes
//	docker.<name>.{net_rx_bps,net_tx_bps,blk_read_bps,blk_write_bps}  bytes/s
func init() {
	metrics.DefaultRegistry.Register(
		metrics.Desc{Pattern: "cpu.**.freq", Unit: metrics.UnitHertz, Help: "CPU frequency"},
		metrics.Desc{Pattern: "cpu.freq", Unit: metrics.UnitHertz, Help: "Average CPU frequency"},
		metrics.Desc{Pattern: "cpu.temp", Unit: metrics.UnitCelsius, Max: 100, Help: "CPU temperature"},
		metrics.Desc{Pattern: "cpu.**", Unit: metrics.UnitPercent, Max: 100, Help: "CPU usage"},
		metrics.Desc{Pattern: "load.*", Unit: metrics.UnitNone, Help: "Load average"},
		metrics.Desc{Pattern: "system.uptime", Unit: metrics.UnitSeconds, Help: "Uptime"},
		metrics.Desc{Pattern: "mem.used_percent", Unit: metrics.UnitPercent, Max: 100, Help: "Memory used"},
		metrics.Desc{Pattern: "mem.*", Unit: metrics.UnitBytes, Help: "Memory"},
		metrics.Desc{Pattern: "swap.used_percent", Unit: metrics.UnitPercent, Max: 100, Help: "Swap used"},
		metrics.Desc{Pattern: "swap.*", Unit: metrics.UnitBytes, Help: "Swap"},
		metrics.Desc{Pattern: "disk.fs.*.used_percent", Unit: metrics.UnitPercent, Max: 100, Help: "Filesystem used"},
		metrics.Desc{Pattern: "disk.fs.**", Unit: metrics.UnitBytes, Help: "Filesystem size"},
		metrics.Desc{Pattern: "disk.io.*.util", Unit: metrics.UnitPercent, Max: 100, Help: "Disk busy time"},
		metrics.Desc{Pattern: "disk.io.*.*_bps", Unit: metrics.UnitBytesPerSec, Help: "Disk throughput"},
		metrics.Desc{Pattern: "disk.io.*.*_iops", Unit: metrics.UnitPerSec, Help: "Disk IOPS"},
		metrics.Desc{Pattern: "net.*.*_bps", Unit: metrics.UnitBytesPerSec, Help: "Network throughput"},
		metrics.Desc{Pattern: "net.*.*_total", Unit: metrics.UnitBytes, Help: "Network bytes since boot"},
		metrics.Desc{Pattern: "net.*.*", Unit: metrics.UnitPerSec, Help: "Network packets/errors"},
		metrics.Desc{Pattern: "proc.*.cpu", Unit: metrics.UnitPercent, Help: "Process CPU"},
		metrics.Desc{Pattern: "proc.*.mem", Unit: metrics.UnitPercent, Max: 100, Help: "Process memory"},
		metrics.Desc{Pattern: "proc.*", Unit: metrics.UnitCount, Help: "Process counts"},
		metrics.Desc{Pattern: "sensors.**.temp", Unit: metrics.UnitCelsius, Max: 100, Help: "Temperature"},
		metrics.Desc{Pattern: "sensors.**.fan", Unit: metrics.UnitRPM, Help: "Fan speed"},
		metrics.Desc{Pattern: "gpu.*.temp", Unit: metrics.UnitCelsius, Max: 100, Help: "GPU temperature"},
		metrics.Desc{Pattern: "gpu.*.mem_used", Unit: metrics.UnitBytes, Help: "GPU memory used"},
		metrics.Desc{Pattern: "gpu.*.mem_total", Unit: metrics.UnitBytes, Help: "GPU memory"},
		metrics.Desc{Pattern: "gpu.*.power*", Unit: metrics.UnitWatts, Help: "GPU power"},
		metrics.Desc{Pattern: "gpu.*.*clock", Unit: metrics.UnitHertz, Help: "GPU clock"},
		metrics.Desc{Pattern: "gpu.*.*", Unit: metrics.UnitPercent, Max: 100, Help: "GPU utilization"},
		metrics.Desc{Pattern: "battery.percent", Unit: metrics.UnitPercent, Max: 100, Help: "Battery charge"},
		metrics.Desc{Pattern: "battery.power", Unit: metrics.UnitWatts, Help: "Battery power"},
		metrics.Desc{Pattern: "battery.time_left", Unit: metrics.UnitSeconds, Help: "Battery time left"},
		metrics.Desc{Pattern: "docker.*.mem_used", Unit: metrics.UnitBytes, Help: "Container memory"},
		metrics.Desc{Pattern: "docker.*.mem_limit", Unit: metrics.UnitBytes, Help: "Container memory limit"},
		metrics.Desc{Pattern: "docker.*.*_bps", Unit: metrics.UnitBytesPerSec, Help: "Container I/O"},
		metrics.Desc{Pattern: "docker.*.mem_percent", Unit: metrics.UnitPercent, Max: 100, Help: "Container memory"},
		metrics.Desc{Pattern: "docker.*.cpu", Unit: metrics.UnitPercent, Help: "Container CPU (100 = one core)"},
	)
}
