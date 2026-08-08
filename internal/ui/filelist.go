package ui

import (
	"strings"

	"github.com/charmbracelet/lipgloss"
	"github.com/discobox-ai/difftui/internal/git"
)

// fileList is one of the two file panes. It renders an icon per file exactly
// the way git-gui picks its bitmaps: from the index character in the staged
// pane and the working directory character in the unstaged pane.
type fileList struct {
	side   git.Side
	label  string // overrides the title, for the commit being looked at
	files  []git.File
	cursor int
	offset int
	anchor int // start of a visual selection, or -1
	width  int
	height int // rows available for file rows, title excluded
}

func newFileList(side git.Side) *fileList {
	return &fileList{side: side, anchor: -1}
}

func (l *fileList) title() string {
	if l.label != "" {
		return l.label
	}
	if l.side == git.SideIndex {
		return "Staged Changes (Will Commit)"
	}
	return "Unstaged Changes"
}

// setFiles replaces the contents. The cursor stays on the same file when that
// file is still in the list, and otherwise stays on the same row.
//
// The second half is what makes staging repeat. A file staged from this list
// leaves it, and the row it occupied is now the next file down, so holding
// Space walks down the list staging as it goes. Following the path instead
// would chase the file into the other list, and returning to the top would
// mean the second press staged something you had already passed. At the end of
// the list there is no next file, so the cursor comes to rest on the last one.
func (l *fileList) setFiles(files []git.File) {
	prev, row := l.currentPath(), l.cursor
	l.files = files
	l.anchor = -1
	l.cursor = row
	for i, f := range files {
		if f.Path == prev {
			l.cursor = i
			break
		}
	}
	l.clamp()
}

func (l *fileList) currentPath() string {
	if f := l.current(); f != nil {
		return f.Path
	}
	return ""
}

func (l *fileList) current() *git.File {
	if l.cursor < 0 || l.cursor >= len(l.files) {
		return nil
	}
	return &l.files[l.cursor]
}

// targets returns the paths a command should act on: the visual selection, or
// the file under the cursor when nothing is selected.
func (l *fileList) targets() []string {
	lo, hi, ok := l.selection()
	if !ok {
		if f := l.current(); f != nil {
			return []string{f.Path}
		}
		return nil
	}
	out := make([]string, 0, hi-lo+1)
	for i := lo; i <= hi; i++ {
		out = append(out, l.files[i].Path)
	}
	return out
}

// selection is the range of rows a visual selection covers, clamped to the
// list, and whether there is one at all.
func (l *fileList) selection() (int, int, bool) {
	if l.anchor < 0 || len(l.files) == 0 {
		return 0, 0, false
	}
	lo, hi := min(l.anchor, l.cursor), max(l.anchor, l.cursor)
	return max(lo, 0), min(hi, len(l.files)-1), true
}

// selected counts the files a selection covers.
func (l *fileList) selected() int {
	lo, hi, ok := l.selection()
	if !ok {
		return 0
	}
	return hi - lo + 1
}

// toggleVisual starts a selection on the current file, or leaves it again, the
// way V does in vim and in the diff pane.
func (l *fileList) toggleVisual() {
	if l.anchor >= 0 {
		l.anchor = -1
		return
	}
	l.anchor = l.cursor
}

// visual reports whether a selection is in progress.
func (l *fileList) visual() bool { return l.anchor >= 0 }

// clearVisual leaves visual selection.
func (l *fileList) clearVisual() { l.anchor = -1 }

// inSelection reports whether a row is inside the selection.
func (l *fileList) inSelection(i int) bool {
	lo, hi, ok := l.selection()
	return ok && i >= lo && i <= hi
}

func (l *fileList) move(delta int) {
	l.cursor += delta
	l.clamp()
}

func (l *fileList) moveTo(i int) {
	l.cursor = i
	l.clamp()
}

func (l *fileList) clamp() {
	if l.cursor >= len(l.files) {
		l.cursor = len(l.files) - 1
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

// icon maps a state character to a glyph, following git-gui's all_icons table.
func (l *fileList) icon(s git.State) string {
	if l.side == git.SideCommit {
		// A commit's status characters say what it did, not what is staged.
		switch s.Index() {
		case 'A':
			return "+"
		case 'D':
			return "✘"
		case 'R', 'C':
			return "⇄"
		case 'T':
			return "⇄"
		default:
			return "✎"
		}
	}
	var c byte
	if l.side == git.SideIndex {
		c = s.Index()
	} else if s.Index() == 'U' {
		c = 'U'
	} else {
		c = s.Work()
	}
	if l.side == git.SideIndex {
		switch c {
		case 'M', 'R', 'C':
			return "✔" // file_fulltick: fully staged
		case 'D':
			return "✘" // file_removed
		case 'U':
			return "!" // file_merge
		case 'T':
			return "⇄" // file_statechange
		default:
			return "▪" // file_plain: A, _
		}
	}
	switch c {
	case 'M':
		return "✎" // file_mod
	case 'D':
		return "?" // file_question: missing from the working directory
	case 'U':
		return "!" // file_merge
	case 'T':
		return "⇄" // file_statechange
	case 'O':
		return "+" // untracked
	default:
		return "▪"
	}
}

func (l *fileList) view(st *styles, focused bool) string {
	titleStyle := st.titleUnstaged
	switch l.side {
	case git.SideIndex:
		titleStyle = st.titleStaged
	case git.SideCommit:
		titleStyle = st.titleCommits
	}
	if !focused {
		titleStyle = st.titleDim
	}
	count := ""
	if n := len(l.files); n > 0 {
		count = plural(n, "file", "files")
	}
	title := renderTitle(titleStyle, l.title(), count, l.width)

	rows := make([]string, 0, l.height)
	if len(l.files) == 0 {
		rows = append(rows, st.dimText.Render(pad("  (nothing)", l.width)))
	}
	for i := l.offset; i < len(l.files) && len(rows) < l.height; i++ {
		f := l.files[i]
		name := f.Path
		if f.From != "" {
			name = f.From + " → " + f.Path
		}
		line := pad("  "+l.icon(f.State)+" "+name, l.width)
		// The cursor is the brighter band and the selection a dimmer shade of
		// it, so the row you are on still reads inside a selection.
		switch {
		case i == l.cursor && focused:
			rows = append(rows, st.sel.Render(line))
		case i == l.cursor:
			rows = append(rows, st.selInactive.Render(line))
		case l.inSelection(i):
			rows = append(rows, st.selRange.Render(line))
		default:
			rows = append(rows, line)
		}
	}
	for len(rows) < l.height {
		rows = append(rows, strings.Repeat(" ", max(l.width, 0)))
	}
	return lipgloss.JoinVertical(lipgloss.Left, append([]string{title}, rows...)...)
}
