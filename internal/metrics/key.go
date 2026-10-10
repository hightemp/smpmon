// Package metrics defines metric keys, selectors, the metric registry,
// fixed-size time series buffers and the in-memory Store that collectors
// write to and widgets read from.
package metrics

import (
	"strconv"
	"strings"
)

// Key identifies a numeric metric series. It is a dot-separated list of
// segments, e.g. "cpu.core.3.usage". Segments that contain '.' or '\' are
// escaped with a backslash, so arbitrary strings (mount points, interface
// names) can be used as segments; build keys with NewKey to get this right.
type Key string

// NewKey joins segments into a Key, escaping each segment.
func NewKey(segments ...string) Key {
	var b strings.Builder
	for i, s := range segments {
		if i > 0 {
			b.WriteByte('.')
		}
		b.WriteString(EscapeSegment(s))
	}
	return Key(b.String())
}

// EscapeSegment escapes '.' and '\' in a single key segment.
func EscapeSegment(s string) string {
	if !strings.ContainsAny(s, `.\`) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 2)
	for i := 0; i < len(s); i++ {
		if s[i] == '.' || s[i] == '\\' {
			b.WriteByte('\\')
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

// Segments returns the unescaped segments of k.
func (k Key) Segments() []string {
	return splitEscaped(string(k))
}

// Child returns a new key with additional segments appended.
func (k Key) Child(segments ...string) Key {
	if k == "" {
		return NewKey(segments...)
	}
	return k + "." + NewKey(segments...)
}

// splitEscaped splits s on unescaped dots and removes escape characters.
func splitEscaped(s string) []string {
	if s == "" {
		return nil
	}
	if !strings.Contains(s, `\`) {
		return strings.Split(s, ".")
	}
	var (
		out []string
		cur strings.Builder
	)
	for i := 0; i < len(s); i++ {
		switch c := s[i]; {
		case c == '\\' && i+1 < len(s):
			i++
			cur.WriteByte(s[i])
		case c == '.':
			out = append(out, cur.String())
			cur.Reset()
		default:
			cur.WriteByte(c)
		}
	}
	return append(out, cur.String())
}

// CompareKeys orders keys segment by segment, comparing numeric segments by
// value so that "cpu.core.2" sorts before "cpu.core.10".
func CompareKeys(a, b Key) int {
	if a == b {
		return 0
	}
	as, bs := a.Segments(), b.Segments()
	for i := 0; i < len(as) && i < len(bs); i++ {
		if c := compareSegment(as[i], bs[i]); c != 0 {
			return c
		}
	}
	return len(as) - len(bs)
}

func compareSegment(a, b string) int {
	if a == b {
		return 0
	}
	ai, aerr := strconv.ParseUint(a, 10, 64)
	bi, berr := strconv.ParseUint(b, 10, 64)
	switch {
	case aerr == nil && berr == nil:
		if ai < bi {
			return -1
		}
		return 1
	case aerr == nil:
		return -1 // numbers before names
	case berr == nil:
		return 1
	}
	return strings.Compare(a, b)
}
