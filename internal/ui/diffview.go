package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/obot-platform/difftui/internal/git"
)

// diffView is the diff pane. It keeps the parsed patch around so that hunks
// and individual lines can be staged, unstaged or reverted like git-gui's
// diff context menu does.
type diffView struct {
	file  *git.File
	side  git.Side
	patch *git.Patch
	lines []git.DiffLine
	spans [][]span // syntax highlighting, parallel to lines
	text  string   // shown instead of a patch for messages such as "no differences"

	cursor int
	offset int
	anchor int // start of a line selection, or -1

	width  int
	height int // rows for the body, header excluded
}

func newDiffView() *diffView { return &diffView{anchor: -1} }

func (d *diffView) clear() {
	d.file, d.patch, d.lines, d.text, d.spans = nil, nil, nil, "", nil
	d.cursor, d.offset, d.anchor = 0, 0, -1
}

func (d *diffView) setMessage(msg string) {
	d.patch, d.lines, d.spans = nil, nil, nil
	d.text = msg
	d.cursor, d.offset, d.anchor = 0, 0, -1
}

func (d *diffView) setPatch(f *git.File, side git.Side, raw string) {
	d.file, d.side, d.text = f, side, ""
	d.patch = git.ParsePatch(raw)
	d.lines = d.patch.Render()
	d.spans = highlightLines(f.Path, d.lines)
	d.cursor, d.offset, d.anchor = 0, 0, -1
	// Start on the first hunk header so the first stage-hunk does the
	// obvious thing.
	for i, l := range d.lines {
		if l.Kind == '@' {
			d.cursor = i
			break
		}
	}
	d.clamp()
}

func (d *diffView) empty() bool { return len(d.lines) == 0 }

func (d *diffView) move(delta int) {
	d.cursor += delta
	d.clamp()
}

func (d *diffView) moveTo(i int) {
	d.cursor = i
	d.clamp()
}

func (d *diffView) clamp() {
	if d.cursor >= len(d.lines) {
		d.cursor = len(d.lines) - 1
	}
	if d.cursor < 0 {
		d.cursor = 0
	}
	if d.height <= 0 {
		return
	}
	if d.cursor < d.offset {
		d.offset = d.cursor
	}
	if d.cursor >= d.offset+d.height {
		d.offset = d.cursor - d.height + 1
	}
	if d.offset < 0 {
		d.offset = 0
	}
}

// currentHunk returns the index of the hunk the cursor sits in, or -1.
func (d *diffView) currentHunk() int {
	if d.cursor < 0 || d.cursor >= len(d.lines) {
		return -1
	}
	return d.lines[d.cursor].Hunk
}

func (d *diffView) hunkCount() int {
	if d.patch == nil {
		return 0
	}
	return len(d.patch.Hunks)
}

// nextHunk moves the cursor to the next or previous hunk header.
func (d *diffView) jumpHunk(dir int) {
	for i := d.cursor + dir; i >= 0 && i < len(d.lines); i += dir {
		if d.lines[i].Kind == '@' {
			d.moveTo(i)
			return
		}
	}
}

// toggleVisual starts visual line selection on the current line, or leaves it
// again, the way V does in vim.
func (d *diffView) toggleVisual() {
	if d.anchor >= 0 {
		d.anchor = -1
		return
	}
	d.anchor = d.cursor
}

// visual reports whether a line selection is in progress.
func (d *diffView) visual() bool { return d.anchor >= 0 }

// clearVisual leaves visual line selection.
func (d *diffView) clearVisual() { d.anchor = -1 }

// selection returns the hunk index and the set of body line indexes currently
// selected. With no active selection it is just the line under the cursor.
func (d *diffView) selection() (int, map[int]bool) {
	if d.cursor < 0 || d.cursor >= len(d.lines) {
		return -1, nil
	}
	lo, hi := d.cursor, d.cursor
	if d.anchor >= 0 {
		lo, hi = min(d.anchor, d.cursor), max(d.anchor, d.cursor)
	}
	hunk := -1
	sel := map[int]bool{}
	for i := lo; i <= hi && i < len(d.lines); i++ {
		l := d.lines[i]
		if l.Line < 0 || l.Hunk < 0 {
			continue
		}
		if l.Kind != '+' && l.Kind != '-' {
			continue
		}
		if hunk == -1 {
			hunk = l.Hunk
		} else if hunk != l.Hunk {
			// git-gui also refuses to stage a range spanning hunks.
			continue
		}
		sel[l.Line] = true
	}
	return hunk, sel
}

