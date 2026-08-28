package ui

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
	"github.com/discobox-ai/difftui/internal/git"
)

func testRepo(t *testing.T) *git.Repo {
	t.Helper()
	r, err := git.Open(testRepoDir(t))
	if err != nil {
		t.Fatal(err)
	}
	return r
}

// testRepoDir builds the working tree the tests run against and returns its
// path, for the few that want to open it through a System of their own.
func testRepoDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.name", "Test"},
		{"config", "user.email", "test@example.com"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	writeFile(t, dir, "foo.txt", "one\ntwo\nthree\nfour\nfive\n")
	writeFile(t, dir, "bar.txt", "alpha\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "initial")

	// One modified file, one staged file and one untracked file so every
	// pane has something in it.
	writeFile(t, dir, "foo.txt", "one\nTWO\nthree\nfour\nfive\n")
	writeFile(t, dir, "bar.txt", "beta\n")
	gitRun(t, dir, "add", "bar.txt")
	writeFile(t, dir, "new.txt", "brand new\n")

	return dir
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

// drive feeds a message to the model and runs whatever command comes back,
// looping until there is nothing left to do. It is a miniature bubbletea
// runtime, enough to test the model without a terminal.
func drive(t *testing.T, m Model, msgs ...tea.Msg) Model {
	t.Helper()
	queue := append([]tea.Msg{}, msgs...)
	var cmds []tea.Cmd
	for i := 0; i < 200 && (len(queue) > 0 || len(cmds) > 0); i++ {
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
		next, cmd := m.Update(msg)
		m = next.(Model)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return m
}

func TestViewRenders(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)

	view := m.View()
	if strings.TrimSpace(view) == "" {
		t.Fatal("view is empty")
	}
	for _, want := range []string{
		"Current Branch:", "main",
		"Unstaged Changes", "Staged Changes (Will Commit)",
		"foo.txt", "bar.txt", "new.txt",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("view is missing %q", want)
		}
	}

	lines := strings.Split(view, "\n")
	if len(lines) != 40 {
		t.Errorf("view has %d lines, want 40", len(lines))
	}
	if t.Failed() || testing.Verbose() {
		t.Logf("\n%s", view)
	}
}

// The branch bar names the remote branch this one is based on, where the two
// last met, and how far each has moved since.
func TestBranchBarShowsTheMergeBase(t *testing.T) {
	dir := testRepoDir(t)
	bare := t.TempDir()
	gitRun(t, bare, "init", "-q", "--bare", "-b", "main", ".")
	gitRun(t, dir, "remote", "add", "origin", bare)
	gitRun(t, dir, "push", "-q", "origin", "main")
	gitRun(t, dir, "remote", "set-head", "origin", "main")
	// One commit past what origin has, so the counts are not both zero.
	gitRun(t, dir, "commit", "-qm", "ahead")

	repo, err := git.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	if m.base.Ref != "origin/main" {
		t.Fatalf("base = %+v, want origin/main", m.base)
	}
	bar := m.viewBranchBar()
	for _, want := range []string{"base: ", "origin/main", m.base.Commit, "↑1", "↓0"} {
		if !strings.Contains(bar, want) {
			t.Errorf("branch bar is missing %q:\n%s", want, bar)
		}
	}
}

// With no remote there is nothing to be based on, and the bar says nothing
// rather than showing an empty base.
func TestBranchBarWithoutARemote(t *testing.T) {
	m := newTestModel(t)
	if m.base.Ref != "" {
		t.Fatalf("base = %+v, want none", m.base)
	}
	if bar := m.viewBranchBar(); strings.Contains(bar, "base:") {
		t.Errorf("branch bar names a base with no remote:\n%s", bar)
	}
}

// A narrow terminal drops the base rather than truncating the branch name.
func TestNarrowBranchBarDropsTheBase(t *testing.T) {
	m := newTestModel(t)
	m.base = git.Base{Ref: "origin/main", Commit: "abc1234", Ahead: 3, Behind: 1}
	if bar := m.viewBranchBar(); !strings.Contains(bar, "origin/main") {
		t.Fatalf("the base should fit at %d columns:\n%s", m.width, bar)
	}
	m.width = 52
	bar := m.viewBranchBar()
	if strings.Contains(bar, "base:") {
		t.Errorf("the base survived at 52 columns:\n%s", bar)
	}
	if !strings.Contains(bar, m.branch) {
		t.Errorf("the branch name was dropped instead:\n%s", bar)
	}
}

// aheadModel builds a repository with a base and n commits on top of it, plus
// the fixture's uncommitted work, and drives a model to its first scan.
func aheadModel(t *testing.T, n int) Model {
	t.Helper()
	m, _ := aheadModelWith(t, n, git.Local{})
	return m
}

// aheadModelWith is aheadModel on a chosen System, for tests that need to see
// what the model asked of the machine.
func aheadModelWith(t *testing.T, n int, sys git.System) (Model, *git.Repo) {
	t.Helper()
	dir := testRepoDir(t)
	bare := t.TempDir()
	gitRun(t, bare, "init", "-q", "--bare", "-b", "main", ".")
	gitRun(t, dir, "stash", "-q", "-u")
	gitRun(t, dir, "remote", "add", "origin", bare)
	gitRun(t, dir, "push", "-q", "origin", "main")
	gitRun(t, dir, "remote", "set-head", "origin", "main")
	for i := 0; i < n; i++ {
		writeFile(t, dir, "ahead.txt", strings.Repeat("x\n", i+1))
		gitRun(t, dir, "add", "ahead.txt")
		gitRun(t, dir, "commit", "-qm", "commit number "+itoa(i+1))
	}
	gitRun(t, dir, "stash", "pop", "-q")

	repo, err := git.OpenWith(sys, dir)
	if err != nil {
		t.Fatal(err)
	}
	return drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	), repo
}

// The branch history is a third pane on the left, and only when the branch has
// commits of its own.
func TestBranchHistoryAppearsWhenAhead(t *testing.T) {
	m := aheadModel(t, 2)
	if !m.showCommits() {
		t.Fatal("showCommits() is false with two commits ahead")
	}
	if len(m.commits.commits) != 2 {
		t.Fatalf("history holds %d commits, want 2", len(m.commits.commits))
	}
	view := m.View()
	for _, want := range []string{"This Branch", "working tree", "commit number 2", "commit number 1"} {
		if !strings.Contains(view, want) {
			t.Errorf("the history pane is missing %q:\n%s", want, view)
		}
	}
	// Newest first, under the working tree row.
	if m.commits.commits[0].Subject != "commit number 2" {
		t.Errorf("commits[0] = %q, want the newest", m.commits.commits[0].Subject)
	}

	// A branch level with its base has no pane.
	level := newTestModel(t)
	if level.showCommits() {
		t.Error("a branch with no base and no commits still showed the pane")
	}
	if strings.Contains(level.View(), "This Branch") {
		t.Errorf("the history pane is on screen with nothing to show:\n%s", level.View())
	}
}

// The cursor starts on the working tree, and the two working lists are what is
// on screen until a commit is picked.
func TestHistoryStartsOnTheWorkingTree(t *testing.T) {
	m := aheadModel(t, 2)
	if m.browsing() {
		t.Fatal("the history should start on the working tree")
	}
	view := m.View()
	if !strings.Contains(view, "Unstaged Changes") || !strings.Contains(view, "Staged Changes") {
		t.Errorf("the working lists should be on screen:\n%s", view)
	}
	if strings.Contains(view, "Files in ") {
		t.Errorf("a commit's files are on screen already:\n%s", view)
	}
}

// Selecting a commit swaps the two working lists for its files, and the diff
// follows to the file under the cursor.
func TestSelectingACommitSwapsTheLists(t *testing.T) {
	m := aheadModel(t, 2)
	m = drive(t, m, altKey("5"), tea.KeyMsg{Type: tea.KeyDown})

	c, ok := m.commits.selected()
	if !ok {
		t.Fatal("down from the working tree did not reach a commit")
	}
	if c.Subject != "commit number 2" {
		t.Errorf("selected %q, want the newest commit", c.Subject)
	}
	if !m.browsing() {
		t.Fatal("browsing() is false with a commit selected")
	}

	view := m.View()
	if !strings.Contains(view, "Files in "+c.Short) {
		t.Errorf("the commit's files are not on screen:\n%s", view)
	}
	if strings.Contains(view, "Unstaged Changes") || strings.Contains(view, "Staged Changes") {
		t.Errorf("the working lists survived the swap:\n%s", view)
	}
	if len(m.history.files) != 1 || m.history.files[0].Path != "ahead.txt" {
		t.Errorf("the commit's files = %+v, want ahead.txt", m.history.files)
	}

	// The diff is that commit's own change to the file, keyed to the commit.
	if m.diff.shown.commit != c.Hash {
		t.Errorf("diff commit = %q, want %q", m.diff.shown.commit, c.Hash)
	}
	if m.diff.side != git.SideCommit {
		t.Errorf("diff side = %v, want SideCommit", m.diff.side)
	}
	status, path := m.diff.headerText()
	if path != "ahead.txt" || !strings.Contains(status, c.Short) {
		t.Errorf("diff header = %q / %q, want it to name the commit", status, path)
	}

	// And picking the working tree again brings the lists back.
	m = drive(t, m, altKey("5"), tea.KeyMsg{Type: tea.KeyUp})
	if m.browsing() {
		t.Fatal("up from the first commit did not reach the working tree")
	}
	if !strings.Contains(m.View(), "Unstaged Changes") {
		t.Errorf("the working lists did not come back:\n%s", m.View())
	}
}

// History is read only: nothing that changes the tree or the index is reachable
// while an earlier commit is on screen.
func TestHistoryIsReadOnly(t *testing.T) {
	base := drive(t, aheadModel(t, 1), altKey("5"), tea.KeyMsg{Type: tea.KeyDown})
	if !base.browsing() {
		t.Fatal("not browsing")
	}
	before, err := base.repo.Git("status", "--porcelain")
	if err != nil {
		t.Fatal(err)
	}
	// U is not here: it pops commits, which is the one change history allows.
	// TestPopMeansTheSameThingEverywhere covers it.
	// e is not here either: it opens the commit's version of the file as a
	// read-only copy, which changes nothing. TestEditAgainstHistory covers it.
	for _, k := range []tea.KeyMsg{
		rkey("s"), rkey("u"), rkey("v"), rkey("D"),
		rkey("L"), rkey("r"), {Type: tea.KeySpace},
	} {
		m := drive(t, base, k)
		if !m.statusEr {
			t.Errorf("%v was allowed against history", k)
		}
		if m.dialog != nil {
			t.Errorf("%v opened the %q dialog against history", k, m.dialog.title)
		}
	}
	after, err := base.repo.Git("status", "--porcelain")
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("the working tree moved:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// Right chains down the column: history, then the commit's files, then the
// diff. It is the only arrow route into the files pane, since leaving the
// history upwards means passing its top row and so dropping the commit.
func TestRightChainsFromHistoryToTheDiff(t *testing.T) {
	m := drive(t, aheadModel(t, 1), altKey("5"), tea.KeyMsg{Type: tea.KeyDown})
	if m.focus != focusCommits {
		t.Fatalf("focus = %v, want focusCommits", m.focus)
	}
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyRight})
	if m.focus != focusHistory {
		t.Fatalf("focus = %v, want the commit's files", m.focus)
	}
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyRight})
	if m.focus != focusDiff {
		t.Fatalf("focus = %v, want the diff", m.focus)
	}
	// And on the working tree, right from the history goes straight to the diff.
	w := drive(t, aheadModel(t, 1), altKey("5"), tea.KeyMsg{Type: tea.KeyRight})
	if w.focus != focusDiff {
		t.Errorf("focus = %v, want the diff from the working tree row", w.focus)
	}
}

// Tab reaches only the panes that are on screen, which differ between the
// working tree and a commit.
func TestTabVisitsWhatIsOnScreen(t *testing.T) {
	m := aheadModel(t, 1)
	seen := map[focusArea]bool{}
	for i := 0; i < int(focusCount)*2; i++ {
		m = drive(t, m, tea.KeyMsg{Type: tea.KeyTab})
		if m.focus == focusHistory {
			t.Fatal("Tab reached the commit files pane from the working tree")
		}
		seen[m.focus] = true
	}
	for _, want := range []focusArea{focusWorkdir, focusIndex, focusCommits, focusDiff} {
		if !seen[want] {
			t.Errorf("Tab never reached pane %v", want)
		}
	}

	b := drive(t, aheadModel(t, 1), altKey("5"), tea.KeyMsg{Type: tea.KeyDown})
	for i := 0; i < int(focusCount)*2; i++ {
		b = drive(t, b, tea.KeyMsg{Type: tea.KeyTab})
		if b.focus == focusWorkdir || b.focus == focusIndex {
			t.Fatalf("Tab reached pane %v while browsing a commit", b.focus)
		}
	}
}

// A commit that is no longer on the branch takes the cursor back to the
// working tree rather than leaving it pointing at nothing.
func TestHistoryDropsACommitThatWentAway(t *testing.T) {
	m := drive(t, aheadModel(t, 2), altKey("5"), tea.KeyMsg{Type: tea.KeyDown})
	if !m.browsing() {
		t.Fatal("not browsing")
	}
	// Rewrite the branch so the selected commit no longer exists.
	if _, err := m.repo.Git("reset", "-q", "--hard", "HEAD~2"); err != nil {
		t.Fatal(err)
	}
	m = drive(t, m, rkey("R"))
	if m.browsing() {
		t.Errorf("still browsing a commit that is gone: %+v", m.commits.commits)
	}
	if len(m.commits.commits) != 0 {
		t.Errorf("history still lists %+v", m.commits.commits)
	}
	if !strings.Contains(m.View(), "Unstaged Changes") {
		t.Errorf("the working lists did not come back:\n%s", m.View())
	}
}

// A commit's diff is its own change, not the working tree's.
func TestCommitDiffIsTheCommitsOwnChange(t *testing.T) {
	m := drive(t, aheadModel(t, 2), altKey("5"), tea.KeyMsg{Type: tea.KeyDown})
	var text []string
	for _, l := range m.diff.lines {
		text = append(text, l.Text)
	}
	joined := strings.Join(text, "\n")
	// The second commit added one line to ahead.txt; it did not create it.
	if strings.Contains(joined, "new file") {
		t.Errorf("the second commit's diff reads as if it created the file:\n%s", joined)
	}
	if !strings.Contains(joined, "+x") {
		t.Errorf("the commit's own added line is missing:\n%s", joined)
	}
}

