package gpu

import (
	"context"
	"fmt"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hightemp/smpmon/internal/collect"
)

// nvidia reads NVIDIA GPUs through long-running nvidia-smi processes:
// `--query-gpu -lms` for card metrics, `dmon -s t` for PCIe throughput and
// `pmon` for per-process usage. Using the CLI rather than NVML keeps the
// Linux binary static (dlopen needs a dynamic binary), and the same code
// works on Windows. Each stream runs only while its data is wanted.
type nvidia struct {
	bin    string
	fields []string       // --query-gpu fields, base ones first
	col    map[string]int // field -> column

	query, pcie, pmon *stream

	mu    sync.Mutex
	gpus  map[int]collect.GPU
	uuids map[string]int
	links map[int][2]float64 // PCIe rx, tx bytes/s

	pmonCols  map[string]int
	pmonBatch map[[2]int32]collect.GPUProcess // lines of the interval being read
	pmonLast  time.Time
	pmonProcs []collect.GPUProcess

	appsAt   time.Time // last --query-compute-apps, the fallback for pmon
	appProcs []collect.GPUProcess
}

var baseFields = []string{"index", "uuid", "name", "utilization.gpu", "utilization.memory", "memory.used",
	"memory.total", "temperature.gpu", "power.draw", "power.limit", "clocks.gr", "clocks.mem", "fan.speed"}

// optionalFields depend on the driver version; alternatives are tried in
// order (clocks_throttle_reasons was renamed in driver 535).
var optionalFields = [][]string{
	{"utilization.encoder"}, {"utilization.decoder"}, {"pstate"},
	{"clocks_event_reasons.active", "clocks_throttle_reasons.active"},
	{"pcie.link.gen.current"}, {"pcie.link.width.current"},
}

const csvFormat = "--format=csv,noheader,nounits"

func newNVIDIA(period time.Duration) (*nvidia, error) {
	bin, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return nil, err
	}
	n := &nvidia{bin: bin, gpus: map[int]collect.GPU{}, uuids: map[string]int{}, links: map[int][2]float64{}}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	run := func(fields []string) (string, error) {
		out, err := exec.CommandContext(ctx, bin, "--query-gpu="+strings.Join(fields, ","), csvFormat).Output()
		return string(out), err
	}
	// One synchronous query proves the driver works and fills initial data.
	fields := slices.Clone(baseFields)
	for _, alts := range optionalFields {
		fields = append(fields, alts[0])
	}
	out, err := run(fields)
	if err != nil {
		// An older driver rejects unknown fields: keep the ones it knows.
		if out, err = run(baseFields); err != nil {
			return nil, fmt.Errorf("nvidia-smi: %w", err)
		}
		fields = slices.Clone(baseFields)
		for _, alts := range optionalFields {
			for _, f := range alts {
				if _, err := run([]string{"index", f}); err == nil {
					fields = append(fields, f)
					break
				}
			}
		}
		if len(fields) > len(baseFields) {
			if out, err = run(fields); err != nil {
				return nil, fmt.Errorf("nvidia-smi: %w", err)
			}
		}
	}
	n.setFields(fields)
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		n.parseLine(line)
	}
	if len(n.gpus) == 0 {
		return nil, fmt.Errorf("nvidia-smi reported no GPUs")
	}
	ms := strconv.Itoa(int(period.Milliseconds()))
	n.query = newStream(bin, n.parseLine, "--query-gpu="+strings.Join(fields, ","), csvFormat, "-lms", ms)
	n.pcie = newStream(bin, n.parseDmon, "dmon", "-s", "t", "-d", "1")
	n.pmon = newStream(bin, n.parsePmon, "pmon", "-s", "um", "-d", "2")
	return n, nil
}

func (n *nvidia) setFields(fields []string) {
	n.fields = fields
	n.col = make(map[string]int, len(fields))
	for i, f := range fields {
		n.col[f] = i
	}
}

// num parses a CSV field; "[N/A]", "-" and similar become -1.
func num(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return -1
	}
	return v
}

// Clock event reasons (NVML nvmlClocksEventReason*) that mean the card is
// held back; idle and application/display clock settings are not.
var throttleReasons = []struct {
	bit  uint64
	name string
	slow bool
}{
	{0x1, "idle", false},
	{0x2, "app clocks", false},
	{0x4, "power cap", true},
	{0x8, "hw slowdown", true},
	{0x10, "sync boost", false},
	{0x20, "sw thermal", true},
	{0x40, "hw thermal", true},
	{0x80, "power brake", true},
	{0x100, "display clocks", false},
}

