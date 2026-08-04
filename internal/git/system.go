package git

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"strings"
	"time"
)

// System is every call difftui makes outside its own process: running git, and
// the handful of files it touches directly rather than through git.
//
// Everything else in this package is built on it, so a different backend — a
// repository on another machine, a test double — is a matter of implementing
// this interface and passing it to OpenWith. Nothing outside this file runs a
// command or touches a file.
type System interface {
	// Git runs git in dir and returns its standard output. stdin is fed to the
	// command when it is not nil. On failure the error carries git's standard
	// error, and the output collected so far is still returned.
	Git(dir string, stdin []byte, args ...string) (string, error)

	// The files difftui reads and writes directly. These are the ones git has
	// no command for: the merge and squash messages it leaves in the git
	// directory, the commit template, and difftui's own saved commit message.
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte) error
	Remove(path string) error
	RemoveAll(path string) error
	Rename(oldPath, newPath string) error
	Stat(path string) (FileInfo, error)

	// HomeDir expands a leading "~" in configuration such as commit.template.
	HomeDir() (string, error)

	// DevNull is the null device, which the diff of an untracked file is taken
	// against.
	DevNull() string
}

// FileInfo is the part of a stat difftui uses. It is deliberately small: a
// remote backend should not have to synthesise a full fs.FileInfo to answer
// "does this exist, and which of these two is newer".
type FileInfo struct {
	ModTime time.Time
	IsDir   bool
}

// Local is the System that runs git as a child process and reads files from
// the machine difftui is running on. It is the default.
type Local struct{}

var _ System = Local{}

// Git runs git in dir.
func (Local) Git(dir string, stdin []byte, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if stdin != nil {
		cmd.Stdin = bytes.NewReader(stdin)
	}
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	// A pager would block forever, and a localised git would break the output
	// parsing.
	cmd.Env = append(os.Environ(), "GIT_PAGER=cat", "LC_ALL=C")

	if err := cmd.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if msg == "" {
			msg = err.Error()
		}
		return stdout.String(), errors.New(msg)
	}
	return stdout.String(), nil
}

// ReadFile reads a file.
func (Local) ReadFile(path string) ([]byte, error) { return os.ReadFile(path) }

// WriteFile writes a file, creating it if needed.
func (Local) WriteFile(path string, data []byte) error {
	return os.WriteFile(path, data, 0o644)
}

// Remove deletes a file, and is not an error when it is already gone.
func (Local) Remove(path string) error {
	err := os.Remove(path)
	if err != nil && os.IsNotExist(err) {
		return nil
	}
	return err
}

// RemoveAll deletes a path and anything under it.
func (Local) RemoveAll(path string) error { return os.RemoveAll(path) }

// Rename moves a file over another.
func (Local) Rename(oldPath, newPath string) error { return os.Rename(oldPath, newPath) }

// Stat reports what difftui needs to know about a path.
func (Local) Stat(path string) (FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{ModTime: info.ModTime(), IsDir: info.IsDir()}, nil
}

// HomeDir is the current user's home directory.
func (Local) HomeDir() (string, error) { return os.UserHomeDir() }

// DevNull is the null device.
func (Local) DevNull() string { return os.DevNull }
