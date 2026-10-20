package store_test

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
	"time"

	"github.com/discobox-ai/review/pkg/source"
	"github.com/discobox-ai/review/pkg/store"
)

// Open makes the directory a review is kept in, however deep, and what is
// written there is read back whole, with no temporary file left beside it.
func TestOpenKeepsAReviewInADirectoryOfItsOwn(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "github.com", "owner", "name", "pulls", "7")
	kept, err := store.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	if kept.StateFileExists("review.json") || kept.StateFileStamp("review.json") != (source.Stamp{}) {
		t.Fatal("a fresh directory already has a review in it")
	}
	if err := kept.WriteStateFile("review.json", "{}\n"); err != nil {
		t.Fatal(err)
	}
	if got, ok := kept.ReadStateFile("review.json"); !ok || got != "{}\n" {
		t.Fatalf("read back %q, %v", got, ok)
	}
	if _, err := os.Stat(filepath.Join(dir, "review.json.tmp")); err == nil {
		t.Error("the write left its temporary file behind")
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if perm := info.Mode().Perm(); runtime.GOOS != "windows" && perm&0o077 != 0 {
		t.Errorf("the directory is open to others: %v", perm)
	}
}

// Its lock is the lock everybody writing the review takes: held, a second
// taker waits and then gives up.
func TestOpenLocksTheReview(t *testing.T) {
	kept, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	unlock, err := kept.LockStateFile("review.json.lock", 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := kept.LockStateFile("review.json.lock", 20*time.Millisecond); err == nil {
		t.Error("the lock was taken twice")
	}
	unlock()
	again, err := kept.LockStateFile("review.json.lock", 0)
	if err != nil {
		t.Fatalf("let go of, the lock cannot be taken again: %v", err)
	}
	again()
}
