package git

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testRepo builds a throwaway repository with one committed file.
func testRepo(t *testing.T) *Repo {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.name", "Test"},
		{"config", "user.email", "test@example.com"},
		{"config", "commit.gpgsign", "false"},
	} {
		if _, err := run(dir, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	write(t, dir, "foo.txt", "one\ntwo\nthree\nfour\nfive\n")
	if _, err := run(dir, "add", "foo.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := run(dir, "commit", "-q", "-m", "initial"); err != nil {
		t.Fatal(err)
	}
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func read(t *testing.T, r *Repo, name string) string {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(r.Dir, name))
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// stateOf finds a path in the scan result.
func stateOf(t *testing.T, r *Repo, path string) State {
	t.Helper()
	files, err := r.Status(r.StatusParent(false))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range files {
		if f.Path == path {
			return f.State
		}
	}
	return "__"
}

func TestStatusStates(t *testing.T) {
	r := testRepo(t)

	write(t, r.Dir, "foo.txt", "one\nTWO\nthree\nfour\nfive\n")
	write(t, r.Dir, "new.txt", "brand new\n")

	if got := stateOf(t, r, "foo.txt"); got != "_M" {
		t.Errorf("foo.txt state = %s, want _M", got)
	}
	if got := stateOf(t, r, "new.txt"); got != "_O" {
		t.Errorf("new.txt state = %s, want _O", got)
	}

	if err := r.Stage([]string{"foo.txt"}); err != nil {
		t.Fatal(err)
	}
	if got := stateOf(t, r, "foo.txt"); got != "M_" {
		t.Errorf("after stage, foo.txt state = %s, want M_", got)
	}

	// Modify again: partially staged, so it shows up in both lists.
	write(t, r.Dir, "foo.txt", "one\nTWO\nthree\nFOUR\nfive\n")
	got := stateOf(t, r, "foo.txt")
	if got != "MM" {
		t.Errorf("foo.txt state = %s, want MM", got)
	}
	if !got.Staged() || !got.Unstaged() {
		t.Error("MM should appear in both file lists")
	}

	if err := r.Unstage([]string{"foo.txt"}, "HEAD"); err != nil {
		t.Fatal(err)
	}
	if got := stateOf(t, r, "foo.txt"); got != "_M" {
		t.Errorf("after unstage, foo.txt state = %s, want _M", got)
	}

	// Deletion.
	if err := os.Remove(filepath.Join(r.Dir, "foo.txt")); err != nil {
		t.Fatal(err)
	}
	if got := stateOf(t, r, "foo.txt"); got != "_D" {
		t.Errorf("after delete, foo.txt state = %s, want _D", got)
	}
}

func TestDiffUntracked(t *testing.T) {
	r := testRepo(t)
	write(t, r.Dir, "new.txt", "hello\nworld\n")
	f := File{Path: "new.txt", State: "_O"}
	out, err := r.Diff(f, SideWorkdir, 3, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "+hello") || !strings.Contains(out, "+world") {
		t.Errorf("untracked diff should show the whole file as additions:\n%s", out)
	}
}

func TestDiffContextLines(t *testing.T) {
	r := testRepo(t)
	write(t, r.Dir, "foo.txt", "one\ntwo\nthree\nFOUR\nfive\n")
	f := File{Path: "foo.txt", State: "_M"}

	narrow, err := r.Diff(f, SideWorkdir, 1, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	wide, err := r.Diff(f, SideWorkdir, 5, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if len(ParsePatch(narrow).Hunks[0].Lines) >= len(ParsePatch(wide).Hunks[0].Lines) {
		t.Error("more context should produce a longer hunk")
	}
}

// TestStageHunk exercises the path the diff pane uses: parse a diff, rebuild a
// single-hunk patch and apply it to the index.
func TestStageHunk(t *testing.T) {
	r := testRepo(t)
	// Two separate changes, far enough apart to land in different hunks.
	write(t, r.Dir, "foo.txt", "ONE\ntwo\nthree\nfour\nFIVE\n")
	f := File{Path: "foo.txt", State: "_M"}

	raw, err := r.Diff(f, SideWorkdir, 1, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	p := ParsePatch(raw)
	if len(p.Hunks) != 2 {
		t.Fatalf("expected 2 hunks, got %d:\n%s", len(p.Hunks), raw)
	}

	patch, ok := p.HunkPatch(0)
	if !ok {
		t.Fatal("HunkPatch(0) failed")
	}
	if err := r.ApplyPatch(patch, true, false); err != nil {
		t.Fatalf("stage hunk: %v\npatch:\n%s", err, patch)
	}

	// The index now has the first change only.
	staged, err := r.Git("diff", "--cached")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(staged, "+ONE") {
		t.Errorf("first hunk was not staged:\n%s", staged)
	}
	if strings.Contains(staged, "+FIVE") {
		t.Errorf("second hunk should not be staged:\n%s", staged)
	}
	if got := stateOf(t, r, "foo.txt"); got != "MM" {
		t.Errorf("state after staging one hunk = %s, want MM", got)
	}
}

func TestUnstageHunkReverse(t *testing.T) {
	r := testRepo(t)
	write(t, r.Dir, "foo.txt", "ONE\ntwo\nthree\nfour\nFIVE\n")
	if err := r.Stage([]string{"foo.txt"}); err != nil {
		t.Fatal(err)
	}

	f := File{Path: "foo.txt", State: "M_"}
	raw, err := r.Diff(f, SideIndex, 1, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	p := ParsePatch(raw)
	if len(p.Hunks) != 2 {
		t.Fatalf("expected 2 hunks, got %d:\n%s", len(p.Hunks), raw)
	}
	patch, _ := p.HunkPatch(1)
	if err := r.ApplyPatch(patch, true, true); err != nil {
		t.Fatalf("unstage hunk: %v\npatch:\n%s", err, patch)
	}

	staged, err := r.Git("diff", "--cached")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(staged, "+ONE") {
		t.Errorf("first hunk should still be staged:\n%s", staged)
	}
	if strings.Contains(staged, "+FIVE") {
		t.Errorf("second hunk should have been unstaged:\n%s", staged)
	}
	// The working tree is untouched by an index-only unstage.
	if got := read(t, r, "foo.txt"); !strings.Contains(got, "FIVE") {
		t.Errorf("working tree was modified by unstage: %q", got)
	}
}

// TestStageLines checks that a single added line can be staged out of a hunk
// that contains several.
func TestStageLines(t *testing.T) {
	r := testRepo(t)
	write(t, r.Dir, "foo.txt", "one\ntwo\nA\nB\nthree\nfour\nfive\n")
	f := File{Path: "foo.txt", State: "_M"}

	raw, err := r.Diff(f, SideWorkdir, 3, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	p := ParsePatch(raw)
	if len(p.Hunks) != 1 {
		t.Fatalf("expected 1 hunk, got %d:\n%s", len(p.Hunks), raw)
	}

	// Find the body index of the "+A" line and stage only that one.
	idx := -1
	for i, l := range p.Hunks[0].Lines {
		if l == "+A" {
			idx = i
		}
	}
	if idx < 0 {
		t.Fatalf("no +A line in hunk:\n%s", raw)
	}
	patch, ok := p.LinePatch(0, map[int]bool{idx: true}, false)
	if !ok {
		t.Fatal("LinePatch failed")
	}
	if err := r.ApplyPatch(patch, true, false); err != nil {
		t.Fatalf("stage line: %v\npatch:\n%s", err, patch)
	}

	staged, err := r.Git("diff", "--cached")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(staged, "+A") {
		t.Errorf("line A was not staged:\n%s", staged)
	}
	if strings.Contains(staged, "+B") {
		t.Errorf("line B should not have been staged:\n%s", staged)
	}
}

func TestRevertHunk(t *testing.T) {
	r := testRepo(t)
	write(t, r.Dir, "foo.txt", "ONE\ntwo\nthree\nfour\nFIVE\n")
	f := File{Path: "foo.txt", State: "_M"}

	raw, err := r.Diff(f, SideWorkdir, 1, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	p := ParsePatch(raw)
	patch, _ := p.HunkPatch(0)
	// Revert Hunk: apply backwards to the working tree, not the index.
	if err := r.ApplyPatch(patch, false, true); err != nil {
		t.Fatalf("revert hunk: %v\npatch:\n%s", err, patch)
	}
	got := read(t, r, "foo.txt")
	if strings.Contains(got, "ONE") {
		t.Errorf("first hunk should have been reverted: %q", got)
	}
	if !strings.Contains(got, "FIVE") {
		t.Errorf("second hunk should be untouched: %q", got)
	}
}

func TestCommitAndAmend(t *testing.T) {
	r := testRepo(t)
	write(t, r.Dir, "foo.txt", "one\nTWO\nthree\nfour\nfive\n")
	if err := r.Stage([]string{"foo.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("second commit", false); err != nil {
		t.Fatal(err)
	}
	if got := r.LastCommitMessage(); got != "second commit" {
		t.Errorf("LastCommitMessage = %q", got)
	}

	if _, err := r.Commit("second commit, reworded", true); err != nil {
		t.Fatal(err)
	}
	if got := r.LastCommitMessage(); got != "second commit, reworded" {
		t.Errorf("after amend, LastCommitMessage = %q", got)
	}
	count, err := r.Git("rev-list", "--count", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if strings.TrimSpace(count) != "2" {
		t.Errorf("amend created a new commit: rev-list count = %s", strings.TrimSpace(count))
	}
}

func TestStageChangedSkipsUntracked(t *testing.T) {
	r := testRepo(t)
	write(t, r.Dir, "foo.txt", "one\nTWO\nthree\nfour\nfive\n")
	write(t, r.Dir, "new.txt", "untracked\n")

	if err := r.StageChanged(); err != nil {
		t.Fatal(err)
	}
	if got := stateOf(t, r, "foo.txt"); got != "M_" {
		t.Errorf("tracked change should be staged, got %s", got)
	}
	if got := stateOf(t, r, "new.txt"); got != "_O" {
		t.Errorf("untracked file should be left alone, got %s", got)
	}
}

func TestUnstageWithNoHEAD(t *testing.T) {
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.name", "Test"},
		{"config", "user.email", "test@example.com"},
	} {
		if _, err := run(dir, args...); err != nil {
			t.Fatal(err)
		}
	}
	write(t, dir, "a.txt", "hi\n")
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if r.HasHEAD() {
		t.Fatal("fresh repo should have no HEAD")
	}
	if err := r.Stage([]string{"a.txt"}); err != nil {
		t.Fatal(err)
	}
	if got := stateOf(t, r, "a.txt"); got != "A_" {
		t.Errorf("state = %s, want A_", got)
	}
	// The diff for the initial commit must work without a HEAD.
	out, err := r.Diff(File{Path: "a.txt", State: "A_"}, SideIndex, 3, r.StatusParent(false))
	if err != nil {
		t.Fatalf("staged diff without HEAD: %v", err)
	}
	if !strings.Contains(out, "+hi") {
		t.Errorf("initial staged diff:\n%s", out)
	}
	// Unstaging without a HEAD has to fall back to rm --cached.
	if err := r.Unstage([]string{"a.txt"}, r.StatusParent(false)); err != nil {
		t.Fatalf("unstage without HEAD: %v", err)
	}
	if got := stateOf(t, r, "a.txt"); got != "_O" {
		t.Errorf("after unstage, state = %s, want _O", got)
	}
}

func TestMergeConflictState(t *testing.T) {
	r := testRepo(t)
	if _, err := r.Git("checkout", "-q", "-b", "other"); err != nil {
		t.Fatal(err)
	}
	write(t, r.Dir, "foo.txt", "one\nOTHER\nthree\nfour\nfive\n")
	if err := r.Stage([]string{"foo.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("other side", false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Git("checkout", "-q", "main"); err != nil {
		t.Fatal(err)
	}
	write(t, r.Dir, "foo.txt", "one\nMAIN\nthree\nfour\nfive\n")
	if err := r.Stage([]string{"foo.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("main side", false); err != nil {
		t.Fatal(err)
	}

	if _, err := r.Merge("other"); err == nil {
		t.Fatal("expected the merge to conflict")
	}
	if !r.IsMerging() {
		t.Error("IsMerging should be true during a conflicted merge")
	}
	got := stateOf(t, r, "foo.txt")
	if !got.Unmerged() {
		t.Errorf("state = %s, want an unmerged state", got)
	}
	if !got.Unstaged() {
		t.Error("conflicted files belong in the unstaged list")
	}
	if got.Staged() {
		t.Error("conflicted files must not appear in the staged list")
	}
	if r.MergeMessage() == "" {
		t.Error("MergeMessage should be populated during a merge")
	}
}

func TestSignOff(t *testing.T) {
	r := testRepo(t)
	got, err := r.SignOff()
	if err != nil {
		t.Fatal(err)
	}
	if got != "Signed-off-by: Test <test@example.com>" {
		t.Errorf("SignOff = %q", got)
	}
}

// While amending, "staged" means everything the rewritten commit will contain,
// not just what was staged on top of it. git-gui gets this by scanning against
// HEAD's parent rather than HEAD.
func TestStatusWhileAmending(t *testing.T) {
	r := testRepo(t)
	// A second commit that touches two files.
	write(t, r.Dir, "foo.txt", "one\nTWO\nthree\nfour\nfive\n")
	write(t, r.Dir, "added.txt", "added by the commit\n")
	if err := r.Stage([]string{"foo.txt", "added.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("second", false); err != nil {
		t.Fatal(err)
	}

	// Nothing is staged on top of it.
	normal, err := r.Status(r.StatusParent(false))
	if err != nil {
		t.Fatal(err)
	}
	for _, f := range normal {
		if f.State.Staged() {
			t.Errorf("%s shows as staged against HEAD: %s", f.Path, f.State)
		}
	}

	// Against the amend parent, the commit's own contents are staged.
	amending, err := r.Status(r.StatusParent(true))
	if err != nil {
		t.Fatal(err)
	}
	staged := map[string]State{}
	for _, f := range amending {
		if f.State.Staged() {
			staged[f.Path] = f.State
		}
	}
	if got, ok := staged["foo.txt"]; !ok || got.Index() != 'M' {
		t.Errorf("foo.txt while amending = %s (present=%v), want a staged modification", got, ok)
	}
	if got, ok := staged["added.txt"]; !ok || got.Index() != 'A' {
		t.Errorf("added.txt while amending = %s (present=%v), want a staged addition", got, ok)
	}
}

// The staged diff while amending has to be taken against the same parent.
func TestDiffWhileAmending(t *testing.T) {
	r := testRepo(t)
	write(t, r.Dir, "foo.txt", "one\nTWO\nthree\nfour\nfive\n")
	if err := r.Stage([]string{"foo.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("second", false); err != nil {
		t.Fatal(err)
	}

	f := File{Path: "foo.txt", State: "M_"}
	against, err := r.Diff(f, SideIndex, 3, r.StatusParent(true))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(against, "+TWO") {
		t.Errorf("the amend diff should show the commit's own change:\n%s", against)
	}

	// Against HEAD it is empty, which is what made the pane look wrong.
	plain, err := r.Diff(f, SideIndex, 3, r.StatusParent(false))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(plain, "+TWO") {
		t.Errorf("against HEAD the file should show no staged change:\n%s", plain)
	}
}

// Unstaging while amending drops the file out of the rewritten commit,
// restoring the parent's version rather than the one being rewritten.
func TestUnstageWhileAmending(t *testing.T) {
	r := testRepo(t)
	write(t, r.Dir, "foo.txt", "one\nTWO\nthree\nfour\nfive\n")
	write(t, r.Dir, "added.txt", "added by the commit\n")
	if err := r.Stage([]string{"foo.txt", "added.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("second", false); err != nil {
		t.Fatal(err)
	}

	parent := r.StatusParent(true)
	if err := r.Unstage([]string{"foo.txt"}, parent); err != nil {
		t.Fatal(err)
	}
	// foo.txt is back to the parent's version in the index, so committing the
	// amend would no longer carry that change.
	staged, err := r.Git("diff", "--cached", parent)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(staged, "+TWO") {
		t.Errorf("foo.txt should have dropped out of the amended commit:\n%s", staged)
	}
	if !strings.Contains(staged, "added by the commit") {
		t.Errorf("added.txt should still be part of the amended commit:\n%s", staged)
	}

	// Amending now really does drop the change.
	if _, err := r.Commit("second, without foo", true); err != nil {
		t.Fatal(err)
	}
	head, err := r.Git("show", "--stat", "--format=", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(head, "foo.txt") {
		t.Errorf("the amended commit still touches foo.txt:\n%s", head)
	}
	if !strings.Contains(head, "added.txt") {
		t.Errorf("the amended commit lost added.txt:\n%s", head)
	}
}

func TestStatusParentPicksTheRightRevision(t *testing.T) {
	r := testRepo(t)
	if got := r.StatusParent(false); got != "HEAD" {
		t.Errorf("StatusParent(false) = %q, want HEAD", got)
	}
	// The repo has a single commit, so amending it compares against nothing.
	if got := r.StatusParent(true); got != EmptyTree {
		t.Errorf("amending the initial commit = %q, want the empty tree", got)
	}
	write(t, r.Dir, "foo.txt", "changed\n")
	if err := r.Stage([]string{"foo.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("second", false); err != nil {
		t.Fatal(err)
	}
	first, err := r.Git("rev-parse", "HEAD^")
	if err != nil {
		t.Fatal(err)
	}
	if got := r.StatusParent(true); got != strings.TrimSpace(first) {
		t.Errorf("StatusParent(true) = %q, want %q", got, strings.TrimSpace(first))
	}
}
