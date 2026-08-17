// Package git wraps the git plumbing/porcelain commands that git-gui drives.
package git

import (
	"errors"
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
)

// Repo is a handle on a working tree, reached through a System.
type Repo struct {
	Dir    string // path to the top level of the working tree
	GitDir string // path to the .git directory

	sys System
}

// Open locates the working tree that contains dir on the local machine.
func Open(dir string) (*Repo, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	return OpenWith(Local{}, abs)
}

// OpenWith locates the working tree that contains dir through sys. Every call
// the repository goes on to make — running git, reading a file — goes through
// that same sys, so a different backend needs nothing else.
func OpenWith(sys System, dir string) (*Repo, error) {
	out, err := sys.Git(dir, nil, "rev-parse", "--show-toplevel")
	if err != nil {
		return nil, fmt.Errorf("not a git repository: %s", dir)
	}
	r := &Repo{Dir: strings.TrimSpace(out), sys: sys}

	gd, err := sys.Git(r.Dir, nil, "rev-parse", "--git-dir")
	if err != nil {
		return nil, err
	}
	r.GitDir = strings.TrimSpace(gd)
	if !filepath.IsAbs(r.GitDir) {
		r.GitDir = filepath.Join(r.Dir, r.GitDir)
	}
	return r, nil
}

// run is the repository's own shorthand for a git command with no input.
func (r *Repo) run(args ...string) (string, error) {
	return r.sys.Git(r.Dir, nil, args...)
}

// Git runs an arbitrary git command in the work tree and returns its output.
func (r *Repo) Git(args ...string) (string, error) { return r.run(args...) }

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

// changeDescriptions names the raw status characters diff-tree reports for a
// commit, which mean something different from the staging states above.
var changeDescriptions = map[byte]string{
	'M': "Modified", 'A': "Added", 'D': "Deleted",
	'R': "Renamed", 'C': "Copied", 'T': "File type changed",
}

// DescribeChange names the change a commit made, for a State whose index
// character came from diff-tree rather than from a working tree scan.
func (s State) DescribeChange() string {
	if d, ok := changeDescriptions[s.Index()]; ok {
		return d
	}
	return "Changed"
}

// Describe returns the human readable status shown in the diff header.
func (s State) Describe() string {
	if d, ok := stateDescriptions[s]; ok {
		return d
	}
	return "Unknown state " + string(s)
}

// Scan is everything one rescan learns about the repository: the file states
// and the facts the display needs alongside them.
//
// Parent is the revision the scan was taken against, and the one every diff
// from it has to be taken against too. Handing it back means the caller keeps
// it for the diffs that follow rather than asking git again for each.
type Scan struct {
	Files   []File
	Branch  string
	Parent  string
	Base    Base     // empty Ref when there is no remote to measure against
	Commits []Commit // the branch's own commits, newest first
	HasHEAD bool
	Merging bool
	Merge   string // the prepared merge message, when a merge is in progress
}

