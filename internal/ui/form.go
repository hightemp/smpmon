package ui

import (
	"fmt"
	"reflect"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"go.yaml.in/yaml/v3"

	"github.com/hightemp/smpmon/internal/config"
)

// formField is one editable setting of the selected layout node.
type formField struct {
	label  string
	toggle bool // enter flips the value instead of opening an input
	value  func(n *config.Node) string
	set    func(n *config.Node, v string) error
}

// formDialog edits the selected node's title, size and widget options.
// Every change goes through Model.apply, so it can be undone and is
// validated by re-creating the widget.
type formDialog struct {
	m      *Model
	fields []formField
	sel    int
}

func (f *formDialog) title() string { return "Options: " + f.m.edit.describe() }

func (f *formDialog) lines(ctx *Context, w int) []string {
	t := ctx.Theme
	n := f.m.edit.selected()
	labelW := 0
	for _, fl := range f.fields {
		labelW = max(labelW, len(fl.label))
	}
	out := make([]string, 0, len(f.fields)+2)
	for i, fl := range f.fields {
		v := fl.value(n)
		if v == "" {
			v = fg(t.Dim, "default")
		}
		line := fit(" "+fmt.Sprintf("%-*s", labelW, fl.label)+"  "+v, w)
		if i == f.sel {
			line = bg(t.Selected, fg(t.SelectedFg, ansiStrip(line)))
		} else {
			line = fg(t.DialogFg, line)
		}
		out = append(out, line)
	}
	return append(out, "", hint(ctx, "↑↓", "select")+"  "+hint(ctx, "enter", "edit")+"  "+hint(ctx, "esc", "close"))
}

func (f *formDialog) key(k tea.KeyPressMsg) (bool, tea.Cmd) {
	switch k.String() {
	case "up", "k":
		f.sel = (f.sel - 1 + len(f.fields)) % len(f.fields)
	case "down", "j":
		f.sel = (f.sel + 1) % len(f.fields)
	case "esc", "q":
		return true, nil
	case "enter", "space":
		fl := f.fields[f.sel]
		if fl.toggle {
			f.m.apply(func(e *editor) error { return fl.set(e.selected(), "") })
			return false, nil
		}
		f.m.dialog = &inputDialog{
			prompt: fl.label + ":",
			value:  fl.value(f.m.edit.selected()),
			onSubmit: func(v string) tea.Cmd {
				f.m.apply(func(e *editor) error { return fl.set(e.selected(), strings.TrimSpace(v)) })
				f.m.dialog = f // back to the form
				return nil
			},
		}
		return true, nil
	}
	return false, nil
}

func (m *Model) optionsForm() dialog {
	n := m.edit.selected()
	fields := []formField{}
	if n.Dir == config.DirNone {
		fields = append(fields, formField{
			label: "title",
			value: func(n *config.Node) string { return n.Title },
			set:   func(n *config.Node, v string) error { n.Title = v; return nil },
		}, formField{
			label: "source",
			value: func(n *config.Node) string { return n.Source },
			set: func(n *config.Node, v string) error {
				if v != "" && m.hostIndex(v) < 0 {
					return fmt.Errorf("unknown host %q", v)
				}
				n.Source = v
				return nil
			},
		})
	}
	fields = append(fields,
		formField{
			label: "size",
			value: func(n *config.Node) string {
				switch n.Size.Kind {
				case config.SizeFixed:
					return strconv.Itoa(int(n.Size.Value))
				case config.SizePercent:
					return strconv.FormatFloat(n.Size.Value, 'g', -1, 64) + "%"
				}
				return ""
			},
			set: func(n *config.Node, v string) error {
				if v == "" {
					n.Size = config.Size{}
					return nil
				}
				var s config.Size
				if err := s.UnmarshalYAML(&yaml.Node{Kind: yaml.ScalarNode, Value: v}); err != nil {
					return err
				}
				n.Size = s
				return nil
			},
		},
		numField("weight", func(n *config.Node) *float64 { return &n.Weight }, 1),
		intField("min", func(n *config.Node) *int { return &n.Min }),
		intField("max", func(n *config.Node) *int { return &n.Max }),
	)
	if n.Dir == config.DirNone {
		fields = append(fields, optionFields(metas[n.Widget].options, nil)...)
	}
	return &formDialog{m: m, fields: fields}
}

func numField(label string, ptr func(*config.Node) *float64, def float64) formField {
	return formField{
		label: label,
		value: func(n *config.Node) string { return strconv.FormatFloat(*ptr(n), 'g', -1, 64) },
		set: func(n *config.Node, v string) error {
			if v == "" {
				*ptr(n) = def
				return nil
			}
			f, err := strconv.ParseFloat(v, 64)
			if err != nil || f <= 0 {
				return fmt.Errorf("%s must be a positive number", label)
			}
			*ptr(n) = f
			return nil
		},
	}
}