// U on a commit pops it and everything after it off the branch, leaving the
// work staged. Selecting the oldest of a run undoes the whole run at once.
func TestPopTakesTheCommitAndEverythingAfter(t *testing.T) {
	m := aheadModel(t, 2)
	repo := m.repo
	before, err := repo.Git("rev-parse", "HEAD~2")
	if err != nil {
		t.Fatal(err)
	}
	before = strings.TrimSpace(before)

	// Down twice reaches the older of the two commits.
	m = drive(t, m, altKey("5"), tea.KeyMsg{Type: tea.KeyDown}, tea.KeyMsg{Type: tea.KeyDown})
	c, ok := m.commits.selected()
	if !ok || c.Subject != "commit number 1" {
		t.Fatalf("selected %+v, want the older commit", c)
	}

	// It asks first, and says how many come off.
	m = drive(t, m, rkey("U"))
	if m.dialog == nil {
		t.Fatal("U did not ask")
	}
	if !strings.Contains(m.dialog.body, "2 commits") {
		t.Errorf("the question does not say how many come off: %q", m.dialog.body)
	}
	if head, _ := repo.Git("rev-parse", "HEAD"); strings.TrimSpace(head) == before {
		t.Fatal("the reset happened before it was confirmed")
	}

	// n backs out.
	if back := drive(t, m, rkey("n")); back.dialog != nil {
		t.Error("n left the question up")
	}
	if head, _ := repo.Git("rev-parse", "HEAD"); strings.TrimSpace(head) == before {
		t.Fatal("n reset anyway")
	}

	m = drive(t, m, rkey("y"))
	head, err := repo.Git("rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(head) != before {
		t.Errorf("HEAD = %q, want %q", strings.TrimSpace(head), before)
	}
	// Both commits are gone, so the pane is too.
	if m.showCommits() {
		t.Errorf("the history pane survived with %+v", m.commits.commits)
	}
	if m.browsing() {
		t.Error("still browsing a commit that no longer exists")
	}
	// And their work is staged rather than lost.
	staged := map[string]bool{}
	for _, f := range m.index.files {
		staged[f.Path] = true
	}
	if !staged["ahead.txt"] {
		t.Errorf("the popped commits' file is not staged; staged = %v", m.index.files)
	}
}

// A soft reset leaves the working tree exactly as it was.
func TestPopDoesNotTouchTheWorkingTree(t *testing.T) {
	m := aheadModel(t, 1)
	repo := m.repo
	// The fixture leaves foo.txt modified but unstaged; it must stay that way.
	beforeBody, err := repo.Git("show", ":foo.txt")
	if err != nil {
		t.Fatal(err)
	}
	m = drive(t, m, altKey("5"), tea.KeyMsg{Type: tea.KeyDown}, rkey("U"), rkey("y"))
	if m.showCommits() {
		t.Fatal("the commit was not popped")
	}
	unstaged := map[string]bool{}
	for _, f := range m.workdir.files {
		unstaged[f.Path] = true
	}
	if !unstaged["foo.txt"] {
		t.Errorf("foo.txt stopped being unstaged; unstaged = %v", m.workdir.files)
	}
	afterBody, err := repo.Git("show", ":foo.txt")
	if err != nil {
		t.Fatal(err)
	}
	if afterBody != beforeBody {
		t.Error("the reset changed foo.txt in the index")
	}
}

// U with the history cursor on the working tree row has nothing to pop.
func TestPopOnTheWorkingTreeRow(t *testing.T) {
	m := drive(t, aheadModel(t, 1), altKey("5"), rkey("U"))
	if m.dialog != nil {
		t.Errorf("U asked about popping the working tree: %q", m.dialog.body)
	}
	if !m.statusEr {
		t.Error("U should say why it did nothing")
	}
}

// U means one thing wherever it is pressed: pop whatever the branch history
// has selected. It is not a pane-local key, so it works from the diff and the
// file lists too, and says so when there is no commit to act on.
func TestPopMeansTheSameThingEverywhere(t *testing.T) {
	for _, pane := range []struct {
		name string
		key  tea.KeyMsg
	}{
		{"this branch", altKey("5")},
		{"the commit's files", altKey("5")},
		{"the diff", altKey("3")},
	} {
		t.Run(pane.name, func(t *testing.T) {
			m := drive(t, aheadModel(t, 1), altKey("5"), tea.KeyMsg{Type: tea.KeyDown})
			m = drive(t, m, pane.key, rkey("U"))
			if m.dialog == nil {
				t.Fatalf("U from %s did not ask", pane.name)
			}
			m = drive(t, m, rkey("y"))
			if m.showCommits() {
				t.Errorf("U from %s did not pop: %+v", pane.name, m.commits.commits)
			}
		})
	}

	// From the working tree the history cursor is on its own row, so there is
	// nothing selected to pop and it says so rather than guessing.
	t.Run("a file list, nothing selected", func(t *testing.T) {
		m := drive(t, aheadModel(t, 1), altKey("1"), rkey("U"))
		if m.dialog != nil {
			t.Errorf("U from a file list asked about %q", m.dialog.body)
		}
		if !m.statusEr {
			t.Error("U should say there is no commit selected")
		}
		if !m.showCommits() {
			t.Error("U popped something without asking")
		}
	})
}

// p is the previous hunk and nothing else now, and r is revert-hunk and nothing
// else; both are refused against history along with the rest.
func TestOldPopKeysMeanOnlyTheirDiffCommands(t *testing.T) {
	m := drive(t, aheadModel(t, 1), altKey("5"), tea.KeyMsg{Type: tea.KeyDown})
	before := len(m.commits.commits)
	for _, k := range []tea.KeyMsg{rkey("p"), rkey("r")} {
		got := drive(t, m, altKey("3"), k)
		if got.dialog != nil {
			t.Errorf("%v in the diff opened %q", k, got.dialog.title)
		}
		if len(got.commits.commits) != before {
			t.Errorf("%v popped a commit", k)
		}
	}
	// And in the history pane neither does anything at all.
	for _, k := range []tea.KeyMsg{rkey("p")} {
		got := drive(t, m, k)
		if got.dialog != nil || len(got.commits.commits) != before {
			t.Errorf("%v in the history pane did something", k)
		}
	}
}

// p still walks hunks in the diff, where popping means nothing. A file long
// enough for two hunks to stay apart at the default context is built for it.
func TestPrevHunkStillWorksInTheDiff(t *testing.T) {
	dir := testRepoDir(t)
	var lines []string
	for i := 1; i <= 40; i++ {
		lines = append(lines, "line "+itoa(i))
	}
	writeFile(t, dir, "long.txt", strings.Join(lines, "\n")+"\n")
	gitRun(t, dir, "add", "long.txt")
	gitRun(t, dir, "commit", "-qm", "add a long file")
	lines[1] = "CHANGED NEAR THE TOP"
	lines[37] = "CHANGED NEAR THE BOTTOM"
	writeFile(t, dir, "long.txt", strings.Join(lines, "\n")+"\n")

	repo, err := git.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m = drive(t, m, altKey("1"))
	for m.workdir.currentPath() != "long.txt" {
		if m.workdir.cursor >= len(m.workdir.files)-1 {
			t.Fatalf("long.txt is not in the list: %v", m.workdir.files)
		}
		m = drive(t, m, rkey("j"))
	}
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyRight})
	if m.diff.hunkCount() != 2 {
		t.Fatalf("hunks = %d, want 2 to move between", m.diff.hunkCount())
	}

	first := m.diff.currentHunk()
	m = drive(t, m, rkey("n"))
	if m.diff.currentHunk() == first {
		t.Fatalf("n did not move forward from hunk %d", first)
	}
	m = drive(t, m, rkey("p"))
	if got := m.diff.currentHunk(); got != first {
		t.Errorf("p left the cursor on hunk %d, want back on %d", got, first)
	}
	if m.dialog != nil {
		t.Errorf("p in the diff opened the %q dialog", m.dialog.title)
	}
}

// On a narrow window the left column gives up its space to the diff, and takes
// it back the moment the focus returns.
func TestNarrowWindowCollapsesTheLeftColumn(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 80, Height: 24},
		New(repo).rescanCmd()(),
	)
	m = drive(t, m, altKey("1"))
	if m.leftCollapsed() {
		t.Fatal("the column collapsed while a list had the focus")
	}
	if !strings.Contains(m.View(), "Unstaged Changes") {
		t.Fatalf("the lists should be on screen:\n%s", m.View())
	}
	listWidth := m.diff.width

	// Right onto the diff hands it the whole window.
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyRight})
	if m.focus != focusDiff {
		t.Fatalf("focus = %v, want the diff", m.focus)
	}
	if !m.leftCollapsed() {
		t.Fatal("the column did not collapse at 80 columns")
	}
	if m.diff.width != 80 {
		t.Errorf("diff width = %d, want the full 80", m.diff.width)
	}
	if m.diff.width <= listWidth {
		t.Errorf("the diff did not gain any room: %d then %d", listWidth, m.diff.width)
	}
	view := m.View()
	if strings.Contains(view, "Unstaged Changes") || strings.Contains(view, "This Branch") {
		t.Errorf("the left column is still on screen:\n%s", view)
	}
	if strings.Contains(view, "│") {
		t.Errorf("the column separator survived the collapse:\n%s", view)
	}
	if !strings.Contains(view, "← lists") {
		t.Errorf("nothing says how to get the lists back:\n%s", view)
	}

	// The message buffer is the same column, so it stays collapsed there.
	m = drive(t, m, altKey("4"))
	if !m.leftCollapsed() {
		t.Error("moving to the message brought the lists back")
	}

	// Left brings them back.
	m = drive(t, m, altKey("3"), tea.KeyMsg{Type: tea.KeyLeft})
	if m.leftCollapsed() {
		t.Fatal("left did not bring the lists back")
	}
	if !strings.Contains(m.View(), "Unstaged Changes") {
		t.Errorf("the lists did not come back:\n%s", m.View())
	}
	if m.diff.width != listWidth {
		t.Errorf("diff width = %d, want it back to %d", m.diff.width, listWidth)
	}
}

// A window with room for both columns never collapses, whatever has the focus.
func TestWideWindowKeepsBothColumns(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: narrowWidth, Height: 24},
		New(repo).rescanCmd()(),
	)
	for _, f := range []tea.KeyMsg{altKey("1"), altKey("3"), altKey("4")} {
		m = drive(t, m, f)
		if m.leftCollapsed() {
			t.Errorf("the column collapsed at %d columns with focus %v", m.width, m.focus)
		}
		if !strings.Contains(m.View(), "Unstaged Changes") {
			t.Errorf("the lists left the screen at %d columns:\n%s", m.width, m.View())
		}
	}
}

// Staging takes the file out of the list, so the row the cursor is on becomes
// the next file down. Pressing Space repeatedly therefore walks down the list
// rather than jumping back to the top or chasing the file that just left.
func TestStagingWalksDownTheList(t *testing.T) {
	dir := testRepoDir(t)
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		writeFile(t, dir, name, "one\n")
	}
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-qm", "add four files")
	for _, name := range []string{"a.txt", "b.txt", "c.txt", "d.txt"} {
		writeFile(t, dir, name, "changed\n")
	}
	repo, err := git.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m = drive(t, m, altKey("1"))

	// Start on the second file, so a jump to the top would be visible.
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyDown})
	start := m.workdir.currentPath()
	if start != "b.txt" {
		t.Fatalf("expected to start on b.txt, got %q", start)
	}

	var staged []string
	for i := 0; i < 3; i++ {
		staged = append(staged, m.workdir.currentPath())
		m = drive(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	}
	want := []string{"b.txt", "c.txt", "d.txt"}
	if !slices.Equal(staged, want) {
		t.Errorf("three presses staged %v, want %v", staged, want)
	}
	for _, p := range want {
		found := false
		for _, f := range m.index.files {
			if f.Path == p {
				found = true
			}
		}
		if !found {
			t.Errorf("%s is not staged; staged = %v", p, m.index.files)
		}
	}
	// a.txt was above the cursor and must have been left alone.
	if len(m.workdir.files) != 1 || m.workdir.files[0].Path != "a.txt" {
		t.Errorf("unstaged list = %v, want just a.txt", m.workdir.files)
	}
}

