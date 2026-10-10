// Package chart renders graphs, sparklines and meters into strings of
// terminal cells. It writes truecolor SGR sequences directly instead of going
// through lipgloss for speed; Bubble Tea downsamples colors to the terminal's
// profile on output.
package chart

import (
	"fmt"
	"strconv"
	"strings"
)

// RGB is a 24-bit color. It implements image/color.Color so it can be passed
// to lipgloss.
type RGB struct {
	R, G, B uint8
}

// RGBA implements image/color.Color.
func (c RGB) RGBA() (r, g, b, a uint32) {
	r, g, b = uint32(c.R), uint32(c.G), uint32(c.B)
	return r<<8 | r, g<<8 | g, b<<8 | b, 0xffff
}

// Hex returns the color as "#rrggbb".
func (c RGB) Hex() string {
	return fmt.Sprintf("#%02x%02x%02x", c.R, c.G, c.B)
}

// ParseHex parses "#rgb" or "#rrggbb" (the leading '#' is optional).
func ParseHex(s string) (RGB, error) {
	h := strings.TrimPrefix(s, "#")
	if len(h) == 3 {
		h = string([]byte{h[0], h[0], h[1], h[1], h[2], h[2]})
	}
	if len(h) != 6 {
		return RGB{}, fmt.Errorf("invalid color %q", s)
	}
	v, err := strconv.ParseUint(h, 16, 32)
	if err != nil {
		return RGB{}, fmt.Errorf("invalid color %q", s)
	}
	return RGB{uint8(v >> 16), uint8(v >> 8), uint8(v)}, nil
}

// MustHex is ParseHex for constants; it panics on error.
func MustHex(s string) RGB {
	c, err := ParseHex(s)
	if err != nil {
		panic(err)
	}
	return c
}

// Lerp blends from c to o by t in [0,1].
func (c RGB) Lerp(o RGB, t float64) RGB {
	t = clamp01(t)
	mix := func(a, b uint8) uint8 { return uint8(float64(a) + (float64(b)-float64(a))*t + 0.5) }
	return RGB{mix(c.R, o.R), mix(c.G, o.G), mix(c.B, o.B)}
}

// Gradient maps a fraction in [0,1] to a color by interpolating evenly
// spaced stops. A gradient with one stop is a solid color.
type Gradient []RGB

// At returns the color at fraction t.
func (g Gradient) At(t float64) RGB {
	switch len(g) {
	case 0:
		return RGB{255, 255, 255}
	case 1:
		return g[0]
	}
	t = clamp01(t)
	pos := t * float64(len(g)-1)
	i := int(pos)
	if i >= len(g)-1 {
		return g[len(g)-1]
	}
	return g[i].Lerp(g[i+1], pos-float64(i))
}

func clamp01(t float64) float64 {
	switch {
	case t < 0 || t != t: // NaN
		return 0
	case t > 1:
		return 1
	}
	return t
}