func intField(label string, ptr func(*config.Node) *int) formField {
	return formField{
		label: label,
		value: func(n *config.Node) string {
			if *ptr(n) == 0 {
				return ""
			}
			return strconv.Itoa(*ptr(n))
		},
		set: func(n *config.Node, v string) error {
			if v == "" {
				*ptr(n) = 0
				return nil
			}
			i, err := strconv.Atoi(v)
			if err != nil || i < 0 {
				return fmt.Errorf("%s must be a non-negative integer", label)
			}
			*ptr(n) = i
			return nil
		},
	}
}

// optionFields builds fields for the exported, yaml-tagged fields of an
// options struct, recursing into nested structs ("sort.by").
func optionFields(t reflect.Type, prefix []string) []formField {
	var out []formField
	if t == nil {
		return nil
	}
	for i := 0; i < t.NumField(); i++ {
		sf := t.Field(i)
		name, _, _ := strings.Cut(sf.Tag.Get("yaml"), ",")
		if !sf.IsExported() || name == "" || name == "-" {
			continue
		}
		path := append(append([]string{}, prefix...), name)
		ft := sf.Type
		if ft.Kind() == reflect.Struct && ft != reflect.TypeFor[time.Duration]() {
			out = append(out, optionFields(ft, path)...)
			continue
		}
		out = append(out, optionField(path, ft))
	}
	return out
}

func optionField(path []string, ft reflect.Type) formField {
	label := strings.Join(path, ".")
	f := formField{
		label: label,
		value: func(n *config.Node) string {
			v, ok := getOption(n, path)
			if !ok {
				return ""
			}
			if list, ok := v.([]any); ok {
				parts := make([]string, len(list))
				for i, x := range list {
					parts[i] = fmt.Sprint(x)
				}
				return strings.Join(parts, ", ")
			}
			return fmt.Sprint(v)
		},
	}
	base := ft
	if base.Kind() == reflect.Pointer {
		base = base.Elem()
	}
	switch {
	case base.Kind() == reflect.Bool:
		f.toggle = true
		f.set = func(n *config.Node, _ string) error {
			v, ok := getOption(n, path)
			cur, _ := v.(bool)
			switch {
			case ft.Kind() == reflect.Pointer && !ok: // default → true → false → default
				return setOption(n, path, true)
			case ft.Kind() == reflect.Pointer && cur:
				return setOption(n, path, false)
			case ft.Kind() == reflect.Pointer:
				return setOption(n, path, nil)
			}
			return setOption(n, path, !cur)
		}
	case base.Kind() == reflect.Slice:
		f.set = func(n *config.Node, v string) error {
			if v == "" {
				return setOption(n, path, nil)
			}
			var list []any
			for _, p := range strings.Split(v, ",") {
				if p = strings.TrimSpace(p); p != "" {
					list = append(list, p)
				}
			}
			return setOption(n, path, list)
		}
	case base == reflect.TypeFor[time.Duration]():
		f.set = func(n *config.Node, v string) error {
			if v == "" {
				return setOption(n, path, nil)
			}
			if _, err := time.ParseDuration(v); err != nil {
				return fmt.Errorf("%s: %w", label, err)
			}
			return setOption(n, path, v)
		}
	case base.Kind() == reflect.Int, base.Kind() == reflect.Float64:
		f.set = func(n *config.Node, v string) error {
			if v == "" {
				return setOption(n, path, nil)
			}
			x, err := strconv.ParseFloat(v, 64)
			if err != nil {
				return fmt.Errorf("%s must be a number", label)
			}
			if base.Kind() == reflect.Int {
				return setOption(n, path, int(x))
			}
			return setOption(n, path, x)
		}
	default:
		f.set = func(n *config.Node, v string) error {
			if v == "" {
				return setOption(n, path, nil)
			}
			return setOption(n, path, v)
		}
	}
	return f
}

func optionMap(n *config.Node) map[string]any {
	m := map[string]any{}
	if n.Options.Kind != 0 {
		_ = n.Options.Decode(&m)
	}
	return m
}

func getOption(n *config.Node, path []string) (any, bool) {
	var cur any = optionMap(n)
	for _, p := range path {
		m, ok := cur.(map[string]any)
		if !ok {
			return nil, false
		}
		if cur, ok = m[p]; !ok {
			return nil, false
		}
	}
	return cur, true
}

// setOption sets (or with nil, removes) an option and re-encodes them.
func setOption(n *config.Node, path []string, v any) error {
	root := optionMap(n)
	m := root
	for _, p := range path[:len(path)-1] {
		next, ok := m[p].(map[string]any)
		if !ok {
			next = map[string]any{}
			m[p] = next
		}
		m = next
	}
	last := path[len(path)-1]
	if v == nil {
		delete(m, last)
	} else {
		m[last] = v
	}
	pruneEmpty(root)
	if len(root) == 0 {
		n.Options = yaml.Node{}
		return nil
	}
	var node yaml.Node
	if err := node.Encode(root); err != nil {
		return err
	}
	n.Options = node
	return nil
}

func pruneEmpty(m map[string]any) {
	for k, v := range m {
		if sub, ok := v.(map[string]any); ok {
			pruneEmpty(sub)
			if len(sub) == 0 {
				delete(m, k)
			}
		}
	}
}
