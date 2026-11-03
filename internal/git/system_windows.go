//go:build windows

package git

import (
	"io"
	"os"
	"path/filepath"
	"strings"
	"unsafe"

	"golang.org/x/sys/windows"
)

// readFile reads a file. It is the half of [Local.ReadFile] that each kind of
// system spells its own way.
//
// The standard read opens without FILE_SHARE_DELETE, and while a file is open
// that way a rename over it is refused: a lock-free reader would make a
// writer's replace fail, and its write would be lost. Sharing delete lets the
// rename through (see rename) while the reader keeps the contents it opened.
func readFile(path string) ([]byte, error) {
	f, err := openShared(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return io.ReadAll(f)
}

// openShared opens a file to read, sharing read, write and delete with
// whoever else has it.
func openShared(path string) (*os.File, error) {
	name, err := windows.UTF16PtrFromString(longPath(path))
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	h, err := windows.CreateFile(name, windows.GENERIC_READ,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		return nil, &os.PathError{Op: "open", Path: path, Err: err}
	}
	return os.NewFile(uintptr(h), path), nil
}

// rename moves a file over another. It is the half of [Local.Rename] that
// each kind of system spells its own way.
//
// The standard rename, MoveFileEx, refuses to replace a file somebody has
// open, however that somebody shares it. A rename with POSIX semantics
// replaces it, and whoever had the old file open keeps reading it. A file
// system without POSIX semantics (FAT, an old Windows) gets the standard
// rename.
func rename(oldPath, newPath string) error {
	if renamePOSIX(oldPath, newPath) == nil {
		return nil
	}
	return os.Rename(oldPath, newPath)
}

// fileRenameInfo is FILE_RENAME_INFO, with FileName running on past the
// struct to the end of the buffer it is laid over.
type fileRenameInfo struct {
	Flags          uint32
	RootDirectory  windows.Handle
	FileNameLength uint32
	FileName       [1]uint16
}

// renamePOSIX renames oldPath over newPath with POSIX semantics.
func renamePOSIX(oldPath, newPath string) error {
	from, err := windows.UTF16PtrFromString(longPath(oldPath))
	if err != nil {
		return err
	}
	to, err := windows.UTF16FromString(longPath(newPath))
	if err != nil {
		return err
	}
	h, err := windows.CreateFile(from, windows.DELETE|windows.SYNCHRONIZE,
		windows.FILE_SHARE_READ|windows.FILE_SHARE_WRITE|windows.FILE_SHARE_DELETE,
		nil, windows.OPEN_EXISTING, windows.FILE_FLAG_OPEN_REPARSE_POINT, 0)
	if err != nil {
		return err
	}
	defer windows.CloseHandle(h)

	offset := unsafe.Offsetof(fileRenameInfo{}.FileName)
	size := max(offset+uintptr(len(to))*2, unsafe.Sizeof(fileRenameInfo{}))
	buf := make([]uint64, (size+7)/8) // 8-byte aligned, as the struct wants
	info := (*fileRenameInfo)(unsafe.Pointer(&buf[0]))
	info.Flags = windows.FILE_RENAME_REPLACE_IF_EXISTS | windows.FILE_RENAME_POSIX_SEMANTICS
	info.FileNameLength = uint32((len(to) - 1) * 2) // in bytes, without the NUL
	copy(unsafe.Slice(&info.FileName[0], len(to)), to)
	return windows.SetFileInformationByHandle(h, windows.FileRenameInfoEx,
		(*byte)(unsafe.Pointer(info)), uint32(size))
}

// maxShortPath is the length from which os, too, makes a path extended: a
// directory's path has to leave room under MAX_PATH for an 8.3 file name.
const maxShortPath = 248

// longPath turns a path too long for MAX_PATH into an extended-length one,
// which is not held to it. The os package does as much for its own calls; the
// calls here go to the system directly, and a path it could read must not read
// here as no file at all.
//
// A path is measured as the system resolves it, so a short relative one under
// a deep working directory counts as long. One short enough is left as it is,
// as os leaves it: the extended form takes a name literally, so a device such
// as NUL would stop being one.
func longPath(path string) string {
	if strings.HasPrefix(path, `\\?\`) || strings.HasPrefix(path, `\\.\`) {
		return path
	}
	abs, err := filepath.Abs(path)
	if err != nil || len(abs) < maxShortPath {
		return path
	}
	if strings.HasPrefix(abs, `\\`) {
		return `\\?\UNC\` + abs[2:]
	}
	return `\\?\` + abs
}

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
