package chart

import (
	"fmt"
	"math"
)

// Mode selects the glyphs used to draw a graph.
type Mode uint8

// Graph modes.
const (
	// ModeBraille uses braille patterns: 2×4 dots per cell.
	ModeBraille Mode = iota
	// ModeBlock uses block elements: 1×8 levels per cell.
	ModeBlock
	// ModeASCII uses plain ASCII: 1×3 levels per cell.
	ModeASCII
)

var modeNames = [...]string{"braille", "block", "ascii"}

func (m Mode) String() string {
	if int(m) < len(modeNames) {
		return modeNames[m]
	}
	return "unknown"
}

// ParseMode parses "braille", "block" or "ascii".
func ParseMode(s string) (Mode, error) {
	for i, n := range modeNames {
		if s == n {
			return Mode(i), nil
		}
	}
	return 0, fmt.Errorf("unknown graph mode %q (want braille, block or ascii)", s)
}

// Style selects between filled and line graphs.
type Style uint8

// Graph styles.
const (
	StyleArea Style = iota
	StyleLine
)

// Scale maps values to heights.
type Scale uint8

// Scales.
const (
	ScaleLinear Scale = iota
	// ScaleLog compresses large values; useful for network rates.
	ScaleLog
)

// Series is one data series. Values are oldest first; the newest value is
// drawn at the right edge.
type Series struct {
	Values []float64
	// Gradient colors cells by their height in the graph (bottom = 0).
	Gradient Gradient
}

// Graph is a time-series chart. One value occupies one horizontal slot:
// two per cell in braille mode, one per cell otherwise. Older values that
// don't fit are dropped from the left.
type Graph struct {
	Width, Height int
	Mode          Mode
	Style         Style
	Scale         Scale
	// Min and Max set the value range. If Max <= Min, Max is taken from the
	// visible data (auto scale).
	Min, Max float64
	// Invert grows bars downward from the top edge, for mirrored graphs
	// such as network upload under download.
	Invert bool
	Series []Series
}

// Slots returns how many values fit horizontally.
func (g *Graph) Slots() int {
	if g.Mode == ModeBraille {
		return g.Width * 2
	}
	return g.Width
}

// Range returns the effective value range after auto scaling.
func (g *Graph) Range() (lo, hi float64) {
	lo, hi = g.Min, g.Max
	if hi > lo {
		return lo, hi
	}
	hi = lo
	n := g.Slots()
	for _, s := range g.Series {
		for _, v := range Tail(s.Values, n) {
			if v > hi {
				hi = v
			}
		}
	}
	if hi <= lo {
		hi = lo + 1
	}
	return lo, hi
}

// Render draws the graph on a new canvas.
func (g *Graph) Render() *Canvas {
	c := NewCanvas(g.Width, g.Height)
	g.Draw(c, 0, 0)
	return c
}

// Draw draws the graph onto c with its top-left corner at (x0, y0).
func (g *Graph) Draw(c *Canvas, x0, y0 int) {
	if g.Width <= 0 || g.Height <= 0 {
		return
	}
	lo, hi := g.Range()
	norm := func(v float64) float64 {
		if math.IsNaN(v) || v <= lo {
			return 0
		}
		if g.Scale == ScaleLog {
			return clamp01(math.Log1p(v-lo) / math.Log1p(hi-lo))
		}
		return clamp01((v - lo) / (hi - lo))
	}
	switch g.Mode {
	case ModeBraille:
		g.drawBraille(c, x0, y0, norm)
	default:
		g.drawLevels(c, x0, y0, norm)
	}
}

// levelsFor converts a fraction to a number of sub-cell levels, showing at
// least one level for any non-zero value so small activity stays visible.
func levelsFor(f float64, total int) int {
	n := int(math.Round(f * float64(total)))
	if n == 0 && f > 0 {
		n = 1
	}
	return n
}

// rowColor colors cell row y by its height in the graph.
func (g *Graph) rowColor(grad Gradient, y int) RGB {
	f := (float64(g.Height-y) - 0.5) / float64(g.Height)
	if g.Invert {
		f = (float64(y) + 0.5) / float64(g.Height)
	}
	return grad.At(f)
}

