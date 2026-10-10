package ui

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/discobox-ai/review/internal/git"
)

// bigReview fills the review pane with more rows than fit, so that the
// scrolling tests actually scroll. The fixture's own diffs are a few lines
// long and used to make these tests skip, which is the same as not having
// them.
func bigReview(t *testing.T, m *Model, lines int) {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/big.txt b/big.txt\n--- a/big.txt\n+++ b/big.txt\n@@ -1,%d +1,%d @@\n", lines, lines)
	for i := range lines {
		fmt.Fprintf(&b, "+added line %d\n", i)
	}
	m.work = buildFiles(b.String(), "", nil)
	m.workBy = map[string]*reviewFile{}
	for _, f := range m.work {
		m.workBy[f.path] = f
	}
	m.commit = ""
	m.folds["big.txt"] = false // a file opens folded, and this one is for scrolling
	m.rebuild()
	if len(m.view.rows) <= m.view.bodyRows() {
		t.Fatalf("%d rows is not more than the %d on screen",
			len(m.view.rows), m.view.bodyRows())
	}
}

// hasReverseVideo reports whether a row turns reverse video on anywhere. The
// attribute is folded in with whatever else the cell carries, so "\x1b[7m" is
// only one of the spellings it comes in.
func hasReverseVideo(row string) bool {
	for _, sgr := range regexp.MustCompile(`\x1b\[([0-9;]*)m`).FindAllStringSubmatch(row, -1) {
		if slices.Contains(strings.Split(sgr[1], ";"), "7") {
			return true
		}
	}
	return false
}

func pressAt(x, y int) tea.MouseClickMsg {
	return tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft}
}

// blockPressAt is a press with Alt held, which selects a rectangle.
func blockPressAt(x, y int) tea.MouseClickMsg {
	return tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseLeft, Mod: tea.ModAlt}
}

func rightPressAt(x, y int) tea.MouseClickMsg {
	return tea.MouseClickMsg{X: x, Y: y, Button: tea.MouseRight}
}

func dragTo(x, y int) tea.MouseMotionMsg {
	return tea.MouseMotionMsg{X: x, Y: y, Button: tea.MouseLeft}
}

// releaseAt reports no button, the way a terminal using the original X10
// encoding does: which button came up is exactly what that encoding cannot
// say, so nothing may depend on it.
func releaseAt(x, y int) tea.MouseReleaseMsg {
	return tea.MouseReleaseMsg{X: x, Y: y, Button: tea.MouseNone}
}

func wheelAt(x, y int, up bool) tea.MouseWheelMsg {
	button := tea.MouseWheelDown
	if up {
		button = tea.MouseWheelUp
	}
	return tea.MouseWheelMsg{X: x, Y: y, Button: button}
}

// captureClipboard redirects copies so a test can read them back.
func captureClipboard(t *testing.T) *bytes.Buffer {
	t.Helper()
	buf := &bytes.Buffer{}
	prev := clipboard
	clipboard = buf
	t.Cleanup(func() { clipboard = prev })
	return buf
}

// copied is the text of the last OSC 52 sequence written. One gesture can
// copy more than once — the second press of a triple click copies the word
// before the third takes the line — and the last one is what the clipboard
// ends up holding.
func copied(t *testing.T, out string) string {
	t.Helper()
	const prefix = "\x1b]52;c;"
	i := strings.LastIndex(out, prefix)
	if i < 0 || !strings.HasSuffix(out, "\a") {
		t.Fatalf("not an OSC 52 copy: %q", out)
	}
	text, err := base64.StdEncoding.DecodeString(strings.TrimSuffix(out[i+len(prefix):], "\a"))
	if err != nil {
		t.Fatalf("OSC 52 payload does not decode: %v", err)
	}
	return string(text)
}

// contentPoint is the screen position of a content column on a body row of a
// pane, which is what a test means when it says "click the third character of
// the second line".
func contentPoint(t *testing.T, m Model, f focusArea, row, col int) (int, int) {
	t.Helper()
	p := m.selectableFor(f)
	if p == nil {
		t.Fatalf("pane %v has no content", f)
	}
	r := m.rects[f]
	if r.w == 0 {
		t.Fatalf("pane %v is not on screen", f)
	}
	return r.x + contentXAt(p, p.contentOffset()+row) + col, r.y + 1 + row
}

// firstDiffRow is the screen row of the first line of diff on show, which is
// where a test drags from when it wants text rather than a file's heading.
func firstDiffRow(t *testing.T, m Model) int {
	t.Helper()
	for i := m.view.offset; i < len(m.view.rows) && i < m.view.offset+m.view.bodyRows(); i++ {
		if m.view.rows[i].kind == rowDiff {
			return i - m.view.offset
		}
	}
	t.Fatal("no line of diff on screen")
	return 0
}

// headLead is how much of a heading comes before the name: the file's verdict
// and the space after it, then the band's own margin, the disclosure marker
// and the space after that.
const headLead = 5

// histRowText is what a row of the history holds once the indent in front of
// it is taken off, which is what dragging over it copies.
func histRowText(m Model, i int) string { return m.history.rowBody(i) }

