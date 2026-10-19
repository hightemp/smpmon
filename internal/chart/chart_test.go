package chart

import (
	"math"
	"slices"
	"strings"
	"testing"
)

var mono = Gradient{{255, 255, 255}}

func plain(g Graph) string { return g.Render().Plain() }

func TestBrailleArea(t *testing.T) {
	g := Graph{Width: 2, Height: 1, Mode: ModeBraille, Min: 0, Max: 4,
		Series: []Series{{Values: []float64{0, 1, 2, 4}, Gradient: mono}}}
	// Columns: [0,1] -> left dots empty, right dot at bottom row
	//          [2,4] -> left two bottom dots, right full column.
	want := string([]rune{0x2800 | 0x80, 0x2800 | 0x40 | 0x04 | 0x08 | 0x10 | 0x20 | 0x80})
	if got := plain(g); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestBrailleInvert(t *testing.T) {
	g := Graph{Width: 1, Height: 1, Mode: ModeBraille, Max: 4, Invert: true,
		Series: []Series{{Values: []float64{1, 4}, Gradient: mono}}}
	// Left column: top dot; right column: all dots.
	want := string(rune(0x2800 | 0x01 | 0x08 | 0x10 | 0x20 | 0x80))
	if got := plain(g); got != want {
		t.Fatalf("got %q want %q", got, want)
	}
}

func TestBrailleLine(t *testing.T) {
	g := Graph{Width: 1, Height: 2, Mode: ModeBraille, Style: StyleLine, Max: 8,
		Series: []Series{{Values: []float64{8, 1}, Gradient: mono}}}
	// Left column: dot at the top. Right column: dots connecting from the
	// previous level (top) down to level 1 (bottom).
	got := []rune(plain(g))
	if got[0] != 0x2800|0x01|0x08|0x10|0x20|0x80 || got[2] != 0x2800|0x08|0x10|0x20|0x80 {
		t.Fatalf("got %q", string(got))
	}
}

func TestBlock(t *testing.T) {
	g := Graph{Width: 4, Height: 2, Mode: ModeBlock, Max: 16,
		Series: []Series{{Values: []float64{0, 4, 8, 16}, Gradient: mono}}}
	want := "   █\n ▄██"
	if got := plain(g); got != want {
		t.Fatalf("got\n%s\nwant\n%s", got, want)
	}
	g.Invert = true
	want = " ▀██\n   █"
	if got := plain(g); got != want {
		t.Fatalf("inverted got\n%s\nwant\n%s", got, want)
	}
}

func TestASCII(t *testing.T) {
	g := Graph{Width: 3, Height: 1, Mode: ModeASCII, Max: 3,
		Series: []Series{{Values: []float64{1, 2, 3}, Gradient: mono}}}
	if got := plain(g); got != ".:#" {
		t.Fatalf("got %q", got)
	}
}

func TestSmallValuesVisible(t *testing.T) {
	g := Graph{Width: 1, Height: 4, Mode: ModeBlock, Max: 1000,
		Series: []Series{{Values: []float64{1}, Gradient: mono}}}
	if got := plain(g); !strings.HasSuffix(got, "▁") {
		t.Fatalf("tiny value invisible: %q", got)
	}
}

func TestAutoAndLogScale(t *testing.T) {
	g := Graph{Width: 2, Height: 1, Mode: ModeBlock,
		Series: []Series{{Values: []float64{50, 100}, Gradient: mono}}}
	if lo, hi := g.Range(); lo != 0 || hi != 100 {
		t.Fatalf("range %v %v", lo, hi)
	}
	if got := plain(g); got != "▄█" {
		t.Fatalf("auto %q", got)
	}
	g.Scale = ScaleLog
	g.Series[0].Values = []float64{10, 1000}
	// log1p(10)/log1p(1000) ≈ 0.35 -> 3/8.
	if got := plain(g); got != "▃█" {
		t.Fatalf("log %q", got)
	}
	empty := Graph{Width: 2, Height: 1}
	if _, hi := empty.Range(); hi != 1 {
		t.Fatalf("empty range %v", hi)
	}
}

func TestTruncatesOldValues(t *testing.T) {
	g := Graph{Width: 2, Height: 1, Mode: ModeBlock, Max: 8,
		Series: []Series{{Values: []float64{8, 8, 8, 0, 8}, Gradient: mono}}}
	if got := plain(g); got != " █" {
		t.Fatalf("got %q", got)
	}
}

func TestGradientAndColors(t *testing.T) {
	g := Gradient{MustHex("#000000"), MustHex("#ffffff")}
	if c := g.At(0.5); c != (RGB{128, 128, 128}) {
		t.Fatalf("mid %v", c)
	}
	if c := g.At(2); c != (RGB{255, 255, 255}) {
		t.Fatalf("clamp %v", c)
	}
	if c, err := ParseHex("#abc"); err != nil || c.Hex() != "#aabbcc" {
		t.Fatalf("short hex %v %v", c, err)
	}
	if _, err := ParseHex("zzz"); err == nil {
		t.Fatal("bad hex accepted")
	}

	gr := Graph{Width: 1, Height: 2, Mode: ModeBlock, Max: 1,
		Series: []Series{{Values: []float64{1}, Gradient: g}}}
	lines := gr.Render().Lines()
	if !strings.Contains(lines[0], "38;2;191;191;191") || !strings.Contains(lines[1], "38;2;64;64;64") {
		t.Fatalf("row colors %q", lines)
	}
	for _, l := range lines {
		if !strings.HasSuffix(l, "\x1b[39m") {
			t.Fatalf("row not reset: %q", l)
		}
	}
}

func TestMeter(t *testing.T) {
	m := Meter{Width: 4, Gradient: mono, Fill: '#', Blank: '-'}
	c := NewCanvas(4, 1)
	m.Draw(c, 0, 0, 0.5)
	if got := c.Plain(); got != "##--" {
		t.Fatalf("meter %q", got)
	}
	c = NewCanvas(4, 1)
	m.Draw(c, 0, 0, 0.01)
	if got := c.Plain(); got != "#---" {
		t.Fatalf("tiny meter %q", got)
	}
}

func TestResample(t *testing.T) {
	v := []float64{1, 2, 3, 4, 5, 6, 7, 8, 9, 10}
	if got := Resample(nil, v, 5, AggMax); !slices.Equal(got, []float64{2, 4, 6, 8, 10}) {
		t.Fatalf("max %v", got)
	}
	if got := Resample(nil, v, 5, AggAvg); !slices.Equal(got, []float64{1.5, 3.5, 5.5, 7.5, 9.5}) {
		t.Fatalf("avg %v", got)
	}
	if got := Resample(nil, v, 3, AggLast); len(got) != 3 || got[2] != 10 {
		t.Fatalf("last %v", got)
	}
	if got := Resample(nil, v, 20, AggMax); len(got) != 10 {
		t.Fatalf("no-op %v", got)
	}
	nan := math.NaN()
	if got := Resample(nil, []float64{nan, 2, nan, nan}, 2, AggMax); got[0] != 2 || !math.IsNaN(got[1]) {
		t.Fatalf("NaN handling %v", got)
	}
	if got := Tail(v, 3); !slices.Equal(got, []float64{8, 9, 10}) {
		t.Fatalf("tail %v", got)
	}
}

func TestParseMode(t *testing.T) {
	for _, m := range []Mode{ModeBraille, ModeBlock, ModeASCII} {
		if got, err := ParseMode(m.String()); err != nil || got != m {
			t.Fatalf("%v: %v %v", m, got, err)
		}
	}
	if _, err := ParseMode("nope"); err == nil {
		t.Fatal("expected error")
	}
}

func benchGraph(mode Mode) Graph {
	vals := make([]float64, 400)
	for i := range vals {
		vals[i] = float64(i % 100)
	}
	return Graph{Width: 200, Height: 50, Mode: mode, Max: 100,
		Series: []Series{{Values: vals, Gradient: Gradient{MustHex("#00ff00"), MustHex("#ff0000")}}}}
}

func BenchmarkBraille200x50(b *testing.B) {
	g := benchGraph(ModeBraille)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = g.Render().String()
	}
}

func BenchmarkBlock200x50(b *testing.B) {
	g := benchGraph(ModeBlock)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		_ = g.Render().String()
	}
}

func TestColorByValue(t *testing.T) {
	grad := Gradient{{0, 0, 0}, {255, 255, 255}}
	for _, mode := range []Mode{ModeBraille, ModeBlock, ModeASCII} {
		g := Graph{Width: 2, Height: 1, Mode: mode, Max: 100, ColorByValue: true,
			Series: []Series{{Values: []float64{10, 10, 100, 100}, Gradient: grad}}}
		if mode != ModeBraille {
			g.Series[0].Values = []float64{10, 100}
		}
		c := g.Render()
		low, high := c.cells[0].fg, c.cells[1].fg
		if low != grad.At(0.1) || high != grad.At(1) {
			t.Errorf("%v: colors %v %v", mode, low, high)
		}
	}
	if lines := Mini([]float64{50, 50}, 3, 2, ModeBlock, 0, 100, grad); len(lines) != 2 {
		t.Fatalf("mini %q", lines)
	}
}
