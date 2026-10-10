package keymap

import (
	"strings"
	"testing"
)

func TestDefaultsAndPresetsValid(t *testing.T) {
	for _, p := range PresetNames() {
		m, err := Build(p, nil)
		if err != nil {
			t.Fatalf("%s: %v", p, err)
		}
		for _, i := range Actions {
			for _, k := range m.Keys(i.Action) {
				if err := ValidKey(k); err != nil {
					t.Errorf("%s/%s: %v", p, i.Action, err)
				}
			}
		}
	}
	if _, ok := defaults()["nonexistent"]; ok {
		t.Fatal("unexpected")
	}
	for a := range defaults() {
		if _, ok := info(a); !ok {
			t.Errorf("default for unknown action %s", a)
		}
	}
}

func TestMatch(t *testing.T) {
	m, _ := Build("default", nil)
	if a, ok := m.Match("k", Nav, Proc, Global); !ok || a != "proc_kill" {
		t.Fatalf("k -> %v %v", a, ok)
	}
	if a, _ := m.Match("q", Nav, Proc, Global); a != "quit" {
		t.Fatalf("q -> %v", a)
	}
	if _, ok := m.Match("k", Nav, Net, Global); ok {
		t.Fatal("proc key active in net scope")
	}

	vim, _ := Build("vim", nil)
	if a, _ := vim.Match("k", Nav, Proc, Global); a != "up" {
		t.Fatalf("vim k -> %v", a)
	}
	if a, _ := vim.Match("x", Nav, Proc, Global); a != "proc_kill" {
		t.Fatalf("vim x -> %v", a)
	}
	if vim.First("graph_mode") != "M" {
		t.Fatal("vim graph mode")
	}
}

func TestOverrides(t *testing.T) {
	m, err := Build("default", map[string][]string{"quit": {"ctrl+q"}, "pause": {}})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := m.Match("q", Global); ok {
		t.Fatal("q still quits")
	}
	if a, _ := m.Match("ctrl+q", Global); a != "quit" {
		t.Fatal("ctrl+q")
	}
	if _, ok := m.Match("p", Global); ok {
		t.Fatal("pause not unbound")
	}

	cases := map[string]map[string][]string{
		"unknown action": {"explode": {"e"}},
		"bad key":        {"quit": {"pagedown"}},
		"bad modifier":   {"quit": {"hyper+q"}},
		"conflict":       {"proc_tree": {"k"}},
		"global clash":   {"pause": {"q"}},
	}
	for name, ov := range cases {
		if _, err := Build("default", ov); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
	_, err = Build("default", map[string][]string{"proc_tree": {"k"}})
	if !strings.Contains(err.Error(), `"k"`) || !strings.Contains(err.Error(), "proc_kill") {
		t.Fatalf("conflict message: %v", err)
	}
	// A widget key may shadow a global key: the widget wins while focused.
	if _, err := Build("default", map[string][]string{"proc_tree": {"z"}}); err != nil {
		t.Fatalf("shadowing a global key should be allowed: %v", err)
	}
	if _, err := Build("emacs", nil); err == nil {
		t.Fatal("unknown preset accepted")
	}
}
