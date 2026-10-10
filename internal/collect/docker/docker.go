// Package docker collects container metrics from the Docker Engine API.
// It speaks HTTP over the daemon socket directly instead of pulling in the
// Docker SDK.
package docker

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/metrics"
)

const (
	apiVersion  = "v1.41" // first version with one-shot stats
	parallelism = 8
)

// Collector reads containers and their resource usage.
type Collector struct {
	client *http.Client
	base   string // http://docker or the tcp URL

	prev   map[string]sample
	prevMu sync.Mutex
}

// sample keeps cumulative counters to turn them into rates.
type sample struct {
	at                     time.Time
	cpuNs                  uint64
	rx, tx, blkRead, blkWr uint64
}

// New creates the Docker collector.
func New() *Collector { return &Collector{prev: map[string]sample{}} }

// Name implements collect.Collector.
func (c *Collector) Name() string { return "docker" }

// DefaultInterval implements collect.DefaultIntervaler.
func (c *Collector) DefaultInterval() time.Duration { return 3 * time.Second }

// socketCandidates lists where Docker (Engine, Desktop, Colima, Podman)
// usually listens.
func socketCandidates() []string {
	home, _ := os.UserHomeDir()
	runtimeDir := os.Getenv("XDG_RUNTIME_DIR")
	c := []string{"/var/run/docker.sock", "/run/docker.sock"}
	if home != "" {
		c = append(c, filepath.Join(home, ".docker/run/docker.sock"), filepath.Join(home, ".colima/default/docker.sock"))
	}
	if runtimeDir != "" {
		c = append(c, filepath.Join(runtimeDir, "docker.sock"), filepath.Join(runtimeDir, "podman/podman.sock"))
	}
	return c
}

// Init implements collect.Collector.
func (c *Collector) Init() error {
	host := os.Getenv("DOCKER_HOST")
	switch {
	case strings.HasPrefix(host, "tcp://"):
		u, err := url.Parse(host)
		if err != nil {
			return err
		}
		c.base = "http://" + u.Host
		c.client = &http.Client{Timeout: 10 * time.Second}
	case strings.HasPrefix(host, "unix://"):
		c.useSocket(strings.TrimPrefix(host, "unix://"))
	case host == "":
		for _, p := range socketCandidates() {
			if _, err := os.Stat(p); err == nil {
				c.useSocket(p)
				break
			}
		}
	}
	if c.client == nil {
		return fmt.Errorf("no docker socket: %w", collect.ErrUnsupported)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	var v struct{ APIVersion string }
	if err := c.get(ctx, "/version", &v); err != nil {
		var opErr *net.OpError
		if errors.As(err, &opErr) {
			// Not running, or no permission (user not in the docker group).
			return fmt.Errorf("docker: %w: %w", err, collect.ErrUnsupported)
		}
		return err
	}
	return nil
}

func (c *Collector) useSocket(path string) {
	c.base = "http://docker"
	c.client = &http.Client{
		Timeout: 10 * time.Second,
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				var d net.Dialer
				return d.DialContext(ctx, "unix", path)
			},
			MaxIdleConnsPerHost: parallelism,
		},
	}
}

func (c *Collector) get(ctx context.Context, path string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.base+"/"+apiVersion+path, nil)
	if err != nil {
		return err
	}
	resp, err := c.client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("docker %s: %s", path, resp.Status)
	}
	return json.NewDecoder(resp.Body).Decode(v)
}

type apiContainer struct {
	ID     string `json:"Id"`
	Names  []string
	Image  string
	State  string
	Status string
}

type apiStats struct {
	Read     time.Time `json:"read"`
	CPUStats struct {
		CPUUsage struct {
			TotalUsage uint64 `json:"total_usage"`
		} `json:"cpu_usage"`
	} `json:"cpu_stats"`
	MemoryStats struct {
		Usage uint64            `json:"usage"`
		Limit uint64            `json:"limit"`
		Stats map[string]uint64 `json:"stats"`
	} `json:"memory_stats"`
	Networks map[string]struct {
		RxBytes uint64 `json:"rx_bytes"`
		TxBytes uint64 `json:"tx_bytes"`
	} `json:"networks"`
	BlkioStats struct {
		IOServiceBytesRecursive []struct {
			Op    string `json:"op"`
			Value uint64 `json:"value"`
		} `json:"io_service_bytes_recursive"`
	} `json:"blkio_stats"`
}