// longHistory gives the branch more commits than the strip under the review
// can show, which is when it wears a scrollbar.
func longHistory(t *testing.T, m *Model, n int) {
	t.Helper()
	commits := make([]git.Commit, n)
	for i := range commits {
		commits[i] = git.Commit{
			Hash: fmt.Sprintf("%040d", i), Short: fmt.Sprintf("%07d", i),
			Subject: fmt.Sprintf("commit number %d", i),
		}
	}
	m.history.setCommits(commits)
	m.layout()
	if !m.history.scrolls() {
		t.Fatal("the history should want a scrollbar")
	}
}

// Where a pane is drawn and where it is clicked come out of the same
// arithmetic, so the two cannot drift apart: every pane's recorded rectangle
// must start on the row its own title is drawn on, at its own left edge.
func TestPaneRectsMatchWhereThePanesAreDrawn(t *testing.T) {
	m := newTestModel(t)
	rows := strings.Split(m.frame(), "\n")

	seen := 0
	for f := focusArea(0); f < focusCount; f++ {
		r := m.rects[f]
		if r.w == 0 || f == focusCompose {
			continue // not on screen in this layout
		}
		seen++
		if r.y < 0 || r.y >= len(rows) {
			t.Errorf("%v: rect row %d is off the frame", f, r.y)
			continue
		}
		plain := []rune(ansi.Strip(rows[r.y]))
		if r.x+r.w > len(plain) {
			t.Errorf("%v: rect runs to column %d, frame row is %d wide", f, r.x+r.w, len(plain))
			continue
		}
		// The band of the frame the rectangle claims must be the pane's own
		// first row: its title, drawn where the rectangle says it is.
		want := ansi.Strip(strings.SplitN(m.viewPane(f), "\n", 2)[0])
		if got := string(plain[r.x : r.x+r.w]); got != want {
			t.Errorf("%v: frame row %d columns %d..%d are\n  %q\nthe pane draws\n  %q",
				f, r.y, r.x, r.x+r.w, got, want)
		}
	}
	if seen < 2 {
		t.Fatalf("only %d panes had rectangles; the layout was not measured", seen)
	}
}

// Clicking a row is how you get to it: the pane takes the focus and its cursor
// lands on the row that was clicked.
func TestClickFocusesThePaneAndTheRow(t *testing.T) {
	m := newTestModel(t)
	if m.history.rows() < 2 {
		t.Fatalf("need a commit to click, got %d rows of history", m.history.rows())
	}
	if m.focus != focusReview {
		t.Fatalf("focus = %v, want the review", m.focus)
	}

	x, y := contentPoint(t, m, focusHistory, 1, 0)
	m = drive(t, m, pressAt(x, y))

	if m.focus != focusHistory {
		t.Errorf("focus = %v, want the history", m.focus)
	}
	if m.history.cursor != 1 {
		t.Errorf("cursor = %d, want the row that was clicked (1)", m.history.cursor)
	}
	// And clicking a commit is asking to read it.
	if c, ok := m.history.selected(); !ok || m.commit != c.Hash {
		t.Errorf("the review is on %q, want the commit that was clicked", m.commit)
	}
}

// A press that never moves selects nothing: a click is a click, and only a
// drag is a selection.
func TestAPlainClickSelectsNothing(t *testing.T) {
	captureClipboard(t)
	m := newTestModel(t)
	x, y := contentPoint(t, m, focusHistory, 0, 0)
	m = drive(t, m, pressAt(x, y), releaseAt(x, y))
	if m.mouse.hasSelection() {
		t.Error("a click with no drag left a selection behind")
	}
}

// Dragging over the review copies the diff: the +/- marker and the code, with
// the cursor bar, the uncommitted margin and the line numbers left behind as
// the chrome they are.
func TestDragOverTheReviewCopiesCodeWithoutLineNumbers(t *testing.T) {
	// Unified, where a row is one line: what the split view copies is its own
	// test, below.
	m := drive(t, newTestModel(t), letterKey("s"))
	if m.view.splitting() {
		t.Fatal("s did not unify the diff")
	}
	clip := captureClipboard(t)

	// Find an added line on screen to drag over.
	row, want := -1, ""
	for i := m.view.offset; i < len(m.view.rows) && i < m.view.offset+m.view.bodyRows(); i++ {
		r := m.view.rows[i]
		if r.kind != rowDiff || r.file < 0 {
			continue
		}
		f := m.view.files[r.file]
		at := m.view.liveSide(f, r.line)
		if at >= 0 && f.lines[at].Kind == '+' {
			row, want = i-m.view.offset, "+ "+f.texts[at]
			break
		}
	}
	if row < 0 {
		t.Fatalf("no added line on screen in:\n%s", plain(m))
	}

	x, y := contentPoint(t, m, focusReview, row, 0)
	end, _ := contentPoint(t, m, focusReview, row, len([]rune(want))-1)
	m = drive(t, m, pressAt(x, y), dragTo(end, y), releaseAt(end, y))

	if !m.mouse.hasSelection() {
		t.Fatal("the drag left no selection")
	}
	got := copied(t, clip.String())
	if got != want {
		t.Errorf("copied %q, want %q", got, want)
	}
}

