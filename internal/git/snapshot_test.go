package git

import (
	"strings"
	"testing"
)

// A snapshot is the change up to its commit: what the working tree has on top
// of it, committed or not, is not in it.
func TestSnapshotEndsAtItsCommit(t *testing.T) {
	r := testRepo(t)
	withRemote(t, r, true)
	run(t, r, "checkout", "-q", "-b", "feature")
	write(t, r.Dir, "foo.txt", "one\nTWO\nthree\nfour\nfive\n")
	run(t, r, "commit", "-qam", "rename two to TWO")
	head := strings.TrimSpace(run(t, r, "rev-parse", "HEAD"))
	write(t, r.Dir, "foo.txt", "one\nTWO\nTHREE\nfour\nfive\n")
	run(t, r, "commit", "-qam", "after the head")
	write(t, r.Dir, "foo.txt", "one\nTWO\nTHREE\nFOUR\nfive\n")

	s, err := r.Snapshot(head)
	if err != nil {
		t.Fatal(err)
	}
	scan, err := s.Scan("", DefaultContext)
	if err != nil {
		t.Fatal(err)
	}
	if scan.Branch != "feature" {
		t.Errorf("branch = %q, want the one checked out", scan.Branch)
	}
	if scan.Base.Ref != "origin/main" || scan.Base.Ahead != 1 {
		t.Errorf("base = %+v, want origin/main, one ahead", scan.Base)
	}
	if len(scan.Commits) != 1 || scan.Commits[0].Hash != head {
		t.Errorf("commits = %+v, want only the head", scan.Commits)
	}
	if !strings.Contains(scan.Diff, "+TWO") || strings.Contains(scan.Diff, "THREE") || strings.Contains(scan.Diff, "FOUR") {
		t.Errorf("diff is not base to head:\n%s", scan.Diff)
	}
	if len(scan.Files) != 0 || scan.Pending != "" {
		t.Errorf("a commit has nothing uncommitted: files %+v, pending %q", scan.Files, scan.Pending)
	}

	text, err := s.FileContent("", "foo.txt")
	if err != nil || text != "one\nTWO\nthree\nfour\nfive\n" {
		t.Errorf("new side of foo.txt = %q, %v; want the head's", text, err)
	}
	carry, err := s.CarryDiff("main", "foo.txt")
	if err != nil || !strings.Contains(carry, "+TWO") || strings.Contains(carry, "THREE") {
		t.Errorf("carry diff = %q, %v; want main to the head", carry, err)
	}
}

// A base named by commit, as a pull request's is, is measured from where the
// head left it.
func TestSnapshotMeasuresFromTheMergeBase(t *testing.T) {
	r := testRepo(t)
	fork := strings.TrimSpace(run(t, r, "rev-parse", "HEAD"))
	run(t, r, "checkout", "-q", "-b", "feature")
	write(t, r.Dir, "feature.txt", "feature\n")
	run(t, r, "add", "feature.txt")
	run(t, r, "commit", "-qm", "feature")
	head := strings.TrimSpace(run(t, r, "rev-parse", "HEAD"))
	run(t, r, "checkout", "-q", "main")
	write(t, r.Dir, "main.txt", "main\n")
	run(t, r, "add", "main.txt")
	run(t, r, "commit", "-qm", "main moves on")
	base := strings.TrimSpace(run(t, r, "rev-parse", "HEAD"))

	s, err := r.Snapshot(head)
	if err != nil {
		t.Fatal(err)
	}
	scan, err := s.Scan(base, DefaultContext)
	if err != nil {
		t.Fatal(err)
	}
	if scan.Base.SHA != fork || scan.Base.Ahead != 1 || scan.Base.Behind != 1 || scan.Base.Auto {
		t.Errorf("base = %+v, want the fork, one each way, as asked", scan.Base)
	}
	if !strings.Contains(scan.Diff, "feature.txt") || strings.Contains(scan.Diff, "main.txt") {
		t.Errorf("diff carries the base's own change:\n%s", scan.Diff)
	}
}

func TestSnapshotOfNoCommit(t *testing.T) {
	r := testRepo(t)
	if _, err := r.Snapshot("no/such/ref"); err == nil {
		t.Fatal("a snapshot of nothing was made")
	}
}

// A base commit this repository has not fetched is measured from the fallback
// the caller named — a pull request's base branch — and says so, rather than
// from the branch's own upstream, which for a pushed branch is itself.
func TestAMissingBaseFallsBackToTheNamedRef(t *testing.T) {
	r := testRepo(t)
	withRemote(t, r, true)
	run(t, r, "checkout", "-q", "-b", "feature")
	write(t, r.Dir, "foo.txt", "one\nTWO\nthree\nfour\nfive\n")
	run(t, r, "commit", "-qam", "rename two to TWO")
	run(t, r, "push", "-q", "-u", "origin", "feature")
	head := strings.TrimSpace(run(t, r, "rev-parse", "HEAD"))
	const unfetched = "1234567890abcdef1234567890abcdef12345678"

	s, err := r.Snapshot(head)
	if err != nil {
		t.Fatal(err)
	}
	scan, err := s.Scan(unfetched, DefaultContext)
	if err != nil {
		t.Fatal(err)
	}
	if scan.Base.Ref != "origin/feature" || scan.Diff != "" {
		t.Fatalf("with no fallback the base is %q; the test wants the upstream, which is the branch itself", scan.Base.Ref)
	}

	r.FallbackBase = "origin/main"
	scan, err = s.Scan(unfetched, DefaultContext)
	if err != nil {
		t.Fatal(err)
	}
	if scan.Base.Ref != "origin/main" || scan.Base.Ahead != 1 || !strings.Contains(scan.Diff, "+TWO") {
		t.Errorf("base = %+v, want origin/main one behind the head, with its change", scan.Base)
	}
	if !strings.Contains(scan.Base.Err, unfetched) || !strings.Contains(scan.Base.Err, "measured from origin/main") {
		t.Errorf("err = %q, want it to name the missing base and what was used", scan.Base.Err)
	}

	// A fallback that is not there either is said too.
	r.FallbackBase = "origin/nope"
	scan, _ = s.Scan(unfetched, DefaultContext)
	if !strings.Contains(scan.Base.Err, "nor origin/nope") {
		t.Errorf("err = %q, want it to say the fallback is not there either", scan.Base.Err)
	}
	r.FallbackBase = "origin/main"

	// A base that is there is used, fallback or not.
	scan, _ = s.Scan("main", DefaultContext)
	if scan.Base.Ref != "main" || scan.Base.Err != "" {
		t.Errorf("a base that is here: %+v", scan.Base)
	}
}
