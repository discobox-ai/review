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
	r, err := Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	run(t, r, "add", "foo.txt")
	run(t, r, "commit", "-q", "-m", "initial")
	return r
}

// run drives git directly. difftui itself no longer stages or commits, so the
// tests have to build the states they want to read.
func run(t *testing.T, r *Repo, args ...string) string {
	t.Helper()
	out, err := (Local{}).Git(r.Dir, nil, args...)
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return out
}

func write(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(filepath.Join(dir, name)), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

// stateOf finds a path in the scan result.
func stateOf(t *testing.T, r *Repo, path string) State {
	t.Helper()
	files, err := r.Status()
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

	run(t, r, "add", "foo.txt")
	if got := stateOf(t, r, "foo.txt"); got != "M_" {
		t.Errorf("after staging, foo.txt state = %s, want M_", got)
	}

	// Modified again on top of what is staged.
	write(t, r.Dir, "foo.txt", "one\nTWO\nthree\nFOUR\nfive\n")
	if got := stateOf(t, r, "foo.txt"); got != "MM" {
		t.Errorf("part staged foo.txt = %s, want MM", got)
	}
	if got := stateOf(t, r, "foo.txt").Pending(); got != "part staged" {
		t.Errorf("MM pending = %q", got)
	}
}

// ---------------------------------------------------------------------------
// The review diff
// ---------------------------------------------------------------------------

// The review is one diff covering the whole tree, and it does not care which
// side of the index a change is on: staged, unstaged and untracked changes all
// have to be in it, or the review would be of something other than what the
// branch is proposing.
func TestReviewDiffCoversStagedUnstagedAndUntracked(t *testing.T) {
	r := testRepo(t)
	write(t, r.Dir, "staged.txt", "staged\n")
	run(t, r, "add", "staged.txt")
	write(t, r.Dir, "foo.txt", "one\nTWO\nthree\nfour\nfive\n") // unstaged
	write(t, r.Dir, "untracked.txt", "brand new\n")

	scan, err := r.Rescan("", 3)
	if err != nil {
		t.Fatal(err)
	}
	paths := map[string]bool{}
	for _, p := range ParseMulti(scan.Diff) {
		paths[p.Path] = true
	}
	for _, want := range []string{"staged.txt", "foo.txt", "untracked.txt"} {
		if !paths[want] {
			t.Errorf("%s missing from the review diff; got %v\n%s", want, paths, scan.Diff)
		}
	}
}

// Every line in the review that is not yet in a commit shows up in the pending
// diff, and every line that is already committed does not. That is the whole
// basis of the margin mark.
func TestPendingDiffNamesOnlyUncommittedLines(t *testing.T) {
	r := testRepo(t)
	// One committed change on top of the base…
	write(t, r.Dir, "foo.txt", "one\nTWO\nthree\nfour\nfive\n")
	run(t, r, "commit", "-qam", "change two")
	// …and one that is not committed.
	write(t, r.Dir, "foo.txt", "one\nTWO\nthree\nFOUR\nfive\n")

	scan, err := r.Rescan("HEAD~1", 3)
	if err != nil {
		t.Fatal(err)
	}
	review := ParseMulti(scan.Diff)
	if len(review) != 1 {
		t.Fatalf("review covers %d files, want 1", len(review))
	}
	if adds, _ := review[0].Stat(); adds != 2 {
		t.Errorf("review adds %d lines, want 2 (TWO and FOUR)", adds)
	}

	pending := ParseMulti(scan.Pending)
	if len(pending) != 1 {
		t.Fatalf("pending covers %d files, want 1:\n%s", len(pending), scan.Pending)
	}
	marks := pending[0].ChangedNewLines()
	if !marks[4] {
		t.Errorf("line 4 (FOUR, not committed) is not marked: %v", marks)
	}
	if marks[2] {
		t.Errorf("line 2 (TWO, committed) is marked: %v", marks)
	}
}

// A repository with no base and nothing pushed still has a review: what has
// not been committed. Falling back to HEAD is what stops difftui opening on an
// empty window in a repository that has no remote.
func TestRescanFallsBackToHEADWithNoRemote(t *testing.T) {
	r := testRepo(t)
	write(t, r.Dir, "foo.txt", "one\nTWO\nthree\nfour\nfive\n")

	scan, err := r.Rescan("", 3)
	if err != nil {
		t.Fatal(err)
	}
	if scan.Base.Ref != "HEAD" {
		t.Errorf("base ref = %q, want HEAD", scan.Base.Ref)
	}
	if !scan.Base.Auto {
		t.Error("a base difftui worked out is not marked as automatic")
	}
	if !strings.Contains(scan.Diff, "+TWO") {
		t.Errorf("uncommitted change missing from the review:\n%s", scan.Diff)
	}
}

func TestRescanGathersTheWholeScan(t *testing.T) {
	r := testRepo(t)
	write(t, r.Dir, "foo.txt", "one\nTWO\nthree\nfour\nfive\n")
	write(t, r.Dir, "new.txt", "brand new\n")

	scan, err := r.Rescan("", 3)
	if err != nil {
		t.Fatal(err)
	}
	if scan.Branch != "main" {
		t.Errorf("branch = %q, want main", scan.Branch)
	}
	if !scan.HasHEAD {
		t.Error("HasHEAD is false in a repository with a commit")
	}
	if scan.Merging {
		t.Error("Merging is true with no merge in progress")
	}
	states := map[string]State{}
	for _, f := range scan.Files {
		states[f.Path] = f.State
	}
	if states["foo.txt"] != "_M" || states["new.txt"] != "_O" {
		t.Errorf("scan files = %v", states)
	}
}

// Turning the context up gives every hunk more lines without changing what the
// diff says was changed.
func TestDiffContextLines(t *testing.T) {
	r := testRepo(t)
	write(t, r.Dir, "foo.txt", "one\ntwo\nthree\nFOUR\nfive\n")

	narrow, err := r.Rescan("", 1)
	if err != nil {
		t.Fatal(err)
	}
	wide, err := r.Rescan("", 5)
	if err != nil {
		t.Fatal(err)
	}
	n := len(ParseMulti(narrow.Diff)[0].Hunks[0].Lines)
	w := len(ParseMulti(wide.Diff)[0].Hunks[0].Lines)
	if w <= n {
		t.Errorf("context 5 gave %d lines, context 1 gave %d", w, n)
	}
}

// ---------------------------------------------------------------------------
// The base
// ---------------------------------------------------------------------------

// withRemote gives the repository an "origin" that is a clone of itself, so
// the base has something real to be measured against.
func withRemote(t *testing.T, r *Repo, setHead bool) {
	t.Helper()
	remote := t.TempDir()
	run(t, r, "init", "-q", "--bare", "-b", "main", remote)
	run(t, r, "remote", "add", "origin", remote)
	run(t, r, "push", "-q", "origin", "main")
	if setHead {
		run(t, r, "remote", "set-head", "origin", "main")
	}
}

func TestBaseFollowsTheRemoteDefault(t *testing.T) {
	r := testRepo(t)
	withRemote(t, r, true)
	run(t, r, "checkout", "-q", "-b", "feature")
	write(t, r.Dir, "foo.txt", "one\nTWO\nthree\nfour\nfive\n")
	run(t, r, "commit", "-qam", "on the feature branch")

	base := r.ResolveBase("", true)
	if base.Ref != "origin/main" {
		t.Errorf("base ref = %q, want origin/main", base.Ref)
	}
	if base.Ahead != 1 || base.Behind != 0 {
		t.Errorf("ahead/behind = %d/%d, want 1/0", base.Ahead, base.Behind)
	}
	if len(base.SHA) != 40 {
		t.Errorf("merge base sha = %q, want a full hash", base.SHA)
	}
	if base.Commit != base.SHA[:7] {
		t.Errorf("abbreviated base = %q, does not match %q", base.Commit, base.SHA)
	}
}

func TestBaseFollowsTheUpstream(t *testing.T) {
	r := testRepo(t)
	withRemote(t, r, true)
	run(t, r, "checkout", "-q", "-b", "feature")
	run(t, r, "push", "-q", "-u", "origin", "feature")
	write(t, r.Dir, "foo.txt", "one\nTWO\nthree\nfour\nfive\n")
	run(t, r, "commit", "-qam", "unpushed")

	// Tracking wins over the remote's default branch: it is what the branch
	// was told to build on rather than a guess at where it started.
	if base := r.ResolveBase("", true); base.Ref != "origin/feature" {
		t.Errorf("base ref = %q, want origin/feature", base.Ref)
	}
}

// A ref the user named wins over anything difftui would have worked out.
func TestBaseHonoursAChosenRef(t *testing.T) {
	r := testRepo(t)
	run(t, r, "checkout", "-q", "-b", "feature")
	write(t, r.Dir, "foo.txt", "one\nTWO\nthree\nfour\nfive\n")
	run(t, r, "commit", "-qam", "on the feature branch")

	base := r.ResolveBase("main", true)
	if base.Ref != "main" {
		t.Errorf("base ref = %q, want main", base.Ref)
	}
	if base.Auto {
		t.Error("a base the user chose is marked as automatic")
	}
	if base.Err != "" {
		t.Errorf("unexpected error %q", base.Err)
	}
}

// A ref that is not there is reported rather than silently ignored: a review
// measured from somewhere other than where you asked is worse than one that
// says it could not do as it was told.
func TestBaseReportsARefThatIsNotThere(t *testing.T) {
	r := testRepo(t)
	base := r.ResolveBase("no/such/ref", true)
	if base.Err == "" {
		t.Fatal("a missing ref was accepted without a word")
	}
	if !strings.Contains(base.Err, "no/such/ref") {
		t.Errorf("error = %q, does not name the ref", base.Err)
	}
	if base.SHA == "" {
		t.Error("no fallback base: the window would have nothing to show")
	}
}

// Refs lists what the base could be set to, and leaves out the branch you are
// on — a branch measured against itself has nothing to review.
func TestRefsOffersSomewhereToMeasureFrom(t *testing.T) {
	r := testRepo(t)
	withRemote(t, r, true)
	run(t, r, "checkout", "-q", "-b", "feature")

	refs := r.Refs()
	var seenRemote, seenSelf bool
	for _, ref := range refs {
		if ref == "origin/main" {
			seenRemote = true
		}
		if ref == "feature" {
			seenSelf = true
		}
	}
	if !seenRemote {
		t.Errorf("origin/main missing from %v", refs)
	}
	if seenSelf {
		t.Errorf("the checked out branch is offered as its own base: %v", refs)
	}
}

// ---------------------------------------------------------------------------
// History
// ---------------------------------------------------------------------------

func TestCommitsListsWhatTheBranchAdds(t *testing.T) {
	r := testRepo(t)
	withRemote(t, r, true)
	base := strings.TrimSpace(run(t, r, "rev-parse", "HEAD"))
	run(t, r, "checkout", "-q", "-b", "feature")

	write(t, r.Dir, "foo.txt", "one\nTWO\nthree\nfour\nfive\n")
	run(t, r, "commit", "-qam", "rename two to TWO")
	write(t, r.Dir, "beta.go", "package beta\n")
	run(t, r, "add", "beta.go")
	run(t, r, "commit", "-qm", "add  the  beta  module")

	commits, err := r.Commits(base)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 2 {
		t.Fatalf("got %d commits, want 2: %+v", len(commits), commits)
	}
	// Newest first.
	if commits[0].Subject != "add  the  beta  module" {
		t.Errorf("subject = %q; runs of spaces must survive", commits[0].Subject)
	}
	if commits[1].Subject != "rename two to TWO" {
		t.Errorf("second subject = %q", commits[1].Subject)
	}
	if commits[0].Author != "Test" {
		t.Errorf("author = %q, want Test", commits[0].Author)
	}
	if commits[0].Short != commits[0].Hash[:7] {
		t.Errorf("short hash %q does not match %q", commits[0].Short, commits[0].Hash)
	}
}

// One command gives the whole commit, which is what the stack draws.
func TestCommitDiffCoversEveryFileTheCommitTouched(t *testing.T) {
	r := testRepo(t)
	write(t, r.Dir, "foo.txt", "one\nTWO\nthree\nfour\nfive\n")
	write(t, r.Dir, "beta.go", "package beta\n")
	run(t, r, "add", "-A")
	run(t, r, "commit", "-qm", "two things at once")
	hash := strings.TrimSpace(run(t, r, "rev-parse", "HEAD"))

	text, err := r.CommitDiff(hash, 3)
	if err != nil {
		t.Fatal(err)
	}
	patches := ParseMulti(text)
	if len(patches) != 2 {
		t.Fatalf("commit diff covers %d files, want 2:\n%s", len(patches), text)
	}
	got := map[string]bool{patches[0].Path: true, patches[1].Path: true}
	if !got["beta.go"] || !got["foo.txt"] {
		t.Errorf("commit diff covers %v", got)
	}
}

// The very first commit on a branch has no parent to be compared against, and
// still has to show what it introduced.
func TestCommitDiffOnTheRootCommit(t *testing.T) {
	r := testRepo(t)
	hash := strings.TrimSpace(run(t, r, "rev-parse", "HEAD"))
	text, err := r.CommitDiff(hash, 3)
	if err != nil {
		t.Fatal(err)
	}
	patches := ParseMulti(text)
	if len(patches) != 1 || patches[0].Path != "foo.txt" {
		t.Errorf("root commit diff = %v", patches)
	}
}

// CarryDiff is what moves a comment between a commit and the working tree.
// Its old side is the commit and its new side is the tree as it stands.
func TestCarryDiffMovesALineOntoTheWorkingTree(t *testing.T) {
	r := testRepo(t)
	// A commit that adds a line at the top, so everything below it shifts.
	write(t, r.Dir, "foo.txt", "zero\none\ntwo\nthree\nfour\nfive\n")
	run(t, r, "commit", "-qam", "add zero")
	hash := strings.TrimSpace(run(t, r, "rev-parse", "HEAD"))
	// Then two more lines at the top, uncommitted.
	write(t, r.Dir, "foo.txt", "minus two\nminus one\nzero\none\ntwo\nthree\nfour\nfive\n")

	text, err := r.CarryDiff(hash, "foo.txt")
	if err != nil {
		t.Fatal(err)
	}
	p := ParsePatch(text)
	// "three" was line 4 in the commit and is line 6 now.
	got, ok := p.MapOldToNew(4)
	if !ok || got != 6 {
		t.Errorf("MapOldToNew(4) = %d, %v; want 6, true\n%s", got, ok, text)
	}
	back, ok := p.MapNewToOld(6)
	if !ok || back != 4 {
		t.Errorf("MapNewToOld(6) = %d, %v; want 4, true", back, ok)
	}
}

// ---------------------------------------------------------------------------
// Reading files
// ---------------------------------------------------------------------------

func TestFileContent(t *testing.T) {
	r := testRepo(t)
	hash := strings.TrimSpace(run(t, r, "rev-parse", "HEAD"))
	write(t, r.Dir, "foo.txt", "changed on disk\n")

	if got, err := r.FileContent("", "foo.txt"); err != nil || got != "changed on disk\n" {
		t.Errorf("working tree content = %q, %v", got, err)
	}
	if got, err := r.FileContent(hash, "foo.txt"); err != nil || !strings.HasPrefix(got, "one\n") {
		t.Errorf("committed content = %q, %v", got, err)
	}
}

func TestEditRefusesAMissingPath(t *testing.T) {
	r := testRepo(t)
	if _, err := r.Edit("nope.txt"); err == nil {
		t.Error("editing a path that is not on disk was allowed")
	}
}

// A file read out of a commit goes to the editor as a copy, so that history
// cannot be rewritten by an editor that thinks it is editing the file.
func TestEditAtServesACopy(t *testing.T) {
	r := testRepo(t)
	hash := strings.TrimSpace(run(t, r, "rev-parse", "HEAD"))
	write(t, r.Dir, "foo.txt", "changed on disk\n")

	ed, tmp, err := r.EditAt(hash, "foo.txt")
	if err != nil {
		t.Fatal(err)
	}
	if ed == nil || tmp == "" {
		t.Fatalf("EditAt returned %v / %q", ed, tmp)
	}
	if strings.HasPrefix(tmp, r.Dir) {
		t.Errorf("the copy at %q is inside the working tree", tmp)
	}
	body, err := os.ReadFile(tmp)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(body), "one\n") {
		t.Errorf("copy holds %q, want the committed text", body)
	}
	r.RemoveTempCopy(tmp)
	if _, err := os.Stat(tmp); err == nil {
		t.Error("the copy outlived the editor")
	}
}

