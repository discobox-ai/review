// Package git wraps the git plumbing/porcelain commands that git-gui drives.
package git

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// Repo is a handle on a working tree.
type Repo struct {
	Dir    string // absolute path to the top level of the working tree
	GitDir string // absolute path to the .git directory
}

// Open locates the working tree that contains dir.
func Open(dir string) (*Repo, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	out, err := run(abs, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("not a git repository: %s", abs)
	}
	r := &Repo{Dir: strings.TrimSpace(out)}

	gd, err := run(r.Dir, "rev-parse", "--git-dir")
	if err != nil {
		return nil, err
	}
	r.GitDir = strings.TrimSpace(gd)
	if !filepath.IsAbs(r.GitDir) {
		r.GitDir = filepath.Join(r.Dir, r.GitDir)
	}
	return r, nil
}

func run(dir string, args ...string) (string, error) {
	return runInput(dir, nil, args...)
}

func runInput(dir string, stdin []byte, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.Env = append(os.Environ(), "GIT_PAGER=cat", "LC_ALL=C")
	err := cmd.Run()
	if err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), errors.New(msg)
	}
	return stdout.String(), nil
}

// Git runs an arbitrary git command in the work tree and returns its output.
func (r *Repo) Git(args ...string) (string, error) { return run(r.Dir, args...) }

// Name is the repository name shown in the window title, as git-gui does it.
func (r *Repo) Name() string { return filepath.Base(r.Dir) }

// ---------------------------------------------------------------------------
// File states
// ---------------------------------------------------------------------------

// State is git-gui's two character file state: the first character describes
// the index (HEAD vs index) and the second the working directory (index vs
// file). '_' means "no difference", 'O' means untracked and 'U' unmerged.
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

// Staged reports whether the path shows up in the "Staged Changes" list.
// This mirrors git-gui's display_all_files: anything whose index character is
// neither '_' nor 'U'.
func (s State) Staged() bool {
	c := s.Index()
	return c != '_' && c != 'U'
}

// Unstaged reports whether the path shows up in the "Unstaged Changes" list.
func (s State) Unstaged() bool {
	if s.Index() == 'U' {
		return true
	}
	return s.Work() != '_'
}

// File is one entry of the scan.
type File struct {
	Path  string
	From  string // original path for renames/copies
	State State
}

// stateDescriptions is git-gui's all_descs table.
var stateDescriptions = map[State]string{
	"__": "Unmodified",

	"_M": "Modified, not staged",
	"M_": "Staged for commit",
	"MM": "Portions staged for commit",
	"MD": "Staged for commit, missing",

	"_T": "File type changed, not staged",
	"MT": "File type changed, old type staged for commit",
	"AT": "File type changed, old type staged for commit",
	"T_": "File type changed, staged",
	"TM": "File type change staged, modification not staged",
	"TD": "File type change staged, file missing",

	"_O": "Untracked, not staged",
	"A_": "Staged for commit",
	"AM": "Portions staged for commit",
	"AD": "Staged for commit, missing",

	"_D": "Missing",
	"D_": "Staged for removal",
	"DO": "Staged for removal, still present",

	"_U": "Requires merge resolution",
	"U_": "Requires merge resolution",
	"UU": "Requires merge resolution",
	"UM": "Requires merge resolution",
	"UD": "Requires merge resolution",
	"UT": "Requires merge resolution",

	"R_": "Staged for commit",
	"RM": "Portions staged for commit",
	"RD": "Staged for commit, missing",
	"C_": "Staged for commit",
	"CM": "Portions staged for commit",
}

// Describe returns the human readable status shown in the diff header.
func (s State) Describe() string {
	if d, ok := stateDescriptions[s]; ok {
		return d
	}
	return "Unknown state " + string(s)
}