// Dragging over a split row copies what is under the highlight. The two halves
// are two different lines, so a pane that copied the row the unified way would
// hand back the added line while the drag was over the removed one.
func TestDragOverASplitRowCopiesTheHalfUnderIt(t *testing.T) {
	m := newTestModel(t)
	if !m.view.splitting() {
		t.Skipf("the review pane is %d columns, not split", m.view.width)
	}
	clip := captureClipboard(t)

	// The row drawing -two beside +TWO, on screen.
	f := fileIn(t, m, "foo.txt")
	want := f.rowAt[2]
	row := -1
	for i := m.view.offset; i < len(m.view.rows) && i < m.view.offset+m.view.bodyRows(); i++ {
		if r := m.view.rows[i]; r.kind == rowDiff && r.line == want && m.view.files[r.file] == f {
			row = i - m.view.offset
			break
		}
	}
	if row < 0 {
		t.Fatalf("the changed row is not on screen:\n%s", plain(m))
	}

	// Drag the left half, from the start of its content to the rule.
	left, _ := m.view.halvesOf(m.view.rowW())
	x, y := contentPoint(t, m, focusReview, row, 0)
	end, _ := contentPoint(t, m, focusReview, row, left-m.view.chromeW()-1)
	m = drive(t, m, pressAt(x, y), dragTo(end, y), releaseAt(end, y))
	if !m.mouse.hasSelection() {
		t.Fatal("the drag left no selection")
	}
	got := copied(t, clip.String())
	if !strings.Contains(got, "two") {
		t.Errorf("dragging the left half copied %q, want the removed line -two", got)
	}
	if strings.Contains(got, "TWO") {
		t.Errorf("dragging the left half copied the added line: %q", got)
	}
}

// Two presses on the same cell inside the double click window take the whole
// word under them, however far it runs past where the pointer stopped.
func TestDoubleClickSelectsAWord(t *testing.T) {
	m := newTestModel(t)
	clip := captureClipboard(t)

	// The row standing for the whole review: "● everything from the base".
	x, y := contentPoint(t, m, focusHistory, workingRow, 4)
	m = drive(t, m, pressAt(x, y), releaseAt(x, y), pressAt(x, y), releaseAt(x, y))

	if !m.mouse.hasSelection() {
		t.Fatal("the double click selected nothing")
	}
	if got := copied(t, clip.String()); got != "everything" {
		t.Errorf("copied %q, want the whole word %q", got, "everything")
	}
}

// A third press takes the whole line.
func TestTripleClickSelectsALine(t *testing.T) {
	m := newTestModel(t)
	clip := captureClipboard(t)

	x, y := contentPoint(t, m, focusHistory, workingRow, 4)
	m = drive(t, m,
		pressAt(x, y), releaseAt(x, y),
		pressAt(x, y), releaseAt(x, y),
		pressAt(x, y), releaseAt(x, y),
	)
	if got, want := copied(t, clip.String()), histRowText(m, workingRow); got != want {
		t.Errorf("copied %q, want the whole line %q", got, want)
	}
}

// The selection is drawn in reverse video over the cells it covers, and the
// row it is painted onto is exactly as wide afterwards as it was before.
func TestSelectionIsPaintedWithoutChangingTheLayout(t *testing.T) {
	captureClipboard(t)
	m := newTestModel(t)
	before := m.frame()

	x, y := contentPoint(t, m, focusHistory, 0, 0)
	endX, _ := contentPoint(t, m, focusHistory, 0, 5)
	m = drive(t, m, pressAt(x, y), dragTo(endX, y))
	after := m.frame()

	beforeRows, afterRows := strings.Split(before, "\n"), strings.Split(after, "\n")
	if len(beforeRows) != len(afterRows) {
		t.Fatalf("the frame changed height: %d rows, was %d", len(afterRows), len(beforeRows))
	}
	for i := range afterRows {
		if w, want := ansi.StringWidth(afterRows[i]), ansi.StringWidth(beforeRows[i]); w != want {
			t.Errorf("row %d is %d columns wide, was %d", i, w, want)
		}
	}
	if before == after {
		t.Error("the selection is not drawn")
	}
	if !hasReverseVideo(afterRows[y]) {
		t.Errorf("row %d carries no reverse video:\n%q", y, afterRows[y])
	}
}

// The wheel scrolls whatever is under the pointer and takes the focus there,
// leaving the cursor where it was.
func TestWheelScrollsAndFocusesThePane(t *testing.T) {
	m := newTestModel(t)
	m = drive(t, m, altKey("2"))
	if m.focus != focusHistory {
		t.Fatalf("focus = %v, want the history", m.focus)
	}
	bigReview(t, &m, 200)

	r := m.rects[focusReview]
	before, cursor := m.view.offset, m.view.cursor
	m = drive(t, m, wheelAt(r.x+2, r.y+3, false))

	if m.view.offset != before+wheelLines {
		t.Errorf("review offset = %d, want %d", m.view.offset, before+wheelLines)
	}
	if m.focus != focusReview {
		t.Errorf("focus = %v after the wheel, want the review", m.focus)
	}
	if m.view.cursor != cursor {
		t.Errorf("the wheel moved the cursor to %d", m.view.cursor)
	}

	// Going back to a pane with the wheel works the same way.
	h := m.rects[focusHistory]
	m = drive(t, m, wheelAt(h.x+2, h.y+2, false))
	if m.focus != focusHistory {
		t.Errorf("focus = %v after the wheel over the history, want the history", m.focus)
	}

	// Coming back keeps scrolling from wherever the review was, rather than
	// laying out again and snapping back to the cursor.
	before = m.view.offset
	m = drive(t, m, wheelAt(r.x+2, r.y+3, false), wheelAt(r.x+2, r.y+3, false))
	if m.view.offset != before+2*wheelLines {
		t.Errorf("review offset = %d after two more ticks, want %d",
			m.view.offset, before+2*wheelLines)
	}
}

