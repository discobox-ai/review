// Package review is the state of a review: which files have been approved,
// and what has been said about which lines.
//
// It is deliberately a plain document. discobox-review keeps it in a JSON file in the
// git directory rather than in its own memory, because the other reader of a
// review is increasingly an agent: something that wants to see what you
// objected to, answer it, and mark the thread closed, without discobox-review being
// involved at all. So the format is the interface, and everything here is
// arranged around that being pleasant to read and hard to corrupt.
//
// Nothing in this package touches a file. Loading and saving belong to the
// caller, which owns the one system boundary.
package review

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"slices"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/discobox-ai/review/internal/git"
)

// FileName is where the review lives, inside the git directory. A branch's
// review is moved aside under ArchiveName when another branch is checked out,
// so this path always holds the review of whatever is checked out now.
const FileName = "discobox-review.json"

// ArchiveName is where the review of a branch waits while a different branch
// is checked out.
func ArchiveName(branch string) string {
	return "discobox-review." + safeBranch(branch) + ".json"
}

// safeBranch is a branch name that can be one path component: a branch may
// hold slashes, and a file name may not.
func safeBranch(branch string) string {
	return strings.NewReplacer("/", "-", "\\", "-", ":", "-").Replace(branch)
}

// Version is the schema version written into every file. It exists so that a
// reader can tell whether it understands what it is looking at.
//
// 2 is the version in which a file carries a verdict for each reviewer. 1 kept
// a single approval on the file itself, signed by whoever gave it last; a
// review written that way is still read — see [File.UnmarshalJSON].
const Version = 2

// Sides a comment can be anchored to. "new" numbers lines in the working tree
// as it stands now, "old" numbers them in the file as the base has it. Those
// two spaces are what every comment is stored in, whichever view it was
// written from.
const (
	SideNew = "new"
	SideOld = "old"
)

// State is the whole review of one branch.
type State struct {
	Version int `json:"version"`

	// Branch and Base say what this review is of. Base is the ref, empty when
	// discobox-review is left to work one out.
	Branch string `json:"branch"`
	Base   string `json:"base,omitempty"`

	// Files carries one entry per file anybody has given a verdict on.
	// A file nobody has approved has no entry at all.
	Files []*File `json:"files"`

	// Threads are the comments, in the order they were started.
	Threads []*Thread `json:"threads"`
}

// Of reports whether this can be the review of branch. One that names no
// branch — written by hand, or with no branch checked out — is anybody's.
func (s *State) Of(branch string) bool {
	return s.Branch == "" || branch == "" || s.Branch == branch
}

// File is what the review says about one path: what each reviewer has made
// of it, one verdict to a name, in the order of the names.
//
// A verdict each rather than one for the file, because a file an agent has
// approved is not thereby one the person has read. Whose approval counts is
// for whoever is asking to say.
type File struct {
	Path     string     `json:"path"`
	Verdicts []*Verdict `json:"verdicts"`
}

// Verdict is what one reviewer says about one path.
//
// Approved is a claim about a particular set of changes, not about a name:
// Digest identifies the change that was read, so a file edited after it was
// approved no longer matches and the approval falls. Nullified records that
// this is what happened, rather than the approval merely never having been
// given, since the two are worth telling apart.
type Verdict struct {
	By        string `json:"by"`
	Approved  bool   `json:"approved"`
	Digest    string `json:"digest,omitempty"`
	Nullified bool   `json:"nullified,omitempty"`
	// Hunks are the parts of the change signed off one at a time, each named
	// by the digest of a run of changed lines; see [HunkDigests]. They stand
	// whether or not the file as a whole is approved, and one that names a
	// change no longer there simply matches nothing.
	Hunks []string `json:"hunks,omitempty"`
	At    string   `json:"at,omitempty"`
}

// empty reports whether a verdict says nothing: no approval, none that fell,
// and no hunk signed off. Such a one is not kept.
func (v *Verdict) empty() bool {
	return !v.Approved && !v.Nullified && len(v.Hunks) == 0
}

