package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/discobox-ai/review/internal/git"
	"github.com/discobox-ai/review/internal/review"
)

// testRepo builds a throwaway repository on a branch with one change on it.
func testRepo(t *testing.T) *git.Repo {
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
	write(t, dir, "server.go", "package main\n\nfunc handler() error {\n\treturn nil\n}\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-qm", "initial")
	gitRun(t, dir, "checkout", "-q", "-b", "feature")
	write(t, dir, "server.go", "package main\n\nfunc handler() error {\n\treturn write()\n}\n")
	gitRun(t, dir, "commit", "-qam", "return a body")

	r, err := git.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	// A base, the way choosing one in the window records it: without one and
	// with no remote to guess from, the review is only what is uncommitted.
	var out strings.Builder
	if err := Run(r, []string{"base", "main"}, &out); err != nil {
		t.Fatal(err)
	}
	return r
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

func write(t *testing.T, dir, name, body string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// run carries out one command and returns what it printed.
func run(t *testing.T, repo *git.Repo, args ...string) string {
	t.Helper()
	var out strings.Builder
	if err := Run(repo, args, &out); err != nil {
		t.Fatalf("discobox-review %s: %v", strings.Join(args, " "), err)
	}
	return out.String()
}

// fails runs a command that should not work.
func fails(t *testing.T, repo *git.Repo, args ...string) {
	t.Helper()
	var out strings.Builder
	if err := Run(repo, args, &out); err == nil {
		t.Errorf("discobox-review %s succeeded, printing %q", strings.Join(args, " "), out.String())
	}
}

// stored is the review as anything else would read it: off the disk.
func stored(t *testing.T, repo *git.Repo) *review.State {
	t.Helper()
	body, ok := repo.ReadStateFile(review.FileName)
	if !ok {
		t.Fatal("no review file")
	}
	st, err := review.Parse([]byte(body))
	if err != nil {
		t.Fatal(err)
	}
	return st
}

// ---------------------------------------------------------------------------

// The whole round trip, through the file each time — which is what makes these
// safe to interleave with a window open on the same review.
func TestCommentsThroughTheFile(t *testing.T) {
	repo := testRepo(t)

	if got := run(t, repo, "comment", "--by", "git-user", "server.go:4", "why not check the error?"); !strings.HasPrefix(got, "t1") {
		t.Errorf("comment printed %q, want it to name the new thread", got)
	}
	st := stored(t, repo)
	if len(st.Threads) != 1 {
		t.Fatalf("threads = %+v", st.Threads)
	}
	th := st.Threads[0]
	if th.Path != "server.go" || th.Line != 4 || th.Side != review.SideNew {
		t.Errorf("anchored at %s %s:%d", th.Path, th.Side, th.Line)
	}
	if th.Comments[0].By != "Test" {
		t.Errorf("by %q, want git's own name", th.Comments[0].By)
	}
	if st.Branch != "feature" {
		t.Errorf("the review is stamped %q", st.Branch)
	}

	run(t, repo, "reply", "t1", "--by", "claude", "fixing it")
	if got := stored(t, repo).Thread("t1"); len(got.Comments) != 2 || got.Comments[1].By != "claude" {
		t.Errorf("comments = %+v", got.Comments)
	}

	run(t, repo, "resolve", "t1")
	if !stored(t, repo).Thread("t1").Resolved {
		t.Error("resolve did not resolve")
	}
	run(t, repo, "reopen", "t1")
	if stored(t, repo).Thread("t1").Resolved {
		t.Error("reopen did not reopen")
	}

	// One remark of a conversation, then the conversation.
	run(t, repo, "delete", "t1", "1")
	if got := stored(t, repo).Thread("t1"); len(got.Comments) != 1 {
		t.Errorf("comments = %+v, want the reply gone", got.Comments)
	}
	run(t, repo, "delete", "t1")
	if len(stored(t, repo).Threads) != 0 {
		t.Error("delete left the conversation behind")
	}
}

// A comment records the line it is about, the way one written in the window
// does. Nothing depends on the field, which is exactly why it has to be there:
// it is what lets a review be read on its own, and a review whose comments
// carry it only when they came through the window is one document written two
// ways.
func TestCommentRecordsTheLineItIsAbout(t *testing.T) {
	repo := testRepo(t)

	// The new side is the working tree, tab and all: this is the line as it
	// stands, not a tidied-up rendering of it.
	run(t, repo, "comment", "--by", "git-user", "server.go:4", "why not check the error?")
	if got := stored(t, repo).Thread("t1").Text; got != "\treturn write()" {
		t.Errorf("text = %q, want the working tree's line 4", got)
	}

	// The old side is the base, so it is the line the change took away —
	// which is the only place that line still exists.
	run(t, repo, "comment", "--by", "git-user", "server.go:4", "--side", "old", "it was bare")
	if got := stored(t, repo).Thread("t2").Text; got != "\treturn nil" {
		t.Errorf("old-side text = %q, want the base's line 4", got)
	}

	// A line the diff never mentions still has a text, because the side names
	// a whole file rather than the part of it that changed.
	run(t, repo, "comment", "--by", "git-user", "server.go:1", "the package clause")
	if got := stored(t, repo).Thread("t3").Text; got != "package main" {
		t.Errorf("text = %q, want line 1 of the file", got)
	}
}

// A line that is not there gets no text rather than the wrong one. Losing the
// field is nothing; putting somebody else's line in it would be a lie about
// what was being talked about.
func TestCommentOnALineThatIsNotThere(t *testing.T) {
	repo := testRepo(t)

	run(t, repo, "comment", "--by", "git-user", "server.go:999", "off the end")
	if got := stored(t, repo).Thread("t1").Text; got != "" {
		t.Errorf("text = %q, want nothing at all", got)
	}
	run(t, repo, "comment", "--by", "git-user", "nosuch.go:1", "no such file")
	if got := stored(t, repo).Thread("t2").Text; got != "" {
		t.Errorf("text = %q for a file that is not there", got)
	}
	// And the comment itself is still made: the review is the record, and a
	// missing line is not a reason to drop what somebody said about it.
	if len(stored(t, repo).Threads) != 2 {
		t.Error("a comment was dropped for want of a line to quote")
	}
}

// A block comment is written the way a place in a review is written everywhere
// else.
func TestCommentOnARangeOfLines(t *testing.T) {
	repo := testRepo(t)
	run(t, repo, "comment", "--by", "git-user", "server.go:4-6", "this whole block")
	th := stored(t, repo).Threads[0]
	if first, last := th.Span(); first != 4 || last != 6 {
		t.Errorf("Span() = %d–%d, want 4–6", first, last)
	}
	run(t, repo, "comment", "--by", "git-user", "server.go:2", "--side", "old", "and this")
	if got := stored(t, repo).Threads[1]; got.Side != review.SideOld {
		t.Errorf("side = %q, want the old one", got.Side)
	}
}

func TestAnchorSyntax(t *testing.T) {
	cases := []struct {
		in    string
		path  string
		first int
		span  int
		bad   bool
	}{
		{in: "a.go:4", path: "a.go", first: 4, span: 1},
		{in: "a.go:4-9", path: "a.go", first: 4, span: 6},
		{in: "dir/a.go:1", path: "dir/a.go", first: 1, span: 1},
		{in: "a.go", bad: true},
		{in: "a.go:", bad: true},
		{in: ":4", bad: true},
		{in: "a.go:0", bad: true},
		{in: "a.go:x", bad: true},
		{in: "a.go:9-4", bad: true},
	}
	for _, c := range cases {
		path, first, span, err := anchor(c.in)
		if c.bad {
			if err == nil {
				t.Errorf("anchor(%q) was accepted", c.in)
			}
			continue
		}
		if err != nil {
			t.Errorf("anchor(%q): %v", c.in, err)
			continue
		}
		if path != c.path || first != c.first || span != c.span {
			t.Errorf("anchor(%q) = %q %d %d, want %q %d %d",
				c.in, path, first, span, c.path, c.first, c.span)
		}
	}
}

func TestListing(t *testing.T) {
	repo := testRepo(t)
	run(t, repo, "comment", "--by", "git-user", "server.go:4", "first")
	run(t, repo, "comment", "other.go:1", "--by", "claude", "second")
	run(t, repo, "resolve", "t2")

	all := run(t, repo, "list")
	if !strings.Contains(all, "t1") || !strings.Contains(all, "t2") {
		t.Errorf("list = %q", all)
	}
	if !strings.Contains(all, "open") || !strings.Contains(all, "resolved") {
		t.Errorf("list does not say what state they are in:\n%s", all)
	}

	if got := run(t, repo, "list", "--open"); strings.Contains(got, "t2") {
		t.Errorf("--open listed a resolved one:\n%s", got)
	}
	if got := run(t, repo, "list", "--resolved"); strings.Contains(got, "t1") {
		t.Errorf("--resolved listed an open one:\n%s", got)
	}
	if got := run(t, repo, "list", "--path", "other.go"); strings.Contains(got, "t1") {
		t.Errorf("--path listed another file's:\n%s", got)
	}
	if got := run(t, repo, "list", "--path=other.go"); !strings.Contains(got, "t2") {
		t.Errorf("--flag=value form did not work:\n%s", got)
	}
	if got := run(t, repo, "list", "--path", "nothing.go"); !strings.Contains(got, "no comments") {
		t.Errorf("an empty listing says %q", got)
	}
}

func TestShow(t *testing.T) {
	repo := testRepo(t)
	run(t, repo, "comment", "--by", "git-user", "server.go:4", "why?")
	run(t, repo, "reply", "t1", "--by", "claude", "because")

	got := run(t, repo, "show", "t1")
	for _, want := range []string{"t1", "server.go:4", "open", "why?", "because", "claude", "[1]"} {
		if !strings.Contains(got, want) {
			t.Errorf("show is missing %q:\n%s", want, got)
		}
	}
}

// Approving from a command leaves the digest out, so discobox-review stamps in the
// change as it stands when it next reads the review — which is what makes an
// approval given here nullifiable like any other.
func TestApproving(t *testing.T) {
	repo := testRepo(t)
	run(t, repo, "approve", "--by", "git-user", "server.go", "other.go")
	st := stored(t, repo)
	if !st.Approved("server.go") || !st.Approved("other.go") {
		t.Fatalf("files = %+v", st.Files)
	}
	if got := st.FileFor("server.go"); got.Digest != "" {
		t.Errorf("digest = %q, want it left for discobox-review to stamp", got.Digest)
	}
	if got := st.FileFor("server.go").By; got != "Test" {
		t.Errorf("by %q", got)
	}

	run(t, repo, "unapprove", "server.go")
	st = stored(t, repo)
	if st.Approved("server.go") {
		t.Error("unapprove did not take it back")
	}
	if st.Nullified("server.go") {
		t.Error("unapproving marked the file as having changed")
	}
}

// status reads the repository as well as the review: where the review is
// measured from is a commit only git can name, and the file records approvals
// rather than the files there are to approve.
func TestStatus(t *testing.T) {
	repo := testRepo(t)
	write(t, repo.Dir, "other.go", "package main\n")
	gitRun(t, repo.Dir, "add", "other.go")
	run(t, repo, "comment", "--by", "git-user", "server.go:4", "why?")
	run(t, repo, "comment", "--by", "git-user", "server.go:5", "and this")
	run(t, repo, "resolve", "t2")
	run(t, repo, "approve", "--by", "git-user", "server.go")

	got := fields(t, run(t, repo, "status"))
	if got["branch"] != "feature" {
		t.Errorf("branch = %q", got["branch"])
	}
	if got["base"] != "main" {
		t.Errorf("base = %q, want the ref the review names", got["base"])
	}
	// The merge base is the commit, which is the thing the ref cannot say.
	head := strings.TrimSpace(gitOut(t, repo.Dir, "rev-parse", "--short", "main"))
	if got["merge-base"] != head {
		t.Errorf("merge-base = %q, want %q", got["merge-base"], head)
	}
	if got["ahead"] != "1" || got["behind"] != "0" {
		t.Errorf("ahead/behind = %q/%q, want 1/0", got["ahead"], got["behind"])
	}
	// Two files under review, one of them signed off.
	if got["files"] != "2" || got["approved"] != "1" || got["unapproved"] != "1" {
		t.Errorf("files/approved/unapproved = %q/%q/%q, want 2/1/1",
			got["files"], got["approved"], got["unapproved"])
	}
	if got["comments"] != "2" || got["open"] != "1" {
		t.Errorf("comments/open = %q/%q, want 2/1", got["comments"], got["open"])
	}
}

// An approval given from a command carries no digest, so the first thing to
// read the review stamps in the change as it stands — and once it has, editing
// the file nullifies the approval, exactly as it does in the window.
func TestStatusNullifiesAStaleApproval(t *testing.T) {
	repo := testRepo(t)
	run(t, repo, "approve", "--by", "git-user", "server.go")

	// Reading it stamps rather than nullifies: nothing has moved yet.
	if got := fields(t, run(t, repo, "status")); got["approved"] != "1" {
		t.Fatalf("approved = %q after approving", got["approved"])
	}
	if stored(t, repo).FileFor("server.go").Digest == "" {
		t.Fatal("reading the review did not stamp the digest in")
	}

	write(t, repo.Dir, "server.go", "package main\n\nfunc handler() error {\n\treturn boom()\n}\n")
	got := fields(t, run(t, repo, "status"))
	if got["approved"] != "0" || got["unapproved"] != "1" {
		t.Errorf("approved/unapproved = %q/%q, want 0/1", got["approved"], got["unapproved"])
	}
	if got["nullified"] != "server.go" {
		t.Errorf("nullified = %q, want the file named", got["nullified"])
	}
	// And it is written down, so the window sees the same thing.
	if f := stored(t, repo).FileFor("server.go"); f.Approved || !f.Nullified {
		t.Errorf("the review says %+v", f)
	}
	// The diff heading says so too.
	if out := run(t, repo, "diff", "--stat"); !strings.Contains(out, "nullified") {
		t.Errorf("diff --stat = %q", out)
	}
}

// A reading command writes too — it stamps the digest into an approval given
// without one — and it reads the review long before it does. What it writes
// must be that stamp and nothing else: not the copy it read, laid back over a
// remark that arrived while it was working out the diff.
func TestAReadingCommandDoesNotUndoWhatWasWrittenMeanwhile(t *testing.T) {
	repo := testRepo(t)
	run(t, repo, "approve", "--by", "git-user", "server.go")

	// status, caught between reading the review and stamping it.
	branch := repo.CurrentBranch()
	stale, err := review.Load(repo, branch, 0)
	if err != nil {
		t.Fatal(err)
	}
	run(t, repo, "comment", "--by", "claude", "server.go:4", "arrived meanwhile")

	var out strings.Builder
	if err := status(&out, repo, stale, branch, options{}); err != nil {
		t.Fatal(err)
	}

	st := stored(t, repo)
	if len(st.Threads) != 1 {
		t.Fatalf("status wrote its stale copy over the comment: threads = %+v", st.Threads)
	}
	if st.FileFor("server.go").Digest == "" {
		t.Error("the approval was not stamped")
	}
	// And it reports the review it left behind, not the one it started with.
	if got := fields(t, out.String()); got["comments"] != "1" {
		t.Errorf("status reported comments = %q, want the one that is there", got["comments"])
	}
}

// Asking what the base is changes nothing, so it waits for nobody: it answers
// while something else holds the lock, where setting one would queue.
func TestShowingTheBaseTakesNoLock(t *testing.T) {
	repo := testRepo(t)
	unlock, err := review.Lock(repo, 0)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()

	answered := make(chan string, 1)
	go func() {
		var out strings.Builder
		if err := Run(repo, []string{"base"}, &out); err != nil {
			out.WriteString(err.Error())
		}
		answered <- out.String()
	}()
	select {
	case got := <-answered:
		if strings.TrimSpace(got) != "main" {
			t.Errorf("base printed %q, want main", got)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("base is waiting for a lock it has no use for")
	}
}

// Commands run at the same moment all land. Each has its own view of the
// repository, as separate processes would; the lock is on the file, so it
// holds between them here just as it does there.
func TestCommandsRunTogetherAllLand(t *testing.T) {
	repo := testRepo(t)
	run(t, repo, "approve", "--by", "git-user", "server.go")

	const n = 24
	errs := make(chan error, 2*n)
	for i := 0; i < n; i++ {
		go func() {
			r, err := git.Open(repo.Dir)
			if err != nil {
				errs <- err
				return
			}
			var out strings.Builder
			errs <- Run(r, []string{"comment", "--by", "a" + strconv.Itoa(i%3), "server.go:4", "c" + strconv.Itoa(i)}, &out)
		}()
		// And readers among them, each with a digest to stamp or find stamped.
		go func() {
			r, err := git.Open(repo.Dir)
			if err != nil {
				errs <- err
				return
			}
			var out strings.Builder
			errs <- Run(r, []string{"status"}, &out)
		}()
	}
	for i := 0; i < 2*n; i++ {
		if err := <-errs; err != nil {
			t.Error(err)
		}
	}

	st := stored(t, repo)
	if len(st.Threads) != n {
		t.Errorf("%d of %d comments were kept", len(st.Threads), n)
	}
	seen := map[string]bool{}
	for _, th := range st.Threads {
		if seen[th.ID] {
			t.Errorf("two threads were both given the id %s", th.ID)
		}
		seen[th.ID] = true
	}
	if f := st.FileFor("server.go"); f == nil || !f.Approved || f.Digest == "" {
		t.Errorf("the approval did not come through stamped: %+v", f)
	}
}

// A base that cannot be used is reported rather than returned: this is the
// command you run to find out what is wrong, so it has to answer.
func TestStatusReportsABadBase(t *testing.T) {
	repo := testRepo(t)
	got := fields(t, run(t, repo, "status", "--base", "no/such/ref"))
	if !strings.Contains(got["problem"], "no/such/ref") {
		t.Errorf("problem = %q", got["problem"])
	}
	if got["branch"] != "feature" {
		t.Error("it stopped rather than answering with what it could")
	}
}

// A base nothing pinned may move when the branch does, and says so.
func TestStatusSaysWhenTheBaseWasWorkedOut(t *testing.T) {
	repo := testRepo(t)
	if got := fields(t, run(t, repo, "status")); got["base-chosen"] != "" {
		t.Errorf("base-chosen = %q with a base set", got["base-chosen"])
	}
	run(t, repo, "base", "--auto")
	if got := fields(t, run(t, repo, "status")); got["base-chosen"] != "no" {
		t.Errorf("base-chosen = %q with none set", got["base-chosen"])
	}
}

// fields reads status's one-fact-a-line output the way a script would.
func fields(t *testing.T, out string) map[string]string {
	t.Helper()
	got := map[string]string{}
	for _, l := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if l == "" {
			continue
		}
		key, value, ok := strings.Cut(l, " ")
		if !ok {
			t.Errorf("status line %q is not a key and a value", l)
			continue
		}
		got[key] = strings.TrimSpace(value)
	}
	return got
}

// gitOut runs git and returns what it said.
func gitOut(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %v: %v", args, err)
	}
	return string(out)
}

// A review is of a branch, and the commands see the same one the window does.
func TestEachBranchHasItsOwnReview(t *testing.T) {
	repo := testRepo(t)
	run(t, repo, "comment", "--by", "git-user", "server.go:4", "on feature")

	gitRun(t, repo.Dir, "checkout", "-q", "main")
	if got := run(t, repo, "list"); !strings.Contains(got, "no comments") {
		t.Errorf("main inherited feature's comments:\n%s", got)
	}
	run(t, repo, "comment", "--by", "git-user", "server.go:1", "on main")

	gitRun(t, repo.Dir, "checkout", "-q", "feature")
	got := run(t, repo, "list")
	if !strings.Contains(got, "on feature") {
		t.Errorf("the branch's own review did not come back:\n%s", got)
	}
	if strings.Contains(got, "on main") {
		t.Errorf("the other branch's review came with it:\n%s", got)
	}
}

// Every way of getting it wrong says so, and changes nothing.
func TestRefusals(t *testing.T) {
	repo := testRepo(t)
	run(t, repo, "comment", "--by", "git-user", "server.go:4", "why?")
	before, _ := repo.ReadStateFile(review.FileName)

	for _, args := range [][]string{
		{"show", "nope"},
		{"show"},
		{"reply", "--by", "git-user", "nope", "x"},
		{"reply", "--by", "git-user", "t1"},
		{"resolve", "nope"},
		{"resolve"},
		{"delete", "nope"},
		{"delete", "t1", "9"},
		{"delete", "t1", "x"},
		{"comment", "--by", "git-user", "server.go", "no line"},
		{"comment", "--by", "git-user", "server.go:4"},
		{"approve", "--by", "git-user"},
		{"list", "--nonsense"},
		{"list", "--path"},
		{"frobnicate"},
	} {
		fails(t, repo, args...)
	}

	if after, _ := repo.ReadStateFile(review.FileName); after != before {
		t.Errorf("a refused command changed the review:\n%s", after)
	}
}

// help is a command, and names every other one.
func TestHelpNamesEveryCommand(t *testing.T) {
	var out strings.Builder
	if err := Run(testRepo(t), []string{"help"}, &out); err != nil {
		t.Fatal(err)
	}
	for _, c := range Commands {
		if !strings.Contains(out.String(), "discobox-review "+c.Name) {
			t.Errorf("help does not mention %q:\n%s", c.Name, out.String())
		}
		if !Is(c.Name) {
			t.Errorf("%q is listed but not recognised as a command", c.Name)
		}
	}
	if Is("somedir") {
		t.Error("an ordinary path was taken for a command")
	}
}

// ---------------------------------------------------------------------------
// diff
// ---------------------------------------------------------------------------

// The numbers the diff prints are the numbers a comment takes. That agreement
// is the whole point of the command: anything that had to count lines to work
// out where to put a remark would be giving back the one thing the review is
// careful about.
func TestDiffLineNumbersAreTheOnesCommentTakes(t *testing.T) {
	repo := testRepo(t)
	got := run(t, repo, "diff")

	// Find the added line and read its number straight off the output.
	var number string
	for _, l := range strings.Split(got, "\n") {
		if strings.Contains(l, "return write()") {
			number = strings.Fields(l)[0]
		}
	}
	if number == "" {
		t.Fatalf("the change is not in the diff:\n%s", got)
	}

	run(t, repo, "comment", "--by", "git-user", "server.go:"+number, "here")
	th := stored(t, repo).Threads[0]
	if strconv.Itoa(th.Line) != number {
		t.Errorf("commented at line %d, having been told %s", th.Line, number)
	}

	// And the diff then says so on that very line.
	again := run(t, repo, "diff")
	for _, l := range strings.Split(again, "\n") {
		if strings.Contains(l, "return write()") {
			if !strings.Contains(l, "<t1>") {
				t.Errorf("the line does not carry its comment: %q", l)
			}
			return
		}
	}
	t.Fatal("the line went missing")
}

func TestDiffHeadsEachFile(t *testing.T) {
	repo := testRepo(t)
	run(t, repo, "comment", "--by", "git-user", "server.go:4", "why?")
	run(t, repo, "approve", "--by", "git-user", "server.go")

	got := run(t, repo, "diff")
	head := ""
	for _, l := range strings.Split(got, "\n") {
		if strings.HasPrefix(l, "===") {
			head = l
		}
	}
	for _, want := range []string{"server.go", "+1", "−1", "approved", "1 open"} {
		if !strings.Contains(head, want) {
			t.Errorf("the heading %q is missing %q", head, want)
		}
	}
}

// A line whose change is not in a commit is marked, and one already committed
// is not — the same fact the window draws down its margin.
func TestDiffMarksWhatIsNotCommitted(t *testing.T) {
	repo := testRepo(t)
	// The fixture's change is committed; add one that is not.
	write(t, repo.Dir, "server.go",
		"package main\n\nfunc handler() error {\n\treturn write()\n}\n\nvar loose = 1\n")

	got := run(t, repo, "diff")
	var committed, uncommitted string
	for _, l := range strings.Split(got, "\n") {
		switch {
		case strings.Contains(l, "return write()"):
			committed = l
		case strings.Contains(l, "var loose"):
			uncommitted = l
		}
	}
	if committed == "" || uncommitted == "" {
		t.Fatalf("diff:\n%s", got)
	}
	if strings.Contains(committed, "!") {
		t.Errorf("a committed line is marked: %q", committed)
	}
	if !strings.Contains(uncommitted, "!") {
		t.Errorf("an uncommitted line is not marked: %q", uncommitted)
	}
}

func TestDiffStatAndRaw(t *testing.T) {
	repo := testRepo(t)

	stat := run(t, repo, "diff", "--stat")
	if !strings.Contains(stat, "server.go") {
		t.Errorf("--stat = %q", stat)
	}
	if strings.Contains(stat, "func handler") {
		t.Errorf("--stat printed the diff itself:\n%s", stat)
	}

	// --raw is a patch, and has to parse back as one.
	raw := run(t, repo, "diff", "--raw")
	patches := git.ParseMulti(raw)
	if len(patches) != 1 || patches[0].Path != "server.go" {
		t.Fatalf("--raw does not parse as a patch:\n%s", raw)
	}
	if adds, dels := patches[0].Stat(); adds != 1 || dels != 1 {
		t.Errorf("--raw says +%d −%d, want +1 −1", adds, dels)
	}
	if strings.Contains(raw, "===") {
		t.Errorf("--raw carries discobox-review's own headings:\n%s", raw)
	}
}

func TestDiffFiltersAndBase(t *testing.T) {
	repo := testRepo(t)
	if got := run(t, repo, "diff", "--path", "nothing.go"); !strings.Contains(got, "nothing to review") {
		t.Errorf("--path on an unchanged file = %q", got)
	}
	if got := run(t, repo, "diff", "--path", "server.go"); !strings.Contains(got, "server.go") {
		t.Errorf("--path on the changed one = %q", got)
	}
	// Measured from HEAD there is nothing: the change is committed. And the
	// flag is a one-off — it must not have moved the saved base.
	if got := run(t, repo, "diff", "--base", "HEAD"); !strings.Contains(got, "nothing to review") {
		t.Errorf("from HEAD = %q", got)
	}
	if got := stored(t, repo).Base; got != "main" {
		t.Errorf("--base changed the saved base to %q", got)
	}
	// And more context shows more of the file.
	narrow := run(t, repo, "diff", "--context", "0")
	wide := run(t, repo, "diff", "--context", "3")
	if len(strings.Split(wide, "\n")) <= len(strings.Split(narrow, "\n")) {
		t.Errorf("--context did not widen the diff:\n%s\n---\n%s", narrow, wide)
	}
	fails(t, repo, "diff", "--context", "x")
	fails(t, repo, "diff", "--base", "no/such/ref")
}

// The base is part of the review, so setting it is saved and reported; the
// flag is the other thing — one diff seen from somewhere else.
func TestBaseIsSavedAndShown(t *testing.T) {
	repo := testRepo(t)
	if got := run(t, repo, "base"); strings.TrimSpace(got) != "main" {
		t.Errorf("base = %q, want the one the fixture set", got)
	}
	run(t, repo, "base", "HEAD~1")
	if got := stored(t, repo).Base; got != "HEAD~1" {
		t.Errorf("the review says base %q", got)
	}
	run(t, repo, "base", "--auto")
	if got := stored(t, repo).Base; got != "" {
		t.Errorf("--auto left base %q", got)
	}
	if got := run(t, repo, "base"); !strings.Contains(got, "whatever this branch") {
		t.Errorf("base with none set says %q", got)
	}
	fails(t, repo, "base", "one", "two")
}

// ---------------------------------------------------------------------------
// Help
// ---------------------------------------------------------------------------

// Every command answers for itself, and answers the same however it is asked.
func TestEveryCommandHasItsOwnHelp(t *testing.T) {
	repo := testRepo(t)
	for _, c := range Commands {
		viaFlag := run(t, repo, c.Name, "--help")
		viaWord := run(t, repo, "help", c.Name)
		if viaFlag != viaWord {
			t.Errorf("%s --help and help %s differ:\n%s\n---\n%s",
				c.Name, c.Name, viaFlag, viaWord)
		}
		if short := run(t, repo, c.Name, "-h"); short != viaFlag {
			t.Errorf("%s -h differs from --help", c.Name)
		}
		if !strings.Contains(viaFlag, "discobox-review "+c.Name) {
			t.Errorf("%s --help does not name itself:\n%s", c.Name, viaFlag)
		}
		if !strings.Contains(viaFlag, c.What) {
			t.Errorf("%s --help does not say what it is for:\n%s", c.Name, viaFlag)
		}
		for _, f := range c.Flags {
			if !strings.Contains(viaFlag, "--"+f) {
				t.Errorf("%s --help does not mention its own --%s", c.Name, f)
			}
		}
	}
}

// Asking for help must never do the thing instead.
func TestHelpChangesNothing(t *testing.T) {
	repo := testRepo(t)
	run(t, repo, "comment", "--by", "git-user", "server.go:4", "why?")
	before, _ := repo.ReadStateFile(review.FileName)

	for _, c := range Commands {
		run(t, repo, c.Name, "--help")
	}
	if after, _ := repo.ReadStateFile(review.FileName); after != before {
		t.Errorf("asking for help changed the review:\n%s", after)
	}
}

// Nothing writes a remark without being told whose it is. The name is not
// guessed, because an agent running these commands would otherwise sign the
// person's name to its own words; a person says git-user and gets theirs.
func TestEveryRemarkIsSigned(t *testing.T) {
	repo := testRepo(t)

	for _, args := range [][]string{
		{"comment", "server.go:4", "why?"},
		{"reply", "t1", "because"},
		{"approve", "server.go"},
	} {
		fails(t, repo, args...)
		var out strings.Builder
		err := Run(repo, args, &out)
		if err == nil || !strings.Contains(err.Error(), GitUser) {
			t.Errorf("%s refused with %v, want it to name --by %s", args[0], err, GitUser)
		}
	}
	if st := stored(t, repo); len(st.Threads) != 0 || len(st.Files) != 0 {
		t.Errorf("a command with no name to sign with wrote to the review anyway: %+v", st)
	}

	// git-user is the one name discobox-review looks up rather than takes as given.
	run(t, repo, "comment", "--by", GitUser, "server.go:4", "why?")
	run(t, repo, "approve", "--by", GitUser, "server.go")
	st := stored(t, repo)
	if by := st.Threads[0].Comments[0].By; by != "Test" {
		t.Errorf("--by %s filed it under %q, want git's own user.name", GitUser, by)
	}
	if by := st.FileFor("server.go").By; by != "Test" {
		t.Errorf("--by %s approved as %q, want git's own user.name", GitUser, by)
	}

	// Any other name is taken as it is written.
	run(t, repo, "reply", "--by", "claude", "t1", "fixing it")
	if by := stored(t, repo).Thread("t1").Comments[1].By; by != "claude" {
		t.Errorf("--by claude filed it under %q", by)
	}
}

// A flag belongs to the commands that take it, and is refused by the rest —
// quietly ignoring one would mean a command that did not do what it was told.
func TestFlagsBelongToTheirCommands(t *testing.T) {
	repo := testRepo(t)
	fails(t, repo, "list", "--stat")
	fails(t, repo, "comment", "--by", "git-user", "--raw", "server.go:4", "x")
	fails(t, repo, "approve", "--by", "git-user", "--open", "server.go")
	fails(t, repo, "diff", "--by", "someone")

	// And the ones that do belong are taken.
	run(t, repo, "diff", "--stat")
	run(t, repo, "list", "--open")
	run(t, repo, "comment", "--by", "claude", "server.go:4", "x")

	// The refusal says where to look.
	var out strings.Builder
	err := Run(repo, []string{"list", "--stat"}, &out)
	if err == nil || !strings.Contains(err.Error(), "discobox-review list --help") {
		t.Errorf("refusal = %v, want it to point at the command's own help", err)
	}
}

// The table is the only place a command is described, so the two halves of it
// have to agree with each other.
func TestTheCommandTableIsConsistent(t *testing.T) {
	for _, c := range Commands {
		for _, f := range c.Flags {
			if flagHelp[f] == "" {
				t.Errorf("%s takes --%s, which nothing describes", c.Name, f)
			}
			if !c.takes(f) {
				t.Errorf("%s lists --%s but does not take it", c.Name, f)
			}
		}
		// A flag shown in the one-line summary has to be one the command
		// really accepts, or the listing is telling people the wrong thing.
		for _, word := range strings.Fields(c.Args) {
			name := strings.TrimLeft(strings.Trim(word, "[]"), "-")
			if !strings.HasPrefix(strings.Trim(word, "[]"), "--") {
				continue
			}
			if !c.takes(name) {
				t.Errorf("%s advertises --%s in %q but does not take it",
					c.Name, name, c.Args)
			}
		}
	}
	// Nothing is described that no command takes.
	for f := range flagHelp {
		used := false
		for _, c := range Commands {
			used = used || c.takes(f)
		}
		if !used {
			t.Errorf("--%s is described but no command takes it", f)
		}
	}
}

func TestHelpForNothingInParticular(t *testing.T) {
	repo := testRepo(t)
	// Bare help still lists them all, and points at the per-command pages.
	got := run(t, repo, "help")
	for _, c := range Commands {
		if !strings.Contains(got, "discobox-review "+c.Name) {
			t.Errorf("the listing is missing %q", c.Name)
		}
	}
	if !strings.Contains(got, "--help") {
		t.Error("the listing does not say there is more")
	}
	fails(t, repo, "help", "nope")
}
