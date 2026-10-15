package ui

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/discobox-ai/review/internal/git"
	"github.com/discobox-ai/review/internal/review"
)

// ---------------------------------------------------------------------------
// The fixture
// ---------------------------------------------------------------------------

// testRepoDir builds the working tree the tests run against: a branch two
// commits ahead of main, with one uncommitted change, one staged change and
// one untracked file, so that everything the review has to say about a file
// has an example on screen.
//
//	main      initial          foo.txt bar.txt doc.md
//	feature   capitalise two   foo.txt line 2
//	          add beta         beta.go
//	worktree  four → FOUR      foo.txt line 4, not committed
//	          bar.txt          staged, not committed
//	          new.txt          untracked
func testRepoDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.name", "Test"},
		{"config", "user.email", "test@example.com"},
		{"config", "commit.gpgsign", "false"},
	} {
		gitRun(t, dir, args...)
	}
	writeFile(t, dir, "foo.txt", "one\ntwo\nthree\nfour\nfive\n")
	writeFile(t, dir, "bar.txt", "alpha\n")
	writeFile(t, dir, "doc.md", "# Title\n\nSome prose.\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "initial")

	gitRun(t, dir, "checkout", "-q", "-b", "feature")
	writeFile(t, dir, "foo.txt", "one\nTWO\nthree\nfour\nfive\n")
	gitRun(t, dir, "commit", "-qam", "capitalise two")
	writeFile(t, dir, "beta.go", "package beta\n\nfunc Beta() {}\n")
	gitRun(t, dir, "add", "beta.go")
	gitRun(t, dir, "commit", "-qm", "add beta")

	// Not committed: one edit in the working tree, one in the index, one file
	// git has never heard of.
	writeFile(t, dir, "foo.txt", "one\nTWO\nthree\nFOUR\nfive\n")
	writeFile(t, dir, "bar.txt", "beta\n")
	gitRun(t, dir, "add", "bar.txt")
	writeFile(t, dir, "new.txt", "brand new\n")
	return dir
}

func testRepo(t *testing.T) *git.Repo {
	t.Helper()
	r, err := git.Open(testRepoDir(t))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// drive feeds messages to the model and runs whatever commands come back,
// looping until there is nothing left to do. It is a miniature bubbletea
// runtime, enough to test the model without a terminal.
func drive(t *testing.T, m Model, msgs ...tea.Msg) Model {
	t.Helper()
	queue := append([]tea.Msg{}, msgs...)
	var cmds []tea.Cmd
	for i := 0; i < 400 && (len(queue) > 0 || len(cmds) > 0); i++ {
		if len(queue) == 0 {
			cmd := cmds[0]
			cmds = cmds[1:]
			if out := cmd(); out != nil {
				queue = append(queue, out)
			}
			continue
		}
		msg := queue[0]
		queue = queue[1:]
		// A batch is a bundle of commands rather than a message to handle.
		if batch, ok := msg.(tea.BatchMsg); ok {
			cmds = append(cmds, batch...)
			continue
		}
		// The reload tick sleeps for a second before it fires, which a test
		// has no reason to sit through: pickUpChanges is called directly by
		// the tests that care.
		if _, tick := msg.(reloadTickMsg); tick {
			continue
		}
		next, cmd := m.Update(msg)
		m = next.(Model)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return m
}

// newTestModel opens the fixture and reads it once, measured from main so the
// review holds the branch's whole change rather than only what is uncommitted.
func newTestModel(t *testing.T) Model {
	t.Helper()
	return modelFor(t, testRepo(t))
}

// shortTestModel is the same review in a window too short to hold it, which is
// where scrolling — and so where a pane's idea of the reader's place — matters.
func shortTestModel(t *testing.T) Model {
	t.Helper()
	m := New(testRepo(t), Options{Base: "main", Author: "Test"})
	return opened(drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 16}, m.rescanCmd()()))
}

func modelFor(t *testing.T, repo *git.Repo) Model {
	t.Helper()
	m := New(repo, Options{Base: "main", Author: "Test"})
	return opened(drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40}, m.rescanCmd()()))
}

// opened is a window with every file unfolded, the way a reader who has gone
// down the headings with Space leaves it. The window opens with them all shut
// — layout_test.go is about that — and nearly everything else here is about
// what is inside one.
func opened(m Model) Model {
	for _, f := range m.view.files {
		m.folds[f.path] = false
	}
	m.rebuild()
	return m
}

// runCmd runs a command the model handed back, if there is one.
func runCmd(t *testing.T, m Model, cmd tea.Cmd) Model {
	t.Helper()
	if cmd == nil {
		return m
	}
	if msg := cmd(); msg != nil {
		return drive(t, m, msg)
	}
	return m
}

// letterKey is one printable character pressed. A key carries both the code
// and the text it produced, and it is the text a binding is matched against.
func letterKey(s string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: []rune(s)[0], Text: s}
}

// altKey is the same key with Alt held, which produces no text of its own.
func altKey(s string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: []rune(s)[0], Mod: tea.ModAlt}
}

// ctrlKey is a control chord, which likewise produces no text.
func ctrlKey(s string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: []rune(s)[0], Mod: tea.ModCtrl}
}

// typeText presses every key of a string, in one go.
//
// It is one call rather than a loop of them because every keystroke in the
// composer hands back a command that waits for the caret to blink, and each of
// those cancels the one before it: sent together they all come back at once,
// where sent one at a time they are half a second each.
//
// It types rather than setting the value because the box sizes itself to what
// has been typed, and a value pushed straight in is not typing.
func typeText(t *testing.T, m Model, s string) Model {
	t.Helper()
	msgs := make([]tea.Msg, 0, len(s))
	for _, r := range s {
		msgs = append(msgs, letterKey(string(r)))
	}
	return drive(t, m, msgs...)
}

// namedKey is a key with no character of its own: Enter, Esc, an arrow.
func namedKey(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

// plain is the rendered window with the styling taken off, for the tests that
// ask what it says rather than how it looks.
func plain(m Model) string { return ansi.Strip(m.frame()) }

// reviewJSON reads the review file back the way an agent would.
func reviewJSON(t *testing.T, repo *git.Repo) *review.State {
	t.Helper()
	body, ok := repo.ReadStateFile(review.FileName)
	if !ok {
		t.Fatal("no review file was written")
	}
	st, err := review.Parse([]byte(body))
	if err != nil {
		t.Fatalf("the review file will not parse: %v\n%s", err, body)
	}
	return st
}

// fileScreen is how far down the review pane a file's heading is drawn,
// counted from the first body row. It is negative when the heading has been
// scrolled off the top, which is still an answer: the heading is not on
// screen.
func fileScreen(m Model, path string) (int, bool) {
	i, ok := m.view.fileRow(path)
	if !ok {
		return 0, false
	}
	return i - m.view.offset, true
}

// fileIn finds a file's section of the stack.
func fileIn(t *testing.T, m Model, path string) *reviewFile {
	t.Helper()
	for _, f := range m.view.files {
		if f.path == path {
			return f
		}
	}
	t.Fatalf("%s is not in the review; it holds %v", path, pathsOf(m))
	return nil
}

func pathsOf(m Model) []string {
	var out []string
	for _, f := range m.view.files {
		out = append(out, f.path)
	}
	return out
}

// cursorTo puts the review cursor on the first row matching a test.
func cursorTo(t *testing.T, m Model, want func(reviewRow) bool) Model {
	t.Helper()
	for i, r := range m.view.rows {
		if want(r) {
			m.view.moveTo(i)
			m.focus = focusReview
			return m
		}
	}
	t.Fatal("no row matched")
	return m
}

// onLine puts the cursor on a particular line of a particular file.
//
// A split row draws two lines and the cursor is on one of them, so the helper
// crosses to the side the wanted line is numbered on — which is what S does,
// and what a reader would have to do to get there.
func onLine(t *testing.T, m Model, path string, kind byte, text string) Model {
	t.Helper()
	// A file opens folded, and reading a line of one is opening it first.
	if i, ok := m.view.byPath[path]; ok && m.view.files[i].folded {
		m.folds[path] = false
		m.rebuild()
	}
	m.view.onOld = kind == '-' && m.view.splitting()
	return cursorTo(t, m, func(r reviewRow) bool {
		if r.kind != rowDiff || r.file < 0 {
			return false
		}
		f := m.view.files[r.file]
		if f.path != path {
			return false
		}
		// The row, not the line: in a split view a row can draw two lines, and
		// the cursor lands on the one the pane says it is on. A helper that
		// read f.lines[r.line] would agree with a bug that did the same.
		at := m.view.liveSide(f, r.line)
		if at < 0 || at >= len(f.lines) {
			return false
		}
		l := f.lines[at]
		return l.Kind == kind && strings.Contains(l.Text, text)
	})
}

// ---------------------------------------------------------------------------
// The stack
// ---------------------------------------------------------------------------

func TestViewRenders(t *testing.T) {
	m := newTestModel(t)
	view := plain(m)
	if strings.TrimSpace(view) == "" {
		t.Fatal("view is empty")
	}
	for _, want := range []string{"Reviewing:", "feature", "Review", "History"} {
		if !strings.Contains(view, want) {
			t.Errorf("view is missing %q:\n%s", want, view)
		}
	}
	if lines := strings.Split(m.frame(), "\n"); len(lines) != m.height {
		t.Errorf("frame has %d rows, want %d", len(lines), m.height)
	}
}

// The review is everything between the base and the working tree, whichever
// side of the index a change happens to be on. All four files are in one
// stack, in one scroll.
func TestTheReviewIsOneStackOfEveryChange(t *testing.T) {
	m := newTestModel(t)
	want := []string{"bar.txt", "beta.go", "foo.txt", "new.txt"}
	got := pathsOf(m)
	if len(got) != len(want) {
		t.Fatalf("review holds %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("file %d = %q, want %q", i, got[i], want[i])
		}
	}
	// doc.md did not change, so it is not part of the review at all.
	for _, p := range got {
		if p == "doc.md" {
			t.Error("an unchanged file is in the review")
		}
	}
	// Every file gets a heading of its own in the one buffer.
	headings := 0
	for _, r := range m.view.rows {
		if r.kind == rowFile {
			headings++
		}
	}
	if headings != 4 {
		t.Errorf("%d file headings in the stack, want 4", headings)
	}
}

// foo.txt has two changes from the base: one committed, one not. Only the
// uncommitted one wears the margin.
func TestOnlyUncommittedLinesWearTheMargin(t *testing.T) {
	m := newTestModel(t)
	f := fileIn(t, m, "foo.txt")

	var marked, unmarked []string
	for i, l := range f.lines {
		if l.Kind != '+' {
			continue
		}
		if f.marked(i) {
			marked = append(marked, l.Text)
		} else {
			unmarked = append(unmarked, l.Text)
		}
	}
	if len(marked) != 1 || !strings.Contains(marked[0], "FOUR") {
		t.Errorf("marked lines = %v, want only FOUR", marked)
	}
	if len(unmarked) != 1 || !strings.Contains(unmarked[0], "TWO") {
		t.Errorf("unmarked lines = %v, want only TWO", unmarked)
	}

	// An untracked file has not been committed at all, so every line of it is
	// marked.
	newFile := fileIn(t, m, "new.txt")
	for i, l := range newFile.lines {
		if l.Kind == '+' && !newFile.marked(i) {
			t.Errorf("untracked line %q is not marked", l.Text)
		}
	}
}

// The heading of each file says what still stands between it and a clean tree.
func TestFileHeadingsSayWhatIsNotCommitted(t *testing.T) {
	m := newTestModel(t)
	for path, want := range map[string]string{
		"foo.txt": "uncommitted",
		"bar.txt": "staged",
		"new.txt": "untracked",
	} {
		if got := heading(m, path); !strings.Contains(got, want) {
			t.Errorf("the heading for %s is %q, want it to mention %q", path, got, want)
		}
	}
	// beta.go is committed, so it has nothing outstanding to say.
	if got := heading(m, "beta.go"); strings.Contains(got, "uncommitted") {
		t.Errorf("a committed file is marked uncommitted: %q", got)
	}
}

// heading is a file's band in the review, drawn wide enough to hold all of it.
func heading(m Model, path string) string { return headingAt(m, path, 100) }

// ---------------------------------------------------------------------------
// Approving
// ---------------------------------------------------------------------------

func TestApproveWritesTheReviewFile(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "beta.go", '+', "package beta")
	m = drive(t, m, letterKey("a"))

	st := reviewJSON(t, m.src.(*git.Repo))
	if !st.Approved("beta.go", "Test") {
		t.Fatalf("beta.go was not approved: %s", st.Bytes())
	}
	f := st.VerdictOf("beta.go", "Test")
	if f.Digest == "" {
		t.Error("the approval carries no digest, so nothing could ever nullify it")
	}
	if f.By != "Test" {
		t.Errorf("approved by %q, want the git committer name", f.By)
	}
	if st.Branch != "feature" || st.Base != "main" {
		t.Errorf("review says branch %q base %q", st.Branch, st.Base)
	}

	// And pressing it again takes the approval back.
	m = drive(t, m, letterKey("a"))
	st = reviewJSON(t, m.src.(*git.Repo))
	if st.Approved("beta.go", "Test") {
		t.Error("a second press did not withdraw the approval")
	}
	if st.Nullified("beta.go", "Test") {
		t.Error("withdrawing an approval marked the file as having changed")
	}
}

// An approval is of a change. Editing the file afterwards nullifies it, and
// the file comes back marked as having been approved once rather than never.
func TestApprovalIsNullifiedWhenTheFileChanges(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = drive(t, m, letterKey("a"))
	if !reviewJSON(t, m.src.(*git.Repo)).Approved("foo.txt", "Test") {
		t.Fatal("foo.txt was not approved to begin with")
	}

	writeFile(t, m.src.(*git.Repo).Dir, "foo.txt", "one\nTWO\nthree\nFOUR\nFIVE\n")
	m = drive(t, m, m.rescanCmd()())

	st := reviewJSON(t, m.src.(*git.Repo))
	if st.Approved("foo.txt", "Test") {
		t.Error("the approval survived the file changing")
	}
	if !st.Nullified("foo.txt", "Test") {
		t.Error("the review does not record that the approval fell")
	}
	if !fileIn(t, m, "foo.txt").nullified {
		t.Error("the pane does not show the approval as nullified")
	}
	if !strings.Contains(plain(m), "nullified") {
		t.Errorf("the window says nothing about it:\n%s", plain(m))
	}
}

// Turning the context up and down redraws every hunk without touching a single
// approval: what was approved is the change, not the way it is being shown.
func TestContextDoesNotNullifyApprovals(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = drive(t, m, letterKey("a"))

	m = drive(t, m, letterKey("="), letterKey("="), letterKey("-"))
	if m.context == defaultContext {
		t.Fatal("the context did not change")
	}
	if !reviewJSON(t, m.src.(*git.Repo)).Approved("foo.txt", "Test") {
		t.Error("changing the diff context nullified an approval")
	}
	if !fileIn(t, m, "foo.txt").approved {
		t.Error("the pane lost the approval")
	}
}

// A file that has been approved and has nothing outstanding on it folds away,
// so what is left on screen is what is left to do.
func TestAnApprovedFileFoldsAway(t *testing.T) {
	m := newTestModel(t)
	before := countRows(m, rowDiff)

	m = onLine(t, m, "beta.go", '+', "package beta")
	m = drive(t, m, letterKey("a"))
	if !fileIn(t, m, "beta.go").folded {
		t.Fatal("an approved file with nothing outstanding did not fold")
	}
	if after := countRows(m, rowDiff); after >= before {
		t.Errorf("the stack still has %d diff rows, was %d", after, before)
	}

	// Space on its heading opens it again, and the choice sticks.
	m = cursorTo(t, m, func(r reviewRow) bool {
		return r.kind == rowFile && m.view.files[r.file].path == "beta.go"
	})
	m = drive(t, m, letterKey(" "))
	if fileIn(t, m, "beta.go").folded {
		t.Error("Space did not unfold the file")
	}
	m = drive(t, m, m.rescanCmd()())
	if fileIn(t, m, "beta.go").folded {
		t.Error("a rescan folded a file the reader had opened by hand")
	}
}

func countRows(m Model, kind rowKind) int {
	n := 0
	for _, r := range m.view.rows {
		if r.kind == kind {
			n++
		}
	}
	return n
}

// A on everything is worth having, and worth asking about first.
func TestApproveEverythingAsksFirst(t *testing.T) {
	m := newTestModel(t)
	m = drive(t, m, letterKey("A"))
	if m.dialog == nil {
		t.Fatal("A approved everything without asking")
	}
	if reviewJSON2(m.src.(*git.Repo)) != nil && reviewJSON2(m.src.(*git.Repo)).Open() != 0 {
		t.Error("something was written before the question was answered")
	}
	m = drive(t, m, letterKey("y"))
	st := reviewJSON(t, m.src.(*git.Repo))
	for _, p := range pathsOf(m) {
		if !st.Approved(p, "Test") {
			t.Errorf("%s was not approved", p)
		}
	}
}

// reviewJSON2 is reviewJSON without the fatal, for the cases that expect
// nothing to have been written yet.
func reviewJSON2(repo *git.Repo) *review.State {
	body, ok := repo.ReadStateFile(review.FileName)
	if !ok {
		return nil
	}
	st, err := review.Parse([]byte(body))
	if err != nil {
		return nil
	}
	return st
}

// ---------------------------------------------------------------------------
// Comments
// ---------------------------------------------------------------------------

// comment writes one against whatever the cursor is on.
func comment(t *testing.T, m Model, body string) Model {
	t.Helper()
	m = drive(t, m, letterKey("c"))
	if m.compose == nil {
		t.Fatalf("c did not open the composer (status: %q)", m.status)
	}
	m.editor.SetValue(body)
	return drive(t, m, ctrlKey("j"))
}

func TestCommentHangsOffTheLine(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "why shout?")

	st := reviewJSON(t, m.src.(*git.Repo))
	if len(st.Threads) != 1 {
		t.Fatalf("threads = %+v", st.Threads)
	}
	th := st.Threads[0]
	if th.Path != "foo.txt" || th.Side != review.SideNew || th.Line != 4 {
		t.Errorf("anchored at %s %s:%d, want foo.txt new:4", th.Path, th.Side, th.Line)
	}
	if !strings.Contains(th.Text, "FOUR") {
		t.Errorf("the line snapshot is %q", th.Text)
	}
	if len(th.Comments) != 1 || th.Comments[0].Body != "why shout?" ||
		th.Comments[0].By != "Test" {
		t.Errorf("comments = %+v", th.Comments)
	}

	// And it is drawn where it was written.
	view := plain(m)
	if !strings.Contains(view, "why shout?") {
		t.Errorf("the comment is not on screen:\n%s", view)
	}
	if fileIn(t, m, "foo.txt").open != 1 {
		t.Error("the file does not count its open comment")
	}
}

// A comment on a line the change took away is anchored on the base's side of
// the diff, where that line still exists.
func TestCommentOnARemovedLine(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '-', "two")
	m = comment(t, m, "this was fine")

	th := reviewJSON(t, m.src.(*git.Repo)).Threads[0]
	if th.Side != review.SideOld || th.Line != 2 {
		t.Errorf("anchored at %s:%d, want old:2", th.Side, th.Line)
	}
	if !strings.Contains(plain(m), "this was fine") {
		t.Error("a comment on a removed line is not drawn")
	}
}

// A hunk header is not a line anyone can be talking about.
func TestCommentRefusesSomethingThatIsNotALine(t *testing.T) {
	m := newTestModel(t)
	m = cursorTo(t, m, func(r reviewRow) bool { return r.kind == rowFile })
	m = drive(t, m, letterKey("c"))
	if m.compose != nil {
		t.Error("a file heading accepted a line comment")
	}
	if m.status == "" {
		t.Error("nothing was said about why")
	}
}

