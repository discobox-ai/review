package ui

// The panes' side of the mouse selection: what each one calls content, and
// where on screen that content starts. See [selectable] for what the two
// coordinate spaces are and why they differ.

// listIndent is the chrome a file list draws before a path: two spaces of
// indent, the one column status icon, and the space after it.
const listIndent = 4

// commitIndent is the branch history's indent. The hash is content — it is
// most of the reason to copy a line out of that pane — so only the two spaces
// in front of it are chrome.
const commitIndent = 2

// diffSel is the diff pane's content: the +/- marker and the code, with the
// cursor bar and the line numbers left behind as chrome.
type diffSel struct {
	d  *diffView
	st *styles
}

func (s diffSel) contentX() int {
	if len(s.d.lines) == 0 {
		// A message stands in for the patch and is drawn across the full
		// width, with no bar and no numbers in front of it.
		return 0
	}
	return min(diffChromeWidth(s.d.numW, s.d.rowW()), max(s.d.rowW(), 0))
}

// The width is the row's, not the pane's: the vertical scrollbar has already
// taken its column, and a bar is furniture rather than something to select.
func (s diffSel) contentWidth() int        { return max(s.d.rowW()-s.contentX(), 0) }
func (s diffSel) contentLines() (int, int) { return 0, s.d.rowCount() }
func (s diffSel) contentOffset() int       { return s.d.offset }

// bodyRows rather than height, so the horizontal scrollbar along the foot is
// not mistaken for a row of diff.
func (s diffSel) contentRows() int { return s.d.bodyRows() }

func (s diffSel) contentRow(line int) string {
	if len(s.d.lines) == 0 {
		msg := s.d.messageLines()
		if line < 0 || line >= len(msg) {
			return ""
		}
		return s.d.messageRow(s.st, msg[line])
	}
	if line < 0 || line >= len(s.d.lines) {
		return ""
	}
	return renderDiffContent(s.d.rowFor(line, s.st, false, false), s.contentWidth())
}

// listSel is a file list's content: the path, without the indent or the
// status icon in front of it, so that dragging down a list yields a list of
// paths rather than a column of glyphs.
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
func (s listSel) contentLines() (int, int) { return 0, len(s.l.files) }
func (s listSel) contentOffset() int       { return s.l.offset }
func (s listSel) contentRows() int         { return s.l.height }

func (s listSel) contentRow(line int) string {
	if line < 0 || line >= len(s.l.files) {
		return ""
	}
	return pad(s.l.name(line), s.contentWidth())
}

// commitsSel is the branch history's content: the hash and the subject.
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
