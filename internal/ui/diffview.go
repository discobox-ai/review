package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/mattn/go-runewidth"
	"github.com/discobox-ai/difftui/internal/git"
)

// diffView is the diff pane. It keeps the parsed patch around so that hunks
// and individual lines can be staged, unstaged or reverted like git-gui's
// diff context menu does.
type diffView struct {
	file  *git.File
	side  git.Side
	patch *git.Patch
	lines []git.DiffLine
	texts []string   // code text with tabs expanded, parallel to lines
	spans [][]span   // syntax highlighting, parallel to lines
	emph  []emphasis // the changed part of a modified line, parallel to lines
	numW  int        // width of the line number column
	text  string     // shown instead of a patch for messages such as "no differences"

	cursor int
	offset int
	anchor int // start of a line selection, or -1

	width  int
	height int // rows for the body, header excluded
}

func newDiffView() *diffView { return &diffView{anchor: -1} }

func (d *diffView) clear() {
	d.file, d.patch, d.lines, d.text = nil, nil, nil, ""
	d.texts, d.spans, d.emph, d.numW = nil, nil, nil, 0
	d.cursor, d.offset, d.anchor = 0, 0, -1
}

func (d *diffView) setMessage(msg string) {
	d.patch, d.lines, d.spans, d.emph, d.texts = nil, nil, nil, nil, nil
	d.numW = 0
	d.text = msg
	d.cursor, d.offset, d.anchor = 0, 0, -1
}