// Status scans the working tree, comparing the index against parent.
//
// This is git-gui's rescan, and parent is its PARENT: the commit the index is
// measured against. Normally that is HEAD, but while amending it is HEAD's own
// parent, so the staged list shows everything the amended commit will contain
// rather than only what was added on top of it. Use AmendParent to pick it.
//
// The scan is assembled the way git-gui assembles it, from three commands: the
// index side, the working directory side, and the untracked files. A single
// "git status" cannot express it, because status always measures the index
// against HEAD.
func (r *Repo) Status(parent string) ([]File, error) {
	if parent == "" {
		parent = "HEAD"
	}
	states := map[string]*[2]byte{}
	froms := map[string]string{}
	at := func(path string) *[2]byte {
		if s, ok := states[path]; ok {
			return s
		}
		s := &[2]byte{'_', '_'}
		states[path] = s
		return s
	}

	// The index side: HEAD (or the amend parent) versus the index.
	out, err := run(r.Dir, "diff-index", "--cached", "-M",
		"--ignore-submodules=dirty", "-z", parent)
	if err != nil {
		return nil, err
	}
	parseRawDiff(out, func(status byte, path, from string) {
		at(path)[0] = status
		if from != "" {
			froms[path] = from
		}
	})

	// The working directory side: the index versus the files on disk. This is
	// the same whether or not we are amending.
	out, err = run(r.Dir, "diff-files", "--ignore-submodules=dirty", "-z")
	if err != nil {
		return nil, err
	}
	parseRawDiff(out, func(status byte, path, _ string) {
		at(path)[1] = status
	})

	// Untracked files show as 'O' on the working directory side.
	out, err = run(r.Dir, "ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	for _, path := range splitZ(out) {
		at(path)[1] = 'O'
	}

	// Unmerged paths override both sides.
	out, err = run(r.Dir, "ls-files", "--unmerged", "-z")
	if err != nil {
		return nil, err
	}
	for _, rec := range splitZ(out) {
		// "<mode> <sha> <stage>\t<path>"
		if i := strings.IndexByte(rec, '\t'); i >= 0 {
			s := at(rec[i+1:])
			s[0], s[1] = 'U', 'U'
		}
	}

	files := make([]File, 0, len(states))
	for path, s := range states {
		state := mapState(s[0], s[1])
		if state == "__" {
			continue
		}
		files = append(files, File{Path: path, From: froms[path], State: state})
	}
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// splitZ splits NUL terminated output into its records.
func splitZ(out string) []string {
	var records []string
	for _, rec := range strings.Split(out, "\x00") {
		if rec != "" {
			records = append(records, rec)
		}
	}
	return records
}

// parseRawDiff walks the raw -z output of diff-index and diff-files, whose
// records are ":<srcmode> <dstmode> <srcsha> <dstsha> <status>" followed by the
// path, or by the source and destination paths for a rename or copy.
func parseRawDiff(out string, fn func(status byte, path, from string)) {
	fields := strings.Split(out, "\x00")
	for i := 0; i < len(fields); i++ {
		header := fields[i]
		if !strings.HasPrefix(header, ":") {
			continue
		}
		parts := strings.Fields(header)
		if len(parts) == 0 {
			continue
		}
		status := parts[len(parts)-1]
		if status == "" {
			continue
		}
		// A rename or copy carries a similarity score, "R100", and two paths.
		if (status[0] == 'R' || status[0] == 'C') && i+2 < len(fields) {
			from, path := fields[i+1], fields[i+2]
			i += 2
			fn(status[0], path, from)
			continue
		}
		if i+1 >= len(fields) {
			return
		}
		i++
		fn(status[0], fields[i], "")
	}
}

// StatusParent returns the revision Status should compare the index against:
// HEAD normally, HEAD's own parent while amending, and the empty tree when
// there is no commit yet.
func (r *Repo) StatusParent(amend bool) string {
	if !r.HasHEAD() {
		return EmptyTree
	}
	if amend {
		return r.AmendParent()
	}
	return "HEAD"
}

// AmendParent returns the revision the index should be compared against while
// amending: HEAD's first parent, or the empty tree when HEAD is the initial
// commit and there is nothing before it.
func (r *Repo) AmendParent() string {
	out, err := run(r.Dir, "rev-list", "--parents", "-n", "1", "HEAD")
	if err != nil {
		return EmptyTree
	}
	fields := strings.Fields(strings.TrimSpace(out))
	if len(fields) < 2 {
		return EmptyTree
	}
	return fields[1]
}

// mapState converts porcelain XY codes into git-gui's state characters.
func mapState(x, y byte) State {
	if x == '?' && y == '?' {
		return "_O"
	}
	// Unmerged combinations: DD AU UD UA DU AA UU
	if x == 'U' || y == 'U' || (x == 'A' && y == 'A') || (x == 'D' && y == 'D') {
		return "UU"
	}
	conv := func(c byte) byte {
		if c == ' ' {
			return '_'
		}
		return c
	}
	return State([]byte{conv(x), conv(y)})
}

// ---------------------------------------------------------------------------
// Branch / HEAD information
// ---------------------------------------------------------------------------

// CurrentBranch returns the checked out branch, or a detached HEAD label.
func (r *Repo) CurrentBranch() string {
	if out, err := run(r.Dir, "symbolic-ref", "--short", "HEAD"); err == nil {
		return strings.TrimSpace(out)
	}
	if out, err := run(r.Dir, "rev-parse", "--short", "HEAD"); err == nil {
		return "(detached " + strings.TrimSpace(out) + ")"
	}
	return "(unknown)"
}

// HasHEAD reports whether any commit exists yet.
func (r *Repo) HasHEAD() bool {
	_, err := run(r.Dir, "rev-parse", "--verify", "HEAD")
	return err == nil
}

// IsMerging reports whether a merge is in progress (MERGE_HEAD present).
func (r *Repo) IsMerging() bool {
	_, err := os.Stat(filepath.Join(r.GitDir, "MERGE_HEAD"))
	return err == nil
}

// MergeMessage returns the prepared merge commit message, if any.
func (r *Repo) MergeMessage() string {
	msg, _ := r.ReadStateFile("MERGE_MSG")
	return msg
}

// Branches lists local branch names.
func (r *Repo) Branches() ([]string, error) {
	out, err := run(r.Dir, "for-each-ref", "--format=%(refname:short)", "refs/heads")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l != "" {
			names = append(names, l)
		}
	}
	return names, nil
}

