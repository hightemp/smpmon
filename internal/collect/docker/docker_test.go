package docker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/metrics"
)

// fakeDaemon serves a minimal Docker API on a unix socket.
func fakeDaemon(t *testing.T, cpuNs *uint64, at *time.Time) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("unix sockets")
	}
	sock := filepath.Join(t.TempDir(), "d.sock")
	l, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc("/v1.41/version", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `{"ApiVersion":"1.45"}`)
	})
	mux.HandleFunc("/v1.41/containers/json", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = fmt.Fprint(w, `[{"Id":"aaaaaaaaaaaaaaaa","Names":["/web"],"Image":"nginx","State":"running","Status":"Up 1 hour"},
		               {"Id":"bbbbbbbbbbbbbbbb","Names":["/old"],"Image":"redis","State":"exited","Status":"Exited (0)"}]`)
	})
	mux.HandleFunc("/v1.41/containers/aaaaaaaaaaaaaaaa/stats", func(w http.ResponseWriter, r *http.Request) {
		if !strings.Contains(r.URL.RawQuery, "one-shot=true") {
			t.Errorf("query %q", r.URL.RawQuery)
		}
		_, _ = fmt.Fprintf(w, `{"read":%q,"cpu_stats":{"cpu_usage":{"total_usage":%d}},
			"memory_stats":{"usage":300,"limit":1000,"stats":{"inactive_file":100}},
			"networks":{"eth0":{"rx_bytes":%d,"tx_bytes":0}},
			"blkio_stats":{"io_service_bytes_recursive":[{"op":"read","value":10},{"op":"Write","value":20}]}}`,
			at.Format(time.RFC3339Nano), *cpuNs, *cpuNs/1000)
	})
	srv := &http.Server{Handler: mux}
	go func() { _ = srv.Serve(l) }()
	t.Cleanup(func() { _ = srv.Close() })
	return sock
}

func TestFakeDaemon(t *testing.T) {
	cpuNs := uint64(1e9)
	at := time.Unix(1000, 0)
	sock := fakeDaemon(t, &cpuNs, &at)
	t.Setenv("DOCKER_HOST", "unix://"+sock)
	c := New()
	if err := c.Init(); err != nil {
		t.Fatal(err)
	}
	s := metrics.Sample{Time: time.Now()}
	if err := c.Collect(context.Background(), &s); err != nil {
		t.Fatal(err)
	}
	cpuNs += 5e8 // half a core for one second
	at = at.Add(time.Second)
	s = metrics.Sample{Time: time.Now()}
	if err := c.Collect(context.Background(), &s); err != nil {
		t.Fatal(err)
	}
	cts := s.Tables[collect.TableContainers].([]collect.Container)
	if len(cts) != 2 || cts[0].Name != "old" || cts[1].Name != "web" {
		t.Fatalf("containers %+v", cts)
	}
	web := cts[1]
	if web.CPU != 50 || web.MemUsed != 200 || web.MemLimit != 1000 || web.NetRx != 5e5 || web.State != "running" {
		t.Fatalf("web %+v", web)
	}
	got := map[metrics.Key]float64{}
	for _, p := range s.Points {
		got[p.Key] = p.Value
	}
	if got["docker.web.mem_percent"] != 20 || got["docker.web.cpu"] != 50 {
		t.Fatalf("points %v", got)
	}
	if _, ok := got["docker.old.cpu"]; ok {
		t.Fatal("stopped container has metrics")
	}
}

func TestNoDaemon(t *testing.T) {
	t.Setenv("DOCKER_HOST", "unix://"+filepath.Join(t.TempDir(), "missing.sock"))
	if err := New().Init(); !errors.Is(err, collect.ErrUnsupported) {
		t.Fatalf("err %v", err)
	}
}

// TestLive talks to a real daemon when one is reachable.
func TestLive(t *testing.T) {
	c := New()
	if err := c.Init(); err != nil {
		t.Skipf("no docker: %v", err)
	}
	for range 2 {
		s := metrics.Sample{Time: time.Now()}
		start := time.Now()
		if err := c.Collect(context.Background(), &s); err != nil {
			t.Fatal(err)
		}
		t.Logf("%d containers in %v", len(s.Tables[collect.TableContainers].([]collect.Container)), time.Since(start))
		time.Sleep(time.Second)
	}
}