// ---------------------------------------------------------------------------
// Files in the git directory
// ---------------------------------------------------------------------------

// A review is written whole, through a temporary file, so that a reader
// polling it never catches half of one.
func TestWriteStateFileIsAtomic(t *testing.T) {
	r := testRepo(t)
	if err := r.WriteStateFile("review.json", "{}\n"); err != nil {
		t.Fatal(err)
	}
	if got, ok := r.ReadStateFile("review.json"); !ok || got != "{}\n" {
		t.Errorf("read back %q (present=%v)", got, ok)
	}
	if _, err := os.Stat(filepath.Join(r.GitDir, "review.json.tmp")); err == nil {
		t.Error("the temporary file was left behind")
	}
}

// The stamp is what tells difftui somebody else has written the review. It has
// to change when the contents change, even within the same second — which is
// why it carries the size as well as the time.
func TestStateFileStampNoticesAChange(t *testing.T) {
	r := testRepo(t)
	if got := r.StateFileStamp("review.json"); got != (Stamp{}) {
		t.Errorf("a missing file has stamp %+v, want the zero stamp", got)
	}
	if err := r.WriteStateFile("review.json", "{}\n"); err != nil {
		t.Fatal(err)
	}
	first := r.StateFileStamp("review.json")
	if first == (Stamp{}) {
		t.Fatal("a written file still has the zero stamp")
	}
	if err := r.WriteStateFile("review.json", `{"version":1}`+"\n"); err != nil {
		t.Fatal(err)
	}
	if second := r.StateFileStamp("review.json"); second == first {
		t.Errorf("stamp did not change: %+v", second)
	}
}