// Dragging past the bottom edge scrolls the pane and keeps selecting, so a
// selection can reach past what is on screen.
func TestDragPastTheBottomScrolls(t *testing.T) {
	captureClipboard(t)
	m := newTestModel(t)
	bigReview(t, &m, 200)
	r := m.rects[focusReview]
	// From a line of diff, not the file's heading: column 0 of a heading is
	// the disclosure marker, which folds rather than selects.
	x, y := contentPoint(t, m, focusReview, firstDiffRow(t, m), 0)
	before := m.view.offset

	m = drive(t, m, pressAt(x, y), dragTo(x, r.y+r.h+4))
	if m.view.offset <= before {
		t.Errorf("offset = %d, want the drag to have scrolled past %d", m.view.offset, before)
	}
	if !m.mouse.hasSelection() {
		t.Error("the drag past the edge stopped selecting")
	}
}

// Right clicking a showing selection copies it and drops it, the gesture the
// Windows terminals use.
func TestRightClickCopiesTheSelection(t *testing.T) {
	m := newTestModel(t)
	clip := captureClipboard(t)

	x, y := contentPoint(t, m, focusHistory, 0, 0)
	endX, _ := contentPoint(t, m, focusHistory, 0, 4)
	m = drive(t, m, pressAt(x, y), dragTo(endX, y), releaseAt(endX, y))
	clip.Reset()

	m = drive(t, m, rightPressAt(x, y))
	if clip.Len() == 0 {
		t.Fatal("the right click copied nothing")
	}
	if m.mouse.hasSelection() {
		t.Error("the right click left the selection on screen")
	}
}

// Esc drops a selection before it means anything else, so nobody is stuck
// behind a highlight.
func TestEscapeDropsTheSelection(t *testing.T) {
	captureClipboard(t)
	m := newTestModel(t)
	x, y := contentPoint(t, m, focusHistory, 0, 0)
	endX, _ := contentPoint(t, m, focusHistory, 0, 4)
	m = drive(t, m, pressAt(x, y), dragTo(endX, y), releaseAt(endX, y))
	if !m.mouse.hasSelection() {
		t.Fatal("the drag left no selection")
	}
	m = drive(t, m, namedKey(tea.KeyEscape))
	if m.mouse.hasSelection() {
		t.Error("Esc left the selection on screen")
	}
}

// A selection names lines of content. A rescan replaces that content, so the
// selection goes with it rather than sliding onto text nobody selected.
func TestRescanDropsTheSelection(t *testing.T) {
	captureClipboard(t)
	m := newTestModel(t)
	x, y := contentPoint(t, m, focusHistory, 0, 0)
	endX, _ := contentPoint(t, m, focusHistory, 0, 4)
	m = drive(t, m, pressAt(x, y), dragTo(endX, y), releaseAt(endX, y))
	if !m.mouse.hasSelection() {
		t.Fatal("the drag left no selection")
	}
	m = drive(t, m, m.rescanCmd()())
	if m.mouse.hasSelection() {
		t.Error("the rescan left a selection over content it replaced")
	}
}