func TestReplyAndResolve(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "why shout?")
	id := reviewJSON(t, m.src.(*git.Repo)).Threads[0].ID

	m = cursorTo(t, m, func(r reviewRow) bool { return r.thread == id && r.first })
	m = drive(t, m, letterKey("r"))
	if m.compose == nil || m.compose.thread != id {
		t.Fatalf("r did not open a reply to %s (compose=%+v)", id, m.compose)
	}
	m.editor.SetValue("because it is a shout")
	m = drive(t, m, ctrlKey("j"))

	th := reviewJSON(t, m.src.(*git.Repo)).Thread(id)
	if len(th.Comments) != 2 || th.Comments[1].Body != "because it is a shout" {
		t.Fatalf("comments = %+v", th.Comments)
	}

	// Resolving closes it, and it collapses to one line.
	m = cursorTo(t, m, func(r reviewRow) bool { return r.thread == id && r.first })
	m = drive(t, m, letterKey("x"))
	if !reviewJSON(t, m.src.(*git.Repo)).Thread(id).Resolved {
		t.Fatal("x did not resolve the thread")
	}
	rows := 0
	for _, r := range m.view.rows {
		if r.thread == id {
			rows++
		}
	}
	if rows != 1 {
		t.Errorf("a resolved thread takes %d rows, want 1", rows)
	}
	if !strings.Contains(plain(m), "resolved") {
		t.Error("nothing on screen says it is resolved")
	}

	// Space opens it again without reopening it.
	m = cursorTo(t, m, func(r reviewRow) bool { return r.thread == id })
	m = drive(t, m, letterKey(" "))
	if !reviewJSON(t, m.src.(*git.Repo)).Thread(id).Resolved {
		t.Error("unfolding a resolved thread reopened it")
	}
	if !strings.Contains(plain(m), "because it is a shout") {
		t.Error("unfolding did not show the comments")
	}

	// And x again reopens it.
	m = cursorTo(t, m, func(r reviewRow) bool { return r.thread == id && r.first })
	m = drive(t, m, letterKey("x"))
	if reviewJSON(t, m.src.(*git.Repo)).Thread(id).Resolved {
		t.Error("x did not reopen the thread")
	}
}

func TestDeletingAThreadAsksFirst(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "never mind")
	id := reviewJSON(t, m.src.(*git.Repo)).Threads[0].ID

	m = cursorTo(t, m, func(r reviewRow) bool { return r.thread == id && r.first })
	m = drive(t, m, letterKey("X"))
	if m.dialog == nil {
		t.Fatal("X deleted the thread without asking")
	}
	if reviewJSON(t, m.src.(*git.Repo)).Thread(id) == nil {
		t.Fatal("the thread went before the question was answered")
	}
	m = drive(t, m, letterKey("y"))
	if reviewJSON(t, m.src.(*git.Repo)).Thread(id) != nil {
		t.Error("the thread survived the answer")
	}
}

// t walks to the next conversation that is still asking for something, and
// steps over the ones that are closed.
func TestJumpingBetweenOpenComments(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "bar.txt", '+', "beta")
	m = comment(t, m, "first")
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "second")

	ids := []string{}
	for _, th := range reviewJSON(t, m.src.(*git.Repo)).Threads {
		ids = append(ids, th.ID)
	}
	// Close the first, then walk from the top.
	m = cursorTo(t, m, func(r reviewRow) bool { return r.thread == ids[0] && r.first })
	m = drive(t, m, letterKey("x"))

	m.view.moveTo(0)
	m = drive(t, m, letterKey("t"))
	if got := m.view.thread(); got != ids[1] {
		t.Errorf("t landed on %q, want the open thread %q", got, ids[1])
	}
	m = drive(t, m, letterKey("t"))
	if m.status == "" {
		t.Error("running out of open comments said nothing")
	}
}

// ---------------------------------------------------------------------------
// Reading one commit
// ---------------------------------------------------------------------------

// selectCommit puts the history cursor on a commit by subject.
func selectCommit(t *testing.T, m Model, subject string) Model {
	t.Helper()
	for i, c := range m.history.commits {
		if c.Subject == subject {
			m.history.moveTo(i + 1)
			return runCmd(t, m, m.selectedCommitCmd())
		}
	}
	t.Fatalf("no commit %q in %+v", subject, m.history.commits)
	return m
}

func TestSelectingACommitShowsOnlyItsChange(t *testing.T) {
	m := newTestModel(t)
	if len(m.history.commits) != 2 {
		t.Fatalf("history holds %+v, want the branch's two commits", m.history.commits)
	}
	m = selectCommit(t, m, "capitalise two")

	if got := pathsOf(m); len(got) != 1 || got[0] != "foo.txt" {
		t.Fatalf("the commit's stack holds %v, want only foo.txt", got)
	}
	f := fileIn(t, m, "foo.txt")
	// That commit changed line 2 and nothing else.
	var added []string
	for _, l := range f.lines {
		if l.Kind == '+' {
			added = append(added, strings.TrimSpace(l.Text))
		}
	}
	if len(added) != 1 || added[0] != "+TWO" {
		t.Errorf("the commit's diff adds %v, want only TWO", added)
	}
	if !strings.Contains(plain(m), "capitalise two") {
		t.Error("the pane does not say which commit is being read")
	}

	// The top row of the history goes back to the whole review.
	m.history.moveTo(0)
	m = runCmd(t, m, m.selectedCommitCmd())
	if len(pathsOf(m)) != 4 {
		t.Errorf("going back gave %v, want all four files", pathsOf(m))
	}
}

// A remark made while reading one commit belongs to the review as a whole: it
// is carried into the working tree's numbering as it is written, so going back
// to the whole review finds it beside the same code.
func TestACommentMadeOnACommitIsCarriedToTheReview(t *testing.T) {
	m := newTestModel(t)
	// Push the line the commit touched further down the file, so that the
	// commit's own numbering and the working tree's disagree.
	writeFile(t, m.src.(*git.Repo).Dir, "foo.txt", "zero a\nzero b\none\nTWO\nthree\nFOUR\nfive\n")
	m = drive(t, m, m.rescanCmd()())

	m = selectCommit(t, m, "capitalise two")
	m = onLine(t, m, "foo.txt", '+', "TWO")
	if _, l, ok := m.view.line(); !ok || l.New != 2 {
		t.Fatalf("in the commit, TWO is line %d, want 2", l.New)
	}
	m = comment(t, m, "shouting again")

	th := reviewJSON(t, m.src.(*git.Repo)).Threads[0]
	if th.Line != 4 {
		t.Errorf("thread anchored at working-tree line %d, want 4", th.Line)
	}
	if th.Origin == nil || th.Origin.Line != 2 {
		t.Errorf("origin = %+v, want the commit's own line 2", th.Origin)
	}
	// It is drawn here…
	if !strings.Contains(plain(m), "shouting again") {
		t.Error("the comment is not shown in the commit it was written against")
	}

	// …and it is drawn back in the whole review, beside the same code.
	m.history.moveTo(0)
	m = runCmd(t, m, m.selectedCommitCmd())
	view := plain(m)
	if !strings.Contains(view, "shouting again") {
		t.Fatalf("the comment did not come back to the whole review:\n%s", view)
	}
	f := fileIn(t, m, "foo.txt")
	found := false
	for at, threads := range f.threads {
		for range threads {
			if f.lines[at].New == 4 {
				found = true
			}
		}
	}
	if !found {
		t.Errorf("the comment is not on working-tree line 4: %v", f.threads)
	}
}

// The other direction: a comment written against the working tree shows up
// beside the same code while a commit that touched it is being read.
func TestAReviewCommentShowsUpInsideTheCommit(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "TWO")
	m = comment(t, m, "about the capital")

	m = selectCommit(t, m, "capitalise two")
	if !strings.Contains(plain(m), "about the capital") {
		t.Errorf("the comment is missing while reading the commit:\n%s", plain(m))
	}
	f := fileIn(t, m, "foo.txt")
	if len(f.loose) != 0 {
		t.Errorf("the comment came through as loose rather than on its line: %+v", f.loose)
	}
}

// A comment about something the other commit changed is not on any line of
// this one, so it is kept under the file's heading rather than shown against
// the wrong code.
func TestACommentOutsideTheCommitIsNotPutOnTheWrongLine(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR") // uncommitted, not in either commit
	m = comment(t, m, "about FOUR")

	m = selectCommit(t, m, "capitalise two")
	f := fileIn(t, m, "foo.txt")
	onALine := 0
	for _, threads := range f.threads {
		onALine += len(threads)
	}
	if onALine != 0 {
		t.Errorf("a comment about another change was hung off this commit's code")
	}
	if len(f.loose) != 1 {
		t.Fatalf("loose comments = %+v, want the one about FOUR", f.loose)
	}
	if !strings.Contains(plain(m), "about FOUR") {
		t.Error("the comment vanished rather than being kept")
	}
}

// A comment can outlive the change it was about. Losing it silently would be
// the worst thing this window could do, so the file keeps its place with
// nothing but its remarks in it.
func TestCommentsSurviveTheChangeGoingAway(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "new.txt", '+', "brand new")
	m = comment(t, m, "do we need this?")

	if err := os.Remove(filepath.Join(m.src.(*git.Repo).Dir, "new.txt")); err != nil {
		t.Fatal(err)
	}
	m = drive(t, m, m.rescanCmd()())

	f := fileIn(t, m, "new.txt")
	if !f.gone {
		t.Error("the file is not marked as no longer changed")
	}
	if len(f.loose) != 1 {
		t.Fatalf("loose comments = %+v", f.loose)
	}
	view := plain(m)
	if !strings.Contains(view, "do we need this?") {
		t.Errorf("the comment was lost:\n%s", view)
	}
	if !strings.Contains(view, "no longer changed") {
		t.Error("nothing explains why the file has no diff")
	}
}

// ---------------------------------------------------------------------------
// The review file as an interface
// ---------------------------------------------------------------------------

// Something else writes the review; discobox-review picks it up.
func TestAnAgentsEditIsPickedUp(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "why shout?")
	id := reviewJSON(t, m.src.(*git.Repo)).Threads[0].ID

	// An agent answers and closes the thread, and approves a file.
	st := reviewJSON(t, m.src.(*git.Repo))
	st.Reply(id, "claude", "toned it down")
	st.SetResolved(id, true)
	st.Approve("beta.go", "", "claude")
	if err := m.src.(*git.Repo).WriteStateFile(review.FileName, string(st.Bytes())); err != nil {
		t.Fatal(err)
	}

	m = polled(t, m)
	if !strings.Contains(plain(m), "resolved") {
		t.Error("the answer did not reach the screen")
	}
	if f := fileIn(t, m, "beta.go"); !slices.Contains(f.others, "claude") || f.approved {
		t.Errorf("the agent's approval did not reach the screen as the agent's: others %v, approved %v", f.others, f.approved)
	}
	// An approval written without a digest is stamped with the current one, so
	// that it can be nullified like any other.
	if got := reviewJSON(t, m.src.(*git.Repo)).VerdictOf("beta.go", "claude").Digest; got == "" {
		t.Error("the hand written approval was not stamped with a digest")
	}
}

// A file caught halfway through being written leaves what is on screen alone
// and says so, rather than wiping the review.
func TestAHalfWrittenReviewIsNotTakenSeriously(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "still here")

	if err := m.src.(*git.Repo).WriteStateFile(review.FileName, `{"version":1,`); err != nil {
		t.Fatal(err)
	}
	m.pickUpChanges()

	if len(m.rv.Threads) != 1 {
		t.Errorf("the review was thrown away: %+v", m.rv.Threads)
	}
	if m.rvErr == "" {
		t.Error("nothing was said about the unreadable file")
	}
	m.clearStatus()
	if !strings.Contains(plain(m), "will not parse") {
		t.Error("the status bar does not mention it")
	}
}

// discobox-review re-reads the file before every write of its own, so a comment an
// agent added a moment ago is not quietly dropped by the next approval.
func TestAConcurrentEditIsNotClobbered(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "mine")

	// Somebody else appends a thread without discobox-review noticing.
	st := reviewJSON(t, m.src.(*git.Repo))
	st.Start(&review.Thread{Path: "bar.txt", Side: review.SideNew, Line: 1}, "claude", "theirs")
	if err := m.src.(*git.Repo).WriteStateFile(review.FileName, string(st.Bytes())); err != nil {
		t.Fatal(err)
	}

	m = onLine(t, m, "beta.go", '+', "package beta")
	m = drive(t, m, letterKey("a"))

	back := reviewJSON(t, m.src.(*git.Repo))
	if len(back.Threads) != 2 {
		t.Fatalf("threads = %+v, want both", back.Threads)
	}
	if !back.Approved("beta.go", "Test") {
		t.Error("the approval was lost to the re-read")
	}
}

// Stamping a digest into an approval, or letting one fall, is a write the
// window makes without being asked. It goes onto the review as it stands, not
// onto the copy on screen: a comment that landed since the last tick must not
// be undone by it.
func TestStampingAnApprovalDoesNotClobberAConcurrentEdit(t *testing.T) {
	m := newTestModel(t)

	// An approval given without a digest, which the window has read but not
	// yet stamped.
	st := review.New(m.branch)
	st.Base = m.baseRef
	st.Approve("beta.go", "", "claude")
	if err := m.src.(*git.Repo).WriteStateFile(review.FileName, string(st.Bytes())); err != nil {
		t.Fatal(err)
	}
	m.readReview()

	// A comment lands before the window gets to it.
	st.Start(&review.Thread{Path: "bar.txt", Side: review.SideNew, Line: 1}, "claude", "theirs")
	if err := m.src.(*git.Repo).WriteStateFile(review.FileName, string(st.Bytes())); err != nil {
		t.Fatal(err)
	}

	m.reconcile(true)

	back := reviewJSON(t, m.src.(*git.Repo))
	if len(back.Threads) != 1 {
		t.Fatalf("the stamp was written over the comment: threads = %+v", back.Threads)
	}
	if back.VerdictOf("beta.go", "claude").Digest == "" {
		t.Error("the approval was not stamped")
	}
	if len(m.rv.Threads) != 1 {
		t.Error("the comment is on disk but not on screen")
	}
}

// The window is one more writer of the review, so it waits its turn like the
// commands do: while something else holds the lock it writes nothing, and what
// it then writes is on top of whatever that something did.
func TestTheWindowWaitsForTheLock(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "mine")

	unlock, err := review.Lock(m.src.(*git.Repo), 0)
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan struct{})
	go func() {
		m.mutate(func(st *review.State) { st.Approve("beta.go", "d", "Test") })
		close(done)
	}()
	select {
	case <-done:
		t.Fatal("the window wrote while something else held the lock")
	case <-time.After(100 * time.Millisecond):
	}

	// Whoever holds the lock finishes what it was doing.
	st := reviewJSON(t, m.src.(*git.Repo))
	st.Start(&review.Thread{Path: "bar.txt", Side: review.SideNew, Line: 1}, "claude", "theirs")
	if err := m.src.(*git.Repo).WriteStateFile(review.FileName, string(st.Bytes())); err != nil {
		t.Fatal(err)
	}
	unlock()

	select {
	case <-done:
	case <-time.After(10 * time.Second):
		t.Fatal("the window never got the lock")
	}
	back := reviewJSON(t, m.src.(*git.Repo))
	if len(back.Threads) != 2 || !back.Approved("beta.go", "Test") {
		t.Errorf("want both comments and the approval, got %d threads, approved = %v",
			len(back.Threads), back.Approved("beta.go", "Test"))
	}
}

// A window that waited for the lock for as long as it was held would be a
// window that had stopped. It gives up, says so, and has changed nothing —
// and what was being written is still in the box to post again.
func TestALockThatIsNotLetGoOfDoesNotStopTheWindow(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "mine")

	unlock, err := review.Lock(m.src.(*git.Repo), 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	err = m.update(0, func(st *review.State) { st.Approve("beta.go", "d", "Test") })
	if !errors.Is(err, git.ErrLocked) {
		t.Fatalf("err = %v, want the window told the review is locked", err)
	}
	if m.rv.Approved("beta.go", "Test") || reviewJSON(t, m.src.(*git.Repo)).Approved("beta.go", "Test") {
		t.Error("the approval shows although it was never saved")
	}

	// The same from the keyboard: a comment that cannot be posted is not
	// thrown away, and is not announced as posted.
	m = onLine(t, m, "beta.go", '+', "package beta")
	m = drive(t, m, letterKey("c"))
	m = typeText(t, m, "still in the box")
	m = drive(t, m, ctrlKey("s"))
	if m.focus != focusCompose || m.editor.Value() != "still in the box" {
		t.Errorf("the draft was closed: focus = %v, text = %q", m.focus, m.editor.Value())
	}
	if !m.statusEr {
		t.Errorf("status = %q, want the failure and not a success", m.status)
	}
	if n := len(reviewJSON(t, m.src.(*git.Repo)).Threads); n != 1 {
		t.Errorf("threads on disk = %d, want only the first", n)
	}
}

// A base set from a command a moment ago, which the window has not yet seen,
// is not put back by the window's next write: the window takes it up instead.
func TestAWriteDoesNotPutBackABaseSomethingElseChanged(t *testing.T) {
	m := newTestModel(t)
	m.basePinned = false
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "mine")

	st := reviewJSON(t, m.src.(*git.Repo))
	st.Base = "main~0"
	if err := m.src.(*git.Repo).WriteStateFile(review.FileName, string(st.Bytes())); err != nil {
		t.Fatal(err)
	}

	m = onLine(t, m, "beta.go", '+', "package beta")
	m = drive(t, m, letterKey("a"))

	back := reviewJSON(t, m.src.(*git.Repo))
	if back.Base != "main~0" {
		t.Errorf("base on disk = %q: the window wrote its own over the one just set", back.Base)
	}
	if !back.Approved("beta.go", "Test") {
		t.Error("the approval was not saved")
	}
	// And the poll goes on to measure from it.
	m.pickUpChanges()
	if m.baseRef != "main~0" {
		t.Errorf("the window still measures from %q", m.baseRef)
	}
}

// A review caught halfway through being written is somebody else's to finish.
// The window does not write its own copy over it.
func TestAHalfWrittenReviewIsNotWrittenOver(t *testing.T) {
	m := newTestModel(t)
	if err := m.src.(*git.Repo).WriteStateFile(review.FileName, `{"version":1,`); err != nil {
		t.Fatal(err)
	}
	m = onLine(t, m, "beta.go", '+', "package beta")
	m = drive(t, m, letterKey("a"))

	if body, _ := m.src.(*git.Repo).ReadStateFile(review.FileName); body != `{"version":1,` {
		t.Errorf("the file was overwritten: %q", body)
	}
	if !m.statusEr {
		t.Errorf("status = %q, want it to say the approval was not saved", m.status)
	}
}

// A verdict that could not be saved is not shown as if it stood: the screen
// goes on saying what the file says.
func TestAStampThatCannotBeSavedIsNotShown(t *testing.T) {
	m := newTestModel(t)
	st := review.New(m.branch)
	st.Base = m.baseRef
	st.Approve("beta.go", "", "claude")
	if err := m.src.(*git.Repo).WriteStateFile(review.FileName, string(st.Bytes())); err != nil {
		t.Fatal(err)
	}
	m.readReview()

	// The file stops being one that can be written back to.
	if err := m.src.(*git.Repo).WriteStateFile(review.FileName, `{"version":1,`); err != nil {
		t.Fatal(err)
	}
	m.reconcile(true)

	if got := m.rv.VerdictOf("beta.go", "claude"); got == nil || got.Digest != "" {
		t.Errorf("the screen shows a stamp nothing saved: %+v", got)
	}
	if !m.statusEr {
		t.Errorf("status = %q, want it to say the review could not be saved", m.status)
	}
}

// After a checkout something else can swap the new branch's review in before
// this window has scanned and noticed. That review is not this window's to
// show under the old branch's name.
func TestAnotherBranchsReviewIsNotPutOnScreen(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "mine")

	theirs := review.New("some-other-branch")
	theirs.Start(&review.Thread{Path: "bar.txt", Side: review.SideNew, Line: 1}, "claude", "theirs")
	if err := m.src.(*git.Repo).WriteStateFile(review.FileName, string(theirs.Bytes())); err != nil {
		t.Fatal(err)
	}
	m.pickUpChanges()

	if len(m.rv.Threads) != 1 || m.rv.Threads[0].Comments[0].Body != "mine" {
		t.Errorf("the window took up another branch's review: %+v", m.rv.Threads)
	}
}

// Nor is the last branch's review kept on screen under the new branch's name
// when the new branch's own could not be read. Threads are numbered from one
// in every review, so acting on one would act on a stranger.
func TestTheLastBranchsReviewIsNotKeptForTheNext(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "on the feature branch")

	// The window is told of a checkout, and the file it then reads is still
	// not the new branch's.
	m.branch = "elsewhere"
	m.readReview()

	if len(m.rv.Threads) != 0 || m.rv.Branch != "elsewhere" {
		t.Errorf("the screen still shows %q's review: %+v", m.rv.Branch, m.rv.Threads)
	}
	if m.rvErr == "" {
		t.Error("nothing says the review could not be read")
	}
}

