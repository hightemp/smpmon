package metrics

import (
	"errors"
	"fmt"
)

// Selector matches metric keys against a pattern. A pattern is a key whose
// segments may be "*" (exactly one segment) or "**" (zero or more segments),
// e.g. "cpu.core.*.usage" or "net.**". A literal "*" segment can be written
// as "\*".
type Selector struct {
	pattern string
	segs    []selSeg
	literal bool
}

type selKind uint8

const (
	segLiteral selKind = iota
	segOne
	segMany
)

type selSeg struct {
	kind selKind
	text string
}

// ParseSelector compiles a selector pattern.
func ParseSelector(pattern string) (Selector, error) {
	if pattern == "" {
		return Selector{}, errors.New("empty selector")
	}
	raw := splitRaw(pattern)
	sel := Selector{pattern: pattern, literal: true, segs: make([]selSeg, 0, len(raw))}
	for _, r := range raw {
		switch r {
		case "*":
			sel.segs = append(sel.segs, selSeg{kind: segOne})
			sel.literal = false
		case "**":
			sel.segs = append(sel.segs, selSeg{kind: segMany})
			sel.literal = false
		case "":
			return Selector{}, fmt.Errorf("selector %q: empty segment", pattern)
		default:
			sel.segs = append(sel.segs, selSeg{kind: segLiteral, text: unescape(r)})
		}
	}
	return sel, nil
}

// MustSelector is like ParseSelector but panics on error. Intended for
// selectors that are compile-time constants.
func MustSelector(pattern string) Selector {
	s, err := ParseSelector(pattern)
	if err != nil {
		panic(err)
	}
	return s
}

// String returns the original pattern.
func (s Selector) String() string { return s.pattern }

// IsLiteral reports whether the selector contains no wildcards and therefore
// matches exactly one key.
func (s Selector) IsLiteral() bool { return s.literal }

// Key returns the single key matched by a literal selector.
func (s Selector) Key() Key {
	segs := make([]string, len(s.segs))
	for i, sg := range s.segs {
		segs[i] = sg.text
	}
	return NewKey(segs...)
}

// Match reports whether k matches the selector.
func (s Selector) Match(k Key) bool {
	return matchSegs(s.segs, k.Segments())
}

func matchSegs(pat []selSeg, segs []string) bool {
	for len(pat) > 0 {
		p := pat[0]
		if p.kind == segMany {
			rest := pat[1:]
			if len(rest) == 0 {
				return true
			}
			for i := 0; i <= len(segs); i++ {
				if matchSegs(rest, segs[i:]) {
					return true
				}
			}
			return false
		}
		if len(segs) == 0 {
			return false
		}
		if p.kind == segLiteral && p.text != segs[0] {
			return false
		}
		pat, segs = pat[1:], segs[1:]
	}
	return len(segs) == 0
}

// splitRaw splits on unescaped dots but keeps escapes in place so that "\*"
// can be distinguished from "*".
func splitRaw(s string) []string {
	var (
		out   []string
		start int
	)
	for i := 0; i < len(s); i++ {
		switch s[i] {
		case '\\':
			i++
		case '.':
			out = append(out, s[start:i])
			start = i + 1
		}
	}
	return append(out, s[start:])
}

// unescape removes escapes from a raw segment produced by splitRaw (which
// never contains unescaped dots, so splitEscaped yields exactly one segment).
func unescape(s string) string {
	return splitEscaped(s)[0]
}
