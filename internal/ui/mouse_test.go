package ui

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"regexp"
	"slices"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
	"github.com/discobox-ai/difftui/internal/git"
)

// bigDiff fills the diff pane with more lines than fit, so that the scrolling
// tests actually scroll. The test repository's own diffs are a few lines long
// and used to make these tests skip, which is the same as not having them.
func bigDiff(t *testing.T, m *Model, lines int) {
	t.Helper()
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/big.txt b/big.txt\n--- a/big.txt\n+++ b/big.txt\n@@ -1,%d +1,%d @@\n", lines, lines)
	for i := range lines {
		fmt.Fprintf(&b, "+added line %d\n", i)
	}
	m.diff.setPatch(git.File{Path: "big.txt"}, diffID{path: "big.txt"}, b.String())
	m.layout()
	if len(m.diff.lines) <= m.diff.bodyRows() {
		t.Fatalf("%d diff lines is not more than the %d rows on screen",
			len(m.diff.lines), m.diff.bodyRows())
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

func pressAt(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonLeft}
}

func rightPressAt(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: tea.MouseButtonRight}
}

func dragTo(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionMotion, Button: tea.MouseButtonLeft}
}

// releaseAt reports no button, the way a terminal using the original X10
// encoding does: which button came up is exactly what that encoding cannot
// say, so nothing may depend on it.
func releaseAt(x, y int) tea.MouseMsg {
	return tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionRelease, Button: tea.MouseButtonNone}
}

func wheelAt(x, y int, up bool) tea.MouseMsg {
	button := tea.MouseButtonWheelDown
	if up {
		button = tea.MouseButtonWheelUp
	}
	return tea.MouseMsg{X: x, Y: y, Action: tea.MouseActionPress, Button: button}
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
	return r.x + p.contentX() + col, r.y + 1 + row
}

