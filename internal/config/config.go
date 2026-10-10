// Package config loads, merges and validates the smpmon YAML configuration.
package config

import (
	_ "embed"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"time"

	"go.yaml.in/yaml/v3"
)

// CurrentVersion is the config schema version written by this build.
const CurrentVersion = 1

// MinInterval is the smallest allowed collection interval.
const MinInterval = 100 * time.Millisecond

//go:embed default.yaml
var defaultYAML []byte

// DefaultYAML returns the built-in default configuration, including comments.
func DefaultYAML() []byte { return slices.Clone(defaultYAML) }

// Config is the full configuration.
type Config struct {
	Version    int                        `yaml:"version"`
	General    General                    `yaml:"general"`
	Collectors map[string]CollectorConfig `yaml:"collectors"`
	Dashboards Dashboards                 `yaml:"dashboards"`
}

// General holds global UI and collection settings.
type General struct {
	Interval  time.Duration `yaml:"interval"`
	GraphMode string        `yaml:"graph_mode"`
	Theme     string        `yaml:"theme"`
	Border    string        `yaml:"border"`
	Mouse     bool          `yaml:"mouse"`
	// Dashboards lists the dashboards shown as tabs, in order. Empty means
	// all of them in definition order.
	Dashboards       []string `yaml:"dashboards"`
	DefaultDashboard string   `yaml:"default_dashboard"`
}

// CollectorConfig tunes one collector.
type CollectorConfig struct {
	Interval time.Duration `yaml:"interval,omitempty"`
	Enabled  *bool         `yaml:"enabled,omitempty"`
}

// Dashboard is a named layout.
type Dashboard struct {
	Title    string    `yaml:"title"`
	Layout   *Node     `yaml:"layout"`
	Variants []Variant `yaml:"variants,omitempty"`
}

// Variant is an alternative layout used when the terminal size matches.
type Variant struct {
	When   Breakpoint `yaml:"when"`
	Layout *Node      `yaml:"layout"`
}

// Breakpoint matches terminal sizes. Zero bounds are ignored.
type Breakpoint struct {
	MinWidth  int `yaml:"min_width"`
	MaxWidth  int `yaml:"max_width"`
	MinHeight int `yaml:"min_height"`
	MaxHeight int `yaml:"max_height"`
}

// Matches reports whether a w×h terminal satisfies the breakpoint.
func (b Breakpoint) Matches(w, h int) bool {
	return (b.MinWidth == 0 || w >= b.MinWidth) && (b.MaxWidth == 0 || w <= b.MaxWidth) &&
		(b.MinHeight == 0 || h >= b.MinHeight) && (b.MaxHeight == 0 || h <= b.MaxHeight)
}

// LayoutFor returns the layout of the first matching variant, or the default.
func (d *Dashboard) LayoutFor(w, h int) *Node {
	for _, v := range d.Variants {
		if v.When.Matches(w, h) {
			return v.Layout
		}
	}
	return d.Layout
}

// Dashboards is an ordered set of named dashboards.
type Dashboards struct {
	Names  []string
	ByName map[string]*Dashboard
}

// Get returns the named dashboard.
func (d *Dashboards) Get(name string) (*Dashboard, bool) {
	db, ok := d.ByName[name]
	return db, ok
}

// Set adds or replaces a dashboard, keeping the position of an existing one.
func (d *Dashboards) Set(name string, db *Dashboard) {
	if d.ByName == nil {
		d.ByName = map[string]*Dashboard{}
	}
	if _, ok := d.ByName[name]; !ok {
		d.Names = append(d.Names, name)
	}
	d.ByName[name] = db
}

// UnmarshalYAML implements yaml.Unmarshaler, preserving definition order.
func (d *Dashboards) UnmarshalYAML(n *yaml.Node) error {
	if n.Kind != yaml.MappingNode {
		return fmt.Errorf("line %d: dashboards must be a mapping of name to dashboard", n.Line)
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		name := n.Content[i].Value
		var db Dashboard
		if err := decodeStrict(n.Content[i+1], &db); err != nil {
			return fmt.Errorf("dashboard %q: %w", name, err)
		}
		if db.Layout == nil {
			return fmt.Errorf("line %d: dashboard %q has no layout", n.Content[i].Line, name)
		}
		for j, v := range db.Variants {
			if v.Layout == nil {
				return fmt.Errorf("dashboard %q: variant %d has no layout", name, j+1)
			}
		}
		d.Set(name, &db)
	}
	return nil
}

