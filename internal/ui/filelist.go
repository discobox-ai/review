package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/mattn/go-runewidth"
)

// fileList is the pane at the top left: every file the review touches, as a
// contents page for the stack beside it.
//
// It does not show a diff of its own. Moving the cursor scrolls the review to
// that file, and reading down the review moves the cursor here, so the two are
// always looking at the same thing — the list is a way of getting about a long
// review quickly, and a place to see at a glance how much of it is done.
type fileList struct {
	label   string // overrides the title, for the commit being read
	entries []fileEntry

	// hidden counts the files the filter left out, which are still part of
	// the review and still counted in the title.
	hidden int
	cursor int
	offset int
	width  int
	height int // rows available for file rows, title excluded
}

// fileEntry is one row: a path and every verdict standing against it.
type fileEntry struct {
	path      string
	from      string // the old name, when the file was renamed
	adds      int
	dels      int
	approved  bool
	nullified bool
	open      int    // unresolved threads
	total     int    // threads of any kind
	pending   string // what stops the file being committed, "" when it is
}

func newFileList() *fileList { return &fileList{} }

func (l *fileList) title() string {
	if l.label != "" {
		return l.label
	}
	if l.hidden > 0 {
		// The list is the index of what is on screen, so when it stops being
		// all of them it has to say so.
		return "Unfinished Files"
	}
	return "Changed Files"
}

// setEntries replaces the contents, keeping the cursor on the same file when
// that file is still there and otherwise on the same row.
func (l *fileList) setEntries(entries []fileEntry) {
	prev, row := l.currentPath(), l.cursor
	l.entries = entries
	l.cursor = row
	for i, e := range entries {
		if e.path == prev {
			l.cursor = i
			break
		}
	}
	l.clamp()
}

func (l *fileList) currentPath() string {
	if e := l.current(); e != nil {
		return e.path
	}
	return ""
}

func (l *fileList) current() *fileEntry {
	if l.cursor < 0 || l.cursor >= len(l.entries) {
		return nil
	}
	return &l.entries[l.cursor]
}

// selectPath puts the cursor on a path without disturbing anything when it is
// already there. It is how reading down the review keeps the list in step.
func (l *fileList) selectPath(path string) {
	if path == "" || l.currentPath() == path {
		return
	}
	for i, e := range l.entries {
		if e.path == path {
			l.moveTo(i)
			return
		}
	}
}

func (l *fileList) move(delta int) { l.cursor += delta; l.clamp() }

// scroll moves the viewport without moving the cursor, the way a wheel does.
func (l *fileList) scroll(delta int) {
	l.offset = min(max(l.offset+delta, 0), l.maxOffset())
}

func (l *fileList) moveTo(i int) { l.cursor = i; l.clamp() }

func (l *fileList) clamp() {
	l.cursor = min(max(l.cursor, 0), max(len(l.entries)-1, 0))
	if l.height <= 0 {
		return
	}
	if l.cursor < l.offset {
		l.offset = l.cursor
	}
	if l.cursor >= l.offset+l.height {
		l.offset = l.cursor - l.height + 1
	}
	l.offset = min(max(l.offset, 0), l.maxOffset())
}

// scrolls reports whether the list holds more files than it can show, which is
// the only time it wears a scrollbar.
func (l *fileList) scrolls() bool { return scrolls(len(l.entries), l.height, l.width) }

func (l *fileList) maxOffset() int { return listMaxOffset(len(l.entries), l.height) }

// verdict is the one glyph in front of a path: where the file stands with the
// reviewer. Everything a review is trying to get to the bottom of is in this
// column, so it is read down rather than across.
func (e fileEntry) verdict() string {
	switch {
	case e.approved:
		return "✔"
	case e.nullified:
		return "✻"
	case e.open > 0:
		return "◆"
	}
	return "○"
}

// name is what a row reads as once the chrome is taken off: the path, or both
// paths when the file was renamed.
func (l *fileList) name(i int) string {
	if i < 0 || i >= len(l.entries) {
		return ""
	}
	e := l.entries[i]
	if e.from != "" {
		return e.from + " → " + e.path
	}
	return e.path
}

