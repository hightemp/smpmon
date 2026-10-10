package history

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/metrics"
)

// Tier is a resolution level.
type Tier string

// Tiers.
const (
	Raw    Tier = "raw"
	Ten    Tier = "10s"
	Minute Tier = "1m"
	Tables Tier = "tables"
)

type tierSpec struct {
	step   time.Duration // record spacing
	period time.Duration // one file per period
	block  int           // records per frame
}

var specs = map[Tier]tierSpec{
	Raw:    {time.Second, time.Hour, 60},
	Ten:    {10 * time.Second, 24 * time.Hour, 30},
	Minute: {time.Minute, 24 * time.Hour, 10},
	Tables: {10 * time.Second, time.Hour, 6},
}

// metricTiers are queried from finest to coarsest.
var metricTiers = []Tier{Raw, Ten, Minute}

// DefaultMetrics are recorded unless the config says otherwise. Per-process
// series are excluded; top processes are kept as tables instead.
var DefaultMetrics = []string{
	"cpu.total.*", "cpu.core.*.usage", "cpu.freq", "cpu.temp", "load.*", "mem.*", "swap.*",
	"disk.fs.*.used_percent", "disk.io.*.*", "net.*.*_bps", "net.total.*", "sensors.**",
	"gpu.*.*", "battery.*", "docker.*.cpu", "docker.*.mem_used", "proc.count", "proc.threads",
}

// DefaultRetention per tier.
var DefaultRetention = map[Tier]time.Duration{
	Raw: time.Hour, Ten: 24 * time.Hour, Minute: 7 * 24 * time.Hour, Tables: 24 * time.Hour,
}

// Options configures a Recorder.
type Options struct {
	Dir       string
	Metrics   []metrics.Selector
	Processes bool // record top processes
	Retention map[Tier]time.Duration
	MaxSize   int64 // bytes for the whole Dir; 0 = unlimited
}

// topProcs is how many processes by CPU and by memory are kept.
const topProcs = 20

// recordedTables are kept with the processes.
var recordedTables = []string{
	collect.TableFilesystems, collect.TableInterfaces, collect.TableCPUInfo, collect.TableSystem, collect.TableGPUs,
}

// Recorder writes a store's metrics to disk.
type Recorder struct {
	opts  Options
	store *metrics.Store

	mu      sync.Mutex
	writers map[Tier]*tierWriter
	agg     map[Tier]*aggregator
}

// NewRecorder creates a recorder for store.
func NewRecorder(store *metrics.Store, opts Options) *Recorder {
	if opts.Retention == nil {
		opts.Retention = DefaultRetention
	}
	r := &Recorder{opts: opts, store: store, writers: map[Tier]*tierWriter{}, agg: map[Tier]*aggregator{}}
	for _, t := range []Tier{Raw, Ten, Minute, Tables} {
		r.writers[t] = &tierWriter{dir: filepath.Join(opts.Dir, string(t)), spec: specs[t]}
	}
	r.agg[Ten] = &aggregator{}
	r.agg[Minute] = &aggregator{}
	return r
}

// Run records once per second until ctx is cancelled, then flushes.
func (r *Recorder) Run(ctx context.Context) error {
	if err := os.MkdirAll(r.opts.Dir, 0o755); err != nil {
		return err
	}
	r.cleanup(time.Now())
	tick := time.NewTicker(time.Second)
	defer tick.Stop()
	defer func() {
		if err := r.Close(); err != nil {
			slog.Warn("history close", "err", err)
		}
	}()
	for {
		select {
		case <-ctx.Done():
			return nil
		case now := <-tick.C:
			if err := r.Record(now); err != nil {
				slog.Warn("history write", "err", err)
			}
			if now.Second() == 0 {
				r.cleanup(now)
			}
		}
	}
}

// snapshot reads the current value of every recorded key. Values older than
// a few seconds are skipped so stopped collectors leave gaps, not flat lines.
func (r *Recorder) snapshot(now time.Time) map[string]float64 {
	out := map[string]float64{}
	for _, sel := range r.opts.Metrics {
		for _, k := range r.store.Keys(sel) {
			t, v, ok := r.store.LastAt(k)
			if ok && now.Sub(t) < 5*time.Second && !math.IsNaN(v) {
				out[string(k)] = v
			}
		}
	}
	return out
}

