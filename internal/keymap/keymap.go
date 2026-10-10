// Package keymap maps key names to UI actions. Keys are Bubble Tea key
// strings such as "q", "ctrl+c", "shift+tab", "pgdown" or "space".
package keymap

import (
	"errors"
	"fmt"
	"slices"
	"strings"
)

// Action identifies something the user can do.
type Action string

// Scope groups actions that are active at the same time. Keys must be unique
// within a scope; widget scopes take precedence over the global scope.
type Scope string

// Scopes.
const (
	Global Scope = "global"
	Nav    Scope = "nav" // list navigation shared by list widgets
	Proc   Scope = "proc"
	Net    Scope = "net"
	Docker Scope = "docker"
	Editor Scope = "editor" // layout editor
)

// ActionInfo describes an action.
type ActionInfo struct {
	Action Action
	Scope  Scope
	Help   string
}

// Actions lists every action in help order.
var Actions = []ActionInfo{
	{"quit", Global, "quit"},
	{"help", Global, "help"},
	{"focus_next", Global, "focus next widget"},
	{"focus_prev", Global, "focus previous widget"},
	{"zoom", Global, "zoom the focused widget"},
	{"back", Global, "close zoom"},
	{"pause", Global, "pause the display"},
	{"interval_up", Global, "update less often"},
	{"interval_down", Global, "update more often"},
	{"graph_mode", Global, "cycle graph mode"},
	{"theme", Global, "choose theme"},
	{"edit", Global, "edit the dashboard layout"},
	{"host", Global, "choose the host to show"},
	{"dashboard_1", Global, "dashboard 1"}, {"dashboard_2", Global, "dashboard 2"},
	{"dashboard_3", Global, "dashboard 3"}, {"dashboard_4", Global, "dashboard 4"},
	{"dashboard_5", Global, "dashboard 5"}, {"dashboard_6", Global, "dashboard 6"},
	{"dashboard_7", Global, "dashboard 7"}, {"dashboard_8", Global, "dashboard 8"},
	{"dashboard_9", Global, "dashboard 9"},

	{"up", Nav, "move up"},
	{"down", Nav, "move down"},
	{"page_up", Nav, "page up"},
	{"page_down", Nav, "page down"},
	{"home", Nav, "first row"},
	{"end", Nav, "last row"},
	{"left", Nav, "previous column / item"},
	{"right", Nav, "next column / item"},

	{"proc_filter", Proc, "filter"},
	{"proc_reverse", Proc, "reverse sort order"},
	{"proc_tree", Proc, "tree view"},
	{"proc_collapse", Proc, "collapse or expand a branch"},
	{"proc_details", Proc, "details panel"},
	{"proc_kill", Proc, "terminate (SIGTERM)"},
	{"proc_kill_force", Proc, "kill (SIGKILL)"},
	{"proc_signal", Proc, "choose a signal"},
	{"proc_renice", Proc, "renice"},
	{"proc_per_core", Proc, "CPU per core / of machine"},

	{"net_prev", Net, "previous interface"},
	{"net_next", Net, "next interface"},

	{"docker_all", Docker, "show stopped containers"},

	{"edit_parent", Editor, "select the parent container"},
	{"edit_child", Editor, "select the first child"},
	{"edit_split_h", Editor, "split: add a widget to the right"},
	{"edit_split_v", Editor, "split: add a widget below"},
	{"edit_add", Editor, "add a widget after the selection"},
	{"edit_delete", Editor, "delete the selection"},
	{"edit_move_prev", Editor, "move before the previous sibling"},
	{"edit_move_next", Editor, "move after the next sibling"},
	{"edit_grow", Editor, "make bigger"},
	{"edit_shrink", Editor, "make smaller"},
	{"edit_rotate", Editor, "switch row / column"},
	{"edit_type", Editor, "change the widget type"},
	{"edit_options", Editor, "edit title, size and options"},
	{"edit_undo", Editor, "undo"},
	{"edit_redo", Editor, "redo"},
	{"edit_save", Editor, "save to the config file"},
	{"edit_new", Editor, "new dashboard"},
	{"edit_title", Editor, "rename the dashboard"},
}