// UnmarshalJSON reads a file's entry in either of the shapes it has had.
//
// The older one is the verdict written straight onto the file, with nothing
// around it. That is how a version 1 review has it, and it is also still the
// least a hand can write to approve something — a path, "approved": true and
// a name — so it goes on being read: as the verdict of whoever "by" names.
// It goes after the list proper, so that where the same name has a verdict in
// both, this one is the later of the two — see [State.normalise].
func (f *File) UnmarshalJSON(data []byte) error {
	var raw struct {
		Path     string     `json:"path"`
		Verdicts []*Verdict `json:"verdicts"`
		Verdict
	}
	if err := json.Unmarshal(data, &raw); err != nil {
		return err
	}
	f.Path, f.Verdicts = raw.Path, raw.Verdicts
	if flat := raw.Verdict; !flat.empty() {
		f.Verdicts = append(f.Verdicts, &flat)
	}
	return nil
}

// verdict is the one a reviewer has given on this file, or nil.
func (f *File) verdict(by string) *Verdict {
	for _, v := range f.Verdicts {
		if v != nil && v.By == by {
			return v
		}
	}
	return nil
}

// Thread is one conversation, hanging off one line.
//
// Line is numbered in the working tree when Side is "new" and in the base when
// it is "old", whichever view the comment was written from — a comment made
// against a single commit is carried into those coordinates when it is
// written, so that there is only ever one place a comment lives. Line is 0 for
// a thread that could not be carried, which is what Origin is for: it says
// where the remark was made, so it can still be shown there.
//
// Text is the line as it read when the comment was written. Nothing depends on
// it, which is the point: it is there so that the file can be read on its own.
type Thread struct {
	ID   string `json:"id"`
	Path string `json:"path"`
	Side string `json:"side"`
	Line int    `json:"line"`

	// Lines is how many lines the remark covers, counting from Line. It is
	// left out for the ordinary one-line comment, so a reader that ignores it
	// entirely is still right about where the comment goes.
	Lines    int       `json:"lines,omitempty"`
	Text     string    `json:"text,omitempty"`
	Origin   *Origin   `json:"origin,omitempty"`
	Resolved bool      `json:"resolved"`
	Comments []Comment `json:"comments"`
}

// Origin is the commit a thread was written against, when it was not written
// against the working tree.
type Origin struct {
	Commit string `json:"commit"`
	Side   string `json:"side"`
	Line   int    `json:"line"`
}

// Comment is one remark in a thread.
type Comment struct {
	By   string `json:"by"`
	At   string `json:"at"`
	Body string `json:"body"`
}

// New is an empty review of a branch.
func New(branch string) *State {
	return &State{Version: Version, Branch: branch}
}

// Parse reads a review. A file that is not valid JSON is an error rather than
// an empty review, so that a half written one — an agent caught mid-save —
// leaves what discobox-review already has on screen instead of wiping it.
func Parse(data []byte) (*State, error) {
	if strings.TrimSpace(string(data)) == "" {
		return nil, fmt.Errorf("the review is empty")
	}
	var s State
	if err := json.Unmarshal(data, &s); err != nil {
		return nil, err
	}
	// Written by a discobox-review newer than this one. Reading it as though
	// it were understood would be followed by writing it back without
	// whatever was not, so it is not read at all.
	if s.Version > Version {
		return nil, fmt.Errorf("the review is version %d and this discobox-review reads up to %d: it wants upgrading", s.Version, Version)
	}
	s.normalise()
	return &s, nil
}

