#!/usr/bin/env python3
"""Convert a frame with truecolor ANSI SGR codes (from `smpmon frame`) to SVG.

Words become <text> elements stretched to their exact cell width, so
alignment survives font differences. Braille, block elements and squares are
drawn as rectangles, since fallback fonts render them poorly.

Usage: smpmon frame ... | scripts/ansi2svg.py > docs/screenshot.svg
"""
import html
import re
import sys

CELL_W, CELL_H, FONT_SIZE, PAD = 8.4, 17.0, 14, 16
DEFAULT_FG, BACKGROUND = "#cccccc", "#16171a"
SGR = re.compile(r"\x1b\[([0-9;]*)m")
OTHER_ESC = re.compile(r"\x1b(\[[0-9;?]*[A-Za-z]|\][^\x07]*\x07|.)")


def parse(line):
    """Yield (char, fg, bg, bold) for each cell of the line."""
    fg, bg, bold = None, None, False
    pos = 0
    while pos < len(line):
        m = SGR.match(line, pos)
        if m:
            params = [int(p) if p else 0 for p in m.group(1).split(";")]
            i = 0
            while i < len(params):
                p = params[i]
                if p == 0:
                    fg, bg, bold = None, None, False
                elif p == 1:
                    bold = True
                elif p == 22:
                    bold = False
                elif p == 39:
                    fg = None
                elif p == 49:
                    bg = None
                elif p in (38, 48) and i + 4 < len(params) + 0 and params[i + 1] == 2:
                    color = "#%02x%02x%02x" % tuple(params[i + 2:i + 5])
                    if p == 38:
                        fg = color
                    else:
                        bg = color
                    i += 4
                i += 1
            pos = m.end()
            continue
        if line[pos] == "\x1b":
            m = OTHER_ESC.match(line, pos)
            pos = m.end() if m else pos + 1
            continue
        yield line[pos], fg, bg, bold
        pos += 1


BLOCKS = "▁▂▃▄▅▆▇█"


def is_shape(ch):
    return 0x2800 < ord(ch) <= 0x28FF or ch in BLOCKS or ch in "▀▔■"


def rect(x, y, w, h, fill):
    return f'<rect x="{x:.2f}" y="{y:.2f}" width="{w:.2f}" height="{h:.2f}" fill="{fill}"/>'


def shape(ch, x, y, fill):
    """Return SVG rects drawing ch in the cell at (x, y), or nothing."""
    o = ord(ch)
    if 0x2800 < o <= 0x28FF:
        bits = o - 0x2800
        # dot n -> (column, row): 1,2,3,7 left; 4,5,6,8 right
        layout = [(0, 0), (0, 1), (0, 2), (1, 0), (1, 1), (1, 2), (0, 3), (1, 3)]
        dw, dh = CELL_W / 2, CELL_H / 4
        return [rect(x + c * dw + dw * 0.18, y + r * dh + dh * 0.15, dw * 0.64, dh * 0.7, fill)
                for i, (c, r) in enumerate(layout) if bits & (1 << i)]
    if ch in BLOCKS:
        h = CELL_H * (BLOCKS.index(ch) + 1) / 8
        return [rect(x, y + CELL_H - h, CELL_W, h, fill)]
    if ch == "▀":
        return [rect(x, y, CELL_W, CELL_H / 2, fill)]
    if ch == "▔":
        return [rect(x, y, CELL_W, CELL_H / 8, fill)]
    if ch == "■":
        return [rect(x + CELL_W * 0.12, y + CELL_H * 0.3, CELL_W * 0.76, CELL_H * 0.42, fill)]
    return []


def runs(cells, key):
    """Group consecutive cells by key(cell)."""
    out, start = [], 0
    for i in range(1, len(cells) + 1):
        if i == len(cells) or key(cells[i]) != key(cells[start]):
            out.append((start, cells[start:i]))
            start = i
    return out


def main():
    lines = sys.stdin.read().rstrip("\n").split("\n")
    rows = [list(parse(l)) for l in lines]
    cols = max(len(r) for r in rows)
    width = cols * CELL_W + 2 * PAD
    height = len(rows) * CELL_H + 2 * PAD
    out = [
        f'<svg xmlns="http://www.w3.org/2000/svg" width="{width:.0f}" height="{height:.0f}" '
        f'viewBox="0 0 {width:.0f} {height:.0f}">',
        f'<rect width="100%" height="100%" rx="8" fill="{BACKGROUND}"/>',
        f'<g font-family="\'DejaVu Sans Mono\', Menlo, Consolas, \'Liberation Mono\', monospace" '
        f'font-size="{FONT_SIZE}" xml:space="preserve">',
    ]
    for y, cells in enumerate(rows):
        top = PAD + y * CELL_H
        for start, run in runs(cells, lambda c: c[2]):
            if run[0][2]:
                out.append(f'<rect x="{PAD + start * CELL_W:.1f}" y="{top:.1f}" '
                           f'width="{len(run) * CELL_W:.1f}" height="{CELL_H:.1f}" fill="{run[0][2]}"/>')
        for x, (ch, fg, _, _) in enumerate(cells):
            out.extend(shape(ch, PAD + x * CELL_W, top, fg or DEFAULT_FG))
        word_key = lambda c: (c[1], c[3], c[0] == " " or is_shape(c[0]))
        for start, run in runs(cells, word_key):
            text = "".join(c[0] for c in run)
            if text[0] == " " or is_shape(text[0]):
                continue
            fg, bold = run[0][1] or DEFAULT_FG, run[0][3]
            weight = ' font-weight="bold"' if bold else ""
            out.append(f'<text x="{PAD + start * CELL_W:.1f}" y="{top + CELL_H * 0.78:.1f}" fill="{fg}"{weight} '
                       f'textLength="{len(run) * CELL_W:.1f}" lengthAdjust="spacingAndGlyphs">'
                       f'{html.escape(text)}</text>')
    out.append("</g></svg>")
    print("\n".join(out))


if __name__ == "__main__":
    main()