// A thread deleted by something else while a reply to it was being written
// takes the reply's place with it, not the reply: that stays in the box.
func TestAReplyToAThreadThatHasGoneIsNotThrownAway(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "mine")
	id := reviewJSON(t, m.src.(*git.Repo)).Threads[0].ID

	m = cursorTo(t, m, func(r reviewRow) bool { return r.thread == id })
	m = drive(t, m, letterKey("r"))
	m = typeText(t, m, "an answer")

	err := review.Update(m.src.(*git.Repo), m.branch, 0, func(st *review.State) error {
		st.Remove(id)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	m = drive(t, m, ctrlKey("s"))

	if m.focus != focusCompose || m.editor.Value() != "an answer" {
		t.Errorf("the reply was thrown away: focus = %v, text = %q", m.focus, m.editor.Value())
	}
	if !m.statusEr {
		t.Errorf("status = %q, want it to say the thread has gone", m.status)
	}
}

// The review is of a branch, so checking out another one puts this one away
// and brings that one back.
func TestEachBranchKeepsItsOwnReview(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "on the feature branch")

	gitRun(t, m.src.(*git.Repo).Dir, "add", "-A")
	gitRun(t, m.src.(*git.Repo).Dir, "commit", "-qm", "finish the work")
	gitRun(t, m.src.(*git.Repo).Dir, "checkout", "-q", "main")
	m.baseRef, m.basePinned = "", false
	m = drive(t, m, m.rescanCmd()())
	if len(m.rv.Threads) != 0 {
		t.Errorf("main inherited feature's comments: %+v", m.rv.Threads)
	}

	gitRun(t, m.src.(*git.Repo).Dir, "checkout", "-q", "feature")
	m.baseRef, m.basePinned = "main", true
	m = drive(t, m, m.rescanCmd()())
	if len(m.rv.Threads) != 1 || m.rv.Threads[0].Comments[0].Body != "on the feature branch" {
		t.Errorf("the branch's own review did not come back: %+v", m.rv.Threads)
	}
}

// The base is part of the review, so it survives into the next session and an
// agent reading the file knows what the comments are measured from.
func TestChoosingABaseIsRemembered(t *testing.T) {
	m := newTestModel(t)
	m = drive(t, m, letterKey("b"))
	if m.dialog == nil {
		t.Fatal("b did not offer anywhere to measure from")
	}
	m = drive(t, m, baseChosenMsg{ref: "HEAD~1"})

	if m.baseRef != "HEAD~1" {
		t.Fatalf("baseRef = %q", m.baseRef)
	}
	if got := reviewJSON(t, m.src.(*git.Repo)).Base; got != "HEAD~1" {
		t.Errorf("the review file says base %q", got)
	}
	// Measured from the branch's own last commit, the change it made two
	// commits ago is behind the base: TWO is context now rather than an
	// addition under review.
	for _, l := range fileIn(t, m, "foo.txt").lines {
		if (l.Kind == '+' || l.Kind == '-') && strings.Contains(l.Text, "TWO") {
			t.Errorf("a change behind the new base is still under review: %q", l.Text)
		}
	}
	m.dialog = nil
	if !strings.Contains(plain(m), "HEAD~1") {
		t.Error("the bar does not say what the review is measured from")
	}
}

// ---------------------------------------------------------------------------
// The scoreboard
// ---------------------------------------------------------------------------

// The status bar counts the three things a finished review has none of.
func TestTheScoreboardCountsWhatIsLeft(t *testing.T) {
	m := newTestModel(t)
	if got := m.scoreboard(); !strings.Contains(got, "4 to read") ||
		!strings.Contains(got, "3 to commit") {
		t.Errorf("scoreboard = %q", got)
	}
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "one thing")
	if got := m.scoreboard(); !strings.Contains(got, "1 open") {
		t.Errorf("scoreboard = %q, want the open comment counted", got)
	}
}

// And says so plainly when there is nothing left.
func TestTheScoreboardSaysCleanWhenItIs(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.name", "Test"},
		{"config", "user.email", "test@example.com"},
		{"config", "commit.gpgsign", "false"},
	} {
		gitRun(t, dir, args...)
	}
	writeFile(t, dir, "a.txt", "one\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "initial")
	gitRun(t, dir, "checkout", "-q", "-b", "feature")
	writeFile(t, dir, "a.txt", "ONE\n")
	gitRun(t, dir, "commit", "-qam", "shout")

	repo, err := git.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := modelFor(t, repo)
	m = drive(t, m, letterKey("A"), letterKey("y"))
	if got := m.scoreboard(); !strings.Contains(got, "clean") || !strings.Contains(got, "reviewed") {
		t.Errorf("scoreboard = %q, want it to say the job is done", got)
	}
}

// ---------------------------------------------------------------------------
// Panes and keys
// ---------------------------------------------------------------------------

func TestTabVisitsWhatIsOnScreen(t *testing.T) {
	m := newTestModel(t)
	seen := map[focusArea]bool{}
	for i := 0; i < int(focusCount)*2; i++ {
		seen[m.focus] = true
		m = drive(t, m, namedKey(tea.KeyTab))
	}
	for _, f := range []focusArea{focusReview, focusHistory} {
		if !seen[f] {
			t.Errorf("Tab never reached %v", f)
		}
	}
	if seen[focusCompose] {
		t.Error("Tab stopped at the composer while nothing was being written")
	}
}

func TestAltKeysJumpToPanes(t *testing.T) {
	m := newTestModel(t)
	for key, want := range map[string]focusArea{
		"1": focusReview, "2": focusHistory,
	} {
		m = drive(t, m, altKey(key))
		if m.focus != want {
			t.Errorf("Alt-%s went to %v, want %v", key, m.focus, want)
		}
	}
}

func TestSmallTerminalDoesNotPanic(t *testing.T) {
	m := newTestModel(t)
	for _, size := range []tea.WindowSizeMsg{
		{Width: 1, Height: 1}, {Width: 10, Height: 3}, {Width: 40, Height: 6},
		{Width: 200, Height: 60},
	} {
		m = drive(t, m, size)
		_ = m.frame()
	}
}

// Every letter that runs a command is a bare letter: Ctrl belongs to the text
// field, where it edits.
func TestNoCtrlCommandsOutsideTheComposer(t *testing.T) {
	k := newKeyMap()
	for _, b := range commandBindings(k) {
		for _, key := range b.Keys() {
			if strings.HasPrefix(key, "ctrl+") {
				t.Errorf("%q is a command (%s) but needs Ctrl", key, b.name)
			}
		}
	}
}

// The help lists every command letter, so the window can be learned from
// inside it.
func TestHelpNamesEveryCommand(t *testing.T) {
	help := New(testRepo(t), Options{Author: "Test"}).helpText()
	for _, b := range commandBindings(newKeyMap()) {
		if len(b.Keys()) == 0 {
			t.Errorf("%q has no key at all", b.name)
			continue
		}
		key := b.Keys()[0]
		if len(key) != 1 {
			continue
		}
		if !strings.Contains(help, "    "+key+"  ") && !strings.Contains(help, " "+key+" ") {
			t.Errorf("%q (%s) is not in the help", key, b.name)
		}
	}
}

// Over a source that can do everything, the help reads as it always has: two
// entries that share a line still share it.
func TestTheFullHelpKeepsItsLines(t *testing.T) {
	help := New(testRepo(t), Options{Author: "Test"}).helpText()
	for _, line := range []string{
		"    c  comment on this line   r  reply to this comment\n",
		"    v  read the whole file      o  read it as the base has it\n",
		"    Ctrl-C or q  quit         F1 or ?  these keys\n",
	} {
		if !strings.Contains(help, line) {
			t.Errorf("the help has lost the line %q", line)
		}
	}
	if strings.Contains(help, pair) {
		t.Error("the help shows the mark that pairs two entries")
	}
}

// ---------------------------------------------------------------------------
// Reading a file
// ---------------------------------------------------------------------------

func TestEditOpensTheFileInView(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	path, commit, _ := m.fileInView()
	if path != "foo.txt" || commit != "" {
		t.Errorf("fileInView = %q @ %q, want foo.txt in the working tree", path, commit)
	}
	m = selectCommit(t, m, "capitalise two")
	m = onLine(t, m, "foo.txt", '+', "TWO")
	if _, commit, _ := m.fileInView(); commit == "" {
		t.Error("while reading a commit, e would open the working tree's copy")
	}
}

func TestMarkdownReaderRefusesWhatItCannotRender(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = drive(t, m, letterKey("m"))
	if m.md != nil {
		t.Error("a .txt file was opened in the markdown reader")
	}
	if !strings.Contains(m.status, "not a markdown file") {
		t.Errorf("status = %q", m.status)
	}
}

func TestMarkdownReaderOpens(t *testing.T) {
	m := newTestModel(t)
	writeFile(t, m.src.(*git.Repo).Dir, "doc.md", "# Title\n\nSome prose.\n\nAnd more.\n")
	m = drive(t, m, m.rescanCmd()())
	m = onLine(t, m, "doc.md", '+', "And more")
	m = drive(t, m, letterKey("m"))
	if m.md == nil {
		t.Fatalf("m did not open the reader (status %q)", m.status)
	}
	if !strings.Contains(ansi.Strip(m.frame()), "Title") {
		t.Error("the reader is not showing the document")
	}
	m = drive(t, m, letterKey("m"))
	if m.md != nil {
		t.Error("m did not put the reader away")
	}
}

// ---------------------------------------------------------------------------
// Nothing to review
// ---------------------------------------------------------------------------

func TestACleanBranchSaysSo(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.name", "Test"},
		{"config", "user.email", "test@example.com"},
		{"config", "commit.gpgsign", "false"},
	} {
		gitRun(t, dir, args...)
	}
	writeFile(t, dir, "a.txt", "one\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "initial")

	repo, err := git.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := New(repo, Options{Base: "", Author: "Test"})
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 30}, m.rescanCmd()())
	if len(m.view.files) != 0 {
		t.Fatalf("a clean tree has %v to review", pathsOf(m))
	}
	if !strings.Contains(plain(m), "Nothing to review") {
		t.Errorf("the window does not say so:\n%s", plain(m))
	}
	_ = m.frame()
}

// The review file is JSON with a version in it, so that whatever reads it next
// can tell whether it understands what it is looking at.
func TestTheReviewFileIsPlainJSON(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "hello")

	body, ok := m.src.(*git.Repo).ReadStateFile(review.FileName)
	if !ok {
		t.Fatal("no review file")
	}
	var doc struct {
		Version int `json:"version"`
		Branch  string
		Threads []struct {
			ID       string
			Path     string
			Line     int
			Comments []struct{ By, Body string }
		}
	}
	if err := json.Unmarshal([]byte(body), &doc); err != nil {
		t.Fatalf("not JSON: %v\n%s", err, body)
	}
	if doc.Version != review.Version || doc.Branch != "feature" {
		t.Errorf("version %d branch %q", doc.Version, doc.Branch)
	}
	if len(doc.Threads) != 1 || doc.Threads[0].Comments[0].Body != "hello" {
		t.Errorf("threads = %+v", doc.Threads)
	}
}

// V selects a run of lines and c comments on the block, which is anchored to
// the first of them and drawn under the last.
func TestCommentingOnABlockOfLines(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "TWO")
	m = drive(t, m, letterKey("V"))
	if !m.view.visual() {
		t.Fatal("V did not start a selection")
	}
	// Down to the added line further into the file.
	for i := 0; i < 4; i++ {
		m = drive(t, m, namedKey(tea.KeyDown))
	}
	m = comment(t, m, "these two shouts")

	// TWO down to five: four lines of the file as it now stands.
	th := reviewJSON(t, m.src.(*git.Repo)).Threads[0]
	if th.Line != 2 || th.Lines != 4 {
		t.Fatalf("thread covers line %d for %d lines, want 2 for 4", th.Line, th.Lines)
	}
	if first, last := th.Span(); first != 2 || last != 5 {
		t.Errorf("Span() = %d–%d, want 2–5", first, last)
	}
	if m.view.visual() {
		t.Error("posting the comment left the selection open")
	}
	view := plain(m)
	if !strings.Contains(view, "lines 2–5") {
		t.Errorf("the block is not labelled:\n%s", view)
	}
	// Drawn under the last line of the block, which is where it reads as being
	// about what came above it.
	f := fileIn(t, m, "foo.txt")
	for at, threads := range f.threads {
		for range threads {
			// Keyed by the line, which is what a remark is about; the row it
			// gets drawn on is the pane's business and changes with the view.
			if f.lines[at].New != 5 {
				t.Errorf("the block comment hangs off line %d, want 5", f.lines[at].New)
			}
		}
	}
}

// Folding a file empties the space under its heading; it does not move the
// heading. A pane that jerked the line you just pressed Space on up to the top
// of itself would take the files above it off the screen, and the reader would
// have to find their place again to carry on down the review.
func TestFoldingLeavesTheHeadingWhereItIs(t *testing.T) {
	// Short enough that the review scrolls, which is when the difference shows.
	m := shortTestModel(t)
	m = cursorTo(t, m, func(r reviewRow) bool {
		return r.kind == rowFile && m.view.files[r.file].path == "foo.txt"
	})
	before, ok := fileScreen(m, "foo.txt")
	if !ok || before <= 0 {
		t.Fatalf("foo.txt starts at screen row %d; the test wants it partway down a scrolling pane", before)
	}

	m = drive(t, m, letterKey(" "))
	if !fileIn(t, m, "foo.txt").folded {
		t.Fatal("Space did not fold the file")
	}
	if got, _ := fileScreen(m, "foo.txt"); got != before {
		t.Errorf("folding moved the heading to screen row %d, want %d", got, before)
	}

	m = drive(t, m, letterKey(" "))
	if fileIn(t, m, "foo.txt").folded {
		t.Fatal("Space did not open the file again")
	}
	if got, _ := fileScreen(m, "foo.txt"); got != before {
		t.Errorf("unfolding moved the heading to screen row %d, want %d", got, before)
	}
}

// Approving a file folds it away, which is the same shape of change and wants
// the same stability: the heading stays where it was drawn and the diff under
// it goes, rather than the pane hauling the heading up to its top.
func TestApprovingLeavesTheHeadingWhereItIs(t *testing.T) {
	m := shortTestModel(t)
	m = cursorTo(t, m, func(r reviewRow) bool {
		return r.kind == rowFile && m.view.files[r.file].path == "foo.txt"
	})
	before, ok := fileScreen(m, "foo.txt")
	if !ok || before <= 0 {
		t.Fatalf("foo.txt starts at screen row %d; the test wants it partway down a scrolling pane", before)
	}

	m = drive(t, m, letterKey("a"))
	if !fileIn(t, m, "foo.txt").folded {
		t.Fatal("approving the file did not fold it away")
	}
	if got, _ := fileScreen(m, "foo.txt"); got != before {
		t.Errorf("approving moved the heading to screen row %d, want %d", got, before)
	}

	// And withdrawing the approval opens it again in place.
	m = drive(t, m, letterKey("a"))
	if fileIn(t, m, "foo.txt").folded {
		t.Fatal("withdrawing the approval left the file folded")
	}
	if got, _ := fileScreen(m, "foo.txt"); got != before {
		t.Errorf("unapproving moved the heading to screen row %d, want %d", got, before)
	}
}

// F folds every file, and folds them open again once they are all shut: a long
// review folded down to its headings is the map of it, and one key reaches
// that from either direction.
func TestFoldAllAndOpenAllAreTheSameKey(t *testing.T) {
	m := newTestModel(t)
	if countRows(m, rowDiff) == 0 {
		t.Fatal("nothing is open to begin with")
	}
	m = onLine(t, m, "foo.txt", '+', "FOUR")

	m = drive(t, m, letterKey("F"))
	for _, f := range m.view.files {
		if !f.folded {
			t.Errorf("%s is still open after F", f.path)
		}
	}
	if n := countRows(m, rowDiff); n != 0 {
		t.Errorf("%d lines of diff survived the fold", n)
	}
	// Every file still has a heading, which is the point: it is a list of them.
	if n := countRows(m, rowFile); n != len(m.view.files) {
		t.Errorf("%d headings for %d files", n, len(m.view.files))
	}
	// And the file being read is still the one under the cursor.
	if got := m.view.pathAt(); got != "foo.txt" {
		t.Errorf("the cursor moved to %q", got)
	}

	m = drive(t, m, letterKey("F"))
	for _, f := range m.view.files {
		if f.folded {
			t.Errorf("%s is still folded after the second F", f.path)
		}
	}
	if countRows(m, rowDiff) == 0 {
		t.Error("the second F opened nothing")
	}
}

// With one file left open, F tidies rather than opening the rest: the first
// press always folds.
func TestFoldAllFoldsWhenAnythingIsOpen(t *testing.T) {
	m := newTestModel(t)
	m = drive(t, m, letterKey("F"))
	// Open one back up by hand.
	m = cursorTo(t, m, func(r reviewRow) bool {
		return r.kind == rowFile && m.view.files[r.file].path == "foo.txt"
	})
	m = drive(t, m, letterKey(" "))
	if fileIn(t, m, "foo.txt").folded {
		t.Fatal("Space did not open the file")
	}

	m = drive(t, m, letterKey("F"))
	if !fileIn(t, m, "foo.txt").folded {
		t.Error("F opened everything instead of shutting the one that was open")
	}
}

// Approving a file hands its fold back to the rule that folds what is done
// with, so a file signed off after F opened everything still goes away.
func TestApprovingAfterOpeningEverythingStillFolds(t *testing.T) {
	m := newTestModel(t)
	m = drive(t, m, letterKey("F"), letterKey("F")) // shut, then open, explicitly
	if fileIn(t, m, "beta.go").folded {
		t.Fatal("beta.go is folded after opening everything")
	}
	m = onLine(t, m, "beta.go", '+', "package beta")
	m = drive(t, m, letterKey("a"))
	if !fileIn(t, m, "beta.go").folded {
		t.Error("an approved file with nothing outstanding did not fold away")
	}
}

// ---------------------------------------------------------------------------
// Going to a line
// ---------------------------------------------------------------------------

// 24g goes to line 24 of the file the cursor is in, counted in the file as it
// now stands — the space a comment or a compiler error names a line in.
func TestCountThenGGoesToTheLine(t *testing.T) {
	m := newTestModel(t)
	// A file long enough that its line numbers are worth naming.
	writeFile(t, m.src.(*git.Repo).Dir, "long.txt", numberedLines(1, 40))
	gitRun(t, m.src.(*git.Repo).Dir, "add", "long.txt")
	m = drive(t, m, m.rescanCmd()())

	// From the file's own heading, which is where you land coming off the list.
	m = cursorTo(t, m, func(r reviewRow) bool {
		return r.kind == rowFile && m.view.files[r.file].path == "long.txt"
	})
	m = drive(t, m, letterKey("2"), letterKey("4"))
	if m.count != "24" {
		t.Fatalf("count = %q, want 24", m.count)
	}
	if !strings.Contains(plain(m), "go to line 24") {
		t.Error("the status bar does not show the count building")
	}

	m = drive(t, m, letterKey("g"))
	if m.count != "" {
		t.Errorf("the count survived the jump: %q", m.count)
	}
	_, l, ok := m.view.line()
	if !ok || l.New != 24 {
		t.Fatalf("landed on %+v, want new line 24", l)
	}
	// A little of what came before is left on screen above it.
	if m.view.cursor <= m.view.offset {
		t.Errorf("cursor %d is at the very top (offset %d); no context above it",
			m.view.cursor, m.view.offset)
	}
	if !strings.Contains(m.status, "long.txt:24") {
		t.Errorf("status = %q", m.status)
	}
}

// G takes a count too, which is what vim trained everyone's hands to type.
func TestCountThenShiftGAlsoGoesToTheLine(t *testing.T) {
	m := newTestModel(t)
	writeFile(t, m.src.(*git.Repo).Dir, "long.txt", numberedLines(1, 40))
	gitRun(t, m.src.(*git.Repo).Dir, "add", "long.txt")
	m = drive(t, m, m.rescanCmd()())
	m = cursorTo(t, m, func(r reviewRow) bool {
		return r.kind == rowFile && m.view.files[r.file].path == "long.txt"
	})

	m = drive(t, m, letterKey("9"), letterKey("G"))
	if _, l, ok := m.view.line(); !ok || l.New != 9 {
		t.Fatalf("9G landed on %+v, want new line 9", l)
	}
}

// Bare g and G are still the two ends of the review.
func TestBareGStillMeansTopAndBottom(t *testing.T) {
	m := newTestModel(t)
	m.view.moveTo(len(m.view.rows) / 2)
	m = drive(t, m, letterKey("g"))
	if m.view.cursor != 0 {
		t.Errorf("g went to row %d, want the top", m.view.cursor)
	}
	m = drive(t, m, letterKey("G"))
	if m.view.cursor != len(m.view.rows)-1 {
		t.Errorf("G went to row %d, want the bottom (%d)", m.view.cursor, len(m.view.rows)-1)
	}
}