// At the end of the list there is no next file, so the cursor settles on the
// last one rather than running off.
func TestStagingTheLastFileStepsBack(t *testing.T) {
	m := drive(t, newTestModel(t), altKey("1"), rkey("G"))
	last := m.workdir.currentPath()
	if last == "" || len(m.workdir.files) < 2 {
		t.Fatalf("need a list with a last file, got %v", m.workdir.files)
	}
	above := m.workdir.files[len(m.workdir.files)-2].Path

	m = drive(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	if got := m.workdir.currentPath(); got != above {
		t.Errorf("after staging the last file the cursor is on %q, want %q", got, above)
	}
	if m.workdir.cursor != len(m.workdir.files)-1 {
		t.Errorf("cursor = %d, want the new last row %d", m.workdir.cursor, len(m.workdir.files)-1)
	}
}

// A rescan that changes nothing leaves the cursor on the file it was on, which
// is what keeps R and the automatic rescans from moving it about.
func TestRescanKeepsTheCursorOnItsFile(t *testing.T) {
	m := drive(t, newTestModel(t), altKey("1"), tea.KeyMsg{Type: tea.KeyDown})
	before := m.workdir.currentPath()
	if before == "" {
		t.Fatal("no file under the cursor")
	}
	m = drive(t, m, rkey("R"))
	if got := m.workdir.currentPath(); got != before {
		t.Errorf("the rescan moved the cursor from %q to %q", before, got)
	}
}

// Unstaging walks down the staged list the same way.
func TestUnstagingWalksDownTheList(t *testing.T) {
	m := drive(t, newTestModel(t), altKey("1"), rkey("V"), rkey("G"), rkey("s"))
	m = drive(t, m, altKey("2"))
	if len(m.index.files) < 3 {
		t.Fatalf("need three staged files, got %v", m.index.files)
	}
	var unstaged []string
	for i := 0; i < 2; i++ {
		unstaged = append(unstaged, m.index.currentPath())
		m = drive(t, m, tea.KeyMsg{Type: tea.KeyEnter})
	}
	if unstaged[0] == unstaged[1] {
		t.Errorf("two presses unstaged the same file twice: %v", unstaged)
	}
	for _, p := range unstaged {
		for _, f := range m.index.files {
			if f.Path == p {
				t.Errorf("%s is still staged; staged = %v", p, m.index.files)
			}
		}
	}
}

// e opens the file in view in the user's editor, and rescans afterwards since
// the editor may well have changed it.
func TestEditOpensTheFileInView(t *testing.T) {
	sys := &recordingSystem{}
	repo, err := git.OpenWith(sys, testRepoDir(t))
	if err != nil {
		t.Fatal(err)
	}
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m = drive(t, m, altKey("1"))
	want := m.workdir.currentPath()
	if want == "" {
		t.Fatal("no file under the cursor")
	}

	m = drive(t, m, rkey("e"))
	if len(sys.edited) != 1 {
		t.Fatalf("e opened %v, want the one file", sys.edited)
	}
	if !strings.HasSuffix(sys.edited[0], want) {
		t.Errorf("e opened %q, want the file in view %q", sys.edited[0], want)
	}
	// It means the same thing from the diff pane, which shows the same file.
	sys.edited = nil
	m = drive(t, m, altKey("3"), rkey("e"))
	if len(sys.edited) != 1 || !strings.HasSuffix(sys.edited[0], want) {
		t.Errorf("e from the diff opened %v, want %q", sys.edited, want)
	}
}

// What happens once the editor exits: the file is rescanned, since it may have
// changed under the lists and the diff. bubbletea runs this in the runtime
// rather than through Update, so it is checked directly.
func TestEditorClosedRescans(t *testing.T) {
	repo, err := git.OpenWith(&recordingSystem{}, testRepoDir(t))
	if err != nil {
		t.Fatal(err)
	}
	msg, ok := editorClosed(repo, "foo.txt", "")(nil).(opMsg)
	if !ok {
		t.Fatalf("editorClosed returned %T, want an opMsg", editorClosed(repo, "foo.txt", "")(nil))
	}
	if !msg.rescan {
		t.Error("a finished edit must trigger a rescan")
	}
	if msg.err != nil || !strings.Contains(msg.text, "foo.txt") {
		t.Errorf("msg = %+v, want it to name the file and carry no error", msg)
	}

	failed, ok := editorClosed(repo, "foo.txt", "")(errors.New("editor blew up")).(opMsg)
	if !ok {
		t.Fatal("a failed edit did not produce an opMsg")
	}
	if failed.err == nil {
		t.Error("a failed edit lost its error")
	}
	if failed.rescan {
		t.Error("a failed edit should not claim the file changed")
	}
}

// A file that is not on disk cannot be edited; opening it would create it.
func TestEditRefusesAMissingFile(t *testing.T) {
	sys := &recordingSystem{}
	dir := testRepoDir(t)
	repo, err := git.OpenWith(sys, dir)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Join(dir, "foo.txt")); err != nil {
		t.Fatal(err)
	}
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m = drive(t, m, altKey("1"))
	for m.workdir.currentPath() != "foo.txt" {
		if m.workdir.cursor >= len(m.workdir.files)-1 {
			t.Fatalf("foo.txt is not in the unstaged list: %v", m.workdir.files)
		}
		m = drive(t, m, rkey("j"))
	}
	m = drive(t, m, rkey("e"))
	if len(sys.edited) != 0 {
		t.Errorf("e opened %v for a file that is not there", sys.edited)
	}
	if !m.statusEr {
		t.Error("e should say why it did nothing")
	}
}

// Only the working tree is editable; a file from an earlier commit is not.
func TestEditAgainstHistory(t *testing.T) {
	sys := &recordingSystem{}
	m, repo := aheadModelWith(t, 1, sys)
	m = drive(t, m, altKey("5"), tea.KeyMsg{Type: tea.KeyDown})
	if !m.browsing() {
		t.Fatal("not browsing")
	}
	before, err := repo.Git("status", "--porcelain")
	if err != nil {
		t.Fatal(err)
	}
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyRight}, rkey("e"))
	if len(sys.edited) != 1 {
		t.Fatalf("e opened %v, want the commit's copy of the one file", sys.edited)
	}
	// The editor got a copy, never the working tree's own file, and the copy
	// keeps the file's name so the editor knows what it is looking at.
	tmp := sys.edited[0]
	if strings.HasPrefix(tmp, repo.Dir) {
		t.Errorf("e against history opened %q, inside the working tree", tmp)
	}
	if filepath.Base(tmp) != "ahead.txt" {
		t.Errorf("the copy is named %q, want the file's own name", filepath.Base(tmp))
	}
	b, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if string(b) != "x\n" {
		t.Errorf("the copy holds %q, want the committed content", b)
	}
	after, err := repo.Git("status", "--porcelain")
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("the working tree moved:\nbefore:\n%s\nafter:\n%s", before, after)
	}
	// The runtime hands editorClosed the exit; the copy goes with it, and no
	// rescan is claimed since nothing can have changed.
	msg, ok := editorClosed(repo, "ahead.txt", tmp)(nil).(opMsg)
	if !ok || msg.rescan {
		t.Errorf("closing a history edit produced %+v, want no rescan", msg)
	}
	if _, err := os.Stat(tmp); !os.IsNotExist(err) {
		t.Errorf("the read-only copy %q was not cleaned up", tmp)
	}
}

// m opens the markdown reader on the file in view, full screen, and the same
// keys that opened it put it away.
func TestMarkdownReader(t *testing.T) {
	dir := testRepoDir(t)
	writeFile(t, dir, "README.md", "# A Heading\n\nSome body text here.\n")
	repo, err := git.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	// README.md sorts first in the unstaged list, so it is the file in view.
	m = drive(t, m, altKey("1"))
	if got := m.workdir.currentPath(); got != "README.md" {
		t.Fatalf("file in view is %q, want README.md", got)
	}
	m = drive(t, m, rkey("m"))
	if m.md == nil {
		t.Fatalf("m did not open the reader; status %q", m.status)
	}
	// glamour styles the text word by word, so matching is done on the plain
	// text under the styling.
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "A Heading") || !strings.Contains(view, "Some body text here.") {
		t.Errorf("the reader does not show the rendered file:\n%s", view)
	}
	if !strings.Contains(view, "README.md") {
		t.Errorf("the reader does not name the file:\n%s", view)
	}
	m = drive(t, m, rkey("q"))
	if m.md != nil {
		t.Error("q did not close the reader")
	}
	if m.quit {
		t.Error("q in the reader quit the program")
	}
}

// m refuses a file it cannot render, and reads history when a commit is
// selected — reading is the one thing history is happy to allow.
func TestMarkdownReaderScope(t *testing.T) {
	m := drive(t, aheadModel(t, 1), altKey("1"), rkey("m"))
	if m.md != nil || !m.statusEr {
		t.Errorf("m on a .txt file opened the reader; status %q", m.status)
	}
	m = drive(t, m, altKey("5"), tea.KeyMsg{Type: tea.KeyDown})
	if !m.browsing() {
		t.Fatal("not browsing")
	}
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyRight}, rkey("m"))
	if m.md != nil || !m.statusEr {
		t.Errorf("m against a commit's .txt file opened the reader; status %q", m.status)
	}
}

// The reader marks the blocks the diff touched, so a modified file shows
// where to look — and only there.
func TestMarkdownReaderMarksChanges(t *testing.T) {
	dir := testRepoDir(t)
	writeFile(t, dir, "NOTES.md", "# Title\n\nfirst paragraph\n\nsecond paragraph\n")
	gitRun(t, dir, "add", "NOTES.md")
	gitRun(t, dir, "commit", "-qm", "add notes")
	writeFile(t, dir, "NOTES.md", "# Title\n\nfirst paragraph\n\nsecond paragraph, edited\n")

	repo, err := git.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m = drive(t, m, altKey("1"))
	if got := m.workdir.currentPath(); got != "NOTES.md" {
		t.Fatalf("file in view is %q, want NOTES.md", got)
	}
	m = drive(t, m, rkey("m"))
	if m.md == nil {
		t.Fatalf("m did not open the reader; status %q", m.status)
	}
	if len(m.md.jumps) != 1 {
		t.Fatalf("jumps = %v, want the one edited paragraph", m.md.jumps)
	}
	for _, line := range strings.Split(ansi.Strip(m.View()), "\n") {
		marked := strings.HasPrefix(line, "▍")
		if marked && !strings.Contains(line, "second paragraph, edited") {
			t.Errorf("an untouched line is marked: %q", line)
		}
		if !marked && strings.Contains(line, "second paragraph, edited") {
			t.Errorf("the edited paragraph is not marked: %q", line)
		}
	}
}

// The reader shows the version the window is showing: the selected commit's,
// not the working tree's.
func TestMarkdownReaderReadsHistory(t *testing.T) {
	dir := testRepoDir(t)
	bare := t.TempDir()
	gitRun(t, bare, "init", "-q", "--bare", "-b", "main", ".")
	gitRun(t, dir, "stash", "-q", "-u")
	gitRun(t, dir, "remote", "add", "origin", bare)
	gitRun(t, dir, "push", "-q", "origin", "main")
	gitRun(t, dir, "remote", "set-head", "origin", "main")
	writeFile(t, dir, "NOTES.md", "committed version\n")
	gitRun(t, dir, "add", "NOTES.md")
	gitRun(t, dir, "commit", "-qm", "add notes")
	gitRun(t, dir, "stash", "pop", "-q")
	writeFile(t, dir, "NOTES.md", "working tree version\n")

	repo, err := git.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m = drive(t, m, altKey("5"), tea.KeyMsg{Type: tea.KeyDown})
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyRight}, rkey("m"))
	if m.md == nil {
		t.Fatalf("m did not open the reader; status %q", m.status)
	}
	view := ansi.Strip(m.View())
	if !strings.Contains(view, "committed version") {
		t.Errorf("the reader does not show the commit's version:\n%s", view)
	}
	if strings.Contains(view, "working tree version") {
		t.Errorf("the reader shows the working tree against a commit:\n%s", view)
	}
	if !strings.Contains(view, "NOTES.md @") {
		t.Errorf("the reader title does not name the commit:\n%s", view)
	}
}

func TestFileListRouting(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)

	unstaged := map[string]bool{}
	for _, f := range m.workdir.files {
		unstaged[f.Path] = true
	}
	staged := map[string]bool{}
	for _, f := range m.index.files {
		staged[f.Path] = true
	}
	if !unstaged["foo.txt"] || !unstaged["new.txt"] {
		t.Errorf("unstaged list = %v", unstaged)
	}
	if unstaged["bar.txt"] {
		t.Error("a fully staged file must not be in the unstaged list")
	}
	if !staged["bar.txt"] || len(staged) != 1 {
		t.Errorf("staged list = %v", staged)
	}
}

func TestStageThroughUI(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)

	// Focus the unstaged list and stage the file under the cursor.
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1"), Alt: true})
	if m.focus != focusWorkdir {
		t.Fatalf("focus = %v, want focusWorkdir", m.focus)
	}
	target := m.workdir.current()
	if target == nil {
		t.Fatal("no file under the cursor")
	}
	name := target.Path

	m = drive(t, m, rkey("s"))
	m = drive(t, m, New(repo).rescanCmd()())

	for _, f := range m.index.files {
		if f.Path == name {
			return
		}
	}
	t.Errorf("%s was not staged; staged list = %v", name, m.index.files)
}

func TestSmallTerminalDoesNotPanic(t *testing.T) {
	repo := testRepo(t)
	for _, size := range []tea.WindowSizeMsg{
		{Width: 20, Height: 8},
		{Width: 40, Height: 10},
		{Width: 200, Height: 60},
		{Width: 60, Height: 15},
	} {
		m := drive(t, New(repo), size, New(repo).rescanCmd()())
		view := m.View()
		if strings.TrimSpace(view) == "" {
			t.Errorf("empty view at %dx%d", size.Width, size.Height)
		}
	}
}

func TestSignOffAppendsTrailer(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m.commit.SetValue("do a thing")
	m = drive(t, m, rkey("o"))

	got := m.commit.Value()
	want := "do a thing\n\nSigned-off-by: Test <test@example.com>"
	if got != want {
		t.Errorf("commit buffer =\n%q\nwant\n%q", got, want)
	}

	// A second sign off must not duplicate the trailer.
	m = drive(t, m, rkey("o"))
	if m.commit.Value() != want {
		t.Errorf("sign off was applied twice:\n%q", m.commit.Value())
	}
}

func TestSignOffJoinsExistingTrailerBlock(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m.commit.SetValue("subject\n\nCo-authored-by: Someone <a@b.c>")
	m = drive(t, m, rkey("o"))

	want := "subject\n\nCo-authored-by: Someone <a@b.c>\nSigned-off-by: Test <test@example.com>"
	if m.commit.Value() != want {
		t.Errorf("commit buffer =\n%q\nwant\n%q", m.commit.Value(), want)
	}
}

func TestContextKeysChangeDiffContext(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1"), Alt: true})
	start := m.context

	m = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("=")})
	if m.context != start+1 {
		t.Errorf("context = %d, want %d", m.context, start+1)
	}
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-")})
	if m.context != start {
		t.Errorf("context = %d, want %d", m.context, start)
	}
}

func TestDiffPaneLoadsForSelectedFile(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1"), Alt: true})
	if m.diff.file == nil {
		t.Fatal("diff pane has no file loaded")
	}
	if m.diff.empty() {
		t.Fatal("diff pane is empty")
	}
	view := m.View()
	if !strings.Contains(view, m.diff.file.Path) {
		t.Errorf("diff header does not name the file %q", m.diff.file.Path)
	}
}

// countingSystem is the real System with a tally of the git commands run
// through it, so a test can say how much work moving the cursor costs.
type countingSystem struct {
	git.Local
	mu   sync.Mutex
	runs []string
}

func (c *countingSystem) Git(dir string, stdin []byte, args ...string) (string, error) {
	c.mu.Lock()
	c.runs = append(c.runs, strings.Join(args, " "))
	c.mu.Unlock()
	return c.Local.Git(dir, stdin, args...)
}

// since returns the commands run after a mark taken from count.
func (c *countingSystem) since(mark int) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	return append([]string{}, c.runs[mark:]...)
}

func (c *countingSystem) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return len(c.runs)
}