func defaults() map[Action][]string {
	return map[Action][]string{
		"quit": {"q", "ctrl+c"}, "help": {"?", "h", "f1"},
		"focus_next": {"tab"}, "focus_prev": {"shift+tab"},
		"zoom": {"z"}, "back": {"esc"}, "pause": {"p"},
		"interval_up": {"+", "="}, "interval_down": {"-", "_"},
		"graph_mode": {"g"}, "theme": {"T"},
		"dashboard_1": {"1"}, "dashboard_2": {"2"}, "dashboard_3": {"3"}, "dashboard_4": {"4"},
		"dashboard_5": {"5"}, "dashboard_6": {"6"}, "dashboard_7": {"7"}, "dashboard_8": {"8"},
		"dashboard_9": {"9"},

		"up": {"up"}, "down": {"down"}, "page_up": {"pgup"}, "page_down": {"pgdown"},
		"home": {"home"}, "end": {"end"}, "left": {"left"}, "right": {"right"},

		"proc_filter": {"f", "/"}, "proc_reverse": {"r"}, "proc_tree": {"t"},
		"proc_collapse": {"space"}, "proc_details": {"enter"},
		"proc_kill": {"k"}, "proc_kill_force": {"K"}, "proc_signal": {"s"},
		"proc_renice": {"i"}, "proc_per_core": {"c"},

		"net_prev": {"b"}, "net_next": {"n"},
		"docker_all": {"a"},
		"edit":       {"e"},
		"host":       {"H"},

		"edit_parent": {"["}, "edit_child": {"]"},
		"edit_split_h": {"s"}, "edit_split_v": {"v"}, "edit_add": {"a"},
		"edit_delete":    {"d", "delete"},
		"edit_move_prev": {"<", ","}, "edit_move_next": {">", "."},
		"edit_grow": {"+", "="}, "edit_shrink": {"-", "_"},
		"edit_rotate": {"r"}, "edit_type": {"t"}, "edit_options": {"o", "enter"},
		"edit_undo": {"u", "ctrl+z"}, "edit_redo": {"U", "ctrl+y"},
		"edit_save": {"ctrl+s"}, "edit_new": {"n"}, "edit_title": {"R"},
	}
}

// presets modify the defaults.
var presets = map[string]map[Action][]string{
	"default": {},
	// vim: hjkl navigation; actions on those letters move elsewhere.
	"vim": {
		"help":            {"?", "f1"},
		"up":              {"up", "k"},
		"down":            {"down", "j"},
		"left":            {"left", "h"},
		"right":           {"right", "l"},
		"page_up":         {"pgup", "ctrl+u"},
		"page_down":       {"pgdown", "ctrl+d"},
		"home":            {"home", "g"},
		"end":             {"end", "G"},
		"graph_mode":      {"M"},
		"proc_kill":       {"x"},
		"proc_kill_force": {"X"},
	},
}

// PresetNames lists the presets.
func PresetNames() []string {
	names := make([]string, 0, len(presets))
	for n := range presets {
		names = append(names, n)
	}
	slices.Sort(names)
	return names
}

// Keymap resolves keys to actions.
type Keymap struct {
	keys   map[Action][]string
	byKey  map[Scope]map[string]Action
	scopes map[Action]Scope
}

func known(a Action) bool {
	for _, i := range Actions {
		if i.Action == a {
			return true
		}
	}
	return false
}