// Braille dot bits, indexed by [column][row] within a cell.
var brailleBits = [2][4]uint8{
	{0x01, 0x02, 0x04, 0x40},
	{0x08, 0x10, 0x20, 0x80},
}

func (g *Graph) drawBraille(c *Canvas, x0, y0 int, norm func(float64) float64) {
	w, h := g.Width, g.Height
	dotsH := h * 4
	masks := make([]uint8, w*h)
	owner := make([]int8, w*h) // series index that colors the cell
	for i := range owner {
		owner[i] = -1
	}
	setDot := func(dx, dy, si int) {
		if g.Invert {
			dy = dotsH - 1 - dy
		}
		// dy counts from the bottom; convert to rows from the top.
		row := dotsH - 1 - dy
		cx, cy := dx/2, row/4
		i := cy*w + cx
		masks[i] |= brailleBits[dx%2][row%4]
		owner[i] = int8(si)
	}

	slots := w * 2
	for si, s := range g.Series {
		vals := Tail(s.Values, slots)
		off := slots - len(vals)
		prev := -1
		for i, v := range vals {
			dx := off + i
			lv := levelsFor(norm(v), dotsH)
			if g.Style == StyleArea {
				for dy := 0; dy < lv; dy++ {
					setDot(dx, dy, si)
				}
				continue
			}
			if lv == 0 {
				prev = 0
				setDot(dx, 0, si)
				continue
			}
			top := lv - 1
			from := top
			if prev >= 0 && prev != top { // connect to the previous point
				from = prev
			}
			for dy := min(from, top); dy <= max(from, top); dy++ {
				setDot(dx, dy, si)
			}
			prev = top
		}
	}
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := y*w + x
			if masks[i] == 0 {
				continue
			}
			c.Set(x0+x, y0+y, rune(0x2800+int(masks[i])), g.rowColor(g.Series[owner[i]].Gradient, y))
		}
	}
}

var (
	blockLevels   = []rune{' ', '▁', '▂', '▃', '▄', '▅', '▆', '▇', '█'}
	blockInverted = []rune{' ', '▔', '▔', '▔', '▀', '▀', '▀', '▀', '█'}
	asciiLevels   = []rune{' ', '.', ':', '#'}
	asciiInverted = []rune{' ', '\'', ':', '#'}
)

// drawLevels draws block or ASCII graphs, which stack per-cell levels.
func (g *Graph) drawLevels(c *Canvas, x0, y0 int, norm func(float64) float64) {
	runes, per := blockLevels, 8
	switch {
	case g.Mode == ModeASCII && g.Invert:
		runes, per = asciiInverted, 3
	case g.Mode == ModeASCII:
		runes, per = asciiLevels, 3
	case g.Invert:
		runes = blockInverted
	}
	w, h := g.Width, g.Height
	for _, s := range g.Series {
		vals := Tail(s.Values, w)
		off := w - len(vals)
		for i, v := range vals {
			x := off + i
			lv := levelsFor(norm(v), h*per)
			full, part := lv/per, lv%per
			first := 0 // with StyleLine only the topmost cell is drawn
			if g.Style == StyleLine && part == 0 && full > 0 {
				first = full - 1
			} else if g.Style == StyleLine {
				first = full
			}
			for k := first; k < full; k++ {
				y := h - 1 - k
				if g.Invert {
					y = k
				}
				c.Set(x0+x, y0+y, runes[per], g.rowColor(s.Gradient, y))
			}
			if part > 0 && full < h {
				y := h - 1 - full
				if g.Invert {
					y = full
				}
				c.Set(x0+x, y0+y, runes[part], g.rowColor(s.Gradient, y))
			}
		}
	}
}

// Sparkline renders values as a one-row block graph scaled to [lo, hi]
// (auto-scaled if hi <= lo).
func Sparkline(values []float64, width int, lo, hi float64, grad Gradient) string {
	g := Graph{Width: width, Height: 1, Mode: ModeBlock, Min: lo, Max: hi,
		Series: []Series{{Values: values, Gradient: grad}}}
	return g.Render().String()
}