// A resize re-wraps every row, so the lines a selection named are no longer
// the lines it was made over.
func TestResizeDropsTheSelection(t *testing.T) {
	captureClipboard(t)
	m := newTestModel(t)
	x, y := contentPoint(t, m, focusHistory, 0, 0)
	endX, _ := contentPoint(t, m, focusHistory, 0, 4)
	m = drive(t, m, pressAt(x, y), dragTo(endX, y), releaseAt(endX, y))
	if !m.mouse.hasSelection() {
		t.Fatal("the drag left no selection")
	}
	m = drive(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	if m.mouse.hasSelection() {
		t.Error("the resize left a selection over rows it re-drew")
	}
}

// The reader is where prose gets copied out of, so it selects too — and the
// changed-block markers down its left edge are chrome, not text.
func TestReaderSelectsProseWithoutTheChangeMarkers(t *testing.T) {
	clip := captureClipboard(t)
	m := newTestModel(t)

	m.md = newMdView("notes.md", "The quick brown fox\n\njumps over it\n", map[int]bool{1: true}, m.width, m.height)
	if len(m.md.jumps) == 0 {
		t.Fatal("no changed block, so no markers to leave behind")
	}

	// The first rendered line with words on it.
	row := -1
	for i, l := range m.md.lines {
		if strings.TrimSpace(ansi.Strip(l)) != "" {
			row = i
			break
		}
	}
	if row < 0 {
		t.Fatal("the reader rendered nothing")
	}
	want := strings.TrimRight(ansi.Strip(m.md.lines[row]), " ")

	p := mdSel{m.md}
	x, y := p.contentX(), 1+row-m.md.offset
	m = drive(t, m, pressAt(x, y), dragTo(x+len([]rune(want))-1, y), releaseAt(x+len([]rune(want))-1, y))

	got := copied(t, clip.String())
	if got != want {
		t.Errorf("copied %q, want %q", got, want)
	}
	if strings.ContainsAny(got, "▌▍") {
		t.Errorf("the copy picked up the change markers: %q", got)
	}
}

// The wheel scrolls the reader.
func TestWheelScrollsTheReader(t *testing.T) {
	m := newTestModel(t)
	m.md = newMdView("notes.md", strings.Repeat("A paragraph of prose.\n\n", 80), nil, m.width, m.height)
	if len(m.md.lines) <= m.md.body() {
		t.Fatal("the reader fits on screen, so there is nothing to scroll")
	}
	m = drive(t, m, wheelAt(10, 5, false))
	if m.md.offset != wheelLines {
		t.Errorf("reader offset = %d, want %d", m.md.offset, wheelLines)
	}
	m = drive(t, m, wheelAt(10, 5, true))
	if m.md.offset != 0 {
		t.Errorf("reader offset = %d, want back to 0", m.md.offset)
	}
}

// A sideways wheel slides the review, which is the one pane with anywhere to
// go: it draws lines too long for it rather than wrapping them.
func TestHorizontalWheelScrollsTheReview(t *testing.T) {
	m := newTestModel(t)
	m.view.long = m.view.codeWidth() + 200 // a line far past the right margin
	if m.view.maxHOffset() <= 0 {
		t.Fatal("the review has nothing to scroll sideways")
	}
	r := m.rects[focusReview]

	m = drive(t, m, tea.MouseWheelMsg{X: r.x + 2, Y: r.y + 2, Button: tea.MouseWheelRight})
	if m.view.hoffset != hWheelStep {
		t.Errorf("hoffset = %d, want %d", m.view.hoffset, hWheelStep)
	}
	m = drive(t, m, tea.MouseWheelMsg{X: r.x + 2, Y: r.y + 2, Button: tea.MouseWheelLeft})
	if m.view.hoffset != 0 {
		t.Errorf("hoffset = %d, want back to 0", m.view.hoffset)
	}
}

// A scrollbar is furniture. It must not be selectable, and a selection running
// to the right margin must not paint over it.
func TestTheScrollbarIsNotSelectable(t *testing.T) {
	clip := captureClipboard(t)
	m := newTestModel(t)
	longHistory(t, &m, 60)

	p := commitsSel{m.history}
	if got, want := p.rowW(), m.history.width-1; got != want {
		t.Errorf("row width = %d, want %d with a bar", got, want)
	}
	if p.contentX()+p.contentWidth() > m.history.width-1 {
		t.Errorf("content runs to column %d, into the bar at %d",
			p.contentX()+p.contentWidth(), m.history.width-1)
	}

	// Drag the whole first row and check the bar survives the highlight.
	x, y := contentPoint(t, m, focusHistory, 0, 0)
	endX, _ := contentPoint(t, m, focusHistory, 0, p.contentWidth()+10)
	m = drive(t, m, pressAt(x, y), dragTo(endX, y), releaseAt(endX, y))

	if got := copied(t, clip.String()); strings.ContainsAny(got, "█│") {
		t.Errorf("the copy picked up the scrollbar: %q", got)
	}
	rows := strings.Split(m.viewPane(focusHistory), "\n")
	if w, want := ansi.StringWidth(rows[1]), m.history.width; w != want {
		t.Errorf("highlighted row is %d columns, want %d — the bar was truncated", w, want)
	}
}

// The horizontal scrollbar along the review's foot is not a row of review.
func TestTheReviewFootBarIsNotARow(t *testing.T) {
	m := newTestModel(t)
	m.view.long = m.view.codeWidth() + 200
	m.layout()
	if _, horz := m.view.bars(); !horz {
		t.Skip("the review does not want a horizontal bar here")
	}
	p := reviewSel{m.view, m.st}
	if p.contentRows() != m.view.bodyRows() {
		t.Errorf("contentRows = %d, want bodyRows %d", p.contentRows(), m.view.bodyRows())
	}
	if p.contentRows() >= m.view.height {
		t.Errorf("contentRows = %d, should be short of height %d by the bar row",
			p.contentRows(), m.view.height)
	}
}

// Every row of the review starts its content in the same column, whatever kind
// of row it is, so a selection dragged down the pane takes the code and the
// comments about it and nothing else.
func TestEveryReviewRowStartsItsContentInTheSameColumn(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "one remark")

	want := m.view.chromeW()
	kinds := map[rowKind]bool{}
	for i, r := range m.view.rows {
		kinds[r.kind] = true
		row := m.view.render(i, m.st, m.view.rowW(), false, false)
		content := m.view.content(i, m.st, m.view.contentWidth())
		if r.kind == rowDiff {
			continue // drawn by the diff row renderer, measured below
		}
		if got := ansi.StringWidth(row) - ansi.StringWidth(content); got != want {
			t.Errorf("row %d (kind %v) has %d columns of chrome, want %d", i, r.kind, got, want)
		}
	}
	for _, k := range []rowKind{rowFile, rowDiff, rowThread} {
		if !kinds[k] {
			t.Errorf("no row of kind %v was drawn, so nothing was checked", k)
		}
	}
}

