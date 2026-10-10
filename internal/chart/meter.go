package chart

import "math"

// Meter is a horizontal bar showing a fraction, colored along its length.
type Meter struct {
	Width    int
	Gradient Gradient
	Empty    RGB  // color of the unfilled part
	Fill     rune // default '■'
	Blank    rune // default '■' drawn in Empty color
}

// Render returns the meter for fraction f in [0,1] as a single line.
func (m Meter) Render(f float64) string {
	c := NewCanvas(m.Width, 1)
	m.Draw(c, 0, 0, f)
	return c.String()
}

// Draw draws the meter onto c at (x0, y0).
func (m Meter) Draw(c *Canvas, x0, y0 int, f float64) {
	fill, blank := m.Fill, m.Blank
	if fill == 0 {
		fill = '■'
	}
	if blank == 0 {
		blank = fill
	}
	n := int(math.Round(clamp01(f) * float64(m.Width)))
	if n == 0 && f > 0 && m.Width > 0 {
		n = 1
	}
	for x := 0; x < m.Width; x++ {
		if x < n {
			c.Set(x0+x, y0, fill, m.Gradient.At((float64(x)+0.5)/float64(m.Width)))
		} else {
			c.Set(x0+x, y0, blank, m.Empty)
		}
	}
}
