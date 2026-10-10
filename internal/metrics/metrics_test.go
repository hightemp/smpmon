package metrics

import (
	"slices"
	"testing"
	"time"
)

func TestKeyEscaping(t *testing.T) {
	k := NewKey("disk", "fs", "/mnt/data.v2", "used")
	if want := `disk.fs./mnt/data\.v2.used`; string(k) != want {
		t.Fatalf("got %q want %q", k, want)
	}
	if got := k.Segments(); !slices.Equal(got, []string{"disk", "fs", "/mnt/data.v2", "used"}) {
		t.Fatalf("segments: %q", got)
	}
	if got := NewKey("a").Child("b.c", "d"); got != `a.b\.c.d` {
		t.Fatalf("child: %q", got)
	}
}

func TestCompareKeys(t *testing.T) {
	keys := []Key{"cpu.core.10", "cpu.core.2", "cpu.total", "cpu.core.1"}
	slices.SortFunc(keys, CompareKeys)
	want := []Key{"cpu.core.1", "cpu.core.2", "cpu.core.10", "cpu.total"}
	if !slices.Equal(keys, want) {
		t.Fatalf("got %v want %v", keys, want)
	}
}

func TestSelector(t *testing.T) {
	cases := []struct {
		pat  string
		key  Key
		want bool
	}{
		{"cpu.total.usage", "cpu.total.usage", true},
		{"cpu.core.*.usage", "cpu.core.3.usage", true},
		{"cpu.core.*.usage", "cpu.core.3.freq", false},
		{"cpu.core.*", "cpu.core.3.usage", false},
		{"net.**", "net.eth0.rx", true},
		{"net.**", "net", true},
		{"**.rx", "net.eth0.rx", true},
		{"**.rx", "net.eth0.tx", false},
		{"a.**.z", "a.b.c.z", true},
		{"a.**.z", "a.z", true},
		{`disk.fs./mnt/data\.v2.*`, NewKey("disk", "fs", "/mnt/data.v2", "used"), true},
		{`x.\*`, "x.y", false},
		{`x.\*`, NewKey("x", "*"), true},
		{"net.*.*_bps", "net.eth0.rx_bps", true},
		{"net.*.*_bps", "net.eth0.rx_pps", false},
		{"net.eth?.**", "net.eth1.rx", true},
		{"disk.io.sd[ab].util", "disk.io.sdb.util", true},
		{"disk.io.sd[ab].util", "disk.io.sdc.util", false},
	}
	for _, c := range cases {
		sel, err := ParseSelector(c.pat)
		if err != nil {
			t.Fatalf("%s: %v", c.pat, err)
		}
		if got := sel.Match(c.key); got != c.want {
			t.Errorf("%s ~ %s: got %v want %v", c.pat, c.key, got, c.want)
		}
	}
	for _, bad := range []string{"", "a..b", ".a", "a.[x"} {
		if _, err := ParseSelector(bad); err == nil {
			t.Errorf("expected error for %q", bad)
		}
	}
	if s := MustSelector(`disk.fs./a\.b.used`); !s.IsLiteral() || s.Key() != NewKey("disk", "fs", "/a.b", "used") {
		t.Errorf("literal key: %v", s.Key())
	}
}

func TestRing(t *testing.T) {
	r := NewRing(3)
	if _, _, ok := r.Last(); ok {
		t.Fatal("empty ring has last")
	}
	base := time.UnixMilli(1000)
	for i := range 5 {
		r.Push(base.Add(time.Duration(i)*time.Second), float64(i))
	}
	if r.Len() != 3 || r.Cap() != 3 {
		t.Fatalf("len/cap %d/%d", r.Len(), r.Cap())
	}
	if got := r.Values(nil, 0); !slices.Equal(got, []float64{2, 3, 4}) {
		t.Fatalf("values %v", got)
	}
	if got := r.Values(nil, 2); !slices.Equal(got, []float64{3, 4}) {
		t.Fatalf("values(2) %v", got)
	}
	if got := r.Since(nil, base.Add(3*time.Second)); !slices.Equal(got, []float64{3, 4}) {
		t.Fatalf("since %v", got)
	}
	if ts, v := r.At(0); v != 2 || !ts.Equal(base.Add(2*time.Second)) {
		t.Fatalf("at(0) %v %v", ts, v)
	}
	r.Reset()
	r.Push(base, 7)
	if got := r.Values(nil, 0); !slices.Equal(got, []float64{7}) {
		t.Fatalf("after reset %v", got)
	}
}

func TestRingGrowth(t *testing.T) {
	r := NewRing(1000)
	for i := range 1500 {
		r.Push(time.UnixMilli(int64(i)), float64(i))
	}
	got := r.Values(nil, 0)
	if len(got) != 1000 || got[0] != 500 || got[999] != 1499 {
		t.Fatalf("len %d first %v last %v", len(got), got[0], got[len(got)-1])
	}
}

