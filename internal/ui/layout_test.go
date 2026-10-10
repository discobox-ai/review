package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// freshTestModel is the window exactly as the program opens it: one column,
// every file folded to its heading, and the history under them. newTestModel
// is the same window with the files opened, which is where reading starts.
func freshTestModel(t *testing.T) Model {
	t.Helper()
	m := New(testRepo(t), "main", "Test")
	return drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40}, m.rescanCmd()())
}

// The window opens as a list of what changed: no list of files beside the
// review, every file shut, and the history unfolded at the foot.
func TestTheWindowOpensCollapsed(t *testing.T) {
	m := freshTestModel(t)

	if r := m.rects[focusReview]; r.x != 0 || r.w != m.width {
		t.Errorf("the review is at x=%d, %d wide, want the whole %d", r.x, r.w, m.width)
	}
	for _, f := range m.view.files {
		if !f.folded {
			t.Errorf("%s opened unfolded", f.path)
		}
	}
	if r := m.rects[focusHistory]; r.h != 1+m.history.rows() || r.y != m.rects[focusReview].y+m.rects[focusReview].h {
		t.Errorf("the history is at %+v, want its title and %d rows straight under the review",
			r, m.history.rows())
	}
	if !strings.Contains(plain(m), "[-] History") || !strings.Contains(plain(m), "everything from the base") {
		t.Errorf("the history did not open unfolded:\n%s", plain(m))
	}
}

// H folds the history and unfolds it again, and so does the button on its
// title. Folding it from inside hands the keys back to the review.
func TestTheHistoryFoldsAndUnfolds(t *testing.T) {
	m := freshTestModel(t)

	m = drive(t, m, letterKey("H"))
	if m.histOpen || !strings.Contains(plain(m), "[+] History") ||
		strings.Contains(plain(m), "everything from the base") {
		t.Fatalf("H did not fold the history:\n%s", plain(m))
	}
	m = drive(t, m, letterKey("H"))
	if !m.histOpen || !strings.Contains(plain(m), "[-] History") ||
		!strings.Contains(plain(m), "everything from the base") {
		t.Fatalf("H did not unfold the history:\n%s", plain(m))
	}
	if m.focus != focusReview {
		t.Errorf("H moved the focus to %v", m.focus)
	}

	r := m.rects[focusHistory]
	m = drive(t, m, pressAt(r.x+2, r.y), releaseAt(r.x+2, r.y))
	if m.histOpen {
		t.Error("clicking [-] did not fold the history")
	}
	r = m.rects[focusHistory]
	m = drive(t, m, pressAt(r.x+2, r.y), releaseAt(r.x+2, r.y))
	if !m.histOpen {
		t.Fatal("clicking [+] did not unfold the history")
	}

	// A row of it is still a commit to click on.
	r = m.rects[focusHistory]
	m = drive(t, m, pressAt(r.x+6, r.y+2), releaseAt(r.x+6, r.y+2))
	if m.focus != focusHistory {
		t.Fatalf("clicking a commit left the focus at %v", m.focus)
	}
	m = drive(t, m, letterKey("H"))
	if m.histOpen || m.focus != focusReview {
		t.Errorf("H from inside the history left open=%v focus=%v", m.histOpen, m.focus)
	}
}

// With the history folded there is one pane, so nothing moves the focus off
// it — not Tab, not ←, not the wheel over the title.
func TestNothingTakesTheFocusToWhatIsNotDrawn(t *testing.T) {
	m := drive(t, freshTestModel(t), letterKey("H"))
	r := m.rects[focusHistory]
	m = drive(t, m,
		tea.KeyPressMsg{Code: tea.KeyTab},
		tea.KeyPressMsg{Code: tea.KeyLeft},
		tea.MouseWheelMsg{X: r.x + 20, Y: r.y, Button: tea.MouseWheelDown},
	)
	if m.focus != focusReview {
		t.Errorf("the focus went to %v", m.focus)
	}
}