// tally is the right hand end of a row: how much changed, and how much has
// been said about it.
func (e fileEntry) tally() string {
	var parts []string
	if e.adds > 0 {
		parts = append(parts, "+"+itoa(e.adds))
	}
	if e.dels > 0 {
		parts = append(parts, "−"+itoa(e.dels))
	}
	if e.total > 0 {
		n := e.open
		if n == 0 {
			n = e.total
		}
		parts = append(parts, "("+itoa(n)+")")
	}
	return strings.Join(parts, " ")
}

// row is a whole line of the pane, chrome included, laid out as
//
//	[ ][pending][verdict][ ][path…][ tally]
//
// The pending column repeats the review pane's own margin, so a file with
// work still to commit is marked the same way in both places.
func (l *fileList) row(i, w int) (chrome, body string) {
	e := l.entries[i]
	mark := " "
	if e.pending != "" {
		mark = pendingGlyph
	}
	chrome = " " + mark + e.verdict() + " "
	avail := max(w-width(chrome), 0)

	name, tally := l.name(i), e.tally()
	if tally != "" && width(tally)+2 <= avail {
		name = elideLeft(name, avail-width(tally)-1)
		gap := avail - width(name) - width(tally)
		return chrome, name + strings.Repeat(" ", max(gap, 1)) + tally
	}
	return chrome, pad(elideLeft(name, avail), avail)
}

// elideLeft shortens a path from the front, since the end of one is the half
// that says which file it is.
func elideLeft(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if width(s) <= w {
		return s
	}
	runes := []rune(s)
	for i := range runes {
		if candidate := "…" + string(runes[i:]); width(candidate) <= w {
			return candidate
		}
	}
	return runewidth.Truncate(s, w, "…")
}

func (l *fileList) view(st *styles, focused bool) string {
	titleStyle := st.titleFiles
	if !focused {
		titleStyle = st.titleDim
	}
	count := ""
	if n := len(l.entries) + l.hidden; n > 0 {
		approved := l.hidden // everything hidden is approved: that is why
		for _, e := range l.entries {
			if e.approved {
				approved++
			}
		}
		count = itoa(approved) + "/" + itoa(n)
	}
	title := renderTitle(titleStyle, l.title(), count, l.width)

	// The bar takes its column out of the rows, never out of the title, so the
	// pane keeps its width and the file names give up the room.
	bar := l.scrolls()
	rowW := l.width
	if bar {
		rowW = max(l.width-1, 0)
	}

	rows := make([]string, 0, l.height)
	if len(l.entries) == 0 {
		rows = append(rows, st.dimText.Render(pad("  (nothing to review)", rowW)))
	}
	for i := l.offset; i < len(l.entries) && len(rows) < l.height; i++ {
		chrome, body := l.row(i, rowW)
		e := l.entries[i]

		markSty := st.body
		if e.pending != "" {
			markSty = st.pendingBar
		}
		verdictSty := st.dimText
		switch {
		case e.approved:
			verdictSty = st.verdictOK
		case e.nullified:
			verdictSty = st.verdictStale
		case e.open > 0:
			verdictSty = st.verdictComment
		}

		var line string
		if width(chrome) >= 4 {
			runes := []rune(chrome)
			line = string(runes[0]) + markSty.Render(string(runes[1])) +
				verdictSty.Render(string(runes[2])) + string(runes[3])
		} else {
			line = chrome
		}
		line += body

		// The cursor is a band across the whole row, so it takes the plain
		// text: colours picked to read against the pane's own background
		// would fight one laid over the top of them.
		switch {
		case i == l.cursor && focused:
			rows = append(rows, st.sel.Render(pad(chrome+body, rowW)))
		case i == l.cursor:
			rows = append(rows, st.selInactive.Render(pad(chrome+body, rowW)))
		default:
			rows = append(rows, line)
		}
	}
	for len(rows) < l.height {
		rows = append(rows, strings.Repeat(" ", max(rowW, 0)))
	}
	if bar {
		attachVScroll(st, rows, blankTrack, len(l.entries), l.offset, l.maxOffset())
	}
	return lipgloss.JoinVertical(lipgloss.Left, append([]string{title}, rows...)...)
}
