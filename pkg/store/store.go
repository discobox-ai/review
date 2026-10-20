// Package store keeps a review in a directory of its own.
//
// The command keeps a branch's review in its git directory. A source with no
// git directory — a pull request read from a hosting service — still has a
// review worth keeping, and this is where a program embedding the window keeps
// it: the same file, written the same way, under the same lock, in whatever
// directory the program gives it.
package store

import (
	"github.com/discobox-ai/review/internal/git"
	"github.com/discobox-ai/review/pkg/source"
)

// Open is the review kept in dir, which is made if it is not there yet. It is
// the file the command keeps in the git directory, under the same name, so
// dir is the review of one change: a pull request wants a directory of its
// own, and keeps it from one head to the next.
func Open(dir string) (source.Store, error) {
	sys := git.Local{}
	if err := sys.MkdirAll(dir); err != nil {
		return nil, err
	}
	return git.NewStateDir(sys, dir), nil
}