// Most of a file is not in its own diff, so a line that is nowhere on screen
// lands on the nearest one that is — and says so rather than pretending it
// went where it was asked.
func TestGoingToALineThatIsNotInTheDiff(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = drive(t, m, letterKey("9"), letterKey("9"), letterKey("g"))

	if !strings.Contains(m.status, "not in the diff") {
		t.Errorf("status = %q, want it to say the line is not shown", m.status)
	}
	if !m.statusWa {
		t.Error("the note is not marked as a warning")
	}
	if _, _, ok := m.view.line(); !ok {
		t.Error("the cursor did not land on a line at all")
	}
}

// Any other key ends the count, the way vim does it.
func TestAnotherKeyEndsTheCount(t *testing.T) {
	m := newTestModel(t)
	m = drive(t, m, letterKey("2"), letterKey("4"))
	if m.count != "24" {
		t.Fatalf("count = %q", m.count)
	}
	m = drive(t, m, namedKey(tea.KeyEscape))
	if m.count != "" {
		t.Errorf("Esc left the count at %q", m.count)
	}
	// And a leading zero is not the start of one.
	m = drive(t, m, letterKey("0"))
	if m.count != "" {
		t.Errorf("a leading 0 started a count: %q", m.count)
	}
	m = drive(t, m, letterKey("1"), letterKey("0"))
	if m.count != "10" {
		t.Errorf("count = %q, want 10 — a zero after a digit is part of it", m.count)
	}
}

// numberedLines is a file whose contents say which line they are on.
func numberedLines(from, to int) string {
	var b strings.Builder
	for i := from; i <= to; i++ {
		b.WriteString("line " + itoa(i) + "\n")
	}
	return b.String()
}

// Opening everything steps over the files that are done with: a file folds
// itself away once it is approved with nothing outstanding, and "show me
// everything" means everything there is still something to do about.
func TestOpeningEverythingSkipsTheApproved(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "beta.go", '+', "package beta")
	m = drive(t, m, letterKey("a"))
	if !fileIn(t, m, "beta.go").approved {
		t.Fatal("beta.go was not approved")
	}

	m = drive(t, m, letterKey("F")) // everything shut
	for _, f := range m.view.files {
		if !f.folded {
			t.Fatalf("%s is still open after the first F", f.path)
		}
	}

	m = drive(t, m, letterKey("F")) // and open again, bar the one that is done
	if !fileIn(t, m, "beta.go").folded {
		t.Error("an approved file was opened by F")
	}
	for _, f := range m.view.files {
		if f.path != "beta.go" && f.folded {
			t.Errorf("%s was left folded", f.path)
		}
	}
	if !strings.Contains(m.status, "1 approved left folded") {
		t.Errorf("status = %q, want it to say what was skipped", m.status)
	}

	// An approved file with something still being said about it is not done
	// with, so it comes back like the rest. Opening it by hand first, since
	// its lines are not on screen while it is folded.
	m = cursorTo(t, m, func(r reviewRow) bool {
		return r.kind == rowFile && m.view.files[r.file].path == "beta.go"
	})
	m = drive(t, m, letterKey(" "))
	m = onLine(t, m, "beta.go", '+', "package beta")
	m = comment(t, m, "one more thing")
	m = drive(t, m, letterKey("F"), letterKey("F"))
	if fileIn(t, m, "beta.go").folded {
		t.Error("an approved file with an open comment was left folded")
	}
}

// A review where every file is done with still has to have a second press that
// does something, or the key looks broken.
func TestOpeningEverythingWhenEverythingIsApproved(t *testing.T) {
	m := newTestModel(t)
	m = drive(t, m, letterKey("A"), letterKey("y"))
	for _, f := range m.view.files {
		if !f.folded {
			t.Fatalf("%s did not fold itself away once approved", f.path)
		}
	}
	m = drive(t, m, letterKey("F"))
	for _, f := range m.view.files {
		if f.folded {
			t.Errorf("%s stayed folded; F did nothing at all", f.path)
		}
	}
	// And F still shuts them again from there.
	m = drive(t, m, letterKey("F"))
	for _, f := range m.view.files {
		if !f.folded {
			t.Errorf("%s did not fold again", f.path)
		}
	}
}

// ---------------------------------------------------------------------------
// Writing a comment where it lands
// ---------------------------------------------------------------------------

// draftRows is the run of rows the box being written takes up.
func draftRows(m Model) (first, last int, ok bool) { return m.view.draftSpan() }

// The box is drawn into the stack, under the line the comment is about, so
// what you are looking at while you write it is what you will be looking at
// when you read it back.
func TestTheCommentBoxIsDrawnUnderItsLine(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	anchor, height := m.view.cursor, m.view.height

	m = drive(t, m, letterKey("c"))
	first, last, ok := draftRows(m)
	if !ok {
		t.Fatal("no box in the stack")
	}
	if first != anchor+1 {
		t.Errorf("the box starts at row %d, want the row after the line (%d)", first, anchor+1)
	}
	if last <= first {
		t.Errorf("the box is %d rows; it needs at least a border and a field", last-first+1)
	}
	// The line it is about is still on screen above it.
	if anchor < m.view.offset {
		t.Errorf("the line being commented on scrolled off: anchor %d, offset %d",
			anchor, m.view.offset)
	}
	if last >= m.view.offset+m.view.bodyRows() {
		t.Errorf("the foot of the box (row %d) is below the pane, which ends at %d",
			last, m.view.offset+m.view.bodyRows()-1)
	}

	view := plain(m)
	if !strings.Contains(view, "Comment on foo.txt:4") {
		t.Errorf("the box does not say where the comment lands:\n%s", view)
	}
	if !strings.Contains(view, postHint) {
		t.Error("the box does not say how to post it")
	}
	// And it is inside the review, not a pane of its own under it.
	if m.rects[focusCompose].w != 0 {
		t.Error("the composer took a pane of its own")
	}
	if m.view.height != height {
		t.Errorf("the review gave up %d rows to the composer", height-m.view.height)
	}
}

// What is typed shows up in the box, and the box grows to hold it.
func TestTheBoxGrowsWithWhatIsTyped(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = drive(t, m, letterKey("c"))
	_, _, _ = draftRows(m)
	first, last, _ := draftRows(m)
	oneLine := last - first + 1

	for _, r := range "why?" {
		m = drive(t, m, letterKey(string(r)))
	}
	if !strings.Contains(plain(m), "why?") {
		t.Errorf("what was typed is not in the box:\n%s", plain(m))
	}

	for i := 0; i < 3; i++ {
		m = drive(t, m, namedKey(tea.KeyEnter))
		m = drive(t, m, letterKey("m"))
	}
	first, last, _ = draftRows(m)
	if got := last - first + 1; got <= oneLine {
		t.Errorf("the box is still %d rows after four lines were typed, was %d", got, oneLine)
	}
}

// A remark is usually one long sentence with no newline anywhere in it, so the
// box has to grow for that too. What decides its height is how many rows the
// text takes on screen, not how many times Enter was pressed: counting
// paragraphs leaves the box one row tall and scrolls the remark out of sight
// while it is still being written.
func TestTheBoxGrowsForAWrappedLine(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = drive(t, m, letterKey("c"))
	first, last, _ := draftRows(m)
	empty := last - first + 1

	long := strings.Repeat("the rest of the file is lower case, so this reads as a typo. ", 4)
	m = typeText(t, m, long)

	first, last, ok := draftRows(m)
	if !ok {
		t.Fatal("the box went away while it was being typed into")
	}
	if got := last - first + 1; got <= empty {
		t.Errorf("the box is still %d rows after %d characters with no newline in them",
			got, len(long))
	}
	// And the end of it is on screen, rather than scrolled out of a box that
	// never grew to hold it.
	if !strings.Contains(plain(m), "reads as a typo.") {
		t.Errorf("the end of the remark is not in the box:\n%s", plain(m))
	}
}

// It stops at maxDraftHeight and scrolls inside itself after that, whether the
// rows came from newlines or from wrapping.
func TestTheBoxStopsGrowing(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = drive(t, m, letterKey("c"))

	long := strings.Repeat("this remark goes on rather longer than anybody wanted it to. ", 12)
	m = typeText(t, m, long)

	first, last, ok := draftRows(m)
	if !ok {
		t.Fatal("the box went away")
	}
	// The rule above and the rule below are the box's own, on top of its text.
	if got := last - first + 1; got > maxDraftHeight+2 {
		t.Errorf("the box grew to %d rows, past the %d it should stop at",
			got, maxDraftHeight+2)
	}
}

// Esc takes the box away and puts the cursor back on the line it was about.
func TestEscapeTakesTheBoxAway(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	anchor := m.view.cursor
	m = drive(t, m, letterKey("c"), namedKey(tea.KeyEscape))

	if _, _, ok := draftRows(m); ok {
		t.Error("the box is still in the stack")
	}
	if m.compose != nil || m.focus != focusReview {
		t.Errorf("compose=%v focus=%v", m.compose, m.focus)
	}
	if m.view.cursor != anchor {
		t.Errorf("cursor = %d, want it back on the line (%d)", m.view.cursor, anchor)
	}
}

// Posting takes the box away and leaves the cursor on the thread it became, so
// r and x act on what was just written.
func TestPostingLeavesTheCursorOnTheNewThread(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "why shout?")

	if _, _, ok := draftRows(m); ok {
		t.Error("the box outlived the comment")
	}
	id := reviewJSON(t, m.src.(*git.Repo)).Threads[0].ID
	if got := m.view.thread(); got != id {
		t.Errorf("cursor is on thread %q, want %q", got, id)
	}
	// So the very next key can answer it.
	m = drive(t, m, letterKey("x"))
	if !reviewJSON(t, m.src.(*git.Repo)).Thread(id).Resolved {
		t.Error("x did not reach the thread just written")
	}
}

// A block comment's box goes under the last line of the block, which is where
// the thread itself will be drawn.
func TestTheBoxForABlockGoesUnderTheLastLine(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "TWO")
	m = drive(t, m, letterKey("V"))
	for i := 0; i < 4; i++ {
		m = drive(t, m, namedKey(tea.KeyDown))
	}
	// The last line of the block is line 5 of the file as it now stands — the
	// box goes under that one, not under the first of them. Named here rather
	// than asked of commentTarget, which is the thing under test: a test that
	// takes its expected answer from the code it is checking agrees with that
	// code however wrong it is.
	end := -1
	for i, r := range m.view.rows {
		if r.kind != rowDiff || r.file < 0 || m.view.files[r.file].path != "foo.txt" {
			continue
		}
		f := m.view.files[r.file]
		at := m.view.liveSide(f, r.line)
		if at >= 0 && at < len(f.lines) && isCodeLine(f.lines[at].Kind) && f.lines[at].New == 5 {
			end = i
			break
		}
	}
	if end < 0 {
		t.Fatal("line 5 of foo.txt is not drawn")
	}
	m = drive(t, m, letterKey("c"))

	first, _, ok := draftRows(m)
	if !ok {
		t.Fatal("no box")
	}
	if first != end+1 {
		t.Errorf("the box starts at %d, want just after the last selected line (%d)",
			first, end+1)
	}
	if !strings.Contains(plain(m), "foo.txt:2–5") {
		t.Errorf("the box does not name the block:\n%s", plain(m))
	}
}

// A reply's box goes under the conversation it answers.
func TestTheReplyBoxGoesUnderTheThread(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "why shout?")
	id := reviewJSON(t, m.src.(*git.Repo)).Threads[0].ID

	m = cursorTo(t, m, func(r reviewRow) bool { return r.thread == id && r.first })
	m = drive(t, m, letterKey("r"))
	first, _, ok := draftRows(m)
	if !ok {
		t.Fatal("no box")
	}
	// Every row of the thread comes before the box, and none after it.
	for i, r := range m.view.rows {
		if r.kind == rowThread && r.thread == id && i > first {
			t.Errorf("thread row %d is below the reply box at %d", i, first)
		}
	}
	if !strings.Contains(plain(m), "Reply to "+id) {
		t.Error("the box does not say what it is a reply to")
	}
}

// Clicking inside the box is not asking to go somewhere else.
func TestClickingInTheBoxKeepsTheKeyboardInIt(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = drive(t, m, letterKey("c"))
	first, _, ok := draftRows(m)
	if !ok {
		t.Fatal("no box")
	}
	before := m.view.cursor

	x, y := contentPoint(t, m, focusReview, first+1-m.view.offset, 6)
	m = drive(t, m, pressAt(x, y))
	if m.focus != focusCompose {
		t.Errorf("focus = %v, want the composer", m.focus)
	}
	if m.view.cursor != before {
		t.Errorf("the click moved the review cursor to %d", m.view.cursor)
	}
}

// Posting has to have a key that arrives in every terminal.
//
// Ctrl-Enter is what a hand trained on the web reaches for, but plenty of
// terminals send a carriage return for it — byte-identical to Enter, which
// inside a text field has to stay a newline. So the box names Ctrl-S, and the
// binding takes both.
func TestEveryWayOfPostingAComment(t *testing.T) {
	for _, k := range []tea.KeyPressMsg{
		ctrlKey("s"),
		ctrlKey("j"),                           // Ctrl-Enter, as a line feed
		{Code: tea.KeyEnter, Mod: tea.ModAlt},  // Alt-Enter, where Alt is Meta
		{Code: tea.KeyEnter, Mod: tea.ModCtrl}, // and Ctrl-Enter itself, on a
		//                                         terminal that can say so
	} {
		m := newTestModel(t)
		m = onLine(t, m, "foo.txt", '+', "FOUR")
		m = drive(t, m, letterKey("c"))
		m.editor.SetValue("posted with " + k.String())
		m = drive(t, m, k)

		st := reviewJSON(t, m.src.(*git.Repo))
		if len(st.Threads) != 1 {
			t.Errorf("%s did not post: threads = %+v", k.String(), st.Threads)
			continue
		}
		if m.compose != nil {
			t.Errorf("%s left the box open", k.String())
		}
	}
}

// Plain Enter is a newline, not a post: a comment is prose and needs its
// paragraphs.
func TestEnterInTheBoxIsANewline(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = drive(t, m, letterKey("c"))
	m.editor.SetValue("first")
	m = drive(t, m, namedKey(tea.KeyEnter))
	m = drive(t, m, letterKey("s"))

	if m.compose == nil {
		t.Fatal("Enter posted the comment")
	}
	if got := m.editor.Value(); !strings.Contains(got, "\n") {
		t.Errorf("Enter did not put a newline in: %q", got)
	}
	if reviewJSON2(m.src.(*git.Repo)) != nil && len(reviewJSON2(m.src.(*git.Repo)).Threads) != 0 {
		t.Error("something was posted")
	}
}

// And the box says the key that always works, rather than the one that
// sometimes does.
func TestTheBoxNamesAKeyThatArrives(t *testing.T) {
	if !strings.Contains(postHint, "Ctrl-S") {
		t.Errorf("postHint = %q, want it to name Ctrl-S", postHint)
	}
	for _, k := range newKeyMap().Post.Keys() {
		if k == "ctrl+s" {
			return
		}
	}
	t.Error("the box names Ctrl-S but nothing binds it")
}

// The screen's own state — the alternate screen, the mouse, the keyboard
// enhancements — is part of the view rather than something settled once at
// startup, so it is worth an assertion: dropping any of it would take the
// mouse, the window or Ctrl-Enter away without anything failing to compile.
func TestTheViewAsksForTheScreenItNeeds(t *testing.T) {
	v := newTestModel(t).View()
	if !v.AltScreen {
		t.Error("the view does not ask for the alternate screen")
	}
	if v.MouseMode != tea.MouseModeCellMotion {
		t.Errorf("mouse mode = %v, want cell motion", v.MouseMode)
	}
	if v.Content == "" {
		t.Error("the view has no content")
	}
}

// And Ctrl-Enter is a key the runtime can now name, which is what the
// enhancement buys: on a terminal that reports it, the binding fires.
func TestCtrlEnterIsADistinctKeyNow(t *testing.T) {
	plainEnter := tea.KeyPressMsg{Code: tea.KeyEnter}
	ctrlEnter := tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModCtrl}
	if plainEnter.String() == ctrlEnter.String() {
		t.Fatalf("both spell %q; Ctrl-Enter cannot be bound", plainEnter.String())
	}
	if got := ctrlEnter.String(); got != "ctrl+enter" {
		t.Errorf("Ctrl-Enter spells %q", got)
	}
	if !key.Matches(ctrlEnter, newKeyMap().Post) {
		t.Error("Ctrl-Enter does not post")
	}
	if key.Matches(plainEnter, newKeyMap().Post) {
		t.Error("plain Enter posts, so a comment could never have two paragraphs")
	}
}

// ---------------------------------------------------------------------------
// What a file's heading says
// ---------------------------------------------------------------------------

func TestTheHeadingCountsOpenComments(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "why shout?")
	m = onLine(t, m, "foo.txt", '+', "TWO")
	m = comment(t, m, "and this one")

	if got := heading(m, "foo.txt"); !strings.Contains(got, "2 open comments") {
		t.Errorf("heading = %q, want it to count the open comments", got)
	}
	// A file nobody has said anything about says nothing about comments.
	if got := heading(m, "beta.go"); strings.Contains(got, "comment") {
		t.Errorf("heading = %q, want no mention of comments", got)
	}

	// Once they are all answered it counts what was said rather than what is
	// outstanding, since nothing is.
	for _, th := range reviewJSON(t, m.src.(*git.Repo)).Threads {
		m = cursorTo(t, m, func(r reviewRow) bool { return r.thread == th.ID && r.first })
		m = drive(t, m, letterKey("x"))
	}
	if got := heading(m, "foo.txt"); !strings.Contains(got, "2 comments") {
		t.Errorf("heading = %q, want the total once they are all resolved", got)
	}
}

// The heading gives way in a fixed order as the pane narrows, and a count of
// people waiting for an answer is the last thing to go — it goes to a glyph
// and a number before it goes away.
func TestTheHeadingKeepsTheCommentCountLongest(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "why shout?")
	f := fileIn(t, m, "foo.txt")

	// Wide enough for everything.
	if got := f.headerTail(80); !strings.Contains(got, "1 open comment") ||
		!strings.Contains(got, "uncommitted") || !strings.Contains(got, "+2") {
		t.Errorf("at 80 the tail is %q, want all of it", got)
	}
	// Too narrow for the words, but never too narrow for the count.
	for _, avail := range []int{40, 24, 12, 6, 2} {
		got := f.headerTail(avail)
		if width(got) > avail {
			t.Errorf("tail at %d is %q, which is %d wide", avail, got, width(got))
		}
		if !strings.Contains(got, "1") {
			t.Errorf("tail at %d is %q, which has lost the count", avail, got)
		}
	}
	// And with no room at all it says nothing rather than overflowing.
	if got := f.headerTail(1); got != "" {
		t.Errorf("tail at 1 = %q, want nothing", got)
	}
}

// A file with an open comment shortens its own name to keep saying so. A file
// with nothing to act on does not: its heading is only arithmetic, and the
// name is worth more than that.
func TestTheNameGivesWayOnlyForSomethingToActOn(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "why shout?")

	const narrow = 16
	if got := heading(m, "foo.txt"); !strings.Contains(headingAt(m, "foo.txt", narrow), "◆1") {
		t.Errorf("at %d columns foo.txt says %q, losing its open comment (wide: %q)",
			narrow, headingAt(m, "foo.txt", narrow), got)
	}
	if got := headingAt(m, "new.txt", narrow); !strings.Contains(got, "new.txt") {
		t.Errorf("at %d columns new.txt says %q; its whole name should have been kept",
			narrow, got)
	}
}

// headingAt is a file's band drawn to a given width.
func headingAt(m Model, path string, w int) string {
	for _, f := range m.view.files {
		if f.path == path {
			return ansi.Strip(m.view.fileHeader(f, m.st, m.st.fileBand, w))
		}
	}
	return ""
}

// ---------------------------------------------------------------------------
// Hiding the files the reader has approved
// ---------------------------------------------------------------------------