// Moving over a file list is the one thing done over and over, so it is the
// one thing that has to be cheap: one git for a file not seen yet, and none at
// all for one already looked at, or for arriving back on the file already on
// screen.
func TestSelectingAFileRunsAtMostOneGit(t *testing.T) {
	sys := &countingSystem{}
	repo, err := git.OpenWith(sys, testRepoDir(t))
	if err != nil {
		t.Fatal(err)
	}
	m := drive(t, New(repo), tea.WindowSizeMsg{Width: 120, Height: 40})
	m = drive(t, m, New(repo).rescanCmd()())
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1"), Alt: true})
	if len(m.workdir.files) < 2 {
		t.Fatalf("need two unstaged files to move between, got %v", m.workdir.files)
	}
	first := m.diff.file.Path

	// Down onto a file not looked at yet: its diff, and nothing else.
	mark := sys.count()
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyDown})
	moved := sys.since(mark)
	if len(moved) != 1 || !strings.HasPrefix(moved[0], "diff ") {
		t.Errorf("moving to a new file ran %d git commands, want 1 diff: %q", len(moved), moved)
	}
	if m.diff.file.Path == first {
		t.Fatalf("the diff still shows %q", first)
	}

	// Back up to the file whose diff was already taken: nothing at all.
	mark = sys.count()
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if back := sys.since(mark); len(back) != 0 {
		t.Errorf("returning to a file already looked at ran %v", back)
	}
	if m.diff.file.Path != first {
		t.Errorf("the diff shows %q, want %q", m.diff.file.Path, first)
	}

	// Leaving for the diff pane and coming back is the same file again.
	mark = sys.count()
	m = drive(t, m,
		tea.KeyMsg{Type: tea.KeyRight},
		tea.KeyMsg{Type: tea.KeyLeft},
	)
	if idle := sys.since(mark); len(idle) != 0 {
		t.Errorf("stepping into the diff and back ran %v", idle)
	}
}

// A diff is taken against the scan that asked for it, so the one arriving from
// a scan since replaced is dropped rather than shown over the current one.
func TestStaleDiffIsIgnored(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1"), Alt: true})
	shown := m.diff.shown

	stale := diffMsg{
		file: m.workdir.files[0],
		key:  diffID{path: m.workdir.files[0].Path, side: git.SideWorkdir, context: m.context, gen: m.gen - 1},
		text: "@@ -1 +1 @@\n-gone\n+stale\n",
	}
	next, _ := m.Update(stale)
	m = next.(Model)
	if m.diff.shown != shown {
		t.Errorf("a diff from an old scan replaced the one on screen")
	}
	if _, ok := m.diffs[stale.key]; ok {
		t.Error("a diff from an old scan was kept")
	}
}

// Everything a rescan learns is thrown away with it: the tree has moved under
// every diff taken against it.
func TestRescanDropsKeptDiffs(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1"), Alt: true})
	if len(m.diffs) == 0 {
		t.Fatal("no diff was kept to begin with")
	}
	before := m.gen

	m = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("R")})
	if m.gen <= before {
		t.Errorf("the scan count did not move: %d", m.gen)
	}
	for key := range m.diffs {
		if key.gen != m.gen {
			t.Errorf("a diff from scan %d outlived it", key.gen)
		}
	}
}

func TestQuitKeys(t *testing.T) {
	repo := testRepo(t)
	base := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)

	// Ctrl-Q and Ctrl-C quit from anywhere, including the message buffer.
	for _, k := range []tea.KeyMsg{{Type: tea.KeyCtrlQ}, {Type: tea.KeyCtrlC}} {
		if m := drive(t, base, k); !m.quit {
			t.Errorf("%v did not quit", k)
		}
	}

	// A bare q quits from a list, but must be typable in the message buffer.
	fromList := drive(t, base, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1"), Alt: true})
	if m := drive(t, fromList, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")}); !m.quit {
		t.Error("q did not quit from the unstaged list")
	}
	inBuffer := drive(t, base, tea.KeyMsg{Type: tea.KeyCtrlJ})
	inBuffer = drive(t, inBuffer, rkey("q"))
	if inBuffer.quit {
		t.Error("q must not quit from the message buffer")
	}
	if inBuffer.commit.Value() != "q" {
		t.Errorf("buffer = %q, want \"q\"", inBuffer.commit.Value())
	}
}

// key builds a plain rune key message.
func rkey(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

// altKey builds an Alt-modified rune key message.
func altKey(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s), Alt: true}
}

func newTestModel(t *testing.T) Model {
	t.Helper()
	repo := testRepo(t)
	return drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
}

// V selects a run of files the way it selects a run of lines in the diff, and
// Space acts on the whole run.
func TestVisualSelectsARangeOfFiles(t *testing.T) {
	m := drive(t, newTestModel(t), altKey("1"))
	if len(m.workdir.files) < 2 {
		t.Fatalf("need two unstaged files to select, got %v", m.workdir.files)
	}
	want := []string{m.workdir.files[0].Path, m.workdir.files[1].Path}

	m = drive(t, m, rkey("V"))
	if !m.workdir.visual() {
		t.Fatal("V did not start a selection")
	}
	if got := m.workdir.targets(); !slices.Equal(got, want[:1]) {
		t.Errorf("a fresh selection covers %v, want just the cursor file", got)
	}

	m = drive(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if got := m.workdir.targets(); !slices.Equal(got, want) {
		t.Errorf("selection = %v, want %v", got, want)
	}

	// Another down would cross into the staged list; a selection holds the
	// cursor, since leaving would abandon it mid-gesture.
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyDown})
	if m.focus != focusWorkdir {
		t.Errorf("focus left the list mid-selection: %v", m.focus)
	}
	if got := m.workdir.targets(); !slices.Equal(got, want) {
		t.Errorf("at the end of the list, selection = %v, want %v", got, want)
	}

	m = drive(t, m, rkey(" "))
	staged := map[string]bool{}
	for _, f := range m.index.files {
		staged[f.Path] = true
	}
	for _, p := range want {
		if !staged[p] {
			t.Errorf("%s was not staged; staged list = %v", p, m.index.files)
		}
	}
	if m.workdir.visual() {
		t.Error("the selection should end once it has been staged")
	}
}

// Selecting upwards covers the same range: the anchor is where V was pressed,
// not the top of the range.
func TestVisualSelectsUpwards(t *testing.T) {
	m := drive(t, newTestModel(t), altKey("1"))
	if len(m.workdir.files) < 2 {
		t.Fatalf("need two unstaged files to select, got %v", m.workdir.files)
	}
	want := []string{m.workdir.files[0].Path, m.workdir.files[1].Path}

	m = drive(t, m, rkey("j"), rkey("V"), rkey("k"))
	if got := m.workdir.targets(); !slices.Equal(got, want) {
		t.Errorf("selection = %v, want %v", got, want)
	}
	// And it does not run off the top either.
	m = drive(t, m, rkey("k"))
	if m.focus != focusWorkdir {
		t.Errorf("focus left the list mid-selection: %v", m.focus)
	}
	if got := m.workdir.targets(); !slices.Equal(got, want) {
		t.Errorf("at the top of the list, selection = %v, want %v", got, want)
	}
}

// A selection belongs to the pane and the moment it was made in.
func TestVisualSelectionEnds(t *testing.T) {
	for _, tc := range []struct {
		how string
		key tea.KeyMsg
	}{
		{"Esc", tea.KeyMsg{Type: tea.KeyEsc}},
		{"V again", rkey("V")},
		{"leaving for the diff pane", altKey("3")},
		{"a rescan", rkey("R")},
	} {
		t.Run(tc.how, func(t *testing.T) {
			m := drive(t, newTestModel(t), altKey("1"), rkey("V"), rkey("j"))
			if !m.workdir.visual() {
				t.Fatal("no selection to end")
			}
			m = drive(t, m, tc.key)
			if m.workdir.visual() {
				t.Errorf("the selection outlived %s", tc.how)
			}
		})
	}
}

// The staged list selects the same way, and Space unstages the lot.
func TestVisualUnstagesFromTheStagedList(t *testing.T) {
	m := drive(t, newTestModel(t), altKey("1"), rkey("V"), rkey("j"), rkey(" "))
	if len(m.index.files) < 2 {
		t.Fatalf("need two staged files to select, got %v", m.index.files)
	}
	m = drive(t, m, altKey("2"))
	picked := []string{m.index.files[0].Path, m.index.files[1].Path}
	m = drive(t, m, rkey("V"), rkey("j"))
	if got := m.index.targets(); !slices.Equal(got, picked) {
		t.Fatalf("selection = %v, want %v", got, picked)
	}
	m = drive(t, m, rkey(" "))
	for _, f := range m.index.files {
		if slices.Contains(picked, f.Path) {
			t.Errorf("%s is still staged; staged list = %v", f.Path, m.index.files)
		}
	}
}

func TestDownAtEndOfListEntersPaneBelow(t *testing.T) {
	m := drive(t, newTestModel(t), tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1"), Alt: true})
	if len(m.workdir.files) < 2 {
		t.Fatalf("need at least 2 unstaged files, got %d", len(m.workdir.files))
	}

	// Walk to the bottom of the unstaged list; it should not leave yet.
	for i := 0; i < len(m.workdir.files)-1; i++ {
		m = drive(t, m, rkey("j"))
		if m.focus != focusWorkdir {
			t.Fatalf("left the unstaged list early, at row %d", i+1)
		}
	}
	// One more crosses into the staged list, landing at its top.
	m = drive(t, m, rkey("j"))
	if m.focus != focusIndex {
		t.Fatalf("focus = %v, want focusIndex", m.focus)
	}
	if m.index.cursor != 0 {
		t.Errorf("entered the staged list at row %d, want 0", m.index.cursor)
	}
	// The diff should now be showing the staged file.
	if m.diff.file == nil || m.diff.side != git.SideIndex {
		t.Error("moving into the staged list did not load its diff")
	}
}

func TestUpAtTopOfStagedListEntersPaneAbove(t *testing.T) {
	m := drive(t, newTestModel(t), tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2"), Alt: true})
	if m.focus != focusIndex {
		t.Fatalf("focus = %v, want focusIndex", m.focus)
	}
	m = drive(t, m, rkey("k"))
	if m.focus != focusWorkdir {
		t.Fatalf("focus = %v, want focusWorkdir", m.focus)
	}
	// Coming up from below lands on the last row.
	if want := len(m.workdir.files) - 1; m.workdir.cursor != want {
		t.Errorf("entered the unstaged list at row %d, want %d", m.workdir.cursor, want)
	}
}

func TestUpAtTopOfUnstagedListStays(t *testing.T) {
	m := drive(t, newTestModel(t), tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1"), Alt: true})
	m = drive(t, m, rkey("k"), rkey("k"))
	if m.focus != focusWorkdir {
		t.Errorf("focus = %v, want to stay on focusWorkdir", m.focus)
	}
	if m.workdir.cursor != 0 {
		t.Errorf("cursor = %d, want 0", m.workdir.cursor)
	}
}

func TestLeftRightCrossColumns(t *testing.T) {
	base := newTestModel(t)
	cases := []struct {
		name  string
		start focusArea
		key   string
		want  focusArea
	}{
		{"unstaged right to diff", focusWorkdir, "l", focusDiff},
		{"staged right to diff", focusIndex, "l", focusDiff},
		{"unstaged left stays", focusWorkdir, "h", focusWorkdir},
		{"staged left stays", focusIndex, "h", focusIndex},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, cmd := base.setFocus(c.start)
			got := drive(t, m.(Model), rkey(c.key))
			_ = cmd
			if got.focus != c.want {
				t.Errorf("focus = %v, want %v", got.focus, c.want)
			}
		})
	}
}

func TestArrowKeysNavigateLikeHJKL(t *testing.T) {
	base := newTestModel(t)
	m, _ := base.setFocus(focusWorkdir)
	got := drive(t, m.(Model), tea.KeyMsg{Type: tea.KeyRight})
	if got.focus != focusDiff {
		t.Errorf("right arrow: focus = %v, want focusDiff", got.focus)
	}
	got = drive(t, got, tea.KeyMsg{Type: tea.KeyLeft})
	if got.focus != focusWorkdir {
		t.Errorf("left arrow: focus = %v, want focusWorkdir", got.focus)
	}
	got = drive(t, got, tea.KeyMsg{Type: tea.KeyDown})
	if got.workdir.cursor != 1 {
		t.Errorf("down arrow: cursor = %d, want 1", got.workdir.cursor)
	}
}

func step(m Model, msg tea.Msg) Model {
	next, _ := m.Update(msg)
	return next.(Model)
}

func stateFile(t *testing.T, repo *git.Repo, name string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repo.GitDir, name))
	if err != nil {
		return "", false
	}
	return string(b), true
}

func TestQuitSavesCommitBuffer(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m.commit.SetValue("a message in progress\n")

	m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if !m.quit {
		t.Fatal("Ctrl-Q did not quit")
	}
	got, ok := stateFile(t, repo, git.MsgFile)
	if !ok {
		t.Fatalf("%s was not written", git.MsgFile)
	}
	if got != "a message in progress" {
		t.Errorf("saved message = %q", got)
	}
	// The crash backup must not be left behind after a clean exit.
	if _, ok := stateFile(t, repo, git.BackupFile); ok {
		t.Errorf("%s should have been removed on quit", git.BackupFile)
	}

	// A new session picks the message back up.
	next := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	if next.commit.Value() != "a message in progress" {
		t.Errorf("reopened buffer = %q, want the saved message", next.commit.Value())
	}
}

func TestQuitWithEmptyBufferSavesNothing(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if _, ok := stateFile(t, repo, git.MsgFile); ok {
		t.Error("an empty buffer should not be saved")
	}
}

func TestBackupTickWritesCrashBackup(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m.commit.SetValue("half typed  \n")

	m = step(m, backupTickMsg{})
	got, ok := stateFile(t, repo, git.BackupFile)
	if !ok {
		t.Fatalf("%s was not written", git.BackupFile)
	}
	if got != "half typed" {
		t.Errorf("backup = %q, want trailing whitespace stripped", got)
	}

	// Clearing the buffer removes the backup again.
	m.commit.SetValue("")
	m = step(m, backupTickMsg{})
	if _, ok := stateFile(t, repo, git.BackupFile); ok {
		t.Error("backup should be removed once the buffer is empty")
	}
}

func TestCrashBackupIsRestoredOnStartup(t *testing.T) {
	repo := testRepo(t)
	if err := repo.WriteStateFile(git.BackupFile, "recovered text"); err != nil {
		t.Fatal(err)
	}
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	if m.commit.Value() != "recovered text" {
		t.Errorf("buffer = %q, want the crash backup", m.commit.Value())
	}
}

func TestNewerSavedMessageBeatsStaleBackup(t *testing.T) {
	repo := testRepo(t)
	if err := repo.WriteStateFile(git.BackupFile, "stale backup"); err != nil {
		t.Fatal(err)
	}
	// Make the saved message strictly newer than the backup.
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(filepath.Join(repo.GitDir, git.BackupFile), old, old); err != nil {
		t.Fatal(err)
	}
	if err := repo.WriteStateFile(git.MsgFile, "the newer saved message"); err != nil {
		t.Fatal(err)
	}

	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	if m.commit.Value() != "the newer saved message" {
		t.Errorf("buffer = %q, want the newer saved message", m.commit.Value())
	}
	if _, ok := stateFile(t, repo, git.BackupFile); ok {
		t.Error("the stale backup should have been discarded")
	}
}

