// Package git wraps the git commands discobox-review drives.
//
// discobox-review reads a repository and never writes one: everything here either
// asks git a question or opens a file for reading. The one exception is
// checking out a branch, which is choosing what to review rather than
// changing what is under review.
package git

import (
	"fmt"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/discobox-ai/review/pkg/source"
)

// Repo is a handle on a working tree, reached through a System. Its state
// files are the ones in its git directory.
type Repo struct {
	StateDir

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
	r.StateDir = NewStateDir(sys, r.GitDir)
	return r, nil
}

// run is the repository's own shorthand for a git command with no input.
func (r *Repo) run(args ...string) (string, error) {
	return r.sys.Git(r.Dir, nil, args...)
}

// DefaultContext is how many lines of context a diff is taken with unless
// something says otherwise. It is git's own default of three widened a little:
// a review is read to be understood rather than applied, and the two lines
// either side are usually the ones that say what the change means.
const DefaultContext = 5

// EmptyTree is the well known hash of git's empty tree object. It stands in
// for the state of a repository with no commits in it.
const EmptyTree = "4b825dc642cb6eb9a060e54bf8d69288fbee4904"

// ---------------------------------------------------------------------------
// File states
// ---------------------------------------------------------------------------

// The types a scan is made of are the source package's, since they are what
// the window reads from any source, this one included.
type (
	State  = source.State
	File   = source.File
	Scan   = source.Scan
	Base   = source.Base
	Commit = source.Commit
	Stamp  = source.Stamp
)

// ---------------------------------------------------------------------------
// The scan
// ---------------------------------------------------------------------------

// Scan collects the whole scan in one go.
//
// baseRef is the ref the review is measured against, or empty to let discobox-review
// work one out. context is how many lines of context every diff is taken with,
// so that changing it re-reads the review rather than re-rendering a stale one.
func (r *Repo) Scan(baseRef string, context int) (Scan, error) {
	s := Scan{HasHEAD: r.HasHEAD()}
	s.Branch = r.CurrentBranch()
	s.Merging = r.IsMerging()
	s.Base = r.ResolveBase(baseRef, s.HasHEAD)
	if s.HasHEAD && s.Base.SHA != "" && s.Base.SHA != EmptyTree {
		s.Commits, _ = r.Commits(s.Base.SHA)
	}

	files, err := r.Status()
	if err != nil {
		return s, err
	}
	s.Files = files

	untracked := make([]string, 0, len(files))
	for _, f := range files {
		if f.State.Untracked() {
			untracked = append(untracked, f.Path)
		}
	}

	s.Diff, err = r.treeDiff(s.Base.SHA, context, untracked)
	if err != nil {
		return s, err
	}
	head := "HEAD"
	if !s.HasHEAD {
		head = EmptyTree
	}
	s.Pending, _ = r.treeDiff(head, context, untracked)
	return s, nil
}

// treeDiff is the whole working tree measured against a revision, with the
// untracked files git would otherwise leave out added on the end.
//
// One command covers every tracked path, staged and unstaged together, because
// a diff against a commit reads straight through the index. Untracked files
// have to be asked for one at a time — there is no revision they belong to —
// but they are the rare case, and leaving them out would mean a review that
// silently omits every new file.
func (r *Repo) treeDiff(rev string, context int, untracked []string) (string, error) {
	if rev == "" {
		rev = EmptyTree
	}
	out, err := r.run("diff", "--no-color", "--no-ext-diff",
		fmt.Sprintf("-U%d", context), "-M", rev)
	if err != nil && strings.TrimSpace(out) == "" {
		return "", err
	}
	var b strings.Builder
	b.WriteString(out)
	for _, path := range untracked {
		add, err := r.untrackedDiff(path, fmt.Sprintf("-U%d", context))
		if err != nil || add == "" {
			continue
		}
		if b.Len() > 0 && !strings.HasSuffix(b.String(), "\n") {
			b.WriteString("\n")
		}
		b.WriteString(add)
	}
	return b.String(), nil
}

// untrackedDiff shows a file git has never been told about as one long
// addition, which is what it is.
func (r *Repo) untrackedDiff(path, ctx string) (string, error) {
	info, err := r.sys.Stat(filepath.Join(r.Dir, path))
	if err != nil || info.IsDir {
		return "", err
	}
	// --no-index exits 1 when the files differ, which is the normal case here.
	out, _ := r.run("diff", "--no-color", "--no-ext-diff", ctx,
		"--no-index", "--", r.sys.DevNull(), path)
	return out, nil
}

