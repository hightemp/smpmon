package ui

import (
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"

	"github.com/hightemp/smpmon/internal/chart"
)

// fg wraps s in a truecolor foreground SGR sequence. Plain SGR is used rather
// than lipgloss styles because widgets build many small fragments per frame.
func fg(c chart.RGB, s string) string {
	return "\x1b[38;2;" + rgbParams(c) + "m" + s + "\x1b[39m"
}

// rgbParams returns "r;g;b" for SGR sequences.
func rgbParams(c chart.RGB) string {
	return strconv.Itoa(int(c.R)) + ";" + strconv.Itoa(int(c.G)) + ";" + strconv.Itoa(int(c.B))
}

// bg wraps s in a truecolor background SGR sequence.
func bg(c chart.RGB, s string) string {
	return "\x1b[48;2;" + rgbParams(c) + "m" + s + "\x1b[49m"
}

func bold(s string) string { return "\x1b[1m" + s + "\x1b[22m" }

// width returns the display width of s, ignoring escape sequences.
//
// Full grapheme segmentation is slow and dominated rendering, while frames
// consist almost entirely of ASCII, box drawing, block and braille
// characters, which are all one cell wide. Those are counted directly; any
// other character falls back to ansi.StringWidth.
func width(s string) int {
	n := 0
	for i := 0; i < len(s); {
		c := s[i]
		switch {
		case c == 0x1b:
			i = skipEscape(s, i)
			continue
		case c < 0x80:
			if c >= 0x20 && c != 0x7f {
				n++
			}
			i++
			continue
		}
		r, size := utf8.DecodeRuneInString(s[i:])
		if !narrow(r) {
			return ansi.StringWidth(s)
		}
		n++
		i += size
	}
	return n
}

// narrow reports runes known to occupy exactly one cell.
func narrow(r rune) bool {
	return (r >= 0xa0 && r < 0x300) || // Latin-1 and Latin Extended
		(r >= 0x2010 && r < 0x2600) || // punctuation, arrows, box drawing, blocks, shapes
		(r >= 0x2800 && r < 0x2900) // braille
}

// skipEscape returns the index after the escape sequence starting at i.
// Only CSI (ESC [ ... final) and two-byte sequences are produced by the UI.
func skipEscape(s string, i int) int {
	if i+1 >= len(s) {
		return len(s)
	}
	if s[i+1] != '[' {
		return i + 2
	}
	for j := i + 2; j < len(s); j++ {
		if s[j] >= 0x40 && s[j] <= 0x7e {
			return j + 1
		}
	}
	return len(s)
}

// fit truncates or pads s with spaces to exactly w cells.
func fit(s string, w int) string {
	if w <= 0 {
		return ""
	}
	sw := width(s)
	if sw > w {
		s = ansi.Truncate(s, w, "")
		sw = width(s)
	}
	if sw < w {
		s += strings.Repeat(" ", w-sw)
	}
	return s
}

// fitLeft truncates with an ellipsis if needed and pads to w cells.
func fitEllipsis(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if width(s) > w {
		s = ansi.Truncate(s, w, "…")
	}
	return fit(s, w)
}

// alignRight right-aligns s in w cells.
func alignRight(s string, w int) string {
	sw := width(s)
	if sw >= w {
		return ansi.Truncate(s, w, "")
	}
	return strings.Repeat(" ", w-sw) + s
}

// spread places left and right text in w cells with spaces between.
func spread(left, right string, w int) string {
	gap := w - width(left) - width(right)
	if gap < 1 {
		return fit(left, w)
	}
	return left + strings.Repeat(" ", gap) + right
}

// block makes a w×h block from lines, padding or truncating as needed.
func block(lines []string, w, h int) []string {
	out := make([]string, h)
	blank := strings.Repeat(" ", max(w, 0))
	for i := range out {
		if i < len(lines) {
			out[i] = fit(lines[i], w)
		} else {
			out[i] = blank
		}
	}
	return out
}

// ansiStrip removes escape sequences, e.g. to restyle a whole line.
func ansiStrip(s string) string { return ansi.Strip(s) }

// cutLeft drops the first n cells of s, keeping its colors.
func cutLeft(s string, n int) string { return ansi.TruncateLeft(s, n, "") }
