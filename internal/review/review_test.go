package review

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/discobox-ai/difftui/internal/git"
)

const patchText = `diff --git a/foo.txt b/foo.txt
--- a/foo.txt
+++ b/foo.txt
@@ -1,3 +1,3 @@
 one
-two
+TWO
 three
`

// ---------------------------------------------------------------------------
// The file itself
// ---------------------------------------------------------------------------

// The review is an interface, so what it reads as matters as much as what it
// holds: a plain object, one entry per file, one per conversation.
func TestBytesAreReadable(t *testing.T) {
	s := New("feature/x")
	s.Base = "origin/main"
	s.Approve("a.go", "deadbeef", "Darren")
	s.Start(&Thread{Path: "b.go", Side: SideNew, Line: 12, Text: "\treturn nil"},
		"Darren", "Why nil?")

	var doc map[string]any
	if err := json.Unmarshal(s.Bytes(), &doc); err != nil {
		t.Fatalf("what difftui wrote is not JSON: %v\n%s", err, s.Bytes())
	}
	if doc["branch"] != "feature/x" || doc["base"] != "origin/main" {
		t.Errorf("branch/base = %v / %v", doc["branch"], doc["base"])
	}
	if !strings.HasSuffix(string(s.Bytes()), "\n") {
		t.Error("the file does not end in a newline")
	}
	// Indented, so a person can read it and a diff of it is useful.
	if !strings.Contains(string(s.Bytes()), "\n  \"files\": [") {
		t.Errorf("not indented:\n%s", s.Bytes())
	}
}

// Saving the same state twice gives the same bytes, so a file only changes
// when the review does.
func TestBytesAreStable(t *testing.T) {
	s := New("main")
	s.Approve("z.go", "1", "Darren")
	s.Approve("a.go", "2", "Darren")
	first := string(s.Bytes())
	if second := string(s.Bytes()); second != first {
		t.Errorf("two saves differ:\n%s\n---\n%s", first, second)
	}
	// Files come out sorted, whatever order they went in.
	if strings.Index(first, `"a.go"`) > strings.Index(first, `"z.go"`) {
		t.Errorf("files are not in path order:\n%s", first)
	}
}