func (d *diffView) inSelection(i int) bool {
	if d.anchor < 0 {
		return false
	}
	return i >= min(d.anchor, d.cursor) && i <= max(d.anchor, d.cursor)
}

func (d *diffView) headerText() (string, string) {
	if d.file == nil {
		return "", ""
	}
	status := d.file.State.Describe()
	if d.side == git.SideIndex {
		status += " (staged)"
	}
	return status, d.file.Path
}

func (d *diffView) view(st *styles, focused bool) string {
	titleStyle := st.titleDiff
	if !focused {
		titleStyle = st.titleDim
	}
	status, path := d.headerText()
	label := "Diff"
	if path != "" {
		label = status + "   File: " + path
	}
	right := ""
	if n := d.hunkCount(); n > 0 {
		h := d.currentHunk()
		if h >= 0 {
			right = "hunk " + itoa(h+1) + "/" + itoa(n)
		} else {
			right = plural(n, "hunk", "hunks")
		}
	}
	header := renderTitle(titleStyle, label, right, d.width)

	rows := make([]string, 0, d.height)
	if len(d.lines) == 0 {
		for _, l := range strings.Split(strings.TrimRight(d.text, "\n"), "\n") {
			if len(rows) >= d.height {
				break
			}
			rows = append(rows, st.dimText.Render(pad(l, d.width)))
		}
	}
	for i := d.offset; i < len(d.lines) && len(rows) < d.height; i++ {
		l := d.lines[i]
		var style lipgloss.Style
		switch l.Kind {
		case '+':
			style = st.diffAdd
		case '-':
			style = st.diffDel
		case '@':
			style = st.diffHunk
		case 'h', '\\':
			style = st.diffMeta
		default:
			style = st.body
		}
		switch {
		case d.inSelection(i) && focused:
			style = style.Background(colSelBG)
		case d.inSelection(i):
			style = style.Background(colInactive)
		case i == d.cursor && focused:
			style = style.Background(colSelBG)
		}
		if isCodeLine(l.Kind) {
			rows = append(rows, renderSyntaxLine(l.Text, d.spanAt(i), style, st.syntax, d.width))
			continue
		}
		rows = append(rows, style.Render(pad(l.Text, d.width)))
	}
	for len(rows) < d.height {
		rows = append(rows, strings.Repeat(" ", max(d.width, 0)))
	}
	return lipgloss.JoinVertical(lipgloss.Left, append([]string{header}, rows...)...)
}

// spanAt returns the highlighting for a rendered line, if any.
func (d *diffView) spanAt(i int) []span {
	if i < 0 || i >= len(d.spans) {
		return nil
	}
	return d.spans[i]
}

// renderSyntaxLine draws one diff line, colouring the runs the lexer
// identified while keeping the line's own foreground everywhere else and its
// background across the full pane width.
func renderSyntaxLine(text string, spans []span, base lipgloss.Style, palette syntaxPalette, width int) string {
	if width <= 0 {
		return ""
	}
	runes := []rune(text)
	fg := make([]lipgloss.TerminalColor, len(runes))
	for _, s := range spans {
		value, ok := palette.color(s.token)
		if !ok {
			continue
		}
		// Spans are offsets into the code text, which starts one column in,
		// after the +/-/space marker.
		for at := max(s.start+1, 0); at < s.end+1 && at < len(runes); at++ {
			fg[at] = value
		}
	}

	var b strings.Builder
	var buf []rune
	var current lipgloss.TerminalColor
	flush := func() {
		if len(buf) == 0 {
			return
		}
		style := base
		if current != nil {
			style = style.Foreground(current)
		}
		b.WriteString(style.Render(string(buf)))
		buf = buf[:0]
	}

	used, truncated := 0, false
	for at, r := range runes {
		w := runewidth.RuneWidth(r)
		if used+w > width {
			truncated = true
			break
		}
		if fg[at] != current {
			flush()
			current = fg[at]
		}
		buf = append(buf, r)
		used += w
	}
	flush()
	if truncated && used < width {
		b.WriteString(base.Render("…"))
		used++
	}
	if used < width {
		b.WriteString(base.Render(strings.Repeat(" ", width-used)))
	}
	return b.String()
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}