// throttleNames decodes a reasons bitmask such as "0x0000000000000004".
func throttleNames(s string) ([]string, bool) {
	s = strings.TrimSpace(s)
	v, err := strconv.ParseUint(strings.TrimPrefix(strings.ToLower(s), "0x"), 16, 64)
	if err != nil {
		return nil, false
	}
	out := []string{} // known to be none, unlike nil
	slowed := false
	for _, r := range throttleReasons {
		if v&r.bit != 0 {
			out = append(out, r.name)
			slowed = slowed || r.slow
		}
	}
	return out, slowed
}

func (n *nvidia) parseLine(line string) {
	f := strings.Split(line, ",")
	if len(f) < len(n.fields) {
		return
	}
	get := func(name string) string {
		if i, ok := n.col[name]; ok {
			return strings.TrimSpace(f[i])
		}
		return ""
	}
	idx, err := strconv.Atoi(get("index"))
	if err != nil {
		return
	}
	mib := func(s string) uint64 {
		return uint64(max(num(s), 0) * 1024 * 1024)
	}
	mhz := func(s string) float64 {
		v := num(s)
		if v < 0 {
			return -1
		}
		return v * 1e6
	}
	g := collect.UnknownGPU()
	g.Index, g.Name, g.Vendor = idx, get("name"), "nvidia"
	g.Util, g.MemUtil = num(get("utilization.gpu")), num(get("utilization.memory"))
	g.MemUsed, g.MemTotal = mib(get("memory.used")), mib(get("memory.total"))
	g.Temp, g.Power, g.PowerLimit = num(get("temperature.gpu")), num(get("power.draw")), num(get("power.limit"))
	g.Clock, g.MemClock, g.Fan = mhz(get("clocks.gr")), mhz(get("clocks.mem")), num(get("fan.speed"))
	g.Enc, g.Dec = num(get("utilization.encoder")), num(get("utilization.decoder"))
	if p := get("pstate"); strings.HasPrefix(p, "P") {
		if v, err := strconv.Atoi(p[1:]); err == nil {
			g.PState = v
		}
	}
	reasons := get("clocks_event_reasons.active")
	if reasons == "" {
		reasons = get("clocks_throttle_reasons.active")
	}
	g.Throttle, _ = throttleNames(reasons)
	g.PCIeGen = max(int(num(get("pcie.link.gen.current"))), 0)
	g.PCIeWidth = max(int(num(get("pcie.link.width.current"))), 0)
	n.mu.Lock()
	n.gpus[idx] = g
	n.uuids[get("uuid")] = idx
	n.mu.Unlock()
}

// isSlowed reports whether the reasons include a real slowdown.
func isSlowed(reasons []string) bool {
	for _, r := range throttleReasons {
		if r.slow && slices.Contains(reasons, r.name) {
			return true
		}
	}
	return false
}

// header splits a "# gpu pid type ..." line into column names.
func header(line string) []string {
	return strings.Fields(strings.TrimPrefix(strings.TrimSpace(line), "#"))
}

// parseDmon reads `dmon -s t` lines: "# gpu rxpci txpci", "# Idx MB/s MB/s",
// then "0 12 3" per GPU and interval.
func (n *nvidia) parseDmon(line string) {
	if strings.HasPrefix(strings.TrimSpace(line), "#") {
		return
	}
	f := strings.Fields(line)
	if len(f) < 3 {
		return
	}
	idx, err := strconv.Atoi(f[0])
	if err != nil {
		return
	}
	mb := func(s string) float64 {
		v := num(s)
		if v < 0 {
			return -1
		}
		return v * (1 << 20)
	}
	n.mu.Lock()
	n.links[idx] = [2]float64{mb(f[1]), mb(f[2])}
	n.mu.Unlock()
}

// parsePmon reads `pmon -s um` lines. Columns vary between drivers, so
// they are taken from the header. Each interval prints one line per
// process and GPU; a repeated (gpu, pid) starts the next interval.
func (n *nvidia) parsePmon(line string) {
	n.mu.Lock()
	defer n.mu.Unlock()
	trim := strings.TrimSpace(line)
	if strings.HasPrefix(trim, "#") {
		h := header(trim)
		if slices.Contains(h, "pid") && slices.Contains(h, "command") {
			n.pmonCols = map[string]int{}
			for i, c := range h {
				n.pmonCols[c] = i
			}
		}
		return
	}
	cols := n.pmonCols
	if cols == nil {
		return
	}
	f := strings.Fields(trim)
	if len(f) < len(cols)-1 { // the command may be empty
		return
	}
	at := func(name string) string {
		if i, ok := cols[name]; ok && i < len(f) {
			return f[i]
		}
		return ""
	}
	n.pmonLast = time.Now()
	gpu, err := strconv.Atoi(at("gpu"))
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(at("pid"))
	if err != nil { // "-": no processes on this GPU in this interval
		return
	}
	// "-" means the process did nothing measurable in the interval.
	pct := func(name string) float64 {
		if _, ok := cols[name]; !ok {
			return -1
		}
		return max(num(at(name)), 0)
	}
	p := collect.GPUProcess{
		PID: int32(pid), GPU: gpu, Type: at("type"),
		Util: pct("sm"), Enc: pct("enc"), Dec: pct("dec"),
		MemUsed: uint64(max(num(at("fb")), 0) * 1024 * 1024),
	}
	if i, ok := cols["command"]; ok && i < len(f) {
		p.Name = strings.Join(f[i:], " ")
	}
	key := [2]int32{int32(gpu), int32(pid)}
	if _, dup := n.pmonBatch[key]; dup || n.pmonBatch == nil {
		n.flushPmon()
	}
	n.pmonBatch[key] = p
}

