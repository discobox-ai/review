package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"github.com/discobox-ai/difftui/internal/git"
	"github.com/discobox-ai/difftui/internal/review"
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
		t.Fatalf("difftui %s: %v", strings.Join(args, " "), err)
	}
	return out.String()
}

// fails runs a command that should not work.
func fails(t *testing.T, repo *git.Repo, args ...string) {
	t.Helper()
	var out strings.Builder
	if err := Run(repo, args, &out); err == nil {
		t.Errorf("difftui %s succeeded, printing %q", strings.Join(args, " "), out.String())
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

	if got := run(t, repo, "comment", "server.go:4", "why not check the error?"); !strings.HasPrefix(got, "t1") {
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

// A block comment is written the way a place in a review is written everywhere
// else.
func TestCommentOnARangeOfLines(t *testing.T) {
	repo := testRepo(t)
	run(t, repo, "comment", "server.go:4-6", "this whole block")
	th := stored(t, repo).Threads[0]
	if first, last := th.Span(); first != 4 || last != 6 {
		t.Errorf("Span() = %d–%d, want 4–6", first, last)
	}
	run(t, repo, "comment", "server.go:2", "--side", "old", "and this")
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
	run(t, repo, "comment", "server.go:4", "first")
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
	run(t, repo, "comment", "server.go:4", "why?")
	run(t, repo, "reply", "t1", "--by", "claude", "because")

	got := run(t, repo, "show", "t1")
	for _, want := range []string{"t1", "server.go:4", "open", "why?", "because", "claude", "[1]"} {
		if !strings.Contains(got, want) {
			t.Errorf("show is missing %q:\n%s", want, got)
		}
	}
}

// Approving from a command leaves the digest out, so difftui stamps in the
// change as it stands when it next reads the review — which is what makes an
// approval given here nullifiable like any other.
func TestApproving(t *testing.T) {
	repo := testRepo(t)
	run(t, repo, "approve", "server.go", "other.go")
	st := stored(t, repo)
	if !st.Approved("server.go") || !st.Approved("other.go") {
		t.Fatalf("files = %+v", st.Files)
	}
	if got := st.FileFor("server.go"); got.Digest != "" {
		t.Errorf("digest = %q, want it left for difftui to stamp", got.Digest)
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

func TestStatus(t *testing.T) {
	repo := testRepo(t)
	run(t, repo, "comment", "server.go:4", "why?")
	run(t, repo, "comment", "server.go:5", "and this")
	run(t, repo, "resolve", "t2")
	run(t, repo, "approve", "server.go")

	got := run(t, repo, "status")
	for _, want := range []string{"feature", "approved  1 file", "comments  2, 1 open"} {
		if !strings.Contains(got, want) {
			t.Errorf("status is missing %q:\n%s", want, got)
		}
	}
}

// A review is of a branch, and the commands see the same one the window does.
func TestEachBranchHasItsOwnReview(t *testing.T) {
	repo := testRepo(t)
	run(t, repo, "comment", "server.go:4", "on feature")

	gitRun(t, repo.Dir, "checkout", "-q", "main")
	if got := run(t, repo, "list"); !strings.Contains(got, "no comments") {
		t.Errorf("main inherited feature's comments:\n%s", got)
	}
	run(t, repo, "comment", "server.go:1", "on main")

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
	run(t, repo, "comment", "server.go:4", "why?")
	before, _ := repo.ReadStateFile(review.FileName)

	for _, args := range [][]string{
		{"show", "nope"},
		{"show"},
		{"reply", "nope", "x"},
		{"reply", "t1"},
		{"resolve", "nope"},
		{"resolve"},
		{"delete", "nope"},
		{"delete", "t1", "9"},
		{"delete", "t1", "x"},
		{"comment", "server.go", "no line"},
		{"comment", "server.go:4"},
		{"approve"},
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
		if !strings.Contains(out.String(), "difftui "+c.Name) {
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

	run(t, repo, "comment", "server.go:"+number, "here")
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
	run(t, repo, "comment", "server.go:4", "why?")
	run(t, repo, "approve", "server.go")

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
		t.Errorf("--raw carries difftui's own headings:\n%s", raw)
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
