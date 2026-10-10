// Package layout turns a config layout tree into screen rectangles.
package layout

import (
	"math"
	"slices"

	"github.com/hightemp/smpmon/internal/config"
)

// Rect is a screen rectangle in cells.
type Rect struct {
	X, Y, W, H int
}

// Contains reports whether the cell (x, y) is inside r.
func (r Rect) Contains(x, y int) bool {
	return x >= r.X && y >= r.Y && x < r.X+r.W && y < r.Y+r.H
}

// Empty reports whether r has no area.
func (r Rect) Empty() bool { return r.W <= 0 || r.H <= 0 }

// Placement is a widget leaf with its computed rectangle.
type Placement struct {
	Node *config.Node
	Rect Rect
	// Path is the list of child indexes from the root, a stable identifier
	// of the node within its layout.
	Path []int
}

// Compute lays out root inside r and returns the widget leaves that got a
// non-empty rectangle, in depth-first order.
func Compute(root *config.Node, r Rect) []Placement {
	var out []Placement
	compute(root, r, nil, &out)
	return out
}

func compute(n *config.Node, r Rect, path []int, out *[]Placement) {
	if r.Empty() {
		return
	}
	if n.Dir == config.DirNone {
		*out = append(*out, Placement{Node: n, Rect: r, Path: slices.Clone(path)})
		return
	}
	total := r.W
	if n.Dir == config.DirCol {
		total = r.H
	}
	items := make([]Item, len(n.Children))
	for i, c := range n.Children {
		items[i] = Item{Size: c.Size, Weight: c.Weight, Min: c.Min, Max: c.Max}
	}
	sizes := Split(total, items)
	pos := 0
	for i, c := range n.Children {
		cr := Rect{X: r.X + pos, Y: r.Y, W: sizes[i], H: r.H}
		if n.Dir == config.DirCol {
			cr = Rect{X: r.X, Y: r.Y + pos, W: r.W, H: sizes[i]}
		}
		pos += sizes[i]
		compute(c, cr, append(path, i), out)
	}
}

// Item describes one child for Split.
type Item struct {
	Size   config.Size
	Weight float64
	Min    int
	Max    int // 0 = unbounded
}

func (it Item) clamp(v float64) float64 {
	v = max(v, float64(it.Min))
	if it.Max > 0 {
		v = min(v, float64(it.Max))
	}
	return v
}

// Split divides total cells among items:
//
//  1. fixed and percentage sizes are reserved first (clamped to min/max);
//     if they don't fit they shrink proportionally and weighted items get
//     nothing;
//  2. the remainder is shared by weight among the other items, honoring
//     their min/max;
//  3. fractions are rounded with the largest-remainder method so the sizes
//     add up exactly (unless every item hit its max).
func Split(total int, items []Item) []int {
	n := len(items)
	out := make([]int, n)
	if n == 0 || total <= 0 {
		return out
	}
	alloc := make([]float64, n)
	reserved := 0.0
	var flex []int
	for i, it := range items {
		switch it.Size.Kind {
		case config.SizeFixed:
			alloc[i] = it.clamp(it.Size.Value)
		case config.SizePercent:
			alloc[i] = it.clamp(it.Size.Value / 100 * float64(total))
		default:
			flex = append(flex, i)
			continue
		}
		reserved += alloc[i]
	}

	if reserved > float64(total) {
		scale := float64(total) / reserved
		for i := range alloc {
			alloc[i] *= scale
		}
	} else {
		shareFlex(alloc, items, flex, float64(total)-reserved)
	}
	return round(alloc, total)
}

// shareFlex distributes rem among the flex items by weight. Items whose
// share violates min or max are pinned to that bound and the rest is
// redistributed until stable.
func shareFlex(alloc []float64, items []Item, flex []int, rem float64) {
	free := slices.Clone(flex)
	for len(free) > 0 {
		var wsum float64
		for _, i := range free {
			wsum += items[i].Weight
		}
		if wsum <= 0 {
			wsum = float64(len(free))
		}
		pinned := false
		next := free[:0:0]
		for _, i := range free {
			w := items[i].Weight
			if w <= 0 {
				w = 1
			}
			share := max(rem, 0) * w / wsum
			if c := items[i].clamp(share); c != share {
				alloc[i] = c
				rem -= c
				pinned = true
				continue
			}
			alloc[i] = share
			next = append(next, i)
		}
		if !pinned {
			return
		}
		free = next
	}
}

// round converts allocations to integers whose sum equals the rounded total
// of alloc (capped at total), using the largest remainder method.
func round(alloc []float64, total int) []int {
	out := make([]int, len(alloc))
	var sum float64
	used := 0
	for i, a := range alloc {
		a = max(a, 0)
		sum += a
		out[i] = int(math.Floor(a))
		used += out[i]
	}
	target := min(total, int(math.Round(sum)))
	order := make([]int, len(alloc))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		fa, fb := alloc[a]-math.Floor(alloc[a]), alloc[b]-math.Floor(alloc[b])
		switch {
		case fa > fb:
			return -1
		case fa < fb:
			return 1
		}
		return 0
	})
	for k := 0; used < target && k < len(order); k++ {
		out[order[k]]++
		used++
	}
	return out
}

// Find returns the placement containing the cell (x, y).
func Find(ps []Placement, x, y int) (Placement, bool) {
	for _, p := range ps {
		if p.Rect.Contains(x, y) {
			return p, true
		}
	}
	return Placement{}, false
}
