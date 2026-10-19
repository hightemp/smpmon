package config

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"go.yaml.in/yaml/v3"
)

func TestDefaultIsValid(t *testing.T) {
	cfg := Default()
	if err := cfg.Validate(nil); err != nil {
		t.Fatal(err)
	}
	if cfg.General.Interval != time.Second || cfg.General.GraphMode != "braille" {
		t.Fatalf("general %+v", cfg.General)
	}
	if got := cfg.Tabs(); len(got) != 8 || got[0] != "btop" {
		t.Fatalf("tabs %v", got)
	}
	if cfg.CollectorIntervals()["procs"] != 2*time.Second {
		t.Fatalf("collector intervals %v", cfg.CollectorIntervals())
	}
}

func TestMergeOverDefaults(t *testing.T) {
	cfg, err := Parse([]byte(`
general:
  interval: 500ms
  dashboards: [mine, btop]
  default_dashboard: mine
collectors:
  net: {enabled: false}
dashboards:
  mine:
    layout:
      row:
        - widget: cpu
          size: 30%
        - widget: mem
          options: {foo: 1}
  btop:
    layout: {widget: proc}
`))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(nil); err != nil {
		t.Fatal(err)
	}
	g := cfg.General
	if g.Interval != 500*time.Millisecond || g.GraphMode != "braille" || !g.Mouse {
		t.Fatalf("general not merged: %+v", g)
	}
	if cfg.CollectorEnabled("net") || !cfg.CollectorEnabled("cpu") {
		t.Fatal("collector enabled flags")
	}
	if _, ok := cfg.Collectors["procs"]; !ok {
		t.Fatal("default collector settings lost")
	}
	if db, _ := cfg.Dashboards.Get("btop"); db.Layout.Widget != "proc" {
		t.Fatal("btop not replaced")
	}
	if names := cfg.Dashboards.Names; names[0] != "btop" || names[len(names)-1] != "mine" {
		t.Fatalf("order %v", names)
	}
	mine, _ := cfg.Dashboards.Get("mine")
	if mine.Layout.Dir != DirRow || len(mine.Layout.Children) != 2 {
		t.Fatalf("mine layout %+v", mine.Layout)
	}
	c0 := mine.Layout.Children[0]
	if c0.Size.Kind != SizePercent || c0.Size.Value != 30 || c0.Weight != 1 {
		t.Fatalf("child %+v", c0)
	}
	var opts struct {
		Foo int `yaml:"foo"`
	}
	if err := mine.Layout.Children[1].DecodeOptions(&opts); err != nil || opts.Foo != 1 {
		t.Fatalf("options %v %v", opts, err)
	}
	var strict struct {
		Bar int `yaml:"bar"`
	}
	if err := mine.Layout.Children[1].DecodeOptions(&strict); err == nil {
		t.Fatal("unknown option accepted")
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		"unknown top":      "colours: 1\n",
		"unknown general":  "general:\n  intreval: 1s\n",
		"unknown node key": "dashboards:\n  x:\n    layout:\n      widget: cpu\n      sise: 3\n",
		"two kinds":        "dashboards:\n  x:\n    layout:\n      widget: cpu\n      row: [{widget: mem}]\n",
		"empty container":  "dashboards:\n  x:\n    layout:\n      row: []\n",
		"bad size":         "dashboards:\n  x:\n    layout:\n      row:\n        - widget: cpu\n          size: big\n",
		"bad percent":      "dashboards:\n  x:\n    layout:\n      row:\n        - widget: cpu\n          size: 120%\n",
		"min>max":          "dashboards:\n  x:\n    layout:\n      row:\n        - widget: cpu\n          min: 5\n          max: 2\n",
		"no layout":        "dashboards:\n  x:\n    title: X\n",
		"future version":   "version: 99\n",
		"not a mapping":    "- a\n",
	}
	for name, in := range cases {
		if _, err := Parse([]byte(in)); err == nil {
			t.Errorf("%s: expected error", name)
		}
	}
	_, err := Parse([]byte("general:\n  mouse: true\n  intreval: 1s\n"))
	if err == nil || !strings.Contains(err.Error(), "line 3") || !strings.Contains(err.Error(), "intreval") {
		t.Fatalf("error lacks line/field: %v", err)
	}
}

func TestValidate(t *testing.T) {
	cfg, err := Parse([]byte(`
general:
  interval: 10ms
  graph_mode: dots
  border: wavy
  dashboards: [nope]
dashboards:
  x:
    layout: {widget: bogus}
`))
	if err != nil {
		t.Fatal(err)
	}
	err = cfg.Validate(func(w string) bool { return w != "bogus" })
	if err == nil {
		t.Fatal("expected errors")
	}
	for _, want := range []string{"interval", "graph_mode", "border", `unknown dashboard "nope"`, `unknown widget "bogus"`} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("missing %q in %v", want, err)
		}
	}
}

func TestVariants(t *testing.T) {
	db, _ := Default().Dashboards.Get("btop")
	if db.LayoutFor(80, 40) == db.Layout {
		t.Fatal("narrow variant not used")
	}
	if db.LayoutFor(200, 50) != db.Layout {
		t.Fatal("default layout not used")
	}
}

