package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// A path too long for the pane is cut in the middle, so both ends survive:
// the front says where in the tree the file is and the end says which file it
// is, and a cut at either end throws one of those away.
func TestALongNameIsCutInTheMiddle(t *testing.T) {
	const path = "internal/ui/reviewview.go"
	for _, w := range []int{1, 2, 3, 9, 16, 20, 24, 25, 40} {
		got := elideMiddle(path, w)
		if width(got) > w {
			t.Errorf("elideMiddle(%q, %d) = %q, %d columns wide", path, w, got, width(got))
		}
		if w >= width(path) {
			if got != path {
				t.Errorf("elideMiddle(%q, %d) = %q, want it whole", path, w, got)
			}
			continue
		}
		head, tail, cut := strings.Cut(got, "…")
		if !cut {
			t.Errorf("elideMiddle(%q, %d) = %q, with nothing to say it was cut", path, w, got)
			continue
		}
		if !strings.HasPrefix(path, head) || !strings.HasSuffix(path, tail) {
			t.Errorf("elideMiddle(%q, %d) = %q, which is not the two ends of it", path, w, got)
		}
		// Room for a name at all means the name is the end that gets it.
		if w >= 4 && tail == "" {
			t.Errorf("elideMiddle(%q, %d) = %q, which threw away the file name", path, w, got)
		}
	}
}

// The cut is made at a slash whenever the name it stands in front of will fit,
// so what follows the ellipsis is a whole path segment. A name with a hole in
// the middle of it reads as a different file; a path with one does not.
func TestTheCutIsMadeAtASlash(t *testing.T) {
	for _, c := range []struct {
		path string
		w    int
		want string
	}{
		{"internal/ui/reviewview.go", 25, "internal/ui/reviewview.go"},
		{"internal/ui/reviewview.go", 24, "internal/…/reviewview.go"},
		{"internal/ui/reviewview.go", 18, "int…/reviewview.go"},
		{"cmd/discobox-review/main.go", 10, "c…/main.go"},
		// The name will not fit even on its own, and there is no slash to cut
		// at: the two ends share what there is instead.
		{"internal/ui/reviewview.go", 14, "intern…view.go"},
		{"averylongfilenamewithnodirectoryatall.go", 14, "averyl…tall.go"},
	} {
		if got := elideMiddle(c.path, c.w); got != c.want {
			t.Errorf("elideMiddle(%q, %d) = %q, want %q", c.path, c.w, got, c.want)
		}
	}
}

// The row keeps the pane's width whatever the name does to fit into it, and
// whatever the files beside it do to the name.
func TestARowFitsTheWidthItIsGiven(t *testing.T) {
	l := newFileList()
	l.setEntries(alignmentEntries())
	for w := 4; w <= 60; w++ {
		l.shown = nil // as a resize does
		for i := range l.entries {
			chrome, body := l.row(i, w)
			if got := width(chrome + body); got != w {
				t.Errorf("a row %d columns wide came out %d: %q", w, got, chrome+body)
			}
		}
	}
}

// alignmentEntries is a directory whose files disagree about how much room
// they need — a long name, a wide tally, a short row that would fit whole —
// beside two directories of their own.
func alignmentEntries() []fileEntry {
	return []fileEntry{
		{path: "internal/ui/reviewview.go", adds: 120, dels: 33, total: 2},
		{path: "internal/ui/model.go", adds: 4},
		{path: "internal/ui/view.go", adds: 12, total: 2},
		{path: "internal/git/git.go", adds: 3},
		{path: "README.md", approved: true},
	}
}

// nameColumn is where the slash in front of a file's name is drawn, counting
// from the left of the pane, and -1 for a row with no slash left in it. A
// tally holds no slash, so the last one on the row is always the path's.
func nameColumn(l *fileList, i, w int) int {
	chrome, body := l.row(i, w)
	j := strings.LastIndexByte(body, '/')
	if j < 0 {
		return -1
	}
	return width(chrome) + width(body[:j])
}

// The files of one directory are cut in one column: the eye finds a file by
// its name, and the names only line up if the slash in front of them does. A
// path that would have fitted whole is elided anyway to keep that column, which
// is the whole of what is being bought.
func TestADirectoryIsCutInOneColumn(t *testing.T) {
	l := newFileList()
	l.setEntries(alignmentEntries())
	const w = 34

	var want int
	for i, e := range l.entries[:3] {
		got := nameColumn(l, i, w)
		if i == 0 {
			want = got
		}
		if got != want {
			t.Errorf("%s puts its name at column %d, want %d", e.path, got, want)
		}
	}
	if _, body := l.row(1, w); !strings.HasPrefix(body, "in…/model.go") {
		t.Errorf("internal/ui/model.go drew %q — it fitted whole, but its "+
			"directory did not, so it should have been cut with the rest", body)
	}

	// A directory of its own is nobody else's business: git.go has room for
	// its whole path and keeps it.
	if _, body := l.row(3, w); !strings.HasPrefix(body, "internal/git/git.go") {
		t.Errorf("internal/git/git.go drew %q, want it whole", body)
	}
}

// A file with no room even for its own name is elided on its own rather than
// dragging the rest of its directory down to nothing.
func TestAnImpossibleNameDragsNobodyDown(t *testing.T) {
	l := newFileList()
	l.setEntries(alignmentEntries())
	const w = 30 // "/reviewview.go" alone will not fit beside +120 −33 (2)

	if _, body := l.row(0, w); strings.Contains(body, "/reviewview.go") {
		t.Errorf("reviewview.go drew %q, which it has no room for", body)
	}
	for _, i := range []int{1, 2} {
		if _, body := l.row(i, w); !strings.HasPrefix(body, "internal/…/") {
			t.Errorf("%s drew %q, want the column its own directory can afford",
				l.entries[i].path, body)
		}
	}
	if a, b := nameColumn(l, 1, w), nameColumn(l, 2, w); a != b {
		t.Errorf("the two rows that could line up did not: columns %d and %d", a, b)
	}
}

