package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// These run the built program rather than calling into it, because what they
// are about is the decision main makes before anything else: whether there is
// a terminal to draw in. Nothing below main can be asked that.

var binary string

func TestMain(m *testing.M) {
	dir, err := os.MkdirTemp("", "difftui-main")
	if err != nil {
		panic(err)
	}
	binary = filepath.Join(dir, "difftui")
	out, err := exec.Command("go", "build", "-o", binary, ".").CombinedOutput()
	if err != nil {
		panic(string(out))
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

// repoDir is a throwaway repository with a change on a branch.
func repoDir(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.name", "Test"},
		{"config", "user.email", "test@example.com"},
		{"config", "commit.gpgsign", "false"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "a.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	for _, args := range [][]string{{"add", "."}, {"commit", "-qm", "initial"}} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	return dir
}

// difftui runs the program with its output captured, which is to say with no
// terminal anywhere near it — the way anything but a person runs it.
func difftui(t *testing.T, dir string, args ...string) (stdout, stderr string, code int) {
	t.Helper()
	cmd := exec.Command(binary, args...)
	cmd.Dir = dir
	var out, errs strings.Builder
	cmd.Stdout, cmd.Stderr = &out, &errs
	err := cmd.Run()
	if ee, ok := err.(*exec.ExitError); ok {
		code = ee.ExitCode()
	} else if err != nil {
		t.Fatalf("running difftui %v: %v", args, err)
	}
	return out.String(), errs.String(), code
}

// With nowhere to draw, difftui says what it can do without a terminal rather
// than putting a screenful of escape sequences down a pipe and then waiting
// for keys that are never coming.
func TestNoTerminalPrintsWhatWorksWithoutOne(t *testing.T) {
	stdout, stderr, code := difftui(t, repoDir(t))

	if code != 0 {
		t.Errorf("exit %d, want 0: this is an answer, not a refusal", code)
	}
	for _, want := range []string{"difftui diff", "difftui comment", "difftui list"} {
		if !strings.Contains(stdout, want) {
			t.Errorf("stdout does not offer %q:\n%s", want, stdout)
		}
	}
	// The usable half goes to stdout on its own, so it can be read by whatever
	// asked; the explanation goes the other way.
	if !strings.Contains(stderr, "no terminal") {
		t.Errorf("stderr = %q, want it to say why", stderr)
	}
	if strings.Contains(stdout, "no terminal") {
		t.Errorf("the note is mixed into stdout:\n%s", stdout)
	}
	// And nothing was drawn: no alternate screen, no mouse, no cursor moves.
	for _, seq := range []string{"\x1b[?1049h", "\x1b[?1002h", "\x1b[2J"} {
		if strings.Contains(stdout, seq) || strings.Contains(stderr, seq) {
			t.Errorf("it drew %q at something that cannot show it", seq)
		}
	}
}

// The subcommands are the point of having a fallback at all, so they have to
// work in the same conditions.
func TestSubcommandsWorkWithNoTerminal(t *testing.T) {
	dir := repoDir(t)

	if _, _, code := difftui(t, dir, "base", "main"); code != 0 {
		t.Fatalf("base exited %d", code)
	}
	if out, _, code := difftui(t, dir, "comment", "a.txt:1", "why one?"); code != 0 ||
		!strings.HasPrefix(out, "t1") {
		t.Fatalf("comment exited %d printing %q", code, out)
	}
	out, _, code := difftui(t, dir, "list")
	if code != 0 || !strings.Contains(out, "why one?") {
		t.Errorf("list exited %d printing %q", code, out)
	}

	// And a bad one still fails, rather than falling back to the usage and
	// looking like it worked.
	if _, stderr, code := difftui(t, dir, "show", "nope"); code == 0 {
		t.Errorf("a bad command exited 0, saying %q", stderr)
	}
}

// -version needs no terminal either, and must not be answered with the usage.
func TestVersionWithNoTerminal(t *testing.T) {
	stdout, _, code := difftui(t, repoDir(t), "-version")
	if code != 0 || !strings.HasPrefix(stdout, "difftui ") {
		t.Errorf("-version exited %d printing %q", code, stdout)
	}
	if strings.Contains(stdout, "difftui diff") {
		t.Errorf("-version was answered with the usage:\n%s", stdout)
	}
}