func TestUserNameComesFromGit(t *testing.T) {
	r := testRepo(t)
	if got := r.UserName(); got != "Test" {
		t.Errorf("UserName() = %q, want Test", got)
	}
}

// Recent lists the branch's own history, newest first, for the base picker.
func TestRecentListsHistoryNewestFirst(t *testing.T) {
	r := testRepo(t)
	write(t, r.Dir, "foo.txt", "one\nTWO\nthree\nfour\nfive\n")
	run(t, r, "commit", "-qam", "second  commit")
	write(t, r.Dir, "foo.txt", "one\nTWO\nTHREE\nfour\nfive\n")
	run(t, r, "commit", "-qam", "third")

	commits, err := r.Recent(2)
	if err != nil {
		t.Fatal(err)
	}
	if len(commits) != 2 {
		t.Fatalf("got %d commits, want the 2 asked for", len(commits))
	}
	if commits[0].Subject != "third" {
		t.Errorf("first is %q, want the newest", commits[0].Subject)
	}
	if commits[1].Subject != "second  commit" {
		t.Errorf("second is %q; runs of spaces must survive", commits[1].Subject)
	}
	if commits[0].Short != commits[0].Hash[:7] || commits[0].When == "" {
		t.Errorf("commit = %+v, want it filled in", commits[0])
	}
	if got, _ := r.Recent(0); got != nil {
		t.Errorf("Recent(0) = %v, want nothing", got)
	}
}
