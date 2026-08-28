package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/discobox-ai/difftui/internal/git"
	"github.com/mattn/go-runewidth"
)

// diffView is the diff pane. It keeps the parsed patch around so that hunks
// and individual lines can be staged, unstaged or reverted like git-gui's
// diff context menu does.
type diffView struct {
	file  *git.File
	side  git.Side
	shown diffID // which diff is on screen, so an unchanged one is left alone
	patch *git.Patch
	lines []git.DiffLine
	texts []string   // code text with tabs expanded, parallel to lines
	spans [][]span   // syntax highlighting, parallel to lines
	emph  []emphasis // the changed part of a modified line, parallel to lines
	numW  int        // width of the line number column
	long  int        // runes in the longest line, for the horizontal scroll
	text  string     // shown instead of a patch for messages such as "no differences"

	cursor  int
	offset  int
	hoffset int // runes of code scrolled off to the left
	anchor  int // start of a line selection, or -1

	width  int
	height int // rows for the body, header excluded
}

func newDiffView() *diffView { return &diffView{anchor: -1} }

func (d *diffView) clear() {
	d.file, d.patch, d.lines, d.text = nil, nil, nil, ""
	d.texts, d.spans, d.emph, d.numW, d.long = nil, nil, nil, 0, 0
	d.cursor, d.offset, d.hoffset, d.anchor = 0, 0, 0, -1
	d.shown = diffID{}
}

func (d *diffView) setMessage(msg string) {
	d.patch, d.lines, d.spans, d.emph, d.texts = nil, nil, nil, nil, nil
	d.numW, d.long = 0, 0
	d.text = msg
	d.cursor, d.offset, d.hoffset, d.anchor = 0, 0, 0, -1
	// A message is what is left when a diff could not be taken, so nothing is
	// recorded as shown: the next move asks for it again.
	d.shown = diffID{}
}

// setPatch renders the diff named by key. The file is copied rather than
// referred to, since the list it came from is replaced by the next rescan.
func (d *diffView) setPatch(f git.File, key diffID, raw string) {
	d.file, d.side, d.shown, d.text = &f, key.side, key, ""
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
	d.long = longestLine(d.texts)
	d.cursor, d.offset, d.hoffset, d.anchor = 0, 0, 0, -1
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
	d.hoffset = min(max(d.hoffset, 0), d.maxHOffset())
	if d.cursor >= len(d.lines) {
		d.cursor = len(d.lines) - 1
	}
	if d.cursor < 0 {
		d.cursor = 0
	}
	rows := d.bodyRows()
	if rows <= 0 {
		return
	}
	if d.cursor < d.offset {
		d.offset = d.cursor
	}
	if d.cursor >= d.offset+rows {
		d.offset = d.cursor - rows + 1
	}
	if d.offset < 0 {
		d.offset = 0
	}
	if d.offset > d.maxOffset() {
		d.offset = d.maxOffset()
	}
}

// hScrollStep is how far one press of left or right slides the code, two tab
// stops: far enough that walking across a long line does not take a dozen
// presses, near enough that the eye keeps its place.
const hScrollStep = 2 * tabWidth

// bars says which scrollbars the pane needs: one only when there is something
// to scroll, since a bar that can never move says nothing and costs a column
// of code. Each one takes its space out of the body, which can be what makes
// the other one necessary, so the two are settled together rather than one at
// a time.
func (d *diffView) bars() (vert, horz bool) {
	for i := 0; i < 3; i++ {
		v := scrolls(len(d.lines), d.rowsWith(horz), d.width)
		h := d.long > d.codeWidthWith(vert) && d.height > 1 && d.width > 1
		if v == vert && h == horz {
			break
		}
		vert, horz = v, h
	}
	return vert, horz
}

// rowsWith is how many lines of diff fit once the horizontal bar, if there is
// one, has taken a row.
func (d *diffView) rowsWith(horz bool) int {
	if horz {
		return max(d.height-1, 0)
	}
	return max(d.height, 0)
}

// rowWidth is how wide a line of diff is drawn, once the vertical bar, if
// there is one, has taken a column.
func (d *diffView) rowWidth(vert bool) int {
	if vert {
		return max(d.width-1, 0)
	}
	return max(d.width, 0)
}

// codeWidthWith is the columns left for the code itself, once the cursor bar,
// the line number column and the +/- gutter have taken theirs. Meta lines have
// no gutter and so a little more room, but sizing the scroll to a code line is
// what matters: it is the code that runs off the edge.
func (d *diffView) codeWidthWith(vert bool) int {
	w := d.rowWidth(vert) - 1 - len(diffGutter(' '))
	if d.numW > 0 {
		w -= d.numW + 1
	}
	return max(w, 0)
}

