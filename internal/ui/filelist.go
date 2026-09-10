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

	// shown is the path each row draws, elided so that a directory's files cut
	// in the same column; shownW is the row width it was worked out for. See
	// fit, which fills them in.
	shown  []string
	shownW int
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
	l.entries, l.shown = entries, nil
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
		return "✓"
	case e.nullified:
		return "◈"
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
	chrome = l.chrome(i)
	avail := max(w-width(chrome), 0)

	l.fit(w)
	name, tally := l.shown[i], l.entries[i].tally()
	if tally != "" && width(tally)+2 <= avail {
		gap := avail - width(name) - width(tally)
		return chrome, name + strings.Repeat(" ", max(gap, 1)) + tally
	}
	return chrome, pad(name, avail)
}

// chrome is the four cells in front of a path: the margin, the mark for work
// still to commit, the verdict, and a space.
func (l *fileList) chrome(i int) string {
	mark := " "
	if l.entries[i].pending != "" {
		mark = pendingGlyph
	}
	return " " + mark + l.entries[i].verdict() + " "
}

// room is how many columns row i has for its path, once the chrome and the
// tally have taken theirs.
func (l *fileList) room(i, w int) int {
	avail := max(w-width(l.chrome(i)), 0)
	if tally := l.entries[i].tally(); tally != "" && width(tally)+2 <= avail {
		return avail - width(tally) - 1
	}
	return avail
}

// fit works out the path every row draws. It cannot be done a row at a time:
// rows sharing a directory are cut in the same column, so a path that would
// have fitted whole is elided anyway when a file beside it could not.
//
// That alignment is what makes a list of paths a column rather than a wall of
// text. The eye finds a file by its name, and the names line up only if the
// slash in front of them does — so a long name, or a wide tally, pushes the
// cut left for every file in its directory at once:
//
//	○ internal/ui/model.go  +4        ○ inte…/model.go            +4
//	○ internal/ui/view.go  +12 (2)    ○ inte…/view.go        +12 (2)
//	○ inte…/reviewview.go +120 −33    ○ inte…/reviewview.go +120 −33
//
// The result is cached against the width it was laid out for; setEntries drops
// it, which is the only way the rows change.
func (l *fileList) fit(w int) {
	if l.shown != nil && l.shownW == w {
		return
	}
	l.shownW, l.shown = w, make([]string, len(l.entries))

	// What each directory can show of itself: the least any one of its rows
	// can afford, since a column is only a column if they all cut in the same
	// place.
	heads := map[string]int{}
	for i := range l.entries {
		if dir, _, afford, ok := l.place(i, w); ok {
			if h, seen := heads[dir]; !seen || afford < h {
				heads[dir] = afford
			}
		}
	}
	for i := range l.entries {
		name, room := l.name(i), l.room(i, w)
		dir, base, _, ok := l.place(i, w)
		switch h := heads[dir]; {
		case ok && h < width(dir):
			// Cut where the directory column says, not where this row alone
			// would have. The head is never the whole of the directory here,
			// so what is drawn is never wider than the path itself.
			l.shown[i] = runewidth.Truncate(dir, h, "") + "…" + base
		case width(name) <= room:
			l.shown[i] = name
		default:
			l.shown[i] = elideMiddle(name, room)
		}
	}
}

// place is what one row would like to draw: the directory it is in, its own
// name with the slash in front of it, and how much of the directory it can
// afford to show — all of it, when the whole path fits.
//
// ok is false for a row that lines up with nothing: one with no directory at
// all, and one with no room even for its own name, which is elided on its own
// rather than dragging the rest of its directory down with it.
func (l *fileList) place(i, w int) (dir, base string, afford int, ok bool) {
	name, room := l.name(i), l.room(i, w)
	j := strings.LastIndexByte(name, '/')
	if j < 0 {
		return "", "", 0, false
	}
	dir, base = name[:j], name[j:]
	if width(name) <= room {
		return dir, base, width(dir), true
	}
	// The ellipsis takes a column and the directory must keep at least one,
	// or there is nothing left to line up.
	if width(base) > room-2 {
		return "", "", 0, false
	}
	return dir, base, room - 1 - width(base), true
}

// elideMiddle shortens a path from the middle, keeping both ends. Which file
// it is lives at the end of a path and where in the tree it sits lives at the
// front, so a cut at either end throws one of those away; the part a reader
// can fill in is the run of directories between them.
//
// The cut is made at a slash whenever there is room for the name it stands in
// front of, so what follows the ellipsis is a whole path segment:
//
//	internal/ui/filelist.go  →  inter…/filelist.go
//
// "inter…/filelist.go" reads as a file at the end of a path it does not have
// room to name, where "inter…filelist.go" reads as a name with a hole in the
// middle of it. Only when the name will not fit even on its own do the two
// ends fall back to sharing what there is between them.
func elideMiddle(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if width(s) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	runes := []rune(s)

	// Where the tail starts. The slash goes with the name, and w-2 leaves the
	// ellipsis its column and the front at least one of its own.
	tail := len(runes)
	if i := strings.LastIndexByte(s, '/'); i >= 0 && width(s[i:]) <= w-2 {
		tail = len([]rune(s[:i]))
	} else {
		for used, tailW := 0, w/2; tail > 0; tail-- {
			if used += runewidth.RuneWidth(runes[tail-1]); used > tailW {
				break
			}
		}
	}

	// The front takes whatever the tail leaves. It always stops short of the
	// tail: a name that fitted whole was never elided in the first place.
	headW := w - 1 - width(string(runes[tail:]))
	head := 0
	for used := 0; head < tail; head++ {
		if used += runewidth.RuneWidth(runes[head]); used > headW {
			break
		}
	}
	return string(runes[:head]) + "…" + string(runes[tail:])
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