// normalise fills in what a hand written file is allowed to leave out, so that
// everything above this line can assume a complete document.
func (s *State) normalise() {
	if s.Version == 0 {
		s.Version = Version
	}
	// A path named twice is one file: somebody adding their approval by hand
	// may well have added an entry rather than looked for the one there, and
	// what they said belongs with the rest of what is said about it.
	first := map[string]*File{}
	for _, f := range s.Files {
		if f == nil {
			continue
		}
		if into := first[f.Path]; into != nil {
			into.Verdicts = append(into.Verdicts, f.Verdicts...)
			f.Path = ""
			continue
		}
		first[f.Path] = f
	}
	files := s.Files[:0]
	for _, f := range s.Files {
		if f == nil || f.Path == "" {
			continue
		}
		// One verdict to a name, and none that says nothing; a file left
		// with none has nothing said about it.
		//
		// The last of them, where a name has several. Nothing here writes a
		// second, so a second is somebody's hand: a reviewer whose approval
		// fell adding the one that replaces it, below the one that is there.
		// The hunks they had signed off are not what they were taking back,
		// so those come along when the later verdict names none.
		verdicts := f.Verdicts[:0]
		for _, v := range f.Verdicts {
			if v == nil || v.empty() {
				continue
			}
			at := slices.IndexFunc(verdicts, func(o *Verdict) bool { return o.By == v.By })
			if at < 0 {
				verdicts = append(verdicts, v)
				continue
			}
			if len(v.Hunks) == 0 {
				v.Hunks = verdicts[at].Hunks
			}
			verdicts[at] = v
		}
		f.Verdicts = verdicts
		sort.SliceStable(f.Verdicts, func(i, j int) bool { return f.Verdicts[i].By < f.Verdicts[j].By })
		if len(f.Verdicts) > 0 {
			files = append(files, f)
		}
	}
	s.Files = files
	sort.SliceStable(s.Files, func(i, j int) bool { return s.Files[i].Path < s.Files[j].Path })

	threads := s.Threads[:0]
	seen := map[string]bool{}
	for _, t := range s.Threads {
		if t == nil || t.Path == "" {
			continue
		}
		if t.Side != SideOld {
			t.Side = SideNew
		}
		if t.Lines <= 1 {
			// One line is the ordinary case and the default, so it is left
			// out — a reader that ignores the field entirely is still right
			// about where the comment goes.
			t.Lines = 0
		}
		if t.ID == "" || seen[t.ID] {
			t.ID = nextID(threads)
		}
		seen[t.ID] = true
		threads = append(threads, t)
	}
	s.Threads = threads
}

// Bytes renders the review for writing out: indented, with the keys in a fixed
// order, so that two saves of the same state produce the same bytes and a
// human diffing the file sees only what really changed.
func (s *State) Bytes() []byte {
	s.Version = Version
	s.normalise()
	if s.Files == nil {
		s.Files = []*File{}
	}
	if s.Threads == nil {
		s.Threads = []*Thread{}
	}
	data, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return nil
	}
	return append(data, '\n')
}

// Now is the timestamp format the file uses: RFC 3339, which sorts as text and
// which every language reads.
func Now() string { return time.Now().UTC().Format(time.RFC3339) }

// ---------------------------------------------------------------------------
// Files
// ---------------------------------------------------------------------------

// FileFor is the entry for a path, or nil.
func (s *State) FileFor(path string) *File {
	for _, f := range s.Files {
		if f.Path == path {
			return f
		}
	}
	return nil
}

// entryFor is FileFor, making the entry when there is none.
func (s *State) entryFor(path string) *File {
	if f := s.FileFor(path); f != nil {
		return f
	}
	f := &File{Path: path}
	s.Files = append(s.Files, f)
	sort.SliceStable(s.Files, func(i, j int) bool { return s.Files[i].Path < s.Files[j].Path })
	return f
}

// VerdictOf is what a reviewer has said about a path, or nil.
func (s *State) VerdictOf(path, by string) *Verdict {
	if f := s.FileFor(path); f != nil {
		return f.verdict(by)
	}
	return nil
}

// verdictFor is VerdictOf, making one when there is none.
func (s *State) verdictFor(path, by string) *Verdict {
	f := s.entryFor(path)
	if v := f.verdict(by); v != nil {
		return v
	}
	v := &Verdict{By: by}
	f.Verdicts = append(f.Verdicts, v)
	sort.SliceStable(f.Verdicts, func(i, j int) bool { return f.Verdicts[i].By < f.Verdicts[j].By })
	return v
}

// Approved reports whether a reviewer has approved a path as it now stands.
func (s *State) Approved(path, by string) bool {
	v := s.VerdictOf(path, by)
	return v != nil && v.Approved
}

// Nullified reports whether a reviewer approved a path which then changed.
func (s *State) Nullified(path, by string) bool {
	v := s.VerdictOf(path, by)
	return v != nil && !v.Approved && v.Nullified
}

// Approvers is everybody whose approval of a path stands, by name.
func (s *State) Approvers(path string) []string {
	return s.named(path, func(v *Verdict) bool { return v.Approved })
}

// Fallen is everybody who approved a path which then changed, by name.
func (s *State) Fallen(path string) []string {
	return s.named(path, func(v *Verdict) bool { return !v.Approved && v.Nullified })
}

func (s *State) named(path string, want func(*Verdict) bool) []string {
	f := s.FileFor(path)
	if f == nil {
		return nil
	}
	var out []string
	for _, v := range f.Verdicts {
		if want(v) {
			out = append(out, v.By)
		}
	}
	return out
}

