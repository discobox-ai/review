//go:build windows

package git

import (
	"io"
	"testing"
)

// A reader that has the file open when a write lands keeps what it opened,
// and the write is not refused: the race TestAReaderDoesNotRefuseAWrite hunts
// for, held still.
func TestAWriteReplacesAFileHeldOpenByAReader(t *testing.T) {
	d := NewStateDir(Local{}, t.TempDir())
	if err := d.WriteStateFile("review.json", "old\n"); err != nil {
		t.Fatal(err)
	}
	f, err := openShared(d.dir + `\review.json`)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	if err := d.WriteStateFile("review.json", "new\n"); err != nil {
		t.Fatalf("write refused while the file is held open: %v", err)
	}
	if got, ok := d.ReadStateFile("review.json"); !ok || got != "new\n" {
		t.Errorf("read back %q (present=%v), want the new file", got, ok)
	}
	if b, err := io.ReadAll(f); err != nil || string(b) != "old\n" {
		t.Errorf("the reader read %q (%v), want the file it opened", b, err)
	}
}
