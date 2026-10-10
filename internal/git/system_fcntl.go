//go:build solaris || aix

package git

import (
	"os"
	"syscall"
)

// tryLockFile takes the exclusive lock on an open file if nobody has it, and
// reports whether it did. It is the half of [Local.Lock] that each kind of
// system spells its own way.
//
// These systems have no flock, so the lock is fcntl's, on the whole file. It
// belongs to the process, and goes when the file is closed or the process
// dies.
func tryLockFile(f *os.File) (bool, error) {
	lock := syscall.Flock_t{Type: syscall.F_WRLCK, Whence: 0}
	for {
		switch err := syscall.FcntlFlock(f.Fd(), syscall.F_SETLK, &lock); err {
		case nil:
			return true, nil
		case syscall.EAGAIN, syscall.EACCES:
			return false, nil
		case syscall.EINTR:
		default:
			return false, err
		}
	}
}