// Approve records a reviewer's approval of the change identified by digest.
// Anybody else's verdict on the path is left as it is.
func (s *State) Approve(path, digest, by string) {
	v := s.verdictFor(path, by)
	v.Approved, v.Digest, v.Nullified = true, digest, false
	v.At = Now()
}

// Unapprove takes a reviewer's approval back, and the hunks they signed off
// with it. It is not a nullification: the reviewer changed their mind rather
// than the file changing under them.
func (s *State) Unapprove(path, by string) {
	s.drop(path, func(v *Verdict) bool { return v.By == by })
}

// UnapproveAll takes back every approval a path has, whoever gave it.
func (s *State) UnapproveAll(path string) {
	s.drop(path, func(*Verdict) bool { return true })
}

// drop removes the verdicts on a path that match, and the path's entry with
// them when none is left.
func (s *State) drop(path string, match func(*Verdict) bool) {
	f := s.FileFor(path)
	if f == nil {
		return
	}
	f.Verdicts = slices.DeleteFunc(f.Verdicts, match)
	if len(f.Verdicts) == 0 {
		s.Files = slices.DeleteFunc(s.Files, func(o *File) bool { return o == f })
	}
}

// ApprovedHunks is the runs of changed lines in a path that a reviewer has
// signed off one at a time, as a set of their digests.
func (s *State) ApprovedHunks(path, by string) map[string]bool {
	v := s.VerdictOf(path, by)
	if v == nil || len(v.Hunks) == 0 {
		return nil
	}
	set := make(map[string]bool, len(v.Hunks))
	for _, d := range v.Hunks {
		set[d] = true
	}
	return set
}

// SetHunks replaces the runs of changed lines a reviewer has signed off in a
// path.
func (s *State) SetHunks(path, by string, digests []string) {
	if len(digests) == 0 {
		if v := s.VerdictOf(path, by); v != nil {
			v.Hunks = nil
			if v.empty() {
				s.Unapprove(path, by)
			}
		}
		return
	}
	digests = append([]string(nil), digests...)
	sort.Strings(digests)
	s.verdictFor(path, by).Hunks = digests
}

// ClaimHunks hands a reviewer the hunks that are signed off under no name, in
// every path, and reports whether there were any.
//
// A version 1 review named whoever approved a file and nobody for a hunk
// signed off on its own, so those come over belonging to no one — see
// [File.UnmarshalJSON]. But only a window has ever signed off a hunk, and a
// window has one reader, so the first one to open the review takes them as
// its reader's: they are that person's reading, and would otherwise sit in
// the file counting for nobody while every hunk came back unread.
//
// An approval of the whole file under no name is left where it is. That is
// what a hand writes, and whose hand is exactly what must not be guessed.
func (s *State) ClaimHunks(by string) bool {
	if by == "" {
		return false
	}
	claimed := false
	for _, f := range slices.Clone(s.Files) {
		nameless := f.verdict("")
		if nameless == nil || len(nameless.Hunks) == 0 {
			continue
		}
		mine := s.verdictFor(f.Path, by)
		for _, d := range nameless.Hunks {
			if !slices.Contains(mine.Hunks, d) {
				mine.Hunks = append(mine.Hunks, d)
			}
		}
		sort.Strings(mine.Hunks)
		nameless.Hunks, claimed = nil, true
		s.drop(f.Path, (*Verdict).empty)
	}
	return claimed
}

// PruneHunks drops the hunk approvals of a path that are of changes no longer
// there, given the runs of changed lines the path has now, and reports whether
// it dropped any.
//
// Without it an approval of an edit that was then undone would lie in wait and
// come back with the edit, signed off by nobody who read it the second time.
// A file's approval does not come back that way, and nor should a hunk's.
func (s *State) PruneHunks(path string, present map[string]bool) bool {
	f := s.FileFor(path)
	if f == nil {
		return false
	}
	changed := false
	for _, v := range f.Verdicts {
		kept := slices.DeleteFunc(slices.Clone(v.Hunks), func(d string) bool { return !present[d] })
		if len(kept) == len(v.Hunks) {
			continue
		}
		changed = true
		if v.Hunks = kept; len(kept) == 0 {
			v.Hunks = nil
		}
	}
	if changed {
		s.drop(path, (*Verdict).empty)
	}
	return changed
}