// memUsed subtracts the page cache like `docker stats` does.
func (s *apiStats) memUsed() uint64 {
	m := s.MemoryStats
	cache := m.Stats["inactive_file"]                // cgroup v2
	if v, ok := m.Stats["total_inactive_file"]; ok { // cgroup v1
		cache = v
	}
	if cache > m.Usage {
		return m.Usage
	}
	return m.Usage - cache
}

func rate(cur, prev uint64, d time.Duration) float64 {
	if cur < prev || d <= 0 {
		return 0
	}
	return float64(cur-prev) / d.Seconds()
}

// Collect implements collect.Collector.
func (c *Collector) Collect(ctx context.Context, s *metrics.Sample) error {
	var list []apiContainer
	if err := c.get(ctx, "/containers/json?all=true", &list); err != nil {
		return err
	}
	out := make([]collect.Container, len(list))
	var wg sync.WaitGroup
	sem := make(chan struct{}, parallelism)
	for i, ac := range list {
		name := ac.ID[:min(12, len(ac.ID))]
		if len(ac.Names) > 0 {
			name = strings.TrimPrefix(ac.Names[0], "/")
		}
		out[i] = collect.Container{ID: ac.ID[:min(12, len(ac.ID))], Name: name, Image: ac.Image, State: ac.State, Status: ac.Status}
		if ac.State != "running" {
			continue
		}
		wg.Add(1)
		go func(i int, id string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			c.fillStats(ctx, id, &out[i])
		}(i, ac.ID)
	}
	wg.Wait()

	seen := map[string]bool{}
	for _, ct := range out {
		seen[ct.ID] = true
		if ct.State != "running" {
			continue
		}
		b := metrics.NewKey("docker", ct.Name)
		s.Add(b.Child("cpu"), ct.CPU)
		s.Add(b.Child("mem_used"), float64(ct.MemUsed))
		if ct.MemLimit > 0 {
			s.Add(b.Child("mem_limit"), float64(ct.MemLimit))
			s.Add(b.Child("mem_percent"), 100*float64(ct.MemUsed)/float64(ct.MemLimit))
		}
		s.Add(b.Child("net_rx_bps"), ct.NetRx)
		s.Add(b.Child("net_tx_bps"), ct.NetTx)
		s.Add(b.Child("blk_read_bps"), ct.BlkRead)
		s.Add(b.Child("blk_write_bps"), ct.BlkWrite)
	}
	c.prevMu.Lock()
	for id := range c.prev {
		if !seen[id] {
			delete(c.prev, id)
		}
	}
	c.prevMu.Unlock()
	slices.SortFunc(out, func(a, b collect.Container) int { return strings.Compare(a.Name, b.Name) })
	s.SetTable(collect.TableContainers, out)
	return nil
}

func (c *Collector) fillStats(ctx context.Context, id string, ct *collect.Container) {
	var st apiStats
	if err := c.get(ctx, "/containers/"+id+"/stats?stream=false&one-shot=true", &st); err != nil {
		return
	}
	cur := sample{at: st.Read, cpuNs: st.CPUStats.CPUUsage.TotalUsage}
	if cur.at.IsZero() {
		cur.at = time.Now()
	}
	for _, n := range st.Networks {
		cur.rx += n.RxBytes
		cur.tx += n.TxBytes
	}
	for _, e := range st.BlkioStats.IOServiceBytesRecursive {
		switch strings.ToLower(e.Op) {
		case "read":
			cur.blkRead += e.Value
		case "write":
			cur.blkWr += e.Value
		}
	}
	ct.MemUsed, ct.MemLimit = st.memUsed(), st.MemoryStats.Limit

	c.prevMu.Lock()
	prev, ok := c.prev[ct.ID]
	c.prev[ct.ID] = cur
	c.prevMu.Unlock()
	if !ok {
		return
	}
	d := cur.at.Sub(prev.at)
	ct.CPU = rate(cur.cpuNs, prev.cpuNs, d) / 1e9 * 100
	ct.NetRx, ct.NetTx = rate(cur.rx, prev.rx, d), rate(cur.tx, prev.tx, d)
	ct.BlkRead, ct.BlkWrite = rate(cur.blkRead, prev.blkRead, d), rate(cur.blkWr, prev.blkWr, d)
}