// bodyRows is how many lines of diff are on screen, and codeWidth how many
// columns of each: what the cursor and the two offsets are clamped against.
func (d *diffView) bodyRows() int {
	_, horz := d.bars()
	return d.rowsWith(horz)
}

func (d *diffView) codeWidth() int {
	vert, _ := d.bars()
	return d.codeWidthWith(vert)
}

// rowW is how wide a body row is drawn, once the vertical scrollbar has taken
// its column. It is what the row renderer fills and what the mouse selection
// measures its columns against, so the two cannot disagree about where the
// text ends and the bar begins.
func (d *diffView) rowW() int {
	vert, _ := d.bars()
	return d.rowWidth(vert)
}

// maxOffset is the first line that can sit at the top with the last line still
// on screen.
func (d *diffView) maxOffset() int {
	return listMaxOffset(len(d.lines), d.bodyRows())
}

// maxHOffset is how far the code can slide before its longest line ends at the
// right margin, so there is never empty space to scroll into.
func (d *diffView) maxHOffset() int {
	if d.codeWidth() <= 0 {
		return 0
	}
	return max(d.long-d.codeWidth(), 0)
}

// scrollH slides the code sideways, reporting whether it moved. Left and right
// belong to the diff for as long as there is more line to see; only once the
// code is back against the left margin do they mean the pane beside it.
func (d *diffView) scrollH(delta int) bool {
	want := min(max(d.hoffset+delta, 0), d.maxHOffset())
	if want == d.hoffset {
		return false
	}
	d.hoffset = want
	return true
}

// longestLine measures the widest rendered line, which is what the horizontal
// scroll has to cover. Tabs are already expanded, so runes count columns.
func longestLine(texts []string) int {
	longest := 0
	for _, t := range texts {
		longest = max(longest, len([]rune(t)))
	}
	return longest
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
	switch d.side {
	case git.SideCommit:
		// A commit's status says what it did, not what is staged.
		return d.file.State.DescribeChange() + " in " + abbrevHash(d.shown.commit), d.file.Path
	case git.SideIndex:
		return d.file.State.Describe() + " (staged)", d.file.Path
	}
	return d.file.State.Describe(), d.file.Path
}

// abbrevHash shortens a hash for the diff header, matching what the branch
// history shows beside each commit.
func abbrevHash(hash string) string {
	if len(hash) > 7 {
		return hash[:7]
	}
	return hash
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
	// Scrolled sideways the left margin is no longer column one, and nothing
	// else on screen says so.
	if d.hoffset > 0 {
		right = "col " + itoa(d.hoffset+1) + "   " + right
	}
	header := renderTitle(titleStyle, label, right, d.width)

	// The bars come out of the body, never out of the header, so the pane
	// keeps its width and the code is what gives up the room.
	vert, horz := d.bars()
	rowW, body := d.rowWidth(vert), d.rowsWith(horz)

	rows := make([]string, 0, body)
	msg := d.messageLines()
	for i := d.offset; i < len(msg) && len(rows) < body; i++ {
		rows = append(rows, d.messageRow(st, msg[i]))
	}
	for i := d.offset; i < len(d.lines) && len(rows) < body; i++ {
		// The cursor and the selection live in the left edge bar, leaving the
		// background free to carry the diff itself.
		rows = append(rows, renderDiffRow(d.rowFor(i, st, i == d.cursor && focused, d.inSelection(i))))
	}
	for len(rows) < body {
		rows = append(rows, strings.Repeat(" ", rowW))
	}
	if vert {
		attachVScroll(st, rows, vertTrack, len(d.lines), d.offset, d.maxOffset())
	}
	out := append([]string{header}, rows...)
	if horz {
		out = append(out, d.hbar(st, vert))
	}
	return lipgloss.JoinVertical(lipgloss.Left, out...)
}

// hbar draws the horizontal scrollbar along the foot of the pane. It spans the
// whole width, gutter included, the way an editor's does; the corner under the
// vertical bar is left blank.
func (d *diffView) hbar(st *styles, vert bool) string {
	track := d.rowWidth(vert)
	lo, size := thumbSpan(track, d.long, d.codeWidthWith(vert), d.hoffset, d.maxHOffset())
	var b strings.Builder
	for i := 0; i < track; i++ {
		b.WriteString(scrollCell(st, i >= lo && i < lo+size, horzTrack, thumbGlyph))
	}
	if vert {
		b.WriteString(" ")
	}
	return b.String()
}

// messageLines is the text shown in place of a patch — "no differences", or
// why a diff could not be taken — split into the rows that carry it.
func (d *diffView) messageLines() []string {
	if len(d.lines) > 0 || d.text == "" {
		return nil
	}
	return strings.Split(strings.TrimRight(d.text, "\n"), "\n")
}

