package git

import (
	"fmt"
	"strings"

	"github.com/discobox-ai/review/pkg/source"
)

// Snapshot is a repository's change up to one commit rather than up to its
// working tree: what a pull request's head is, read where its commits are.
//
// The review over it is the repository's own, kept under the branch checked
// out, and the Repo is the store handed to the window beside it: what an agent
// wrote there while it worked on the branch, its card among it, is what the
// window shows over the commit it pushed. Nothing it reads is uncommitted, and it is no
// [source.Worktree]: a commit has no branch to change and no file to edit, so
// the window offers neither.
type Snapshot struct {
	repo *Repo
	// head is the commit the change runs to, in full.
	head string
}

var (
	_ source.Source  = (*Snapshot)(nil)
	_ source.Carrier = (*Snapshot)(nil)
)

// Snapshot is the change up to the commit rev names, which the repository has
// to have.
func (r *Repo) Snapshot(rev string) (*Snapshot, error) {
	head, ok := r.CommitOf(rev)
	if !ok {
		return nil, fmt.Errorf("no such commit: %s", rev)
	}
	return &Snapshot{repo: r, head: head}, nil
}

// Scan reads the change from the base to the commit. The branch is the one
// checked out, since that is whose review this is.
func (s *Snapshot) Scan(baseRef string, context int) (Scan, error) {
	r := s.repo
	scan := Scan{Branch: r.CurrentBranch(), HasHEAD: true}
	scan.Base = r.resolveBase(baseRef, s.head)
	if scan.Base.SHA == "" {
		return scan, fmt.Errorf("cannot measure %s from anything", abbrev(s.head))
	}
	scan.Commits, _ = r.log(scan.Base.SHA + ".." + s.head)
	out, err := r.run("diff", "--no-color", "--no-ext-diff",
		fmt.Sprintf("-U%d", context), "-M", scan.Base.SHA, s.head)
	if err != nil && strings.TrimSpace(out) == "" {
		return scan, err
	}
	scan.Diff = out
	return scan, nil
}

// CommitDiff is everything one commit changed, as the repository has it.
func (s *Snapshot) CommitDiff(hash string, context int) (string, error) {
	return s.repo.CommitDiff(hash, context)
}

// FileContent is a file as commit recorded it, or as the snapshot's own
// commit does when commit is empty.
func (s *Snapshot) FileContent(commit, path string) (string, error) {
	if commit == "" {
		commit = s.head
	}
	return s.repo.FileContent(commit, path)
}

// CarryDiff is the diff of one path from rev to the snapshot's commit, with no
// context: see [Repo.CarryDiff].
func (s *Snapshot) CarryDiff(rev, path string) (string, error) {
	return s.repo.run("diff", "--no-color", "--no-ext-diff", "-U0", rev, s.head, "--", path)
}