// Remotes lists configured remotes.
func (r *Repo) Remotes() ([]string, error) {
	out, err := run(r.Dir, "remote")
	if err != nil {
		return nil, err
	}
	var names []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l != "" {
			names = append(names, l)
		}
	}
	return names, nil
}

// ---------------------------------------------------------------------------
// Persisted commit buffer
// ---------------------------------------------------------------------------

// git-gui stashes the commit message buffer in the git directory so it
// survives quitting: MsgFile is written on a clean exit, BackupFile is a
// periodic crash backup. difftui reuses the same names, so a message typed in
// one tool is picked up by the other.
const (
	MsgFile    = "GITGUI_MSG"
	BackupFile = "GITGUI_BCK"
)

// ReadStateFile reads a file from the git directory.
func (r *Repo) ReadStateFile(name string) (string, bool) {
	b, err := os.ReadFile(filepath.Join(r.GitDir, name))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// WriteStateFile writes a file into the git directory.
func (r *Repo) WriteStateFile(name, content string) error {
	return os.WriteFile(filepath.Join(r.GitDir, name), []byte(content), 0o644)
}

// RemoveStateFile deletes a file from the git directory, ignoring a missing
// file.
func (r *Repo) RemoveStateFile(name string) error {
	err := os.Remove(filepath.Join(r.GitDir, name))
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}

// RenameStateFile moves one git directory file over another.
func (r *Repo) RenameStateFile(from, to string) error {
	return os.Rename(filepath.Join(r.GitDir, from), filepath.Join(r.GitDir, to))
}

// StateFileNewer reports whether a is strictly newer than b. It is false when
// either file is missing.
func (r *Repo) StateFileNewer(a, b string) bool {
	sa, err := os.Stat(filepath.Join(r.GitDir, a))
	if err != nil {
		return false
	}
	sb, err := os.Stat(filepath.Join(r.GitDir, b))
	if err != nil {
		return false
	}
	return sa.ModTime().After(sb.ModTime())
}

// StateFileExists reports whether a git directory file is present.
func (r *Repo) StateFileExists(name string) bool {
	_, err := os.Stat(filepath.Join(r.GitDir, name))
	return err == nil
}

// SquashMessage returns .git/SQUASH_MSG, used after a squash merge.
func (r *Repo) SquashMessage() string {
	msg, _ := r.ReadStateFile("SQUASH_MSG")
	return msg
}

// CommitTemplate returns the contents of commit.template, if one is set.
func (r *Repo) CommitTemplate() string {
	out, err := run(r.Dir, "config", "commit.template")
	if err != nil {
		return ""
	}
	path := strings.TrimSpace(out)
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := os.UserHomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(r.Dir, path)
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return ""
	}
	return string(b)
}

