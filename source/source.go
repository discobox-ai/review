// Package source is what the review window reads a change from.
//
// The window was written against a Git working tree, and that is still the
// source the discobox-review command gives it. It is not the only one there can
// be: a pull request has a base, a head and a diff between them, and nothing
// on disk at all. Everything the window needs of a change is said here, as a
// small interface every source implements and a few that only some do — a
// working tree to check out and edit in, a place to keep the review — so that
// a source with no working tree is one that does not have those, rather than
// one that has to pretend.
//
// The types are plain data. A source fills them in however it likes; the
// window never asks where they came from.
package source

import (
	"errors"
	"io"
	"time"
)

// Source is the change under review: one diff, the commits it was made in,
// and the text of any file at any of them.
type Source interface {
	// Scan reads the whole review in one go. base is the ref the review is
	// measured from, or empty to let the source decide; context is how many
	// unchanged lines every diff is taken with.
	Scan(base string, context int) (Scan, error)

	// CommitDiff is everything one commit changed, as a unified diff taken
	// with context lines of context.
	CommitDiff(hash string, context int) (string, error)

	// FileContent is a file's whole text as commit recorded it, or as the
	// review's new side has it — the working tree, a pull request's head —
	// when commit is empty.
	FileContent(commit, path string) (string, error)
}

// Carrier is a source that can say how one path moved between a commit and
// the review's new side, which is what carries a comment written while reading
// that commit onto the review and back.
//
// A source without it still shows every commit; comments simply stay on the
// review as a whole.
type Carrier interface {
	// CarryDiff is the diff of one path from rev to the review's new side,
	// with no context: only its line numbers are read.
	CarryDiff(rev, path string) (string, error)
}

// Store keeps the review: its comments, its approvals, and the base it is
// measured from, as one document under a name. It is how a Git working tree
// keeps it, in files in the git directory, and it is shaped as files because
// that is what the commands and anything else reading the review share.
//
// A source that is also a Store has a review that can be written. One that is
// not shows the change with no review over it — nothing to approve, and
// nowhere to put a comment — and the window offers neither.
type Store interface {
	// ReadStateFile reads the named document, reporting whether there is one.
	ReadStateFile(name string) (string, bool)

	// WriteStateFile replaces the named document whole. A reader must never
	// see half of one.
	WriteStateFile(name, content string) error

	// RenameStateFile moves one document over another.
	RenameStateFile(from, to string) error

	// StateFileExists reports whether the named document is there.
	StateFileExists(name string) bool

	// LockStateFile takes the lock the name stands for, waiting up to wait
	// for whoever holds it and then failing with [ErrLocked], and returns
	// what lets go of it. Whoever reads the review to write it back holds it
	// from the read to the write.
	LockStateFile(name string, wait time.Duration) (unlock func(), err error)

	// StateFileStamp is enough of the named document to tell that it has
	// changed since it was read: the zero Stamp when there is none.
	StateFileStamp(name string) Stamp
}

// Worktree is a source with a working tree behind it: one whose branch can be
// changed, whose files can be opened in an editor, and whose base can be any
// ref or commit the repository can name.
//
// A source without it — a pull request — has none of those, and the window
// leaves out the keys and the help that offer them.
type Worktree interface {
	// Branches lists the branches that can be checked out.
	Branches() ([]string, error)

	// Checkout switches the working tree to branch.
	Checkout(branch string) (string, error)

	// Refs lists the refs worth offering as a base.
	Refs() []string

	// Recent lists the last n commits, newest first, to measure from.
	Recent(n int) ([]Commit, error)

	// EditAt opens a file in the user's editor: the working tree's copy when
	// commit is empty, and otherwise a read-only copy of the commit's, whose
	// path it returns for RemoveTempCopy once the editor exits.
	EditAt(commit, path string) (ed Editor, tmp string, err error)

	// RemoveTempCopy removes a copy EditAt made. The empty path is nothing to
	// remove.
	RemoveTempCopy(path string)
}

// Editor is a command that owns the terminal until it exits: the shape
// Bubble Tea's exec support wants.
type Editor interface {
	Run() error
	SetStdin(io.Reader)
	SetStdout(io.Writer)
	SetStderr(io.Writer)
}

// ErrLocked is what taking a lock fails with when somebody else held it for
// longer than the caller was willing to wait.
var ErrLocked = errors.New("locked by another program")