func TestMergeMessageSeedsBuffer(t *testing.T) {
	repo := testRepo(t)
	if err := repo.WriteStateFile("MERGE_HEAD", "0000000000000000000000000000000000000000\n"); err != nil {
		t.Fatal(err)
	}
	if err := repo.WriteStateFile("MERGE_MSG", "Merge branch 'other'"); err != nil {
		t.Fatal(err)
	}
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	if m.commit.Value() != "Merge branch 'other'" {
		t.Errorf("buffer = %q, want the merge message", m.commit.Value())
	}
	// The buffer's title names the kind of commit being written.
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	if !strings.Contains(m.View(), "Merge Commit Message") {
		t.Errorf("the buffer should say Merge Commit Message:\n%s", m.View())
	}
}

func TestCommitTemplateSeedsBuffer(t *testing.T) {
	repo := testRepo(t)
	tmpl := filepath.Join(repo.Dir, ".gitmessage")
	if err := os.WriteFile(tmpl, []byte("# describe your change"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Git("config", "commit.template", tmpl); err != nil {
		t.Fatal(err)
	}
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	if m.commit.Value() != "# describe your change" {
		t.Errorf("buffer = %q, want the commit template", m.commit.Value())
	}
}

func TestAmendLoadsAndRestoresBuffer(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m.commit.SetValue("work in progress")

	// A turns on amend and pulls in HEAD's message.
	m = drive(t, m, rkey("A"))
	if !m.amend {
		t.Fatal("A did not turn on amend")
	}
	if m.commit.Value() != "initial" {
		t.Errorf("buffer = %q, want HEAD's message", m.commit.Value())
	}
	// Amending is announced in the branch bar, and again by the buffer.
	if !strings.Contains(m.View(), "[amending]") {
		t.Errorf("the branch bar should show the amend state:\n%s", m.View())
	}
	opened := drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	if !strings.Contains(opened.View(), "Amend Last Commit") {
		t.Errorf("the buffer should say Amend Last Commit:\n%s", opened.View())
	}

	// Turning it back off restores what was being typed.
	m = drive(t, m, rkey("A"))
	if m.amend {
		t.Fatal("A did not turn amend back off")
	}
	if m.commit.Value() != "work in progress" {
		t.Errorf("buffer = %q, want the previous message back", m.commit.Value())
	}
}

// A backup tick while amending used to leave the amend text on disk, because
// the quit path promoted the backup without checking for amend.
func TestAmendMessageIsNotSavedOnQuitAfterABackup(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m = drive(t, m, rkey("A"))
	if !m.amend {
		t.Fatal("amend was not enabled")
	}
	m.commit.SetValue("initial, reworded")

	// The periodic backup fires, and then nothing else is typed, so the
	// buffer is clean with respect to the backup.
	m = step(m, backupTickMsg{})
	if !m.bckExists {
		t.Fatal("the backup tick did not write a backup")
	}
	if m.msgDirty() {
		t.Fatal("the buffer should be clean right after a backup")
	}

	m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if got, ok := stateFile(t, repo, git.MsgFile); ok {
		t.Errorf("an amend message was stashed as the next commit message: %q", got)
	}
	if _, ok := stateFile(t, repo, git.BackupFile); ok {
		t.Error("the backup should be removed on quit")
	}
}

func TestAmendMessageIsNotSavedOnQuit(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m = drive(t, m, rkey("A"))
	if !m.amend {
		t.Fatal("amend was not enabled")
	}
	m.commit.SetValue("reworded HEAD")
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})

	if _, ok := stateFile(t, repo, git.MsgFile); ok {
		t.Error("an amend message must not be stashed as the next commit message")
	}
}

func TestStartsFocusedOnFirstUnstagedFile(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	if m.focus != focusWorkdir {
		t.Fatalf("focus = %v, want focusWorkdir", m.focus)
	}
	if m.workdir.cursor != 0 {
		t.Errorf("cursor = %d, want the first file", m.workdir.cursor)
	}
	if m.workdir.current() == nil {
		t.Fatal("no file under the cursor")
	}
	// The first file's diff should already be showing.
	if m.diff.file == nil || m.diff.file.Path != m.workdir.current().Path {
		t.Errorf("diff pane shows %v, want %s", m.diff.file, m.workdir.current().Path)
	}
	// Single letter keys are live immediately, rather than being typed into
	// the message buffer.
	got := drive(t, m, rkey("j"))
	if got.workdir.cursor != 1 {
		t.Errorf("j at startup moved to %d, want 1", got.workdir.cursor)
	}
	if got.commit.Value() != "" {
		t.Errorf("keys leaked into the commit buffer: %q", got.commit.Value())
	}
}

func TestStartupFocusWithNoUnstagedFiles(t *testing.T) {
	repo := testRepo(t)
	// Stage everything, so the unstaged list is empty at startup.
	if err := repo.Stage([]string{"."}); err != nil {
		t.Fatal(err)
	}
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	if len(m.workdir.files) != 0 {
		t.Fatalf("expected an empty unstaged list, got %v", m.workdir.files)
	}
	if m.focus != focusWorkdir {
		t.Errorf("focus = %v, want focusWorkdir", m.focus)
	}
	if strings.TrimSpace(m.View()) == "" {
		t.Error("view is empty")
	}
	// Moving down out of the empty list still reaches the staged one.
	got := drive(t, m, rkey("j"))
	if got.focus != focusIndex {
		t.Errorf("focus = %v, want focusIndex", got.focus)
	}
}

// The diff pane remembers which list it is showing, so leaving it and coming
// straight back lands on the same file rather than snapping to the other list.
func TestDiffRemembersWhichListItIsShowing(t *testing.T) {
	for _, c := range []struct {
		name  string
		start focusArea
		focus tea.KeyMsg
		side  git.Side
	}{
		{"unstaged", focusWorkdir, altKey("1"), git.SideWorkdir},
		{"staged", focusIndex, altKey("2"), git.SideIndex},
	} {
		t.Run(c.name, func(t *testing.T) {
			repo := testRepo(t)
			m := drive(t, New(repo),
				tea.WindowSizeMsg{Width: 120, Height: 40},
				New(repo).rescanCmd()(),
			)
			m = drive(t, m, c.focus)
			if m.focus != c.start {
				t.Fatalf("focus = %v, want %v", m.focus, c.start)
			}
			list := m.workdir
			if c.start == focusIndex {
				list = m.index
			}
			want := list.currentPath()
			if want == "" {
				t.Fatal("no file selected")
			}

			// Right into the diff: it must be showing this list's side.
			m = drive(t, m, rkey("l"))
			if m.focus != focusDiff {
				t.Fatalf("focus = %v, want focusDiff", m.focus)
			}
			if m.diff.side != c.side {
				t.Errorf("diff side = %v, want %v", m.diff.side, c.side)
			}
			if m.diff.file == nil || m.diff.file.Path != want {
				t.Errorf("diff shows %v, want %s", m.diff.file, want)
			}

			// Left again: back to the same list, same selection.
			m = drive(t, m, rkey("h"))
			if m.focus != c.start {
				t.Errorf("focus = %v, want to return to %v", m.focus, c.start)
			}
			back := m.workdir
			if c.start == focusIndex {
				back = m.index
			}
			if got := back.currentPath(); got != want {
				t.Errorf("selection = %q, want %q", got, want)
			}
		})
	}
}

func TestStatusMessageClearsOnTheNextKey(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)

	// Changing the diff context reports it in the status bar.
	m = drive(t, m, rkey("="))
	if m.status == "" {
		t.Fatal("changing the context set no status message")
	}
	if !strings.Contains(m.View(), m.status) {
		t.Error("the status message is not on screen")
	}
	shown := m.status

	// Any following key press clears it again.
	m = drive(t, m, rkey("j"))
	if m.status != "" {
		t.Errorf("status = %q, want it cleared by the next key", m.status)
	}
	if strings.Contains(m.View(), shown) {
		t.Error("the stale status message is still on screen")
	}
	// With nothing to report, the bar falls back to the key hints.
	if !strings.Contains(m.View(), "Space stage") {
		t.Error("the status bar should fall back to the hints")
	}

	// Repeating the action still shows its own message.
	m = drive(t, m, rkey("="))
	if m.status == "" {
		t.Error("the action's own message was cleared along with the old one")
	}
}

// diffModel returns a model focused on the diff of a file with several
// changed lines in one hunk.
func diffModel(t *testing.T) Model {
	t.Helper()
	repo := testRepo(t)
	writeFile(t, repo.Dir, "foo.txt", "one\nA\nB\nC\nfive\n")
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m = drive(t, m, altKey("1"))
	for m.workdir.current() != nil && m.workdir.current().Path != "foo.txt" {
		m = drive(t, m, rkey("j"))
	}
	m = drive(t, m, rkey("l"))
	if m.focus != focusDiff {
		t.Fatalf("focus = %v, want focusDiff", m.focus)
	}
	return m
}

// cursorTo puts the diff cursor on the first line of the given kind.
func cursorTo(t *testing.T, m Model, kind byte) Model {
	t.Helper()
	for i, l := range m.diff.lines {
		if l.Kind == kind {
			m.diff.moveTo(i)
			return m
		}
	}
	t.Fatalf("no %c line in the diff", kind)
	return m
}

func TestVisualStartsOnTheCurrentLine(t *testing.T) {
	m := cursorTo(t, diffModel(t), '+')
	at := m.diff.cursor

	m = drive(t, m, rkey("V"))
	if !m.diff.visual() {
		t.Fatal("V did not start visual line selection")
	}
	if m.diff.anchor != at || m.diff.cursor != at {
		t.Errorf("anchor/cursor = %d/%d, want both %d", m.diff.anchor, m.diff.cursor, at)
	}
	if !m.diff.inSelection(at) {
		t.Error("the starting line should be selected")
	}
	// The hint line tells the user what is possible now.
	view := m.View()
	if !strings.Contains(view, "VISUAL LINE") {
		t.Error("the status bar should announce visual line mode")
	}
	for _, want := range []string{"extend", "Space", "Esc"} {
		if !strings.Contains(view, want) {
			t.Errorf("the visual hint should mention %q", want)
		}
	}
}

func TestVisualExtendsWithUpAndDown(t *testing.T) {
	m := cursorTo(t, diffModel(t), '+')
	start := m.diff.cursor
	m = drive(t, m, rkey("V"), rkey("j"), rkey("j"))
	if m.diff.cursor != start+2 {
		t.Fatalf("cursor = %d, want %d", m.diff.cursor, start+2)
	}
	for i := start; i <= start+2; i++ {
		if !m.diff.inSelection(i) {
			t.Errorf("line %d should be selected", i)
		}
	}
	if m.diff.inSelection(start - 1) {
		t.Error("the line above the anchor should not be selected")
	}
	// Arrow keys behave the same.
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if m.diff.cursor != start+1 {
		t.Errorf("after up, cursor = %d, want %d", m.diff.cursor, start+1)
	}
}

func TestVisualDoesNotLeaveTheDiffPane(t *testing.T) {
	m := diffModel(t)
	m.diff.moveTo(len(m.diff.lines) - 1)
	m = drive(t, m, rkey("V"), rkey("j"), rkey("j"))
	if m.focus != focusDiff {
		t.Errorf("focus = %v: running off the end abandoned the selection", m.focus)
	}
	if !m.diff.visual() {
		t.Error("the selection was dropped")
	}
}

func TestVisualCancels(t *testing.T) {
	for _, k := range []struct {
		name string
		msg  tea.KeyMsg
	}{
		{"Esc", tea.KeyMsg{Type: tea.KeyEsc}},
		{"V again", rkey("V")},
	} {
		t.Run(k.name, func(t *testing.T) {
			m := drive(t, cursorTo(t, diffModel(t), '+'), rkey("V"))
			if !m.diff.visual() {
				t.Fatal("visual mode did not start")
			}
			m = drive(t, m, k.msg)
			if m.diff.visual() {
				t.Errorf("%s did not cancel visual mode", k.name)
			}
			if strings.Contains(m.View(), "VISUAL LINE") {
				t.Error("the visual hint is still showing")
			}
		})
	}
}

func TestVisualClearedByLeavingThePane(t *testing.T) {
	m := drive(t, cursorTo(t, diffModel(t), '+'), rkey("V"))
	m = drive(t, m, altKey("1"))
	if m.diff.visual() {
		t.Error("leaving the diff pane should drop the selection")
	}
}

// wideDiffModel opens a diff whose lines run well past the right edge of the
// pane, so that left and right have somewhere to scroll to.
func wideDiffModel(t *testing.T) Model {
	t.Helper()
	repo := testRepo(t)
	long := "two " + strings.Repeat("and something else again ", 12)
	writeFile(t, repo.Dir, "foo.txt", "one\n"+long+"\nthree\nfour\nfive\n")
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m = drive(t, m, altKey("1"))
	for m.workdir.current() != nil && m.workdir.current().Path != "foo.txt" {
		m = drive(t, m, rkey("j"))
	}
	m = drive(t, m, rkey("l"))
	if m.focus != focusDiff {
		t.Fatalf("focus = %v, want focusDiff", m.focus)
	}
	if m.diff.maxHOffset() <= hScrollStep {
		t.Fatalf("the fixture fits the pane: longest line %d, code width %d",
			m.diff.long, m.diff.codeWidth())
	}
	return m
}

// A line wider than the pane belongs to left and right until it is back
// against the left margin; only then do they mean the pane beside it.
func TestLongLinesScrollSideways(t *testing.T) {
	m := wideDiffModel(t)
	before := m.View()

	m = drive(t, m, rkey("l"))
	if m.focus != focusDiff {
		t.Fatalf("right left the diff instead of scrolling: focus = %v", m.focus)
	}
	if m.diff.hoffset != hScrollStep {
		t.Errorf("hoffset = %d, want %d", m.diff.hoffset, hScrollStep)
	}
	if m.View() == before {
		t.Error("scrolling right changed nothing on screen")
	}
	// Nothing else on screen says the left margin has moved.
	if want := "col " + itoa(hScrollStep+1); !strings.Contains(m.View(), want) {
		t.Errorf("the title should say %q:\n%s", want, m.View())
	}

	// Left slides it back, staying in the diff.
	m = drive(t, m, rkey("h"))
	if m.focus != focusDiff {
		t.Fatalf("left abandoned the diff with the code still scrolled")
	}
	if m.diff.hoffset != 0 {
		t.Errorf("hoffset = %d, want the code back at the margin", m.diff.hoffset)
	}
	if m.View() != before {
		t.Error("scrolling out and back did not restore the diff")
	}

	// And from the margin it means the list again, on the same file.
	m = drive(t, m, rkey("h"))
	if m.focus != focusWorkdir {
		t.Errorf("focus = %v, want left from column one to leave for the list", m.focus)
	}
}

