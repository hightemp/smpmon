package ui

import (
	"fmt"
	"slices"

	"github.com/hightemp/smpmon/internal/chart"
)

// Theme holds every color the UI uses.
type Theme struct {
	Name string

	Fg          chart.RGB // normal text
	Dim         chart.RGB // secondary text, inactive elements
	Title       chart.RGB // widget titles
	Hotkey      chart.RGB // key hints in titles and the status bar
	Border      chart.RGB // default border
	Selected    chart.RGB // background of the selected row
	SelectedFg  chart.RGB
	MeterEmpty  chart.RGB
	Warn, Crit  chart.RGB
	StatusBg    chart.RGB
	DialogBg    chart.RGB
	DialogFg    chart.RGB
	DialogFrame chart.RGB

	// Box border colors per widget type; types not listed use Border.
	Boxes map[string]chart.RGB

	CPU, Mem, Cached, Free, Swap, Disk, NetRx, NetTx, Proc, Temp chart.Gradient
}

// BoxColor returns the border color for a widget type.
func (t *Theme) BoxColor(widget string) chart.RGB {
	if c, ok := t.Boxes[widget]; ok {
		return c
	}
	return t.Border
}

// Gradient returns a named gradient; unknown names fall back to CPU.
func (t *Theme) Gradient(name string) chart.Gradient {
	switch name {
	case "mem", "used":
		return t.Mem
	case "cached":
		return t.Cached
	case "free", "available":
		return t.Free
	case "swap":
		return t.Swap
	case "disk":
		return t.Disk
	case "rx", "download":
		return t.NetRx
	case "tx", "upload":
		return t.NetTx
	case "proc":
		return t.Proc
	case "temp":
		return t.Temp
	}
	return t.CPU
}

func hex(s string) chart.RGB { return chart.MustHex(s) }

func grad(stops ...string) chart.Gradient {
	g := make(chart.Gradient, len(stops))
	for i, s := range stops {
		g[i] = hex(s)
	}
	return g
}