// ---------------------------------------------------------------------------
// The rule between the columns
// ---------------------------------------------------------------------------

func TestTheFileListCanBeWidened(t *testing.T) {
	m := drive(t, newTestModel(t), tea.WindowSizeMsg{Width: 140, Height: 40})
	was, review := m.files.width, m.view.width

	m = drive(t, m, letterKey(">"))
	if m.files.width != was+resizeStep {
		t.Errorf("> made the list %d columns, want %d", m.files.width, was+resizeStep)
	}
	if m.view.width != review-resizeStep {
		t.Errorf("the review is %d columns, want %d — the room came from somewhere else",
			m.view.width, review-resizeStep)
	}
	if m.rects[focusFiles].w != m.files.width {
		t.Errorf("the list is drawn %d columns wide but clicked as %d",
			m.files.width, m.rects[focusFiles].w)
	}

	m = drive(t, m, letterKey("<"), letterKey("<"))
	if m.files.width != was-resizeStep {
		t.Errorf("< made the list %d columns, want %d", m.files.width, was-resizeStep)
	}

	// Alt and an arrow is the same verb for a hand on the arrows.
	m = drive(t, m, tea.KeyPressMsg{Code: tea.KeyRight, Mod: tea.ModAlt})
	if m.files.width != was {
		t.Errorf("Alt-→ made the list %d columns, want %d", m.files.width, was)
	}
}

// However hard the rule is pushed, the review keeps enough of the window to be
// worth reading and the list keeps enough to say anything at all.
func TestTheRuleStopsBeforeItEatsAPane(t *testing.T) {
	m := drive(t, newTestModel(t), tea.WindowSizeMsg{Width: 140, Height: 40})
	for i := 0; i < 100; i++ {
		m = drive(t, m, letterKey(">"))
	}
	if m.view.width < minReviewWidth {
		t.Errorf("the review is down to %d columns", m.view.width)
	}
	for i := 0; i < 100; i++ {
		m = drive(t, m, letterKey("<"))
	}
	if m.files.width < minLeftWidth {
		t.Errorf("the list is down to %d columns", m.files.width)
	}
	_ = m.frame()
}

// Widening always widens. On a window too small to hold both columns
// comfortably the ceiling would otherwise sit under the width already drawn,
// and a press of > would narrow the column it was asked to widen.
func TestWideningNeverNarrows(t *testing.T) {
	for _, w := range []int{60, 80, 100, 140, 200} {
		m := drive(t, newTestModel(t), tea.WindowSizeMsg{Width: w, Height: 30}, altKey("1"))
		was := m.files.width
		m = drive(t, m, letterKey(">"))
		if m.files.width < was {
			t.Errorf("in a %d column window > took the list from %d to %d",
				w, was, m.files.width)
		}
	}
}

// The keys say why they did nothing rather than doing nothing quietly, when
// the column they move is not on the screen at all.
func TestResizingSaysWhyItCannot(t *testing.T) {
	m := drive(t, newTestModel(t), tea.WindowSizeMsg{Width: 80, Height: 30}, altKey("2"))
	if !m.leftCollapsed() {
		t.Fatal("the left column is still drawn")
	}
	m = drive(t, m, letterKey(">"))
	if m.status == "" {
		t.Error("> with the list folded away said nothing")
	}
}

// The width set by hand outlives a resize of the window, and gives way to it
// only when the window is too small to keep it.
func TestTheWidthSetByHandIsKept(t *testing.T) {
	m := drive(t, newTestModel(t), tea.WindowSizeMsg{Width: 140, Height: 40}, letterKey(">"), letterKey(">"))
	want := m.files.width
	m = drive(t, m, tea.WindowSizeMsg{Width: 160, Height: 40})
	if m.files.width != want {
		t.Errorf("a wider window moved the rule to %d, want %d", m.files.width, want)
	}
}

// The keys that move the rule are offered on the bar while the list has the
// focus: it is the pane they are about, and a name it had to cut is what sends
// a hand looking for them.
func TestTheListOffersTheWidthKeys(t *testing.T) {
	m := drive(t, newTestModel(t), tea.WindowSizeMsg{Width: 140, Height: 40}, altKey("1"))
	if hint := m.hints(); !strings.Contains(hint, "<") || !strings.Contains(hint, ">") {
		t.Errorf("the list offers %q, which does not name the width keys", hint)
	}
	m = drive(t, m, letterKey("f"))
	if hint := m.hints(); !strings.Contains(hint, "<") || !strings.Contains(hint, ">") {
		t.Errorf("with the finished files hidden the list offers %q", hint)
	}
}

func TestDraggingTheRuleMovesIt(t *testing.T) {
	m := drive(t, newTestModel(t), tea.WindowSizeMsg{Width: 140, Height: 40})
	col := m.files.width
	focus := m.focus

	m = drive(t, m, pressAt(col, 5), dragTo(col+8, 9), releaseAt(col+8, 9))
	if m.files.width != col+8 {
		t.Errorf("the rule was dragged 8 columns and the list is %d, want %d",
			m.files.width, col+8)
	}
	if m.rule != nil {
		t.Error("the rule is still held after the button came up")
	}
	if m.mouse.hasSelection() {
		t.Error("dragging the rule selected text as well")
	}
	if m.focus != focus {
		t.Errorf("dragging the rule moved the focus to %v", m.focus)
	}
}
