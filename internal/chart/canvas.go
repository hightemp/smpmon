package chart

import (
	"strconv"
	"strings"
)

type cell struct {
	r     rune
	fg    RGB
	hasFg bool
}

// Canvas is a fixed-size grid of colored cells.
type Canvas struct {
	w, h  int
	cells []cell
}

// NewCanvas creates a w×h canvas filled with spaces.
func NewCanvas(w, h int) *Canvas {
	w, h = max(w, 0), max(h, 0)
	c := &Canvas{w: w, h: h, cells: make([]cell, w*h)}
	for i := range c.cells {
		c.cells[i].r = ' '
	}
	return c
}

// Width returns the canvas width in cells.
func (c *Canvas) Width() int { return c.w }

// Height returns the canvas height in cells.
func (c *Canvas) Height() int { return c.h }

// Set puts a rune at (x, y). Out-of-range coordinates are ignored.
func (c *Canvas) Set(x, y int, r rune, fg RGB) {
	if x < 0 || y < 0 || x >= c.w || y >= c.h {
		return
	}
	c.cells[y*c.w+x] = cell{r: r, fg: fg, hasFg: true}
}

// SetPlain puts an uncolored rune at (x, y).
func (c *Canvas) SetPlain(x, y int, r rune) {
	if x < 0 || y < 0 || x >= c.w || y >= c.h {
		return
	}
	c.cells[y*c.w+x] = cell{r: r}
}

// Text writes s starting at (x, y), clipped to the canvas.
func (c *Canvas) Text(x, y int, s string, fg RGB) {
	for _, r := range s {
		c.Set(x, y, r, fg)
		x++
	}
}

// Rune returns the rune at (x, y).
func (c *Canvas) Rune(x, y int) rune {
	if x < 0 || y < 0 || x >= c.w || y >= c.h {
		return 0
	}
	return c.cells[y*c.w+x].r
}

// Lines renders each row to a string with truecolor SGR sequences. Color
// codes are only emitted on changes and each row ends with a reset, so rows
// can be composed independently.
func (c *Canvas) Lines() []string {
	out := make([]string, c.h)
	var b strings.Builder
	for y := 0; y < c.h; y++ {
		b.Reset()
		b.Grow(c.w * 4)
		var cur RGB
		colored := false
		for x := 0; x < c.w; x++ {
			ce := c.cells[y*c.w+x]
			switch {
			case ce.hasFg && (!colored || ce.fg != cur):
				writeFg(&b, ce.fg)
				cur, colored = ce.fg, true
			case !ce.hasFg && colored && ce.r != ' ':
				b.WriteString("\x1b[39m")
				colored = false
			}
			b.WriteRune(ce.r)
		}
		if colored {
			b.WriteString("\x1b[39m")
		}
		out[y] = b.String()
	}
	return out
}

// String renders the canvas as newline-separated rows.
func (c *Canvas) String() string {
	return strings.Join(c.Lines(), "\n")
}

// Plain renders the canvas without any color codes (for tests and ASCII
// terminals).
func (c *Canvas) Plain() string {
	var b strings.Builder
	for y := 0; y < c.h; y++ {
		if y > 0 {
			b.WriteByte('\n')
		}
		for x := 0; x < c.w; x++ {
			b.WriteRune(c.cells[y*c.w+x].r)
		}
	}
	return b.String()
}

func writeFg(b *strings.Builder, c RGB) {
	b.WriteString("\x1b[38;2;")
	b.WriteString(strconv.Itoa(int(c.R)))
	b.WriteByte(';')
	b.WriteString(strconv.Itoa(int(c.G)))
	b.WriteByte(';')
	b.WriteString(strconv.Itoa(int(c.B)))
	b.WriteByte('m')
}
