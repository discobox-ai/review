package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/discobox-ai/review/internal/git"
)

// commitList is the strip under the review: the commits this branch has made
// since the base, newest first, under a first row standing for the review as a
// whole.
//
// That first row is what makes the pane a place to be rather than a menu. The
// whole review — every change from the base to the working tree, committed or
// not — is what you normally want in front of you; the commits below it are
// the same work cut into the steps it was made in. Picking one narrows the
// review to that step without losing anything said about the rest.
type commitList struct {
	commits []git.Commit
	base    string // the ref the review is measured from, for the title
	cursor  int
	offset  int
	width   int
	height  int

	// button is drawn in front of the title when the pane can be folded to
	// it: "[-] " while it is open and "[+] " while it is shut. See buttonHit.
	button string
}

// buttonHit reports whether a column of the title, counted from the pane's
// left edge, is on the button. The title style pads one cell first.
func (l *commitList) buttonHit(x int) bool {
	return l.button != "" && x >= 1 && x < width(l.button)
}

func newCommitList() *commitList { return &commitList{} }

// workingRow is the index of the row standing for the review as a whole.
const workingRow = 0

// setCommits replaces the history, keeping the cursor on the same commit when
// it is still there. A commit that has gone — amended, rebased away — drops
// the cursor back to the whole review, which always exists.
func (l *commitList) setCommits(commits []git.Commit) {
	prev, wasBrowsing := l.selected()
	l.commits = commits
	l.cursor = workingRow
	if wasBrowsing {
		for i, c := range commits {
			if c.Hash == prev.Hash {
				l.cursor = i + 1
				break
			}
		}
	}
	l.clamp()
}

// rows is how many entries the pane holds: the working tree plus the commits.
func (l *commitList) rows() int { return len(l.commits) + 1 }

// selected is the commit under the cursor, and false when that is the working
// tree rather than a commit.
func (l *commitList) selected() (git.Commit, bool) {
	i := l.cursor - 1
	if i < 0 || i >= len(l.commits) {
		return git.Commit{}, false
	}
	return l.commits[i], true
}

func (l *commitList) move(delta int) {
	l.cursor += delta
	l.clamp()
}

// scroll moves the viewport without moving the cursor, the way a wheel does.
func (l *commitList) scroll(delta int) {
	l.offset = min(max(l.offset+delta, 0), l.maxOffset())
}

func (l *commitList) moveTo(i int) {
	l.cursor = i
	l.clamp()
}

func (l *commitList) clamp() {
	if l.cursor >= l.rows() {
		l.cursor = l.rows() - 1
	}
	if l.cursor < 0 {
		l.cursor = 0
	}
	if l.height <= 0 {
		return
	}
	if l.cursor < l.offset {
		l.offset = l.cursor
	}
	if l.cursor >= l.offset+l.height {
		l.offset = l.cursor - l.height + 1
	}
	if l.offset < 0 {
		l.offset = 0
	}
	if l.offset > l.maxOffset() {
		l.offset = l.maxOffset()
	}
}

// scrolls reports whether the history is longer than the pane can show, which
// is the only time it wears a scrollbar.
func (l *commitList) scrolls() bool { return scrolls(l.rows(), l.height, l.width) }

// maxOffset is the first row that can sit at the top with the last one still
// on screen.
func (l *commitList) maxOffset() int { return listMaxOffset(l.rows(), l.height) }

// rowText is what one entry reads as. The working tree is named rather than
// hashed, since it has no hash to give.
func (l *commitList) rowText(i int) string { return "  " + l.rowBody(i) }

// rowBody is the entry without its indent: the hash and the subject, or the
// name the working tree goes by.
func (l *commitList) rowBody(i int) string {
	if i == workingRow {
		return "● everything from the base"
	}
	if i < 1 || i > len(l.commits) {
		return ""
	}
	c := l.commits[i-1]
	return c.Short + " " + c.Subject
}

func (l *commitList) view(st *styles, focused bool) string {
	titleStyle := st.titleCommits
	if !focused {
		titleStyle = st.titleDim
	}
	count := ""
	if n := len(l.commits); n > 0 {
		count = plural(n, "commit", "commits")
	}
	name := "History"
	if l.base != "" {
		name = "History from " + l.base
	}
	title := renderTitle(titleStyle, l.button+name, count, l.width)
	if l.height <= 0 {
		return title // folded to its title
	}

	// The bar takes its column out of the rows, never out of the title, so the
	// pane keeps its width and the subjects give up the room.
	bar := l.scrolls()
	rowW := l.width
	if bar {
		rowW = max(l.width-1, 0)
	}

	rows := make([]string, 0, l.height)
	for i := l.offset; i < l.rows() && len(rows) < l.height; i++ {
		line := pad(l.rowText(i), rowW)
		switch {
		case i == l.cursor && focused:
			rows = append(rows, st.sel.Render(line))
		case i == l.cursor:
			rows = append(rows, st.selInactive.Render(line))
		case i == workingRow:
			// The whole review keeps its weight even unselected: it is where
			// the rest of the window normally lives.
			rows = append(rows, st.commitHead.Render(line))
		default:
			rows = append(rows, line)
		}
	}
	for len(rows) < l.height {
		rows = append(rows, strings.Repeat(" ", max(rowW, 0)))
	}
	if bar {
		attachVScroll(st, rows, vertTrack, l.rows(), l.offset, l.maxOffset())
	}
	return lipgloss.JoinVertical(lipgloss.Left, append([]string{title}, rows...)...)
}
