package config

import (
	"fmt"
	"reflect"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"
)

var (
	unmarshalerType = reflect.TypeFor[yaml.Unmarshaler]()
	yamlNodeType    = reflect.TypeFor[yaml.Node]()
)

// checkKnownFields reports mapping keys in n that have no matching field in
// v's type, recursing into nested structs, maps and slices. yaml.Node.Decode
// has no strict mode, and unknown keys are almost always typos in a
// hand-written config, so they are worth a precise error with a line number.
func checkKnownFields(n *yaml.Node, v any) error {
	return checkType(n, reflect.TypeOf(v))
}

func checkType(n *yaml.Node, t reflect.Type) error {
	if n == nil {
		return nil
	}
	if n.Kind == yaml.DocumentNode && len(n.Content) > 0 {
		n = n.Content[0]
	}
	if n.Kind == yaml.AliasNode {
		n = n.Alias
	}
	for t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	if t == yamlNodeType || reflect.PointerTo(t).Implements(unmarshalerType) {
		return nil // custom decoders validate themselves
	}
	switch t.Kind() {
	case reflect.Struct:
		if n.Kind != yaml.MappingNode {
			return nil
		}
		fields := yamlFields(t)
		for i := 0; i+1 < len(n.Content); i += 2 {
			k, val := n.Content[i], n.Content[i+1]
			ft, ok := fields[k.Value]
			if !ok {
				return fmt.Errorf("line %d: %w %q (valid: %s)", k.Line, errUnknownField, k.Value, strings.Join(sortedKeys(fields), ", "))
			}
			if err := checkType(val, ft); err != nil {
				return err
			}
		}
	case reflect.Map:
		if n.Kind != yaml.MappingNode {
			return nil
		}
		for i := 1; i < len(n.Content); i += 2 {
			if err := checkType(n.Content[i], t.Elem()); err != nil {
				return err
			}
		}
	case reflect.Slice:
		if n.Kind != yaml.SequenceNode {
			return nil
		}
		for _, c := range n.Content {
			if err := checkType(c, t.Elem()); err != nil {
				return err
			}
		}
	}
	return nil
}

// yamlFields maps YAML keys to field types, flattening inline structs.
func yamlFields(t reflect.Type) map[string]reflect.Type {
	out := map[string]reflect.Type{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		tag := f.Tag.Get("yaml")
		if tag == "-" {
			continue
		}
		name, opts, _ := strings.Cut(tag, ",")
		if strings.Contains(opts, "inline") {
			for k, v := range yamlFields(f.Type) {
				out[k] = v
			}
			continue
		}
		if name == "" {
			name = strings.ToLower(f.Name)
		}
		out[name] = f.Type
	}
	return out
}

func sortedKeys(m map[string]reflect.Type) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	slices.Sort(keys)
	return keys
}
