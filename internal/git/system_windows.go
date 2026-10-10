//go:build windows

package git

import (
	"os"

	"golang.org/x/sys/windows"
)

// tryLockFile takes the exclusive lock on an open file if nobody has it, and
// reports whether it did. It is the half of [Local.Lock] that each kind of
// system spells its own way.
//
// The lock is on the file's first byte and belongs to the handle, so it goes
// when the file is closed and when the process holding it dies.
func tryLockFile(f *os.File) (bool, error) {
	err := windows.LockFileEx(windows.Handle(f.Fd()),
		windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY,
		0, 1, 0, new(windows.Overlapped))
	switch err {
	case nil:
		return true, nil
	case windows.ERROR_LOCK_VIOLATION, windows.ERROR_IO_PENDING:
		return false, nil
	}
	return false, err
}
