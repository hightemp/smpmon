package gpu

import (
	"context"
	"os/exec"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/metrics"
)

func testNVIDIA() *nvidia {
	n := &nvidia{gpus: map[int]collect.GPU{}, uuids: map[string]int{}, links: map[int][2]float64{}}
	fields := slices.Clone(baseFields)
	for _, alts := range optionalFields {
		fields = append(fields, alts[0])
	}
	n.setFields(fields)
	return n
}

func TestParseLine(t *testing.T) {
	n := testNVIDIA()
	n.parseLine("0, GPU-abc, NVIDIA GeForce RTX 4070, 17, 11, 1696, 12282, 41, 17.35, 200.00, 210, 405, 0, 3, 0, P2, 0x0000000000000004, 4, 16")
	n.parseLine("1, GPU-def, Tesla T4, 5, [N/A], 100, 15360, 50, [N/A], 70.00, 585, 5000, [N/A], [N/A], [N/A], [N/A], [N/A], [N/A], [N/A]")
	n.parseLine("garbage")
	g := n.gpus[0]
	if g.Name != "NVIDIA GeForce RTX 4070" || g.Util != 17 || g.MemUsed != 1696<<20 || g.Temp != 41 ||
		g.Power != 17.35 || g.Clock != 210e6 || g.Fan != 0 || g.Vendor != "nvidia" ||
		g.Enc != 3 || g.Dec != 0 || g.PState != 2 || g.PCIeGen != 4 || g.PCIeWidth != 16 ||
		!slices.Equal(g.Throttle, []string{"power cap"}) || !isSlowed(g.Throttle) {
		t.Fatalf("gpu0 %+v", g)
	}
	t4 := n.gpus[1]
	if t4.MemUtil != -1 || t4.Power != -1 || t4.Fan != -1 || t4.Enc != -1 || t4.PState != -1 ||
		t4.Throttle != nil || t4.PCIeGen != 0 || n.uuids["GPU-def"] != 1 {
		t.Fatalf("gpu1 %+v", t4)
	}
}

func TestParseLineOldDriver(t *testing.T) {
	n := &nvidia{gpus: map[int]collect.GPU{}, uuids: map[string]int{}}
	n.setFields(append(slices.Clone(baseFields), "clocks_throttle_reasons.active"))
	n.parseLine("0, GPU-abc, GTX 1080, 1, 2, 100, 8192, 40, 30, 180, 1000, 5000, 20, 0x0000000000000001")
	g := n.gpus[0]
	if g.Enc != -1 || g.PState != -1 || !slices.Equal(g.Throttle, []string{"idle"}) || isSlowed(g.Throttle) {
		t.Fatalf("%+v", g)
	}
}

func TestThrottleNames(t *testing.T) {
	names, slowed := throttleNames("0x0000000000000000")
	if names == nil || len(names) != 0 || slowed {
		t.Fatalf("none: %v %v", names, slowed)
	}
	names, slowed = throttleNames("0x0000000000000061")
	if !slices.Equal(names, []string{"idle", "sw thermal", "hw thermal"}) || !slowed {
		t.Fatalf("%v %v", names, slowed)
	}
	if _, ok := throttleNames("[N/A]"); ok {
		t.Fatal("N/A parsed")
	}
}

func TestParseDmon(t *testing.T) {
	n := testNVIDIA()
	for _, l := range []string{"# gpu  rxpci  txpci ", "# Idx   MB/s   MB/s ", "    0     12      3 ", "    1      -      - "} {
		n.parseDmon(l)
	}
	if n.links[0] != [2]float64{12 << 20, 3 << 20} || n.links[1] != [2]float64{-1, -1} {
		t.Fatalf("%v", n.links)
	}
}

const pmonOut = `# gpu         pid   type     sm    mem    enc    dec    jpg    ofa     fb   ccpm    command 
# Idx           #    C/G      %      %      %      %      %      %     MB     MB    name 
    0       9662     G      3      1      -      -      -      -    531      0    Xorg           
    0      41911     G      -      -      -      -      -      -    129      0    brave --type=gp
    1       4242   C+G     55     20     10      2      -      -   2048      0    python3
    0       9662     G      4      1      -      -      -      -    531      0    Xorg           
`