// A heading opens with the file's verdict, in front of the band rather than on
// it, and says how much changed in the colours those lines have further down.
func TestAHeadingOpensWithTheVerdict(t *testing.T) {
	m := freshTestModel(t)
	for i, r := range m.view.rows {
		if r.kind != rowFile {
			continue
		}
		f := m.view.files[r.file]
		row := m.view.render(i, m.st, m.view.rowW(), false, false)
		text := ansi.Strip(row)
		if want := " " + f.verdict() + "  ▸ " + f.path; !strings.HasPrefix(text, want) {
			t.Errorf("%s is headed %q, want it to start %q", f.path, text, want)
		}
		if w := ansi.StringWidth(row); w != m.view.rowW() {
			t.Errorf("%s: the heading is %d columns, want %d", f.path, w, m.view.rowW())
		}
		// The verdict and the space after it are drawn before the band's
		// background begins.
		band := strings.Index(row, "48;5;")
		if band < 0 || !strings.Contains(row[:band], f.verdict()) {
			t.Errorf("%s: the verdict is drawn on the band: %q", f.path, row)
		}
		if f.adds > 0 && !strings.Contains(row, m.st.fileBand.Foreground(m.st.statAdd.GetForeground()).Render("+"+itoa(f.adds))) {
			t.Errorf("%s: +%d is not drawn in the colour of an addition: %q", f.path, f.adds, row)
		}
		if f.dels > 0 && !strings.Contains(row, m.st.fileBand.Foreground(m.st.statDel.GetForeground()).Render("−"+itoa(f.dels))) {
			t.Errorf("%s: −%d is not drawn in the colour of a removal: %q", f.path, f.dels, row)
		}
	}
}

// A folded file is one row, so a run of them reads as a list; an open one ends
// with a blank row before the next heading.
func TestOnlyAnOpenFileHasAGapUnderIt(t *testing.T) {
	m := freshTestModel(t)
	for i, r := range m.view.rows {
		if r.kind != rowFile {
			t.Fatalf("row %d of a window that has just opened is a %v, want nothing but headings", i, r.kind)
		}
	}
	if len(m.view.rows) != len(m.view.files) {
		t.Fatalf("%d rows for %d folded files", len(m.view.rows), len(m.view.files))
	}

	m = drive(t, m, letterKey(" ")) // the first file
	last := -1
	for i, r := range m.view.rows {
		if r.file == 0 {
			last = i
		}
	}
	if m.view.rows[last].kind != rowGap {
		t.Errorf("the open file ends on a %v, want the gap", m.view.rows[last].kind)
	}
	if next := m.view.rows[last+1]; next.kind != rowFile || next.file != 1 {
		t.Errorf("after the gap comes %+v, want the next file's heading", next)
	}
}

// t goes to the next comment still waiting for an answer, and a comment in a
// file nobody has opened is still waiting: the file opens rather than being
// walked past.
func TestTheNextOpenCommentIsFoundInAFoldedFile(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "why shout?")

	// A window opened afresh on that review, with everything shut.
	m = modelFor(t, m.repo)
	for _, f := range m.view.files {
		delete(m.folds, f.path)
	}
	m.rebuild()
	m.view.moveTo(0)
	if !fileIn(t, m, "foo.txt").folded {
		t.Fatal("foo.txt is open before anything opened it")
	}

	m = drive(t, m, letterKey("t"))
	if fileIn(t, m, "foo.txt").folded {
		t.Fatal("t left the file with the open comment folded")
	}
	if r, ok := m.view.row(); !ok || r.kind != rowThread {
		t.Errorf("t landed on %+v, want the conversation (status %q)", r, m.status)
	}
	if fileIn(t, m, "bar.txt").folded == false {
		t.Error("t opened a file with nothing waiting in it")
	}
}

// A line asked for by number is a line asked to be seen, so 4g from the
// heading of a folded file opens it.
func TestGoingToALineOpensTheFile(t *testing.T) {
	m := freshTestModel(t)
	m = cursorTo(t, m, func(r reviewRow) bool {
		return r.kind == rowFile && m.view.files[r.file].path == "foo.txt"
	})
	m = drive(t, m, letterKey("4"), letterKey("g"))
	if fileIn(t, m, "foo.txt").folded {
		t.Fatal("4g left the file folded")
	}
	if _, l, ok := m.view.line(); !ok || l.New != 4 {
		t.Errorf("4g landed on %+v, want new line 4 (status %q)", l, m.status)
	}
}

// Approving a file folds it away again however it came to be open, and taking
// the approval back opens it: that is having more to say about it.
func TestApprovingFoldsAndWithdrawingOpens(t *testing.T) {
	m := freshTestModel(t)
	m = drive(t, m, letterKey("F")) // open them all
	for _, f := range m.view.files {
		if f.folded {
			t.Fatalf("F left %s folded", f.path)
		}
	}
	m = drive(t, m, letterKey("A"), letterKey("y"))
	for _, f := range m.view.files {
		if !f.folded {
			t.Errorf("%s stayed open after every file was approved", f.path)
		}
	}

	m.view.moveTo(0)
	path := m.view.pathAt()
	m = drive(t, m, letterKey("a"))
	if m.rv.Approved(path) {
		t.Fatalf("a did not withdraw the approval of %s", path)
	}
	if fileIn(t, m, path).folded {
		t.Errorf("%s is still folded after its approval was withdrawn", path)
	}
}

