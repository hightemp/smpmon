package config

import (
	"errors"
	"fmt"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// SizeKind says how a layout node's size along its parent's axis is given.
type SizeKind uint8

// Size kinds.
const (
	SizeAuto    SizeKind = iota // share of the remaining space by weight
	SizeFixed                   // exact number of cells
	SizePercent                 // percent of the parent's size
)

// Size is a fixed or percentage size, written as `10` or `"30%"`.
type Size struct {
	Kind  SizeKind
	Value float64
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (s *Size) UnmarshalYAML(n *yaml.Node) error {
	v := strings.TrimSpace(n.Value)
	if p, ok := strings.CutSuffix(v, "%"); ok {
		f, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil || f < 0 || f > 100 {
			return fmt.Errorf("line %d: invalid percentage %q", n.Line, v)
		}
		*s = Size{Kind: SizePercent, Value: f}
		return nil
	}
	f, err := strconv.ParseUint(v, 10, 16)
	if err != nil {
		return fmt.Errorf("line %d: invalid size %q (want cells like 10 or a percentage like \"30%%\")", n.Line, v)
	}
	*s = Size{Kind: SizeFixed, Value: float64(f)}
	return nil
}

// MarshalYAML implements yaml.Marshaler.
func (s Size) MarshalYAML() (any, error) {
	switch s.Kind {
	case SizePercent:
		return strconv.FormatFloat(s.Value, 'f', -1, 64) + "%", nil
	case SizeFixed:
		return int(s.Value), nil
	}
	return nil, nil
}

// IsZero lets omitempty drop unset sizes.
func (s Size) IsZero() bool { return s.Kind == SizeAuto }

// Direction of a container node.
type Direction uint8

// Container directions.
const (
	DirNone Direction = iota // leaf (widget)
	DirRow                   // children side by side, left to right
	DirCol                   // children stacked, top to bottom
)

// Node is a layout tree node: either a container (row/col) with children or
// a widget leaf. In YAML exactly one of `row`, `col` or `widget` is set:
//
//	row:
//	  - widget: cpu
//	    weight: 3
//	  - col:
//	      - widget: mem
//	      - widget: net
//	        options: {scale: log}
//	size: 40%
type Node struct {
	Dir      Direction
	Children []*Node

	Widget  string
	Title   string
	Source  string    // metrics source; empty means local
	Options yaml.Node // widget-specific options, decoded by the widget

	Size   Size
	Weight float64 // relative share for SizeAuto nodes; default 1
	Min    int     // minimum cells along the parent's axis
	Max    int     // maximum cells along the parent's axis; 0 = unlimited

	Line int // source line for error messages
}

type rawNode struct {
	Row     []*Node   `yaml:"row"`
	Col     []*Node   `yaml:"col"`
	Widget  string    `yaml:"widget"`
	Title   string    `yaml:"title"`
	Source  string    `yaml:"source"`
	Options yaml.Node `yaml:"options"`
	Size    Size      `yaml:"size"`
	Weight  float64   `yaml:"weight"`
	Min     int       `yaml:"min"`
	Max     int       `yaml:"max"`
}

// UnmarshalYAML implements yaml.Unmarshaler.
func (n *Node) UnmarshalYAML(y *yaml.Node) error {
	if y.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: layout node must be a mapping with row, col or widget", y.Line)
	}
	var r rawNode
	if err := decodeStrict(y, &r); err != nil {
		return err
	}
	set := 0
	for _, has := range []bool{hasKey(y, "row"), hasKey(y, "col"), r.Widget != ""} {
		if has {
			set++
		}
	}
	if set != 1 {
		return fmt.Errorf("line %d: layout node needs exactly one of row, col or widget", y.Line)
	}
	*n = Node{
		Widget: r.Widget, Title: r.Title, Source: r.Source, Options: r.Options,
		Size: r.Size, Weight: r.Weight, Min: r.Min, Max: r.Max, Line: y.Line,
	}
	switch {
	case hasKey(y, "row"):
		n.Dir, n.Children = DirRow, r.Row
	case hasKey(y, "col"):
		n.Dir, n.Children = DirCol, r.Col
	}
	if n.Dir != DirNone && len(n.Children) == 0 {
		return fmt.Errorf("line %d: container has no children", y.Line)
	}
	if n.Weight < 0 || n.Min < 0 || n.Max < 0 {
		return fmt.Errorf("line %d: weight, min and max must not be negative", y.Line)
	}
	if n.Max > 0 && n.Min > n.Max {
		return fmt.Errorf("line %d: min (%d) is greater than max (%d)", y.Line, n.Min, n.Max)
	}
	if n.Weight == 0 {
		n.Weight = 1
	}
	return nil
}

// MarshalYAML implements yaml.Marshaler.
func (n *Node) MarshalYAML() (any, error) {
	out := struct {
		Row     []*Node    `yaml:"row,omitempty"`
		Col     []*Node    `yaml:"col,omitempty"`
		Widget  string     `yaml:"widget,omitempty"`
		Title   string     `yaml:"title,omitempty"`
		Source  string     `yaml:"source,omitempty"`
		Options *yaml.Node `yaml:"options,omitempty"`
		Size    Size       `yaml:"size,omitempty"`
		Weight  float64    `yaml:"weight,omitempty"`
		Min     int        `yaml:"min,omitempty"`
		Max     int        `yaml:"max,omitempty"`
	}{Widget: n.Widget, Title: n.Title, Source: n.Source, Size: n.Size, Min: n.Min, Max: n.Max}
	if n.Weight != 1 {
		out.Weight = n.Weight
	}
	if n.Options.Kind != 0 {
		out.Options = &n.Options
	}
	switch n.Dir {
	case DirRow:
		out.Row = n.Children
	case DirCol:
		out.Col = n.Children
	}
	return out, nil
}

// Walk calls f for n and all descendants in depth-first order. The path is
// the list of child indexes from the root.
func (n *Node) Walk(f func(n *Node, path []int) error) error {
	return n.walk(nil, f)
}

func (n *Node) walk(path []int, f func(*Node, []int) error) error {
	if err := f(n, path); err != nil {
		return err
	}
	for i, c := range n.Children {
		if err := c.walk(append(path[:len(path):len(path)], i), f); err != nil {
			return err
		}
	}
	return nil
}

// Widgets returns all widget leaves.
func (n *Node) Widgets() []*Node {
	var out []*Node
	_ = n.Walk(func(n *Node, _ []int) error {
		if n.Dir == DirNone {
			out = append(out, n)
		}
		return nil
	})
	return out
}

// DecodeOptions decodes the widget options into v, rejecting unknown fields.
func (n *Node) DecodeOptions(v any) error {
	if n.Options.Kind == 0 {
		return nil
	}
	if err := decodeStrict(&n.Options, v); err != nil {
		return fmt.Errorf("widget %q options: %w", n.Widget, err)
	}
	return nil
}

func hasKey(m *yaml.Node, key string) bool {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return true
		}
	}
	return false
}

// decodeStrict decodes a YAML node into v and fails on unknown fields.
func decodeStrict(n *yaml.Node, v any) error {
	if err := checkKnownFields(n, v); err != nil {
		return err
	}
	return n.Decode(v)
}

var errUnknownField = errors.New("unknown field")
