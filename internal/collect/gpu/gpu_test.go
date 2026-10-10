package gpu

import (
	"context"
	"os/exec"
	"testing"
	"time"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/metrics"
)

func TestParseLine(t *testing.T) {
	n := &nvidia{gpus: map[int]collect.GPU{}, uuids: map[string]int{}}
	n.parseLine("0, GPU-abc, NVIDIA GeForce RTX 4070, 17, 11, 1696, 12282, 41, 17.35, 200.00, 210, 405, 0")
	n.parseLine("1, GPU-def, Tesla T4, 5, [N/A], 100, 15360, 50, [N/A], 70.00, 585, 5000, [N/A]")
	n.parseLine("garbage")
	g := n.gpus[0]
	if g.Name != "NVIDIA GeForce RTX 4070" || g.Util != 17 || g.MemUsed != 1696<<20 || g.Temp != 41 ||
		g.Power != 17.35 || g.Clock != 210e6 || g.Fan != 0 || g.Vendor != "nvidia" {
		t.Fatalf("gpu0 %+v", g)
	}
	t4 := n.gpus[1]
	if t4.MemUtil != -1 || t4.Power != -1 || t4.Fan != -1 || n.uuids["GPU-def"] != 1 {
		t.Fatalf("gpu1 %+v", t4)
	}
}

// TestLiveNVIDIA streams from a real NVIDIA GPU when one is present.
func TestLiveNVIDIA(t *testing.T) {
	if _, err := exec.LookPath("nvidia-smi"); err != nil {
		t.Skip("no nvidia-smi")
	}
	c := New()
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
	c.nv.mu.Lock()
	running := c.nv.running
	c.nv.mu.Unlock()
	if !running {
		t.Fatal("stream not running")
	}
	t.Logf("%+v", gpus[0])
}

// TestCloseStopsStream checks that no nvidia-smi is left behind.
func TestCloseStopsStream(t *testing.T) {
	if _, err := exec.LookPath("nvidia-smi"); err != nil {
		t.Skip("no nvidia-smi")
	}
	c := New()
	if err := c.Init(); err != nil {
		t.Skipf("init: %v", err)
	}
	s := metrics.Sample{Time: time.Now()}
	_ = c.Collect(context.Background(), &s) // starts the stream
	time.Sleep(300 * time.Millisecond)
	_ = c.Close()
	deadline := time.Now().Add(3 * time.Second)
	for {
		c.nv.mu.Lock()
		running := c.nv.running
		c.nv.mu.Unlock()
		if !running {
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("stream still running after Close")
		}
		time.Sleep(50 * time.Millisecond)
	}
}
