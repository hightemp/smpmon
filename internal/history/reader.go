package history

import (
	"bytes"
	"compress/flate"
	"encoding/json"
	"io"
	"math"
	"os"
	"path/filepath"
	"slices"
	"time"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/metrics"
)

func compressJSONLines(recs [][]byte) ([]byte, error) {
	var out bytes.Buffer
	w, err := flate.NewWriter(&out, flate.DefaultCompression)
	if err != nil {
		return nil, err
	}
	for _, r := range recs {
		if _, err := w.Write(append(r, '\n')); err != nil {
			return nil, err
		}
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// Point is a recorded value.
type Point struct {
	T int64 // unix ms
	V float64
}

// files returns the tier's files overlapping [from, to], oldest first.
func files(dir string, tier Tier, from, to time.Time) []string {
	spec := specs[tier]
	entries, err := os.ReadDir(filepath.Join(dir, string(tier)))
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		start, ok := parseFileName(e.Name(), spec.period)
		if !ok || start.After(to) || start.Add(spec.period).Before(from) {
			continue
		}
		out = append(out, filepath.Join(dir, string(tier), e.Name()))
	}
	slices.Sort(out)
	return out
}

// Oldest returns the time of the oldest recorded data, if any.
func Oldest(dir string) (time.Time, bool) {
	var oldest time.Time
	for _, tier := range metricTiers {
		fs := files(dir, tier, time.Time{}, time.Now().Add(time.Hour))
		if len(fs) == 0 {
			continue
		}
		if t, ok := parseFileName(filepath.Base(fs[0]), specs[tier].period); ok && (oldest.IsZero() || t.Before(oldest)) {
			oldest = t
		}
	}
	return oldest, !oldest.IsZero()
}

// readTier returns the points of keys matching sels within [from, to].
func readTier(dir string, tier Tier, sels []metrics.Selector, from, to time.Time) map[string][]Point {
	out := map[string][]Point{}
	lo, hi := from.UnixMilli(), to.UnixMilli()
	match := map[string]bool{}
	for _, path := range files(dir, tier, from, to) {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		_ = readFrames(f, func(frame []byte) error {
			b, err := decodeBlock(frame)
			if err != nil || len(b.times) == 0 || b.times[len(b.times)-1] < lo || b.times[0] > hi {
				return nil // skip corrupt or out-of-range blocks
			}
			for ki, k := range b.keys {
				ok, seen := match[k]
				if !seen {
					for _, s := range sels {
						if s.Match(metrics.Key(k)) {
							ok = true
							break
						}
					}
					match[k] = ok
				}
				if !ok {
					continue
				}
				for i, t := range b.times {
					if t >= lo && t <= hi && !math.IsNaN(b.vals[ki][i]) {
						out[k] = append(out[k], Point{t, b.vals[ki][i]})
					}
				}
			}
			return nil
		})
		_ = f.Close()
	}
	return out
}

// Query returns recorded series for keys matching sels in [from, to],
// choosing the finest tier that still has data for the start of the window
// and isn't much finer than needed.
func Query(dir string, sels []metrics.Selector, from, to time.Time) (Tier, map[string][]Point) {
	window := to.Sub(from)
	start := 0
	switch {
	case window > 24*time.Hour:
		start = 2
	case window > time.Hour:
		start = 1
	}
	var best map[string][]Point
	bestTier := metricTiers[start]
	for _, tier := range metricTiers[start:] {
		res := readTier(dir, tier, sels, from, to)
		if len(res) == 0 {
			continue
		}
		if best == nil {
			best, bestTier = res, tier
		}
		// Prefer a coarser tier if the finer one has already expired for
		// the start of the window.
		if earliest(res) <= from.UnixMilli()+int64(window/10/time.Millisecond) {
			return tier, res
		}
	}
	return bestTier, best
}

func earliest(series map[string][]Point) int64 {
	e := int64(math.MaxInt64)
	for _, pts := range series {
		if len(pts) > 0 && pts[0].T < e {
			e = pts[0].T
		}
	}
	return e
}

// TablesAt returns the table record closest before at (within a minute).
func TablesAt(dir string, at time.Time) (map[string]any, time.Time, bool) {
	from := at.Add(-time.Minute)
	var best tableRecord
	found := false
	for _, path := range files(dir, Tables, from, at) {
		f, err := os.Open(path)
		if err != nil {
			continue
		}
		_ = readFrames(f, func(frame []byte) error {
			data, err := io.ReadAll(flate.NewReader(bytes.NewReader(frame)))
			if err != nil {
				return nil
			}
			for _, line := range bytes.Split(data, []byte("\n")) {
				var rec tableRecord
				if len(line) == 0 || json.Unmarshal(line, &rec) != nil {
					continue
				}
				if rec.Time <= at.UnixMilli() && rec.Time >= from.UnixMilli() && rec.Time >= best.Time {
					best, found = rec, true
				}
			}
			return nil
		})
		_ = f.Close()
	}
	if !found {
		return nil, time.Time{}, false
	}
	out := map[string]any{}
	for name, raw := range best.Tables {
		if v, err := collect.DecodeTable(name, raw); err == nil {
			out[name] = v
		}
	}
	return out, time.UnixMilli(best.Time), true
}

// BuildStore resamples series into n evenly spaced samples between from and
// to and returns them as a store, so widgets can show history as if it were
// live. Empty buckets become NaN (drawn as gaps). tables, if not nil, are
// attached to the last sample.
func BuildStore(series map[string][]Point, tables map[string]any, from, to time.Time, n int) *metrics.Store {
	st := metrics.NewStore(metrics.StoreOptions{Capacity: n})
	span := to.Sub(from)
	if n <= 0 || span <= 0 {
		return st
	}
	bucket := span / time.Duration(n)
	type acc struct {
		sum float64
		cnt int
	}
	cols := make(map[string][]acc, len(series))
	for k, pts := range series {
		col := make([]acc, n)
		for _, p := range pts {
			i := int(time.UnixMilli(p.T).Sub(from) / bucket)
			if i >= 0 && i < n {
				col[i].sum += p.V
				col[i].cnt++
			}
		}
		cols[k] = col
	}
	for i := 0; i < n; i++ {
		s := metrics.Sample{Time: from.Add(bucket * time.Duration(i+1))}
		for k, col := range cols {
			v := math.NaN()
			if col[i].cnt > 0 {
				v = col[i].sum / float64(col[i].cnt)
			}
			s.Add(metrics.Key(k), v)
		}
		if i == n-1 {
			for name, t := range tables {
				s.SetTable(name, t)
			}
		}
		st.Write(s)
	}
	return st
}