// themes are the built-in themes. "default" follows btop's default theme.
var themes = map[string]*Theme{
	"default": {
		Name: "default",
		Fg:   hex("#cccccc"), Dim: hex("#5c5c5c"), Title: hex("#eeeeee"), Hotkey: hex("#b54040"),
		Border: hex("#5a5a5a"), Selected: hex("#6a2f2f"), SelectedFg: hex("#ffffff"),
		MeterEmpty: hex("#303030"), Warn: hex("#dcd179"), Crit: hex("#dc4c4c"),
		StatusBg: hex("#1e1e1e"), DialogBg: hex("#262626"), DialogFg: hex("#eeeeee"), DialogFrame: hex("#b54040"),
		Boxes: map[string]chart.RGB{
			"cpu": hex("#3d7b46"), "mem": hex("#8a882e"), "disks": hex("#8a882e"),
			"net": hex("#423ba5"), "proc": hex("#923535"),
		},
		CPU:    grad("#77ca9b", "#cbc06c", "#dc4c4c"),
		Mem:    grad("#592b26", "#d9626d"),
		Cached: grad("#163350", "#74e6fc"),
		Free:   grad("#384f21", "#b5e685"),
		Swap:   grad("#4e3f0e", "#ffd77a"),
		Disk:   grad("#384f21", "#cbc06c", "#dc4c4c"),
		NetRx:  grad("#291f75", "#4f43a3", "#b0a9de"),
		NetTx:  grad("#620665", "#7d4180", "#dcafde"),
		Proc:   grad("#80d0a3", "#dcd179", "#d45454"),
		Temp:   grad("#4897d4", "#5474e8", "#ff40b6"),
	},
	"nord": {
		Name: "nord",
		Fg:   hex("#d8dee9"), Dim: hex("#4c566a"), Title: hex("#eceff4"), Hotkey: hex("#88c0d0"),
		Border: hex("#4c566a"), Selected: hex("#3b4252"), SelectedFg: hex("#eceff4"),
		MeterEmpty: hex("#3b4252"), Warn: hex("#ebcb8b"), Crit: hex("#bf616a"),
		StatusBg: hex("#2e3440"), DialogBg: hex("#3b4252"), DialogFg: hex("#eceff4"), DialogFrame: hex("#88c0d0"),
		Boxes: map[string]chart.RGB{
			"cpu": hex("#5e81ac"), "mem": hex("#81a1c1"), "disks": hex("#81a1c1"),
			"net": hex("#88c0d0"), "proc": hex("#8fbcbb"),
		},
		CPU:    grad("#a3be8c", "#ebcb8b", "#bf616a"),
		Mem:    grad("#5e81ac", "#bf616a"),
		Cached: grad("#4c566a", "#88c0d0"),
		Free:   grad("#4c566a", "#a3be8c"),
		Swap:   grad("#4c566a", "#ebcb8b"),
		Disk:   grad("#a3be8c", "#ebcb8b", "#bf616a"),
		NetRx:  grad("#5e81ac", "#88c0d0"),
		NetTx:  grad("#b48ead", "#d8dee9"),
		Proc:   grad("#a3be8c", "#ebcb8b", "#bf616a"),
		Temp:   grad("#81a1c1", "#b48ead", "#bf616a"),
	},
	"gruvbox": {
		Name: "gruvbox",
		Fg:   hex("#ebdbb2"), Dim: hex("#665c54"), Title: hex("#fbf1c7"), Hotkey: hex("#fe8019"),
		Border: hex("#665c54"), Selected: hex("#504945"), SelectedFg: hex("#fbf1c7"),
		MeterEmpty: hex("#3c3836"), Warn: hex("#fabd2f"), Crit: hex("#fb4934"),
		StatusBg: hex("#1d2021"), DialogBg: hex("#3c3836"), DialogFg: hex("#fbf1c7"), DialogFrame: hex("#fe8019"),
		Boxes: map[string]chart.RGB{
			"cpu": hex("#98971a"), "mem": hex("#d79921"), "disks": hex("#d79921"),
			"net": hex("#458588"), "proc": hex("#cc241d"),
		},
		CPU:    grad("#b8bb26", "#fabd2f", "#fb4934"),
		Mem:    grad("#9d0006", "#fb4934"),
		Cached: grad("#076678", "#83a598"),
		Free:   grad("#79740e", "#b8bb26"),
		Swap:   grad("#b57614", "#fabd2f"),
		Disk:   grad("#b8bb26", "#fabd2f", "#fb4934"),
		NetRx:  grad("#076678", "#83a598"),
		NetTx:  grad("#8f3f71", "#d3869b"),
		Proc:   grad("#b8bb26", "#fabd2f", "#fb4934"),
		Temp:   grad("#83a598", "#d3869b", "#fb4934"),
	},
	"dracula": {
		Name: "dracula",
		Fg:   hex("#f8f8f2"), Dim: hex("#6272a4"), Title: hex("#f8f8f2"), Hotkey: hex("#ff79c6"),
		Border: hex("#44475a"), Selected: hex("#44475a"), SelectedFg: hex("#f8f8f2"),
		MeterEmpty: hex("#343746"), Warn: hex("#f1fa8c"), Crit: hex("#ff5555"),
		StatusBg: hex("#21222c"), DialogBg: hex("#343746"), DialogFg: hex("#f8f8f2"), DialogFrame: hex("#bd93f9"),
		Boxes: map[string]chart.RGB{
			"cpu": hex("#50fa7b"), "mem": hex("#f1fa8c"), "disks": hex("#f1fa8c"),
			"net": hex("#bd93f9"), "proc": hex("#ff79c6"),
		},
		CPU:    grad("#50fa7b", "#f1fa8c", "#ff5555"),
		Mem:    grad("#6272a4", "#ff79c6"),
		Cached: grad("#44475a", "#8be9fd"),
		Free:   grad("#44475a", "#50fa7b"),
		Swap:   grad("#44475a", "#ffb86c"),
		Disk:   grad("#50fa7b", "#f1fa8c", "#ff5555"),
		NetRx:  grad("#6272a4", "#bd93f9"),
		NetTx:  grad("#6272a4", "#ff79c6"),
		Proc:   grad("#50fa7b", "#f1fa8c", "#ff5555"),
		Temp:   grad("#8be9fd", "#bd93f9", "#ff5555"),
	},
	"monochrome": {
		Name: "monochrome",
		Fg:   hex("#c0c0c0"), Dim: hex("#606060"), Title: hex("#ffffff"), Hotkey: hex("#ffffff"),
		Border: hex("#808080"), Selected: hex("#404040"), SelectedFg: hex("#ffffff"),
		MeterEmpty: hex("#303030"), Warn: hex("#e0e0e0"), Crit: hex("#ffffff"),
		StatusBg: hex("#1a1a1a"), DialogBg: hex("#2a2a2a"), DialogFg: hex("#ffffff"), DialogFrame: hex("#ffffff"),
		CPU: grad("#707070", "#ffffff"), Mem: grad("#707070", "#ffffff"), Cached: grad("#505050", "#b0b0b0"),
		Free: grad("#505050", "#b0b0b0"), Swap: grad("#707070", "#ffffff"), Disk: grad("#707070", "#ffffff"),
		NetRx: grad("#707070", "#ffffff"), NetTx: grad("#505050", "#b0b0b0"), Proc: grad("#707070", "#ffffff"),
		Temp: grad("#707070", "#ffffff"),
	},
}

// ThemeNames lists built-in and user themes.
func ThemeNames() []string {
	userThemesMu.RLock()
	defer userThemesMu.RUnlock()
	names := make([]string, 0, len(themes)+len(userThemes))
	for n := range themes {
		names = append(names, n)
	}
	for n := range userThemes {
		if _, dup := themes[n]; !dup {
			names = append(names, n)
		}
	}
	slices.Sort(names)
	return names
}

// LookupTheme returns a user theme or a built-in theme by name. User themes
// take precedence, so a file can override a built-in theme.
func LookupTheme(name string) (*Theme, error) {
	userThemesMu.RLock()
	t, ok := userThemes[name]
	userThemesMu.RUnlock()
	if ok {
		return t, nil
	}
	if t, ok := themes[name]; ok {
		return t, nil
	}
	return nil, fmt.Errorf("unknown theme %q (available: %v)", name, ThemeNames())
}
