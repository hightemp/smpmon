package history

import (
	"bytes"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"testing"
	"time"

	"github.com/hightemp/smpmon/internal/collect"
	"github.com/hightemp/smpmon/internal/metrics"
)

func TestBlockRoundTrip(t *testing.T) {
	b := &block{
		keys:  []string{"a", "b.c"},
		times: []int64{1000, 2000, 3500},
		vals:  [][]float64{{1, 1, 1.5}, {math.NaN(), 7, -3}},
	}
	data, err := b.encode()
	if err != nil {
		t.Fatal(err)
	}
	got, err := decodeBlock(data)
	if err != nil {
		t.Fatal(err)
	}
	if got.keys[1] != "b.c" || got.times[2] != 3500 || got.vals[0][2] != 1.5 || !math.IsNaN(got.vals[1][0]) || got.vals[1][2] != -3 {
		t.Fatalf("%+v", got)
	}
	if _, err := decodeBlock([]byte("garbage")); err == nil {
		t.Fatal("garbage decoded")
	}
}

// feed writes a sample into the store and records it at the same time.
func feed(t *testing.T, st *metrics.Store, r *Recorder, at time.Time, vals map[string]float64) {
	t.Helper()
	s := metrics.Sample{Time: at}
	for k, v := range vals {
		s.Add(metrics.Key(k), v)
	}
	st.Write(s)
	if err := r.Record(at); err != nil {
		t.Fatal(err)
	}
}

func newRecorder(t *testing.T) (*Recorder, *metrics.Store, string) {
	dir := t.TempDir()
	st := metrics.NewStore(metrics.DefaultStoreOptions())
	sels := []metrics.Selector{metrics.MustSelector("cpu.**"), metrics.MustSelector("mem.used")}
	r := NewRecorder(st, Options{Dir: dir, Metrics: sels, Processes: true})
	return r, st, dir
}

func TestRecordAndQuery(t *testing.T) {
	r, st, dir := newRecorder(t)
	t0 := time.Date(2026, 10, 10, 11, 58, 0, 0, time.UTC) // crosses an hour boundary
	for i := 0; i < 300; i++ {
		at := t0.Add(time.Duration(i) * time.Second)
		vals := map[string]float64{"cpu.total.usage": float64(i % 100), "mem.used": 1000, "ignored.metric": 1}
		if i >= 100 {
			vals["cpu.core.0.usage"] = 5 // appears later
		}
		if i == 150 {
			st.Write(metrics.Sample{Time: at, Tables: map[string]any{collect.TableProcesses: []collect.Process{{PID: 1, Name: "init", CPU: 3}}}})
		}
		feed(t, st, r, at, vals)
	}
	if err := r.Close(); err != nil {
		t.Fatal(err)
	}
	if fs := files(dir, Raw, t0, t0.Add(10*time.Minute)); len(fs) != 2 {
		t.Fatalf("raw files %v", fs)
	}

	sels := []metrics.Selector{metrics.MustSelector("cpu.**"), metrics.MustSelector("mem.*"), metrics.MustSelector("ignored.*")}
	tier, series := Query(dir, sels, t0, t0.Add(5*time.Minute))
	if tier != Raw {
		t.Fatalf("tier %s", tier)
	}
	if n := len(series["cpu.total.usage"]); n != 300 {
		t.Fatalf("raw points %d", n)
	}
	if len(series["cpu.core.0.usage"]) != 200 || len(series["ignored.metric"]) != 0 {
		t.Fatalf("core %d ignored %d", len(series["cpu.core.0.usage"]), len(series["ignored.metric"]))
	}

	// The 10s tier holds averages of complete steps.
	ten := readTier(dir, Ten, sels, t0, t0.Add(5*time.Minute))
	pts := ten["cpu.total.usage"]
	if len(pts) < 28 || pts[0].V != 4.5 { // mean of 0..9
		t.Fatalf("10s points %d first %v", len(pts), pts[0])
	}
	if ten["mem.used"][0].V != 1000 {
		t.Fatal("mem average")
	}

	// A day-long window uses the 10s tier.
	if tier, _ := Query(dir, sels, t0.Add(-12*time.Hour), t0.Add(12*time.Hour)); tier != Ten {
		t.Fatalf("day window tier %s", tier)
	}

	// Tables are found near a time.
	tables, at, ok := TablesAt(dir, t0.Add(165*time.Second))
	if !ok {
		t.Fatal("no tables")
	}
	procs := tables[collect.TableProcesses].([]collect.Process)
	if len(procs) != 1 || procs[0].Name != "init" || at.After(t0.Add(165*time.Second)) {
		t.Fatalf("tables %v at %v", procs, at)
	}
	if _, _, ok := TablesAt(dir, t0.Add(-time.Hour)); ok {
		t.Fatal("tables before recording")
	}
	if old, ok := Oldest(dir); !ok || old.After(t0) {
		t.Fatalf("oldest %v", old)
	}
}

