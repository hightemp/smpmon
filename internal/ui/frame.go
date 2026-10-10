package ui

import (
	"strings"

	"github.com/hightemp/smpmon/internal/chart"
)

type borderChars struct {
	tl, tr, bl, br, h, v string
}

var borders = map[string]borderChars{
	"rounded": {"╭", "╮", "╰", "╯", "─", "│"},
	"single":  {"┌", "┐", "└", "┘", "─", "│"},
	"double":  {"╔", "╗", "╚", "╝", "═", "║"},
	"thick":   {"┏", "┓", "┗", "┛", "━", "┃"},
}

// frame draws a bordered box of exactly w×h cells. title goes on the left of
// the top edge, info on the right of the top edge and footer on the right of
// the bottom edge. content receives the inner size and returns its lines.
type frame struct {
	style  string
	color  chart.RGB
	title  string // already styled
	info   string // already styled
	footer string // already styled
}

// inner returns the content size for a w×h frame.
func (f frame) inner(w, h int) (int, int) {
	if f.style == "none" {
		return w, h
	}
	return max(w-2, 0), max(h-2, 0)
}

func (f frame) render(w, h int, content []string) []string {
	if w <= 0 || h <= 0 {
		return nil
	}
	b, ok := borders[f.style]
	if !ok {
		return block(content, w, h)
	}
	if w < 2 || h < 2 {
		return block(nil, w, h)
	}
	iw, ih := w-2, h-2
	out := make([]string, 0, h)
	out = append(out, f.edge(b.tl, b.tr, b.h, f.title, f.info, iw))
	lines := block(content, iw, ih)
	side := fg(f.color, b.v)
	for _, l := range lines {
		out = append(out, side+l+side)
	}
	out = append(out, f.edge(b.bl, b.br, b.h, "", f.footer, iw))
	return out
}

// edge builds a horizontal border line with optional left and right labels:
// ╭─ title ───── info ─╮
func (f frame) edge(left, right, h, ltext, rtext string, iw int) string {
	var sb strings.Builder
	sb.WriteString(left)
	used := 0
	plainH := func(n int) {
		if n > 0 {
			sb.WriteString(strings.Repeat(h, n))
			used += n
		}
	}
	// Labels are dropped when there is no room, right label first.
	lw := 0
	if ltext != "" {
		lw = width(ltext) + 2
	}
	rw := 0
	if rtext != "" {
		rw = width(rtext) + 2
	}
	if 1+lw+rw+1 > iw {
		rw, rtext = 0, ""
	}
	if 1+lw+1 > iw {
		lw, ltext = 0, ""
	}

	var line strings.Builder
	if ltext != "" {
		plainH(1)
		line.WriteString(fg(f.color, sb.String()))
		sb.Reset()
		line.WriteString(" " + ltext + " ")
		used += lw
	}
	fill := iw - used - rw - 1
	if rtext == "" {
		fill = iw - used
	}
	plainH(fill)
	if rtext != "" {
		line.WriteString(fg(f.color, sb.String()))
		sb.Reset()
		line.WriteString(" " + rtext + " ")
		plainH(1)
	}
	sb.WriteString(right)
	line.WriteString(fg(f.color, sb.String()))
	return line.String()
}
