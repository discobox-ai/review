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
	files  []git.File
	cursor int
	offset int
	marked map[string]bool
	width  int
	height int // rows available for file rows, title excluded
}

func newFileList(side git.Side) *fileList {
	return &fileList{side: side, marked: map[string]bool{}}
}

func (l *fileList) title() string {
	if l.side == git.SideIndex {
		return "Staged Changes (Will Commit)"
	}
	return "Unstaged Changes"
}

// setFiles replaces the contents while keeping the cursor on the same path.
func (l *fileList) setFiles(files []git.File) {
	prev := l.currentPath()
	l.files = files
	for p := range l.marked {
		found := false
		for _, f := range files {
			if f.Path == p {
				found = true
				break
			}
		}
		if !found {
			delete(l.marked, p)
		}
	}
	l.cursor = 0
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

// targets returns the paths a command should act on: every marked file, or the
// file under the cursor when nothing is marked.
func (l *fileList) targets() []string {
	var out []string
	for _, f := range l.files {
		if l.marked[f.Path] {
			out = append(out, f.Path)
		}
	}
	if len(out) > 0 {
		return out
	}
	if f := l.current(); f != nil {
		return []string{f.Path}
	}
	return nil
}

func (l *fileList) toggleMark() {
	f := l.current()
	if f == nil {
		return
	}
	if l.marked[f.Path] {
		delete(l.marked, f.Path)
	} else {
		l.marked[f.Path] = true
	}
}

func (l *fileList) clearMarks() { l.marked = map[string]bool{} }

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
	if l.side == git.SideIndex {
		titleStyle = st.titleStaged
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
		mark := " "
		if l.marked[f.Path] {
			mark = "•"
		}
		name := f.Path
		if f.From != "" {
			name = f.From + " → " + f.Path
		}
		line := pad(mark+" "+l.icon(f.State)+" "+name, l.width)
		switch {
		case i == l.cursor && focused:
			rows = append(rows, st.sel.Render(line))
		case i == l.cursor:
			rows = append(rows, st.selInactive.Render(line))
		default:
			rows = append(rows, line)
		}
	}
	for len(rows) < l.height {
		rows = append(rows, strings.Repeat(" ", max(l.width, 0)))
	}
	return lipgloss.JoinVertical(lipgloss.Left, append([]string{title}, rows...)...)
}