// f takes the files the reader has approved off the screen: what is left is
// what they have yet to read. A comment still open on one does not keep it —
// that is what keeps a file from folding, and the title goes on counting it.
func TestHidingTheFilesThatAreApproved(t *testing.T) {
	m := newTestModel(t)
	// bar.txt: approved, though somebody is still waiting for an answer on it.
	m = onLine(t, m, "bar.txt", '+', "beta")
	m = comment(t, m, "hold on")
	m = onLine(t, m, "bar.txt", '+', "beta")
	m = drive(t, m, letterKey("a"))
	// beta.go: approved and quiet.
	m = onLine(t, m, "beta.go", '+', "package beta")
	m = drive(t, m, letterKey("a"))

	m = drive(t, m, letterKey("f"))
	got := pathsOf(m)
	want := []string{"foo.txt", "new.txt"}
	if len(got) != len(want) {
		t.Fatalf("showing %v, want %v — approving a file is all it takes", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("file %d = %q, want %q", i, got[i], want[i])
		}
	}
	if m.view.hidden != 2 {
		t.Errorf("hidden = %d, want 2", m.view.hidden)
	}
	if !strings.Contains(m.status, "Hiding 2 files you have approved") {
		t.Errorf("status = %q", m.status)
	}
	if !strings.Contains(plain(m), "1 open comment") {
		t.Errorf("the comment on a hidden file stopped being counted:\n%s", plain(m))
	}
	// t cannot go to a row that is not there, so it says where the comment is.
	m = drive(t, m, letterKey("t"))
	if !strings.Contains(m.status, "1 open comment in files f has hidden") {
		t.Errorf("t said %q of a comment the title is counting", m.status)
	}

	// And f again brings them back.
	m = drive(t, m, letterKey("f"))
	if len(pathsOf(m)) != 4 || m.view.hidden != 0 {
		t.Errorf("showing %v with %d hidden, want all four back", pathsOf(m), m.view.hidden)
	}
}

// A hidden file is still part of the review, so the counts still count it. A
// tally that forgot it would report a review shrinking as it was finished.
func TestTheTalliesCountWhatIsHidden(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "beta.go", '+', "package beta")
	m = drive(t, m, letterKey("a"))
	m = drive(t, m, letterKey("f"))

	view := plain(m)
	if !strings.Contains(view, "1/4 approved") {
		t.Errorf("the review tally lost the hidden file:\n%s", view)
	}
	if !strings.Contains(view, "1 hidden") {
		t.Error("the review pane does not say anything is hidden")
	}
	// The scoreboard is about the whole review either way.
	if got := m.scoreboard(); !strings.Contains(got, "3 to read") {
		t.Errorf("scoreboard = %q, want it unchanged by the filter", got)
	}
}

// Approving the file you are reading, with the filter on, takes it off the
// screen — and leaves the cursor somewhere real rather than nowhere.
func TestApprovingWhileHidingMovesOn(t *testing.T) {
	m := newTestModel(t)
	m = drive(t, m, letterKey("f"))
	m = onLine(t, m, "beta.go", '+', "package beta")
	m = drive(t, m, letterKey("a"))

	for _, p := range pathsOf(m) {
		if p == "beta.go" {
			t.Fatal("the file that was just approved is still on screen")
		}
	}
	if m.view.cursor < 0 || m.view.cursor >= len(m.view.rows) {
		t.Errorf("cursor = %d, off the end of %d rows", m.view.cursor, len(m.view.rows))
	}
	if _, ok := m.view.file(); !ok && len(m.view.files) > 0 {
		t.Error("the cursor is on no file at all")
	}
}

// With everything finished and the filter on there is nothing to draw, and
// saying "nothing to review" would be wrong — there is plenty, it is just done.
func TestHidingEverythingSaysWhy(t *testing.T) {
	m := newTestModel(t)
	m = drive(t, m, letterKey("A"), letterKey("y"), letterKey("f"))
	if len(m.view.files) != 0 {
		t.Fatalf("still showing %v", pathsOf(m))
	}
	view := plain(m)
	if !strings.Contains(view, "Nothing left to read") {
		t.Errorf("the pane does not say why it is empty:\n%s", view)
	}
	if !strings.Contains(view, "Press f") {
		t.Error("the pane does not say how to see them again")
	}
}

// ---------------------------------------------------------------------------
// Reading a whole file
// ---------------------------------------------------------------------------

// v opens the file the cursor is in, whole — and lands on the line that was
// being read, not at the top.
func TestViewOpensTheFileAtTheLine(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = drive(t, m, letterKey("v"))

	if m.file == nil {
		t.Fatalf("v opened nothing (status %q)", m.status)
	}
	if m.file.path != "foo.txt" || m.file.side != sideNow {
		t.Errorf("reading %q on side %v", m.file.path, m.file.side)
	}
	// The whole file, not the parts of it the diff talks about.
	if got := len(m.file.lines); got != 5 {
		t.Errorf("the reader has %d lines, want the file's 5", got)
	}
	if m.file.cursor != 3 {
		t.Errorf("cursor on line %d, want line 4 — the line that was being read",
			m.file.cursor+1)
	}
	// The lines the review touches are marked, so the diff is findable in it.
	if !m.file.changed[2] || !m.file.changed[4] {
		t.Errorf("changed = %v, want the two added lines marked", m.file.changed)
	}
	if m.file.changed[1] {
		t.Error("an untouched line is marked")
	}
	view := plain(m)
	if !strings.Contains(view, "foo.txt") || !strings.Contains(view, "five") {
		t.Errorf("the reader is not showing the file:\n%s", view)
	}

	// v again puts it away, the way m does for the markdown reader.
	m = drive(t, m, letterKey("v"))
	if m.file != nil {
		t.Error("v did not close the reader")
	}
}

// The reader colours the file the way the review colours a hunk.
//
// It gets that by handing the highlighter one hunk's worth of lines. The
// highlighter skips any line that belongs to no hunk, so lines built without
// one are silently dropped and the whole file comes out grey — which looks
// like a file nothing recognises rather than like a bug.
func TestTheReaderHighlightsTheFile(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "beta.go", '+', "package beta")
	m = drive(t, m, letterKey("v"))
	if m.file == nil {
		t.Fatalf("v opened nothing (status %q)", m.status)
	}

	var tokens int
	for _, line := range m.file.spans {
		tokens += len(line)
	}
	if tokens == 0 {
		t.Fatalf("%s was tokenised into nothing at all", m.file.path)
	}
	// Not just somewhere: the line the cursor is on has its own colours.
	if len(m.file.spans[m.file.cursor]) == 0 {
		t.Errorf("line %d, %q, has no tokens",
			m.file.cursor+1, m.file.texts[m.file.cursor])
	}
	// And a file nothing has a lexer for is still shown, plainly.
	m = drive(t, m, letterKey("v"))
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = drive(t, m, letterKey("v"))
	if m.file == nil {
		t.Fatal("v would not open a file with no lexer for it")
	}
	if !strings.Contains(plain(m), "five") {
		t.Errorf("an unhighlightable file was not shown:\n%s", plain(m))
	}
}

// o opens the other side: the file as the base has it.
func TestViewOldOpensTheBaseVersion(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = drive(t, m, letterKey("o"))

	if m.file == nil {
		t.Fatalf("o opened nothing (status %q)", m.status)
	}
	if m.file.side != sideBefore {
		t.Errorf("side = %v, want the version before", m.file.side)
	}
	if !strings.Contains(m.file.title(), "main") {
		t.Errorf("title = %q, want it to name the base", m.file.title())
	}
	// The base has the old text, and the lines the change took away are the
	// ones marked on this side.
	body := strings.Join(m.file.texts, "\n")
	if !strings.Contains(body, "two") || strings.Contains(body, "TWO") {
		t.Errorf("the reader is showing the wrong version:\n%s", body)
	}
	if !m.file.changed[2] || !m.file.changed[4] {
		t.Errorf("changed = %v, want the two removed lines marked", m.file.changed)
	}
}

// Inside the reader, v and o swap versions and keep the line — the point of
// the gesture is to see what this line used to say.
func TestTheReaderSwapsSidesKeepingTheLine(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = drive(t, m, letterKey("v"))
	if m.file.cursor != 3 {
		t.Fatalf("cursor on line %d", m.file.cursor+1)
	}

	m = drive(t, m, letterKey("o"))
	if m.file == nil || m.file.side != sideBefore {
		t.Fatalf("o did not swap to the version before: %v", m.file)
	}
	if m.file.cursor != 3 {
		t.Errorf("landed on line %d of the old side, want the same line 4",
			m.file.cursor+1)
	}
	m = drive(t, m, letterKey("v"))
	if m.file == nil || m.file.side != sideNow {
		t.Fatalf("v did not swap back: %v", m.file)
	}
}

// e goes straight from reading a file to editing it: one thought, one key.
func TestEditFromInsideTheReader(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = drive(t, m, letterKey("v"))
	if m.file == nil {
		t.Fatal("the reader did not open")
	}
	mdl, cmd := m.fileKey(letterKey("e"))
	m = mdl.(Model)
	if m.file != nil {
		t.Error("the reader is still up behind the editor")
	}
	if cmd == nil {
		t.Error("e did not ask for the editor")
	}
}

// n and p hop between the runs of lines the review touched, so a long file can
// be walked by its changes rather than by its length.
func TestTheReaderHopsBetweenChangedRuns(t *testing.T) {
	m := newTestModel(t)
	writeFile(t, m.src.(*git.Repo).Dir, "long.txt", numberedLines(1, 60))
	gitRun(t, m.src.(*git.Repo).Dir, "add", "long.txt")
	m = drive(t, m, m.rescanCmd()())
	m = cursorTo(t, m, func(r reviewRow) bool {
		return r.kind == rowFile && m.view.files[r.file].path == "long.txt"
	})
	m = drive(t, m, letterKey("v"))
	if m.file == nil {
		t.Fatalf("the reader did not open (status %q)", m.status)
	}
	// A brand new file is one long run of changed lines.
	if len(m.file.jumps) != 1 {
		t.Fatalf("jumps = %v, want one run", m.file.jumps)
	}
	m = drive(t, m, letterKey("n"))
	if m.file.cur != 0 {
		t.Errorf("n landed on run %d", m.file.cur)
	}
	if !strings.Contains(plain(m), "change 1 of 1") {
		t.Error("the title does not count the runs")
	}
}

// Esc and q close it, and while it is up the reader owns every other key: the
// review behind it must not move under a keystroke meant for the file.
func TestTheReaderOwnsTheKeys(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = drive(t, m, letterKey("v"))
	if m.file == nil {
		t.Fatal("the reader did not open")
	}
	review, at := m.view.cursor, m.file.cursor

	m = drive(t, m, namedKey(tea.KeyDown), namedKey(tea.KeyDown))
	if m.view.cursor != review {
		t.Errorf("the review cursor moved to %d while the reader was up", m.view.cursor)
	}
	// The fixture's foo.txt is five lines, so two downs from line 4 run into
	// the end of it: what matters is that they moved the reader and not the
	// review.
	if m.file.cursor <= at {
		t.Errorf("the reader did not scroll: still on line %d", m.file.cursor+1)
	}
	// A command letter is the reader's too, not the review's.
	m = drive(t, m, letterKey("a"))
	if reviewJSON2(m.src.(*git.Repo)) != nil && len(reviewJSON2(m.src.(*git.Repo)).Files) != 0 {
		t.Error("a approved a file from inside the reader")
	}

	m = drive(t, m, namedKey(tea.KeyEscape))
	if m.file != nil {
		t.Error("Esc did not close the reader")
	}
}

// ---------------------------------------------------------------------------
// Split and unified
// ---------------------------------------------------------------------------

// The review opens with the two sides of a change beside each other: what a
// line was on the left, what it became on the right. s unifies it and back.
func TestSplitPutsTheTwoSidesSideBySide(t *testing.T) {
	m := newTestModel(t)
	if !m.view.splitting() {
		t.Fatalf("the review does not open split (width %d)", m.view.width)
	}

	// What the same diff costs unified, to compare the pairs against.
	m = drive(t, m, letterKey("s"))
	if m.view.splitting() {
		t.Fatalf("s did not unify (status %q)", m.status)
	}
	unified := len(m.view.rows)

	m = drive(t, m, letterKey("s"))
	if !m.view.splitting() {
		t.Fatalf("s did not split again (status %q, width %d)", m.status, m.view.width)
	}
	if !strings.Contains(plain(m), "· split") {
		t.Error("the title does not say the diff is split")
	}
	// foo.txt swaps two lines, so its four changed rows become two.
	if len(m.view.rows) >= unified {
		t.Errorf("split has %d rows, unified had %d — the pairs did not fold up",
			len(m.view.rows), unified)
	}

	f := fileIn(t, m, "foo.txt")
	pairs := 0
	for _, r := range f.rows {
		if r.old < 0 || r.new < 0 {
			continue
		}
		if f.lines[r.old].Kind == '-' && f.lines[r.new].Kind == '+' {
			pairs++
		}
	}
	if pairs != 2 {
		t.Errorf("%d rows pair a removal with an addition, want 2", pairs)
	}

	// The two are drawn on one line, in that order.
	row := ""
	for _, l := range strings.Split(plain(m), "\n") {
		if strings.Contains(l, "-two") {
			row = l
			break
		}
	}
	if row == "" {
		t.Fatalf("no row holds the removed line:\n%s", plain(m))
	}
	if !strings.Contains(row, "+ TWO") {
		t.Errorf("the row beside -two is %q, want it to hold + TWO", row)
	}
	if strings.Index(row, "-two") > strings.Index(row, "+ TWO") {
		t.Error("the new side is drawn to the left of the old one")
	}

	// And s again puts it back the other way.
	m = drive(t, m, letterKey("s"))
	if m.view.splitting() || len(m.view.rows) != unified {
		t.Errorf("s did not unify: splitting=%v rows=%d want %d",
			m.view.splitting(), len(m.view.rows), unified)
	}
}

// A pure insertion has nothing on the left of it, and a hunk header belongs to
// neither side, so it runs the whole width.
func TestSplitLeavesTheMissingSideEmpty(t *testing.T) {
	m := newTestModel(t)
	if !m.view.splitting() {
		t.Fatal("the review does not open split")
	}
	// beta.go is a new file: every line of it is an addition with nothing it
	// replaced.
	f := fileIn(t, m, "beta.go")
	code := 0
	for _, r := range f.rows {
		l, _ := f.rowLine(0)
		_ = l
		switch {
		case r.new >= 0 && f.lines[r.new].Kind == '+':
			code++
			if r.old >= 0 {
				t.Errorf("an added line was paired with %q", f.lines[r.old].Text)
			}
		case r.old >= 0 && r.new >= 0 && r.old != r.new:
			t.Errorf("a row pairs two lines that are not an edit: %+v", r)
		}
	}
	if code == 0 {
		t.Fatal("no added lines at all")
	}

	// The header spans both sides rather than being cut in half.
	header := ""
	for _, l := range strings.Split(plain(m), "\n") {
		if strings.Contains(l, "@@ -0,0") {
			header = l
			break
		}
	}
	if header == "" {
		t.Fatalf("no hunk header on screen:\n%s", plain(m))
	}
	if strings.Count(header, "│") > 1 {
		t.Errorf("the hunk header was split: %q", header)
	}
}

// A comment is filed against a line, and stays on it in either view and across
// every switch between them. The line is the anchor; which row draws it is the
// pane's business and changes underneath.
func TestCommentsSurviveTheSplit(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "why shout?")

	// Checked on both sides of the toggle, starting with the split view the
	// review opens in, since that is the one this is named for.
	for _, view := range []string{"split", "unified", "split again"} {
		if !strings.Contains(plain(m), "why shout?") {
			t.Errorf("the comment is missing from the %s view:\n%s", view, plain(m))
		}
		f := fileIn(t, m, "foo.txt")
		placed := 0
		for at, threads := range f.threads {
			placed += len(threads)
			if f.lines[at].New != 4 {
				t.Errorf("in the %s view the comment is filed against line %d, want 4",
					view, f.lines[at].New)
			}
		}
		if placed != 1 {
			t.Errorf("%d comments in the %s view, want 1", placed, view)
		}
		// And drawn against that line, not merely filed against it.
		head := -1
		for i, r := range m.view.rows {
			if r.kind == rowThread && r.first {
				head = i
			}
		}
		if head < 1 {
			t.Fatalf("the conversation is not drawn in the %s view", view)
		}
		above := m.view.rows[head-1]
		if l, ok := m.view.files[above.file].rowLine(above.line); !ok || l.New != 4 {
			t.Errorf("in the %s view it is drawn under %q, want under FOUR", view, l.Text)
		}
		m = drive(t, m, letterKey("s"))
	}
	m = drive(t, m, letterKey("s")) // back to the view the review opened in

	// A comment written while split lands in the same place as one written
	// unified: the review does not know which way it was being read.
	m = onLine(t, m, "foo.txt", '+', "TWO")
	m = comment(t, m, "and this")
	th := reviewJSON(t, m.src.(*git.Repo)).Threads[1]
	if th.Side != review.SideNew || th.Line != 2 {
		t.Errorf("anchored at %s:%d, want new:2", th.Side, th.Line)
	}
}

// Every row of a split diff comments on the line it draws. A split row folds a
// removal and the addition that replaced it onto one line of the screen, so
// there are fewer rows than lines and the two stop counting alike — a pane
// that reached for f.lines[row] would anchor each remark to a line further and
// further above the one the cursor was on.
func TestSplitCommentsOnTheLineTheCursorIsOn(t *testing.T) {
	m := newTestModel(t)
	if !m.view.splitting() {
		t.Fatal("the review does not open split")
	}
	checked := 0
	for i, r := range m.view.rows {
		if r.kind != rowDiff || r.file < 0 {
			continue
		}
		f := m.view.files[r.file]
		at := m.view.liveSide(f, r.line)
		if at < 0 || at >= len(f.lines) || !isCodeLine(f.lines[at].Kind) {
			continue
		}
		m.view.cursor = i
		got, line, _, last, ok := m.view.commentTarget()
		if !ok {
			t.Errorf("row %d draws %q but offers nothing to comment on",
				i, f.lines[at].Text)
			continue
		}
		if got != f || line.Text != f.lines[at].Text {
			t.Errorf("the cursor is on %q, but a comment would land on %q",
				f.lines[at].Text, line.Text)
		}
		// And the index it reports is into the file's lines, which is what
		// the box and the thread are hung off.
		if last != at {
			t.Errorf("on %q: reported line %d, want %d", f.lines[at].Text, last, at)
		}
		checked++
	}
	if checked < 4 {
		t.Fatalf("only %d code rows checked; the fixture is not exercising this", checked)
	}
}

// S crosses a split row to the line the change took away, which is the one
// thing a split view cannot point at by moving up and down.
func TestSideKeyReachesTheRemovedLine(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "TWO")

	// As it stands, the cursor is on the new side of that row.
	_, l, ok := m.view.line()
	if !ok || l.Kind != '+' {
		t.Fatalf("the cursor is on %+v, want the added line", l)
	}

	m = drive(t, m, letterKey("S"))
	_, l, ok = m.view.line()
	if !ok || l.Kind != '-' || !strings.Contains(l.Text, "two") {
		t.Fatalf("S put the cursor on %+v, want the removed line -two", l)
	}
	if !strings.Contains(plain(m), "old side") {
		t.Error("the title does not say which side the cursor is on")
	}

	m = comment(t, m, "why was this dropped?")
	th := reviewJSON(t, m.src.(*git.Repo)).Threads[0]
	if th.Side != review.SideOld || th.Line != 2 {
		t.Errorf("anchored at %s:%d, want old:2 — the removed line", th.Side, th.Line)
	}

	// And S again comes back to the side the change is proposing. Posting
	// left the cursor on the conversation, so it goes back to a line first.
	m = onLine(t, m, "foo.txt", '-', "two")
	m = drive(t, m, letterKey("S"))
	if _, l, ok := m.view.line(); !ok || l.Kind != '+' || !strings.Contains(l.Text, "TWO") {
		t.Errorf("S did not come back to the new side: %+v", l)
	}
}

// A row with only one side draws the cursor on the side it has, whichever one
// is live: an insertion has nothing on its left to put a cursor on.
func TestSideKeyFallsBackToTheSideThatExists(t *testing.T) {
	m := drive(t, newTestModel(t), letterKey("S"))
	if !m.view.onOld {
		t.Fatal("S did not cross to the old side")
	}
	// beta.go is a new file: every line is an addition with no left half.
	m = onLine(t, m, "beta.go", '+', "package beta")
	_, l, ok := m.view.line()
	if !ok || l.Kind != '+' {
		t.Fatalf("on a pure insertion the cursor is on %+v, want the added line", l)
	}
	m = comment(t, m, "fine")
	th := reviewJSON(t, m.src.(*git.Repo)).Threads[0]
	if th.Side != review.SideNew {
		t.Errorf("anchored on the %s side, want new: there is no old line here", th.Side)
	}
}

// S says so rather than doing nothing when there are no sides to cross.
func TestSideKeyIsInertWhenUnified(t *testing.T) {
	m := drive(t, newTestModel(t), letterKey("s"))
	if m.view.splitting() {
		t.Fatal("s did not unify the diff")
	}
	m = onLine(t, m, "foo.txt", '+', "TWO")
	m = drive(t, m, letterKey("S"))
	if m.view.onOld {
		t.Error("S crossed sides in a view that has none")
	}
	if !strings.Contains(m.status, "unified") {
		t.Errorf("S said %q, which does not explain itself", m.status)
	}
}