func TestRoundTrip(t *testing.T) {
	cfg := Default()
	out, err := yaml.Marshal(cfg)
	if err != nil {
		t.Fatal(err)
	}
	back, err := parse(out, nil)
	if err != nil {
		t.Fatalf("%v\n%s", err, out)
	}
	if err := back.Validate(nil); err != nil {
		t.Fatal(err)
	}
	if len(back.Dashboards.Names) != len(cfg.Dashboards.Names) {
		t.Fatalf("dashboards lost: %v", back.Dashboards.Names)
	}
	db, _ := back.Dashboards.Get("btop")
	if db.Layout.Children[0].Size != (Size{Kind: SizePercent, Value: 35}) {
		t.Fatalf("size lost: %+v", db.Layout.Children[0].Size)
	}
}

func TestLoad(t *testing.T) {
	dir := t.TempDir()
	if _, err := Load(filepath.Join(dir, "missing.yaml"), true); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(filepath.Join(dir, "missing.yaml"), false); err == nil {
		t.Fatal("missing file accepted")
	}
	p := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(p, []byte("general: {theme: nord}\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(p, false)
	if err != nil || cfg.General.Theme != "nord" {
		t.Fatalf("%v %v", cfg, err)
	}
}

func TestWatch(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "c.yaml")
	if err := os.WriteFile(p, []byte("a: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	changed := make(chan struct{}, 10)
	if err := Watch(ctx, p, func() { changed <- struct{}{} }); err != nil {
		t.Fatal(err)
	}
	// Unrelated files are ignored; atomic replace via rename is seen.
	_ = os.WriteFile(filepath.Join(dir, "other"), []byte("x"), 0o644)
	tmp := filepath.Join(dir, "c.yaml.tmp")
	_ = os.WriteFile(tmp, []byte("a: 2\n"), 0o644)
	if err := os.Rename(tmp, p); err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	case <-time.After(3 * time.Second):
		t.Fatal("no change notification")
	}
}

func TestCloneAndAt(t *testing.T) {
	db, _ := Default().Dashboards.Get("server")
	cp := db.Clone()
	if cp.Layout == db.Layout || cp.Layout.Children[0] == db.Layout.Children[0] {
		t.Fatal("clone shares nodes")
	}
	g := cp.Layout.At([]int{0, 1})
	if g == nil || g.Widget != "graph" {
		t.Fatalf("At: %+v", g)
	}
	g.Options.Content[1].Value = "changed"
	if db.Layout.At([]int{0, 1}).Options.Content[1].Value == "changed" {
		t.Fatal("clone shares options")
	}
	if cp.Layout.At([]int{9}) != nil {
		t.Fatal("At out of range")
	}
}

func TestSaveDashboard(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sub", "config.yaml")
	db := &Dashboard{Title: "Mine", Layout: &Node{Dir: DirRow, Weight: 1, Children: []*Node{
		{Widget: "cpu", Weight: 2}, {Widget: "mem", Weight: 1, Size: Size{Kind: SizePercent, Value: 30}},
	}}}
	// New file.
	if err := SaveDashboard(path, "mine", db, []string{"btop", "mine"}); err != nil {
		t.Fatal(err)
	}
	cfg, err := Load(path, false)
	if err != nil {
		t.Fatal(err)
	}
	got, ok := cfg.Dashboards.Get("mine")
	if !ok || got.Title != "Mine" || got.Layout.Children[1].Size.Value != 30 || got.Layout.Children[0].Weight != 2 {
		t.Fatalf("saved %+v", got)
	}
	if tabs := cfg.Tabs(); len(tabs) != 2 || tabs[1] != "mine" {
		t.Fatalf("tabs %v", tabs)
	}

	// Existing file: comments and other settings survive, a backup is made.
	orig := "# my config\ngeneral:\n  theme: nord # favourite\ndashboards:\n  other:\n    layout: {widget: proc}\n"
	if err := os.WriteFile(path, []byte(orig), 0o644); err != nil {
		t.Fatal(err)
	}
	db.Title = "Mine 2"
	if err := SaveDashboard(path, "mine", db, nil); err != nil {
		t.Fatal(err)
	}
	data, _ := os.ReadFile(path)
	for _, want := range []string{"# my config", "# favourite", "theme: nord", "other:", "Mine 2"} {
		if !strings.Contains(string(data), want) {
			t.Errorf("missing %q in:\n%s", want, data)
		}
	}
	if bak, _ := os.ReadFile(path + ".bak"); string(bak) != orig {
		t.Fatal("backup differs")
	}
	if _, err := Load(path, false); err != nil {
		t.Fatal(err)
	}
}

func TestHosts(t *testing.T) {
	cfg, err := Parse([]byte("hosts:\n  web1: {ssh: deploy@web1}\n  edge: {url: 'tls://edge:9200', token_file: /x}\n"))
	if err != nil {
		t.Fatal(err)
	}
	if err := cfg.Validate(nil); err != nil {
		t.Fatal(err)
	}
	if names := cfg.HostNames(); len(names) != 2 || names[0] != "edge" {
		t.Fatalf("names %v", names)
	}
	for _, bad := range []string{
		"hosts:\n  local: {ssh: x}\n",
		"hosts:\n  a: {}\n",
		"hosts:\n  a: {ssh: x, url: 'tls://y'}\n",
		"hosts:\n  a: {url: 'http://y', token_file: /x}\n",
		"hosts:\n  a: {url: 'tls://y'}\n",
	} {
		cfg, err := Parse([]byte(bad))
		if err == nil {
			err = cfg.Validate(nil)
		}
		if err == nil {
			t.Errorf("accepted %q", bad)
		}
	}
	if _, err := Parse([]byte("hosts:\n  a: {sshh: x}\n")); err == nil {
		t.Error("unknown host field accepted")
	}
}
