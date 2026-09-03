package ui

// The panes' side of the mouse selection: what each one calls content, and
// where on screen it starts. See [selectable] for what the two coordinate
// spaces are and why they differ.

// listIndent is the chrome the file list draws before a path: a space, the
// "not committed" column, the verdict glyph, and the space after it.
const listIndent = 4

// commitIndent is the history's indent. The hash is content — it is most of
// the reason to copy a line out of that pane — so only the two spaces in front
// of it are chrome.
const commitIndent = 2

// reviewSel is the review pane's content.
//
// Every row of that pane draws the same chrome, whether it is a line of code,
// a file's heading or a comment, so one offset describes the whole pane: the
// cursor bar, the uncommitted margin and the line numbers are left behind, and
// everything from the +/- marker rightwards is taken. Dragging down a review
// therefore yields the diff and the remarks made on it, in the order they were
// read, which is a thing worth pasting somewhere.
type reviewSel struct {
	v  *reviewView
	st *styles
}

func (s reviewSel) contentX() int {
	return min(s.v.chromeW(), max(s.v.rowW(), 0))
}

// The width is the row's, not the pane's: the vertical scrollbar has already
// taken its column, and a bar is furniture rather than something to select.
func (s reviewSel) contentWidth() int        { return max(s.v.rowW()-s.contentX(), 0) }
func (s reviewSel) contentLines() (int, int) { return 0, len(s.v.rows) }
func (s reviewSel) contentOffset() int       { return s.v.offset }

// bodyRows rather than height, so the horizontal scrollbar along the foot is
// not mistaken for a row of review.
func (s reviewSel) contentRows() int { return s.v.bodyRows() }

func (s reviewSel) contentRow(line int) string {
	if line < 0 || line >= len(s.v.rows) {
		return ""
	}
	if r := s.v.rows[line]; r.kind == rowDiff {
		return renderDiffContent(
			s.v.diffRowFor(r, s.st, s.v.rowW(), false, false), s.contentWidth())
	}
	return s.v.content(line, s.st, s.contentWidth())
}

// listSel is the file list's content: the path and its tally, without the
// verdict glyph in front of them, so that dragging down the list yields paths
// rather than a column of ticks.
type listSel struct{ l *fileList }

// rowW is how wide a row is drawn, once the scrollbar has taken its column.
func (s listSel) rowW() int {
	if s.l.scrolls() {
		return max(s.l.width-1, 0)
	}
	return max(s.l.width, 0)
}

func (s listSel) contentX() int            { return min(listIndent, max(s.rowW(), 0)) }
func (s listSel) contentWidth() int        { return max(s.rowW()-s.contentX(), 0) }
func (s listSel) contentLines() (int, int) { return 0, len(s.l.entries) }
func (s listSel) contentOffset() int       { return s.l.offset }
func (s listSel) contentRows() int         { return s.l.height }

func (s listSel) contentRow(line int) string {
	if line < 0 || line >= len(s.l.entries) {
		return ""
	}
	_, body := s.l.row(line, s.rowW())
	return pad(body, s.contentWidth())
}

// commitsSel is the history's content: the hash and the subject.
type commitsSel struct{ c *commitList }

// rowW is how wide a row is drawn, once the scrollbar has taken its column.
func (s commitsSel) rowW() int {
	if s.c.scrolls() {
		return max(s.c.width-1, 0)
	}
	return max(s.c.width, 0)
}

func (s commitsSel) contentX() int            { return min(commitIndent, max(s.rowW(), 0)) }
func (s commitsSel) contentWidth() int        { return max(s.rowW()-s.contentX(), 0) }
func (s commitsSel) contentLines() (int, int) { return 0, s.c.rows() }
func (s commitsSel) contentOffset() int       { return s.c.offset }
func (s commitsSel) contentRows() int         { return s.c.height }

func (s commitsSel) contentRow(line int) string {
	if line < 0 || line >= s.c.rows() {
		return ""
	}
	return pad(s.c.rowBody(line), s.contentWidth())
}

// fileSel is the file reader's content: the code, with the cursor bar, the
// changed-line margin and the line numbers left behind as the chrome they are
// — the same split the review makes, in the same columns.
type fileSel struct {
	v  *fileView
	st *styles
}

func (s fileSel) contentX() int {
	return min(diffChromeWidth(s.v.numW, s.v.width), max(s.v.width, 0))
}

func (s fileSel) contentWidth() int        { return max(s.v.width-s.contentX(), 0) }
func (s fileSel) contentLines() (int, int) { return 0, len(s.v.lines) }
func (s fileSel) contentOffset() int       { return s.v.offset }
func (s fileSel) contentRows() int         { return s.v.body() }

func (s fileSel) contentRow(line int) string {
	if line < 0 || line >= len(s.v.lines) {
		return ""
	}
	return renderDiffContent(s.v.rowFor(line, s.st, false), s.contentWidth())
}

// mdSel is the markdown reader's content: the rendered prose, without the
// changed-block markers down its left edge.
type mdSel struct{ v *mdView }

func (s mdSel) contentX() int {
	if len(s.v.jumps) == 0 {
		return 0
	}
	return min(mdGutter, max(s.v.width, 0))
}

func (s mdSel) contentWidth() int        { return max(s.v.width-s.contentX(), 0) }
func (s mdSel) contentLines() (int, int) { return 0, len(s.v.lines) }
func (s mdSel) contentOffset() int       { return s.v.offset }
func (s mdSel) contentRows() int         { return s.v.body() }

func (s mdSel) contentRow(line int) string {
	if line < 0 || line >= len(s.v.lines) {
		return ""
	}
	return truncateANSI(s.v.lines[line], s.contentWidth())
}