// ---------------------------------------------------------------------------
// The scrollbars, and the disclosure marker
// ---------------------------------------------------------------------------

// The ▾ in front of a file's name is a disclosure marker, so clicking it
// discloses.
func TestClickingTheFoldMarkerFolds(t *testing.T) {
	m := newTestModel(t)
	row, path := headingOnScreen(t, m)

	x, y := contentPoint(t, m, focusReview, row, 0)
	m = drive(t, m, pressAt(x, y), releaseAt(x, y))
	if !fileIn(t, m, path).folded {
		t.Errorf("clicking the marker on %s did not fold it", path)
	}
	m = drive(t, m, pressAt(x, y), releaseAt(x, y))
	if fileIn(t, m, path).folded {
		t.Errorf("clicking it again did not open %s", path)
	}
}

// headingOnScreen is the first file heading the review pane is drawing, as a
// row of the screen and the file it names.
func headingOnScreen(t *testing.T, m Model) (int, string) {
	t.Helper()
	for i := m.view.offset; i < len(m.view.rows) && i < m.view.offset+m.view.bodyRows(); i++ {
		if m.view.rows[i].kind == rowFile {
			return i - m.view.offset, m.view.files[m.view.rows[i].file].path
		}
	}
	t.Fatalf("no file heading on screen:\n%s", plain(m))
	return 0, ""
}

// The word naming the way the diff is drawn is the one part of the pane's
// title that names a choice, so it is the one part that offers to change it.
func TestClickingTheViewModeTogglesIt(t *testing.T) {
	m := newTestModel(t)
	if !m.view.splitting() {
		t.Skipf("the review pane is %d columns, too narrow to split", m.view.width)
	}
	if !strings.Contains(plain(m), "split") {
		t.Fatalf("the title does not name the view:\n%s", plain(m))
	}

	r := m.rects[focusReview]
	_, at, w := m.view.titleLabel()

	m = drive(t, m, pressAt(r.x+at, r.y))
	if m.view.splitting() {
		t.Error("clicking `split` did not unify the diff")
	}
	if !strings.Contains(plain(m), "unified") {
		t.Errorf("the title does not name the view it changed to:\n%s", plain(m))
	}

	// And back, at whatever width the other word takes.
	_, at, w = m.view.titleLabel()
	m = drive(t, m, pressAt(r.x+at+w-1, r.y))
	if !m.view.splitting() {
		t.Error("clicking `unified` did not split the diff")
	}
}

// The rest of the title is chrome: it takes the focus, and a second press
// zooms the pane. Only the word that names a choice is a button.
func TestClickingTheRestOfTheTitleStillFocuses(t *testing.T) {
	m := newTestModel(t)
	m.focus = focusHistory
	r := m.rects[focusReview]
	_, at, _ := m.view.titleLabel()

	split := m.view.splitting()
	m = drive(t, m, pressAt(r.x+at-2, r.y)) // just before the word
	if m.focus != focusReview {
		t.Error("clicking the title did not take the focus")
	}
	if m.view.splitting() != split {
		t.Error("clicking beside the word changed the view")
	}
}

// Clicking one half of a split row is pointing at the line drawn in it, which
// is the plainest way of saying which side a comment is about.
func TestClickingAHalfPicksTheSide(t *testing.T) {
	m := newTestModel(t)
	if !m.view.splitting() {
		t.Fatalf("the review does not open split (pane %d columns)", m.view.width)
	}
	// The row drawing -two beside +TWO.
	f := fileIn(t, m, "foo.txt")
	want := f.rowAt[2]
	row := -1
	for i := m.view.offset; i < len(m.view.rows) && i < m.view.offset+m.view.bodyRows(); i++ {
		if r := m.view.rows[i]; r.kind == rowDiff && r.line == want && m.view.files[r.file] == f {
			row = i - m.view.offset
			break
		}
	}
	if row < 0 {
		t.Fatal("the changed row is not on screen")
	}

	r := m.rects[focusReview]
	left, _ := m.view.halvesOf(m.view.rowW())
	y := r.y + 1 + row

	m = drive(t, m, pressAt(r.x+left+4, y))
	if m.view.onOld {
		t.Error("clicking the right half put the cursor on the old side")
	}
	if _, l, ok := m.view.line(); !ok || l.Kind != '+' {
		t.Errorf("clicking the right half landed on %+v, want the added line", l)
	}

	m = drive(t, m, pressAt(r.x+left-4, y))
	if !m.view.onOld {
		t.Error("clicking the left half did not put the cursor on the old side")
	}
	if _, l, ok := m.view.line(); !ok || l.Kind != '-' {
		t.Errorf("clicking the left half landed on %+v, want the removed line", l)
	}
}