// Reconcile checks every approval against the change now in front of the
// reviewer, and reports whether anything moved, and which paths had an
// approval fall.
//
// digests maps a path to the digest of its current change. An approval whose
// digest no longer matches is nullified: the file has been edited since it was
// read, so the approval was of something else — whoever gave it. An approval
// carrying no digest at all — the shape an agent writes when it approves a
// file by hand — is stamped with the current one rather than being treated as
// stale, since "approved" plainly means "approved as it is now".
func (s *State) Reconcile(digests map[string]string) (changed bool, nullified []string) {
	for _, f := range s.Files {
		cur, ok := digests[f.Path]
		if !ok {
			// Not part of the review as it now stands: the base may have
			// moved, or the change may have been committed away. Leaving the
			// approvals alone means they are still there if the file comes
			// back.
			continue
		}
		fell := false
		for _, v := range f.Verdicts {
			switch {
			case !v.Approved:
			case v.Digest == "":
				v.Digest, changed = cur, true
				if v.At == "" {
					v.At = Now()
				}
			case v.Digest != cur:
				v.Approved, v.Nullified, changed, fell = false, true, true, true
			}
		}
		if fell {
			nullified = append(nullified, f.Path)
		}
	}
	sort.Strings(nullified)
	return changed, nullified
}

// Digest identifies a change to one file, independently of how it is being
// looked at.
//
// Only the changed lines and their numbers go in, so that turning the diff
// context up or down — which moves every hunk boundary — does not nullify a
// single approval, while editing one character of one line nullifies exactly
// the file it was in.
func Digest(p *git.Patch) string {
	if p == nil {
		return ""
	}
	h := sha256.New()
	for _, hunk := range p.Hunks {
		oldNo, newNo := hunk.OldStart, hunk.NewStart
		for _, l := range hunk.Lines {
			if l == "" {
				continue
			}
			switch l[0] {
			case ' ':
				oldNo++
				newNo++
			case '-':
				fmt.Fprintf(h, "-%d %s\n", oldNo, l[1:])
				oldNo++
			case '+':
				fmt.Fprintf(h, "+%d %s\n", newNo, l[1:])
				newNo++
			}
		}
	}
	return hex.EncodeToString(h.Sum(nil))[:16]
}

// HunkDigests identifies the parts of a change, hunk by hunk: for each hunk of
// the patch, one digest for every unbroken run of changed lines in it.
//
// A run rather than the hunk, for the reason [Digest] leaves the context out.
// Where a hunk begins and ends is a matter of how much context was asked for,
// and two that were apart become one when it is turned up; the runs of changed
// lines inside them are the same runs either way. So an approval is recorded
// against the runs, and a hunk is approved when all of its runs are.
func HunkDigests(p *git.Patch) [][]string {
	if p == nil {
		return nil
	}
	out := make([][]string, len(p.Hunks))
	for i, hunk := range p.Hunks {
		h := sha256.New()
		open := false
		flush := func() {
			if open {
				out[i] = append(out[i], hex.EncodeToString(h.Sum(nil))[:16])
				h.Reset()
				open = false
			}
		}
		oldNo, newNo := hunk.OldStart, hunk.NewStart
		for _, l := range hunk.Lines {
			if l == "" {
				continue
			}
			switch l[0] {
			case ' ':
				flush()
				oldNo++
				newNo++
			case '-':
				fmt.Fprintf(h, "-%d %s\n", oldNo, l[1:])
				oldNo++
				open = true
			case '+':
				fmt.Fprintf(h, "+%d %s\n", newNo, l[1:])
				newNo++
				open = true
			}
		}
		flush()
	}
	return out
}

// ---------------------------------------------------------------------------
// Threads
// ---------------------------------------------------------------------------

// nextID is the first "tN" no thread is using. Ids are generated rather than
// counted in the file so that an agent appending a thread has one less field
// to keep correct; it may use any unique string it likes.
func nextID(threads []*Thread) string {
	highest := 0
	for _, t := range threads {
		if !strings.HasPrefix(t.ID, "t") {
			continue
		}
		if n, err := strconv.Atoi(t.ID[1:]); err == nil && n > highest {
			highest = n
		}
	}
	return "t" + strconv.Itoa(highest+1)
}

// NextID is the id discobox-review gives the next thread it starts.
func (s *State) NextID() string { return nextID(s.Threads) }

