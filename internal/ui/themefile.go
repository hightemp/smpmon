package ui

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"go.yaml.in/yaml/v3"

	"github.com/hightemp/smpmon/internal/chart"
)

// ThemeFile is the YAML form of a theme. Unset fields come from the theme
// named in Inherit (default: "default").
type ThemeFile struct {
	Name      string              `yaml:"name,omitempty"`
	Inherit   string              `yaml:"inherit,omitempty"`
	Colors    map[string]string   `yaml:"colors,omitempty"`
	Boxes     map[string]string   `yaml:"boxes,omitempty"`
	Gradients map[string][]string `yaml:"gradients,omitempty"`
}

// themeColorFields maps YAML color names to Theme fields.
func themeColorFields(t *Theme) map[string]*chart.RGB {
	return map[string]*chart.RGB{
		"fg": &t.Fg, "dim": &t.Dim, "title": &t.Title, "hotkey": &t.Hotkey, "border": &t.Border,
		"selected": &t.Selected, "selected_fg": &t.SelectedFg, "meter_empty": &t.MeterEmpty,
		"warn": &t.Warn, "crit": &t.Crit, "status_bg": &t.StatusBg,
		"dialog_bg": &t.DialogBg, "dialog_fg": &t.DialogFg, "dialog_frame": &t.DialogFrame,
	}
}

func themeGradientFields(t *Theme) map[string]*chart.Gradient {
	return map[string]*chart.Gradient{
		"cpu": &t.CPU, "mem": &t.Mem, "cached": &t.Cached, "free": &t.Free, "swap": &t.Swap,
		"disk": &t.Disk, "rx": &t.NetRx, "tx": &t.NetTx, "proc": &t.Proc, "temp": &t.Temp,
	}
}

func sortedNames[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	slices.Sort(out)
	return out
}

// build resolves the file into a Theme. lookup finds the base theme.
func (f *ThemeFile) build(name string, lookup func(string) (*Theme, error)) (*Theme, error) {
	baseName := f.Inherit
	if baseName == "" {
		baseName = "default"
	}
	if baseName == name {
		return nil, fmt.Errorf("theme %q inherits from itself", name)
	}
	base, err := lookup(baseName)
	if err != nil {
		return nil, fmt.Errorf("inherit: %w", err)
	}
	t := *base
	t.Name = name
	t.Boxes = map[string]chart.RGB{}
	for k, v := range base.Boxes {
		t.Boxes[k] = v
	}
	colors := themeColorFields(&t)
	for k, v := range f.Colors {
		dst, ok := colors[k]
		if !ok {
			return nil, fmt.Errorf("colors: unknown color %q (valid: %s)", k, strings.Join(sortedNames(colors), ", "))
		}
		c, err := chart.ParseHex(v)
		if err != nil {
			return nil, fmt.Errorf("colors.%s: %w", k, err)
		}
		*dst = c
	}
	for k, v := range f.Boxes {
		c, err := chart.ParseHex(v)
		if err != nil {
			return nil, fmt.Errorf("boxes.%s: %w", k, err)
		}
		t.Boxes[k] = c
	}
	grads := themeGradientFields(&t)
	for k, stops := range f.Gradients {
		dst, ok := grads[k]
		if !ok {
			return nil, fmt.Errorf("gradients: unknown gradient %q (valid: %s)", k, strings.Join(sortedNames(grads), ", "))
		}
		if len(stops) == 0 {
			return nil, fmt.Errorf("gradients.%s: needs at least one color", k)
		}
		g := make(chart.Gradient, len(stops))
		for i, s := range stops {
			c, err := chart.ParseHex(s)
			if err != nil {
				return nil, fmt.Errorf("gradients.%s[%d]: %w", k, i, err)
			}
			g[i] = c
		}
		*dst = g
	}
	return &t, nil
}

// ExportTheme renders a theme as a complete YAML theme file.
func ExportTheme(t *Theme) ([]byte, error) {
	// The name is left out: a saved file is named after its file name, so a
	// copy of a built-in theme doesn't silently replace it.
	f := ThemeFile{Colors: map[string]string{}, Boxes: map[string]string{}, Gradients: map[string][]string{}}
	cp := *t
	for k, p := range themeColorFields(&cp) {
		f.Colors[k] = p.Hex()
	}
	for k, c := range t.Boxes {
		f.Boxes[k] = c.Hex()
	}
	for k, p := range themeGradientFields(&cp) {
		for _, c := range *p {
			f.Gradients[k] = append(f.Gradients[k], c.Hex())
		}
	}
	var buf bytes.Buffer
	buf.WriteString("# smpmon theme. Save as <config dir>/smpmon/themes/<name>.yaml.\n")
	buf.WriteString("# Exported from \"" + t.Name + "\". Remove entries you don't change and set\n")
	buf.WriteString("# `inherit` to start from another theme (default: default).\n")
	enc := yaml.NewEncoder(&buf)
	enc.SetIndent(2)
	if err := enc.Encode(f); err != nil {
		return nil, err
	}
	return buf.Bytes(), enc.Close()
}

var (
	userThemesMu sync.RWMutex
	userThemes   = map[string]*Theme{}
)

// ThemesDir returns <user config dir>/smpmon/themes.
func ThemesDir() (string, error) {
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "smpmon", "themes"), nil
}

// LoadUserThemes reads every *.yaml in dir, replacing previously loaded user
// themes. A theme's name is its `name` field or file name. Themes may inherit
// from built-in themes or from each other. A missing dir is not an error.
func LoadUserThemes(dir string) error {
	paths, err := filepath.Glob(filepath.Join(dir, "*.yaml"))
	if err != nil {
		return err
	}
	files := map[string]*ThemeFile{}
	var errs []error
	for _, p := range paths {
		data, err := os.ReadFile(p)
		if err != nil {
			errs = append(errs, err)
			continue
		}
		var f ThemeFile
		dec := yaml.NewDecoder(bytes.NewReader(data))
		dec.KnownFields(true)
		if err := dec.Decode(&f); err != nil && len(bytes.TrimSpace(data)) > 0 {
			errs = append(errs, fmt.Errorf("%s: %w", p, err))
			continue
		}
		name := f.Name
		if name == "" {
			name = strings.TrimSuffix(filepath.Base(p), ".yaml")
		}
		files[name] = &f
	}

	built := map[string]*Theme{}
	var resolve func(name string, depth int) (*Theme, error)
	resolve = func(name string, depth int) (*Theme, error) {
		if t, ok := built[name]; ok {
			return t, nil
		}
		f, ok := files[name]
		if !ok {
			if t, ok := themes[name]; ok {
				return t, nil
			}
			return nil, fmt.Errorf("unknown theme %q", name)
		}
		if depth > 10 {
			return nil, errors.New("inheritance chain too long or cyclic")
		}
		t, err := f.build(name, func(base string) (*Theme, error) { return resolve(base, depth+1) })
		if err != nil {
			return nil, fmt.Errorf("theme %q: %w", name, err)
		}
		built[name] = t
		return t, nil
	}
	for _, name := range sortedNames(files) {
		if _, err := resolve(name, 0); err != nil {
			errs = append(errs, err)
		}
	}
	userThemesMu.Lock()
	userThemes = built
	userThemesMu.Unlock()
	return errors.Join(errs...)
}