// ---------------------------------------------------------------------------
// Diffs
// ---------------------------------------------------------------------------

// Side identifies which of the two file lists a diff was requested from.
type Side int

const (
	// SideWorkdir is the "Unstaged Changes" list.
	SideWorkdir Side = iota
	// SideIndex is the "Staged Changes (Will Commit)" list.
	SideIndex
)

// Diff produces the unified diff git-gui shows for a file, with the given
// number of context lines.
//
// parent is the revision the index is measured against, as in Status: the
// staged diff of a file while amending has to be taken against HEAD's parent,
// or it shows only what was staged on top of the commit being amended.
func (r *Repo) Diff(f File, side Side, context int, parent string) (string, error) {
	ctx := fmt.Sprintf("-U%d", context)

	if f.State == "_O" {
		// Untracked: git-gui shows the whole file as an addition.
		return r.untrackedDiff(f.Path, ctx)
	}
	if parent == "" {
		parent = r.StatusParent(false)
	}

	args := []string{"diff", "--no-color", "--no-ext-diff", ctx}
	if side == SideIndex {
		args = append(args, "--cached", parent, "--", f.Path)
	} else {
		args = append(args, "--", f.Path)
	}
	out, err := run(r.Dir, args...)
	if err != nil && strings.TrimSpace(out) == "" {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		return "* No differences detected.\n", nil
	}
	return out, nil
}

// EmptyTree is the well known hash of git's empty tree object. It stands in
// for the parent of an initial commit.
const EmptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

func (r *Repo) untrackedDiff(path, ctx string) (string, error) {
	info, err := os.Stat(filepath.Join(r.Dir, path))
	if err != nil {
		return "", err
	}
	if info.IsDir() {
		return "* " + path + " is a directory.\n", nil
	}
	// --no-index exits 1 when files differ, which is the normal case here.
	out, _ := run(r.Dir, "diff", "--no-color", "--no-ext-diff", ctx,
		"--no-index", "--", os.DevNull, path)
	if strings.TrimSpace(out) == "" {
		return "* " + path + " is empty.\n", nil
	}
	return out, nil
}

// ---------------------------------------------------------------------------
// Index manipulation
// ---------------------------------------------------------------------------

// Stage adds paths to the index ("Stage To Commit").
func (r *Repo) Stage(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	args := append([]string{"add", "--"}, paths...)
	_, err := run(r.Dir, args...)
	return err
}

// StageChanged stages every tracked file that was modified or deleted, which
// is git-gui's "Stage Changed Files To Commit".
func (r *Repo) StageChanged() error {
	_, err := run(r.Dir, "add", "--update", "--", ".")
	return err
}