func TestParsePmon(t *testing.T) {
	n := testNVIDIA()
	for _, l := range strings.Split(pmonOut, "\n") {
		n.parsePmon(l)
	}
	// The repeated Xorg line started a new interval.
	if len(n.pmonProcs) != 3 || len(n.pmonBatch) != 1 {
		t.Fatalf("procs %+v batch %+v", n.pmonProcs, n.pmonBatch)
	}
	xorg, brave, py := n.pmonProcs[0], n.pmonProcs[1], n.pmonProcs[2]
	if xorg.PID != 9662 || xorg.Util != 3 || xorg.MemUsed != 531<<20 || xorg.Type != "G" || xorg.Name != "Xorg" {
		t.Fatalf("xorg %+v", xorg)
	}
	if brave.Util != 0 || brave.Name != "brave --type=gp" {
		t.Fatalf("brave %+v", brave)
	}
	if py.GPU != 1 || py.Type != "C+G" || py.Util != 55 || py.Enc != 10 || py.Dec != 2 {
		t.Fatalf("python %+v", py)
	}
	// Old drivers have no jpg/ofa/ccpm columns.
	n = testNVIDIA()
	for _, l := range []string{"# gpu        pid  type    sm   mem   enc   dec   command", "    0       100     C    10     5     0     0   app", "    0         -     -     -     -     -     -   -"} {
		n.parsePmon(l)
	}
	n.flushPmon()
	if len(n.pmonProcs) != 1 || n.pmonProcs[0].Util != 10 || n.pmonProcs[0].Name != "app" {
		t.Fatalf("old %+v", n.pmonProcs)
	}
}

func TestParseComputeApps(t *testing.T) {
	n := testNVIDIA()
	n.uuids["GPU-b"] = 1
	ps := n.parseComputeApps("200, GPU-b, 512, C:\\Program Files\\app.exe\n100, GPU-a, [N/A], /usr/bin/python3\n")
	if len(ps) != 2 || ps[0].PID != 100 || ps[0].Name != "python3" || ps[1].GPU != 1 || ps[1].MemUsed != 512<<20 ||
		ps[1].Name != "app.exe" || ps[1].Util != -1 {
		t.Fatalf("%+v", ps)
	}
}

// TestLiveNVIDIA streams from a real NVIDIA GPU when one is present.
func TestLiveNVIDIA(t *testing.T) {
	if _, err := exec.LookPath("nvidia-smi"); err != nil {
		t.Skip("no nvidia-smi")
	}
	cs := Collectors()
	c, procs := cs[0].(*Collector), cs[2].(*ProcCollector)
	if err := c.Init(); err != nil {
		t.Skipf("init: %v", err)
	}
	defer func() { _ = c.Close() }()
	var s metrics.Sample
	for range 3 {
		s = metrics.Sample{Time: time.Now()}
		if err := c.Collect(context.Background(), &s); err != nil {
			t.Fatal(err)
		}
		time.Sleep(1200 * time.Millisecond)
	}
	gpus := s.Tables[collect.TableGPUs].([]collect.GPU)
	if len(gpus) == 0 || gpus[0].MemTotal == 0 || gpus[0].Name == "" {
		t.Fatalf("gpus %+v", gpus)
	}
	if !c.dev.nv.query.isRunning() {
		t.Fatal("stream not running")
	}
	t.Logf("%+v", gpus[0])
	if err := procs.Init(); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		s = metrics.Sample{Time: time.Now()}
		if err := procs.Collect(context.Background(), &s); err != nil {
			t.Fatal(err)
		}
		time.Sleep(2500 * time.Millisecond)
	}
	t.Logf("procs %+v", s.Tables[collect.TableGPUProcs])
}

// TestCloseStopsStream checks that no nvidia-smi is left behind.
func TestCloseStopsStream(t *testing.T) {
	if _, err := exec.LookPath("nvidia-smi"); err != nil {
		t.Skip("no nvidia-smi")
	}
	c := Collectors()[0].(*Collector)
	if err := c.Init(); err != nil {
		t.Skipf("init: %v", err)
	}
	s := metrics.Sample{Time: time.Now()}
	_ = c.Collect(context.Background(), &s) // starts the stream
	time.Sleep(300 * time.Millisecond)
	_ = c.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		if !c.dev.nv.query.isRunning() {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("stream still running after Close")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