func TestTruncatedFile(t *testing.T) {
	r, st, dir := newRecorder(t)
	t0 := time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 130; i++ {
		feed(t, st, r, t0.Add(time.Duration(i)*time.Second), map[string]float64{"cpu.total.usage": 1})
	}
	_ = r.Close()
	path := files(dir, Raw, t0, t0.Add(time.Hour))[0]
	data, _ := os.ReadFile(path)
	if err := os.WriteFile(path, data[:len(data)-5], 0o644); err != nil { // crash mid-frame
		t.Fatal(err)
	}
	_, series := Query(dir, []metrics.Selector{metrics.MustSelector("cpu.**")}, t0, t0.Add(time.Hour))
	if n := len(series["cpu.total.usage"]); n != 120 { // two complete blocks of 60
		t.Fatalf("points after truncation %d", n)
	}
	// Appending after a restart continues the file.
	r2 := NewRecorder(st, Options{Dir: dir, Metrics: []metrics.Selector{metrics.MustSelector("cpu.**")}})
	feed(t, st, r2, t0.Add(200*time.Second), map[string]float64{"cpu.total.usage": 2})
	_ = r2.Close()
}

func TestRetention(t *testing.T) {
	dir := t.TempDir()
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	mk := func(tier Tier, start time.Time, size int) string {
		p := filepath.Join(dir, string(tier), fileName(start, specs[tier].period))
		_ = os.MkdirAll(filepath.Dir(p), 0o755)
		_ = os.WriteFile(p, bytes.Repeat([]byte("x"), size), 0o644)
		return p
	}
	oldRaw := mk(Raw, now.Add(-3*time.Hour), 10)
	newRaw := mk(Raw, now.Add(-time.Hour), 10)
	oldDay := mk(Ten, now.Add(-72*time.Hour), 10)
	r := NewRecorder(metrics.NewStore(metrics.DefaultStoreOptions()), Options{Dir: dir})
	r.cleanup(now)
	for p, want := range map[string]bool{oldRaw: false, newRaw: true, oldDay: false} {
		if _, err := os.Stat(p); (err == nil) != want {
			t.Errorf("%s exists=%v want %v", filepath.Base(p), err == nil, want)
		}
	}
	// Size limit removes the oldest files first.
	a := mk(Minute, now.Add(-48*time.Hour), 600)
	b := mk(Minute, now.Add(-24*time.Hour), 600)
	r.opts.MaxSize = 700
	r.cleanup(now)
	if _, err := os.Stat(a); err == nil {
		t.Error("oldest file kept over the size limit")
	}
	if _, err := os.Stat(b); err != nil {
		t.Error("newer file removed")
	}
}

func TestBuildStore(t *testing.T) {
	t0 := time.UnixMilli(0)
	series := map[string][]Point{"cpu.total.usage": {{0, 10}, {500, 20}, {3000, 30}}}
	st := BuildStore(series, map[string]any{"x": 1}, t0, t0.Add(4*time.Second), 4)
	vals := st.Series("cpu.total.usage", nil, 0)
	if len(vals) != 4 || vals[0] != 15 || !math.IsNaN(vals[1]) || vals[3] != 30 {
		t.Fatalf("vals %v", vals)
	}
	if _, _, ok := st.Table("x"); !ok {
		t.Fatal("tables not attached")
	}
}

func TestCompression(t *testing.T) {
	r, st, dir := newRecorder(t)
	r.opts.Metrics = []metrics.Selector{metrics.MustSelector("cpu.**")}
	t0 := time.Date(2026, 10, 10, 10, 0, 0, 0, time.UTC)
	for i := 0; i < 3600; i++ {
		vals := map[string]float64{}
		for c := 0; c < 100; c++ {
			vals["cpu.core."+strconv.Itoa(c)+".usage"] = float64((i/30+c)%7) * 10
		}
		feed(t, st, r, t0.Add(time.Duration(i)*time.Second), vals)
	}
	_ = r.Close()
	info, _ := os.Stat(files(dir, Raw, t0, t0.Add(time.Hour))[0])
	raw := int64(3600 * 100 * 8)
	t.Logf("raw tier: %d bytes for 360k points (%.1fx smaller than 8 bytes/point)", info.Size(), float64(raw)/float64(info.Size()))
	if info.Size() > raw/4 {
		t.Fatalf("poor compression: %d bytes", info.Size())
	}
}