// The arrows scroll the same way h and l do.
func TestArrowsScrollTheDiffSideways(t *testing.T) {
	m := drive(t, wideDiffModel(t), tea.KeyMsg{Type: tea.KeyRight})
	if m.diff.hoffset != hScrollStep || m.focus != focusDiff {
		t.Errorf("right arrow: hoffset = %d, focus = %v", m.diff.hoffset, m.focus)
	}
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyLeft})
	if m.diff.hoffset != 0 || m.focus != focusDiff {
		t.Errorf("left arrow: hoffset = %d, focus = %v", m.diff.hoffset, m.focus)
	}
}

// The scroll stops with the longest line at the right margin, so there is
// never empty space to scroll into.
func TestScrollStopsAtTheLongestLine(t *testing.T) {
	m := wideDiffModel(t)
	for i := 0; i < 200 && m.diff.hoffset < m.diff.maxHOffset(); i++ {
		m = drive(t, m, rkey("l"))
	}
	m = drive(t, m, rkey("l"), rkey("l"))
	if m.diff.hoffset != m.diff.maxHOffset() {
		t.Errorf("hoffset = %d, want it held at %d", m.diff.hoffset, m.diff.maxHOffset())
	}
	if m.focus != focusDiff {
		t.Errorf("focus = %v: right at the end of the line should stay put", m.focus)
	}
	// The rows keep the pane's width however far over they are.
	widths := map[int]bool{}
	for _, row := range strings.Split(m.View(), "\n") {
		widths[lipgloss.Width(row)] = true
	}
	if len(widths) != 1 {
		t.Errorf("scrolled rows have differing widths: %v", widths)
	}
}

// A diff that fits its pane keeps the old behaviour: left leaves at once.
func TestShortDiffLeavesOnLeft(t *testing.T) {
	m := diffModel(t)
	if m.diff.maxHOffset() != 0 {
		t.Fatalf("the fixture does not fit the pane: %d", m.diff.maxHOffset())
	}
	m = drive(t, m, rkey("h"))
	if m.focus != focusWorkdir {
		t.Errorf("focus = %v, want focusWorkdir", m.focus)
	}
}

// Scrolling is the one sideways move visual line mode allows: it does not
// abandon the selection, and it does not leave the pane.
func TestScrollingDoesNotEndVisualMode(t *testing.T) {
	m := drive(t, wideDiffModel(t), rkey("V"), rkey("l"))
	if !m.diff.visual() || m.focus != focusDiff {
		t.Fatalf("visual = %v, focus = %v", m.diff.visual(), m.focus)
	}
	if m.diff.hoffset != hScrollStep {
		t.Errorf("hoffset = %d, want %d", m.diff.hoffset, hScrollStep)
	}
	m = drive(t, m, rkey("h"), rkey("h"))
	if !m.diff.visual() || m.focus != focusDiff {
		t.Errorf("left out of a selection: visual = %v, focus = %v", m.diff.visual(), m.focus)
	}
}

// Moving to another file starts it at the left margin again.
func TestANewDiffStartsAtTheMargin(t *testing.T) {
	m := drive(t, wideDiffModel(t), rkey("l"), rkey("l"))
	if m.diff.hoffset == 0 {
		t.Fatal("the diff did not scroll")
	}
	m = drive(t, m, altKey("1"), rkey("j"), altKey("3"))
	if m.diff.hoffset != 0 {
		t.Errorf("hoffset = %d, want a new diff to start at column one", m.diff.hoffset)
	}
}

// tallDiffModel opens a diff of more lines than the pane can show, on short
// enough lines that only the vertical scroll is in play.
func tallDiffModel(t *testing.T) Model {
	t.Helper()
	repo := testRepo(t)
	var body []string
	for i := 0; i < 60; i++ {
		body = append(body, "line "+itoa(i))
	}
	return openDiff(t, repo, strings.Join(body, "\n")+"\n", 120, 24)
}

// bothWaysDiffModel needs scrolling in both directions at once.
func bothWaysDiffModel(t *testing.T) Model {
	t.Helper()
	repo := testRepo(t)
	var body []string
	for i := 0; i < 60; i++ {
		body = append(body, "line "+itoa(i)+" "+strings.Repeat("and something else again ", 4))
	}
	return openDiff(t, repo, strings.Join(body, "\n")+"\n", 120, 24)
}

// openDiff writes foo.txt, opens the window at the given size and moves to the
// diff of that file.
func openDiff(t *testing.T, repo *git.Repo, content string, w, h int) Model {
	t.Helper()
	writeFile(t, repo.Dir, "foo.txt", content)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: w, Height: h},
		New(repo).rescanCmd()(),
	)
	m = drive(t, m, altKey("1"))
	for m.workdir.current() != nil && m.workdir.current().Path != "foo.txt" {
		m = drive(t, m, rkey("j"))
	}
	m = drive(t, m, rkey("l"))
	if m.focus != focusDiff {
		t.Fatalf("focus = %v, want focusDiff", m.focus)
	}
	return m
}

// paneRows renders the diff pane on its own, so the assertions are about its
// own edges rather than about the whole window.
func paneRows(m Model) []string {
	return strings.Split(m.diff.view(m.st, true), "\n")
}

// rightEdge is the last column of a block of rows, which is where a vertical
// scrollbar lives.
func rightEdge(rows []string) string {
	var b strings.Builder
	for _, r := range rows {
		text := []rune(plainText(r))
		if len(text) == 0 {
			continue
		}
		b.WriteRune(text[len(text)-1])
	}
	return b.String()
}

// A bar that could never move says nothing and costs a column of code, so a
// diff that fits its pane has neither.
func TestNoScrollbarsWhenNothingScrolls(t *testing.T) {
	m := diffModel(t)
	if vert, horz := m.diff.bars(); vert || horz {
		t.Fatalf("bars on a diff that fits: vertical %v, horizontal %v", vert, horz)
	}
	rows := paneRows(m)
	if len(rows) != m.diff.height+1 {
		t.Errorf("pane is %d rows, want the header and %d of body", len(rows), m.diff.height)
	}
	for i, r := range rows {
		if strings.ContainsAny(plainText(r), thumbGlyph+vertTrack+horzTrack) {
			t.Errorf("row %d has scrollbar furniture: %q", i, plainText(r))
		}
	}
}

// The vertical bar says how much of the diff is on screen and where.
func TestVerticalScrollbarShowsWhereTheViewIs(t *testing.T) {
	m := tallDiffModel(t)
	vert, horz := m.diff.bars()
	if !vert || horz {
		t.Fatalf("want only the vertical bar: vertical %v, horizontal %v", vert, horz)
	}

	edge := rightEdge(paneRows(m)[1:])
	if !strings.HasPrefix(edge, thumbGlyph) {
		t.Errorf("at the top of the diff the thumb should start at the top: %q", edge)
	}
	if strings.HasSuffix(edge, thumbGlyph) {
		t.Errorf("the thumb fills the track though most of the diff is off screen: %q", edge)
	}
	if n := strings.Count(edge, thumbGlyph); n < 1 {
		t.Errorf("no thumb in the track: %q", edge)
	}

	m = drive(t, m, rkey("G"))
	edge = rightEdge(paneRows(m)[1:])
	if !strings.HasSuffix(edge, thumbGlyph) {
		t.Errorf("at the end of the diff the thumb should reach the bottom: %q", edge)
	}
	if strings.HasPrefix(edge, thumbGlyph) {
		t.Errorf("the thumb is still at the top at the end of the diff: %q", edge)
	}

	// Somewhere in the middle it touches neither end.
	m = drive(t, m, rkey("g"),
		tea.KeyMsg{Type: tea.KeyPgDown}, tea.KeyMsg{Type: tea.KeyPgDown})
	edge = rightEdge(paneRows(m)[1:])
	if m.diff.offset == 0 || m.diff.offset == m.diff.maxOffset() {
		t.Fatalf("offset = %d, want the middle of a %d line diff", m.diff.offset, len(m.diff.lines))
	}
	if strings.HasPrefix(edge, thumbGlyph) || strings.HasSuffix(edge, thumbGlyph) {
		t.Errorf("part way down the diff the thumb touches an end: %q", edge)
	}
}

// The horizontal bar is the same idea along the foot of the pane.
func TestHorizontalScrollbarShowsHowFarAcross(t *testing.T) {
	m := wideDiffModel(t)
	vert, horz := m.diff.bars()
	if vert || !horz {
		t.Fatalf("want only the horizontal bar: vertical %v, horizontal %v", vert, horz)
	}

	rows := paneRows(m)
	foot := plainText(rows[len(rows)-1])
	if !strings.HasPrefix(foot, thumbGlyph) {
		t.Errorf("at column one the thumb should start at the left: %q", foot)
	}
	if strings.HasSuffix(foot, thumbGlyph) {
		t.Errorf("the thumb fills the track though the line runs off the pane: %q", foot)
	}

	for m.diff.hoffset < m.diff.maxHOffset() {
		m = drive(t, m, rkey("l"))
	}
	rows = paneRows(m)
	foot = plainText(rows[len(rows)-1])
	if !strings.HasSuffix(foot, thumbGlyph) {
		t.Errorf("at the end of the line the thumb should reach the right: %q", foot)
	}
	if strings.HasPrefix(foot, thumbGlyph) {
		t.Errorf("the thumb is still at the left at the end of the line: %q", foot)
	}
}

// The bars come out of the body, so the pane keeps its shape and the code
// gives up the room.
func TestScrollbarsTakeTheirSpaceFromTheBody(t *testing.T) {
	m := bothWaysDiffModel(t)
	vert, horz := m.diff.bars()
	if !vert || !horz {
		t.Fatalf("want both bars: vertical %v, horizontal %v", vert, horz)
	}

	rows := paneRows(m)
	if len(rows) != m.diff.height+1 {
		t.Errorf("pane is %d rows, want the header and %d of body", len(rows), m.diff.height)
	}
	for i, r := range rows {
		if w := lipgloss.Width(r); w != m.diff.width {
			t.Errorf("row %d is %d cells, want %d", i, w, m.diff.width)
		}
	}
	// One row of the body went to the horizontal bar, one column of each
	// remaining row to the vertical one.
	if got := m.diff.bodyRows(); got != m.diff.height-1 {
		t.Errorf("bodyRows = %d, want %d", got, m.diff.height-1)
	}
	if m.diff.codeWidthWith(true) != m.diff.codeWidthWith(false)-1 {
		t.Errorf("the vertical bar took %d columns from the code",
			m.diff.codeWidthWith(false)-m.diff.codeWidthWith(true))
	}
	// The last body row is the horizontal bar, not a line of diff.
	foot := plainText(rows[len(rows)-1])
	if !strings.ContainsAny(foot, thumbGlyph+horzTrack) {
		t.Errorf("the foot of the pane is not the horizontal bar: %q", foot)
	}
	// And the corner under the vertical bar is left blank.
	if !strings.HasSuffix(foot, " ") {
		t.Errorf("the corner should be blank: %q", foot)
	}
}

// crowdedModel opens on a working directory with more files than the unstaged
// list can show.
func crowdedModel(t *testing.T, n int) Model {
	t.Helper()
	repo := testRepo(t)
	for i := 0; i < n; i++ {
		writeFile(t, repo.Dir, fmt.Sprintf("file%03d.txt", i), "x\n")
	}
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 30},
		New(repo).rescanCmd()(),
	)
	return drive(t, m, altKey("1"))
}

// The lists wear the same bar as the diff, on the same terms — but with no
// track of their own, since the pane separator is already a rule down that
// edge.
func TestListScrollbarAppearsOnlyWhenNeeded(t *testing.T) {
	m := newTestModel(t)
	if m.workdir.scrolls() {
		t.Fatalf("a list of %d files in %d rows wants a bar", len(m.workdir.files), m.workdir.height)
	}
	for i, r := range strings.Split(m.workdir.view(m.st, true), "\n") {
		if strings.Contains(plainText(r), thumbGlyph) {
			t.Errorf("row %d has a thumb though the list fits: %q", i, plainText(r))
		}
	}

	m = crowdedModel(t, 60)
	if !m.workdir.scrolls() {
		t.Fatalf("a list of %d files in %d rows wants no bar", len(m.workdir.files), m.workdir.height)
	}
	rows := strings.Split(m.workdir.view(m.st, true), "\n")
	for i, r := range rows {
		if w := lipgloss.Width(r); w != m.workdir.width {
			t.Errorf("row %d is %d cells, want the pane's %d", i, w, m.workdir.width)
		}
	}
	edge := rightEdge(rows[1:])
	if strings.Contains(edge, vertTrack) {
		t.Errorf("the list drew a track beside the pane separator: %q", edge)
	}
	if !strings.HasPrefix(edge, thumbGlyph) {
		t.Errorf("at the top of the list the thumb should start at the top: %q", edge)
	}
	if strings.HasSuffix(edge, thumbGlyph) {
		t.Errorf("the thumb fills the track though most of the list is off screen: %q", edge)
	}

	m = drive(t, m, rkey("G"))
	edge = rightEdge(strings.Split(m.workdir.view(m.st, true), "\n")[1:])
	if !strings.HasSuffix(edge, thumbGlyph) {
		t.Errorf("at the end of the list the thumb should reach the bottom: %q", edge)
	}
	if strings.HasPrefix(edge, thumbGlyph) {
		t.Errorf("the thumb is still at the top at the end of the list: %q", edge)
	}
}

// The branch history is the same again, counting the working tree row.
func TestCommitListScrollbar(t *testing.T) {
	m := aheadModel(t, 2)
	if m.commits.scrolls() {
		t.Fatalf("two commits in %d rows want a bar", m.commits.height)
	}

	m = aheadModel(t, 40)
	if !m.commits.scrolls() {
		t.Fatalf("%d rows of history in %d want no bar", m.commits.rows(), m.commits.height)
	}
	rows := strings.Split(m.commits.view(m.st, true), "\n")
	for i, r := range rows {
		if w := lipgloss.Width(r); w != m.commits.width {
			t.Errorf("row %d is %d cells, want the pane's %d", i, w, m.commits.width)
		}
	}
	edge := rightEdge(rows[1:])
	if !strings.HasPrefix(edge, thumbGlyph) {
		t.Errorf("the history starts on the working tree, so the thumb starts at the top: %q", edge)
	}

	m = drive(t, m, altKey("5"), rkey("G"))
	edge = rightEdge(strings.Split(m.commits.view(m.st, true), "\n")[1:])
	if !strings.HasSuffix(edge, thumbGlyph) {
		t.Errorf("at the oldest commit the thumb should reach the bottom: %q", edge)
	}
}

