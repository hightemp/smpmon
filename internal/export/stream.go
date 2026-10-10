package export

import (
	"context"
	"encoding/json"
	"io"
	"math"
	"slices"
	"time"

	"github.com/hightemp/smpmon/internal/metrics"
)

// Record is one line of the JSON stream.
type Record struct {
	Time    time.Time          `json:"time"`
	Host    string             `json:"host,omitempty"`
	Metrics map[string]float64 `json:"metrics"`
}

// Snapshot returns the current values of keys matching sels (all if empty).
func Snapshot(st *metrics.Store, sels []metrics.Selector) map[string]float64 {
	out := map[string]float64{}
	for _, k := range st.Keys(metrics.MustSelector("**")) {
		if len(sels) > 0 && !slices.ContainsFunc(sels, func(s metrics.Selector) bool { return s.Match(k) }) {
			continue
		}
		if v, ok := st.Last(k); ok && !math.IsNaN(v) && !math.IsInf(v, 0) {
			out[string(k)] = v
		}
	}
	return out
}

// Stream writes one JSON line per host every interval until ctx ends.
func Stream(ctx context.Context, w io.Writer, hosts []Host, sels []metrics.Selector, interval time.Duration) error {
	enc := json.NewEncoder(w)
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-t.C:
			for _, h := range hosts {
				rec := Record{Time: now.UTC(), Metrics: Snapshot(h.Store, sels)}
				if len(hosts) > 1 || h.Name != "local" {
					rec.Host = h.Name
				}
				if len(rec.Metrics) == 0 {
					continue
				}
				if err := enc.Encode(rec); err != nil {
					return err
				}
			}
		}
	}
}
