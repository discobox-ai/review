package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/discobox-ai/difftui/internal/git"
)

// commitList is the third pane on the left: the commits this branch adds on
// top of its base, newest first, under a first row standing for the working
// tree itself.
//
// That first row is what makes the pane a place to be rather than a menu. The
// working tree is where you start and where staging happens; the commits below
// it are the same branch at earlier points. Moving between them changes what
// the pane above shows, so the whole left column reads as one question: which
// version of this branch am I looking at.
type commitList struct {
	commits []git.Commit
	cursor  int
	offset  int
	width   int
	height  int
}

func newCommitList() *commitList { return &commitList{} }

// workingRow is the index of the row standing for the working tree.
const workingRow = 0

// setCommits replaces the history, keeping the cursor on the same commit when
// it is still there. A commit that has gone — amended, rebased away — drops
// the cursor back to the working tree, which always exists.
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
}

// rowText is what one entry reads as. The working tree is named rather than
// hashed, since it has no hash to give.
func (l *commitList) rowText(i int) string {
	if i == workingRow {
		return "  ● working tree"
	}
	c := l.commits[i-1]
	return "  " + c.Short + " " + c.Subject
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
	title := renderTitle(titleStyle, "This Branch", count, l.width)

	rows := make([]string, 0, l.height)
	for i := l.offset; i < l.rows() && len(rows) < l.height; i++ {
		line := pad(l.rowText(i), l.width)
		switch {
		case i == l.cursor && focused:
			rows = append(rows, st.sel.Render(line))
		case i == l.cursor:
			rows = append(rows, st.selInactive.Render(line))
		case i == workingRow:
			// The working tree keeps its weight even unselected: it is where
			// the rest of the window normally lives.
			rows = append(rows, st.commitHead.Render(line))
		default:
			rows = append(rows, line)
		}
	}
	for len(rows) < l.height {
		rows = append(rows, strings.Repeat(" ", max(l.width, 0)))
	}
	return lipgloss.JoinVertical(lipgloss.Left, append([]string{title}, rows...)...)
}
