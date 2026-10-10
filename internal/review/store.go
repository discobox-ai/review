package review

import (
	"fmt"
	"time"
)

// Reading and writing the review, without touching a file.
//
// The document lives in the git directory, and getting it in and out is the
// same job for the window and for the commands — the same branch swap, the
// same "a file that will not parse is a moment, not a reason to lose a
// review". Doing it here means the two cannot come to disagree about what a
// review file is.

// Files is the handful of git-directory operations a review needs. It is an
// interface so that this package touches nothing itself: [git.Repo] satisfies
// it, and the one system boundary stays where it is.
type Files interface {
	ReadStateFile(name string) (string, bool)
	WriteStateFile(name, content string) error
	RenameStateFile(from, to string) error
	StateFileExists(name string) bool
	LockStateFile(name string, wait time.Duration) (unlock func(), err error)
}

// LockName is the file whose lock everything that writes the review holds
// while it does: a file of its own beside the review, because the review
// itself is replaced by every write and a lock on it would be a lock on a file
// that is no longer there.
const LockName = FileName + ".lock"

// Lock takes the review's lock and returns what lets go of it. While something
// else holds it, it waits — for as long as wait, and then fails.
//
// Every writer writes the file whole, so a writer working from a copy read a
// moment ago writes that moment back over whatever has landed since: of two
// commands run together, one's remark is gone and neither is told. The lock
// is what makes reading the review, changing it and writing it back a single
// step. It is held from the read to the write and for nothing else, and by
// every writer there is: the commands, the stamp a reading command leaves on
// an approval, and the window.
//
// Reading takes no lock. A write arrives by rename, so a reader has either
// all of the old file or all of the new one.
//
// How long to wait is the caller's to say, because only it knows what is
// waiting with it: a command can sit for as long as the commands ahead of it
// take, and a window cannot stop drawing for that long.
//
// The lock is not re-entrant: none of this package's own functions may be
// called while it is held, since they take it themselves.
func Lock(fs Files, wait time.Duration) (unlock func(), err error) {
	return fs.LockStateFile(LockName, wait)
}

// The names the review went by when the program did: difftui, before it became
// discobox-review. A review written then is still a review, so it is carried
// over the first time its branch is looked at rather than left behind under a
// name nothing reads any more.
//
// This can go once nobody has one.
const legacyFileName = "difftui-review.json"

func legacyArchiveName(branch string) string {
	return "difftui-review." + safeBranch(branch) + ".json"
}

// migrate brings a review written under the old name over to the new one.
//
// It is done a branch at a time, at the moment that branch is looked at, which
// is the only moment the name of its archive is known — and the only moment
// anybody would notice it missing.
func migrate(fs Files, branch string) error {
	if !fs.StateFileExists(FileName) && fs.StateFileExists(legacyFileName) {
		if err := fs.RenameStateFile(legacyFileName, FileName); err != nil {
			return err
		}
	}
	if branch == "" {
		return nil
	}
	if !fs.StateFileExists(ArchiveName(branch)) && fs.StateFileExists(legacyArchiveName(branch)) {
		return fs.RenameStateFile(legacyArchiveName(branch), ArchiveName(branch))
	}
	return nil
}

// Adopt makes the file at [FileName] the review of branch.
//
// A review is of a branch, so switching branches has to switch reviews. Doing
// it by moving files rather than by keying them all inside one means the live
// file is always at one path, holding one review, in the flattest shape
// anything reading it could ask for — which is the whole reason the state is a
// file.
//
// Moving the files is a write like any other, so it is done under the lock:
// two programs noticing the same branch switch must not each move the other's
// file aside. But it is only wanted when a branch has just been switched to,
// and the rest of the time — which is nearly all of it — there is nothing to
// move and so no lock to wait for.
func Adopt(fs Files, branch string, wait time.Duration) error {
	if settled(fs, branch) {
		return nil
	}
	unlock, err := Lock(fs, wait)
	if err != nil {
		return err
	}
	defer unlock()
	return adopt(fs, branch)
}