func (d *diffView) setPatch(f *git.File, side git.Side, raw string) {
	d.file, d.side, d.text = f, side, ""
	d.patch = git.ParsePatch(raw)
	d.lines = d.patch.Render()

	// Tabs are expanded first, so the syntax spans, the emphasis span and the
	// padding all count the same columns.
	d.texts = make([]string, len(d.lines))
	for i, l := range d.lines {
		if isCodeLine(l.Kind) {
			d.texts[i] = expandTabs(codeText(l.Text))
		} else {
			d.texts[i] = l.Text
		}
	}
	d.spans = highlightLines(f.Path, d.lines, d.texts)
	d.emph = pairEmphasis(d.lines, d.texts)
	d.numW = numberWidth(d.lines)
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

		// The cursor and the selection live in the left edge bar, leaving the
		// background free to carry the diff itself.
		cursor := i == d.cursor && focused
		selected := d.inSelection(i)
		bar := st.body
		switch {
		case cursor:
			bar = st.cursorBar
		case selected:
			bar = st.selectBar
		}
		numSty := st.dimText
		if cursor {
			numSty = st.numberOn
		}

		if !isCodeLine(l.Kind) {
			style := st.diffMeta
			if l.Kind == '@' {
				style = st.diffHunk
			}
			rows = append(rows, renderDiffRow(diffRow{
				bar: bar, numW: d.numW, numSty: numSty,
				gutter: "", code: l.Text, base: style, emphSty: style,
				width: d.width,
			}))
			continue
		}

		base, emphSty := st.body, st.body
		switch l.Kind {
		case '+':
			base, emphSty = st.diffAdd, st.diffAddEmph
		case '-':
			base, emphSty = st.diffDel, st.diffDelEmph
		}
		rows = append(rows, renderDiffRow(diffRow{
			bar:    bar,
			number: lineNumber(l), numW: d.numW, numSty: numSty,
			gutter: diffGutter(l.Kind), code: d.texts[i],
			spans: d.spanAt(i), emph: d.emphAt(i),
			base: base, emphSty: emphSty,
			palette: st.syntax, width: d.width,
		}))
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

// emphAt returns the changed range within a rendered line, if any.
func (d *diffView) emphAt(i int) emphasis {
	if i < 0 || i >= len(d.emph) {
		return emphasis{}
	}
	return d.emph[i]
}

// lineNumber is the number shown for a line: its own side's, since that is the
// one it exists on.
func lineNumber(l git.DiffLine) int {
	if l.Kind == '-' {
		return l.Old
	}
	return l.New
}

// numberWidth sizes the line number column to the largest number the diff will
// print, so the numbers do not jitter between files.
func numberWidth(lines []git.DiffLine) int {
	largest := 0
	for _, l := range lines {
		largest = max(largest, max(l.Old, l.New))
	}
	width := len(itoa(largest))
	return max(width, minNumberWidth)
}

// minNumberWidth keeps the column steady for short files.
const minNumberWidth = 3

// diffGutter is the two column marker in front of each line of code.
//
// A unified diff puts + and - in the same column, so an added line's code
// starts immediately after the +. Giving each side a column of its own means
// an addition always has a space between the marker and the code, which reads
// more like the file itself; a deletion sits tight against its marker, which
// matters less because deletions are the side you skim.
//
//	"+ added"
//	" -removed"
//	"  context"
func diffGutter(kind byte) string {
	switch kind {
	case '+':
		return "+ "
	case '-':
		return " -"
	default:
		return "  "
	}
}

// diffRow is everything needed to draw one line of code in the diff pane.
type diffRow struct {
	bar     lipgloss.Style // the one column cursor/selection bar at the left
	number  int            // line number, 0 to leave the column blank
	numW    int            // width of the number column
	numSty  lipgloss.Style
	gutter  string // the two column +/- marker
	code    string // the line's text, tabs already expanded
	spans   []span
	emph    emphasis
	base    lipgloss.Style // the line's band: a background, or none for context
	emphSty lipgloss.Style // the stronger band for the characters that changed
	palette syntaxPalette
	width   int
}

// renderDiffRow draws one line as
//
//	[bar][number][ ][gutter][code, padded to the pane width]
//
// resolving three channels at once. The background says what the diff did:
// a band across added and removed lines, a stronger shade over the characters
// that actually changed. The foreground says what the code is, from the
// lexer. And the bar at the left edge says where the cursor is and what is
// selected — it lives outside the code because the background it would
// otherwise use is already carrying the diff.
func renderDiffRow(r diffRow) string {
	if r.width <= 0 {
		return ""
	}

	var b strings.Builder
	// The bar column is always drawn, blank when the row is neither the
	// cursor nor selected, so rows never shift sideways.
	b.WriteString(r.bar.Render(" "))
	used := 1
	if used >= r.width {
		return truncateANSI(b.String(), r.width)
	}
	if r.numW > 0 {
		numText := strings.Repeat(" ", r.numW)
		if r.number > 0 {
			n := itoa(r.number)
			if len(n) < r.numW {
				n = strings.Repeat(" ", r.numW-len(n)) + n
			}
			numText = n
		}
		b.WriteString(r.numSty.Render(numText + " "))
		used += runewidth.StringWidth(numText) + 1
		if used >= r.width {
			return truncateANSI(b.String(), r.width)
		}
	}

	runes := []rune(r.gutter + r.code)
	offset := len([]rune(r.gutter))
	fg := make([]lipgloss.TerminalColor, len(runes))
	for _, s := range r.spans {
		value, ok := r.palette.color(s.token)
		if !ok {
			continue
		}
		// Spans are offsets into the code, which starts after the gutter.
		for at := max(s.start+offset, 0); at < s.end+offset && at < len(runes); at++ {
			fg[at] = value
		}
	}
	changed := make([]bool, len(runes))
	if !r.emph.empty() {
		for at := max(r.emph.start+offset, 0); at < r.emph.end+offset && at < len(runes); at++ {
			changed[at] = true
		}
	}

	type appearance struct {
		fg      lipgloss.TerminalColor
		changed bool
	}
	var buf []rune
	var current appearance
	flush := func() {
		if len(buf) == 0 {
			return
		}
		style := r.base
		if current.changed {
			style = r.emphSty
		}
		if current.fg != nil {
			style = style.Foreground(current.fg)
		}
		b.WriteString(style.Render(string(buf)))
		buf = buf[:0]
	}

	truncated := false
	for at, ch := range runes {
		w := runewidth.RuneWidth(ch)
		if used+w > r.width {
			truncated = true
			break
		}
		next := appearance{fg: fg[at], changed: changed[at]}
		if next != current {
			flush()
			current = next
		}
		buf = append(buf, ch)
		used += w
	}
	flush()
	if truncated && used < r.width {
		b.WriteString(r.base.Render("…"))
		used++
	}
	// The band runs to the right margin, so a changed line reads as a solid
	// block rather than as ragged text.
	if used < r.width {
		b.WriteString(r.base.Render(strings.Repeat(" ", r.width-used)))
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
