package export

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	_ "github.com/hightemp/smpmon/internal/collect" // metric units and help
	"github.com/hightemp/smpmon/internal/metrics"
)

func store(points map[metrics.Key]float64) *metrics.Store {
	st := metrics.NewStore(metrics.DefaultStoreOptions())
	s := metrics.Sample{Time: time.Now()}
	for k, v := range points {
		s.Add(k, v)
	}
	st.Write(s)
	return st
}

func TestPrometheus(t *testing.T) {
	st := store(map[metrics.Key]float64{
		"cpu.total.usage":  12.5,
		"cpu.core.3.usage": 40,
		"load.5":           1.25,
		"mem.used":         1 << 30,
		"mem.used_percent": 50,
		metrics.NewKey("disk", "fs", "/home", "used_percent"): 90,
		"net.eth0.rx_bps":                    1000,
		"net.eth0.rx_total":                  5e9,
		"net.eth0.rx_pps":                    10,
		"disk.io.sda.read_iops":              3,
		"sensors.coretemp.Package id 0.temp": 55,
		"proc.123.cpu":                       9,
		"gpu.0.power":                        17,
		"weird.thing":                        1,
	})
	var buf bytes.Buffer
	if err := Write(&buf, []Host{{"local", st}}, nil); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{
		"smpmon_cpu_usage_percent 12.5",
		`smpmon_cpu_core_usage_percent{core="3"} 40`,
		`smpmon_load{period="5"} 1.25`,
		"smpmon_memory_used_bytes 1.073741824e+09",
		"smpmon_memory_used_percent 50",
		`smpmon_filesystem_used_percent{mount="/home"} 90`,
		`smpmon_network_rx_bytes_per_second{interface="eth0"} 1000`,
		`smpmon_network_rx_bytes_total{interface="eth0"} 5e+09`,
		"# TYPE smpmon_network_rx_bytes_total counter",
		`smpmon_network_rx_packets_per_second{interface="eth0"} 10`,
		`smpmon_disk_read_operations_per_second{device="sda"} 3`,
		`smpmon_sensor_temperature_celsius{chip="coretemp",sensor="Package id 0"} 55`,
		`smpmon_gpu_power_watts{gpu="0"} 17`,
		"smpmon_weird_thing 1",
		"# TYPE smpmon_cpu_usage_percent gauge",
		"# HELP smpmon_cpu_usage_percent CPU usage",
	} {
		if !strings.Contains(out, want+"\n") {
			t.Errorf("missing %q", want)
		}
	}
	if strings.Contains(out, "proc_123") || strings.Contains(out, `"123"`) {
		t.Error("per-process series exported")
	}

	// Several hosts get a host label; selectors filter.
	buf.Reset()
	other := store(map[metrics.Key]float64{"cpu.total.usage": 99})
	_ = Write(&buf, []Host{{"local", st}, {"web1", other}}, []metrics.Selector{metrics.MustSelector("cpu.total.*")})
	out = buf.String()
	if !strings.Contains(out, `smpmon_cpu_usage_percent{host="web1"} 99`) || !strings.Contains(out, `{host="local"} 12.5`) {
		t.Fatalf("hosts:\n%s", out)
	}
	if strings.Contains(out, "memory") {
		t.Fatal("selector not applied")
	}
}

func TestHandler(t *testing.T) {
	st := store(map[metrics.Key]float64{"cpu.total.usage": 1})
	srv := httptest.NewServer(Handler(func() []Host { return []Host{{"local", st}} }, nil))
	defer srv.Close()
	resp, err := srv.Client().Get(srv.URL + "/metrics")
	if err != nil {
		t.Fatal(err)
	}
	body, _ := io.ReadAll(resp.Body)
	_ = resp.Body.Close()
	if !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/plain") || !strings.Contains(string(body), "smpmon_cpu_usage_percent 1") {
		t.Fatalf("%s %s", resp.Header.Get("Content-Type"), body)
	}
	resp, _ = srv.Client().Get(srv.URL + "/nope")
	if resp.StatusCode != 404 {
		t.Fatalf("status %d", resp.StatusCode)
	}
	_ = resp.Body.Close()
}

func TestStream(t *testing.T) {
	st := store(map[metrics.Key]float64{"cpu.total.usage": 7, "mem.used": 3})
	var buf bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 350*time.Millisecond)
	defer cancel()
	if err := Stream(ctx, &buf, []Host{{"local", st}}, []metrics.Selector{metrics.MustSelector("cpu.**")}, 100*time.Millisecond); err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	if len(lines) < 2 {
		t.Fatalf("lines %q", lines)
	}
	var rec Record
	if err := json.Unmarshal([]byte(lines[0]), &rec); err != nil {
		t.Fatal(err)
	}
	if rec.Metrics["cpu.total.usage"] != 7 || len(rec.Metrics) != 1 || rec.Host != "" {
		t.Fatalf("record %+v", rec)
	}
}