// Unstage removes paths from the index ("Unstage From Commit"), restoring
// their entries from parent.
//
// While amending, parent is HEAD's parent, so unstaging a file drops it out of
// the amended commit entirely rather than resetting it to the very commit
// being rewritten. git-gui does this by feeding update-index the PARENT entry
// it recorded during the scan; resetting the paths to that revision is the
// same thing.
func (r *Repo) Unstage(paths []string, parent string) error {
	if len(paths) == 0 {
		return nil
	}
	if parent == "" {
		parent = r.StatusParent(false)
	}
	if !r.HasHEAD() {
		args := append([]string{"rm", "--cached", "-r", "-q", "--"}, paths...)
		_, err := run(r.Dir, args...)
		return err
	}
	args := append([]string{"reset", "-q", parent, "--"}, paths...)
	_, err := run(r.Dir, args...)
	return err
}

// Revert throws away working directory changes ("Revert Changes").
func (r *Repo) Revert(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	args := append([]string{"checkout", "-f", "--"}, paths...)
	_, err := run(r.Dir, args...)
	return err
}

// Delete removes untracked files from disk.
func (r *Repo) Delete(paths []string) error {
	for _, p := range paths {
		if err := os.RemoveAll(filepath.Join(r.Dir, p)); err != nil {
			return err
		}
	}
	return nil
}

// ApplyPatch feeds a patch to git apply. When cached is true the patch is
// applied to the index only; reverse applies it backwards.
func (r *Repo) ApplyPatch(patch string, cached, reverse bool) error {
	args := []string{"apply", "--whitespace=nowarn"}
	if cached {
		args = append(args, "--cached")
	}
	if reverse {
		args = append(args, "--reverse")
	}
	args = append(args, "-")
	_, err := runInput(r.Dir, []byte(patch), args...)
	return err
}

// ---------------------------------------------------------------------------
// Committing
// ---------------------------------------------------------------------------

// Commit creates a commit from the current index.
func (r *Repo) Commit(message string, amend bool) (string, error) {
	args := []string{"commit", "--cleanup=strip", "-F", "-"}
	if amend {
		args = append(args, "--amend")
	}
	out, err := runInput(r.Dir, []byte(message), args...)
	return out, err
}

// LastCommitMessage returns HEAD's message, used when switching to amend mode.
func (r *Repo) LastCommitMessage() string {
	out, err := run(r.Dir, "log", "-1", "--pretty=format:%B")
	if err != nil {
		return ""
	}
	return strings.TrimRight(out, "\n")
}

// SignOff returns the "Signed-off-by" trailer for the current committer.
func (r *Repo) SignOff() (string, error) {
	name, err := run(r.Dir, "config", "user.name")
	if err != nil {
		return "", errors.New("user.name is not set")
	}
	email, err := run(r.Dir, "config", "user.email")
	if err != nil {
		return "", errors.New("user.email is not set")
	}
	return fmt.Sprintf("Signed-off-by: %s <%s>", strings.TrimSpace(name), strings.TrimSpace(email)), nil
}

// ---------------------------------------------------------------------------
// Transport / merge
// ---------------------------------------------------------------------------

// Push pushes a branch to a remote.
func (r *Repo) Push(remote, branch string, force, thin bool) (string, error) {
	args := []string{"push", "-v"}
	if force {
		args = append(args, "--force")
	}
	if thin {
		args = append(args, "--thin")
	}
	args = append(args, remote, branch)
	return run(r.Dir, args...)
}

// Merge merges a revision into the current branch.
func (r *Repo) Merge(rev string) (string, error) {
	return run(r.Dir, "merge", "--no-ff", rev)
}

// AbortMerge resets a merge in progress.
func (r *Repo) AbortMerge() (string, error) {
	return run(r.Dir, "merge", "--abort")
}

// Checkout switches branches.
func (r *Repo) Checkout(branch string) (string, error) {
	return run(r.Dir, "checkout", branch)
}

// CreateBranch creates and checks out a new branch.
func (r *Repo) CreateBranch(name string) (string, error) {
	return run(r.Dir, "checkout", "-b", name)
}