func (d *diffView) messageRow(st *styles, line string) string {
	return st.dimText.Render(pad(line, d.rowW()))
}

// rowFor describes one rendered line. Only the left edge bar depends on where
// the cursor is; everything else is a function of the line itself, which is
// what lets a row be rendered for a line that is not currently on screen —
// the mouse selection reads lines either side of the viewport.
func (d *diffView) rowFor(i int, st *styles, cursor, selected bool) diffRow {
	l := d.lines[i]
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
		return diffRow{
			bar: bar, numW: d.numW, numSty: numSty,
			gutter: "", code: l.Text, base: style, emphSty: style,
			hscroll: d.hoffset, width: d.rowW(),
		}
	}

	base, emphSty := st.body, st.body
	switch l.Kind {
	case '+':
		base, emphSty = st.diffAdd, st.diffAddEmph
	case '-':
		base, emphSty = st.diffDel, st.diffDelEmph
	}
	return diffRow{
		bar:    bar,
		number: lineNumber(l), numW: d.numW, numSty: numSty,
		gutter: diffGutter(l.Kind), code: d.texts[i],
		spans: d.spanAt(i), emph: d.emphAt(i),
		base: base, emphSty: emphSty,
		palette: st.syntax, hscroll: d.hoffset, width: d.rowW(),
	}
}

// scroll moves the viewport without moving the cursor, the way a wheel does.
// The cursor is left where it was, so it may scroll out of sight; the next
// key that moves it brings the view back to it, which is what every editor
// does and what makes reading ahead with the wheel worth doing.
func (d *diffView) scroll(delta int) {
	d.offset = min(max(d.offset+delta, 0), listMaxOffset(d.rowCount(), d.bodyRows()))
}

// rowCount is how many body rows the pane has to show, patch or message.
func (d *diffView) rowCount() int {
	if len(d.lines) > 0 {
		return len(d.lines)
	}
	return len(d.messageLines())
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
	hscroll int // runes of code scrolled off to the left
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
	chrome, used := renderDiffChrome(r)
	if used >= r.width {
		return truncateANSI(chrome, r.width)
	}
	return chrome + renderDiffContent(r, r.width-used)
}

// renderDiffChrome draws the columns in front of the code — the cursor bar and
// the line number — and reports how many columns they took.
//
// It is split out because those columns are the pane's own furniture rather
// than the diff: the mouse selection starts after them, so that dragging over
// a diff yields the diff rather than a column of line numbers nobody wants
// pasted anywhere.
func renderDiffChrome(r diffRow) (string, int) {
	var b strings.Builder
	// The bar column is always drawn, blank when the row is neither the
	// cursor nor selected, so rows never shift sideways.
	b.WriteString(r.bar.Render(" "))
	used := 1
	if used < r.width && r.numW > 0 {
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
	}
	return b.String(), used
}

// diffChromeWidth is how many columns renderDiffChrome takes, without going to
// the trouble of rendering it.
func diffChromeWidth(numW, width int) int {
	if width > 1 && numW > 0 {
		return 1 + numW + 1
	}
	return 1
}

// renderDiffContent draws the +/- marker and the line of code into exactly
// avail columns, padding or truncating to fill them.
//
// The marker is on this side of the split rather than with the chrome because
// it is the diff: a run of changed lines copied without it is no longer one.
func renderDiffContent(r diffRow, avail int) string {
	if avail <= 0 {
		return ""
	}
	var b strings.Builder
	used := 0

	// The gutter stays put and the code slides under it, so the +/- marker and
	// the line number keep saying which line is which however far right the
	// pane has been scrolled.
	code := []rune(r.code)
	if r.hscroll > 0 {
		code = code[min(r.hscroll, len(code)):]
	}
	gutW := len([]rune(r.gutter))
	runes := append([]rune(r.gutter), code...)
	// Spans and the emphasis are offsets into the whole code line, so what
	// scrolled off shifts them left; the gutter is where they stop.
	offset := gutW - r.hscroll
	fg := make([]lipgloss.TerminalColor, len(runes))
	for _, s := range r.spans {
		value, ok := r.palette.color(s.token)
		if !ok {
			continue
		}
		for at := max(s.start+offset, gutW); at < s.end+offset && at < len(runes); at++ {
			fg[at] = value
		}
	}
	changed := make([]bool, len(runes))
	if !r.emph.empty() {
		for at := max(r.emph.start+offset, gutW); at < r.emph.end+offset && at < len(runes); at++ {
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
		if used+w > avail {
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
	if truncated && used < avail {
		b.WriteString(r.base.Render("…"))
		used++
	}
	// The band runs to the right margin, so a changed line reads as a solid
	// block rather than as ragged text.
	if used < avail {
		b.WriteString(r.base.Render(strings.Repeat(" ", avail-used)))
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
