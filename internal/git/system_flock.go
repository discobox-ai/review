//go:build unix && !solaris && !aix

package git

import (
	"os"
	"syscall"
)

// tryLockFile takes the exclusive lock on an open file if nobody has it, and
// reports whether it did. It is the half of [Local.Lock] that each kind of
// system spells its own way.
//
// The lock is flock's: it belongs to the open file rather than to its name, so
// it goes when the file is closed and — which is the point of it — when the
// process holding it dies, however it dies.
func tryLockFile(f *os.File) (bool, error) {
	for {
		switch err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err {
		case nil:
			return true, nil
		case syscall.EWOULDBLOCK:
			return false, nil
		case syscall.EINTR:
		default:
			return false, err
		}
	}
}
