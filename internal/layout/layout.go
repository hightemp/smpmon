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

// ComputeAll is like Compute but also returns containers, parents before
// children. Nodes with an empty rectangle are included so editors can still
// select them.
func ComputeAll(root *config.Node, r Rect) []Placement {
	var out []Placement
	computeAll(root, r, nil, &out)
	return out
}

func computeAll(n *config.Node, r Rect, path []int, out *[]Placement) {
	*out = append(*out, Placement{Node: n, Rect: r, Path: slices.Clone(path)})
	for i, c := range n.Children {
		computeAll(c, childRect(n, r, i), append(path, i), out)
	}
}

// childRect returns the rectangle of child i of container n placed in r.
func childRect(n *config.Node, r Rect, i int) Rect {
	sizes := childSizes(n, r)
	pos := 0
	for j := 0; j < i; j++ {
		pos += sizes[j]
	}
	if n.Dir == config.DirCol {
		return Rect{X: r.X, Y: r.Y + pos, W: r.W, H: sizes[i]}
	}
	return Rect{X: r.X + pos, Y: r.Y, W: sizes[i], H: r.H}
}

func childSizes(n *config.Node, r Rect) []int {
	total := r.W
	if n.Dir == config.DirCol {
		total = r.H
	}
	items := make([]Item, len(n.Children))
	for i, c := range n.Children {
		items[i] = Item{Size: c.Size, Weight: c.Weight, Min: c.Min, Max: c.Max}
	}
	return Split(total, items)
}

// Direction for Neighbor.
type Direction uint8

// Directions.
const (
	Up Direction = iota
	Down
	Left
	Right
)

// Neighbor returns the index in ps of the placement closest to from in
// direction d, preferring the one that overlaps it most, or -1.
func Neighbor(ps []Placement, from Rect, d Direction) int {
	best, bestDist, bestOverlap := -1, 1<<30, -1
	for i, p := range ps {
		r := p.Rect
		if r.Empty() || r == from {
			continue
		}
		var dist, overlap int
		switch d {
		case Left:
			dist = from.X - (r.X + r.W)
			overlap = span(from.Y, from.H, r.Y, r.H)
		case Right:
			dist = r.X - (from.X + from.W)
			overlap = span(from.Y, from.H, r.Y, r.H)
		case Up:
			dist = from.Y - (r.Y + r.H)
			overlap = span(from.X, from.W, r.X, r.W)
		case Down:
			dist = r.Y - (from.Y + from.H)
			overlap = span(from.X, from.W, r.X, r.W)
		}
		if dist < 0 || overlap <= 0 {
			continue
		}
		if dist < bestDist || (dist == bestDist && overlap > bestOverlap) {
			best, bestDist, bestOverlap = i, dist, overlap
		}
	}
	return best
}

// span returns the length of the overlap of [a, a+al) and [b, b+bl).
func span(a, al, b, bl int) int {
	return min(a+al, b+bl) - max(a, b)
}

func compute(n *config.Node, r Rect, path []int, out *[]Placement) {
	if r.Empty() {
		return
	}
	if n.Dir == config.DirNone {
		*out = append(*out, Placement{Node: n, Rect: r, Path: slices.Clone(path)})
		return
	}
	sizes := childSizes(n, r)
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
