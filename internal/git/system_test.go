package git

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// forbidden are the standard library calls that reach outside the process.
// They belong in the System implementation and nowhere else, so that a
// different backend has one file to write rather than a codebase to audit.
var forbidden = map[string]map[string]bool{
	"os": {
		"ReadFile": true, "WriteFile": true, "ReadDir": true,
		"Open": true, "OpenFile": true, "Create": true, "CreateTemp": true,
		"Stat": true, "Lstat": true,
		"Remove": true, "RemoveAll": true, "Rename": true, "Link": true, "Symlink": true,
		"Mkdir": true, "MkdirAll": true, "MkdirTemp": true, "Chdir": true,
		"UserHomeDir": true, "UserCacheDir": true, "UserConfigDir": true,
		"DevNull": true, "Environ": true, "Getenv": true,
	},
}

// forbiddenImports may only appear in the System implementation.
var forbiddenImports = map[string]bool{
	"os/exec":   true,
	"net":       true,
	"net/http":  true,
	"io/ioutil": true,
	"os/user":   true,
}

// systemFile is the one place allowed to make those calls.
const systemFile = "system.go"

// TestSystemIsTheOnlyBoundary walks the whole module and fails if anything
// outside the System implementation runs a command or touches a file. It is
// the guard that keeps a remote backend to a single new implementation of
// System, rather than a hunt through the source for stray calls.
func TestSystemIsTheOnlyBoundary(t *testing.T) {
	root, err := filepath.Abs("../..")
	if err != nil {
		t.Fatal(err)
	}

	var offences []string
	err = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			// Skip anything that is not ours.
			if name := d.Name(); name == ".git" || name == "vendor" || strings.HasPrefix(name, ".") && name != "." {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		rel, _ := filepath.Rel(root, path)
		if filepath.Base(path) == systemFile {
			return nil // the boundary itself
		}

		fset := token.NewFileSet()
		file, err := parser.ParseFile(fset, path, nil, 0)
		if err != nil {
			return err
		}

		// Imports.
		aliases := map[string]string{} // local name -> import path
		for _, imp := range file.Imports {
			p := strings.Trim(imp.Path.Value, `"`)
			if forbiddenImports[p] {
				offences = append(offences, rel+": imports "+p)
			}
			name := filepath.Base(p)
			if imp.Name != nil {
				name = imp.Name.Name
			}
			aliases[name] = p
		}

		// Calls.
		ast.Inspect(file, func(n ast.Node) bool {
			sel, ok := n.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			ident, ok := sel.X.(*ast.Ident)
			if !ok {
				return true
			}
			pkg, ok := aliases[ident.Name]
			if !ok {
				return true
			}
			if banned, ok := forbidden[pkg]; ok && banned[sel.Sel.Name] {
				pos := fset.Position(sel.Pos())
				offences = append(offences,
					rel+":"+itoaTest(pos.Line)+": calls "+pkg+"."+sel.Sel.Name)
			}
			return true
		})
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(offences) > 0 {
		t.Errorf("external calls outside %s — move them behind the System interface:\n  %s",
			systemFile, strings.Join(offences, "\n  "))
	}
}

func itoaTest(i int) string {
	if i == 0 {
		return "0"
	}
	var b []byte
	for i > 0 {
		b = append([]byte{byte('0' + i%10)}, b...)
		i /= 10
	}
	return string(b)
}

// fakeSystem serves canned answers and records what was asked of it. It exists
// to prove the interface is sufficient: a Repo driven through it never touches
// the real machine.
type fakeSystem struct {
	git   map[string]string // joined args -> stdout
	files map[string]string
	times map[string]time.Time
	dirs  map[string]bool

	gitCalls  []string
	fileCalls []string
}

func (f *fakeSystem) Git(dir string, stdin []byte, args ...string) (string, error) {
	key := strings.Join(args, " ")
	f.gitCalls = append(f.gitCalls, key)
	if out, ok := f.git[key]; ok {
		return out, nil
	}
	return "", errors.New("fakeSystem: unexpected git " + key)
}

func (f *fakeSystem) ReadFile(path string) ([]byte, error) {
	f.fileCalls = append(f.fileCalls, "read "+path)
	if s, ok := f.files[path]; ok {
		return []byte(s), nil
	}
	return nil, fs.ErrNotExist
}

func (f *fakeSystem) WriteFile(path string, data []byte) error {
	f.fileCalls = append(f.fileCalls, "write "+path)
	if f.files == nil {
		f.files = map[string]string{}
	}
	f.files[path] = string(data)
	return nil
}

func (f *fakeSystem) Remove(path string) error {
	f.fileCalls = append(f.fileCalls, "remove "+path)
	delete(f.files, path)
	return nil
}

func (f *fakeSystem) RemoveAll(path string) error {
	f.fileCalls = append(f.fileCalls, "removeall "+path)
	return nil
}

func (f *fakeSystem) Rename(oldPath, newPath string) error {
	f.fileCalls = append(f.fileCalls, "rename "+oldPath+" "+newPath)
	if s, ok := f.files[oldPath]; ok {
		f.files[newPath] = s
		delete(f.files, oldPath)
	}
	return nil
}

func (f *fakeSystem) Stat(path string) (FileInfo, error) {
	f.fileCalls = append(f.fileCalls, "stat "+path)
	if _, ok := f.files[path]; !ok && !f.dirs[path] {
		return FileInfo{}, fs.ErrNotExist
	}
	return FileInfo{ModTime: f.times[path], IsDir: f.dirs[path]}, nil
}

// fakeEditor stands in for a program that takes the terminal: it records that
// it was asked for and returns without doing anything.
type fakeEditor struct{ path string }

func (fakeEditor) Run() error          { return nil }
func (fakeEditor) SetStdin(io.Reader)  {}
func (fakeEditor) SetStdout(io.Writer) {}
func (fakeEditor) SetStderr(io.Writer) {}

func (f *fakeSystem) Editor(dir, path string) Editor {
	f.fileCalls = append(f.fileCalls, "edit "+path)
	return fakeEditor{path: path}
}

func (f *fakeSystem) HomeDir() (string, error) { return "/home/fake", nil }
func (f *fakeSystem) DevNull() string          { return "/dev/null" }

// A repository driven entirely through a fake System behaves normally, which
// is what makes a remote backend a matter of implementing this one interface.
func TestRepoRunsOnAFakeSystem(t *testing.T) {
	fake := &fakeSystem{
		git: map[string]string{
			"rev-parse --show-toplevel":                                "/work/repo\n",
			"rev-parse --git-dir":                                      "/work/repo/.git\n",
			"rev-parse --verify HEAD":                                  "abc123\n",
			"diff-index --cached -M --ignore-submodules=dirty -z HEAD": ":100644 100644 aaa bbb M\x00staged.txt\x00",
			"diff-files --ignore-submodules=dirty -z":                  ":100644 100644 aaa bbb M\x00worktree.txt\x00",
			"ls-files --others --exclude-standard -z":                  "brand-new.txt\x00",
			"ls-files --unmerged -z":                                   "",
			"symbolic-ref --short HEAD":                                "main\n",
		},
		files: map[string]string{"/work/repo/.git/GITGUI_MSG": "a remembered message"},
	}

	repo, err := OpenWith(fake, "/work/repo")
	if err != nil {
		t.Fatal(err)
	}
	if repo.Dir != "/work/repo" || repo.GitDir != "/work/repo/.git" {
		t.Fatalf("repo = %+v", repo)
	}

	files, err := repo.Status("HEAD")
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]State{}
	for _, f := range files {
		got[f.Path] = f.State
	}
	for path, want := range map[string]State{
		"staged.txt":    "M_",
		"worktree.txt":  "_M",
		"brand-new.txt": "_O",
	} {
		if got[path] != want {
			t.Errorf("%s = %q, want %q", path, got[path], want)
		}
	}

	if repo.CurrentBranch() != "main" {
		t.Errorf("branch = %q", repo.CurrentBranch())
	}
	if msg, ok := repo.ReadStateFile(MsgFile); !ok || msg != "a remembered message" {
		t.Errorf("saved message = %q (present=%v)", msg, ok)
	}

	// And it really did go through the fake rather than the machine.
	if len(fake.gitCalls) == 0 {
		t.Error("no git ran through the System")
	}
	if len(fake.fileCalls) == 0 {
		t.Error("no file access went through the System")
	}
}
