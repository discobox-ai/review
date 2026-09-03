package git

import (
	"bytes"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// System is every call discobox-review makes outside its own process: running git, and
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

	// The files discobox-review reads and writes directly. These are the ones git has
	// no command for: the merge and squash messages it leaves in the git
	// directory, the commit template, and discobox-review's own saved commit message.
	ReadFile(path string) ([]byte, error)
	WriteFile(path string, data []byte) error
	Remove(path string) error
	RemoveAll(path string) error
	Rename(oldPath, newPath string) error
	Stat(path string) (FileInfo, error)

	// WriteTempFile writes data to a fresh read-only file named name, in a
	// directory of its own outside the repository, and returns its path. It
	// carries a file as it stood in a commit out to the editor: the copy is
	// there to be looked at, so nothing that opens it can write into history.
	// The caller removes the copy's directory when it is done with it.
	WriteTempFile(name string, data []byte) (string, error)

	// Editor builds the command that opens path in the user's editor, run in
	// dir. It is the only thing discobox-review runs that is not git, and the only
	// one that takes the terminal over while it lasts.
	Editor(dir, path string) Editor

	// HomeDir expands a leading "~" in configuration such as commit.template.
	HomeDir() (string, error)

	// DevNull is the null device, which the diff of an untracked file is taken
	// against.
	DevNull() string
}

// Editor is a command that owns the terminal until it exits. The shape is what
// bubbletea's exec support wants, written out here so that nothing above this
// file has to know what a process is.
type Editor interface {
	Run() error
	SetStdin(io.Reader)
	SetStdout(io.Writer)
	SetStderr(io.Writer)
}

// FileInfo is the part of a stat discobox-review uses. It is deliberately small: a
// remote backend should not have to synthesise a full fs.FileInfo to answer
// "does this exist", "which of these two is newer", and "has this file changed
// since I last read it" — the last of which wants the size as well, since a
// filesystem's modification times can be too coarse to catch two writes in the
// same second.
type FileInfo struct {
	ModTime time.Time
	Size    int64
	IsDir   bool
}

// Local is the System that runs git as a child process and reads files from
// the machine discobox-review is running on. It is the default.
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

// WriteTempFile puts data in a read-only file of its own under the system
// temporary directory. Keeping the given name, rather than the mangled one
// os.CreateTemp would pick, is what lets an editor recognise the file type;
// the fresh directory is what keeps the name from colliding.
func (Local) WriteTempFile(name string, data []byte) (string, error) {
	dir, err := os.MkdirTemp("", "discobox-review-")
	if err != nil {
		return "", err
	}
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o444); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return path, nil
}

// Stat reports what discobox-review needs to know about a path.
func (Local) Stat(path string) (FileInfo, error) {
	info, err := os.Stat(path)
	if err != nil {
		return FileInfo{}, err
	}
	return FileInfo{ModTime: info.ModTime(), Size: info.Size(), IsDir: info.IsDir()}, nil
}

// defaultEditor is what to open a file with when the environment says nothing.
const defaultEditor = "vi"

// editorCmd adapts an exec.Cmd to the Editor interface, which differs from it
// only in wanting the three streams set through methods.
type editorCmd struct{ *exec.Cmd }

func (c editorCmd) SetStdin(r io.Reader)  { c.Stdin = r }
func (c editorCmd) SetStdout(w io.Writer) { c.Stdout = w }
func (c editorCmd) SetStderr(w io.Writer) { c.Stderr = w }

// Editor opens path in $EDITOR, or in vi when that is unset. EDITOR may carry
// arguments of its own — "code -w", "emacsclient -nw" — so it is split on
// whitespace rather than taken as a bare program name.
func (Local) Editor(dir, path string) Editor {
	fields := strings.Fields(os.Getenv("EDITOR"))
	if len(fields) == 0 {
		fields = []string{defaultEditor}
	}
	cmd := exec.Command(fields[0], append(fields[1:], path)...)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	return editorCmd{cmd}
}

// HomeDir is the current user's home directory.
func (Local) HomeDir() (string, error) { return os.UserHomeDir() }

// DevNull is the null device.
func (Local) DevNull() string { return os.DevNull }