// Build creates a keymap from a preset and user overrides. An override
// replaces all keys of its action; an empty list unbinds it.
func Build(preset string, overrides map[string][]string) (*Keymap, error) {
	if preset == "" {
		preset = "default"
	}
	p, ok := presets[preset]
	if !ok {
		return nil, fmt.Errorf("unknown key preset %q (available: %v)", preset, PresetNames())
	}
	keys := defaults()
	for a, k := range p {
		keys[a] = k
	}
	var errs []error
	for name, k := range overrides {
		a := Action(name)
		if !known(a) {
			errs = append(errs, fmt.Errorf("unknown action %q", name))
			continue
		}
		for _, key := range k {
			if err := ValidKey(key); err != nil {
				errs = append(errs, fmt.Errorf("%s: %w", name, err))
			}
		}
		keys[a] = k
	}
	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}
	m := &Keymap{keys: keys, byKey: map[Scope]map[string]Action{}, scopes: map[Action]Scope{}}
	for _, i := range Actions {
		m.scopes[i.Action] = i.Scope
		if m.byKey[i.Scope] == nil {
			m.byKey[i.Scope] = map[string]Action{}
		}
		for _, k := range keys[i.Action] {
			m.byKey[i.Scope][k] = i.Action
		}
	}
	if err := m.conflicts(); err != nil {
		return nil, err
	}
	return m, nil
}

// conflicts reports keys bound to two actions that are active together:
// within the global scope, and within each widget scope plus navigation.
func (m *Keymap) conflicts() error {
	groups := [][]Scope{{Global}, {Nav, Proc}, {Nav, Net}, {Nav, Docker}, {Nav, Editor}}
	var errs []error
	reported := map[string]bool{}
	for _, g := range groups {
		owner := map[string]Action{}
		for _, i := range Actions {
			if !slices.Contains(g, i.Scope) {
				continue
			}
			for _, k := range m.keys[i.Action] {
				if prev, ok := owner[k]; ok && prev != i.Action {
					msg := fmt.Sprintf("key %q is bound to both %s and %s", k, prev, i.Action)
					if !reported[msg] {
						reported[msg] = true
						errs = append(errs, errors.New(msg))
					}
					continue
				}
				owner[k] = i.Action
			}
		}
	}
	return errors.Join(errs...)
}

// Match returns the action bound to key in the first scope that has one.
func (m *Keymap) Match(key string, scopes ...Scope) (Action, bool) {
	for _, s := range scopes {
		if a, ok := m.byKey[s][key]; ok {
			return a, true
		}
	}
	return "", false
}

// Keys returns the keys bound to an action.
func (m *Keymap) Keys(a Action) []string { return m.keys[a] }

// First returns the first key of an action for hints, or "".
func (m *Keymap) First(a Action) string {
	if k := m.keys[a]; len(k) > 0 {
		return k[0]
	}
	return ""
}

// Label returns all keys of an action joined for help screens.
func (m *Keymap) Label(a Action) string {
	return strings.Join(m.keys[a], " / ")
}

var namedKeys = map[string]bool{
	"up": true, "down": true, "left": true, "right": true, "home": true, "end": true,
	"pgup": true, "pgdown": true, "insert": true, "delete": true, "backspace": true,
	"enter": true, "tab": true, "esc": true, "space": true,
	"f1": true, "f2": true, "f3": true, "f4": true, "f5": true, "f6": true,
	"f7": true, "f8": true, "f9": true, "f10": true, "f11": true, "f12": true,
}

// ValidKey checks a key name such as "x", "ctrl+u", "shift+tab" or "f5".
func ValidKey(k string) error {
	if k == "" {
		return errors.New("empty key")
	}
	if k == "+" || k == "-" {
		return nil
	}
	parts := strings.Split(k, "+")
	base := parts[len(parts)-1]
	for _, mod := range parts[:len(parts)-1] {
		switch mod {
		case "ctrl", "alt", "shift", "super", "meta":
		default:
			return fmt.Errorf("key %q: unknown modifier %q (want ctrl, alt, shift, super)", k, mod)
		}
	}
	if namedKeys[base] || len([]rune(base)) == 1 {
		return nil
	}
	return fmt.Errorf("key %q: unknown key %q", k, base)
}
