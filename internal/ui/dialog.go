package ui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// dialog is a modal box drawn over the dashboard that takes all input.
type dialog interface {
	title() string
	// lines returns the content for an inner width of w.
	lines(ctx *Context, w int) []string
	// key handles a key press; done closes the dialog.
	key(k tea.KeyPressMsg) (done bool, cmd tea.Cmd)
}

type openDialogMsg struct{ d dialog }

type statusMsg struct{ text string }

func dialogCmd(d dialog) tea.Cmd { return func() tea.Msg { return openDialogMsg{d} } }

func statusCmd(s string) tea.Cmd { return func() tea.Msg { return statusMsg{s} } }

// ---- confirm ----

type confirmDialog struct {
	text  string
	onYes func() tea.Cmd
}

func (d *confirmDialog) title() string { return "confirm" }

func (d *confirmDialog) lines(ctx *Context, _ int) []string {
	return []string{
		fg(ctx.Theme.DialogFg, d.text),
		"",
		hint(ctx, "y/enter", "yes") + "   " + hint(ctx, "n/esc", "no"),
	}
}

func (d *confirmDialog) key(k tea.KeyPressMsg) (bool, tea.Cmd) {
	switch k.String() {
	case "y", "Y", "enter":
		return true, d.onYes()
	case "n", "N", "esc", "q":
		return true, nil
	}
	return false, nil
}

// ---- choose ----

type chooseDialog struct {
	heading string
	options []string
	sel     int
	onPick  func(int) tea.Cmd
}

func (d *chooseDialog) title() string { return d.heading }

func (d *chooseDialog) lines(ctx *Context, w int) []string {
	t := ctx.Theme
	out := make([]string, 0, len(d.options)+2)
	for i, o := range d.options {
		line := fit(" "+o, w)
		if i == d.sel {
			line = bg(t.Selected, fg(t.SelectedFg, line))
		} else {
			line = fg(t.DialogFg, line)
		}
		out = append(out, line)
	}
	return append(out, "", hint(ctx, "↑↓", "select")+"  "+hint(ctx, "enter", "send")+"  "+hint(ctx, "esc", "cancel"))
}

func (d *chooseDialog) key(k tea.KeyPressMsg) (bool, tea.Cmd) {
	switch k.String() {
	case "up", "k":
		d.sel = (d.sel - 1 + len(d.options)) % len(d.options)
	case "down", "j":
		d.sel = (d.sel + 1) % len(d.options)
	case "enter":
		return true, d.onPick(d.sel)
	case "esc", "q":
		return true, nil
	}
	return false, nil
}

// ---- input ----

type inputDialog struct {
	prompt   string
	value    string
	onSubmit func(string) tea.Cmd
}

func (d *inputDialog) title() string { return "input" }

func (d *inputDialog) lines(ctx *Context, w int) []string {
	t := ctx.Theme
	field := bg(t.Selected, fg(t.SelectedFg, fit(" "+d.value+"█", min(w, 24))))
	return []string{fg(t.DialogFg, d.prompt), "", field, "", hint(ctx, "enter", "apply") + "  " + hint(ctx, "esc", "cancel")}
}

func (d *inputDialog) key(k tea.KeyPressMsg) (bool, tea.Cmd) {
	switch k.String() {
	case "enter":
		return true, d.onSubmit(d.value)
	case "esc":
		return true, nil
	case "backspace":
		if r := []rune(d.value); len(r) > 0 {
			d.value = string(r[:len(r)-1])
		}
	case "ctrl+u":
		d.value = ""
	default:
		if k.Text != "" {
			d.value += k.Text
		}
	}
	return false, nil
}

// ---- help ----

type helpDialog struct {
	sections []helpSection
}

type helpSection struct {
	name string
	keys [][2]string
}

func (d *helpDialog) title() string { return "help" }

func (d *helpDialog) lines(ctx *Context, _ int) []string {
	t := ctx.Theme
	var out []string
	for i, s := range d.sections {
		if i > 0 {
			out = append(out, "")
		}
		out = append(out, fg(t.Title, bold(s.name)))
		for _, k := range s.keys {
			out = append(out, "  "+fg(t.Hotkey, fmt.Sprintf("%-14s", k[0]))+fg(t.DialogFg, k[1]))
		}
	}
	return append(out, "", fg(t.Dim, "press any key to close"))
}

func (d *helpDialog) key(tea.KeyPressMsg) (bool, tea.Cmd) { return true, nil }

// renderDialog draws a dialog box sized to its content, at most maxW×maxH.
func renderDialog(ctx *Context, d dialog, maxW, maxH int) []string {
	innerW := min(maxW-4, 72)
	content := d.lines(ctx, innerW)
	cw := 0
	for _, l := range content {
		cw = max(cw, width(l))
	}
	innerW = min(max(cw, 24), innerW)
	h := min(len(content)+4, maxH)
	f := frame{style: "rounded", color: ctx.Theme.DialogFrame, title: fg(ctx.Theme.Title, bold(d.title()))}
	padded := make([]string, 0, len(content)+2)
	padded = append(padded, "")
	for _, l := range content {
		padded = append(padded, " "+fit(l, innerW)+" ")
	}
	lines := f.render(innerW+4, h, padded)
	for i, l := range lines {
		lines[i] = bg(ctx.Theme.DialogBg, strings.ReplaceAll(l, "\x1b[49m", "\x1b[48;2;"+rgbParams(ctx.Theme.DialogBg)+"m"))
	}
	return lines
}
