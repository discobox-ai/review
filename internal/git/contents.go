package git

import (
	"fmt"
	"reflect"

	"github.com/discobox-ai/review/pkg/source"
)

// ReadContents reads a stable worktree pair. Each pass includes the manifest,
// modes and clean-filtered bytes; comparing whole passes catches edits that
// neither HEAD nor file timestamps alone can detect. Immutable objects are
// written while applying clean filters, but no index or worktree is changed.
func (r *Repo) ReadContents(scan Scan) (source.Contents, error) {
	for range 3 {
		before, err := r.contents(scan.Base.SHA, "")
		if err != nil {
			return source.Contents{}, err
		}
		after, err := r.contents(scan.Base.SHA, "")
		if err != nil {
			return source.Contents{}, err
		}
		head, _ := r.CommitOf("HEAD")
		if head != scan.Revision || r.CurrentBranch() != scan.Branch {
			return source.Contents{}, fmt.Errorf("source moved since scan; scan again")
		}
		if reflect.DeepEqual(before, after) {
			return after, nil
		}
	}
	return source.Contents{}, fmt.Errorf("source moved while reading contents; scan again")
}

// ReadContents reads immutable repository content at the snapshot's revision.
func (s *Snapshot) ReadContents(scan Scan) (source.Contents, error) {
	if scan.Revision != s.head {
		return source.Contents{}, fmt.Errorf("scan belongs to another revision")
	}
	if s.repo.CurrentBranch() != scan.Branch {
		return source.Contents{}, fmt.Errorf("source branch moved since scan; scan again")
	}
	contents, err := s.repo.contents(scan.Base.SHA, s.head)
	if err == nil && s.repo.CurrentBranch() != scan.Branch {
		return source.Contents{}, fmt.Errorf("source branch moved while reading contents; scan again")
	}
	return contents, err
}