func TestParseRoundTrips(t *testing.T) {
	s := New("main")
	s.Approve("a.go", "digest", "Darren")
	th := s.Start(&Thread{Path: "b.go", Side: SideOld, Line: 4}, "Darren", "hm")
	s.Reply(th.ID, "claude", "fixed")
	s.SetResolved(th.ID, true)

	back, err := Parse(s.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !back.Approved("a.go") {
		t.Error("the approval did not survive")
	}
	got := back.Thread(th.ID)
	if got == nil || len(got.Comments) != 2 || !got.Resolved || got.Side != SideOld {
		t.Errorf("thread came back as %+v", got)
	}
}

// A file caught halfway through being written is a moment, not a reason to
// throw a review away — so it is an error rather than an empty review.
func TestParseRefusesRubbish(t *testing.T) {
	for _, bad := range []string{"", "   ", `{"version":1,`, "not json at all"} {
		if _, err := Parse([]byte(bad)); err == nil {
			t.Errorf("Parse(%q) was accepted", bad)
		}
	}
}

// The point of the format is that something else can write it. The smallest
// thing an agent could reasonably write has to work.
func TestAHandWrittenReviewIsEnough(t *testing.T) {
	s, err := Parse([]byte(`{
	  "files": [{"path": "a.go", "approved": true}],
	  "threads": [
	    {"path": "b.go", "line": 7, "comments": [{"by": "claude", "body": "look here"}]}
	  ]
	}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.Version != Version {
		t.Errorf("version = %d, want %d filled in", s.Version, Version)
	}
	if !s.Approved("a.go") {
		t.Error("an approval written without a digest was not honoured")
	}
	if len(s.Threads) != 1 {
		t.Fatalf("threads = %+v", s.Threads)
	}
	tr := s.Threads[0]
	if tr.ID == "" {
		t.Error("a thread written without an id did not get one")
	}
	if tr.Side != SideNew {
		t.Errorf("side = %q, want the working tree side by default", tr.Side)
	}
}

// Two threads that arrive with the same id are not allowed to stay that way:
// every command in the window names a thread by its id.
func TestParseMakesIdsUnique(t *testing.T) {
	s, err := Parse([]byte(`{"threads":[
	  {"id":"t1","path":"a.go","line":1,"comments":[]},
	  {"id":"t1","path":"a.go","line":2,"comments":[]}
	]}`))
	if err != nil {
		t.Fatal(err)
	}
	if s.Threads[0].ID == s.Threads[1].ID {
		t.Errorf("duplicate ids survived: %q and %q", s.Threads[0].ID, s.Threads[1].ID)
	}
}

// ---------------------------------------------------------------------------
// Approvals and the digest
// ---------------------------------------------------------------------------

// The digest is of the change, not of how it is being shown, so turning the
// diff context up and down must not nullify a single approval.
func TestDigestIgnoresContext(t *testing.T) {
	narrow := git.ParsePatch("--- a/x\n+++ b/x\n@@ -2,1 +2,1 @@\n-two\n+TWO\n")
	wide := git.ParsePatch("--- a/x\n+++ b/x\n@@ -1,3 +1,3 @@\n one\n-two\n+TWO\n three\n")
	if Digest(narrow) != Digest(wide) {
		t.Errorf("the same change digests differently at two contexts:\n%s\n%s",
			Digest(narrow), Digest(wide))
	}
}

// …and a change of one character in one line must nullify exactly that file.
func TestDigestFollowsTheChange(t *testing.T) {
	a := git.ParsePatch(patchText)
	b := git.ParsePatch(strings.Replace(patchText, "+TWO", "+Two", 1))
	if Digest(a) == Digest(b) {
		t.Error("two different changes have the same digest")
	}
	// The line number is part of it: the same text moved is a different change.
	c := git.ParsePatch(strings.Replace(patchText, "@@ -1,3 +1,3 @@", "@@ -9,3 +9,3 @@", 1))
	if Digest(a) == Digest(c) {
		t.Error("the same edit at a different line has the same digest")
	}
	if Digest(nil) != "" {
		t.Error("a missing patch has a digest")
	}
}

// An approval is a claim about a particular change. When the file moves on,
// the claim falls — and it says so, rather than looking as though it was never
// given.
func TestReconcileNullifiesAStaleApproval(t *testing.T) {
	s := New("main")
	s.Approve("a.go", "before", "Darren")

	changed, nullified := s.Reconcile(map[string]string{"a.go": "after"})
	if !changed {
		t.Fatal("Reconcile did not report a change")
	}
	if len(nullified) != 1 || nullified[0] != "a.go" {
		t.Errorf("nullified = %v", nullified)
	}
	if s.Approved("a.go") {
		t.Error("the approval survived the file changing")
	}
	if !s.Nullified("a.go") {
		t.Error("nothing records that the approval fell rather than never being given")
	}
	if f := s.FileFor("a.go"); f.Digest != "before" {
		t.Errorf("the digest that was approved was lost: %q", f.Digest)
	}
}

// An approval whose digest still matches is left alone, and reconciling twice
// changes nothing the second time.
func TestReconcileLeavesAGoodApprovalAlone(t *testing.T) {
	s := New("main")
	s.Approve("a.go", "same", "Darren")
	if changed, _ := s.Reconcile(map[string]string{"a.go": "same"}); changed {
		t.Error("an unchanged file was reported as changed")
	}
	if !s.Approved("a.go") {
		t.Error("the approval was dropped")
	}
}

// An agent approves a file by writing "approved": true and nothing else.
// Stamping the digest in is what makes that approval nullifiable afterwards.
func TestReconcileStampsAnApprovalWrittenByHand(t *testing.T) {
	s, err := Parse([]byte(`{"files":[{"path":"a.go","approved":true}]}`))
	if err != nil {
		t.Fatal(err)
	}
	changed, nullified := s.Reconcile(map[string]string{"a.go": "current"})
	if !changed || len(nullified) != 0 {
		t.Errorf("changed=%v nullified=%v; want a stamp, not a nullification", changed, nullified)
	}
	if f := s.FileFor("a.go"); f.Digest != "current" {
		t.Errorf("digest = %q, want the current one", f.Digest)
	}
	if !s.Approved("a.go") {
		t.Error("stamping the digest dropped the approval")
	}
	// And now it behaves like any other approval.
	if _, nullified := s.Reconcile(map[string]string{"a.go": "other"}); len(nullified) != 1 {
		t.Error("the stamped approval did not fall when the file changed")
	}
}

// A file that is not in the review at all — the base moved, or the change was
// undone — keeps its approval rather than losing it to a comparison that
// cannot be made.
func TestReconcileIgnoresAFileNotInTheReview(t *testing.T) {
	s := New("main")
	s.Approve("a.go", "before", "Darren")
	if changed, _ := s.Reconcile(map[string]string{"b.go": "x"}); changed {
		t.Error("a file outside the review was reconciled anyway")
	}
	if !s.Approved("a.go") {
		t.Error("the approval was dropped for a file that is not being shown")
	}
}

// Withdrawing an approval is not the same as one falling: the reviewer changed
// their mind, and the file is not marked as having moved.
func TestUnapproveIsNotANullification(t *testing.T) {
	s := New("main")
	s.Approve("a.go", "d", "Darren")
	s.Unapprove("a.go")
	if s.Approved("a.go") {
		t.Error("still approved")
	}
	if s.Nullified("a.go") {
		t.Error("withdrawing an approval marked the file as having changed")
	}
}

// ---------------------------------------------------------------------------
// Threads
// ---------------------------------------------------------------------------

func TestThreadIdsCountUp(t *testing.T) {
	s := New("main")
	a := s.Start(&Thread{Path: "a.go", Line: 1}, "Darren", "one")
	b := s.Start(&Thread{Path: "a.go", Line: 2}, "Darren", "two")
	if a.ID != "t1" || b.ID != "t2" {
		t.Errorf("ids = %q, %q; want t1, t2", a.ID, b.ID)
	}
	// An id an agent chose is left alone, and the next one still does not
	// collide with it.
	s.Start(&Thread{ID: "t9", Path: "a.go", Line: 3}, "claude", "nine")
	if next := s.NextID(); next != "t10" {
		t.Errorf("NextID after t9 = %q, want t10", next)
	}
}

func TestCounting(t *testing.T) {
	s := New("main")
	s.Start(&Thread{Path: "a.go", Line: 1}, "Darren", "one")
	closed := s.Start(&Thread{Path: "a.go", Line: 2}, "Darren", "two")
	s.Start(&Thread{Path: "b.go", Line: 3}, "Darren", "three")
	s.SetResolved(closed.ID, true)

	if got := s.Open(); got != 2 {
		t.Errorf("Open() = %d, want 2", got)
	}
	total, open := s.CountFor("a.go")
	if total != 2 || open != 1 {
		t.Errorf("CountFor(a.go) = %d/%d, want 2/1", total, open)
	}
	if total, open := s.CountFor("b.go"); total != 1 || open != 1 {
		t.Errorf("CountFor(b.go) = %d/%d, want 1/1", total, open)
	}
}

func TestRemoveAndReply(t *testing.T) {
	s := New("main")
	th := s.Start(&Thread{Path: "a.go", Line: 1}, "Darren", "why?")
	if !s.Reply(th.ID, "claude", "because") {
		t.Fatal("Reply on a live thread failed")
	}
	if s.Reply("nope", "claude", "x") {
		t.Error("Reply to a thread that is not there succeeded")
	}
	if got := s.Thread(th.ID).Author(); got != "Darren" {
		t.Errorf("Author() = %q, want the person who started it", got)
	}
	if !s.Remove(th.ID) || s.Thread(th.ID) != nil {
		t.Error("Remove did not remove")
	}
}

// A thread with no line to hang off is still a thread: it is shown under its
// file rather than beside a line that is not there.
func TestUnanchoredThread(t *testing.T) {
	th := &Thread{Path: "a.go", Line: 0}
	if th.Anchored() {
		t.Error("a thread with no line claims to be anchored")
	}
	if (&Thread{Path: "a.go", Line: 3}).Anchored() != true {
		t.Error("a thread on line 3 is not anchored")
	}
}

// ---------------------------------------------------------------------------
// Carrying a comment between a commit and the working tree
// ---------------------------------------------------------------------------

// carry is the diff from a commit to the working tree: its old side is the
// commit, its new side the tree. A comment written while reading the commit is
// carried into the tree's numbering there and then.
func TestCarryOntoTheWorkingTree(t *testing.T) {
	carry := git.ParsePatch("--- a/x\n+++ b/x\n@@ -0,0 +1,2 @@\n+added\n+lines\n")
	got, ok := Carry(carry, SideNew, 3)
	if !ok || got != 5 {
		t.Errorf("Carry(new 3) = %d, %v; want 5, true", got, ok)
	}
	back, ok := CarryBack(carry, SideNew, 5)
	if !ok || back != 3 {
		t.Errorf("CarryBack(new 5) = %d, %v; want 3, true", back, ok)
	}
}

// A line the working tree no longer has cannot be carried, and the comment on
// it becomes one of the file's loose remarks rather than being lost.
func TestCarryFailsForALineThatIsGone(t *testing.T) {
	carry := git.ParsePatch("--- a/x\n+++ b/x\n@@ -3,1 +3,0 @@\n-gone\n")
	if _, ok := Carry(carry, SideNew, 3); ok {
		t.Error("a line the working tree no longer has was carried anyway")
	}
	if _, ok := Carry(nil, SideNew, 3); ok {
		t.Error("carrying without a diff succeeded")
	}
	// The base's own numbering is neither end of this diff, so it is refused
	// rather than answered wrongly.
	if _, ok := Carry(carry, SideOld, 3); ok {
		t.Error("an old-side line was carried through a diff that does not reach the base")
	}
}

func TestArchiveNameIsAFileName(t *testing.T) {
	got := ArchiveName("feature/deep/branch")
	if strings.ContainsAny(strings.TrimSuffix(got, ".json"), "/\\:") {
		t.Errorf("ArchiveName = %q, which is not one file name", got)
	}
	if !strings.HasSuffix(got, ".json") {
		t.Errorf("ArchiveName = %q, want a .json file", got)
	}
}

// A one-line comment says nothing about how many lines it covers: that is the
// default, and the file is read by people as well as programs.
func TestOneLineCommentsSayNothingAboutLines(t *testing.T) {
	s := New("main")
	s.Start(&Thread{Path: "a.go", Line: 4, Lines: 1}, "Darren", "hm")
	s.Start(&Thread{Path: "a.go", Line: 8, Lines: 3}, "Darren", "block")

	if strings.Count(string(s.Bytes()), `"lines"`) != 1 {
		t.Errorf("want exactly one thread carrying a line count:\n%s", s.Bytes())
	}
	if first, last := s.Threads[0].Span(); first != 4 || last != 4 {
		t.Errorf("Span() = %d–%d, want 4–4", first, last)
	}
	if first, last := s.Threads[1].Span(); first != 8 || last != 10 {
		t.Errorf("Span() = %d–%d, want 8–10", first, last)
	}
}