// Switching between the two views keeps the reader on the line they were
// reading, and on the side of the diff it is numbered on — not merely in the
// file it belongs to.
func TestSplitKeepsTheLineYouWereOn(t *testing.T) {
	// Unified first, then the cursor put on the removed line there. Landing on
	// it in the split view would set onOld as a side effect, and doSplit's
	// carry would then write true over true — passing whether or not the side
	// were carried at all.
	m := drive(t, newTestModel(t), letterKey("s"))
	if m.view.splitting() {
		t.Fatal("s did not unify the diff")
	}
	m = onLine(t, m, "foo.txt", '-', "four")
	if m.view.onOld {
		t.Fatal("onOld is already set; the carry would not be under test")
	}

	m = drive(t, m, letterKey("s"))
	if !m.view.splitting() {
		t.Fatal("s did not split it again")
	}
	if _, l, ok := m.view.line(); !ok || l.Kind != '-' || !strings.Contains(l.Text, "four") {
		t.Errorf("splitting moved the cursor to %+v, want -four", l)
	}
	if !m.view.onOld {
		t.Error("s did not carry the side across with the line")
	}

	// And back to unified, still on the same line.
	m = drive(t, m, letterKey("s"))
	if m.view.splitting() {
		t.Fatal("s did not unify again")
	}
	if _, l, ok := m.view.line(); !ok || l.Kind != '-' || !strings.Contains(l.Text, "four") {
		t.Errorf("unifying moved the cursor to %+v, want -four", l)
	}
}

// The cursor bar goes on the half the cursor is in, so that what a key is
// about to act on is the thing that looks selected.
func TestSplitBarsOnlyTheLiveHalf(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "TWO")

	// The row as it is actually drawn, colours and all: the bar is a styled
	// space, so stripping the escapes would strip the thing under test.
	changedRow := func(m Model) string {
		t.Helper()
		raw := strings.Split(m.frame(), "\n")
		for i, l := range strings.Split(ansi.Strip(m.frame()), "\n") {
			if strings.Contains(l, "-two") && strings.Contains(l, "+ TWO") {
				return raw[i]
			}
		}
		t.Fatalf("no row draws both sides of the change:\n%s", plain(m))
		return ""
	}

	onNew := changedRow(m)
	m = drive(t, m, letterKey("S"))
	onOld := changedRow(m)
	if onNew == onOld {
		t.Error("the row is drawn the same on either side; nothing says which half is live")
	}

	// The bar is at the head of the half it is in, so the two differ in the
	// left half when the cursor is on the old side and not before it.
	left, _ := m.view.halvesOf(m.view.rowW())
	if ansi.Truncate(onNew, left, "") == ansi.Truncate(onOld, left, "") {
		t.Error("the left half is drawn the same whether or not the cursor is in it")
	}
}

// A comment belongs to a line, and stays on it when the pane changes shape
// under it. A resize can cross the split threshold, and the rows either side of
// that are numbered differently — a remark keyed by a row would move to
// whatever line took its number, which is roughly half a file away.
func TestCommentsSurviveCrossingTheThreshold(t *testing.T) {
	narrow := tea.WindowSizeMsg{Width: minSplitWidth - 10, Height: 40}
	wide := tea.WindowSizeMsg{Width: minSplitWidth + 60, Height: 40}
	m := drive(t, newTestModel(t), wide)
	if !m.view.splitting() {
		t.Fatalf("a window of %d columns did not split the diff", m.view.width)
	}

	// Written while the pane is split, which is what gives the key assertion
	// below any teeth: a comment filed in a unified pane is filed against a
	// line whose index and row number are the same, so a row-keyed map and a
	// line-keyed one would store the same number and neither could be caught.
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "about FOUR")

	// Off and on again: the rows are renumbered each way.
	m = drive(t, m, narrow)
	if m.view.splitting() {
		t.Fatalf("the pane is %d columns and still split; it never crossed", m.view.width)
	}
	m = drive(t, m, wide)
	if !m.view.splitting() {
		t.Fatalf("the pane is %d columns and not split; it never came back", m.view.width)
	}
	f := fileIn(t, m, "foo.txt")
	// The key is an index into the lines, and the row drawing that line is a
	// different number now the pane has split — which is the whole point, and
	// what makes this assertion worth making. Both are named here so that
	// checking the key against the line index is not the same as checking it
	// against the row: in a unified pane those coincide and either would pass.
	wantAt, wantRow := -1, -1
	for i, l := range f.lines {
		if l.Kind == '+' && l.New == 4 {
			wantAt, wantRow = i, f.rowAt[i]
			break
		}
	}
	if wantAt < 0 {
		t.Fatal("FOUR is not in the diff")
	}
	if wantAt == wantRow {
		t.Fatalf("line %d is drawn on row %d: the two coincide, so this proves nothing",
			wantAt, wantRow)
	}
	seen := 0
	for at, threads := range f.threads {
		seen += len(threads)
		if at != wantAt {
			t.Errorf("the comment is keyed to %d (line %d); want line index %d, and note "+
				"row %d is what a row-keyed map would hold",
				at, f.lines[at].New, wantAt, wantRow)
		}
	}
	if seen != 1 {
		t.Fatalf("%d comments after crossing the threshold, want 1", seen)
	}
	// And it is drawn against that line, not merely filed against it.
	head := -1
	for i, r := range m.view.rows {
		if r.kind == rowThread && r.first {
			head = i
		}
	}
	if head < 1 {
		t.Fatal("the conversation is not drawn")
	}
	above := m.view.rows[head-1]
	if l, ok := m.view.files[above.file].rowLine(above.line); !ok || l.New != 4 {
		t.Errorf("the conversation is drawn under %q, want under FOUR", l.Text)
	}
}

// 24g can only mean a line of the file as it now stands, so it lands on the
// new side of a split row whatever side the cursor was on. Otherwise the
// status names one line and c comments on another.
func TestGoToLineComesBackToTheNewSide(t *testing.T) {
	m := newTestModel(t)
	if !m.view.splitting() {
		t.Skipf("the review pane is %d columns, not split", m.view.width)
	}
	m = onLine(t, m, "foo.txt", '-', "two")
	if !m.view.onOld {
		t.Fatal("the cursor is not on the old side to begin with")
	}
	m = drive(t, m, letterKey("4"), letterKey("g"))
	if m.view.onOld {
		t.Error("24g-style jump left the cursor on the old side")
	}
	_, l, ok := m.view.line()
	if !ok || l.New != 4 {
		t.Fatalf("the cursor is on %+v, want new line 4", l)
	}
	// And a comment goes where the status said it would.
	m = comment(t, m, "here")
	th := reviewJSON(t, m.src.(*git.Repo)).Threads[0]
	if th.Side != review.SideNew || th.Line != 4 {
		t.Errorf("anchored at %s:%d, want new:4 — the line jumped to", th.Side, th.Line)
	}
}

// The title names the side the next key will act on, which is not the same as
// the side that was asked for: a row with nothing on its left draws the cursor
// on the right whatever S said, and a comment there anchors to the new side.
func TestTheTitleNamesTheSideThatWillBeActedOn(t *testing.T) {
	m := drive(t, newTestModel(t), letterKey("S"))
	if !m.view.onOld {
		t.Fatal("S did not cross to the old side")
	}
	m = onLine(t, m, "foo.txt", '-', "two")
	// The title, not the whole frame: pressing S puts its own account of what
	// it did in the status bar, which would answer for the title's words.
	if title, _, _ := m.view.titleLabel(); !strings.Contains(title, "old side") {
		t.Errorf("on a removed line the title is %q, want it to say `old side`", title)
	}

	// beta.go is a new file: every line is an addition with no left half.
	m = onLine(t, m, "beta.go", '+', "package beta")
	m = drive(t, m, letterKey("S"))
	if !m.view.onOld {
		t.Fatal("S did not set the old side")
	}
	if title, _, _ := m.view.titleLabel(); strings.Contains(title, "old side") {
		t.Errorf("the title is %q on a row that has no old side", title)
	}
}

// The hint offers what s would do next. On a pane too narrow to split, the
// preference is still on even though the screen is unified, so the offer is to
// turn it off — otherwise `s split` turns the preference off and reports the
// opposite of what was asked for.
func TestTheHintFollowsThePreferenceNotTheDrawing(t *testing.T) {
	m := drive(t, newTestModel(t), tea.WindowSizeMsg{Width: 70, Height: 30})
	m = drive(t, m, altKey("1"))
	if m.view.splitting() {
		t.Skipf("the review pane is %d columns, wide enough to split", m.view.width)
	}
	if !m.view.split {
		t.Fatal("the preference was dropped")
	}
	if got := m.hints(); !strings.Contains(got, "s unify") {
		t.Errorf("hint = %q, want it to offer `s unify`: the preference is on", got)
	}
	m = drive(t, m, letterKey("s"))
	if got := m.hints(); !strings.Contains(got, "s split") {
		t.Errorf("after unifying, hint = %q, want it to offer `s split`", got)
	}
}

// A resize keeps the reader on the line, and on the side of the diff it is
// numbered on: crossing the threshold must not slide the cursor off a removed
// line onto the addition that replaced it.
func TestResizingKeepsTheLineAndItsSide(t *testing.T) {
	// Started narrow, where the pane is unified, and that is the whole point:
	// a unified row draws one line, so the cursor is on the removed line
	// without onOld being set. Started split instead, the helper would set
	// onOld itself and the carry would write true over true — the side would
	// ride along whether or not anything carried it, and this test would pass
	// with the carry deleted.
	m := drive(t, newTestModel(t), tea.WindowSizeMsg{Width: minSplitWidth - 10, Height: 40})
	if m.view.splitting() {
		t.Fatalf("the review pane is %d columns and already split", m.view.width)
	}
	m = onLine(t, m, "foo.txt", '-', "four")
	if m.view.onOld {
		t.Fatal("onOld is already set; the carry would not be under test")
	}
	if _, l, ok := m.view.line(); !ok || l.Kind != '-' {
		t.Fatalf("the cursor is on %+v, want the removed line", l)
	}

	// Widen across the threshold. Without the carry, goToDiffLine finds the
	// row and liveSide then takes its new half — landing the reader on FOUR,
	// the addition that replaced the line they were reading.
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40})
	if !m.view.splitting() {
		t.Fatalf("the pane is %d columns and not split; it never crossed", m.view.width)
	}
	if _, l, ok := m.view.line(); !ok || l.Kind != '-' || !strings.Contains(l.Text, "four") {
		t.Errorf("widening moved the cursor to %+v, want -four", l)
	}
	if !m.view.onOld {
		t.Error("the side was not carried across the threshold")
	}

	// And back down again.
	m = drive(t, m, tea.WindowSizeMsg{Width: minSplitWidth - 10, Height: 40})
	if _, l, ok := m.view.line(); !ok || l.Kind != '-' || !strings.Contains(l.Text, "four") {
		t.Errorf("narrowing moved the cursor to %+v, want -four", l)
	}
}

// A line neither side touched is drawn in both halves, so it cannot say which
// half the cursor is in. A relay must leave the reader's own choice standing
// rather than reading it back off a line that never held it.
func TestRelayKeepsTheSideChosenOnAContextLine(t *testing.T) {
	m := drive(t, newTestModel(t), tea.WindowSizeMsg{Width: 140, Height: 40})
	if !m.view.splitting() {
		t.Skipf("the review pane is %d columns, not split", m.view.width)
	}
	m = onLine(t, m, "foo.txt", ' ', "three")
	m = drive(t, m, letterKey("S"))
	if !m.view.onOld {
		t.Fatal("S did not cross to the old side")
	}

	// Any relay at all: the width moves, the rows are laid out again.
	m = drive(t, m, tea.WindowSizeMsg{Width: 150, Height: 40})
	if !m.view.onOld {
		t.Error("the relay dropped the side the reader chose on a context line")
	}
	// And the cursor is still on that line.
	if _, l, ok := m.view.line(); !ok || !strings.Contains(l.Text, "three") {
		t.Errorf("the cursor moved to %+v, want the context line three", l)
	}
}

// A conversation is found again by its own name. A remark is not a line, and
// the row that inherits its number after a relay may be somebody else's
// remark — where r would reply to something nobody was reading.
func TestRelayKeepsTheCursorOnTheSameConversation(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "TWO")
	m = comment(t, m, "first remark")
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "second remark")

	// Sit on the first conversation, about to reply to it.
	want := reviewJSON(t, m.src.(*git.Repo)).Threads[0].ID
	m = cursorTo(t, m, func(r reviewRow) bool {
		return r.kind == rowThread && r.thread == want && r.first
	})
	if got := m.view.thread(); got != want {
		t.Fatalf("the cursor is on %q, want %q", got, want)
	}

	// Relay the rows under it, both ways across the threshold: at 120 columns
	// the pane is under it and draws unified, at 140 it is over and splits.
	for _, w := range []int{120, 140, 120} {
		m = drive(t, m, tea.WindowSizeMsg{Width: w, Height: 40})
		if got := m.view.thread(); got != want {
			t.Fatalf("at %d columns the cursor is on %q, want %q — r would reply to the wrong remark",
				w, got, want)
		}
	}
}

// ---------------------------------------------------------------------------
// Space discloses, and Enter is the same key
// ---------------------------------------------------------------------------

// Space on a line folds the hunk the line is in and leaves the file open, and
// writes nothing. Enter is the same key.
func TestSpaceInsideAFileFoldsTheHunk(t *testing.T) {
	for _, k := range []tea.KeyPressMsg{letterKey(" "), namedKey(tea.KeyEnter)} {
		m := newTestModel(t)
		m = onLine(t, m, "foo.txt", '+', "FOUR")
		_, before := hunkRows(m, "foo.txt")

		m = drive(t, m, k)
		if fileIn(t, m, "foo.txt").folded {
			t.Errorf("%s on a line folded the whole file", k)
		}
		if _, n := hunkRows(m, "foo.txt"); n != 0 {
			t.Errorf("%s left %d of the hunk's %d lines showing (status %q)", k, n, before, m.status)
		}
		if m.compose != nil || m.view.draft != nil {
			t.Errorf("%s opened a comment box on a line of the diff", k)
		}
		if n := len(m.rv.Threads); n != 0 {
			t.Errorf("%d threads after pressing %s, want 0", n, k)
		}

		// And again opens it: the cursor was left on the header.
		m = drive(t, m, k)
		if _, n := hunkRows(m, "foo.txt"); n != before {
			t.Errorf("%s again showed %d lines, want %d", k, n, before)
		}
	}
}

// c is how a comment begins, and the only way.
func TestCStartsAComment(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = drive(t, m, letterKey("c"))
	if m.compose == nil {
		t.Fatalf("c did not open the composer (status %q)", m.status)
	}
	if m.compose.line != 4 || m.compose.side != review.SideNew {
		t.Errorf("c aimed at %s:%d, want new:4", m.compose.side, m.compose.line)
	}
}

// What Space is for still works: a file's heading folds, and a resolved
// conversation opens and shuts.
func TestSpaceStillFoldsAndUnfolds(t *testing.T) {
	m := newTestModel(t)
	m = cursorTo(t, m, func(r reviewRow) bool { return r.kind == rowFile })
	path := m.view.pathAt()

	want := !fileIn(t, m, path).folded
	m = drive(t, m, letterKey(" "))
	if fileIn(t, m, path).folded != want {
		t.Errorf("Space did not fold %s", path)
	}
	m = drive(t, m, letterKey(" "))
	if fileIn(t, m, path).folded == want {
		t.Errorf("Space did not open %s again", path)
	}

	// A resolved conversation collapses to one line, and Space opens it.
	m = onLine(t, m, "foo.txt", '+', "TWO")
	m = comment(t, m, "answer this")
	id := reviewJSON(t, m.src.(*git.Repo)).Threads[0].ID
	m = drive(t, m, letterKey("x")) // resolve it
	m = cursorTo(t, m, func(r reviewRow) bool {
		return r.kind == rowThread && r.thread == id && r.first
	})
	if m.view.expanded[id] {
		t.Fatal("the resolved thread is already open")
	}
	m = drive(t, m, letterKey(" "))
	if !m.view.expanded[id] {
		t.Error("Space did not open the resolved conversation")
	}
}

// An open conversation is part of the hunk it hangs off, so Space and Enter
// fold that rather than replying to it — replying is writing, and neither of
// them ever writes. r is the one that replies.
func TestSpaceOnAnOpenCommentDoesNotReply(t *testing.T) {
	for _, k := range []tea.KeyPressMsg{letterKey(" "), namedKey(tea.KeyEnter)} {
		m := newTestModel(t)
		m = onLine(t, m, "foo.txt", '+', "TWO")
		m = comment(t, m, "why shout?")
		id := reviewJSON(t, m.src.(*git.Repo)).Threads[0].ID
		m = cursorTo(t, m, func(r reviewRow) bool {
			return r.kind == rowThread && r.thread == id && r.first
		})

		m = drive(t, m, k)
		if m.compose != nil {
			t.Errorf("%s opened a reply box on an open conversation", k)
		}
		if fileIn(t, m, "foo.txt").folded {
			t.Errorf("%s on a conversation folded the whole file", k)
		}
		if _, n := hunkRows(m, "foo.txt"); n != 0 {
			t.Errorf("%s on a conversation did not fold the hunk it is in", k)
		}
	}
}

// A pane too narrow for two columns draws a unified diff without forgetting
// that it would rather be split: it says so in the title, and splits itself as
// soon as there is room.
func TestSplitWaitsForTheRoom(t *testing.T) {
	m := drive(t, newTestModel(t), tea.WindowSizeMsg{Width: 70, Height: 30})
	m = drive(t, m, altKey("1"))
	if m.view.width >= minSplitWidth {
		t.Skipf("the review pane is %d columns, wide enough to split", m.view.width)
	}
	if !m.view.split {
		t.Error("the preference was forgotten")
	}
	if m.view.splitting() {
		t.Error("it split a pane too narrow for it")
	}
	// Nobody asked for it here, so nothing is explained: a default doing the
	// best it can with the room it has is not a request going unmet.
	if strings.Contains(plain(m), "too narrow") {
		t.Errorf("the title explains a choice nobody made:\n%s", plain(m))
	}

	// Asking for it says why it cannot be given rather than doing nothing,
	// and the title keeps saying so after the status line has gone.
	m = drive(t, m, letterKey("s")) // off
	m = drive(t, m, letterKey("s")) // on again, but still too narrow
	if !strings.Contains(m.status, "columns") {
		t.Errorf("status = %q, want it to say why", m.status)
	}
	if !strings.Contains(plain(m), "too narrow") {
		t.Errorf("the title does not say why it is not split:\n%s", plain(m))
	}

	m = drive(t, m, tea.WindowSizeMsg{Width: 200, Height: 30})
	if !m.view.splitting() {
		t.Error("it did not split once there was room")
	}
}

// A large review still has its colours.
//
// Highlighting used to be governed by a budget over the review as a whole, and
// discobox-review's own source went past it: every file was drawn plain, and nothing
// said why. The limit is per file now, because you read one file at a time —
// a review being large is no reason for the thirty line file in it to lose its
// colours.
func TestALargeReviewIsStillHighlighted(t *testing.T) {
	var b strings.Builder
	// Thirty files of a thousand lines: far past anything a whole-review
	// budget would have allowed.
	for f := 0; f < 30; f++ {
		fmt.Fprintf(&b, "diff --git a/f%02d.go b/f%02d.go\n--- a/f%02d.go\n+++ b/f%02d.go\n@@ -1,1000 +1,1000 @@\n", f, f, f, f)
		for i := 0; i < 1000; i++ {
			fmt.Fprintf(&b, "+func f%d() string { return \"x\" }\n", i)
		}
	}
	files := buildFiles(b.String(), "", nil)
	if len(files) != 30 {
		t.Fatalf("%d files", len(files))
	}
	total := 0
	for _, f := range files {
		total += len(f.lines)
		if f.plain {
			t.Errorf("%s was shown plain at %d lines", f.path, len(f.lines))
		}
		coloured := false
		for _, sp := range f.spans {
			if len(sp) > 0 {
				coloured = true
				break
			}
		}
		if !coloured {
			t.Errorf("%s has no syntax spans at all", f.path)
		}
	}
	if total < 20000 {
		t.Fatalf("the fixture is only %d lines; it has to be past the old budget", total)
	}
}