// Where a pane is drawn and where it is clicked come out of the same
// arithmetic, so the two cannot drift apart: every pane's recorded rectangle
// must start on the row its own title is drawn on, at its own left edge.
func TestPaneRectsMatchWhereThePanesAreDrawn(t *testing.T) {
	m := newTestModel(t)
	rows := strings.Split(m.View(), "\n")

	seen := 0
	for f := focusArea(0); f < focusCount; f++ {
		r := m.rects[f]
		if r.w == 0 || f == focusCommit {
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
	if seen < 3 {
		t.Fatalf("only %d panes had rectangles; the layout was not measured", seen)
	}
}

// Clicking a row is how you get to it: the pane takes the focus and its cursor
// lands on the row that was clicked.
func TestClickFocusesThePaneAndTheRow(t *testing.T) {
	m := newTestModel(t)
	if len(m.workdir.files) < 2 {
		t.Fatalf("need two unstaged files, got %v", m.workdir.files)
	}
	// Start somewhere else, so the click has to move the focus.
	m = drive(t, m, altKey("3"))
	if m.focus != focusDiff {
		t.Fatalf("focus = %v, want the diff", m.focus)
	}

	x, y := contentPoint(t, m, focusWorkdir, 1, 0)
	m = drive(t, m, pressAt(x, y))

	if m.focus != focusWorkdir {
		t.Errorf("focus = %v, want the unstaged list", m.focus)
	}
	if m.workdir.cursor != 1 {
		t.Errorf("cursor = %d, want the row that was clicked (1)", m.workdir.cursor)
	}
}

// A press that never moves selects nothing: a click is a click, and only a
// drag is a selection.
func TestAPlainClickSelectsNothing(t *testing.T) {
	captureClipboard(t)
	m := newTestModel(t)
	x, y := contentPoint(t, m, focusWorkdir, 0, 0)
	m = drive(t, m, pressAt(x, y), releaseAt(x, y))
	if m.mouse.hasSelection() {
		t.Error("a click with no drag left a selection behind")
	}
}

// Dragging over the diff copies the diff: the +/- marker and the code, with
// the cursor bar and the line numbers left behind as the chrome they are.
func TestDragOverTheDiffCopiesCodeWithoutLineNumbers(t *testing.T) {
	m := newTestModel(t)
	clip := captureClipboard(t)

	// Find a changed line to drag over.
	row := -1
	for i := m.diff.offset; i < len(m.diff.lines) && i < m.diff.offset+m.diff.height; i++ {
		if m.diff.lines[i].Kind == '+' {
			row = i - m.diff.offset
			break
		}
	}
	if row < 0 {
		t.Fatalf("no added line on screen in:\n%s", m.diff.view(m.st, true))
	}
	want := "+ " + m.diff.texts[m.diff.offset+row]

	x, y := contentPoint(t, m, focusDiff, row, 0)
	end, _ := contentPoint(t, m, focusDiff, row, len([]rune(want))-1)
	m = drive(t, m, pressAt(x, y), dragTo(end, y), releaseAt(end, y))

	if !m.mouse.hasSelection() {
		t.Fatal("the drag left no selection")
	}
	got := copied(t, clip.String())
	if got != want {
		t.Errorf("copied %q, want %q", got, want)
	}
	if strings.ContainsAny(got, "0123456789") && !strings.ContainsAny(want, "0123456789") {
		t.Errorf("the copy picked up the line number column: %q", got)
	}
}

// Dragging down a file list copies paths, not the indent and status icon
// drawn in front of them.
func TestDragOverAFileListCopiesPaths(t *testing.T) {
	m := newTestModel(t)
	clip := captureClipboard(t)
	if len(m.workdir.files) < 2 {
		t.Fatalf("need two unstaged files, got %v", m.workdir.files)
	}

	x, y := contentPoint(t, m, focusWorkdir, 0, 0)
	endX, endY := contentPoint(t, m, focusWorkdir, 1, m.workdir.width)
	m = drive(t, m, pressAt(x, y), dragTo(endX, endY), releaseAt(endX, endY))

	want := m.workdir.name(0) + "\n" + m.workdir.name(1)
	if got := copied(t, clip.String()); got != want {
		t.Errorf("copied %q, want %q", got, want)
	}
}

// Two presses on the same cell inside the double click window take the whole
// word under them, however far it runs past where the pointer stopped.
func TestDoubleClickSelectsAWord(t *testing.T) {
	m := newTestModel(t)
	clip := captureClipboard(t)

	// The first file's path, clicked in the middle of its name.
	name := m.workdir.name(0)
	if len(name) < 3 {
		t.Fatalf("path %q is too short to click into", name)
	}
	x, y := contentPoint(t, m, focusWorkdir, 0, 1)
	m = drive(t, m, pressAt(x, y), releaseAt(x, y), pressAt(x, y), releaseAt(x, y))

	if !m.mouse.hasSelection() {
		t.Fatal("the double click selected nothing")
	}
	// The default word characters keep a dotted filename in one piece.
	if got := copied(t, clip.String()); got != name {
		t.Errorf("copied %q, want the whole word %q", got, name)
	}
}

// A third press takes the whole line.
func TestTripleClickSelectsALine(t *testing.T) {
	m := newTestModel(t)
	clip := captureClipboard(t)

	x, y := contentPoint(t, m, focusWorkdir, 0, 1)
	m = drive(t, m,
		pressAt(x, y), releaseAt(x, y),
		pressAt(x, y), releaseAt(x, y),
		pressAt(x, y), releaseAt(x, y),
	)
	if got := copied(t, clip.String()); got != m.workdir.name(0) {
		t.Errorf("copied %q, want the whole line %q", got, m.workdir.name(0))
	}
}

// The selection is drawn in reverse video over the cells it covers, and the
// row it is painted onto is exactly as wide afterwards as it was before.
func TestSelectionIsPaintedWithoutChangingTheLayout(t *testing.T) {
	captureClipboard(t)
	m := newTestModel(t)
	before := m.View()

	x, y := contentPoint(t, m, focusWorkdir, 0, 0)
	endX, _ := contentPoint(t, m, focusWorkdir, 0, 5)
	m = drive(t, m, pressAt(x, y), dragTo(endX, y))
	after := m.View()

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

// The wheel scrolls whatever is under the pointer and leaves the focus alone:
// reading ahead in a pane is not the same as going to work in it.
func TestWheelScrollsWithoutMovingTheFocus(t *testing.T) {
	m := newTestModel(t)
	m = drive(t, m, altKey("1"))
	if m.focus != focusWorkdir {
		t.Fatalf("focus = %v, want the unstaged list", m.focus)
	}
	bigDiff(t, &m, 200)

	r := m.rects[focusDiff]
	before := m.diff.offset
	m = drive(t, m, wheelAt(r.x+2, r.y+3, false))

	if m.diff.offset != before+wheelLines {
		t.Errorf("diff offset = %d, want %d", m.diff.offset, before+wheelLines)
	}
	if m.focus != focusWorkdir {
		t.Errorf("the wheel moved the focus to %v", m.focus)
	}
	if m.diff.cursor != 0 {
		t.Errorf("the wheel moved the cursor to %d", m.diff.cursor)
	}
}

// Dragging past the bottom edge scrolls the pane and keeps selecting, so a
// selection can reach past what is on screen.
func TestDragPastTheBottomScrolls(t *testing.T) {
	captureClipboard(t)
	m := newTestModel(t)
	bigDiff(t, &m, 200)
	r := m.rects[focusDiff]
	x, y := contentPoint(t, m, focusDiff, 0, 0)
	before := m.diff.offset

	m = drive(t, m, pressAt(x, y), dragTo(x, r.y+r.h+4))
	if m.diff.offset <= before {
		t.Errorf("offset = %d, want the drag to have scrolled past %d", m.diff.offset, before)
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

	x, y := contentPoint(t, m, focusWorkdir, 0, 0)
	endX, _ := contentPoint(t, m, focusWorkdir, 0, 4)
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
	x, y := contentPoint(t, m, focusWorkdir, 0, 0)
	endX, _ := contentPoint(t, m, focusWorkdir, 0, 4)
	m = drive(t, m, pressAt(x, y), dragTo(endX, y), releaseAt(endX, y))
	if !m.mouse.hasSelection() {
		t.Fatal("the drag left no selection")
	}
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.mouse.hasSelection() {
		t.Error("Esc left the selection on screen")
	}
}

// A selection names lines of content. A rescan replaces that content, so the
// selection goes with it rather than sliding onto text nobody selected.
func TestRescanDropsTheSelection(t *testing.T) {
	captureClipboard(t)
	m := newTestModel(t)
	x, y := contentPoint(t, m, focusWorkdir, 0, 0)
	endX, _ := contentPoint(t, m, focusWorkdir, 0, 4)
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
	x, y := contentPoint(t, m, focusWorkdir, 0, 0)
	endX, _ := contentPoint(t, m, focusWorkdir, 0, 4)
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

// A sideways wheel slides the diff, which is the one pane with anywhere to go.
func TestHorizontalWheelScrollsTheDiff(t *testing.T) {
	m := newTestModel(t)
	m.diff.long = m.diff.codeWidth() + 200 // a line far past the right margin
	if m.diff.maxHOffset() <= 0 {
		t.Fatal("the diff has nothing to scroll sideways")
	}
	r := m.rects[focusDiff]

	m = drive(t, m, tea.MouseMsg{
		X: r.x + 2, Y: r.y + 2,
		Action: tea.MouseActionPress, Button: tea.MouseButtonWheelRight,
	})
	if m.diff.hoffset != hWheelStep {
		t.Errorf("hoffset = %d, want %d", m.diff.hoffset, hWheelStep)
	}
	m = drive(t, m, tea.MouseMsg{
		X: r.x + 2, Y: r.y + 2,
		Action: tea.MouseActionPress, Button: tea.MouseButtonWheelLeft,
	})
	if m.diff.hoffset != 0 {
		t.Errorf("hoffset = %d, want back to 0", m.diff.hoffset)
	}
}

// A scrollbar is furniture. It must not be selectable, and a selection running
// to the right margin must not paint over it.
func TestTheScrollbarIsNotSelectable(t *testing.T) {
	clip := captureClipboard(t)
	m := newTestModel(t)

	// A list long enough to want a bar.
	files := make([]git.File, 60)
	for i := range files {
		files[i] = git.File{Path: fmt.Sprintf("file%02d.txt", i), State: m.workdir.files[0].State}
	}
	m.workdir.setFiles(files)
	m.layout()
	if !m.workdir.scrolls() {
		t.Fatal("the list should want a scrollbar")
	}

	p := listSel{m.workdir}
	if got, want := p.rowW(), m.workdir.width-1; got != want {
		t.Errorf("row width = %d, want %d with a bar", got, want)
	}
	if p.contentX()+p.contentWidth() > m.workdir.width-1 {
		t.Errorf("content runs to column %d, into the bar at %d",
			p.contentX()+p.contentWidth(), m.workdir.width-1)
	}

	// Drag the whole first row and check the bar survives the highlight.
	x, y := contentPoint(t, m, focusWorkdir, 0, 0)
	endX, _ := contentPoint(t, m, focusWorkdir, 0, p.contentWidth()+10)
	m = drive(t, m, pressAt(x, y), dragTo(endX, y), releaseAt(endX, y))

	if got := copied(t, clip.String()); strings.ContainsAny(got, "█│") {
		t.Errorf("the copy picked up the scrollbar: %q", got)
	}
	rows := strings.Split(m.viewPane(focusWorkdir), "\n")
	if w, want := ansi.StringWidth(rows[1]), m.workdir.width; w != want {
		t.Errorf("highlighted row is %d columns, want %d — the bar was truncated", w, want)
	}
	if !strings.ContainsAny(ansi.Strip(rows[1]), "█ ") {
		t.Errorf("the bar column is gone: %q", ansi.Strip(rows[1]))
	}
}

// The horizontal scrollbar along the diff's foot is not a row of diff.
func TestTheDiffFootBarIsNotARow(t *testing.T) {
	m := newTestModel(t)
	m.diff.long = m.diff.codeWidth() + 200
	m.layout()
	if _, horz := m.diff.bars(); !horz {
		t.Skip("the diff does not want a horizontal bar here")
	}
	p := diffSel{m.diff, m.st}
	if p.contentRows() != m.diff.bodyRows() {
		t.Errorf("contentRows = %d, want bodyRows %d", p.contentRows(), m.diff.bodyRows())
	}
	if p.contentRows() >= m.diff.height {
		t.Errorf("contentRows = %d, should be short of height %d by the bar row",
			p.contentRows(), m.diff.height)
	}
}