// Status scans the working tree against HEAD.
//
// It is assembled from three commands: the index side, the working directory
// side, and the untracked files. A single "git status" cannot express it,
// because status collapses the two sides into one letter each and discobox-review
// wants both.
func (r *Repo) Status() ([]File, error) {
	parent := "HEAD"
	if !r.HasHEAD() {
		parent = EmptyTree
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

	// The index side: HEAD versus the index.
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

	// The working directory side: the index versus the files on disk.
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
		if _, after, ok := strings.Cut(rec, "\t"); ok {
			s := at(after)
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
	for rec := range strings.SplitSeq(out, "\x00") {
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

// mapState converts raw status characters into discobox-review's state characters.
func mapState(x, y byte) State {
	if x == '?' && y == '?' {
		return "_O"
	}
	// Unmerged combinations: DD AU UD UA DU AA UU
	if x == 'U' || y == 'U' || (x == 'A' && y == 'A') || (x == 'D' && y == 'D') {
		return "UU"
	}
	conv := func(c byte) byte {
		if c == ' ' || c == 0 {
			return '_'
		}
		return c
	}
	return State([]byte{conv(x), conv(y)})
}

// ---------------------------------------------------------------------------
// Branch and base
// ---------------------------------------------------------------------------

// CurrentBranch names the checked out branch, or the detached head.
func (r *Repo) CurrentBranch() string {
	out, err := r.run("symbolic-ref", "--short", "HEAD")
	if err == nil {
		if name := strings.TrimSpace(out); name != "" {
			return name
		}
	}
	out, err = r.run("rev-parse", "--short", "HEAD")
	if err != nil {
		return "(no branch)"
	}
	return "(detached " + strings.TrimSpace(out) + ")"
}

// HasHEAD reports whether the repository has any commit yet.
func (r *Repo) HasHEAD() bool {
	_, err := r.run("rev-parse", "--verify", "--quiet", "HEAD")
	return err == nil
}

// CommitOf is the full hash of the commit rev names, and false when it names
// none.
func (r *Repo) CommitOf(rev string) (string, bool) {
	out, err := r.run("rev-parse", "--verify", "--quiet", rev+"^{commit}")
	if err != nil {
		return "", false
	}
	return strings.TrimSpace(out), true
}

// Head is the commit checked out, in full, or empty when there is none yet.
func (r *Repo) Head() string {
	out, err := r.run("rev-parse", "--verify", "--quiet", "HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// IsMerging reports whether a merge is in progress.
func (r *Repo) IsMerging() bool { return r.StateFileExists("MERGE_HEAD") }

// ResolveBase settles what the review is measured from.
//
// A ref named by the user wins, and is reported back with its error rather
// than silently ignored — a review measured from somewhere other than where
// you asked is worse than one that says it could not do as it was told.
// Otherwise the branch's own upstream or the remote's default branch is used,
// and failing both the review falls back to HEAD, which turns discobox-review into a
// reader of uncommitted work rather than leaving it with nothing to show.
func (r *Repo) ResolveBase(ref string, hasHEAD bool) Base {
	if !hasHEAD {
		return Base{Ref: "the empty tree", SHA: EmptyTree, Commit: abbrev(EmptyTree), Auto: true}
	}
	auto := ref == ""
	var failed string
	if ref != "" {
		if _, err := r.run("rev-parse", "--verify", "--quiet", ref+"^{commit}"); err != nil {
			failed, ref = "no such ref: "+ref, ""
		}
	}
	if ref == "" {
		ref, _ = r.baseRef()
	}
	if ref == "" {
		// Nothing to measure against: review what is not committed.
		sha, _ := r.run("rev-parse", "HEAD")
		sha = strings.TrimSpace(sha)
		return Base{Ref: "HEAD", SHA: sha, Commit: abbrev(sha), Auto: true, Err: failed}
	}

	b := Base{Ref: ref, Auto: auto, Err: failed}
	out, err := r.run("merge-base", "HEAD", ref)
	if err != nil {
		// Unrelated histories have no merge base; measure from the ref itself.
		out, err = r.run("rev-parse", ref+"^{commit}")
		if err != nil {
			sha, _ := r.run("rev-parse", "HEAD")
			sha = strings.TrimSpace(sha)
			return Base{Ref: "HEAD", SHA: sha, Commit: abbrev(sha), Auto: true, Err: failed}
		}
	}
	b.SHA = strings.TrimSpace(out)
	b.Commit = abbrev(b.SHA)
	// --left-right counts both sides of the fork in one pass: commits reachable
	// only from HEAD, then only from ref.
	if out, err := r.run("rev-list", "--left-right", "--count", "HEAD..."+ref); err == nil {
		if fields := strings.Fields(out); len(fields) == 2 {
			b.Ahead, _ = strconv.Atoi(fields[0])
			b.Behind, _ = strconv.Atoi(fields[1])
		}
	}
	return b
}

// abbrevLen is how much of a hash is shown, matching git's own default.
const abbrevLen = 7

func abbrev(sha string) string {
	if len(sha) > abbrevLen {
		return sha[:abbrevLen]
	}
	return sha
}

// baseRef is the ref the branch is measured against when nobody has said: what
// it tracks, and otherwise the remote's default branch.
//
// Tracking comes first because it is what the branch was told to build on
// rather than a guess at where it probably started, and because it is what the
// rest of git already reports — git status, git branch -vv, and the target of
// a bare push or pull all name the same ref.
func (r *Repo) baseRef() (string, bool) {
	if ref, ok := r.upstreamRef(); ok {
		return ref, true
	}
	return r.defaultRemoteBranch()
}

// upstreamRef is the branch this one is configured to build on: git's
// @{upstream}. git treats a branch that tracks nothing as an error rather than
// an empty answer.
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

// Refs lists what the base could reasonably be set to: every remote-tracking
// branch and every local branch but the one checked out, since a branch
// measured against itself has nothing to review.
func (r *Repo) Refs() []string {
	current := r.CurrentBranch()
	var out []string
	seen := map[string]bool{}
	add := func(ref string) {
		if ref == "" || ref == current || seen[ref] {
			return
		}
		seen[ref] = true
		out = append(out, ref)
	}
	if ref, ok := r.upstreamRef(); ok {
		add(ref)
	}
	if ref, ok := r.defaultRemoteBranch(); ok {
		add(ref)
	}
	for _, spec := range []string{"refs/remotes", "refs/heads"} {
		text, err := r.run("for-each-ref", "--sort=-committerdate",
			"--format=%(refname:short)", spec)
		if err != nil {
			continue
		}
		for l := range strings.SplitSeq(strings.TrimSpace(text), "\n") {
			l = strings.TrimSpace(l)
			if l == "" || strings.HasSuffix(l, "/HEAD") {
				continue
			}
			add(l)
		}
	}
	return out
}

// ---------------------------------------------------------------------------
// History
// ---------------------------------------------------------------------------

// Commits lists the commits on HEAD that base does not have, newest first.
func (r *Repo) Commits(base string) ([]Commit, error) {
	if base == "" {
		return nil, nil
	}
	return r.log(base + "..HEAD")
}

// Recent lists the last n commits on HEAD, newest first.
//
// It is what the base picker offers when no ref is the right answer: measuring
// from one of your own commits is how you say "review what I did after this
// point", and the branch's history is the only place that point has a name.
func (r *Repo) Recent(n int) ([]Commit, error) {
	if n <= 0 {
		return nil, nil
	}
	return r.log("-n", strconv.Itoa(n), "HEAD")
}

// log reads commits in discobox-review's own format. The fields are separated by a
// unit separator rather than a space, so that a subject may hold anything —
// including runs of spaces, which is what makes them worth keeping.
func (r *Repo) log(args ...string) ([]Commit, error) {
	const sep = "\x1f"
	out, err := r.run(append([]string{"log",
		"--format=%H" + sep + "%an" + sep + "%ar" + sep + "%s"}, args...)...)
	if err != nil {
		return nil, err
	}
	var commits []Commit
	for line := range strings.SplitSeq(strings.TrimRight(out, "\n"), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, sep, 4)
		if len(parts) != 4 || parts[0] == "" {
			continue
		}
		commits = append(commits, Commit{
			Hash: parts[0], Short: abbrev(parts[0]),
			Author: parts[1], When: parts[2], Subject: parts[3],
		})
	}
	return commits, nil
}

// CommitDiff is everything one commit changed, as one patch.
func (r *Repo) CommitDiff(hash string, context int) (string, error) {
	out, err := r.run("show", "--no-color", "--no-ext-diff", "--format=",
		fmt.Sprintf("-U%d", context), "-M", hash)
	if err != nil && strings.TrimSpace(out) == "" {
		return "", err
	}
	return out, nil
}

// CarryDiff is the diff of one path between a revision and the working tree,
// taken with no context at all because nothing but the line numbers is wanted.
//
// It is what carries a comment written against a commit onto the working tree
// and back again: see [Patch.MapOldToNew].
func (r *Repo) CarryDiff(rev, path string) (string, error) {
	return r.run("diff", "--no-color", "--no-ext-diff", "-U0", rev, "--", path)
}

// ---------------------------------------------------------------------------
// Reading files
// ---------------------------------------------------------------------------

// Edit opens a file from the working tree in the user's editor. It refuses a
// path that is not on disk — a file deleted but not yet committed, say — since
// opening that would create it rather than edit it.
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

// ReadInput reads a file somebody named on a command line — a path of their
// own, relative to where the command was run, not to the repository.
func (r *Repo) ReadInput(path string) ([]byte, error) {
	return r.sys.ReadFile(path)
}

// EditAt opens a file as it stands in a commit, or in the working tree when
// commit is empty, where it defers to Edit.
//
// History cannot be edited, but the editor is still the most familiar place to
// read a file in, so the commit's version is copied to a read-only temporary
// file and the editor opened on that. The returned path is that copy — empty
// for the working tree — and the caller hands it back to RemoveTempCopy once
// the editor exits.
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
	for l := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
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
	for l := range strings.SplitSeq(strings.TrimSpace(out), "\n") {
		if l != "" {
			names = append(names, l)
		}
	}
	return names, nil
}

// Checkout switches branches. It is the one thing discobox-review changes about a
// repository, because choosing what to review is not reviewing.
func (r *Repo) Checkout(branch string) (string, error) {
	return r.run("checkout", branch)
}

// UserName is git's own committer name, which is what `--by git-user` files a
// remark under: the name every other record of this work already carries.
func (r *Repo) UserName() string {
	out, err := r.run("config", "user.name")
	if err != nil {
		return "you"
	}
	if name := strings.TrimSpace(out); name != "" {
		return name
	}
	return "you"
}

// ---------------------------------------------------------------------------
// Files in the git directory
// ---------------------------------------------------------------------------

// StateDir is a directory of state files reached through a System: the git
// directory, for a Repo, or a directory of its own for a review kept away from
// any repository (see pkg/store). It is a [source.Store].
type StateDir struct {
	sys System
	dir string
}

// NewStateDir is the state files in dir, reached through sys. It creates
// nothing: dir has to be there before anything is written into it.
func NewStateDir(sys System, dir string) StateDir { return StateDir{sys: sys, dir: dir} }

var _ source.Store = StateDir{}

// ReadStateFile reads a state file.
func (d StateDir) ReadStateFile(name string) (string, bool) {
	b, err := d.sys.ReadFile(filepath.Join(d.dir, name))
	if err != nil {
		return "", false
	}
	return string(b), true
}

// WriteStateFile writes a state file, through a temporary file of its own so
// that a reader never catches a half written one. Reading takes no lock, so
// the write has to be the thing that is indivisible.
//
// The temporary file has one name, so two writers of the same file must not
// be at it together: see LockStateFile.
func (d StateDir) WriteStateFile(name, content string) error {
	tmp := filepath.Join(d.dir, name+".tmp")
	if err := d.sys.WriteFile(tmp, []byte(content)); err != nil {
		return err
	}
	return d.sys.Rename(tmp, filepath.Join(d.dir, name))
}

// LockStateFile takes the lock that a state file stands for, waiting up to
// wait for whoever holds it — see [System.Lock] — and returns what lets go of
// it. Whoever reads a state file in order to write it back holds one from the
// read to the write.
func (d StateDir) LockStateFile(name string, wait time.Duration) (func(), error) {
	return d.sys.Lock(filepath.Join(d.dir, name), wait)
}

// RemoveStateFile deletes a state file, ignoring a missing file.
func (d StateDir) RemoveStateFile(name string) error {
	return d.sys.Remove(filepath.Join(d.dir, name))
}

// RenameStateFile moves one state file over another.
func (d StateDir) RenameStateFile(from, to string) error {
	return d.sys.Rename(filepath.Join(d.dir, from), filepath.Join(d.dir, to))
}

// StateFileExists reports whether a state file is present.
func (d StateDir) StateFileExists(name string) bool {
	_, err := d.sys.Stat(filepath.Join(d.dir, name))
	return err == nil
}

// StateFileStamp reports a state file's stamp. A missing file has the zero
// stamp, which is a state like any other: it is how "the review has been
// deleted" arrives.
func (d StateDir) StateFileStamp(name string) Stamp {
	info, err := d.sys.Stat(filepath.Join(d.dir, name))
	if err != nil {
		return Stamp{}
	}
	return Stamp{ModTime: info.ModTime.UnixNano(), Size: info.Size}
}

// A Repo is every kind of source there is: it has a working tree, it keeps a
// review in its git directory, and it can carry a comment through a commit.
var (
	_ source.Source   = (*Repo)(nil)
	_ source.Carrier  = (*Repo)(nil)
	_ source.Store    = (*Repo)(nil)
	_ source.Worktree = (*Repo)(nil)
)