// One file too big to lex is drawn plain, and says so — the failure that
// started all this was a silent one.
func TestAFileTooBigToLexSaysSo(t *testing.T) {
	var b strings.Builder
	fmt.Fprintf(&b, "diff --git a/huge.go b/huge.go\n--- a/huge.go\n+++ b/huge.go\n@@ -1,%d +1,%d @@\n",
		maxLexLines+10, maxLexLines+10)
	for i := 0; i < maxLexLines+10; i++ {
		fmt.Fprintf(&b, "+var x%d = 1\n", i)
	}
	files := buildFiles(b.String(), "", nil)
	if len(files) != 1 || !files[0].plain {
		t.Fatalf("files = %+v, want one marked plain", files)
	}
	if got := files[0].headerTail(80); !strings.Contains(got, "plain") {
		t.Errorf("heading tail = %q, want it to say the file is plain", got)
	}
	// And it is drawn, just without colours.
	for _, sp := range files[0].spans {
		if len(sp) > 0 {
			t.Error("a file marked plain was lexed anyway")
			break
		}
	}
}

// ---------------------------------------------------------------------------
// Deleting
// ---------------------------------------------------------------------------

// twoComments is a thread with a remark and a reply on it.
func twoComments(t *testing.T) (Model, string) {
	t.Helper()
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "why shout?")
	id := reviewJSON(t, m.src.(*git.Repo)).Threads[0].ID
	m = cursorTo(t, m, func(r reviewRow) bool { return r.thread == id && r.first })
	m = drive(t, m, letterKey("r"))
	m.editor.SetValue("because it is a shout")
	m = drive(t, m, ctrlKey("s"))
	return m, id
}

// X on one remark of a conversation takes that remark and leaves the rest.
func TestDeletingOneComment(t *testing.T) {
	m, id := twoComments(t)
	if got := len(reviewJSON(t, m.src.(*git.Repo)).Thread(id).Comments); got != 2 {
		t.Fatalf("%d comments to begin with", got)
	}

	// The cursor onto the reply — the second remark, not the first.
	m = cursorTo(t, m, func(r reviewRow) bool {
		return r.thread == id && r.comment == 1
	})
	m = drive(t, m, letterKey("X"))
	if m.dialog == nil {
		t.Fatal("X deleted without asking")
	}
	if len(reviewJSON(t, m.src.(*git.Repo)).Thread(id).Comments) != 2 {
		t.Fatal("it went before the question was answered")
	}
	m = drive(t, m, letterKey("y"))

	th := reviewJSON(t, m.src.(*git.Repo)).Thread(id)
	if th == nil {
		t.Fatal("the whole conversation went")
	}
	if len(th.Comments) != 1 {
		t.Fatalf("%d comments left, want 1", len(th.Comments))
	}
	if th.Comments[0].Body != "why shout?" {
		t.Errorf("the surviving comment is %q, want the first one", th.Comments[0].Body)
	}
	if !strings.Contains(plain(m), "why shout?") || strings.Contains(plain(m), "because it is a shout") {
		t.Error("the screen does not match what is left")
	}
}

// The opening remark can go too: it can be as wrong as any other, and what it
// was anchored to stays, because the replies are still about that line.
func TestDeletingTheOpeningRemark(t *testing.T) {
	m, id := twoComments(t)
	m = cursorTo(t, m, func(r reviewRow) bool {
		return r.thread == id && r.comment == 0
	})
	m = drive(t, m, letterKey("X"), letterKey("y"))

	th := reviewJSON(t, m.src.(*git.Repo)).Thread(id)
	if th == nil || len(th.Comments) != 1 {
		t.Fatalf("thread = %+v", th)
	}
	if th.Comments[0].Body != "because it is a shout" {
		t.Errorf("the reply did not survive: %q", th.Comments[0].Body)
	}
	if th.Line != 4 {
		t.Errorf("the anchor moved to line %d", th.Line)
	}
}

// The rule above a conversation and the hint below it belong to the whole of
// it, so X there takes the whole of it.
func TestDeletingTheWholeConversation(t *testing.T) {
	m, id := twoComments(t)
	m = cursorTo(t, m, func(r reviewRow) bool { return r.thread == id && r.first })
	m = drive(t, m, letterKey("X"))
	if !strings.Contains(m.dialog.body, "2 comments") {
		t.Errorf("the question is %q, want it to name what goes", m.dialog.body)
	}
	m = drive(t, m, letterKey("y"))
	if reviewJSON(t, m.src.(*git.Repo)).Thread(id) != nil {
		t.Error("the conversation survived")
	}
}

// Taking the last remark out takes the conversation with it: an anchor on a
// line with nothing to say is not a thing to leave behind.
func TestDeletingTheLastCommentTakesTheThread(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "never mind")
	id := reviewJSON(t, m.src.(*git.Repo)).Threads[0].ID

	m = cursorTo(t, m, func(r reviewRow) bool { return r.thread == id && r.comment == 0 })
	m = drive(t, m, letterKey("X"))
	// With one comment on it, the question is about the conversation.
	if !strings.Contains(m.dialog.body, "Resolving it instead") {
		t.Errorf("question = %q, want the whole-conversation one", m.dialog.body)
	}
	m = drive(t, m, letterKey("y"))
	if len(reviewJSON(t, m.src.(*git.Repo)).Threads) != 0 {
		t.Error("an empty conversation was left behind")
	}
}

// The base can be a commit, not only a ref: the point you want to measure from
// is often somewhere in your own history, and it has no name but its hash.
func TestChoosingACommitAsTheBase(t *testing.T) {
	m := newTestModel(t)
	m = drive(t, m, letterKey("b"))
	if m.dialog == nil {
		t.Fatal("b offered nothing")
	}
	found := false
	for _, it := range m.dialog.items {
		if it == commitBase {
			found = true
		}
	}
	if !found {
		t.Fatalf("the base list has no way to a commit: %v", m.dialog.items)
	}

	m = drive(t, m, baseChosenMsg{ref: commitBase})
	if m.dialog == nil {
		t.Fatalf("the commit picker did not open (status %q)", m.status)
	}
	if len(m.dialog.items) < 2 {
		t.Fatalf("the picker offers %v", m.dialog.items)
	}
	// Newest first, hash leading, so the row identifies the commit and the
	// choice can be read straight off it.
	first := m.dialog.items[0]
	if !strings.Contains(first, "add beta") {
		t.Errorf("the first row is %q, want the newest commit", first)
	}

	// Picking the second row measures from the commit before the last one.
	second := m.dialog.items[1]
	hash, _, _ := strings.Cut(second, " ")
	m = drive(t, m, baseChosenMsg{ref: hash})

	if m.baseRef != hash {
		t.Errorf("baseRef = %q, want %q", m.baseRef, hash)
	}
	if got := reviewJSON(t, m.src.(*git.Repo)).Base; got != hash {
		t.Errorf("the review file says base %q", got)
	}
	// Measured from "capitalise two", that commit's own change is behind the
	// base and only what came after it is under review.
	for _, l := range fileIn(t, m, "foo.txt").lines {
		if (l.Kind == '+' || l.Kind == '-') && strings.Contains(l.Text, "TWO") {
			t.Errorf("a change behind the chosen commit is still under review: %q", l.Text)
		}
	}
	if !strings.Contains(plain(m), hash[:7]) {
		t.Error("the bar does not say which commit the review is measured from")
	}
}

// Git lists the tracked files in path order and discobox-review appends the untracked
// ones, so a new file arrives at the end of the diff however its name reads.
// The review sorts them back together.
func TestFilesAreSortedByPath(t *testing.T) {
	var b strings.Builder
	// The order they arrive in: the tracked files in path order, then the
	// untracked ones appended however git listed them.
	for _, path := range []string{"internal/mid.go", "zz/last.go", "aardvark.go", "internal/aaa.go"} {
		fmt.Fprintf(&b, "diff --git a/%s b/%s\n--- a/%s\n+++ b/%s\n@@ -1 +1 @@\n+x\n", path, path, path, path)
	}
	files := buildFiles(b.String(), "", nil)

	var got []string
	for _, f := range files {
		got = append(got, f.path)
	}
	want := []string{"aardvark.go", "internal/aaa.go", "internal/mid.go", "zz/last.go"}
	if !slices.Equal(got, want) {
		t.Errorf("the stack is in %v, want %v", got, want)
	}
}

// hunkRows counts the rows a file is drawing: its hunk headers, and the lines
// of code under them.
func hunkRows(m Model, path string) (heads, code int) {
	for _, r := range m.view.rows {
		if r.kind != rowDiff || r.file < 0 || m.view.files[r.file].path != path {
			continue
		}
		switch l, _ := m.view.files[r.file].rowLine(r.line); {
		case l.Kind == '@':
			heads++
		case isCodeLine(l.Kind):
			code++
		}
	}
	return heads, code
}

// twoHunkRepo is a review of two files, one of them changed in two places far
// enough apart to be a hunk each.
func twoHunkRepo(t *testing.T) *git.Repo {
	t.Helper()
	return changedRepo(t, 40, map[int]string{4: "FOUR", 36: "THIRTY-SIX"})
}

