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
		if _, err := (Local{}).Git(dir, nil, args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	write(t, dir, "foo.txt", "one\ntwo\nthree\nfour\nfive\n")
	if _, err := (Local{}).Git(dir, nil, "add", "foo.txt"); err != nil {
		t.Fatal(err)
	}
	if _, err := (Local{}).Git(dir, nil, "commit", "-q", "-m", "initial"); err != nil {
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
		if _, err := (Local{}).Git(dir, nil, args...); err != nil {
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

// Rescan answers in one call everything the display used to ask for one
// command at a time, and hands back the parent it used so the diffs that
// follow do not have to work it out again.
func TestRescanGathersTheWholeScan(t *testing.T) {
	r := testRepo(t)
	write(t, r.Dir, "foo.txt", "one\nTWO\nthree\nfour\nfive\n")
	write(t, r.Dir, "new.txt", "brand new\n")

	scan, err := r.Rescan(false)
	if err != nil {
		t.Fatal(err)
	}
	if scan.Parent != "HEAD" {
		t.Errorf("parent = %q, want HEAD", scan.Parent)
	}
	if !scan.HasHEAD {
		t.Error("HasHEAD is false in a repository with a commit")
	}
	if scan.Branch != "main" {
		t.Errorf("branch = %q, want main", scan.Branch)
	}
	if scan.Merging || scan.Merge != "" {
		t.Errorf("merging = %v, message = %q", scan.Merging, scan.Merge)
	}
	got := map[string]State{}
	for _, f := range scan.Files {
		got[f.Path] = f.State
	}
	if got["foo.txt"] != "_M" || got["new.txt"] != "_O" {
		t.Errorf("files = %v", got)
	}

	// And amending measures the index against HEAD's own parent, exactly as
	// StatusParent does for a caller that asks separately.
	amending, err := r.Rescan(true)
	if err != nil {
		t.Fatal(err)
	}
	if want := r.StatusParent(true); amending.Parent != want {
		t.Errorf("amending parent = %q, want %q", amending.Parent, want)
	}
}

// withRemote gives a repository an origin holding its current history, so the
// merge base has something to measure against.
func withRemote(t *testing.T, r *Repo, setHead bool) {
	t.Helper()
	bare := t.TempDir()
	if _, err := (Local{}).Git(bare, nil, "init", "-q", "--bare", "-b", "main", "."); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"remote", "add", "origin", bare},
		{"push", "-q", "origin", "main"},
	} {
		if _, err := r.Git(args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}
	if setHead {
		if _, err := r.Git("remote", "set-head", "origin", "main"); err != nil {
			t.Fatal(err)
		}
	}
}

// The merge base is where the branch left the remote's default branch, and
// ahead/behind is how far each has moved since.
func TestMergeBaseMeasuresAgainstTheRemoteDefault(t *testing.T) {
	r := testRepo(t)
	withRemote(t, r, true)

	forked, err := r.Git("rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	forked = strings.TrimSpace(forked)

	base, ok := r.MergeBase()
	if !ok {
		t.Fatal("no merge base against a pushed branch")
	}
	if base.Ref != "origin/main" {
		t.Errorf("ref = %q, want origin/main", base.Ref)
	}
	if !strings.HasPrefix(forked, base.Commit) {
		t.Errorf("commit = %q, want a prefix of %q", base.Commit, forked)
	}
	if base.Ahead != 0 || base.Behind != 0 {
		t.Errorf("a just-pushed branch is ↑%d ↓%d, want ↑0 ↓0", base.Ahead, base.Behind)
	}

	// Two commits on top of what was pushed, and the base stays put.
	for _, body := range []string{"local one\n", "local two\n"} {
		write(t, r.Dir, "foo.txt", body)
		if err := r.Stage([]string{"foo.txt"}); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Commit("ahead", false); err != nil {
			t.Fatal(err)
		}
	}
	base, ok = r.MergeBase()
	if !ok {
		t.Fatal("no merge base after committing")
	}
	if !strings.HasPrefix(forked, base.Commit) {
		t.Errorf("the base moved to %q; it should stay at the fork %q", base.Commit, forked)
	}
	if base.Ahead != 2 || base.Behind != 0 {
		t.Errorf("↑%d ↓%d, want ↑2 ↓0", base.Ahead, base.Behind)
	}
}

// Behind counts what the remote branch has that HEAD does not.
func TestMergeBaseCountsBehind(t *testing.T) {
	r := testRepo(t)
	withRemote(t, r, true)

	// Move origin/main on without moving HEAD, the way a fetch would.
	write(t, r.Dir, "foo.txt", "remote work\n")
	if err := r.Stage([]string{"foo.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("upstream", false); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Git("push", "-q", "origin", "main"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Git("reset", "-q", "--hard", "HEAD^"); err != nil {
		t.Fatal(err)
	}

	base, ok := r.MergeBase()
	if !ok {
		t.Fatal("no merge base")
	}
	if base.Ahead != 0 || base.Behind != 1 {
		t.Errorf("↑%d ↓%d, want ↑0 ↓1", base.Ahead, base.Behind)
	}
}

// What the branch tracks wins over the remote's default branch. This is the
// shape you get from adding a second remote by hand and pointing the branch at
// it: origin still has a recorded HEAD, but the branch was told to build on
// something else, and that is what git status reports too.
func TestMergeBaseFollowsTheUpstream(t *testing.T) {
	r := testRepo(t)
	withRemote(t, r, true) // origin, with origin/HEAD -> origin/main

	fork := t.TempDir()
	if _, err := (Local{}).Git(fork, nil, "init", "-q", "--bare", "-b", "main", "."); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{
		{"remote", "add", "fork", fork},
		{"push", "-q", "fork", "main"},
		{"branch", "--set-upstream-to", "fork/main", "main"},
	} {
		if _, err := r.Git(args...); err != nil {
			t.Fatalf("git %v: %v", args, err)
		}
	}

	base, ok := r.MergeBase()
	if !ok {
		t.Fatal("no merge base against the upstream")
	}
	if base.Ref != "fork/main" {
		t.Errorf("ref = %q, want fork/main — the branch tracks it, "+
			"even though origin/HEAD names origin/main", base.Ref)
	}
	// And it agrees with what the rest of git reports for the same branch.
	out, err := r.Git("rev-parse", "--abbrev-ref", "@{upstream}")
	if err != nil {
		t.Fatal(err)
	}
	if want := strings.TrimSpace(out); base.Ref != want {
		t.Errorf("ref = %q, but git says the upstream is %q", base.Ref, want)
	}
}

// A branch that tracks nothing falls back to the remote's default branch, so
// an unpushed branch still shows where it left trunk.
func TestMergeBaseFallsBackWhenNothingIsTracked(t *testing.T) {
	r := testRepo(t)
	withRemote(t, r, true)
	if _, err := r.Git("checkout", "-q", "-b", "feature"); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Git("rev-parse", "--abbrev-ref", "@{upstream}"); err == nil {
		t.Fatal("the new branch was expected to track nothing")
	}

	base, ok := r.MergeBase()
	if !ok {
		t.Fatal("no merge base on an untracked branch")
	}
	if base.Ref != "origin/main" {
		t.Errorf("ref = %q, want origin/main", base.Ref)
	}
}

// A remote whose HEAD was never recorded — a single-branch clone, a remote
// added by hand — still resolves, by the usual branch names.
func TestMergeBaseFallsBackToTheUsualNames(t *testing.T) {
	r := testRepo(t)
	withRemote(t, r, false)
	if _, err := r.Git("symbolic-ref", "--short", "refs/remotes/origin/HEAD"); err == nil {
		t.Skip("this git records origin/HEAD on push; the fallback is untested here")
	}
	base, ok := r.MergeBase()
	if !ok {
		t.Fatal("no merge base without origin/HEAD")
	}
	if base.Ref != "origin/main" {
		t.Errorf("ref = %q, want origin/main", base.Ref)
	}
}

// No remote is an ordinary state, not an error: there is simply no base.
func TestMergeBaseWithoutARemote(t *testing.T) {
	r := testRepo(t)
	if base, ok := r.MergeBase(); ok {
		t.Errorf("a repository with no remote reported a base: %+v", base)
	}
}

// The scan carries the base along with everything else it learns.
func TestRescanCarriesTheBase(t *testing.T) {
	r := testRepo(t)
	withRemote(t, r, true)
	scan, err := r.Rescan(false)
	if err != nil {
		t.Fatal(err)
	}
	if scan.Base.Ref != "origin/main" {
		t.Errorf("scan base = %+v, want origin/main", scan.Base)
	}
}

// The branch's own commits are the ones the base does not have, newest first.
func TestCommitsListsWhatTheBranchAdds(t *testing.T) {
	r := testRepo(t)
	withRemote(t, r, true)
	for _, subject := range []string{"first on top", "second on top"} {
		write(t, r.Dir, "foo.txt", subject+"\n")
		if err := r.Stage([]string{"foo.txt"}); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Commit(subject, false); err != nil {
			t.Fatal(err)
		}
	}

	commits, err := r.Commits("origin/main")
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 2 {
		t.Fatalf("got %d commits, want the 2 added on top: %+v", len(commits), commits)
	}
	if commits[0].Subject != "second on top" {
		t.Errorf("commits[0] = %q, want the newest first", commits[0].Subject)
	}
	if commits[1].Subject != "first on top" {
		t.Errorf("commits[1] = %q", commits[1].Subject)
	}
	for _, c := range commits {
		if len(c.Hash) < 40 || !strings.HasPrefix(c.Hash, c.Short) {
			t.Errorf("hash %q / short %q do not agree", c.Hash, c.Short)
		}
	}

	// The scan carries them, and only when the branch is ahead.
	scan, err := r.Rescan(false)
	if err != nil {
		t.Fatal(err)
	}
	if len(scan.Commits) != 2 {
		t.Errorf("scan carried %d commits, want 2", len(scan.Commits))
	}
	if _, err := r.Git("push", "-q", "origin", "main"); err != nil {
		t.Fatal(err)
	}
	if scan, err = r.Rescan(false); err != nil {
		t.Fatal(err)
	}
	if len(scan.Commits) != 0 {
		t.Errorf("a branch level with its base listed %d commits", len(scan.Commits))
	}
}

// A subject with spaces survives the parse, since only the first space
// separates it from the hash.
func TestCommitSubjectsKeepTheirSpaces(t *testing.T) {
	r := testRepo(t)
	withRemote(t, r, true)
	subject := "Put every external call behind one System interface"
	write(t, r.Dir, "foo.txt", "changed\n")
	if err := r.Stage([]string{"foo.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit(subject, false); err != nil {
		t.Fatal(err)
	}
	commits, err := r.Commits("origin/main")
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 1 || commits[0].Subject != subject {
		t.Errorf("commits = %+v, want the one subject %q", commits, subject)
	}
}

// A commit's files are what it changed against its parent, with the status
// character naming the change rather than a staging state.
func TestCommitFilesAndDiff(t *testing.T) {
	r := testRepo(t)
	write(t, r.Dir, "foo.txt", "one\nCHANGED\nthree\nfour\nfive\n")
	write(t, r.Dir, "added.txt", "brand new\n")
	if err := r.Stage([]string{"foo.txt", "added.txt"}); err != nil {
		t.Fatal(err)
	}
	if _, err := r.Commit("a commit to inspect", false); err != nil {
		t.Fatal(err)
	}
	head, err := r.Git("rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	head = strings.TrimSpace(head)

	files, err := r.CommitFiles(head)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]State{}
	for _, f := range files {
		got[f.Path] = f.State
	}
	if got["foo.txt"] != "M_" || got["added.txt"] != "A_" {
		t.Fatalf("files = %v, want foo.txt M_ and added.txt A_", got)
	}
	if d := State("M_").DescribeChange(); d != "Modified" {
		t.Errorf("M_ describes as %q, want Modified", d)
	}
	if d := State("A_").DescribeChange(); d != "Added" {
		t.Errorf("A_ describes as %q, want Added", d)
	}

	diff, err := r.CommitDiff(head, File{Path: "foo.txt", State: "M_"}, 3)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(diff, "+CHANGED") || !strings.Contains(diff, "-two") {
		t.Errorf("the commit's diff for foo.txt:\n%s", diff)
	}
	// The commit's own header must not be in it, only the patch.
	if strings.Contains(diff, "a commit to inspect") {
		t.Errorf("the commit message leaked into the diff:\n%s", diff)
	}
}

// The very first commit has no parent, and still lists what it brought.
func TestCommitFilesOnTheRootCommit(t *testing.T) {
	r := testRepo(t)
	root, err := r.Git("rev-list", "--max-parents=0", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	files, err := r.CommitFiles(strings.TrimSpace(root))
	if err != nil {
		t.Fatal(err)
	}
	if len(files) == 0 {
		t.Fatal("the root commit listed no files")
	}
	if files[0].Path != "foo.txt" || files[0].State != "A_" {
		t.Errorf("root commit files = %+v, want foo.txt added", files)
	}
}

// A soft reset moves the branch and nothing else: the work in the popped
// commits is still in the index afterwards, because the index never moved.
func TestResetSoftPopsCommitsAndKeepsTheWork(t *testing.T) {
	r := testRepo(t)
	withRemote(t, r, true)
	base, err := r.Git("rev-parse", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	base = strings.TrimSpace(base)

	for _, body := range []string{"first\n", "second\n"} {
		write(t, r.Dir, "foo.txt", body)
		if err := r.Stage([]string{"foo.txt"}); err != nil {
			t.Fatal(err)
		}
		if _, err := r.Commit("on top", false); err != nil {
			t.Fatal(err)
		}
	}
	// Something unstaged as well, which must survive untouched.
	write(t, r.Dir, "loose.txt", "not staged\n")

	commits, err := r.Commits("origin/main")
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 2 {
		t.Fatalf("want 2 commits ahead, got %d", len(commits))
	}
	// The parent of the older of the two is the base.
	parent, ok := r.Parent(commits[1].Hash)
	if !ok {
		t.Fatal("the older commit has no parent")
	}
	if parent != base {
		t.Fatalf("parent = %q, want the base %q", parent, base)
	}

	if err := r.ResetSoft(parent); err != nil {
		t.Fatal(err)
	}

	if head, _ := r.Git("rev-parse", "HEAD"); strings.TrimSpace(head) != base {
		t.Errorf("HEAD = %q, want the base %q", strings.TrimSpace(head), base)
	}
	if left, err := r.Commits("origin/main"); err != nil || len(left) != 0 {
		t.Errorf("commits left = %+v (err %v), want none", left, err)
	}
	// foo.txt's change is staged, and the loose file is still just sitting there.
	files, err := r.Status(r.StatusParent(false))
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]State{}
	for _, f := range files {
		got[f.Path] = f.State
	}
	if got["foo.txt"] != "M_" {
		t.Errorf("foo.txt = %q, want M_ (staged)", got["foo.txt"])
	}
	if got["loose.txt"] != "_O" {
		t.Errorf("loose.txt = %q, want it left untracked", got["loose.txt"])
	}
	if body := read(t, r, "foo.txt"); body != "second\n" {
		t.Errorf("the working tree was touched: foo.txt = %q", body)
	}
}

// The root commit has nothing to reset back to.
func TestParentOfTheRootCommit(t *testing.T) {
	r := testRepo(t)
	root, err := r.Git("rev-list", "--max-parents=0", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if parent, ok := r.Parent(strings.TrimSpace(root)); ok {
		t.Errorf("the root commit reported a parent %q", parent)
	}
}

// The editor comes from $EDITOR, which may carry arguments of its own, and
// falls back to vi when the environment says nothing.
func TestEditorHonoursTheEnvironment(t *testing.T) {
	r := testRepo(t)
	for _, tc := range []struct {
		env  string
		want []string
	}{
		{"", []string{"vi"}},
		{"nano", []string{"nano"}},
		{"code -w", []string{"code", "-w"}},
		{"  emacsclient   -nw  ", []string{"emacsclient", "-nw"}},
	} {
		t.Setenv("EDITOR", tc.env)
		ed, err := r.Edit("foo.txt")
		if err != nil {
			t.Fatalf("EDITOR=%q: %v", tc.env, err)
		}
		cmd, ok := ed.(editorCmd)
		if !ok {
			t.Fatalf("EDITOR=%q: got %T", tc.env, ed)
		}
		want := append(append([]string{}, tc.want...), filepath.Join(r.Dir, "foo.txt"))
		if len(cmd.Args) != len(want) {
			t.Errorf("EDITOR=%q: args = %v, want %v", tc.env, cmd.Args, want)
			continue
		}
		for i := range want {
			if cmd.Args[i] != want[i] {
				t.Errorf("EDITOR=%q: args = %v, want %v", tc.env, cmd.Args, want)
				break
			}
		}
		if cmd.Dir != r.Dir {
			t.Errorf("EDITOR=%q: dir = %q, want the work tree", tc.env, cmd.Dir)
		}
	}
}

// A path that is not on disk is refused rather than created.
func TestEditRefusesAMissingPath(t *testing.T) {
	r := testRepo(t)
	if _, err := r.Edit("never-existed.txt"); err == nil {
		t.Error("editing a path that is not there was allowed")
	}
	if _, err := r.Edit("foo.txt"); err != nil {
		t.Errorf("editing a real file was refused: %v", err)
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