// Record takes one sample of the store at now. Run calls it every second;
// tools and tests can call it directly.
func (r *Recorder) Record(now time.Time) error {
	snap := r.snapshot(now)
	r.mu.Lock()
	defer r.mu.Unlock()
	var errs []error
	if len(snap) > 0 {
		errs = append(errs, r.writers[Raw].add(now, snap))
	}
	for _, t := range []Tier{Ten, Minute} {
		a := r.agg[t]
		step := specs[t].step
		if !a.start.IsZero() && now.Truncate(step) != a.start {
			if avg := a.average(); len(avg) > 0 {
				errs = append(errs, r.writers[t].add(a.start, avg))
			}
			a.reset()
		}
		if a.start.IsZero() {
			a.start = now.Truncate(step)
		}
		a.add(snap)
	}
	if r.opts.Processes && now.Second()%10 == 0 {
		if rec, err := r.tables(now); err == nil && rec != nil {
			errs = append(errs, r.writers[Tables].addRaw(now, rec))
		}
	}
	return errors.Join(errs...)
}

// tableRecord is one entry of the tables tier.
type tableRecord struct {
	Time   int64                      `json:"t"`
	Tables map[string]json.RawMessage `json:"tables"`
}

// tables captures the top processes and a few small tables.
func (r *Recorder) tables(now time.Time) ([]byte, error) {
	rec := tableRecord{Time: now.UnixMilli(), Tables: map[string]json.RawMessage{}}
	if procs, ok := metrics.TableAs[[]collect.Process](r.store, collect.TableProcesses); ok {
		byCPU := slices.Clone(procs)
		slices.SortFunc(byCPU, func(a, b collect.Process) int { return cmp.Compare(b.CPU, a.CPU) })
		byMem := slices.Clone(procs)
		slices.SortFunc(byMem, func(a, b collect.Process) int { return cmp.Compare(b.RSS, a.RSS) })
		seen := map[int32]bool{}
		var top []collect.Process
		for _, list := range [][]collect.Process{byCPU, byMem} {
			for _, p := range list[:min(topProcs, len(list))] {
				if !seen[p.PID] {
					seen[p.PID] = true
					top = append(top, p)
				}
			}
		}
		raw, err := json.Marshal(top)
		if err != nil {
			return nil, err
		}
		rec.Tables[collect.TableProcesses] = raw
	}
	for _, name := range recordedTables {
		if v, _, ok := r.store.Table(name); ok {
			raw, err := json.Marshal(v)
			if err != nil {
				return nil, err
			}
			rec.Tables[name] = raw
		}
	}
	if len(rec.Tables) == 0 {
		return nil, nil
	}
	return json.Marshal(rec)
}

// Flush writes buffered records so readers see them.
func (r *Recorder) Flush() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var errs []error
	for _, w := range r.writers {
		errs = append(errs, w.flush())
	}
	return errors.Join(errs...)
}

// Close flushes and closes the files.
func (r *Recorder) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	var errs []error
	for _, w := range r.writers {
		errs = append(errs, w.flush(), w.close())
	}
	return errors.Join(errs...)
}

// aggregator averages records over a step.
type aggregator struct {
	start time.Time
	sum   map[string]float64
	n     map[string]int
}

func (a *aggregator) add(rec map[string]float64) {
	if a.sum == nil {
		a.sum, a.n = map[string]float64{}, map[string]int{}
	}
	for k, v := range rec {
		a.sum[k] += v
		a.n[k]++
	}
}

func (a *aggregator) average() map[string]float64 {
	out := make(map[string]float64, len(a.sum))
	for k, s := range a.sum {
		out[k] = s / float64(a.n[k])
	}
	return out
}

func (a *aggregator) reset() { *a = aggregator{} }

// tierWriter appends records of one tier to its current file.
type tierWriter struct {
	dir  string
	spec tierSpec

	file   *os.File
	period time.Time // start of the open file's period

	// pending metrics block
	keys  []string
	index map[string]int
	times []int64
	vals  [][]float64

	// pending table records (tables tier)
	raws [][]byte
}

func (w *tierWriter) open(t time.Time) error {
	period := t.UTC().Truncate(w.spec.period)
	if w.file != nil && period.Equal(w.period) {
		return nil
	}
	if err := w.flush(); err != nil {
		return err
	}
	if err := w.close(); err != nil {
		return err
	}
	if err := os.MkdirAll(w.dir, 0o755); err != nil {
		return err
	}
	path := filepath.Join(w.dir, fileName(period, w.spec.period))
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	if st, err := f.Stat(); err == nil && st.Size() == 0 {
		if _, err := f.WriteString(magic); err != nil {
			_ = f.Close()
			return err
		}
	}
	w.file, w.period = f, period
	return nil
}

