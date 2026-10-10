package config

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

// Clone returns a deep copy of the node tree.
func (n *Node) Clone() *Node {
	if n == nil {
		return nil
	}
	cp := *n
	cp.Options = cloneYAML(n.Options)
	cp.Children = make([]*Node, len(n.Children))
	for i, c := range n.Children {
		cp.Children[i] = c.Clone()
	}
	return &cp
}

func cloneYAML(n yaml.Node) yaml.Node {
	cp := n
	if n.Content != nil {
		cp.Content = make([]*yaml.Node, len(n.Content))
		for i, c := range n.Content {
			cc := cloneYAML(*c)
			cp.Content[i] = &cc
		}
	}
	return cp
}

// At returns the node at path (child indexes from n), or nil.
func (n *Node) At(path []int) *Node {
	cur := n
	for _, i := range path {
		if cur == nil || i < 0 || i >= len(cur.Children) {
			return nil
		}
		cur = cur.Children[i]
	}
	return cur
}

// Clone returns a deep copy of the dashboard.
func (d *Dashboard) Clone() *Dashboard {
	cp := &Dashboard{Title: d.Title, Layout: d.Layout.Clone()}
	for _, v := range d.Variants {
		cp.Variants = append(cp.Variants, Variant{When: v.When, Layout: v.Layout.Clone()})
	}
	return cp
}

// SaveDashboard writes one dashboard into the user config file at path,
// creating the file if needed. Only `dashboards.<name>` (and
// `general.dashboards` when tabs is non-nil) are replaced; the rest of the
// file, including comments, is kept. The previous file is saved as
// <path>.bak.
func SaveDashboard(path, name string, db *Dashboard, tabs []string) error {
	var doc yaml.Node
	old, err := os.ReadFile(path)
	switch {
	case err == nil:
		if err := yaml.Unmarshal(old, &doc); err != nil {
			return fmt.Errorf("%s: %w", path, err)
		}
	case errors.Is(err, os.ErrNotExist):
	default:
		return err
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		doc = yaml.Node{Kind: yaml.DocumentNode, Content: []*yaml.Node{{Kind: yaml.MappingNode}}}
		setKey(doc.Content[0], "version", &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!int", Value: fmt.Sprint(CurrentVersion)})
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return fmt.Errorf("%s: config must be a mapping", path)
	}

	var dbNode yaml.Node
	if err := dbNode.Encode(db); err != nil {
		return err
	}
	dashboards := child(root, "dashboards")
	if dashboards == nil || dashboards.Kind != yaml.MappingNode {
		dashboards = &yaml.Node{Kind: yaml.MappingNode}
		setKey(root, "dashboards", dashboards)
	}
	setKey(dashboards, name, &dbNode)

	if tabs != nil {
		general := child(root, "general")
		if general == nil || general.Kind != yaml.MappingNode {
			general = &yaml.Node{Kind: yaml.MappingNode}
			setKey(root, "general", general)
		}
		seq := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
		for _, t := range tabs {
			seq.Content = append(seq.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: t})
		}
		setKey(general, "dashboards", seq)
	}

	var buf bytes.Buffer
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(&doc); err != nil {
		return err
	}
	if err := enc.Close(); err != nil {
		return err
	}
	// The result must load back; never write a config we can't read.
	if _, err := Parse(buf.Bytes()); err != nil {
		return fmt.Errorf("generated config is invalid: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	if old != nil {
		if err := os.WriteFile(path+".bak", old, 0o644); err != nil {
			return fmt.Errorf("backup: %w", err)
		}
	}
	// Write to a temporary file and rename so a crash can't leave a
	// truncated config behind.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf.Bytes(), 0o644); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func child(m *yaml.Node, key string) *yaml.Node {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			return m.Content[i+1]
		}
	}
	return nil
}

func setKey(m *yaml.Node, key string, v *yaml.Node) {
	for i := 0; i+1 < len(m.Content); i += 2 {
		if m.Content[i].Value == key {
			// Keep the comments attached to the old value.
			v.HeadComment, v.LineComment, v.FootComment = m.Content[i+1].HeadComment, m.Content[i+1].LineComment, m.Content[i+1].FootComment
			m.Content[i+1] = v
			return
		}
	}
	m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: key}, v)
}