// Growing the window pulls the view back down onto the rows, rather than
// leaving blank space under the last one.
func TestGrowingThePaneClosesTheGapUnderTheLastRow(t *testing.T) {
	m := drive(t, crowdedModel(t, 60), rkey("G"))
	if m.workdir.offset != m.workdir.maxOffset() {
		t.Fatalf("offset = %d, want the end of the list at %d", m.workdir.offset, m.workdir.maxOffset())
	}
	m = drive(t, m, tea.WindowSizeMsg{Width: 120, Height: 60})
	if m.workdir.offset > m.workdir.maxOffset() {
		t.Errorf("offset = %d, want no more than %d after the pane grew",
			m.workdir.offset, m.workdir.maxOffset())
	}
	rows := strings.Split(m.workdir.view(m.st, true), "\n")[1:]
	if last := plainText(rows[len(rows)-1]); strings.TrimSpace(last) == "" {
		t.Errorf("the last row is blank though files are scrolled off above it")
	}
}

func TestThumbSpan(t *testing.T) {
	for _, c := range []struct {
		name                 string
		track, total, window int
		offset, maxOffset    int
		wantLo, wantSize     int
	}{
		{"nothing to scroll", 10, 5, 10, 0, 0, 0, 10},
		{"at the top", 10, 100, 10, 0, 90, 0, 1},
		{"at the bottom", 10, 100, 10, 90, 90, 9, 1},
		{"one line down is not the top", 10, 100, 10, 1, 90, 1, 1},
		{"one line up is not the bottom", 10, 100, 10, 89, 90, 8, 1},
		{"half way", 10, 20, 10, 5, 10, 2, 5},
		{"no track", 0, 100, 10, 0, 90, 0, 0},
	} {
		lo, size := thumbSpan(c.track, c.total, c.window, c.offset, c.maxOffset)
		if lo != c.wantLo || size != c.wantSize {
			t.Errorf("%s: thumbSpan(%d, %d, %d, %d, %d) = %d, %d, want %d, %d",
				c.name, c.track, c.total, c.window, c.offset, c.maxOffset, lo, size, c.wantLo, c.wantSize)
		}
		if size > 0 && lo+size > c.track {
			t.Errorf("%s: thumb runs off the end of the track", c.name)
		}
	}
}

// The hint line offers the scroll while there is one, and says that the lists
// are behind it on a narrow window.
func TestScrollHint(t *testing.T) {
	m := wideDiffModel(t)
	if hint := m.hints(); !strings.Contains(hint, "scroll") {
		t.Errorf("hint = %q, want it to offer the scroll", hint)
	}
	if hint := diffModel(t).hints(); strings.Contains(hint, "scroll") {
		t.Errorf("hint = %q, want no scroll offered for a diff that fits", hint)
	}
	m = drive(t, m, tea.WindowSizeMsg{Width: 80, Height: 40})
	if !m.leftCollapsed() {
		t.Fatal("the left column should have folded away")
	}
	if hint := m.hints(); !strings.Contains(hint, "lists") {
		t.Errorf("hint = %q, want it to say the lists are still reachable", hint)
	}
}

// Space stages the selection rather than the whole hunk while visual mode is
// open, and the hunk when it is not.
func TestSpaceStagesTheVisualSelection(t *testing.T) {
	m := diffModel(t)
	repo := m.repo

	// Select the first added line only.
	m = cursorTo(t, m, '+')
	m = drive(t, m, rkey("V"))
	m = drive(t, m, rkey(" "))
	m = drive(t, m, New(repo).rescanCmd()())

	staged, err := repo.Git("diff", "--cached")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(staged, "+A") {
		t.Errorf("the selected line was not staged:\n%s", staged)
	}
	if strings.Contains(staged, "+B") || strings.Contains(staged, "+C") {
		t.Errorf("more than the selection was staged:\n%s", staged)
	}
	if m.diff.visual() {
		t.Error("visual mode should end once the selection is staged")
	}
}

func TestSpaceStagesTheHunkWhenNotSelecting(t *testing.T) {
	m := diffModel(t)
	repo := m.repo
	m = drive(t, m, rkey(" "))
	m = drive(t, m, New(repo).rescanCmd()())

	staged, err := repo.Git("diff", "--cached")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"+A", "+B", "+C"} {
		if !strings.Contains(staged, want) {
			t.Errorf("the whole hunk should be staged, missing %s:\n%s", want, staged)
		}
	}
}

// Staging redraws the pane off the rescan that follows it, and nothing else:
// the diff on screen was taken against a working tree that has since moved.
func TestStagingAHunkRedrawsTheDiff(t *testing.T) {
	m := diffModel(t)
	before := m.diff.shown
	m = drive(t, m, rkey(" "))

	if m.diff.shown == before {
		t.Error("the pane is still showing the diff taken before the hunk was staged")
	}
	for _, l := range m.diff.lines {
		if l.Kind == '+' || l.Kind == '-' {
			t.Errorf("the staged hunk is still in the unstaged diff: %q", l.Text)
			break
		}
	}
}

func TestLStagesJustTheCursorLine(t *testing.T) {
	m := diffModel(t)
	repo := m.repo
	m = cursorTo(t, m, '+')
	m = drive(t, m, rkey("L"))
	m = drive(t, m, New(repo).rescanCmd()())

	staged, err := repo.Git("diff", "--cached")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(staged, "+A") {
		t.Errorf("the cursor line was not staged:\n%s", staged)
	}
	if strings.Contains(staged, "+B") {
		t.Errorf("L staged more than one line:\n%s", staged)
	}
}

func TestAmendRefreshesTheStagedList(t *testing.T) {
	repo := testRepo(t)
	// Commit the staged file so the working tree is otherwise settled.
	if _, err := repo.Commit("second", false); err != nil {
		t.Fatal(err)
	}
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	if len(m.index.files) != 0 {
		t.Fatalf("nothing should be staged to begin with, got %v", m.index.files)
	}

	m = drive(t, m, rkey("A"))
	if !m.amend {
		t.Fatal("amend did not turn on")
	}
	// The commit being amended touched bar.txt, so it is now staged.
	var staged []string
	for _, f := range m.index.files {
		staged = append(staged, f.Path)
	}
	if len(staged) == 0 {
		t.Fatal("switching to amend did not refresh the staged list")
	}
	found := false
	for _, p := range staged {
		if p == "bar.txt" {
			found = true
		}
	}
	if !found {
		t.Errorf("staged list = %v, want it to contain bar.txt", staged)
	}

	// Switching back returns the lists to the plain view.
	m = drive(t, m, rkey("A"))
	if m.amend {
		t.Fatal("amend did not turn off")
	}
	if len(m.index.files) != 0 {
		t.Errorf("staged list = %v, want it empty again", m.index.files)
	}
}

// The diff shown for a staged file follows the same parent.
func TestAmendRefreshesTheStagedDiff(t *testing.T) {
	repo := testRepo(t)
	if _, err := repo.Commit("second", false); err != nil {
		t.Fatal(err)
	}
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m = drive(t, m, rkey("A"))
	m = drive(t, m, altKey("2"))
	if m.focus != focusIndex {
		t.Fatalf("focus = %v, want focusIndex", m.focus)
	}
	if m.diff.file == nil {
		t.Fatal("no diff loaded for the staged file")
	}
	var text []string
	for _, l := range m.diff.lines {
		text = append(text, l.Text)
	}
	joined := strings.Join(text, "\n")
	if !strings.Contains(joined, "beta") {
		t.Errorf("the amend diff should show the commit's own content:\n%s", joined)
	}
}