// changedRepo is a review of two files: foo.txt, so many numbered lines long
// with some of them rewritten, and bar.txt with its one line changed.
func changedRepo(t *testing.T, lines int, changes map[int]string) *git.Repo {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.name", "Test"},
		{"config", "user.email", "test@example.com"},
		{"config", "commit.gpgsign", "false"},
	} {
		gitRun(t, dir, args...)
	}
	var was, is strings.Builder
	for i := 1; i <= lines; i++ {
		line := "line " + itoa(i)
		was.WriteString(line + "\n")
		if to, ok := changes[i]; ok {
			line = to
		}
		is.WriteString(line + "\n")
	}
	writeFile(t, dir, "foo.txt", was.String())
	writeFile(t, dir, "bar.txt", "alpha\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "initial")
	gitRun(t, dir, "checkout", "-q", "-b", "feature")
	writeFile(t, dir, "foo.txt", is.String())
	writeFile(t, dir, "bar.txt", "beta\n")
	r, err := git.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// Space folds the hunk the cursor is in and nothing else: the file stays open,
// the cursor lands on the header that is left, and the same key opens it again.
func TestSpaceFoldsJustTheHunk(t *testing.T) {
	for _, k := range []tea.KeyPressMsg{letterKey(" "), namedKey(tea.KeyEnter)} {
		m := modelFor(t, twoHunkRepo(t))
		heads, before := hunkRows(m, "foo.txt")
		if heads != 2 {
			t.Fatalf("foo.txt has %d hunks, want 2", heads)
		}
		_, others := hunkRows(m, "bar.txt")

		m = onLine(t, m, "foo.txt", '+', "FOUR")
		m = drive(t, m, k)
		if fileIn(t, m, "foo.txt").folded {
			t.Fatalf("%s folded the whole file", k)
		}
		heads, after := hunkRows(m, "foo.txt")
		if heads != 2 || after == 0 || after >= before {
			t.Errorf("%s left %d hunks and %d of %d lines, want both hunks and one's lines (status %q)",
				k, heads, after, before, m.status)
		}
		if _, n := hunkRows(m, "bar.txt"); n != others {
			t.Errorf("%s folded a hunk of bar.txt too", k)
		}
		if _, l, ok := m.view.line(); !ok || l.Kind != '@' {
			t.Errorf("%s left the cursor off the folded hunk's header", k)
		}
		if !strings.Contains(plain(m), "10 lines folded") {
			t.Errorf("the folded hunk does not say what it is holding back:\n%s", plain(m))
		}

		// More context joins the two hunks into one, and the fold goes with
		// the first line it changed rather than with a position that moved.
		m = drive(t, m, letterKey("="))
		m = drive(t, m, letterKey("-"))
		if _, n := hunkRows(m, "foo.txt"); n != after {
			t.Errorf("changing the context left %d lines showing, want %d", n, after)
		}

		m = drive(t, m, k)
		if _, n := hunkRows(m, "foo.txt"); n != before {
			t.Errorf("%s again showed %d lines, want %d", k, n, before)
		}
	}
}

// A fold must not be how a comment gets missed: the header of a folded hunk
// says when there is one in there still waiting.
func TestAFoldedHunkSaysItHoldsAnOpenComment(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = drive(t, m, letterKey("c"))
	m = typeText(t, m, "why")
	m = drive(t, m, ctrlKey("s"))
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = drive(t, m, letterKey(" "))
	if !strings.Contains(plain(m), "1 open comment") {
		t.Errorf("the folded hunk hides its comment without a word:\n%s", plain(m))
	}
	for _, r := range m.view.rows {
		if r.kind == rowThread {
			t.Fatal("a thread is still drawn under a folded hunk")
		}
	}
}

// a inside a hunk signs off that hunk: it folds, the file says how far along
// it is, and the file itself is not approved until the last of them is.
func TestAApprovesJustTheHunk(t *testing.T) {
	m := modelFor(t, twoHunkRepo(t))
	_, before := hunkRows(m, "foo.txt")

	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = drive(t, m, letterKey("a"))
	if m.rv.Approved("foo.txt", "Test") {
		t.Fatal("approving one hunk of two approved the file")
	}
	if n := len(reviewJSON(t, m.src.(*git.Repo)).Files[0].Verdicts[0].Hunks); n != 1 {
		t.Errorf("the review records %d approved hunks, want 1", n)
	}
	if _, n := hunkRows(m, "foo.txt"); n == 0 || n >= before {
		t.Errorf("the approved hunk did not fold: %d of %d lines showing", n, before)
	}
	for _, want := range []string{"✓ approved", "✓ 1/2 hunks"} {
		if !strings.Contains(plain(m), want) {
			t.Errorf("the window does not say %q:\n%s", want, plain(m))
		}
	}

	// More context joins the two hunks into one that is only half read, so it
	// is not approved; less puts them back, and the approval with them.
	for i := 0; i < 40; i++ {
		m = drive(t, m, letterKey("="))
	}
	if n, of := fileIn(t, m, "foo.txt").hunksApproved(); n != 0 || of != 1 {
		t.Errorf("with the hunks joined, %d of %d are approved, want 0 of 1", n, of)
	}
	for i := 0; i < 40; i++ {
		m = drive(t, m, letterKey("-"))
	}
	if n, of := fileIn(t, m, "foo.txt").hunksApproved(); n != 1 || of != 2 {
		t.Errorf("with the hunks apart again, %d of %d are approved, want 1 of 2", n, of)
	}

	// a again takes it back.
	m = onLine(t, m, "foo.txt", '@', "-1,")
	m = drive(t, m, letterKey("a"))
	if n, _ := fileIn(t, m, "foo.txt").hunksApproved(); n != 0 {
		t.Errorf("a on an approved hunk left %d approved", n)
	}

	// The last hunk approved is the file approved.
	m = drive(t, m, letterKey("a"))
	m = onLine(t, m, "foo.txt", '+', "THIRTY-SIX")
	m = drive(t, m, letterKey("a"))
	if !m.rv.Approved("foo.txt", "Test") {
		t.Fatalf("approving every hunk did not approve the file (status %q)", m.status)
	}

	// And a hunk taken back out of an approved file leaves the other standing.
	m = cursorTo(t, m, func(r reviewRow) bool {
		return r.kind == rowFile && m.view.files[r.file].path == "foo.txt"
	})
	m = drive(t, m, letterKey(" ")) // open the file the approval folded
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = drive(t, m, letterKey("a"))
	if m.rv.Approved("foo.txt", "Test") {
		t.Error("withdrawing a hunk left the file approved")
	}
	if n, of := fileIn(t, m, "foo.txt").hunksApproved(); n != 1 || of != 2 {
		t.Errorf("%d of %d hunks approved after withdrawing one, want 1 of 2", n, of)
	}
}

// a on a file's heading is still the whole file, whatever its hunks.
func TestAOnTheHeadingApprovesTheFile(t *testing.T) {
	m := modelFor(t, twoHunkRepo(t))
	m = cursorTo(t, m, func(r reviewRow) bool {
		return r.kind == rowFile && m.view.files[r.file].path == "foo.txt"
	})
	m = drive(t, m, letterKey("a"))
	if !m.rv.Approved("foo.txt", "Test") {
		t.Fatalf("a on the heading did not approve the file (status %q)", m.status)
	}
	m = drive(t, m, letterKey("a"))
	if m.rv.Approved("foo.txt", "Test") {
		t.Error("a on the heading did not take the approval back")
	}
	if n, _ := fileIn(t, m, "foo.txt").hunksApproved(); n != 0 {
		t.Errorf("withdrawing the file's approval left %d hunks approved", n)
	}
}

// gapLines counts the lines of a file that an opened gap has let out.
func gapLines(m Model, path string) int {
	n := 0
	f := m.view.files[m.view.byPath[path]]
	for i, l := range f.lines {
		if _, ok := f.gapAt(i); ok && l.Kind == ' ' {
			n++
		}
	}
	return n
}

// Where the diff skips part of a file there is a row saying how much, and
// Enter or Space on it shows those lines and puts them away again.
func TestTheGapBetweenHunksOpens(t *testing.T) {
	for _, k := range []tea.KeyPressMsg{namedKey(tea.KeyEnter), letterKey(" ")} {
		m := modelFor(t, twoHunkRepo(t))
		if !strings.Contains(plain(m), "▸ 21 unchanged lines") {
			t.Fatalf("the gap between the hunks is not counted:\n%s", plain(m))
		}
		m = onLine(t, m, "foo.txt", 'g', "")
		m = drive(t, m, k)
		if n := gapLines(m, "foo.txt"); n != 21 {
			t.Fatalf("%s on the gap let out %d lines, want 21 (status %q)", k, n, m.status)
		}
		if fileIn(t, m, "foo.txt").folded {
			t.Fatalf("%s on the gap folded the file", k)
		}
		for _, want := range []string{"▾ 21 unchanged lines", "line 10", "line 20"} {
			if !strings.Contains(plain(m), want) {
				t.Errorf("the opened gap does not show %q:\n%s", want, plain(m))
			}
		}

		// The lines are numbered, so one can be gone to and commented on.
		m = onLine(t, m, "foo.txt", ' ', "line 20")
		if _, l, ok := m.view.line(); !ok || l.New != 20 || l.Old != 20 {
			t.Errorf("line 20 of the gap is numbered %d/%d", l.Old, l.New)
		}

		// It stays open when the diff is read again with other context, and
		// has fewer lines to show for it.
		m = drive(t, m, letterKey("="))
		if n := gapLines(m, "foo.txt"); n != 19 {
			t.Errorf("after more context the gap shows %d lines, want 19", n)
		}

		// And from any line of it, the same key puts it away.
		m = onLine(t, m, "foo.txt", ' ', "line 20")
		m = drive(t, m, k)
		if n := gapLines(m, "foo.txt"); n != 0 {
			t.Errorf("%s on a line of the gap left %d lines out", k, n)
		}
		if _, l, ok := m.view.line(); !ok || l.Kind != 'g' {
			t.Errorf("%s left the cursor off the gap's row", k)
		}
	}
}

// An approval is of runs of changed lines, and one the present context has
// joined into a hunk that is only half read must survive a decision made
// about another hunk of the same file while it is hidden there.
func TestApprovingAHunkKeepsWhatTheContextHides(t *testing.T) {
	m := modelFor(t, changedRepo(t, 60, map[int]string{4: "FOUR", 20: "TWENTY", 50: "FIFTY"}))
	if n, of := fileIn(t, m, "foo.txt").hunksApproved(); n != 0 || of != 3 {
		t.Fatalf("foo.txt starts with %d of %d hunks approved, want 0 of 3", n, of)
	}
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = drive(t, m, letterKey("a"))

	// Enough context that the first two hunks join, into one that is only
	// half read, and the third stays apart.
	for i := 0; i < 3; i++ {
		m = drive(t, m, letterKey("="))
	}
	if n, of := fileIn(t, m, "foo.txt").hunksApproved(); n != 0 || of != 2 {
		t.Fatalf("with the context up, %d of %d hunks are approved, want 0 of 2", n, of)
	}
	m = onLine(t, m, "foo.txt", '+', "FIFTY")
	m = drive(t, m, letterKey("a"))

	for i := 0; i < 3; i++ {
		m = drive(t, m, letterKey("-"))
	}
	if n, of := fileIn(t, m, "foo.txt").hunksApproved(); n != 2 || of != 3 {
		t.Errorf("%d of %d hunks approved once they are apart again, want 2 of 3: "+
			"approving FIFTY dropped FOUR", n, of)
	}
}

// A decision about one hunk is made against the review as it stands on disk,
// so a hunk something else signed off since the window last read it — an
// agent, another window — is not written over with this window's older picture.
func TestApprovingAHunkKeepsWhatAnotherWriterSigned(t *testing.T) {
	m := modelFor(t, changedRepo(t, 60, map[int]string{4: "FOUR", 20: "TWENTY", 50: "FIFTY"}))
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = drive(t, m, letterKey("a"))

	// Somebody else signs off the last hunk, and this window has not looked.
	other := fileIn(t, m, "foo.txt").blocks[2]
	st := reviewJSON(t, m.src.(*git.Repo))
	st.SetHunks("foo.txt", "Test", append(st.VerdictOf("foo.txt", "Test").Hunks, other...))
	if err := m.src.(*git.Repo).WriteStateFile(review.FileName, string(st.Bytes())); err != nil {
		t.Fatal(err)
	}

	m = onLine(t, m, "foo.txt", '+', "TWENTY")
	m = drive(t, m, letterKey("a"))
	if !m.rv.Approved("foo.txt", "Test") {
		t.Errorf("the third hunk signed off elsewhere was lost: %v (status %q)",
			reviewJSON(t, m.src.(*git.Repo)).VerdictOf("foo.txt", "Test").Hunks, m.status)
	}
}

// The unchanged lines of a file are read from its new side, so a patch that
// has none to read — a link, a deletion, a file a link has replaced — is told
// apart by what git says about it and not by what the file is called.
func TestNoNewSide(t *testing.T) {
	for _, c := range []struct {
		name, preamble string
		want           bool
	}{
		{"an edit", "diff --git a/f b/f\nindex 1111111..2222222 100644\n--- a/f\n+++ b/f", false},
		{"a new file", "diff --git a/f b/f\nnew file mode 100644\nindex 0000000..2222222\n--- /dev/null\n+++ b/f", false},
		{"a file called like a mode", "diff --git a/mode 120000 b/mode 120000\nindex 1111111..2222222 100644\n--- a/mode 120000\n+++ b/mode 120000", false},
		{"a new link", "diff --git a/l b/l\nnew file mode 120000\nindex 0000000..2222222\n--- /dev/null\n+++ b/l", true},
		{"a link pointed elsewhere", "diff --git a/l b/l\nindex 1111111..2222222 120000\n--- a/l\n+++ b/l", true},
		{"a deleted file", "diff --git a/f b/f\ndeleted file mode 100644\nindex 1111111..0000000\n--- a/f\n+++ /dev/null", true},
	} {
		p := &git.Patch{Preamble: strings.Split(c.preamble, "\n")}
		if got := noNewSide(p); got != c.want {
			t.Errorf("%s: noNewSide is %v, want %v", c.name, got, c.want)
		}
	}
}

// A gap's lines come from the file as it is on disk, carriage returns and
// all, and none of those may reach the terminal.
func TestGapLinesLoseTheirCarriageReturns(t *testing.T) {
	f := &reviewFile{path: "f.txt", patch: git.ParsePatch("--- a/f.txt\n+++ b/f.txt\n@@ -3,1 +3,1 @@\n-c\r\n+C\r\n")}
	f.lines = f.patch.Render()
	f.texts = make([]string, len(f.lines))
	f.addGaps("a\r\nb\r\nC\r\nd\r\n")
	for g := range f.gaps {
		f.setGap(g, true)
	}
	n := 0
	for i, l := range f.lines {
		if _, ok := f.gapAt(i); !ok || l.Kind != ' ' {
			continue
		}
		n++
		if strings.Contains(l.Text, "\r") || strings.Contains(f.texts[i], "\r") {
			t.Errorf("gap line %q still ends in a carriage return", l.Text)
		}
	}
	if n != 3 {
		t.Errorf("the gaps let out %d lines, want 3", n)
	}
}

// Deciding about a hunk hands its fold back to the rule, but only once the
// decision is saved. One that could not be written leaves the hunk folded the
// way the reader had it, and says so rather than announcing an approval.
func TestAHunkApprovalThatIsNotSavedMovesNoFold(t *testing.T) {
	m := modelFor(t, changedRepo(t, 60, map[int]string{4: "FOUR", 50: "FIFTY"}))
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	f := fileIn(t, m, "foo.txt")
	k := m.runKeys(f)[0][0]
	m.hunks[k] = false

	// A review caught halfway through being written cannot be saved over.
	if err := m.src.(*git.Repo).WriteStateFile(review.FileName, `{"version":1,`); err != nil {
		t.Fatal(err)
	}
	m = drive(t, m, letterKey("a"))
	if !m.statusEr {
		t.Fatalf("status = %q, want it to say the approval was not saved", m.status)
	}
	if v, ok := m.hunks[k]; !ok || v {
		t.Errorf("the fold the reader set was not put back: hunks[%v] = %v, %v", k, v, ok)
	}
}

// t goes looking for the next open comment, and one inside a hunk the reader
// folded by hand is not a row until the hunk is opened. It opens.
func TestTheNextOpenCommentOpensTheHunkItIsIn(t *testing.T) {
	m := modelFor(t, twoHunkRepo(t))
	m = onLine(t, m, "foo.txt", '+', "THIRTY-SIX")
	m = comment(t, m, "why")
	m = onLine(t, m, "foo.txt", '+', "THIRTY-SIX")
	m = drive(t, m, letterKey(" "))
	if _, n := hunkRows(m, "foo.txt"); n > 11 {
		t.Fatalf("the hunk did not fold: %d lines showing", n)
	}
	m.view.moveTo(0)
	m = drive(t, m, letterKey("t"))
	if m.view.thread() == "" {
		t.Errorf("t walked past the comment in a folded hunk (status %q)", m.status)
	}
}

// Asking for a line by number is asking to see it, in a folded hunk as in a
// folded file.
func TestGoingToALineOpensTheHunkItIsIn(t *testing.T) {
	m := modelFor(t, twoHunkRepo(t))
	m = onLine(t, m, "foo.txt", '+', "THIRTY-SIX")
	m = drive(t, m, letterKey(" "))
	m = drive(t, m, letterKey("3"), letterKey("6"), letterKey("g"))
	if _, l, ok := m.view.line(); !ok || l.New != 36 || l.Kind != '+' {
		t.Errorf("36g landed on %+v, want the changed line 36 (status %q)", l, m.status)
	}
}

// More context can join a hunk somebody folded to one they have not read. The
// fold is of the one, and must not take the other off the screen with it.
func TestAFoldDoesNotSpreadToTheHunkItJoins(t *testing.T) {
	m := modelFor(t, twoHunkRepo(t))
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = drive(t, m, letterKey(" "))
	for i := 0; i < 12; i++ {
		m = drive(t, m, letterKey("="))
	}
	heads, code := hunkRows(m, "foo.txt")
	if heads != 1 {
		t.Fatalf("the hunks did not join: %d of them", heads)
	}
	if code == 0 {
		t.Errorf("the joined hunk is folded, hiding a change nobody folded:\n%s", plain(m))
	}

	// Folding the joined hunk is folding both, and stays so when they part.
	m = onLine(t, m, "foo.txt", '+', "THIRTY-SIX")
	m = drive(t, m, letterKey(" "))
	for i := 0; i < 12; i++ {
		m = drive(t, m, letterKey("-"))
	}
	if heads, code := hunkRows(m, "foo.txt"); heads != 2 || code != 0 {
		t.Errorf("after parting, %d hunks show %d lines, want 2 folded hunks", heads, code)
	}
}

// The bar says what a and Space will reach from where the cursor is: a hunk
// only inside one, the lines left out on a row that counts them, and the file
// from a row that is in neither.
func TestTheHintNamesWhatTheKeysReach(t *testing.T) {
	m := modelFor(t, twoHunkRepo(t))
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	if h := m.hints(); !strings.Contains(h, "approve the hunk") {
		t.Errorf("inside a hunk the hint is %q", h)
	}
	m = cursorTo(t, m, func(r reviewRow) bool {
		if r.kind != rowDiff || r.file < 0 {
			return false
		}
		l, ok := m.view.files[r.file].rowLine(r.line)
		return ok && l.Kind == 'g'
	})
	if h := m.hints(); strings.Contains(h, "hunk ·") && strings.Contains(h, "approve") || !strings.Contains(h, "these lines") {
		t.Errorf("on a row of unchanged lines the hint is %q", h)
	}
	// The blank row that ends an open file is in no hunk and no gap.
	m = cursorTo(t, m, func(r reviewRow) bool { return r.kind == rowGap })
	if h := m.hints(); !strings.Contains(h, "approve the file") {
		t.Errorf("on a row in no hunk the hint is %q", h)
	}
}

// An approved hunk whose change is edited away loses its approval when the
// window next looks, and does not get it back when the edit is undone — on
// screen and on disk both.
func TestAHunkApprovalFallsWithItsChangeAndStaysFallen(t *testing.T) {
	m := modelFor(t, twoHunkRepo(t))
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = drive(t, m, letterKey("a"))
	if n := len(reviewJSON(t, m.src.(*git.Repo)).VerdictOf("foo.txt", "Test").Hunks); n != 1 {
		t.Fatalf("%d hunks recorded, want 1", n)
	}
	is, err := os.ReadFile(filepath.Join(m.src.(*git.Repo).Dir, "foo.txt"))
	if err != nil {
		t.Fatal(err)
	}
	edited := strings.Replace(string(is), "FOUR\n", "FOUR AND MORE\n", 1)

	// A review that cannot be saved over keeps what it showed.
	writeFile(t, m.src.(*git.Repo).Dir, "foo.txt", edited)
	saved := string(reviewJSON(t, m.src.(*git.Repo)).Bytes())
	if err := m.src.(*git.Repo).WriteStateFile(review.FileName, `{"version":1,`); err != nil {
		t.Fatal(err)
	}
	m = drive(t, m, m.rescanCmd()())
	if len(m.rv.ApprovedHunks("foo.txt", "Test")) != 1 {
		t.Error("the approval left the screen though dropping it was never saved")
	}
	if err := m.src.(*git.Repo).WriteStateFile(review.FileName, saved); err != nil {
		t.Fatal(err)
	}

	m = drive(t, m, m.rescanCmd()())
	if f := reviewJSON(t, m.src.(*git.Repo)).VerdictOf("foo.txt", "Test"); f != nil && len(f.Hunks) != 0 {
		t.Fatalf("the approval of a change that is gone was kept: %v", f.Hunks)
	}
	if len(m.rv.ApprovedHunks("foo.txt", "Test")) != 0 {
		t.Error("the window still shows the hunk approved")
	}

	writeFile(t, m.src.(*git.Repo).Dir, "foo.txt", string(is))
	m = drive(t, m, m.rescanCmd()())
	if f := reviewJSON(t, m.src.(*git.Repo)).VerdictOf("foo.txt", "Test"); f != nil && len(f.Hunks) != 0 {
		t.Errorf("undoing the edit brought the approval back: %v", f.Hunks)
	}
}

// agentApproves has somebody other than the reader sign a file off, the way a
// command run beside the window does.
func agentApproves(t *testing.T, m Model, by string, paths ...string) Model {
	t.Helper()
	err := review.Update(m.src.(*git.Repo), m.branch, 0, func(st *review.State) error {
		for _, p := range paths {
			st.Approve(p, "", by)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return polled(t, m)
}

// polled is the window a tick later: whatever was written to the review has
// been noticed, and the reading of git that an approval is judged against —
// when there was one to judge — has come back.
func polled(t *testing.T, m Model) Model {
	t.Helper()
	if cmd := m.pickUpChanges(); cmd != nil {
		m = drive(t, m, cmd())
	}
	return m
}

// A window left open does not hear about an edit to the tree. An approval of
// the file as it now stands, given from a command line while the window still
// shows the file as it was, is judged against the tree and not the screen.
func TestAnApprovalIsNotJudgedByAStaleDiff(t *testing.T) {
	m := newTestModel(t)
	stale := fileIn(t, m, "foo.txt").digest

	// The author edits the file, and the reviewer approves what it now says.
	// Nothing rescans the window in between.
	writeFile(t, m.src.(*git.Repo).Dir, "foo.txt", "one\nTWO\nthree\nFOUR\nFIVE\n")
	scan, err := m.src.(*git.Repo).Scan("main", 0)
	if err != nil {
		t.Fatal(err)
	}
	now := ""
	for _, p := range git.ParseMulti(scan.Diff) {
		if p.Path == "foo.txt" {
			now = review.Digest(p)
		}
	}
	if now == "" || now == stale {
		t.Fatalf("the edit did not move the digest: %q, was %q", now, stale)
	}
	err = review.Update(m.src.(*git.Repo), m.branch, 0, func(st *review.State) error {
		st.Approve("foo.txt", now, "claude")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	m = polled(t, m)
	st := reviewJSON(t, m.src.(*git.Repo))
	if !st.Approved("foo.txt", "claude") || st.Nullified("foo.txt", "claude") {
		t.Errorf("an approval of the file as it stands was struck down by the diff on screen: %+v", st.VerdictOf("foo.txt", "claude"))
	}
	if got := fileIn(t, m, "foo.txt").digest; got != now {
		t.Errorf("the window is still showing the file as it was: digest %q, want %q", got, now)
	}

	// And one given with no digest is stamped with the tree's, not the screen's.
	writeFile(t, m.src.(*git.Repo).Dir, "foo.txt", "one\nTWO\nthree\nFOUR\nfive!\n")
	m = agentApproves(t, m, "reviewer", "foo.txt")
	if v := reviewJSON(t, m.src.(*git.Repo)).VerdictOf("foo.txt", "reviewer"); v == nil || !v.Approved || v.Digest != fileIn(t, m, "foo.txt").digest || v.Digest == now {
		t.Errorf("stamped %+v, want the digest of the file as it now stands", v)
	}
}

// A file an agent has approved is not one the reader has read. It says so in
// a column of its own, and everything the window does about an approved file —
// fold it, count it, hide it — waits for the reader's own.
func TestSomebodyElsesApprovalIsNotTheReaders(t *testing.T) {
	m := newTestModel(t)
	m = agentApproves(t, m, "claude", "beta.go", "foo.txt")

	f := fileIn(t, m, "beta.go")
	if f.approved || f.done() || f.verdict() != "○" || f.theirVerdict() != "✓" {
		t.Errorf("beta.go: approved %v, verdicts %q %q", f.approved, f.verdict(), f.theirVerdict())
	}
	if !strings.Contains(plain(m), "✓ by claude") {
		t.Errorf("the heading does not say whose approval it is:\n%s", plain(m))
	}
	if !strings.Contains(plain(m), "0/4 approved") || !strings.Contains(plain(m), "4 to read") {
		t.Errorf("the counts took the agent's approvals for the reader's:\n%s", plain(m))
	}

	// f hides what the reader is done with, and that is nothing yet.
	m = drive(t, m, letterKey("f"))
	if m.view.hidden != 0 || len(pathsOf(m)) != 4 {
		t.Fatalf("f hid %d files on an agent's word, leaving %v", m.view.hidden, pathsOf(m))
	}
	m = drive(t, m, letterKey("f"))

	// The reader's own approval goes in beside the agent's.
	m = onLine(t, m, "beta.go", '+', "package beta")
	m = drive(t, m, letterKey("a"))
	st := reviewJSON(t, m.src.(*git.Repo))
	if !st.Approved("beta.go", "Test") || !st.Approved("beta.go", "claude") {
		t.Fatalf("approvers = %v, want both", st.Approvers("beta.go"))
	}
	f = fileIn(t, m, "beta.go")
	if f.verdict() != "✓" || f.theirVerdict() != "✓" || !f.done() {
		t.Errorf("beta.go: verdicts %q %q, done %v", f.verdict(), f.theirVerdict(), f.done())
	}
	m = drive(t, m, letterKey("f"))
	if got := pathsOf(m); m.view.hidden != 1 || slices.Contains(got, "beta.go") || !slices.Contains(got, "foo.txt") {
		t.Errorf("hidden %d, showing %v: want beta.go gone and foo.txt, which only the agent approved, still here", m.view.hidden, got)
	}
	m = drive(t, m, letterKey("f"))

	// And taking it back takes back the reader's alone.
	m = onLine(t, m, "beta.go", '+', "package beta")
	m = drive(t, m, letterKey("a"))
	st = reviewJSON(t, m.src.(*git.Repo))
	if st.Approved("beta.go", "Test") || !st.Approved("beta.go", "claude") {
		t.Errorf("approvers = %v, want the agent's left standing", st.Approvers("beta.go"))
	}
}

// Approving everything is the reader approving everything they have not: a
// file somebody else has been through is still one of them.
func TestApprovingEverythingIsTheReadersOwn(t *testing.T) {
	m := newTestModel(t)
	m = agentApproves(t, m, "claude", "beta.go")
	m = drive(t, m, letterKey("A"), letterKey("y"))
	st := reviewJSON(t, m.src.(*git.Repo))
	for _, p := range pathsOf(m) {
		if !st.Approved(p, "Test") {
			t.Errorf("%s was left out: %v", p, st.Approvers(p))
		}
	}
	if !st.Approved("beta.go", "claude") {
		t.Error("the agent's approval was replaced rather than joined")
	}
}

// An approval somebody else gave falls with the file like any other, and the
// second column says that it did.
func TestSomebodyElsesApprovalFallsToo(t *testing.T) {
	m := newTestModel(t)
	m = agentApproves(t, m, "claude", "foo.txt")
	m = drive(t, m, m.rescanCmd()())
	writeFile(t, m.src.(*git.Repo).Dir, "foo.txt", "one\nTWO\nthree\nFOUR\nFIVE\n")
	m = drive(t, m, m.rescanCmd()())

	f := fileIn(t, m, "foo.txt")
	if f.theirVerdict() != "◈" || f.nullified || f.verdict() != "○" {
		t.Errorf("verdicts %q %q, nullified %v", f.verdict(), f.theirVerdict(), f.nullified)
	}
	if !strings.Contains(plain(m), "◈ by claude") {
		t.Errorf("the heading does not say whose approval fell:\n%s", plain(m))
	}
}

// A verdict written by hand can have no name on it. The heading says so
// rather than leaving a gap where the name goes.
func TestAVerdictWithNoNameIsNotDrawnAsOne(t *testing.T) {
	m := newTestModel(t)
	m = agentApproves(t, m, "", "beta.go")
	m = agentApproves(t, m, "claude", "beta.go")
	if got := plain(m); !strings.Contains(got, "✓ by nobody named, claude") || strings.Contains(got, "by ,") {
		t.Errorf("the heading reads:\n%s", got)
	}
}

// A review from before a hunk's sign-off carried a name: the window that
// opens it takes those hunks as its reader's, so what was read stays read.
func TestTheWindowClaimsHunksSignedOffUnderNoName(t *testing.T) {
	repo := changedRepo(t, 60, map[int]string{4: "FOUR", 50: "FIFTY"})
	blocks := fileIn(t, modelFor(t, repo), "foo.txt").blocks
	if err := repo.WriteStateFile(review.FileName,
		`{"version": 1, "files": [{"path": "foo.txt", "approved": false, "hunks": ["`+blocks[0][0]+`"]}]}`); err != nil {
		t.Fatal(err)
	}

	m := modelFor(t, repo)
	if n, of := fileIn(t, m, "foo.txt").hunksApproved(); n != 1 || of != 2 {
		t.Errorf("%d of %d hunks signed off, want the one that was", n, of)
	}
	st := reviewJSON(t, repo)
	if len(st.ApprovedHunks("foo.txt", "Test")) != 1 || st.VerdictOf("foo.txt", "") != nil {
		t.Errorf("the review was left as %s", st.Bytes())
	}
}

// What t says f has hidden is what f hid: the open comments on the files it
// took away, counted as it takes them. Reading one commit puts other files off
// the screen too, and none of that is f's doing.
func TestOnlyWhatFHidIsBlamedOnF(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "bar.txt", '+', "beta")
	m = comment(t, m, "hold on")
	m = drive(t, m, letterKey("f"))
	if m.view.hiddenOpen != 0 {
		t.Fatalf("hiddenOpen = %d with nothing approved", m.view.hiddenOpen)
	}
	m = selectCommit(t, m, "add beta")
	m = drive(t, m, letterKey("t"), letterKey("t"))
	if m.view.hiddenOpen != 0 || strings.Contains(m.status, "hidden") {
		t.Errorf("t said %q, and hiddenOpen = %d, of a comment on a file nobody approved", m.status, m.view.hiddenOpen)
	}

	// Approved, the file goes and its comment is counted as gone with it.
	m = selectCommit(t, m, "capitalise two")
	m.history.moveTo(0)
	m = runCmd(t, m, m.selectedCommitCmd())
	m = drive(t, m, letterKey("f"))
	m = onLine(t, m, "bar.txt", '+', "beta")
	m = drive(t, m, letterKey("a"), letterKey("f"))
	if m.view.hiddenOpen != 1 {
		t.Errorf("hiddenOpen = %d, want the one comment on the file f hid", m.view.hiddenOpen)
	}
}