// MarshalYAML implements yaml.Marshaler.
func (d Dashboards) MarshalYAML() (any, error) {
	m := &yaml.Node{Kind: yaml.MappingNode}
	for _, name := range d.Names {
		var v yaml.Node
		if err := v.Encode(d.ByName[name]); err != nil {
			return nil, err
		}
		m.Content = append(m.Content, &yaml.Node{Kind: yaml.ScalarNode, Value: name}, &v)
	}
	return m, nil
}

// Tabs returns the dashboards to show, in order.
func (c *Config) Tabs() []string {
	if len(c.General.Dashboards) > 0 {
		return c.General.Dashboards
	}
	return c.Dashboards.Names
}

// CollectorEnabled reports whether a collector is enabled (default true).
func (c *Config) CollectorEnabled(name string) bool {
	cc, ok := c.Collectors[name]
	return !ok || cc.Enabled == nil || *cc.Enabled
}

// CollectorIntervals returns per-collector interval overrides.
func (c *Config) CollectorIntervals() map[string]time.Duration {
	out := map[string]time.Duration{}
	for name, cc := range c.Collectors {
		if cc.Interval > 0 {
			out[name] = cc.Interval
		}
	}
	return out
}

// DefaultPath returns <user config dir>/smpmon/config.yaml.
func DefaultPath() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "smpmon", "config.yaml"), nil
}

// Default returns the built-in configuration.
func Default() *Config {
	cfg, err := parse(defaultYAML, nil)
	if err != nil {
		panic(fmt.Sprintf("config: built-in default is invalid: %v", err))
	}
	return cfg
}

// Load reads the config at path merged over the defaults. A missing file is
// not an error when allowMissing is set (used for the default path).
func Load(path string, allowMissing bool) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if allowMissing && errors.Is(err, os.ErrNotExist) {
			return Default(), nil
		}
		return nil, err
	}
	cfg, err := Parse(data)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return cfg, nil
}

// Parse parses user config data merged over the defaults.
func Parse(data []byte) (*Config, error) {
	return parse(data, Default())
}

// parse decodes data on top of base (nil base means start from zero).
//
// Merge rules: scalar settings in `general` override individual defaults,
// `collectors` entries replace defaults per collector, and dashboards with
// the same name replace the default one while new names are appended.
func parse(data []byte, base *Config) (*Config, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	cfg := base
	if cfg == nil {
		cfg = &Config{}
	}
	if cfg.Collectors == nil {
		cfg.Collectors = map[string]CollectorConfig{}
	}
	if doc.Kind == 0 || len(doc.Content) == 0 {
		return cfg, nil // empty file
	}
	root := doc.Content[0]
	if root.Kind != yaml.MappingNode {
		return nil, fmt.Errorf("line %d: config must be a mapping", root.Line)
	}
	if err := checkKnownFields(root, &Config{}); err != nil {
		return nil, err
	}
	for i := 0; i+1 < len(root.Content); i += 2 {
		key, val := root.Content[i].Value, root.Content[i+1]
		var err error
		switch key {
		case "version":
			err = val.Decode(&cfg.Version)
		case "general":
			err = val.Decode(&cfg.General)
		case "collectors":
			err = val.Decode(&cfg.Collectors)
		case "dashboards":
			var ds Dashboards
			if err = val.Decode(&ds); err == nil {
				for _, name := range ds.Names {
					cfg.Dashboards.Set(name, ds.ByName[name])
				}
			}
		}
		if err != nil {
			return nil, fmt.Errorf("%s: %w", key, err)
		}
	}
	if cfg.Version > CurrentVersion {
		return nil, fmt.Errorf("config version %d is newer than supported (%d); upgrade smpmon", cfg.Version, CurrentVersion)
	}
	return cfg, nil
}