func fileName(period time.Time, span time.Duration) string {
	if span >= 24*time.Hour {
		return period.Format("20060102") + ".smh"
	}
	return period.Format("20060102-15") + ".smh"
}

// parseFileName returns the start of the period a file covers.
func parseFileName(name string, span time.Duration) (time.Time, bool) {
	base := name[:max(len(name)-len(".smh"), 0)]
	layout := "20060102-15"
	if span >= 24*time.Hour {
		layout = "20060102"
	}
	t, err := time.ParseInLocation(layout, base, time.UTC)
	return t, err == nil
}

func (w *tierWriter) add(t time.Time, rec map[string]float64) error {
	if err := w.open(t); err != nil {
		return err
	}
	if w.index == nil {
		w.index = map[string]int{}
	}
	n := len(w.times)
	for k := range rec {
		if _, ok := w.index[k]; !ok {
			w.index[k] = len(w.keys)
			w.keys = append(w.keys, k)
			col := make([]float64, n)
			for i := range col {
				col[i] = math.NaN()
			}
			w.vals = append(w.vals, col)
		}
	}
	w.times = append(w.times, t.UnixMilli())
	for i, k := range w.keys {
		v, ok := rec[k]
		if !ok {
			v = math.NaN()
		}
		w.vals[i] = append(w.vals[i], v)
	}
	if len(w.times) >= w.spec.block {
		return w.flush()
	}
	return nil
}

func (w *tierWriter) addRaw(t time.Time, rec []byte) error {
	if err := w.open(t); err != nil {
		return err
	}
	w.raws = append(w.raws, rec)
	if len(w.raws) >= w.spec.block {
		return w.flush()
	}
	return nil
}

func (w *tierWriter) flush() error {
	if w.file == nil {
		return nil
	}
	if len(w.times) > 0 {
		payload, err := (&block{keys: w.keys, times: w.times, vals: w.vals}).encode()
		if err != nil {
			return err
		}
		if err := writeFrame(w.file, payload); err != nil {
			return err
		}
		w.keys, w.index, w.times, w.vals = nil, nil, nil, nil
	}
	if len(w.raws) > 0 {
		payload, err := compressJSONLines(w.raws)
		if err != nil {
			return err
		}
		if err := writeFrame(w.file, payload); err != nil {
			return err
		}
		w.raws = nil
	}
	return nil
}

func (w *tierWriter) close() error {
	if w.file == nil {
		return nil
	}
	err := w.file.Close()
	w.file = nil
	return err
}

// cleanup deletes files past their retention, then the oldest files while
// the directory is over MaxSize.
func (r *Recorder) cleanup(now time.Time) {
	type file struct {
		path  string
		start time.Time
		size  int64
	}
	var all []file
	var total int64
	for tier, spec := range specs {
		dir := filepath.Join(r.opts.Dir, string(tier))
		entries, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		keep := r.opts.Retention[tier]
		if keep == 0 {
			keep = DefaultRetention[tier]
		}
		for _, e := range entries {
			start, ok := parseFileName(e.Name(), spec.period)
			if !ok {
				continue
			}
			path := filepath.Join(dir, e.Name())
			if now.Sub(start.Add(spec.period)) > keep {
				_ = os.Remove(path)
				continue
			}
			if info, err := e.Info(); err == nil {
				all = append(all, file{path, start, info.Size()})
				total += info.Size()
			}
		}
	}
	if r.opts.MaxSize <= 0 || total <= r.opts.MaxSize {
		return
	}
	slices.SortFunc(all, func(a, b file) int { return a.start.Compare(b.start) })
	r.mu.Lock()
	open := map[string]bool{}
	for _, w := range r.writers {
		if w.file != nil {
			open[w.file.Name()] = true
		}
	}
	r.mu.Unlock()
	for _, f := range all {
		if total <= r.opts.MaxSize {
			break
		}
		if open[f.path] {
			continue
		}
		if err := os.Remove(f.path); err == nil {
			total -= f.size
		}
	}
	if total > r.opts.MaxSize {
		slog.Warn("history is over its size limit", "size", total, "limit", r.opts.MaxSize)
	}
}
