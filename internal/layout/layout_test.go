package layout

import (
	"slices"
	"testing"

	"go.yaml.in/yaml/v3"

	"github.com/hightemp/smpmon/internal/config"
)

func fixed(n float64) config.Size   { return config.Size{Kind: config.SizeFixed, Value: n} }
func percent(n float64) config.Size { return config.Size{Kind: config.SizePercent, Value: n} }

func sum(v []int) int {
	s := 0
	for _, x := range v {
		s += x
	}
	return s
}

func TestSplit(t *testing.T) {
	cases := []struct {
		name  string
		total int
		items []Item
		want  []int
	}{
		{"equal weights", 10, []Item{{Weight: 1}, {Weight: 1}, {Weight: 1}}, []int{4, 3, 3}},
		{"weights", 12, []Item{{Weight: 1}, {Weight: 2}}, []int{4, 8}},
		{"fixed+flex", 20, []Item{{Size: fixed(5)}, {Weight: 1}}, []int{5, 15}},
		{"percent", 50, []Item{{Size: percent(30)}, {Weight: 1}}, []int{15, 35}},
		{"min pins", 10, []Item{{Weight: 1, Min: 8}, {Weight: 1}}, []int{8, 2}},
		{"max pins", 10, []Item{{Weight: 1, Max: 2}, {Weight: 1}}, []int{2, 8}},
		{"all maxed", 10, []Item{{Weight: 1, Max: 2}, {Weight: 1, Max: 3}}, []int{2, 3}},
		{"overflow shrinks fixed", 10, []Item{{Size: fixed(10)}, {Size: fixed(10)}, {Weight: 1}}, []int{5, 5, 0}},
		{"zero total", 0, []Item{{Weight: 1}}, []int{0}},
		{"min clamps percent", 100, []Item{{Size: percent(1), Min: 5}, {Weight: 1}}, []int{5, 95}},
	}
	for _, c := range cases {
		got := Split(c.total, c.items)
		if !slices.Equal(got, c.want) {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
	// Sizes always add up exactly when nothing is capped.
	for total := 1; total < 200; total++ {
		items := []Item{{Size: percent(33)}, {Weight: 1.3}, {Weight: 0.7}, {Size: fixed(3)}}
		if s := sum(Split(total, items)); s != total {
			t.Fatalf("total %d: sum %d", total, s)
		}
	}
}

func TestCompute(t *testing.T) {
	var root config.Node
	src := `
col:
  - widget: cpu
    size: 10
  - row:
      - widget: mem
      - widget: proc
        weight: 3
`
	if err := yaml.Unmarshal([]byte(src), &root); err != nil {
		t.Fatal(err)
	}
	ps := Compute(&root, Rect{X: 1, Y: 2, W: 80, H: 30})
	if len(ps) != 3 {
		t.Fatalf("placements %+v", ps)
	}
	want := map[string]Rect{
		"cpu":  {1, 2, 80, 10},
		"mem":  {1, 12, 20, 20},
		"proc": {21, 12, 60, 20},
	}
	for _, p := range ps {
		if p.Rect != want[p.Node.Widget] {
			t.Errorf("%s: %+v want %+v", p.Node.Widget, p.Rect, want[p.Node.Widget])
		}
	}
	if !slices.Equal(ps[2].Path, []int{1, 1}) {
		t.Fatalf("path %v", ps[2].Path)
	}
	if p, ok := Find(ps, 30, 20); !ok || p.Node.Widget != "proc" {
		t.Fatalf("find %+v", p)
	}
	// Too small: zero-height children are dropped.
	if ps := Compute(&root, Rect{W: 80, H: 10}); len(ps) != 1 {
		t.Fatalf("tiny %+v", ps)
	}
}

func TestDefaultDashboardsFill(t *testing.T) {
	cfg := config.Default()
	for _, name := range cfg.Dashboards.Names {
		db, _ := cfg.Dashboards.Get(name)
		for _, size := range [][2]int{{80, 24}, {120, 40}, {250, 70}} {
			l := db.LayoutFor(size[0], size[1])
			area := 0
			for _, p := range Compute(l, Rect{W: size[0], H: size[1]}) {
				area += p.Rect.W * p.Rect.H
			}
			if area != size[0]*size[1] {
				t.Errorf("%s at %v: covered %d of %d cells", name, size, area, size[0]*size[1])
			}
		}
	}
}