// Start opens a thread with its first comment in it.
func (s *State) Start(t *Thread, by, body string) *Thread {
	if t.ID == "" {
		t.ID = s.NextID()
	}
	if t.Side != SideOld {
		t.Side = SideNew
	}
	t.Comments = append(t.Comments, Comment{By: by, At: Now(), Body: body})
	s.Threads = append(s.Threads, t)
	return t
}

// Thread finds a thread by id.
func (s *State) Thread(id string) *Thread {
	for _, t := range s.Threads {
		if t.ID == id {
			return t
		}
	}
	return nil
}

// Reply adds to a thread.
func (s *State) Reply(id, by, body string) bool {
	t := s.Thread(id)
	if t == nil {
		return false
	}
	t.Comments = append(t.Comments, Comment{By: by, At: Now(), Body: body})
	return true
}

// SetResolved closes a thread or opens it again.
func (s *State) SetResolved(id string, resolved bool) bool {
	t := s.Thread(id)
	if t == nil {
		return false
	}
	t.Resolved = resolved
	return true
}

// Remove deletes a thread outright.
func (s *State) Remove(id string) bool {
	for i, t := range s.Threads {
		if t.ID == id {
			s.Threads = append(s.Threads[:i], s.Threads[i+1:]...)
			return true
		}
	}
	return false
}

// RemoveComment deletes one remark from a thread, and reports whether it went
// and whether the thread went with it.
//
// A thread is its comments: taking the last one out leaves an anchor on a line
// with nothing to say, which is not a thing anybody wants left behind. Taking
// out the first of several is allowed — an opening remark can be as wrong as
// any other — and what it was anchored to stays, because the replies are still
// about that line.
func (s *State) RemoveComment(id string, at int) (removed, gone bool) {
	t := s.Thread(id)
	if t == nil || at < 0 || at >= len(t.Comments) {
		return false, false
	}
	t.Comments = append(t.Comments[:at], t.Comments[at+1:]...)
	if len(t.Comments) == 0 {
		return true, s.Remove(id)
	}
	return true, false
}

// ThreadsFor is every thread on a path, in the order they were started.
func (s *State) ThreadsFor(path string) []*Thread {
	var out []*Thread
	for _, t := range s.Threads {
		if t.Path == path {
			out = append(out, t)
		}
	}
	return out
}

// Open counts the threads nobody has closed.
func (s *State) Open() int {
	n := 0
	for _, t := range s.Threads {
		if !t.Resolved {
			n++
		}
	}
	return n
}

// CountFor is how many threads a path carries, and how many of them are open.
func (s *State) CountFor(path string) (total, open int) {
	for _, t := range s.Threads {
		if t.Path != path {
			continue
		}
		total++
		if !t.Resolved {
			open++
		}
	}
	return total, open
}

// Anchored reports whether a thread has a line in the working tree to hang
// off. One that has not is still a comment, and still shown — under the file
// it belongs to rather than beside a line that is no longer there.
func (t *Thread) Anchored() bool { return t.Line > 0 }

// Span is the range of lines a thread covers, both ends inclusive.
func (t *Thread) Span() (first, last int) {
	if t.Lines <= 1 {
		return t.Line, t.Line
	}
	return t.Line, t.Line + t.Lines - 1
}

// Author is who started the thread.
func (t *Thread) Author() string {
	if len(t.Comments) == 0 {
		return ""
	}
	return t.Comments[0].By
}

// ---------------------------------------------------------------------------
// Carrying a comment between a commit and the working tree
// ---------------------------------------------------------------------------

// Carry moves a line number from the coordinates of a commit into the
// coordinates the review stores, using the diff between the two.
//
// carry is the patch from the commit to the working tree, so its old side is
// the commit and its new side is the working tree. A line the working tree no
// longer has cannot be carried, and the comment made on it becomes one of the
// file's unanchored remarks — kept, shown, but no longer beside anything.
func Carry(carry *git.Patch, side string, line int) (int, bool) {
	if carry == nil || line <= 0 {
		return 0, false
	}
	if side == SideOld {
		// An old-side line belongs to the base, which neither end of this
		// diff is: there is nothing here to carry it with.
		return 0, false
	}
	return carry.MapOldToNew(line)
}

// CarryBack is Carry in the other direction: from the working tree into the
// coordinates of a commit, which is how a comment already in the review is
// found again while a single commit is being read.
func CarryBack(carry *git.Patch, side string, line int) (int, bool) {
	if carry == nil || line <= 0 || side == SideOld {
		return 0, false
	}
	return carry.MapNewToOld(line)
}