// Rescan collects the whole scan in one go, running each git command once.
// amend picks the parent, as in Status.
func (r *Repo) Rescan(amend bool) (Scan, error) {
	s := Scan{HasHEAD: r.HasHEAD()}
	s.Parent = r.statusParent(s.HasHEAD, amend)
	s.Branch = r.CurrentBranch()
	if s.HasHEAD {
		s.Base, _ = r.MergeBase()
		if s.Base.Ahead > 0 {
			s.Commits, _ = r.Commits(s.Base.Ref)
		}
	}
	s.Merging = r.IsMerging()
	if s.Merging {
		s.Merge = r.MergeMessage()
	}
	files, err := r.Status(s.Parent)
	if err != nil {
		return s, err
	}
	s.Files = files
	return s, nil
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
	out, err := r.run("diff-index", "--cached", "-M",
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
	out, err = r.run("diff-files", "--ignore-submodules=dirty", "-z")
	if err != nil {
		return nil, err
	}
	parseRawDiff(out, func(status byte, path, _ string) {
		at(path)[1] = status
	})

	// Untracked files show as 'O' on the working directory side.
	out, err = r.run("ls-files", "--others", "--exclude-standard", "-z")
	if err != nil {
		return nil, err
	}
	for _, path := range splitZ(out) {
		at(path)[1] = 'O'
	}

	// Unmerged paths override both sides.
	out, err = r.run("ls-files", "--unmerged", "-z")
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
	return r.statusParent(r.HasHEAD(), amend)
}

// statusParent is StatusParent with the HEAD check already made, so a rescan
// that has to know about HEAD anyway does not run rev-parse for it twice.
func (r *Repo) statusParent(hasHEAD, amend bool) string {
	switch {
	case !hasHEAD:
		return EmptyTree
	case amend:
		return r.AmendParent()
	default:
		return "HEAD"
	}
}

// AmendParent returns the revision the index should be compared against while
// amending: HEAD's first parent, or the empty tree when HEAD is the initial
// commit and there is nothing before it.
func (r *Repo) AmendParent() string {
	out, err := r.run("rev-list", "--parents", "-n", "1", "HEAD")
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
	if out, err := r.run("symbolic-ref", "--short", "HEAD"); err == nil {
		return strings.TrimSpace(out)
	}
	if out, err := r.run("rev-parse", "--short", "HEAD"); err == nil {
		return "(detached " + strings.TrimSpace(out) + ")"
	}
	return "(unknown)"
}

// HasHEAD reports whether any commit exists yet.
func (r *Repo) HasHEAD() bool {
	_, err := r.run("rev-parse", "--verify", "HEAD")
	return err == nil
}

// IsMerging reports whether a merge is in progress (MERGE_HEAD present).
func (r *Repo) IsMerging() bool {
	_, err := r.sys.Stat(filepath.Join(r.GitDir, "MERGE_HEAD"))
	return err == nil
}

// MergeMessage returns the prepared merge commit message, if any.
func (r *Repo) MergeMessage() string {
	msg, _ := r.ReadStateFile("MERGE_MSG")
	return msg
}

// Base is where the current branch stands against the ref it is built on: the
// commit the two last had in common, and how far each has moved since.
type Base struct {
	Ref    string // the branch measured against, "origin/main"
	Commit string // abbreviated hash of the merge base
	Ahead  int    // commits on HEAD since the merge base
	Behind int    // commits on Ref since the merge base
}

// MergeBase measures HEAD against the ref it is built on. It reports false
// when there is nothing to measure against, or no commit yet — both ordinary
// states rather than errors.
func (r *Repo) MergeBase() (Base, bool) {
	ref, ok := r.baseRef()
	if !ok {
		return Base{}, false
	}
	out, err := r.run("merge-base", "HEAD", ref)
	if err != nil {
		// Unrelated histories have no merge base at all.
		return Base{}, false
	}
	sha := strings.TrimSpace(out)
	if sha == "" {
		return Base{}, false
	}
	b := Base{Ref: ref, Commit: abbrev(sha)}
	// --left-right counts both sides of the fork in one pass: commits reachable
	// only from HEAD, then only from ref.
	if out, err := r.run("rev-list", "--left-right", "--count", "HEAD..."+ref); err == nil {
		if fields := strings.Fields(out); len(fields) == 2 {
			b.Ahead, _ = strconv.Atoi(fields[0])
			b.Behind, _ = strconv.Atoi(fields[1])
		}
	}
	return b, true
}

// abbrevLen is how much of a hash is shown, matching git's own default.
const abbrevLen = 7

func abbrev(sha string) string {
	if len(sha) > abbrevLen {
		return sha[:abbrevLen]
	}
	return sha
}

// baseRef is the ref the branch is measured against: what it tracks, when it
// tracks anything, and otherwise the remote's default branch.
//
// Tracking comes first because it is what the branch was told to build on,
// rather than a guess at where it probably started, and because it is what
// the rest of git already reports — git status, git branch -vv, and the
// target of a bare push or pull all name the same ref. A tool that disagreed
// with those about where the branch stands would be worse than one that said
// nothing, since you would have to remember which to believe.
func (r *Repo) baseRef() (string, bool) {
	if ref, ok := r.upstreamRef(); ok {
		return ref, true
	}
	return r.defaultRemoteBranch()
}

// upstreamRef is the branch this one is configured to build on: git's
// @{upstream}, from branch.<name>.remote and branch.<name>.merge. git treats a
// branch that tracks nothing as an error rather than an empty answer.
func (r *Repo) upstreamRef() (string, bool) {
	out, err := r.run("rev-parse", "--abbrev-ref", "@{upstream}")
	if err != nil {
		return "", false
	}
	ref := strings.TrimSpace(out)
	return ref, ref != ""
}

// defaultRemoteBranch is the remote's own HEAD — origin/main on most
// repositories. git records it only when the clone set it up, so a remote
// without one is tried for the usual names instead.
func (r *Repo) defaultRemoteBranch() (string, bool) {
	// origin is much the commonest case, and asking for its HEAD straight off
	// saves listing the remotes at all.
	if ref, ok := r.remoteHEAD("origin"); ok {
		return ref, true
	}
	remotes, err := r.Remotes()
	if err != nil {
		return "", false
	}
	for _, remote := range remotes {
		if remote == "origin" {
			continue // already tried, and its fallbacks are covered below
		}
		if ref, ok := r.remoteHEAD(remote); ok {
			return ref, true
		}
	}
	for _, remote := range remotes {
		for _, name := range []string{"main", "master"} {
			ref := remote + "/" + name
			if _, err := r.run("rev-parse", "--verify", "--quiet",
				"refs/remotes/"+ref); err == nil {
				return ref, true
			}
		}
	}
	return "", false
}

// remoteHEAD is the branch a remote points its own HEAD at.
func (r *Repo) remoteHEAD(remote string) (string, bool) {
	out, err := r.run("symbolic-ref", "--short", "refs/remotes/"+remote+"/HEAD")
	if err != nil {
		return "", false
	}
	ref := strings.TrimSpace(out)
	return ref, ref != ""
}

// Commit is one entry of the branch's own history: a commit made since the
// merge base, and so one of the commits this branch is adding.
type Commit struct {
	Hash    string
	Short   string
	Subject string
}

// Commits lists the commits on HEAD that base does not have, newest first.
// This is the same set rev-list counts as "ahead".
func (r *Repo) Commits(base string) ([]Commit, error) {
	if base == "" {
		return nil, nil
	}
	out, err := r.run("log", "--format=%H %s", base+"..HEAD")
	if err != nil {
		return nil, err
	}
	var commits []Commit
	for _, line := range strings.Split(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		hash, subject, _ := strings.Cut(line, " ")
		if hash == "" {
			continue
		}
		commits = append(commits, Commit{
			Hash: hash, Short: abbrev(hash), Subject: subject,
		})
	}
	return commits, nil
}

// CommitFiles lists what a commit changed, against its parent. The status
// character lands on the index side of the state, so "M_" reads as "this
// commit modified it"; DescribeChange names them.
//
// --root makes the very first commit on a branch list its files rather than
// nothing, having no parent to be compared against.
func (r *Repo) CommitFiles(hash string) ([]File, error) {
	out, err := r.run("diff-tree", "--no-commit-id", "--root", "-M",
		"--ignore-submodules=dirty", "-r", "-z", hash)
	if err != nil {
		return nil, err
	}
	var files []File
	froms := map[string]string{}
	parseRawDiff(out, func(status byte, path, from string) {
		if from != "" {
			froms[path] = from
		}
		files = append(files, File{
			Path: path, From: from, State: State([]byte{status, '_'}),
		})
	})
	sort.Slice(files, func(i, j int) bool { return files[i].Path < files[j].Path })
	return files, nil
}

// CommitDiff is the change one commit made to one file, against its parent.
func (r *Repo) CommitDiff(hash string, f File, context int) (string, error) {
	out, err := r.run("show", "--no-color", "--no-ext-diff", "--format=",
		fmt.Sprintf("-U%d", context), "-M", hash, "--", f.Path)
	if err != nil && strings.TrimSpace(out) == "" {
		return "", err
	}
	if strings.TrimSpace(out) == "" {
		return "* No differences detected.\n", nil
	}
	return out, nil
}

// Parent returns a commit's first parent, and false when it has none — which
// only the root commit of a repository does.
func (r *Repo) Parent(hash string) (string, bool) {
	out, err := r.run("rev-parse", "--verify", "--quiet", hash+"^")
	if err != nil {
		return "", false
	}
	parent := strings.TrimSpace(out)
	return parent, parent != ""
}

// ResetSoft moves the current branch to rev and touches nothing else: the
// index and the working tree are left exactly as they were.
//
// This is how commits are popped off a branch without losing the work in them.
// Everything the popped commits contained is still in the index afterwards,
// because the index never moved — it is the branch that came back to meet it.
func (r *Repo) ResetSoft(rev string) error {
	_, err := r.run("reset", "--soft", rev)
	return err
}

// Edit opens a file from the working tree in the user's editor. It refuses a
// path that is not on disk — a file staged for deletion, say — since opening
// that would create it rather than edit it.
func (r *Repo) Edit(path string) (Editor, error) {
	full := filepath.Join(r.Dir, path)
	if _, err := r.sys.Stat(full); err != nil {
		return nil, fmt.Errorf("%s is not in the working tree", path)
	}
	return r.sys.Editor(r.Dir, full), nil
}

// FileContent is a file's whole text: as the working tree has it when commit
// is empty, or as commit recorded it.
func (r *Repo) FileContent(commit, path string) (string, error) {
	if commit == "" {
		b, err := r.sys.ReadFile(filepath.Join(r.Dir, path))
		if err != nil {
			return "", fmt.Errorf("%s is not in the working tree", path)
		}
		return string(b), nil
	}
	return r.run("show", commit+":"+path)
}

// EditAt opens a file as it stands in a commit, or in the working tree when
// commit is empty, where it defers to Edit.
//
// History cannot be edited, but the editor is still the most familiar place
// to read a file in, so the commit's version is copied to a read-only
// temporary file and the editor opened on that. The copy keeps the file's own
// name, in a directory of its own, so the editor still recognises what it is.
// The returned path is that copy — empty for the working tree — and the
// caller hands it back to RemoveTempCopy once the editor exits.
func (r *Repo) EditAt(commit, path string) (Editor, string, error) {
	if commit == "" {
		ed, err := r.Edit(path)
		return ed, "", err
	}
	content, err := r.FileContent(commit, path)
	if err != nil {
		return nil, "", err
	}
	tmp, err := r.sys.WriteTempFile(filepath.Base(path), []byte(content))
	if err != nil {
		return nil, "", err
	}
	return r.sys.Editor(r.Dir, tmp), tmp, nil
}

// RemoveTempCopy deletes a copy made by EditAt, the directory holding it and
// all. It must only be given a path EditAt returned; the empty path for "the
// working tree itself" is accepted and is nothing to clean up.
func (r *Repo) RemoveTempCopy(path string) {
	if path != "" {
		_ = r.sys.RemoveAll(filepath.Dir(path))
	}
}

// Branches lists local branch names.
func (r *Repo) Branches() ([]string, error) {
	out, err := r.run("for-each-ref", "--format=%(refname:short)", "refs/heads")
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
	out, err := r.run("remote")
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
	b, err := r.sys.ReadFile(filepath.Join(r.GitDir, name))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// WriteStateFile writes a file into the git directory.
func (r *Repo) WriteStateFile(name, content string) error {
	return r.sys.WriteFile(filepath.Join(r.GitDir, name), []byte(content))
}

// RemoveStateFile deletes a file from the git directory, ignoring a missing
// file.
func (r *Repo) RemoveStateFile(name string) error {
	return r.sys.Remove(filepath.Join(r.GitDir, name))
}

// RenameStateFile moves one git directory file over another.
func (r *Repo) RenameStateFile(from, to string) error {
	return r.sys.Rename(filepath.Join(r.GitDir, from), filepath.Join(r.GitDir, to))
}

// StateFileNewer reports whether a is strictly newer than b. It is false when
// either file is missing.
func (r *Repo) StateFileNewer(a, b string) bool {
	sa, err := r.sys.Stat(filepath.Join(r.GitDir, a))
	if err != nil {
		return false
	}
	sb, err := r.sys.Stat(filepath.Join(r.GitDir, b))
	if err != nil {
		return false
	}
	return sa.ModTime.After(sb.ModTime)
}

// StateFileExists reports whether a git directory file is present.
func (r *Repo) StateFileExists(name string) bool {
	_, err := r.sys.Stat(filepath.Join(r.GitDir, name))
	return err == nil
}

// SquashMessage returns .git/SQUASH_MSG, used after a squash merge.
func (r *Repo) SquashMessage() string {
	msg, _ := r.ReadStateFile("SQUASH_MSG")
	return msg
}

// CommitTemplate returns the contents of commit.template, if one is set.
func (r *Repo) CommitTemplate() string {
	out, err := r.run("config", "commit.template")
	if err != nil {
		return ""
	}
	path := strings.TrimSpace(out)
	if path == "" {
		return ""
	}
	if strings.HasPrefix(path, "~/") {
		if home, err := r.sys.HomeDir(); err == nil {
			path = filepath.Join(home, path[2:])
		}
	}
	if !filepath.IsAbs(path) {
		path = filepath.Join(r.Dir, path)
	}
	b, err := r.sys.ReadFile(path)
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
	// SideCommit is a file as one of the branch's own commits changed it,
	// which is history rather than anything stageable.
	SideCommit
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
	out, err := r.run(args...)
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
	info, err := r.sys.Stat(filepath.Join(r.Dir, path))
	if err != nil {
		return "", err
	}
	if info.IsDir {
		return "* " + path + " is a directory.\n", nil
	}
	// --no-index exits 1 when files differ, which is the normal case here.
	out, _ := r.run("diff", "--no-color", "--no-ext-diff", ctx,
		"--no-index", "--", r.sys.DevNull(), path)
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
	_, err := r.run(args...)
	return err
}

// StageChanged stages every tracked file that was modified or deleted, which
// is git-gui's "Stage Changed Files To Commit".
func (r *Repo) StageChanged() error {
	_, err := r.run("add", "--update", "--", ".")
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
		_, err := r.run(args...)
		return err
	}
	args := append([]string{"reset", "-q", parent, "--"}, paths...)
	_, err := r.run(args...)
	return err
}

// Revert throws away working directory changes ("Revert Changes").
func (r *Repo) Revert(paths []string) error {
	if len(paths) == 0 {
		return nil
	}
	args := append([]string{"checkout", "-f", "--"}, paths...)
	_, err := r.run(args...)
	return err
}

// Delete removes untracked files from disk.
func (r *Repo) Delete(paths []string) error {
	for _, p := range paths {
		if err := r.sys.RemoveAll(filepath.Join(r.Dir, p)); err != nil {
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
	_, err := r.sys.Git(r.Dir, []byte(patch), args...)
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
	out, err := r.sys.Git(r.Dir, []byte(message), args...)
	return out, err
}

// LastCommitMessage returns HEAD's message, used when switching to amend mode.
func (r *Repo) LastCommitMessage() string {
	out, err := r.run("log", "-1", "--pretty=format:%B")
	if err != nil {
		return ""
	}
	return strings.TrimRight(out, "\n")
}

// SignOff returns the "Signed-off-by" trailer for the current committer.
func (r *Repo) SignOff() (string, error) {
	name, err := r.run("config", "user.name")
	if err != nil {
		return "", errors.New("user.name is not set")
	}
	email, err := r.run("config", "user.email")
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
	return r.run(args...)
}

// Merge merges a revision into the current branch.
func (r *Repo) Merge(rev string) (string, error) {
	return r.run("merge", "--no-ff", rev)
}

// AbortMerge resets a merge in progress.
func (r *Repo) AbortMerge() (string, error) {
	return r.run("merge", "--abort")
}

// Checkout switches branches.
func (r *Repo) Checkout(branch string) (string, error) {
	return r.run("checkout", branch)
}

// CreateBranch creates and checks out a new branch.
func (r *Repo) CreateBranch(name string) (string, error) {
	return r.run("checkout", "-b", name)
}