// flushPmon publishes the interval being read; n.mu must be held.
func (n *nvidia) flushPmon() {
	if n.pmonBatch != nil {
		n.pmonProcs = make([]collect.GPUProcess, 0, len(n.pmonBatch))
		for _, p := range n.pmonBatch {
			n.pmonProcs = append(n.pmonProcs, p)
		}
		sortProcs(n.pmonProcs)
	}
	n.pmonBatch = map[[2]int32]collect.GPUProcess{}
}

func sortProcs(ps []collect.GPUProcess) {
	slices.SortFunc(ps, func(a, b collect.GPUProcess) int {
		if a.GPU != b.GPU {
			return a.GPU - b.GPU
		}
		return int(a.PID - b.PID)
	})
}

// snapshot returns the latest card readings, (re)starting the stream.
func (n *nvidia) snapshot(withPCIe bool) []collect.GPU {
	n.query.touch()
	if withPCIe {
		n.pcie.touch()
	}
	n.mu.Lock()
	defer n.mu.Unlock()
	out := make([]collect.GPU, 0, len(n.gpus))
	for _, g := range n.gpus {
		if l, ok := n.links[g.Index]; ok && withPCIe {
			g.PCIeRX, g.PCIeTX = l[0], l[1]
		}
		out = append(out, g)
	}
	slices.SortFunc(out, func(a, b collect.GPU) int { return a.Index - b.Index })
	return out
}

// processes returns the processes using NVIDIA GPUs.
func (n *nvidia) processes(ctx context.Context) []collect.GPUProcess {
	if n.pmon.touch() {
		n.mu.Lock()
		defer n.mu.Unlock()
		// The last interval is complete once its lines stopped coming.
		if len(n.pmonBatch) > 0 && time.Since(n.pmonLast) > 500*time.Millisecond {
			n.flushPmon()
		}
		if n.pmonProcs != nil || n.pmon.isRunning() {
			return n.pmonProcs
		}
	}
	return n.computeApps(ctx)
}

// computeApps lists compute processes where pmon is unsupported. Graphics
// processes are not reported this way.
func (n *nvidia) computeApps(ctx context.Context) []collect.GPUProcess {
	n.mu.Lock()
	due := time.Since(n.appsAt) >= 5*time.Second
	if !due {
		defer n.mu.Unlock()
		return n.appProcs
	}
	n.appsAt = time.Now()
	n.mu.Unlock()
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(qctx, n.bin, "--query-compute-apps=pid,gpu_uuid,used_memory,process_name", csvFormat).Output()
	n.mu.Lock()
	defer n.mu.Unlock()
	if err != nil {
		return n.appProcs
	}
	n.appProcs = n.parseComputeApps(string(out))
	return n.appProcs
}

// parseComputeApps parses --query-compute-apps output; n.mu must be held.
func (n *nvidia) parseComputeApps(out string) []collect.GPUProcess {
	var procs []collect.GPUProcess
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		f := strings.SplitN(line, ",", 4)
		if len(f) < 3 {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(f[0]))
		if err != nil {
			continue
		}
		p := collect.GPUProcess{
			PID: int32(pid), GPU: n.uuids[strings.TrimSpace(f[1])], Type: "C",
			MemUsed: uint64(max(num(f[2]), 0) * 1024 * 1024), Util: -1, Enc: -1, Dec: -1,
		}
		if len(f) == 4 {
			name := strings.TrimSpace(f[3])
			if i := strings.LastIndexAny(name, `/\`); i >= 0 {
				name = name[i+1:]
			}
			p.Name = name
		}
		procs = append(procs, p)
	}
	sortProcs(procs)
	return procs
}

func (n *nvidia) close() {
	n.query.close()
	n.pcie.close()
	n.pmon.close()
}