// Stamp is enough of a file to tell whether it has changed since it was last
// read: when it was written and how long it was. The size is there because a
// filesystem's modification times can be too coarse to separate two writes in
// the same second, which is exactly the interval an agent editing the review
// works on.
type Stamp struct {
	ModTime int64
	Size    int64
}

// ---------------------------------------------------------------------------
// The scan
// ---------------------------------------------------------------------------

// Scan is everything one reading of the source learns: where the branch
// stands, what it has committed since the base, what is not committed yet, and
// the two diffs the review is drawn from.
type Scan struct {
	// Branch names what is under review, and is what the review is kept
	// under: a review is of a branch.
	Branch  string
	HasHEAD bool
	Merging bool

	// Base is what the review is measured from. Ref is the name the user
	// chose or the source worked out, and SHA the commit the branch's work
	// begins after.
	Base Base

	// Commits are the branch's own commits, newest first.
	Commits []Commit

	// Files is the state of every path not yet committed, which is where the
	// "not committed yet" marks come from. A source with nothing uncommitted
	// leaves it empty.
	Files []File

	// Diff is the review itself, as a unified diff of every file: every
	// change from the base to the new side.
	Diff string

	// Pending is the same new side measured from the last commit instead, so
	// the lines in Diff that have not been committed yet can be picked out of
	// it. Empty when everything is committed.
	Pending string

	// Incomplete says why Diff is not the whole of the change, and is empty
	// when it is. A source that was handed a diff cut short — a hosting
	// service caps how much of one it sends — says so here, and the window
	// says so in turn rather than showing part of a review as all of it.
	Incomplete string
}

// Base is what the review is measured from: a ref, the commit it and this
// branch last had in common, and how far each has moved since.
type Base struct {
	Ref    string // the ref chosen or worked out, "origin/main"
	SHA    string // the merge base itself, in full
	Commit string // the same, abbreviated for the bar
	Ahead  int    // commits on the branch since the merge base
	Behind int    // commits on Ref since the merge base
	Auto   bool   // true when the source picked the ref rather than the user
	Err    string // why a ref the user named could not be used
}

// Commit is one entry of the branch's own history.
type Commit struct {
	Hash    string
	Short   string
	Subject string
	Author  string
	When    string
}

// File is one entry of the working tree scan.
type File struct {
	Path  string
	From  string // original path for renames/copies
	State State
}

// ---------------------------------------------------------------------------
// File states
// ---------------------------------------------------------------------------

// State is a two character file state: the first character describes the index
// (HEAD vs index) and the second the working directory (index vs file). '_'
// means "no difference", 'O' means untracked and 'U' unmerged. The empty State
// is a committed file.
//
// The review does not care which side of the index a change is on — it reads
// the working tree as one thing — but it does care whether a change has been
// committed, and this is what answers that.
type State string

// Index returns the HEAD-vs-index status character.
func (s State) Index() byte {
	if len(s) > 0 {
		return s[0]
	}
	return '_'
}

// Work returns the index-vs-worktree status character.
func (s State) Work() byte {
	if len(s) > 1 {
		return s[1]
	}
	return '_'
}

// Unmerged reports whether the path is in a conflicted state.
func (s State) Unmerged() bool { return s.Index() == 'U' || s.Work() == 'U' }

// Untracked reports whether git has never been told about the path.
func (s State) Untracked() bool { return s.Work() == 'O' }

// Committed reports whether the working tree agrees with HEAD about the path,
// which is the state the whole review is trying to reach.
func (s State) Committed() bool { return s.Index() == '_' && s.Work() == '_' }

// Staged reports whether the change is in the index but not yet in a commit.
func (s State) Staged() bool {
	c := s.Index()
	return c != '_' && c != 'U'
}

// Pending names, in one word, what still stands between a path and a clean
// tree. It is empty for a path that is already committed.
func (s State) Pending() string {
	switch {
	case s.Unmerged():
		return "unmerged"
	case s.Untracked():
		return "untracked"
	case s.Work() == 'D':
		return "deleted"
	case s.Work() != '_' && s.Staged():
		return "part staged"
	case s.Staged():
		return "staged"
	case s.Work() != '_':
		return "uncommitted"
	}
	return ""
}
