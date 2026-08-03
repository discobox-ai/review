package ui

import (
	"strings"

	"github.com/charmbracelet/bubbles/textinput"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
)

type dialogKind int

const (
	dlgMessage dialogKind = iota
	dlgConfirm
	dlgList
	dlgInput
	dlgText
)

// dialog is the modal layer standing in for git-gui's popup windows.
type dialog struct {
	kind   dialogKind
	title  string
	body   string
	items  []string
	cursor int
	offset int
	input  textinput.Model
	err    bool

	// action receives the dialog result: the chosen item, the entered text,
	// or "yes" for a confirmed question. It is not called on cancel.
	action func(result string) tea.Cmd
}

func messageDialog(title, body string, isErr bool) *dialog {
	return &dialog{kind: dlgMessage, title: title, body: body, err: isErr}
}

func confirmDialog(title, body string, action func(string) tea.Cmd) *dialog {
	return &dialog{kind: dlgConfirm, title: title, body: body, action: action}
}

func listDialog(title, body string, items []string, action func(string) tea.Cmd) *dialog {
	return &dialog{kind: dlgList, title: title, body: body, items: items, action: action}
}

func inputDialog(title, body, placeholder string, action func(string) tea.Cmd) *dialog {
	ti := textinput.New()
	ti.Placeholder = placeholder
	ti.Focus()
	ti.CharLimit = 200
	ti.Width = 40
	return &dialog{kind: dlgInput, title: title, body: body, input: ti, action: action}
}

func textDialog(title, body string) *dialog {
	return &dialog{kind: dlgText, title: title, body: body}
}

// update handles a key press. It returns a command and whether the dialog
// should close.
func (d *dialog) update(msg tea.KeyMsg) (tea.Cmd, bool) {
	switch msg.String() {
	case "esc", "ctrl+q":
		return nil, true
	}

	switch d.kind {
	case dlgMessage, dlgText:
		switch msg.String() {
		case "enter", " ", "q":
			return nil, true
		case "up", "k":
			if d.offset > 0 {
				d.offset--
			}
		case "down", "j":
			d.offset++
		}
		return nil, false

	case dlgConfirm:
		switch strings.ToLower(msg.String()) {
		case "y", "enter":
			if d.action != nil {
				return d.action("yes"), true
			}
			return nil, true
		case "n", "q":
			return nil, true
		}
		return nil, false

	case dlgList:
		switch msg.String() {
		case "up", "k":
			if d.cursor > 0 {
				d.cursor--
			}
		case "down", "j":
			if d.cursor < len(d.items)-1 {
				d.cursor++
			}
		case "enter":
			if d.cursor >= 0 && d.cursor < len(d.items) && d.action != nil {
				return d.action(d.items[d.cursor]), true
			}
			return nil, true
		case "q":
			return nil, true
		}
		return nil, false

	case dlgInput:
		if msg.String() == "enter" {
			v := strings.TrimSpace(d.input.Value())
			if v == "" {
				return nil, true
			}
			if d.action != nil {
				return d.action(v), true
			}
			return nil, true
		}
		var cmd tea.Cmd
		d.input, cmd = d.input.Update(msg)
		return cmd, false
	}
	return nil, false
}

func (d *dialog) view(st *styles, width, height int) string {
	boxWidth := min(max(width-8, 20), 90)
	inner := boxWidth - 6 // dialog padding

	var b strings.Builder
	titleStyle := st.dialogTitle
	if d.err {
		titleStyle = st.statusER.Bold(true)
	}
	b.WriteString(titleStyle.Render(truncate(d.title, inner)))
	b.WriteString("\n\n")

	maxBody := max(height-10, 4)
	if d.body != "" {
		lines := wrap(d.body, inner)
		if d.kind == dlgText || d.kind == dlgMessage {
			if d.offset > max(len(lines)-maxBody, 0) {
				d.offset = max(len(lines)-maxBody, 0)
			}
			if d.offset < 0 {
				d.offset = 0
			}
			end := min(d.offset+maxBody, len(lines))
			shown := lines[d.offset:end]
			b.WriteString(strings.Join(shown, "\n"))
			if len(lines) > maxBody {
				b.WriteString("\n" + st.dimText.Render("  … ↑/↓ to scroll"))
			}
		} else {
			b.WriteString(strings.Join(lines, "\n"))
		}
		b.WriteString("\n")
	}

	switch d.kind {
	case dlgConfirm:
		b.WriteString("\n" + st.key.Render("y") + " yes   " + st.key.Render("n") + " no")
	case dlgMessage, dlgText:
		b.WriteString("\n" + st.dimText.Render("press Esc to close"))
	case dlgList:
		b.WriteString("\n")
		visible := min(len(d.items), max(height-12, 3))
		start := 0
		if d.cursor >= visible {
			start = d.cursor - visible + 1
		}
		for i := start; i < len(d.items) && i < start+visible; i++ {
			line := pad("  "+d.items[i], inner)
			if i == d.cursor {
				b.WriteString(st.sel.Render(line) + "\n")
			} else {
				b.WriteString(line + "\n")
			}
		}
		b.WriteString(st.dimText.Render("Enter to choose, Esc to cancel"))
	case dlgInput:
		b.WriteString("\n" + d.input.View() + "\n\n")
		b.WriteString(st.dimText.Render("Enter to accept, Esc to cancel"))
	}

	box := st.dialog.Width(boxWidth).Render(b.String())
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, box)
}

// wrap performs a simple word wrap that also honours existing newlines.
//
// A line that already fits is passed through untouched, so text laid out in
// columns — the help screen — keeps its alignment instead of being reflowed
// into a single spaced run.
func wrap(s string, width int) []string {
	if width < 4 {
		width = 4
	}
	var out []string
	for _, para := range strings.Split(strings.TrimRight(s, "\n"), "\n") {
		if para == "" {
			out = append(out, "")
			continue
		}
		if runewidth.StringWidth(para) <= width {
			out = append(out, para)
			continue
		}
		line := ""
		for _, word := range strings.Fields(para) {
			switch {
			case line == "":
				line = word
			case len(line)+1+len(word) <= width:
				line += " " + word
			default:
				out = append(out, line)
				line = word
			}
		}
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}