// The whole heading discloses the file, not only the marker in front of it:
// the band is the thing you are pointing at.
func TestClickingAnywhereOnTheHeadingFolds(t *testing.T) {
	m := newTestModel(t)
	row, path := headingOnScreen(t, m)

	for _, col := range []int{0, headLead, 12} {
		want := !fileIn(t, m, path).folded
		x, y := contentPoint(t, m, focusReview, row, col)
		m = drive(t, m, pressAt(x, y), releaseAt(x, y))
		if got := fileIn(t, m, path).folded; got != want {
			t.Errorf("clicking column %d of %s left folded=%v, want %v", col, path, got, want)
		}
	}
}

// Dragging over the heading is not clicking it. The file's name is text
// somebody may well want to copy, and a drag that took any of it copies rather
// than folding — which is what lets the whole band be a button at all.
func TestDraggingOverTheHeadingCopiesInsteadOfFolding(t *testing.T) {
	m := newTestModel(t)
	clip := captureClipboard(t)
	row, path := headingOnScreen(t, m)
	before := fileIn(t, m, path).folded

	x, y := contentPoint(t, m, focusReview, row, 0)
	end, _ := contentPoint(t, m, focusReview, row, len(path)+headLead-1)
	m = drive(t, m, pressAt(x, y), dragTo(end, y), releaseAt(end, y))

	if fileIn(t, m, path).folded != before {
		t.Error("dragging over the heading folded the file")
	}
	if !m.mouse.hasSelection() {
		t.Fatal("the drag left no selection")
	}
	if got := copied(t, clip.String()); !strings.Contains(got, path) {
		t.Errorf("copied %q, want it to hold %q", got, path)
	}
}

// A bar you can only look at is half a bar. Pressing one scrolls the pane to
// where you pointed, and dragging follows the pointer.
func TestDraggingTheVerticalBarScrolls(t *testing.T) {
	m := newTestModel(t)
	bigReview(t, &m, 400)
	m = drive(t, m, altKey("2"))
	b, ok := m.vbar(focusReview)
	if !ok {
		t.Fatal("the review is not wearing a vertical bar")
	}
	if m.view.offset != 0 {
		t.Fatalf("offset starts at %d", m.view.offset)
	}
	cursor := m.view.cursor

	// Press near the foot of the track: the thumb goes there and the pane
	// with it.
	m = drive(t, m, pressAt(b.pos, b.lo+b.length-1))
	if m.view.offset != m.view.maxOffset() {
		t.Errorf("offset = %d after pressing the end of the track, want %d",
			m.view.offset, m.view.maxOffset())
	}
	// Dragging a bar is reading ahead, the same gesture the wheel makes: the
	// cursor stays where it was.
	if m.view.cursor != cursor {
		t.Errorf("dragging the bar moved the cursor to %d", m.view.cursor)
	}
	if m.mouse.hasSelection() {
		t.Error("grabbing the bar started a text selection")
	}
	if m.focus != focusReview {
		t.Errorf("focus = %v after grabbing its bar, want the review", m.focus)
	}

	// And back up to the top by dragging.
	m = drive(t, m, dragTo(b.pos, b.lo))
	if m.view.offset != 0 {
		t.Errorf("offset = %d after dragging to the top, want 0", m.view.offset)
	}

	m = drive(t, m, releaseAt(b.pos, b.lo))
	if m.bar != nil {
		t.Error("letting go did not release the bar")
	}
	// The pane is not left scrolling with every later motion.
	m = drive(t, m, dragTo(b.pos, b.lo+b.length-1))
	if m.view.offset != 0 {
		t.Errorf("the bar kept following the pointer after release: offset %d", m.view.offset)
	}
}

// Grabbing the thumb itself must not make it jump: the pane only moves once
// the hand does.
func TestGrabbingTheThumbDoesNotJump(t *testing.T) {
	m := newTestModel(t)
	bigReview(t, &m, 400)
	m.view.scroll(120)
	before := m.view.offset

	b, ok := m.vbar(focusReview)
	if !ok {
		t.Fatal("no vertical bar")
	}
	lo, size := thumbSpan(b.length, b.total, b.window, b.offset, b.max)
	if size < 2 {
		t.Skip("the thumb is too small to grab off-centre")
	}
	m = drive(t, m, pressAt(b.pos, b.lo+lo))
	if m.view.offset != before {
		t.Errorf("grabbing the thumb moved the pane from %d to %d", before, m.view.offset)
	}
}

// The history wears one too.
func TestDraggingTheHistoryBar(t *testing.T) {
	m := newTestModel(t)
	longHistory(t, &m, 200)

	b, ok := m.vbar(focusHistory)
	if !ok {
		t.Fatal("the history is not wearing a bar")
	}
	m = drive(t, m, pressAt(b.pos, b.lo+b.length-1))
	if m.history.offset != m.history.maxOffset() {
		t.Errorf("offset = %d, want %d", m.history.offset, m.history.maxOffset())
	}
	if m.history.cursor != 0 {
		t.Errorf("dragging the bar moved the cursor to %d", m.history.cursor)
	}
}

