package review

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
}

// Adopt makes the file at [FileName] the review of branch.
//
// A review is of a branch, so switching branches has to switch reviews. Doing
// it by moving files rather than by keying them all inside one means the live
// file is always at one path, holding one review, in the flattest shape
// anything reading it could ask for — which is the whole reason the state is a
// file.
func Adopt(fs Files, branch string) {
	if branch == "" {
		return
	}
	if data, ok := fs.ReadStateFile(FileName); ok {
		st, err := Parse([]byte(data))
		if err != nil || st.Branch == "" || st.Branch == branch {
			return
		}
		_ = fs.RenameStateFile(FileName, ArchiveName(st.Branch))
	}
	if fs.StateFileExists(ArchiveName(branch)) {
		_ = fs.RenameStateFile(ArchiveName(branch), FileName)
	}
}

// Load reads the review of a branch, adopting it first. A review that is not
// there yet is an empty one rather than an error; one that will not parse is
// an error, so that a caller can leave what it already has alone.
func Load(fs Files, branch string) (*State, error) {
	Adopt(fs, branch)
	data, ok := fs.ReadStateFile(FileName)
	if !ok {
		return New(branch), nil
	}
	st, err := Parse([]byte(data))
	if err != nil {
		return nil, err
	}
	return st, nil
}

// Save writes the review out, stamped with the branch it is of.
func Save(fs Files, branch string, s *State) error {
	s.Branch = branch
	return fs.WriteStateFile(FileName, string(s.Bytes()))
}