func TestStore(t *testing.T) {
	opts := DefaultStoreOptions()
	opts.Capacity = 10
	s := NewStore(opts)
	ch, unsub := s.Subscribe()
	defer unsub()

	now := time.Unix(100, 0)
	var sm Sample
	sm.Time = now
	sm.Add("cpu.core.10.usage", 10)
	sm.Add("cpu.core.2.usage", 2)
	sm.Add("cpu.total.usage", 50)
	sm.Add("proc.42.cpu", 1)
	sm.SetTable("procs", []int{42})
	s.Write(sm)

	select {
	case <-ch:
	default:
		t.Fatal("no notification")
	}

	if v, ok := s.Last("cpu.total.usage"); !ok || v != 50 {
		t.Fatalf("last %v %v", v, ok)
	}
	sel := MustSelector("cpu.core.*.usage")
	if got := s.Keys(sel); !slices.Equal(got, []Key{"cpu.core.2.usage", "cpu.core.10.usage"}) {
		t.Fatalf("keys %v", got)
	}
	if got := s.Keys(MustSelector("cpu.total.usage")); len(got) != 1 {
		t.Fatalf("literal keys %v", got)
	}
	if procs, ok := TableAs[[]int](s, "procs"); !ok || procs[0] != 42 {
		t.Fatalf("table %v %v", procs, ok)
	}

	// New key invalidates the selector cache.
	s.Write(Sample{Time: now.Add(time.Second), Points: []Point{{"cpu.core.0.usage", 0}, {"cpu.total.usage", 60}}})
	if got := s.Keys(sel); len(got) != 3 || got[0] != "cpu.core.0.usage" {
		t.Fatalf("keys after add %v", got)
	}
	if got := s.Series("cpu.total.usage", nil, 0); !slices.Equal(got, []float64{50, 60}) {
		t.Fatalf("series %v", got)
	}

	// Per-process series use the smaller capacity rule.
	s.mu.RLock()
	pc := s.series["proc.42.cpu"].ring.Cap()
	s.mu.RUnlock()
	if pc != 120 {
		t.Fatalf("proc capacity %d", pc)
	}

	// Series not updated within StaleAfter are pruned.
	if n := s.Prune(now.Add(time.Second)); n != 3 {
		t.Fatalf("pruned %d", n)
	}
	if s.Has("proc.42.cpu") || !s.Has("cpu.total.usage") {
		t.Fatal("wrong series pruned")
	}
	if got := s.Keys(sel); len(got) != 1 {
		t.Fatalf("keys after prune %v", got)
	}
}

func TestRegistry(t *testing.T) {
	r := NewRegistry()
	r.Register(
		Desc{Pattern: "cpu.**.usage", Unit: UnitPercent, Max: 100},
		Desc{Pattern: "net.*.rx", Unit: UnitBytesPerSec},
	)
	if u := r.UnitOf("cpu.core.1.usage"); u != UnitPercent {
		t.Fatalf("unit %v", u)
	}
	if u := r.UnitOf("net.eth0.rx"); u != UnitBytesPerSec {
		t.Fatalf("unit %v", u)
	}
	if r.Lookup("mem.used") != nil {
		t.Fatal("unexpected match")
	}
}

func TestFormat(t *testing.T) {
	cases := []struct {
		v    float64
		u    Unit
		want string
	}{
		{512, UnitBytes, "512 B"},
		{1536, UnitBytes, "1.50 KiB"},
		{1.5 * (1 << 30), UnitBytes, "1.50 GiB"},
		{150 * (1 << 20), UnitBytesPerSec, "150 MiB/s"},
		{42, UnitPercent, "42.0%"},
		{3.2e9, UnitHertz, "3.20 GHz"},
		{800, UnitHertz, "800 Hz"},
		{65, UnitCelsius, "65°C"},
		{3*86400 + 4*3600 + 5*60 + 6, UnitSeconds, "3d 04:05:06"},
		{125, UnitSeconds, "02:05"},
		{12345, UnitCount, "12.3 k"},
		{1101, UnitCount, "1101"},
	}
	for _, c := range cases {
		if got := Format(c.v, c.u); got != c.want {
			t.Errorf("Format(%v, %v) = %q want %q", c.v, c.u, got, c.want)
		}
	}
}

func BenchmarkRingPush(b *testing.B) {
	r := NewRing(600)
	now := time.Now()
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		r.Push(now, float64(i))
	}
}

func BenchmarkStoreWrite(b *testing.B) {
	s := NewStore(DefaultStoreOptions())
	sm := Sample{Time: time.Now()}
	for i := range 200 {
		sm.Add(NewKey("cpu", "core", string(rune('a'+i%26)), "usage", string(rune('a'+i/26))), float64(i))
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		s.Write(sm)
	}
}