// ↑ off the top of the history goes back into the review, to the row the
// reader left it on.
func TestLeavingTheHistoryKeepsTheReviewsPlace(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "TWO")
	cursor := m.view.cursor

	m = drive(t, m, altKey("2"))
	if m.focus != focusHistory || m.history.cursor != 0 {
		t.Fatalf("focus=%v history cursor=%d", m.focus, m.history.cursor)
	}
	m = drive(t, m, namedKey(tea.KeyUp))
	if m.focus != focusReview {
		t.Fatalf("↑ off the top of the history left the focus at %v", m.focus)
	}
	if m.view.cursor != cursor {
		t.Errorf("the review cursor moved from row %d to %d", cursor, m.view.cursor)
	}
}

// With the review zoomed the history is not on screen, so H is asking to see
// it: the zoom ends and the history is there, rather than folded.
func TestHFromAZoomedReviewShowsTheHistory(t *testing.T) {
	m := drive(t, freshTestModel(t), letterKey("z"))
	if !m.zoom || strings.Contains(plain(m), "History") {
		t.Fatalf("zoom=%v, or the history is still drawn:\n%s", m.zoom, plain(m))
	}
	m = drive(t, m, letterKey("H"))
	if m.zoom || !m.histOpen || !strings.Contains(plain(m), "everything from the base") {
		t.Errorf("H left zoom=%v open=%v:\n%s", m.zoom, m.histOpen, plain(m))
	}
}

// A window too short for a row of history draws its title alone, says so with
// the button, and leaves the keyboard in the review.
func TestAWindowTooShortForTheHistoryFoldsIt(t *testing.T) {
	m := drive(t, freshTestModel(t), altKey("2"))
	if m.focus != focusHistory {
		t.Fatalf("focus = %v, want the history", m.focus)
	}
	m = drive(t, m, tea.WindowSizeMsg{Width: 100, Height: 5})
	if m.history.height > 0 {
		t.Skipf("a 5 row window still gives the history %d rows", m.history.height)
	}
	if m.focus != focusReview {
		t.Errorf("the focus is on %v, which has no rows", m.focus)
	}
	if !strings.Contains(plain(m), "[+] History") {
		t.Errorf("the title does not say the history is folded:\n%s", plain(m))
	}
	m = drive(t, m, namedKey(tea.KeyTab))
	if m.focus != focusReview {
		t.Errorf("Tab went to %v", m.focus)
	}
	// H cannot show it and does not pretend to: it says why, and leaves the
	// history as it was for when there is room again.
	m = drive(t, m, letterKey("H"))
	if !m.histOpen || !strings.Contains(m.status, "too short") {
		t.Errorf("H on a short window left open=%v status=%q", m.histOpen, m.status)
	}
	// And it comes back with the room.
	m = drive(t, m, tea.WindowSizeMsg{Width: 100, Height: 30})
	if !strings.Contains(plain(m), "[-] History") {
		t.Errorf("the history did not unfold again:\n%s", plain(m))
	}
}

// The strip is a third of the window at most, its title included.
func TestTheHistoryTakesAThirdAtMost(t *testing.T) {
	m := freshTestModel(t)
	for _, h := range []int{40, 20, 14, 11, 8} {
		m = drive(t, m, tea.WindowSizeMsg{Width: 100, Height: h})
		longHistory(t, &m, 60)
		body := h - 2
		if got := m.rects[focusHistory].h; got > max(body/3, 2) {
			t.Errorf("at %d rows the history takes %d of the %d under the bars", h, got, body)
		}
	}
}

// A window of nothing but headings has nothing running past its right margin,
// however long the lines inside the folded files are, so it wears no bar along
// its foot.
func TestFoldedFilesAskForNoHorizontalBar(t *testing.T) {
	m := newTestModel(t)
	writeFile(t, m.repo.Dir, "wide.txt", strings.Repeat("a very long line ", 40)+"\n")
	m = drive(t, m, m.rescanCmd()())
	if !fileIn(t, m, "wide.txt").folded {
		t.Fatal("the new file did not arrive folded")
	}
	for _, f := range m.view.files {
		m.folds[f.path] = true
	}
	m.rebuild()
	if _, horz := m.view.bars(); horz {
		t.Error("a window of folded files is wearing a horizontal bar")
	}

	m = onLine(t, m, "wide.txt", '+', "a very long line")
	if _, horz := m.view.bars(); !horz {
		t.Error("the open file's long line did not bring the bar back")
	}
}