// Toggling amend only changes what the index is compared against. It must
// never touch the index itself, or work staged before the toggle would be
// silently rewritten.
func TestAmendToggleLeavesTheIndexAlone(t *testing.T) {
	repo := testRepo(t)
	if _, err := repo.Commit("second", false); err != nil {
		t.Fatal(err)
	}
	// Stage something new on top of that commit.
	writeFile(t, repo.Dir, "foo.txt", "one\nTHREE\nthree\nfour\nfive\n")
	if err := repo.Stage([]string{"foo.txt"}); err != nil {
		t.Fatal(err)
	}
	before, err := repo.Git("diff", "--cached", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(before, "+THREE") {
		t.Fatalf("setup failed, nothing staged:\n%s", before)
	}

	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m = drive(t, m, rkey("A"))
	m = drive(t, m, rkey("A"))
	if m.amend {
		t.Fatal("amend should be off again")
	}

	after, err := repo.Git("diff", "--cached", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("toggling amend changed the index:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

func TestNoCtrlCommandsOutsideTheBuffer(t *testing.T) {
	m := newTestModel(t)
	if m.focus == focusCommit {
		t.Fatal("the cursor should not start in the buffer")
	}
	for _, k := range []tea.KeyMsg{
		{Type: tea.KeyCtrlE},
		{Type: tea.KeyCtrlU},
		{Type: tea.KeyCtrlP},
		{Type: tea.KeyCtrlN},
		{Type: tea.KeyCtrlT},
	} {
		got := drive(t, m, k)
		if got.amend {
			t.Errorf("%v toggled amend", k)
		}
		if got.dialog != nil {
			t.Errorf("%v opened the %q dialog", k, got.dialog.title)
		}
		if got.quit {
			t.Errorf("%v quit", k)
		}
	}
}

func TestEveryCommandHasALetter(t *testing.T) {
	cases := []struct {
		name   string
		letter string
		setup  func(*testing.T, *git.Repo)
		check  func(Model) bool
	}{
		{"amend", "A", nil, func(m Model) bool { return m.amend }},
		{"sign off", "o", nil, func(m Model) bool { return strings.Contains(m.commit.Value(), "Signed-off-by:") }},
		{"push", "P", nil, func(m Model) bool { return m.dialog != nil && strings.Contains(m.dialog.title, "Push") }},
		{"merge", "M",
			// Merging needs somewhere to merge from.
			func(t *testing.T, r *git.Repo) {
				if _, err := r.Git("branch", "other"); err != nil {
					t.Fatal(err)
				}
			},
			func(m Model) bool { return m.dialog != nil && strings.Contains(m.dialog.title, "Merge") }},
		{"new branch", "N", nil, func(m Model) bool { return m.dialog != nil && strings.Contains(m.dialog.title, "Branch") }},
		{"checkout", "K", nil, func(m Model) bool { return m.dialog != nil && strings.Contains(m.dialog.title, "Checkout") }},
		{"revert", "v", nil, func(m Model) bool { return m.dialog != nil && strings.Contains(m.dialog.title, "Revert") }},
	}
	for _, c := range cases {
		build := func(t *testing.T) Model {
			t.Helper()
			repo := testRepo(t)
			if c.setup != nil {
				c.setup(t, repo)
			}
			return drive(t, New(repo),
				tea.WindowSizeMsg{Width: 120, Height: 40},
				New(repo).rescanCmd()(),
			)
		}
		t.Run(c.name+" letter", func(t *testing.T) {
			if m := drive(t, build(t), rkey(c.letter)); !c.check(m) {
				t.Errorf("the bare letter %q did not run %s", c.letter, c.name)
			}
		})

	}
}

func TestCursorAndSelectionUseTheEdgeBar(t *testing.T) {
	m := cursorTo(t, diffModel(t), '+')
	st := m.st

	// The bar column is always present, so rows never shift sideways.
	widths := map[int]bool{}
	for _, row := range strings.Split(m.View(), "\n") {
		widths[lipgloss.Width(row)] = true
	}
	if len(widths) != 1 {
		t.Errorf("rows have differing widths: %v", widths)
	}

	before := m.View()
	m = drive(t, m, rkey("V"), rkey("j"))
	after := m.View()
	if before == after {
		t.Fatal("selecting changed nothing on screen")
	}
	widths = map[int]bool{}
	for _, row := range strings.Split(after, "\n") {
		widths[lipgloss.Width(row)] = true
	}
	if len(widths) != 1 {
		t.Errorf("selecting changed row widths: %v", widths)
	}

	// The two bar colours are distinct, so cursor and selection read apart.
	if st.cursorBar.GetBackground() == st.selectBar.GetBackground() {
		t.Error("the cursor and selection bars are the same colour")
	}
	// And neither is a diff band colour, so they cannot be confused with one.
	for _, band := range []lipgloss.Style{st.diffAdd, st.diffAddEmph, st.diffDel, st.diffDelEmph} {
		if st.cursorBar.GetBackground() == band.GetBackground() {
			t.Error("the cursor bar shares a colour with a diff band")
		}
	}
}

// Bands must not leak into the file lists or the commit area.
func TestBandsAreConfinedToTheDiff(t *testing.T) {
	m := diffModel(t)
	view := strings.Split(m.View(), "\n")
	if len(view) < 4 {
		t.Fatal("view too short")
	}
	// The branch bar and the status bar carry no diff colours.
	for _, i := range []int{0, len(view) - 1} {
		for _, band := range []string{"48;5;22m", "48;5;52m", "48;5;28m", "48;5;88m"} {
			if strings.Contains(view[i], band) {
				t.Errorf("row %d has a diff band: %q", i, view[i])
			}
		}
	}
}

// Nothing puts the message on screen but Ctrl-Enter — not staging, not an
// amend, not a merge. The diff keeps the whole right column until you say you
// are writing a commit.
func TestMessageStaysHiddenUntilAskedFor(t *testing.T) {
	m := newTestModel(t)
	if len(m.index.files) == 0 {
		t.Fatal("the fixture should start with something staged")
	}
	if m.composeOpen || strings.Contains(m.View(), "Commit Message") {
		t.Errorf("the message is on screen with nothing having opened it:\n%s", m.View())
	}
	// An amend does not open it either, though it changes what it will hold.
	amending := drive(t, m, rkey("A"))
	if !amending.amend {
		t.Fatal("A did not turn on amend")
	}
	if amending.composeOpen {
		t.Error("amending opened the message on its own")
	}

	m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	if !m.composeOpen {
		t.Fatal("Ctrl-Enter did not open the message")
	}
	if !strings.Contains(m.View(), "Commit Message") {
		t.Errorf("the message is not on screen after Ctrl-Enter:\n%s", m.View())
	}
	if m.focus != focusCommit {
		t.Errorf("focus = %v, want the message", m.focus)
	}
	// It is ready to type into straight away.
	m = drive(t, m, rkey("h"), rkey("i"))
	if m.commit.Value() != "hi" {
		t.Errorf("buffer = %q, want the typing to have landed", m.commit.Value())
	}
}

// Esc puts it away and keeps what was typed; Tab only moves the cursor off it.
func TestEscClosesTheMessageAndTabDoesNot(t *testing.T) {
	m := drive(t, newTestModel(t), tea.KeyMsg{Type: tea.KeyCtrlJ})
	m.commit.SetValue("half written")

	tabbed := drive(t, m, tea.KeyMsg{Type: tea.KeyTab})
	if !tabbed.composeOpen {
		t.Error("Tab closed the message instead of leaving it open")
	}
	if tabbed.focus == focusCommit {
		t.Error("Tab did not move the cursor off the message")
	}

	m = drive(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.composeOpen {
		t.Error("Esc did not close the message")
	}
	if strings.Contains(m.View(), "Commit Message") {
		t.Errorf("the message is still on screen after Esc:\n%s", m.View())
	}
	if m.commit.Value() != "half written" {
		t.Errorf("buffer = %q, want the message kept", m.commit.Value())
	}
	// And reopening brings it back.
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	if m.commit.Value() != "half written" {
		t.Errorf("buffer = %q after reopening", m.commit.Value())
	}
}

// Alt-4 means "take me to the message", so it brings it up when it is not
// already there. Asking for the pane is asking for the pane.
func TestAltFourOpensTheMessage(t *testing.T) {
	m := newTestModel(t)
	if m.composeOpen {
		t.Fatal("the message should start closed")
	}
	m = drive(t, m, altKey("4"))
	if !m.composeOpen {
		t.Fatal("Alt-4 did not bring the message up")
	}
	if m.focus != focusCommit {
		t.Errorf("focus = %v, want the message", m.focus)
	}
	if !strings.Contains(m.View(), "Commit Message") {
		t.Errorf("the message is not on screen:\n%s", m.View())
	}
	if m.statusEr {
		t.Errorf("Alt-4 complained: %q", m.status)
	}

	// From there Ctrl-Enter is the press that asks, as it would be had
	// Ctrl-Enter opened it.
	m.commit.SetValue("opened with Alt-4")
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	if m.dialog == nil {
		t.Fatal("Ctrl-Enter from an Alt-4 opened message did not ask")
	}
	m = drive(t, m, rkey("y"))
	if got := m.repo.LastCommitMessage(); got != "opened with Alt-4" {
		t.Errorf("HEAD message = %q", got)
	}

	// Esc still puts it away, and Alt-4 brings it back.
	m = drive(t, m, altKey("4"), tea.KeyMsg{Type: tea.KeyEsc})
	if m.composeOpen {
		t.Fatal("Esc did not close it")
	}
	if m = drive(t, m, altKey("4")); !m.composeOpen {
		t.Error("Alt-4 did not reopen it after Esc")
	}
}

// Alt-5 does still refuse, because that pane has nothing to show when the
// branch has no commits of its own. The two are not the same case.
func TestAltFiveRefusesWithNoCommits(t *testing.T) {
	m := drive(t, newTestModel(t), altKey("5"))
	if m.focus == focusCommits {
		t.Error("Alt-5 focused a pane with nothing in it")
	}
	if !m.statusEr {
		t.Error("Alt-5 should say why it did nothing")
	}
}

// Tab steps over the message until it is opened, and through it after.
func TestTabSkipsTheMessageUntilItIsOpen(t *testing.T) {
	m := newTestModel(t)
	for i := 0; i < int(focusCount)+1; i++ {
		m = drive(t, m, tea.KeyMsg{Type: tea.KeyTab})
		if m.focus == focusCommit {
			t.Fatal("Tab landed on a message that is not on screen")
		}
	}

	m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	seen := false
	for i := 0; i < int(focusCount); i++ {
		m = drive(t, m, tea.KeyMsg{Type: tea.KeyTab})
		if m.focus == focusCommit {
			seen = true
		}
	}
	if !seen {
		t.Error("Tab never reached the message once it was open")
	}
}

// Once open the message sits under the diff: down reaches it, up leaves it.
func TestDiffAndMessageAreNeighbours(t *testing.T) {
	m := drive(t, diffModel(t), tea.KeyMsg{Type: tea.KeyCtrlJ}, tea.KeyMsg{Type: tea.KeyEsc})
	if m.composeOpen {
		t.Fatal("the message should be closed to begin with")
	}
	m = drive(t, m, altKey("3"))
	m.diff.moveTo(len(m.diff.lines) - 1)
	m = drive(t, m, rkey("j"))
	if m.focus != focusDiff {
		t.Errorf("focus = %v; with the message closed there is nothing below the diff", m.focus)
	}

	// Open it and the diff gains a neighbour.
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ}, altKey("3"))
	m.diff.moveTo(len(m.diff.lines) - 1)
	m = drive(t, m, rkey("j"))
	if m.focus != focusCommit {
		t.Fatalf("focus = %v, want the message", m.focus)
	}
	// j is text in there, not a move.
	m = drive(t, m, rkey("j"))
	if m.focus != focusCommit || m.commit.Value() != "j" {
		t.Errorf("j moved instead of typing: focus %v, buffer %q", m.focus, m.commit.Value())
	}
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlA}, tea.KeyMsg{Type: tea.KeyCtrlK})
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if m.focus != focusDiff {
		t.Errorf("focus = %v, want up from the first line to reach the diff", m.focus)
	}
}

// Ctrl-Enter escalates one step per press: open the message, put the cursor
// back in it if it has wandered, then ask. Nothing commits without a y.
func TestCtrlEnterEscalatesOneStepPerPress(t *testing.T) {
	m := newTestModel(t)
	repo := m.repo

	// 1. opens and focuses.
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	if !m.composeOpen || m.focus != focusCommit {
		t.Fatalf("first press: open=%v focus=%v", m.composeOpen, m.focus)
	}
	if m.dialog != nil {
		t.Fatal("the first press asked to commit")
	}
	m.commit.SetValue("a real commit")

	// 2. from another pane it only brings the cursor back.
	away := drive(t, m, altKey("1"), tea.KeyMsg{Type: tea.KeyCtrlJ})
	if away.dialog != nil {
		t.Error("Ctrl-Enter from another pane asked to commit rather than returning")
	}
	if away.focus != focusCommit {
		t.Errorf("focus = %v, want it back in the message", away.focus)
	}
	if got := repo.LastCommitMessage(); got == "a real commit" {
		t.Fatal("something committed on the way")
	}

	// 3. from the message itself it asks.
	m = drive(t, away, tea.KeyMsg{Type: tea.KeyCtrlJ})
	if m.dialog == nil {
		t.Fatal("the press from the message did not ask")
	}
	if !strings.Contains(m.View(), "yes") || !strings.Contains(m.View(), "no") {
		t.Errorf("the question should offer y/n:\n%s", m.View())
	}
	if got := repo.LastCommitMessage(); got == "a real commit" {
		t.Fatal("the commit was made before it was confirmed")
	}

	// n backs out with the message intact and still on screen.
	back := drive(t, m, rkey("n"))
	if back.dialog != nil {
		t.Error("n left the question up")
	}
	if !back.composeOpen || back.commit.Value() != "a real commit" {
		t.Errorf("n lost the message: open=%v %q", back.composeOpen, back.commit.Value())
	}

	// y commits, empties the buffer and puts it away.
	m = drive(t, m, rkey("y"))
	if got := repo.LastCommitMessage(); got != "a real commit" {
		t.Errorf("HEAD message = %q", got)
	}
	if m.commit.Value() != "" {
		t.Errorf("the buffer kept %q after committing", m.commit.Value())
	}
	if m.composeOpen {
		t.Error("the message stayed on screen after committing")
	}
	if m.focus == focusCommit {
		t.Error("focus stayed on a message that is no longer there")
	}
	if _, ok := stateFile(t, repo, git.MsgFile); ok {
		t.Error("the stashed message should be gone once committed")
	}
}

// Ctrl-Enter starts the sequence from any pane, since every other key in the
// message is text and this is the one that has to reach it.
func TestCtrlEnterOpensFromAnyPane(t *testing.T) {
	for _, pane := range []struct {
		name string
		key  tea.KeyMsg
	}{
		{"unstaged", altKey("1")},
		{"staged", altKey("2")},
		{"diff", altKey("3")},
	} {
		t.Run(pane.name, func(t *testing.T) {
			m := drive(t, newTestModel(t), pane.key, tea.KeyMsg{Type: tea.KeyCtrlJ})
			if !m.composeOpen || m.focus != focusCommit {
				t.Fatalf("from %s: open=%v focus=%v", pane.name, m.composeOpen, m.focus)
			}
			m.commit.SetValue("from " + pane.name)
			m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
			if m.dialog == nil {
				t.Fatal("the second press did not ask")
			}
			m = drive(t, m, rkey("y"))
			if got := m.repo.LastCommitMessage(); got != "from "+pane.name {
				t.Errorf("HEAD message = %q", got)
			}
		})
	}
}

// The refusals git-gui makes are reported in the status bar rather than as a
// dialog, and an empty message puts the cursor where it has to be typed.
func TestCommitRefusals(t *testing.T) {
	t.Run("empty message keeps the cursor in it", func(t *testing.T) {
		m := drive(t, newTestModel(t), tea.KeyMsg{Type: tea.KeyCtrlJ})
		m.commit.SetValue("   ")
		m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
		if m.dialog != nil {
			t.Errorf("a dialog was raised: %q / %q", m.dialog.title, m.dialog.body)
		}
		if !m.statusEr || !strings.Contains(m.status, "Enter a commit message") {
			t.Errorf("status = %q (error=%v), want the refusal in the status bar",
				m.status, m.statusEr)
		}
		if m.focus != focusCommit {
			t.Errorf("focus = %v, want the message buffer", m.focus)
		}
		if !m.composeOpen {
			t.Error("the refusal closed the message")
		}
		// And it is ready to type into, with the refusal clearing as you do.
		m = drive(t, m, rkey("f"), rkey("i"), rkey("x"))
		if m.commit.Value() != "   fix" {
			t.Errorf("buffer = %q, want the typing to have landed", m.commit.Value())
		}
		if m.statusEr {
			t.Error("the refusal outlived the next keystroke")
		}
	})

	t.Run("nothing staged says so and stays put", func(t *testing.T) {
		repo := testRepo(t)
		if err := repo.Unstage([]string{"bar.txt"}, repo.StatusParent(false)); err != nil {
			t.Fatal(err)
		}
		m := drive(t, New(repo),
			tea.WindowSizeMsg{Width: 120, Height: 40},
			New(repo).rescanCmd()(),
		)
		// The first press opens the message even with nothing staged; the
		// second is where it refuses.
		m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
		m.commit.SetValue("a message")
		m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
		if m.dialog != nil {
			t.Errorf("a dialog was raised: %q / %q", m.dialog.title, m.dialog.body)
		}
		if !strings.Contains(m.status, "Nothing staged") {
			t.Errorf("status = %q, want the nothing-staged refusal", m.status)
		}
	})

	t.Run("nothing staged beats an empty message", func(t *testing.T) {
		repo := testRepo(t)
		if err := repo.Unstage([]string{"bar.txt"}, repo.StatusParent(false)); err != nil {
			t.Fatal(err)
		}
		m := drive(t, New(repo),
			tea.WindowSizeMsg{Width: 120, Height: 40},
			New(repo).rescanCmd()(),
		)
		if !m.messageEmpty() {
			t.Fatal("the buffer should start empty")
		}
		m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ}, tea.KeyMsg{Type: tea.KeyCtrlJ})
		if !strings.Contains(m.status, "Nothing staged") {
			t.Errorf("status = %q; with both wrong, the one you can act on wins", m.status)
		}
	})

	// The status bar is one line, so a refusal has to fit on it.
	t.Run("refusals fit the status bar", func(t *testing.T) {
		m := newTestModel(t)
		m.commit.SetValue("")
		if p := m.commitProblem(); len(p) > 60 {
			t.Errorf("refusal is %d characters: %q", len(p), p)
		}
	})
}

// The buffer is a full text field: letters are text and readline keys edit.
func TestCommitBufferIsATextField(t *testing.T) {
	m := drive(t, newTestModel(t), tea.KeyMsg{Type: tea.KeyCtrlJ})
	typed := "sauveoRPMNKcVD"
	for _, r := range typed {
		m = drive(t, m, rkey(string(r)))
	}
	if m.commit.Value() != typed {
		t.Errorf("buffer = %q, want %q", m.commit.Value(), typed)
	}
	if m.dialog != nil {
		t.Fatalf("typing opened the %q dialog", m.dialog.title)
	}
	if m.amend {
		t.Error("typing e toggled amend")
	}

	// Ctrl-A then Ctrl-K clears the line, as readline would.
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlA}, tea.KeyMsg{Type: tea.KeyCtrlK})
	if m.commit.Value() != "" {
		t.Errorf("buffer = %q, want Ctrl-A Ctrl-K to have cleared it", m.commit.Value())
	}
}

// Sign off and amend both feed the buffer, and show up in it.
func TestSignOffAndAmendFeedTheBuffer(t *testing.T) {
	m := drive(t, newTestModel(t), rkey("o"))
	if !strings.Contains(m.commit.Value(), "Signed-off-by:") {
		t.Errorf("buffer = %q, want the sign off", m.commit.Value())
	}
	// It is in the buffer whether or not the buffer is on screen, and shows up
	// when Ctrl-Enter brings it up.
	if strings.Contains(m.View(), "Signed-off-by:") {
		t.Errorf("the message is on screen without having been opened:\n%s", m.View())
	}
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	if !strings.Contains(m.View(), "Signed-off-by:") {
		t.Errorf("the sign off is not visible once opened:\n%s", m.View())
	}
}

// recordingSystem is the real one with the editor stubbed out: a test must not
// hand the terminal to vi, but it can check which file would have been opened.
type recordingSystem struct {
	git.Local
	mu     sync.Mutex
	edited []string
}

func (s *recordingSystem) Editor(dir, path string) git.Editor {
	s.mu.Lock()
	s.edited = append(s.edited, path)
	s.mu.Unlock()
	return noopEditor{}
}

type noopEditor struct{}

func (noopEditor) Run() error          { return nil }
func (noopEditor) SetStdin(io.Reader)  {}
func (noopEditor) SetStdout(io.Writer) {}
func (noopEditor) SetStderr(io.Writer) {}
