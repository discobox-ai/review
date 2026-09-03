package ui

import (
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/discobox-ai/difftui/internal/git"
	"github.com/discobox-ai/difftui/internal/review"
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

func modelFor(t *testing.T, repo *git.Repo) Model {
	t.Helper()
	m := New(repo, "main")
	return drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40}, m.rescanCmd()())
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
func onLine(t *testing.T, m Model, path string, kind byte, text string) Model {
	t.Helper()
	return cursorTo(t, m, func(r reviewRow) bool {
		if r.kind != rowDiff || r.file < 0 {
			return false
		}
		f := m.view.files[r.file]
		if f.path != path || r.line < 0 || r.line >= len(f.lines) {
			return false
		}
		l := f.lines[r.line]
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
	for _, want := range []string{"Reviewing:", "feature", "Changed Files", "Review"} {
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

// The list on the left is a contents page: moving in one moves the other.
func TestTheListAndTheStackStayInStep(t *testing.T) {
	m := newTestModel(t)
	m.focus = focusFiles
	m.files.moveTo(2) // foo.txt
	m = drive(t, m, letterKey(" "))

	if m.focus != focusReview {
		t.Fatalf("Space in the list left the focus at %v", m.focus)
	}
	if got := m.view.pathAt(); got != "foo.txt" {
		t.Errorf("the stack is showing %q, want foo.txt", got)
	}
	if m.view.rows[m.view.offset].kind != rowFile {
		t.Error("the file's heading is not at the top of the pane")
	}

	// And reading down the stack brings the list along.
	m = drive(t, m, letterKey("]"))
	if got := m.files.currentPath(); got != m.view.pathAt() {
		t.Errorf("list is on %q while the review is on %q", got, m.view.pathAt())
	}
}

// ---------------------------------------------------------------------------
// Approving
// ---------------------------------------------------------------------------

func TestApproveWritesTheReviewFile(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "beta.go", '+', "package beta")
	m = drive(t, m, letterKey("a"))

	st := reviewJSON(t, m.repo)
	if !st.Approved("beta.go") {
		t.Fatalf("beta.go was not approved: %s", st.Bytes())
	}
	f := st.FileFor("beta.go")
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
	st = reviewJSON(t, m.repo)
	if st.Approved("beta.go") {
		t.Error("a second press did not withdraw the approval")
	}
	if st.Nullified("beta.go") {
		t.Error("withdrawing an approval marked the file as having changed")
	}
}

// An approval is of a change. Editing the file afterwards nullifies it, and
// the file comes back marked as having been approved once rather than never.
func TestApprovalIsNullifiedWhenTheFileChanges(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = drive(t, m, letterKey("a"))
	if !reviewJSON(t, m.repo).Approved("foo.txt") {
		t.Fatal("foo.txt was not approved to begin with")
	}

	writeFile(t, m.repo.Dir, "foo.txt", "one\nTWO\nthree\nFOUR\nFIVE\n")
	m = drive(t, m, m.rescanCmd()())

	st := reviewJSON(t, m.repo)
	if st.Approved("foo.txt") {
		t.Error("the approval survived the file changing")
	}
	if !st.Nullified("foo.txt") {
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
	if !reviewJSON(t, m.repo).Approved("foo.txt") {
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
	if reviewJSON2(m.repo) != nil && reviewJSON2(m.repo).Open() != 0 {
		t.Error("something was written before the question was answered")
	}
	m = drive(t, m, letterKey("y"))
	st := reviewJSON(t, m.repo)
	for _, p := range pathsOf(m) {
		if !st.Approved(p) {
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

	st := reviewJSON(t, m.repo)
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

	th := reviewJSON(t, m.repo).Threads[0]
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
	id := reviewJSON(t, m.repo).Threads[0].ID

	m = cursorTo(t, m, func(r reviewRow) bool { return r.thread == id && r.first })
	m = drive(t, m, letterKey("r"))
	if m.compose == nil || m.compose.thread != id {
		t.Fatalf("r did not open a reply to %s (compose=%+v)", id, m.compose)
	}
	m.editor.SetValue("because it is a shout")
	m = drive(t, m, ctrlKey("j"))

	th := reviewJSON(t, m.repo).Thread(id)
	if len(th.Comments) != 2 || th.Comments[1].Body != "because it is a shout" {
		t.Fatalf("comments = %+v", th.Comments)
	}

	// Resolving closes it, and it collapses to one line.
	m = cursorTo(t, m, func(r reviewRow) bool { return r.thread == id && r.first })
	m = drive(t, m, letterKey("x"))
	if !reviewJSON(t, m.repo).Thread(id).Resolved {
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
	if !reviewJSON(t, m.repo).Thread(id).Resolved {
		t.Error("unfolding a resolved thread reopened it")
	}
	if !strings.Contains(plain(m), "because it is a shout") {
		t.Error("unfolding did not show the comments")
	}

	// And x again reopens it.
	m = cursorTo(t, m, func(r reviewRow) bool { return r.thread == id && r.first })
	m = drive(t, m, letterKey("x"))
	if reviewJSON(t, m.repo).Thread(id).Resolved {
		t.Error("x did not reopen the thread")
	}
}

func TestDeletingAThreadAsksFirst(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "never mind")
	id := reviewJSON(t, m.repo).Threads[0].ID

	m = cursorTo(t, m, func(r reviewRow) bool { return r.thread == id && r.first })
	m = drive(t, m, letterKey("X"))
	if m.dialog == nil {
		t.Fatal("X deleted the thread without asking")
	}
	if reviewJSON(t, m.repo).Thread(id) == nil {
		t.Fatal("the thread went before the question was answered")
	}
	m = drive(t, m, letterKey("y"))
	if reviewJSON(t, m.repo).Thread(id) != nil {
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
	for _, th := range reviewJSON(t, m.repo).Threads {
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
	writeFile(t, m.repo.Dir, "foo.txt", "zero a\nzero b\none\nTWO\nthree\nFOUR\nfive\n")
	m = drive(t, m, m.rescanCmd()())

	m = selectCommit(t, m, "capitalise two")
	m = onLine(t, m, "foo.txt", '+', "TWO")
	if got := m.view.files[0].lines[m.view.rows[m.view.cursor].line].New; got != 2 {
		t.Fatalf("in the commit, TWO is line %d, want 2", got)
	}
	m = comment(t, m, "shouting again")

	th := reviewJSON(t, m.repo).Threads[0]
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
	for idx, threads := range f.threads {
		for range threads {
			if f.lines[idx].New == 4 {
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

	if err := os.Remove(filepath.Join(m.repo.Dir, "new.txt")); err != nil {
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

// Something else writes the review; difftui picks it up.
func TestAnAgentsEditIsPickedUp(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "why shout?")
	id := reviewJSON(t, m.repo).Threads[0].ID

	// An agent answers and closes the thread, and approves a file.
	st := reviewJSON(t, m.repo)
	st.Reply(id, "claude", "toned it down")
	st.SetResolved(id, true)
	st.Approve("beta.go", "", "claude")
	if err := m.repo.WriteStateFile(review.FileName, string(st.Bytes())); err != nil {
		t.Fatal(err)
	}

	m.pickUpChanges()
	if !strings.Contains(plain(m), "resolved") {
		t.Error("the answer did not reach the screen")
	}
	if !fileIn(t, m, "beta.go").approved {
		t.Error("the agent's approval did not reach the screen")
	}
	// An approval written without a digest is stamped with the current one, so
	// that it can be nullified like any other.
	if got := reviewJSON(t, m.repo).FileFor("beta.go").Digest; got == "" {
		t.Error("the hand written approval was not stamped with a digest")
	}
}

// A file caught halfway through being written leaves what is on screen alone
// and says so, rather than wiping the review.
func TestAHalfWrittenReviewIsNotTakenSeriously(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "still here")

	if err := m.repo.WriteStateFile(review.FileName, `{"version":1,`); err != nil {
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

// difftui re-reads the file before every write of its own, so a comment an
// agent added a moment ago is not quietly dropped by the next approval.
func TestAConcurrentEditIsNotClobbered(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "mine")

	// Somebody else appends a thread without difftui noticing.
	st := reviewJSON(t, m.repo)
	st.Start(&review.Thread{Path: "bar.txt", Side: review.SideNew, Line: 1}, "claude", "theirs")
	if err := m.repo.WriteStateFile(review.FileName, string(st.Bytes())); err != nil {
		t.Fatal(err)
	}

	m = onLine(t, m, "beta.go", '+', "package beta")
	m = drive(t, m, letterKey("a"))

	back := reviewJSON(t, m.repo)
	if len(back.Threads) != 2 {
		t.Fatalf("threads = %+v, want both", back.Threads)
	}
	if !back.Approved("beta.go") {
		t.Error("the approval was lost to the re-read")
	}
}

// The review is of a branch, so checking out another one puts this one away
// and brings that one back.
func TestEachBranchKeepsItsOwnReview(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "on the feature branch")

	gitRun(t, m.repo.Dir, "add", "-A")
	gitRun(t, m.repo.Dir, "commit", "-qm", "finish the work")
	gitRun(t, m.repo.Dir, "checkout", "-q", "main")
	m.baseRef, m.basePinned = "", false
	m = drive(t, m, m.rescanCmd()())
	if len(m.rv.Threads) != 0 {
		t.Errorf("main inherited feature's comments: %+v", m.rv.Threads)
	}

	gitRun(t, m.repo.Dir, "checkout", "-q", "feature")
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
	if got := reviewJSON(t, m.repo).Base; got != "HEAD~1" {
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
	for _, f := range []focusArea{focusFiles, focusReview, focusHistory} {
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
		"1": focusFiles, "2": focusReview, "3": focusHistory,
	} {
		m = drive(t, m, altKey(key))
		if m.focus != want {
			t.Errorf("Alt-%s went to %v, want %v", key, m.focus, want)
		}
	}
}

func TestLeftFromTheReviewGoesToTheList(t *testing.T) {
	m := newTestModel(t)
	m.focus = focusReview
	m.view.hoffset = 0
	m = drive(t, m, namedKey(tea.KeyLeft))
	if m.focus != focusFiles {
		t.Errorf("left went to %v, want the file list", m.focus)
	}
}

// Under a hundred columns the left column gives up its space while the review
// has the focus, because a review read in a third of a narrow screen is not
// worth reading.
func TestNarrowWindowCollapsesTheLeftColumn(t *testing.T) {
	m := drive(t, newTestModel(t), tea.WindowSizeMsg{Width: 80, Height: 30})
	m.focus = focusFiles
	m.layout()
	if m.leftCollapsed() {
		t.Error("the left column folded away while it had the focus")
	}
	m = drive(t, m, altKey("2"))
	if !m.leftCollapsed() {
		t.Fatal("the left column did not fold away")
	}
	if strings.Contains(plain(m), "Changed Files") {
		t.Error("the list is still drawn")
	}
	m = drive(t, m, namedKey(tea.KeyLeft))
	if !strings.Contains(plain(m), "Changed Files") {
		t.Error("left did not bring the list back")
	}
}

func TestWideWindowKeepsBothColumns(t *testing.T) {
	m := drive(t, newTestModel(t), tea.WindowSizeMsg{Width: 140, Height: 30}, altKey("2"))
	if m.leftCollapsed() {
		t.Error("a wide window folded the left column away")
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
	help := helpText()
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
	writeFile(t, m.repo.Dir, "doc.md", "# Title\n\nSome prose.\n\nAnd more.\n")
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
	m := New(repo, "")
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

	body, ok := m.repo.ReadStateFile(review.FileName)
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
	th := reviewJSON(t, m.repo).Threads[0]
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
	for idx, threads := range f.threads {
		for range threads {
			if f.lines[idx].New != 5 {
				t.Errorf("the block comment hangs off line %d, want 5", f.lines[idx].New)
			}
		}
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
	writeFile(t, m.repo.Dir, "long.txt", numberedLines(1, 40))
	gitRun(t, m.repo.Dir, "add", "long.txt")
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
	writeFile(t, m.repo.Dir, "long.txt", numberedLines(1, 40))
	gitRun(t, m.repo.Dir, "add", "long.txt")
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
	anchor := m.view.cursor

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
	if m.view.height != max(m.height-2-1, 1) {
		t.Errorf("the review gave up %d rows to the composer",
			max(m.height-2-1, 1)-m.view.height)
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
	id := reviewJSON(t, m.repo).Threads[0].ID
	if got := m.view.thread(); got != id {
		t.Errorf("cursor is on thread %q, want %q", got, id)
	}
	// So the very next key can answer it.
	m = drive(t, m, letterKey("x"))
	if !reviewJSON(t, m.repo).Thread(id).Resolved {
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
	end := m.view.cursor
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
	id := reviewJSON(t, m.repo).Threads[0].ID

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

		st := reviewJSON(t, m.repo)
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
	if reviewJSON2(m.repo) != nil && len(reviewJSON2(m.repo).Threads) != 0 {
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
	for _, th := range reviewJSON(t, m.repo).Threads {
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
// Hiding the files that are done with
// ---------------------------------------------------------------------------

// f takes the finished files off the screen: what is left is what is still
// unapproved or still being argued about.
func TestHidingTheFilesThatAreDoneWith(t *testing.T) {
	m := newTestModel(t)
	// bar.txt: approved, but somebody is still waiting for an answer on it.
	m = onLine(t, m, "bar.txt", '+', "beta")
	m = comment(t, m, "hold on")
	m = onLine(t, m, "bar.txt", '+', "beta")
	m = drive(t, m, letterKey("a"))
	// beta.go: approved and quiet.
	m = onLine(t, m, "beta.go", '+', "package beta")
	m = drive(t, m, letterKey("a"))

	m = drive(t, m, letterKey("f"))
	got := pathsOf(m)
	want := []string{"bar.txt", "foo.txt", "new.txt"}
	if len(got) != len(want) {
		t.Fatalf("showing %v, want %v — an approved file with an open comment stays", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("file %d = %q, want %q", i, got[i], want[i])
		}
	}
	if m.view.hidden != 1 {
		t.Errorf("hidden = %d, want 1", m.view.hidden)
	}
	if !strings.Contains(m.status, "1 finished file") {
		t.Errorf("status = %q", m.status)
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
	if !strings.Contains(view, "Unfinished Files") {
		t.Error("the list does not say it is showing only some of them")
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
	writeFile(t, m.repo.Dir, "long.txt", numberedLines(1, 60))
	gitRun(t, m.repo.Dir, "add", "long.txt")
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
	if reviewJSON2(m.repo) != nil && len(reviewJSON2(m.repo).Files) != 0 {
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

// s stands the two sides of a change beside each other: what a line was on the
// left, what it became on the right.
func TestSplitPutsTheTwoSidesSideBySide(t *testing.T) {
	m := newTestModel(t)
	if m.view.splitting() {
		t.Fatal("the review starts split")
	}
	unified := len(m.view.rows)

	m = drive(t, m, letterKey("s"))
	if !m.view.splitting() {
		t.Fatalf("s did not split (status %q, width %d)", m.status, m.view.width)
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

	// And s again puts it back.
	m = drive(t, m, letterKey("s"))
	if m.view.splitting() || len(m.view.rows) != unified {
		t.Errorf("s did not unify: splitting=%v rows=%d want %d",
			m.view.splitting(), len(m.view.rows), unified)
	}
}

// A pure insertion has nothing on the left of it, and a hunk header belongs to
// neither side, so it runs the whole width.
func TestSplitLeavesTheMissingSideEmpty(t *testing.T) {
	m := drive(t, newTestModel(t), letterKey("s"))
	if !m.view.splitting() {
		t.Fatal("not split")
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

// A comment still hangs off the row that draws its line, in either view, and
// survives being switched between them.
func TestCommentsSurviveTheSplit(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "why shout?")

	m = drive(t, m, letterKey("s"))
	if !strings.Contains(plain(m), "why shout?") {
		t.Errorf("the comment is missing from the split view:\n%s", plain(m))
	}
	f := fileIn(t, m, "foo.txt")
	placed := 0
	for row, threads := range f.threads {
		placed += len(threads)
		// It hangs off the row that draws line 4 of the new side.
		if l, ok := f.rowLine(row); !ok || l.New != 4 {
			t.Errorf("the comment hangs off %+v, want the row for new line 4", l)
		}
	}
	if placed != 1 {
		t.Errorf("%d comments placed, want 1", placed)
	}

	// A comment written while split lands in the same place as one written
	// unified: the review does not know which way it was being read.
	m = onLine(t, m, "foo.txt", '+', "TWO")
	m = comment(t, m, "and this")
	th := reviewJSON(t, m.repo).Threads[1]
	if th.Side != review.SideNew || th.Line != 2 {
		t.Errorf("anchored at %s:%d, want new:2", th.Side, th.Line)
	}
}

// A pane too narrow to split honours the request without acting on it: it
// says so, and splits itself as soon as there is room.
func TestSplitWaitsForTheRoom(t *testing.T) {
	m := drive(t, newTestModel(t), tea.WindowSizeMsg{Width: 70, Height: 30})
	m = drive(t, m, altKey("2"), letterKey("s"))
	if m.view.width >= minSplitWidth {
		t.Skipf("the review pane is %d columns, wide enough to split", m.view.width)
	}
	if !m.view.split {
		t.Error("the request was forgotten")
	}
	if m.view.splitting() {
		t.Error("it split a pane too narrow for it")
	}
	if !strings.Contains(m.status, "columns") {
		t.Errorf("status = %q, want it to say why", m.status)
	}

	m = drive(t, m, tea.WindowSizeMsg{Width: 200, Height: 30})
	if !m.view.splitting() {
		t.Error("it did not split once there was room")
	}
}

// A large review still has its colours.
//
// Highlighting used to be governed by a budget over the review as a whole, and
// difftui's own source went past it: every file was drawn plain, and nothing
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
	id := reviewJSON(t, m.repo).Threads[0].ID
	m = cursorTo(t, m, func(r reviewRow) bool { return r.thread == id && r.first })
	m = drive(t, m, letterKey("r"))
	m.editor.SetValue("because it is a shout")
	m = drive(t, m, ctrlKey("s"))
	return m, id
}

// X on one remark of a conversation takes that remark and leaves the rest.
func TestDeletingOneComment(t *testing.T) {
	m, id := twoComments(t)
	if got := len(reviewJSON(t, m.repo).Thread(id).Comments); got != 2 {
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
	if len(reviewJSON(t, m.repo).Thread(id).Comments) != 2 {
		t.Fatal("it went before the question was answered")
	}
	m = drive(t, m, letterKey("y"))

	th := reviewJSON(t, m.repo).Thread(id)
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

	th := reviewJSON(t, m.repo).Thread(id)
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
	if reviewJSON(t, m.repo).Thread(id) != nil {
		t.Error("the conversation survived")
	}
}

// Taking the last remark out takes the conversation with it: an anchor on a
// line with nothing to say is not a thing to leave behind.
func TestDeletingTheLastCommentTakesTheThread(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = comment(t, m, "never mind")
	id := reviewJSON(t, m.repo).Threads[0].ID

	m = cursorTo(t, m, func(r reviewRow) bool { return r.thread == id && r.comment == 0 })
	m = drive(t, m, letterKey("X"))
	// With one comment on it, the question is about the conversation.
	if !strings.Contains(m.dialog.body, "Resolving it instead") {
		t.Errorf("question = %q, want the whole-conversation one", m.dialog.body)
	}
	m = drive(t, m, letterKey("y"))
	if len(reviewJSON(t, m.repo).Threads) != 0 {
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
	if got := reviewJSON(t, m.repo).Base; got != hash {
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