// settled reports whether the review at [FileName] is already the one for
// branch, with nothing under an old name to carry over: that is, whether adopt
// would leave everything where it is.
func settled(fs Files, branch string) bool {
	if !fs.StateFileExists(FileName) && fs.StateFileExists(legacyFileName) {
		return false
	}
	if branch == "" {
		return true
	}
	if !fs.StateFileExists(ArchiveName(branch)) && fs.StateFileExists(legacyArchiveName(branch)) {
		return false
	}
	if data, ok := fs.ReadStateFile(FileName); ok {
		st, err := Parse([]byte(data))
		return err != nil || st.Branch == "" || st.Branch == branch
	}
	return !fs.StateFileExists(ArchiveName(branch))
}

// adopt is Adopt for a caller that holds the lock.
//
// It stops at the first move that fails. The move after it would otherwise
// bring this branch's review in over one that had not been put away, which is
// the only copy of it there is.
func adopt(fs Files, branch string) error {
	if err := migrate(fs, branch); err != nil || branch == "" {
		return err
	}
	if data, ok := fs.ReadStateFile(FileName); ok {
		st, err := Parse([]byte(data))
		if err != nil || st.Branch == "" || st.Branch == branch {
			return nil
		}
		if err := fs.RenameStateFile(FileName, ArchiveName(st.Branch)); err != nil {
			return err
		}
	}
	if fs.StateFileExists(ArchiveName(branch)) {
		return fs.RenameStateFile(ArchiveName(branch), FileName)
	}
	return nil
}

// Load reads the review of a branch, adopting it first. A review that is not
// there yet is an empty one rather than an error; one that will not parse is
// an error, so that a caller can leave what it already has alone.
//
// What comes back is a copy to read from. Changing it and writing it back
// would undo whatever was written in between, which is what [Update] is for.
func Load(fs Files, branch string, wait time.Duration) (*State, error) {
	// A review that is already this branch's is read without a lock or a
	// wait. One that is not, and could not be swapped for this branch's, is
	// not read at all: it is some other branch's review, and handing it back
	// would be answering a different question.
	//
	// The read comes after the lock is let go of, so something else can swap
	// the file in between, or be halfway through doing so. What was read
	// says which branch it is of, and a wrong one is put right and read again
	// — a few times, not for ever.
	var st *State
	for range 3 {
		err := Adopt(fs, branch, wait)
		if err != nil {
			return nil, err
		}
		if st, err = read(fs, branch); err != nil {
			return nil, err
		}
		// Settled still, and not caught halfway through a swap, when the
		// path is empty for a moment with this branch's review beside it.
		if st.Of(branch) && settled(fs, branch) {
			return st, nil
		}
	}
	if st.Of(branch) {
		return st, nil
	}
	return nil, fmt.Errorf("it is the review of %s, not of %s", st.Branch, branch)
}

func read(fs Files, branch string) (*State, error) {
	data, ok := fs.ReadStateFile(FileName)
	if !ok {
		return New(branch), nil
	}
	return Parse([]byte(data))
}

// Update changes the review of a branch: it reads what is on disk now, hands
// it to change, and writes the result back, all under the lock, so that two
// updates made at the same moment both land.
//
// An error from change is returned as it is and nothing is written. Neither
// is a review that change left as it found it, so the file moves only when
// the review does. Nor, above all, is one that would not parse: that is
// somebody's write caught halfway, and theirs to finish.
func Update(fs Files, branch string, wait time.Duration, change func(*State) error) error {
	unlock, err := Lock(fs, wait)
	if err != nil {
		return err
	}
	defer unlock()

	if err := adopt(fs, branch); err != nil {
		return err
	}
	before, existed := fs.ReadStateFile(FileName)
	st := New(branch)
	if existed {
		if st, err = Parse([]byte(before)); err != nil {
			return err
		}
	}
	if err := change(st); err != nil {
		return err
	}
	st.Branch = branch
	after := string(st.Bytes())
	if existed && after == before {
		return nil
	}
	return fs.WriteStateFile(FileName, after)
}
