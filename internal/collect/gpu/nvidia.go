package gpu

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"os/exec"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/hightemp/smpmon/internal/collect"
)

// nvidia reads NVIDIA GPUs through a long-running `nvidia-smi -lms` process
// that prints one CSV line per GPU every period. Using the CLI rather than
// NVML keeps the Linux binary static (dlopen needs a dynamic binary), and
// the same code works on Windows. The stream stops when nobody has asked
// for data for a while, so a hidden GPU widget costs nothing.
type nvidia struct {
	bin    string
	period time.Duration

	mu      sync.Mutex
	gpus    map[int]collect.GPU
	uuids   map[string]int
	procs   []collect.GPUProcess
	lastReq time.Time
	running bool
	cancel  context.CancelFunc
	procsAt time.Time
}

const nvidiaQuery = "index,uuid,name,utilization.gpu,utilization.memory,memory.used,memory.total," +
	"temperature.gpu,power.draw,power.limit,clocks.gr,clocks.mem,fan.speed"

// idleStop stops the stream when Collect hasn't been called for this long.
const idleStop = 10 * time.Second

func newNVIDIA(period time.Duration) (*nvidia, error) {
	bin, err := exec.LookPath("nvidia-smi")
	if err != nil {
		return nil, err
	}
	n := &nvidia{bin: bin, period: period, gpus: map[int]collect.GPU{}, uuids: map[string]int{}}
	// One synchronous query proves the driver works and fills initial data.
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(ctx, bin, "--query-gpu="+nvidiaQuery, "--format=csv,noheader,nounits").Output()
	if err != nil {
		return nil, fmt.Errorf("nvidia-smi: %w", err)
	}
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		n.parseLine(line)
	}
	if len(n.gpus) == 0 {
		return nil, fmt.Errorf("nvidia-smi reported no GPUs")
	}
	return n, nil
}

// num parses a CSV field; "[N/A]" and similar become -1.
func num(s string) float64 {
	v, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil {
		return -1
	}
	return v
}

func (n *nvidia) parseLine(line string) {
	f := strings.Split(line, ",")
	if len(f) < 13 {
		return
	}
	idx, err := strconv.Atoi(strings.TrimSpace(f[0]))
	if err != nil {
		return
	}
	mib := func(s string) uint64 {
		v := num(s)
		if v < 0 {
			return 0
		}
		return uint64(v * 1024 * 1024)
	}
	mhz := func(s string) float64 {
		v := num(s)
		if v < 0 {
			return -1
		}
		return v * 1e6
	}
	g := collect.GPU{
		Index: idx, Name: strings.TrimSpace(f[2]), Vendor: "nvidia",
		Util: num(f[3]), MemUtil: num(f[4]), MemUsed: mib(f[5]), MemTotal: mib(f[6]),
		Temp: num(f[7]), Power: num(f[8]), PowerLimit: num(f[9]),
		Clock: mhz(f[10]), MemClock: mhz(f[11]), Fan: num(f[12]),
	}
	n.mu.Lock()
	n.gpus[idx] = g
	n.uuids[strings.TrimSpace(f[1])] = idx
	n.mu.Unlock()
}

// snapshot returns the latest readings, (re)starting the stream if needed.
func (n *nvidia) snapshot() ([]collect.GPU, []collect.GPUProcess) {
	n.mu.Lock()
	defer n.mu.Unlock()
	n.lastReq = time.Now()
	if !n.running {
		n.start()
	}
	out := make([]collect.GPU, 0, len(n.gpus))
	for _, g := range n.gpus {
		out = append(out, g)
	}
	slices.SortFunc(out, func(a, b collect.GPU) int { return a.Index - b.Index })
	return out, n.procs
}

// start launches the streaming process; n.mu must be held.
func (n *nvidia) start() {
	ctx, cancel := context.WithCancel(context.Background())
	n.cancel, n.running = cancel, true
	go n.stream(ctx)
}

func (n *nvidia) stream(ctx context.Context) {
	defer func() {
		n.mu.Lock()
		n.running = false
		n.mu.Unlock()
	}()
	ms := strconv.Itoa(int(n.period.Milliseconds()))
	cmd := exec.CommandContext(ctx, n.bin, "--query-gpu="+nvidiaQuery, "--format=csv,noheader,nounits", "-lms", ms)
	dieWithParent(cmd)
	out, err := cmd.StdoutPipe()
	if err != nil {
		slog.Debug("nvidia-smi pipe", "err", err)
		return
	}
	if err := cmd.Start(); err != nil {
		slog.Debug("nvidia-smi start", "err", err)
		return
	}
	defer func() { _ = cmd.Wait() }()

	// Stop the stream once nobody asks for data.
	go func() {
		t := time.NewTicker(time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				n.mu.Lock()
				idle := time.Since(n.lastReq) > idleStop
				n.mu.Unlock()
				if idle {
					n.mu.Lock()
					n.cancel()
					n.mu.Unlock()
					return
				}
				n.maybeRefreshProcs(ctx)
			}
		}
	}()

	sc := bufio.NewScanner(out)
	for sc.Scan() {
		n.parseLine(sc.Text())
	}
}

// maybeRefreshProcs updates the GPU process list every few seconds. It is
// a separate, slower query because nvidia-smi can't stream it together
// with GPU metrics.
func (n *nvidia) maybeRefreshProcs(ctx context.Context) {
	n.mu.Lock()
	due := time.Since(n.procsAt) >= 5*time.Second
	if due {
		n.procsAt = time.Now()
	}
	n.mu.Unlock()
	if !due {
		return
	}
	qctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	out, err := exec.CommandContext(qctx, n.bin, "--query-compute-apps=pid,gpu_uuid,used_memory", "--format=csv,noheader,nounits").Output()
	if err != nil {
		return
	}
	var procs []collect.GPUProcess
	n.mu.Lock()
	defer n.mu.Unlock()
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		f := strings.Split(line, ",")
		if len(f) < 3 {
			continue
		}
		pid, err := strconv.Atoi(strings.TrimSpace(f[0]))
		if err != nil {
			continue
		}
		mem := num(f[2])
		procs = append(procs, collect.GPUProcess{
			PID: int32(pid), GPU: n.uuids[strings.TrimSpace(f[1])], MemUsed: uint64(max(mem, 0) * 1024 * 1024),
		})
	}
	n.procs = procs
}

func (n *nvidia) close() {
	n.mu.Lock()
	defer n.mu.Unlock()
	if n.cancel != nil {
		n.cancel()
	}
}