// So does the review's foot, which is the one bar that runs the other way.
func TestDraggingTheHorizontalBarScrolls(t *testing.T) {
	m := newTestModel(t)
	m.view.long = m.view.codeWidth() + 400
	m.layout()
	b, ok := m.hbar(focusReview)
	if !ok {
		t.Skip("the review does not want a horizontal bar here")
	}
	m = drive(t, m, pressAt(b.lo+b.length-1, b.pos))
	if m.view.hoffset != m.view.maxHOffset() {
		t.Errorf("hoffset = %d after pressing the end of the track, want %d",
			m.view.hoffset, m.view.maxHOffset())
	}
	m = drive(t, m, dragTo(b.lo, b.pos), releaseAt(b.lo, b.pos))
	if m.view.hoffset != 0 {
		t.Errorf("hoffset = %d after dragging back, want 0", m.view.hoffset)
	}
}

// A pane with nothing to scroll wears no bar, so there is nothing to grab and
// a press there is an ordinary press.
func TestNoBarNothingToGrab(t *testing.T) {
	m := newTestModel(t)
	if m.history.scrolls() {
		t.Skip("the fixture's history is long enough to want a bar")
	}
	if _, ok := m.vbar(focusHistory); ok {
		t.Error("a list with nothing to scroll reports a bar")
	}
	r := m.rects[focusHistory]
	m = drive(t, m, pressAt(r.x+r.w-1, r.y+2))
	if m.bar != nil {
		t.Error("a press where no bar is drawn grabbed one")
	}
}

// ---------------------------------------------------------------------------
// The top bar
// ---------------------------------------------------------------------------

// barSpanFor is where one of the top bar's clickable parts is drawn.
func barSpanFor(t *testing.T, m Model, what barTarget) barSpan {
	t.Helper()
	_, spans := m.branchBar()
	for _, s := range spans {
		if s.what == what {
			return s
		}
	}
	t.Fatalf("the top bar has no clickable %v in %q", what, plain(m))
	return barSpan{}
}

// The branch name and the base are the two things on the top bar that name a
// choice, so clicking them offers to change it.
func TestClickingTheTopBarOffersTheChoice(t *testing.T) {
	m := newTestModel(t)

	// The branch name asks which branch to review.
	s := barSpanFor(t, m, barBranch)
	m = drive(t, m, pressAt(s.x, 0))
	if m.dialog == nil {
		t.Fatal("clicking the branch name offered nothing")
	}
	if !strings.Contains(m.dialog.title, "branch") {
		t.Errorf("dialog = %q, want the branch chooser", m.dialog.title)
	}
	m.dialog = nil

	// The base asks where to measure from.
	s = barSpanFor(t, m, barBase)
	m = drive(t, m, pressAt(s.x+s.w-1, 0))
	if m.dialog == nil {
		t.Fatal("clicking the base offered nothing")
	}
	if !strings.Contains(m.dialog.title, "base") {
		t.Errorf("dialog = %q, want the base chooser", m.dialog.title)
	}
}

// The bar is drawn where it says it is: every clickable span has to land on
// the text it claims.
func TestTheTopBarSpansMatchWhatIsDrawn(t *testing.T) {
	m := newTestModel(t)
	bar, spans := m.branchBar()
	row := []rune(ansi.Strip(bar))
	if len(spans) != 2 {
		t.Fatalf("%d clickable spans, want the branch and the base", len(spans))
	}
	for _, s := range spans {
		if s.x < 0 || s.x+s.w > len(row) {
			t.Fatalf("span %v runs to column %d, bar is %d wide", s.what, s.x+s.w, len(row))
		}
		got := string(row[s.x : s.x+s.w])
		switch s.what {
		case barBranch:
			if got != m.branch {
				t.Errorf("the branch span covers %q, want %q", got, m.branch)
			}
		case barBase:
			if !strings.Contains(got, m.base.Ref) {
				t.Errorf("the base span covers %q, want it to hold %q", got, m.base.Ref)
			}
		}
	}
}

// Elsewhere on the bar nothing happens: it is a bar, not a row of buttons.
func TestClickingTheRestOfTheTopBarDoesNothing(t *testing.T) {
	m := newTestModel(t)
	m = drive(t, m, pressAt(1, 0)) // inside "Reviewing: "
	if m.dialog != nil {
		t.Errorf("clicking the label opened %q", m.dialog.title)
	}
	m = drive(t, m, pressAt(m.width-4, 0)) // the repo name and the way out
	if m.dialog != nil {
		t.Errorf("clicking the right hand end opened %q", m.dialog.title)
	}
}

// A window too narrow to hold the base does not offer to change one that is
// not on screen.
func TestANarrowBarOffersOnlyWhatItShows(t *testing.T) {
	m := drive(t, newTestModel(t), tea.WindowSizeMsg{Width: 34, Height: 30})
	bar, spans := m.branchBar()
	for _, s := range spans {
		if s.x+s.w > ansi.StringWidth(bar) {
			t.Errorf("span %v runs past the end of a %d column bar", s.what, ansi.StringWidth(bar))
		}
		if s.what == barBase {
			t.Error("a bar with no room for the base still offers to change it")
		}
	}
}
