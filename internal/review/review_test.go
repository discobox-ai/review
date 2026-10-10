package review

import (
	"encoding/json"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/discobox-ai/review/internal/git"
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
		t.Fatalf("what discobox-review wrote is not JSON: %v\n%s", err, s.Bytes())
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

// fakeFiles is a git directory's worth of files, for the store's own tests.
type fakeFiles map[string]string

func (f fakeFiles) ReadStateFile(name string) (string, bool) { s, ok := f[name]; return s, ok }
func (f fakeFiles) WriteStateFile(name, content string) error {
	f[name] = content
	return nil
}
func (f fakeFiles) RenameStateFile(from, to string) error {
	if s, ok := f[from]; ok {
		f[to] = s
		delete(f, from)
	}
	return nil
}
func (f fakeFiles) StateFileExists(name string) bool { _, ok := f[name]; return ok }
func (f fakeFiles) LockStateFile(string, time.Duration) (func(), error) {
	return func() {}, nil
}

// loggedFiles is fakeFiles that writes down what was asked of it, for the
// tests that are about the order things happen in.
type loggedFiles struct {
	fakeFiles
	log []string
}

func (f *loggedFiles) ReadStateFile(name string) (string, bool) {
	f.log = append(f.log, "read "+name)
	return f.fakeFiles.ReadStateFile(name)
}
func (f *loggedFiles) WriteStateFile(name, content string) error {
	f.log = append(f.log, "write "+name)
	return f.fakeFiles.WriteStateFile(name, content)
}
func (f *loggedFiles) RenameStateFile(from, to string) error {
	f.log = append(f.log, "rename "+from)
	return f.fakeFiles.RenameStateFile(from, to)
}
func (f *loggedFiles) LockStateFile(name string, _ time.Duration) (func(), error) {
	f.log = append(f.log, "lock "+name)
	return func() { f.log = append(f.log, "unlock") }, nil
}

// An update reads the review and writes it back with the lock held from the
// one to the other, which is the whole of what stops two of them losing each
// other's work.
func TestAnUpdateHoldsTheLockFromTheReadToTheWrite(t *testing.T) {
	fs := &loggedFiles{fakeFiles: fakeFiles{FileName: string(New("main").Bytes())}}

	err := Update(fs, "main", 0, func(st *State) error {
		st.Start(&Thread{Path: "a.go", Line: 1}, "Darren", "a remark")
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	log := strings.Join(fs.log, ", ")
	if fs.log[0] != "lock "+LockName || fs.log[len(fs.log)-1] != "unlock" {
		t.Errorf("the lock does not go round everything: %s", log)
	}
	read, wrote := strings.Index(log, "read "+FileName), strings.Index(log, "write "+FileName)
	if read < 0 || wrote < read {
		t.Errorf("want the review read and then written, got: %s", log)
	}
	if strings.Count(log, "lock "+LockName) != 1 {
		t.Errorf("the lock was taken more than once, and it is not re-entrant: %s", log)
	}
	if st, _ := Load(fs, "main", 0); len(st.Threads) != 1 {
		t.Errorf("the change was not saved: %+v", st.Threads)
	}
}

// The file moves only when the review does: a change that changed nothing, or
// one that failed, writes nothing.
func TestAnUpdateThatChangesNothingWritesNothing(t *testing.T) {
	fs := &loggedFiles{fakeFiles: fakeFiles{FileName: string(New("main").Bytes())}}

	if err := Update(fs, "main", 0, func(*State) error { return nil }); err != nil {
		t.Fatal(err)
	}
	refused := errors.New("no such thread")
	err := Update(fs, "main", 0, func(st *State) error {
		st.Approve("a.go", "d", "Darren")
		return refused
	})
	if err != refused {
		t.Errorf("err = %v, want the change's own error handed back", err)
	}
	if log := strings.Join(fs.log, ", "); strings.Contains(log, "write") {
		t.Errorf("the file was written: %s", log)
	}

	// A review that is not there yet is written by the first thing that asks,
	// so that there is a file saying which branch it is of.
	empty := &loggedFiles{fakeFiles: fakeFiles{}}
	if err := Update(empty, "main", 0, func(*State) error { return nil }); err != nil {
		t.Fatal(err)
	}
	if !empty.StateFileExists(FileName) {
		t.Error("no review file was started")
	}
}

// A review that will not parse is somebody else's half-finished write, and an
// update must not go over it with an empty one.
func TestAnUpdateLeavesAnUnreadableReviewAlone(t *testing.T) {
	fs := fakeFiles{FileName: `{"version":1,`}
	called := false
	err := Update(fs, "main", 0, func(*State) error { called = true; return nil })
	if err == nil || called {
		t.Errorf("err = %v, change called = %v; want it refused before anything is changed", err, called)
	}
	if fs[FileName] != `{"version":1,` {
		t.Errorf("the file was overwritten: %q", fs[FileName])
	}
}

// Putting one branch's review away and bringing another's back moves files,
// so it is done under the lock like any other write.
func TestTheBranchSwapIsDoneUnderTheLock(t *testing.T) {
	other := New("feature/x")
	fs := &loggedFiles{fakeFiles: fakeFiles{FileName: string(other.Bytes())}}

	if err := Adopt(fs, "main", 0); err != nil {
		t.Fatal(err)
	}
	log := strings.Join(fs.log, ", ")
	locked, moved := strings.Index(log, "lock "+LockName), strings.Index(log, "rename "+FileName)
	if locked < 0 || moved < locked || fs.log[len(fs.log)-1] != "unlock" {
		t.Errorf("want the review moved aside with the lock held, got: %s", log)
	}
}

// Looking at a review whose branch is the one checked out moves nothing, so it
// waits for nobody: a reader is not held up by whoever is writing.
func TestReadingASettledReviewTakesNoLock(t *testing.T) {
	fs := &loggedFiles{fakeFiles: fakeFiles{
		FileName:               string(New("main").Bytes()),
		ArchiveName("feature"): string(New("feature").Bytes()),
	}}
	if _, err := Load(fs, "main", 0); err != nil {
		t.Fatal(err)
	}
	if log := strings.Join(fs.log, ", "); strings.Contains(log, "lock") || strings.Contains(log, "rename") {
		t.Errorf("a plain read took the lock or moved a file: %s", log)
	}

	// The same question on a review with nothing written yet.
	empty := &loggedFiles{fakeFiles: fakeFiles{}}
	if _, err := Load(empty, "main", 0); err != nil {
		t.Fatal(err)
	}
	if log := strings.Join(empty.log, ", "); strings.Contains(log, "lock") {
		t.Errorf("reading a review that is not there took the lock: %s", log)
	}
}

// lockedFiles is a git directory whose lock somebody else is sitting on.
type lockedFiles struct{ fakeFiles }

func (lockedFiles) LockStateFile(string, time.Duration) (func(), error) {
	return nil, errors.New("locked by another program")
}

// After a checkout the file at the path is still the last branch's review
// until it is swapped. A reader that cannot swap it says so, rather than
// reporting that branch's review as this one's.
func TestAnotherBranchsReviewIsNotReadAsThisOnes(t *testing.T) {
	theirs := New("feature/x")
	theirs.Start(&Thread{Path: "a.go", Line: 1}, "Darren", "about feature/x")
	fs := lockedFiles{fakeFiles{FileName: string(theirs.Bytes())}}

	if st, err := Load(fs, "main", 0); err == nil {
		t.Errorf("main was handed feature/x's review: %+v", st.Threads)
	}
	// Its own review needs nothing moved, so the lock does not come into it.
	if st, err := Load(fs, "feature/x", 0); err != nil || len(st.Threads) != 1 {
		t.Errorf("feature/x could not read its own review: %v", err)
	}
}

// swappedFiles is a git directory in which something else swaps another
// branch's review in at the worst moment: between a reader finding the review
// settled and reading it.
type swappedFiles struct {
	fakeFiles
	reads int
}

func (f *swappedFiles) ReadStateFile(name string) (string, bool) {
	if f.reads++; f.reads == 2 {
		f.fakeFiles[ArchiveName("main")] = f.fakeFiles[FileName]
		f.fakeFiles[FileName] = string(New("feature").Bytes())
	}
	return f.fakeFiles.ReadStateFile(name)
}

// The read comes after the lock, so the file can be swapped in between. A
// reader that gets another branch's review notices, and asks again.
func TestAReviewSwappedUnderAReaderIsReadAgain(t *testing.T) {
	mine := New("main")
	mine.Start(&Thread{Path: "a.go", Line: 1}, "Darren", "about main")
	fs := &swappedFiles{fakeFiles: fakeFiles{FileName: string(mine.Bytes())}}

	st, err := Load(fs, "main", 0)
	if err != nil {
		t.Fatal(err)
	}
	if st.Branch != "main" || len(st.Threads) != 1 {
		t.Errorf("main was handed the review of %q: %+v", st.Branch, st.Threads)
	}
}

// midSwapFiles is a git directory caught halfway through a branch swap by a
// reader: the last branch's review has been put away and this branch's not
// yet brought back, so for a moment there is nothing at the path.
type midSwapFiles struct {
	fakeFiles
	reads int
}

func (f *midSwapFiles) ReadStateFile(name string) (string, bool) {
	if f.reads++; f.reads == 2 {
		f.fakeFiles[ArchiveName("main")] = f.fakeFiles[FileName]
		delete(f.fakeFiles, FileName)
	}
	return f.fakeFiles.ReadStateFile(name)
}

// A swap is two moves, and between them the path is empty. A reader landing
// there has not found a branch with no review: it waits for the swap.
func TestAReaderCaughtMidSwapDoesNotReportAnEmptyReview(t *testing.T) {
	mine := New("main")
	mine.Start(&Thread{Path: "a.go", Line: 1}, "Darren", "about main")
	fs := &midSwapFiles{fakeFiles: fakeFiles{FileName: string(mine.Bytes())}}

	st, err := Load(fs, "main", 0)
	if err != nil {
		t.Fatal(err)
	}
	if len(st.Threads) != 1 {
		t.Errorf("the reader was told main has no review: %+v", st)
	}
}

func keysOf(fs fakeFiles) []string {
	var out []string
	for k := range fs {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
