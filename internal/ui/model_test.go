package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/obot-platform/difftui/internal/git"
)

func testRepo(t *testing.T) *git.Repo {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{
		{"init", "-q", "-b", "main"},
		{"config", "user.name", "Test"},
		{"config", "user.email", "test@example.com"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = dir
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	writeFile(t, dir, "foo.txt", "one\ntwo\nthree\nfour\nfive\n")
	writeFile(t, dir, "bar.txt", "alpha\n")
	gitRun(t, dir, "add", ".")
	gitRun(t, dir, "commit", "-q", "-m", "initial")

	// One modified file, one staged file and one untracked file so every
	// pane has something in it.
	writeFile(t, dir, "foo.txt", "one\nTWO\nthree\nfour\nfive\n")
	writeFile(t, dir, "bar.txt", "beta\n")
	gitRun(t, dir, "add", "bar.txt")
	writeFile(t, dir, "new.txt", "brand new\n")

	r, err := git.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return r
}

func writeFile(t *testing.T, dir, name, content string) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func gitRun(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Dir = dir
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v: %v: %s", args, err, out)
	}
}

// drive feeds a message to the model and runs whatever command comes back,
// looping until there is nothing left to do. It is a miniature bubbletea
// runtime, enough to test the model without a terminal.
func drive(t *testing.T, m Model, msgs ...tea.Msg) Model {
	t.Helper()
	queue := append([]tea.Msg{}, msgs...)
	var cmds []tea.Cmd
	for i := 0; i < 200 && (len(queue) > 0 || len(cmds) > 0); i++ {
		if len(queue) == 0 {
			cmd := cmds[0]
			cmds = cmds[1:]
			if out := cmd(); out != nil {
				queue = append(queue, out)
			}
			continue
		}
		msg := queue[0]
		queue = queue[1:]
		// A batch is a bundle of commands rather than a message to handle.
		if batch, ok := msg.(tea.BatchMsg); ok {
			cmds = append(cmds, batch...)
			continue
		}
		next, cmd := m.Update(msg)
		m = next.(Model)
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
	}
	return m
}

func TestViewRenders(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)

	view := m.View()
	if strings.TrimSpace(view) == "" {
		t.Fatal("view is empty")
	}
	for _, want := range []string{
		"Current Branch:", "main",
		"Unstaged Changes", "Staged Changes (Will Commit)",
		"Commit Message:", "Amend Last Commit",
		"Rescan", "Stage Changed", "Sign Off", "Commit", "Push",
		"foo.txt", "bar.txt", "new.txt",
	} {
		if !strings.Contains(view, want) {
			t.Errorf("view is missing %q", want)
		}
	}

	lines := strings.Split(view, "\n")
	if len(lines) != 40 {
		t.Errorf("view has %d lines, want 40", len(lines))
	}
	if t.Failed() || testing.Verbose() {
		t.Logf("\n%s", view)
	}
}

func TestFileListRouting(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)

	unstaged := map[string]bool{}
	for _, f := range m.workdir.files {
		unstaged[f.Path] = true
	}
	staged := map[string]bool{}
	for _, f := range m.index.files {
		staged[f.Path] = true
	}
	if !unstaged["foo.txt"] || !unstaged["new.txt"] {
		t.Errorf("unstaged list = %v", unstaged)
	}
	if unstaged["bar.txt"] {
		t.Error("a fully staged file must not be in the unstaged list")
	}
	if !staged["bar.txt"] || len(staged) != 1 {
		t.Errorf("staged list = %v", staged)
	}
}

func TestStageThroughUI(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)

	// Focus the unstaged list and stage the file under the cursor.
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1"), Alt: true})
	if m.focus != focusWorkdir {
		t.Fatalf("focus = %v, want focusWorkdir", m.focus)
	}
	target := m.workdir.current()
	if target == nil {
		t.Fatal("no file under the cursor")
	}
	name := target.Path

	m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlT})
	m = drive(t, m, New(repo).rescanCmd()())

	for _, f := range m.index.files {
		if f.Path == name {
			return
		}
	}
	t.Errorf("%s was not staged; staged list = %v", name, m.index.files)
}

func TestSmallTerminalDoesNotPanic(t *testing.T) {
	repo := testRepo(t)
	for _, size := range []tea.WindowSizeMsg{
		{Width: 20, Height: 8},
		{Width: 40, Height: 10},
		{Width: 200, Height: 60},
		{Width: 60, Height: 15},
	} {
		m := drive(t, New(repo), size, New(repo).rescanCmd()())
		view := m.View()
		if strings.TrimSpace(view) == "" {
			t.Errorf("empty view at %dx%d", size.Width, size.Height)
		}
	}
}

func TestSignOffAppendsTrailer(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m.commit.SetValue("do a thing")
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlS})

	got := m.commit.Value()
	want := "do a thing\n\nSigned-off-by: Test <test@example.com>"
	if got != want {
		t.Errorf("commit buffer =\n%q\nwant\n%q", got, want)
	}

	// A second sign off must not duplicate the trailer.
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlS})
	if m.commit.Value() != want {
		t.Errorf("sign off was applied twice:\n%q", m.commit.Value())
	}
}

func TestSignOffJoinsExistingTrailerBlock(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m.commit.SetValue("subject\n\nCo-authored-by: Someone <a@b.c>")
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlS})

	want := "subject\n\nCo-authored-by: Someone <a@b.c>\nSigned-off-by: Test <test@example.com>"
	if m.commit.Value() != want {
		t.Errorf("commit buffer =\n%q\nwant\n%q", m.commit.Value(), want)
	}
}

func TestCommitWithEmptyMessageIsRefused(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m.commit.SetValue("   ")
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("c"), Alt: true})
	if m.dialog == nil {
		t.Fatal("expected a dialog complaining about the empty message")
	}
	if !strings.Contains(m.dialog.body, "commit message") {
		t.Errorf("dialog body = %q", m.dialog.body)
	}
}

func TestContextKeysChangeDiffContext(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1"), Alt: true})
	start := m.context

	m = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("=")})
	if m.context != start+1 {
		t.Errorf("context = %d, want %d", m.context, start+1)
	}
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("-")})
	if m.context != start {
		t.Errorf("context = %d, want %d", m.context, start)
	}
}

// Typing in the commit buffer must not trigger the single letter accelerators.
func TestCommitBufferSwallowsLetterKeys(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4"), Alt: true})
	if m.focus != focusCommit {
		t.Fatalf("focus = %v, want focusCommit", m.focus)
	}
	for _, r := range "i M m v - = ?" {
		m = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if m.dialog != nil {
		t.Errorf("typing in the message buffer opened a dialog: %q", m.dialog.title)
	}
	if got := m.commit.Value(); got != "i M m v - = ?" {
		t.Errorf("commit buffer = %q, want the typed text", got)
	}
	if m.context != defaultContext {
		t.Errorf("typing changed the diff context to %d", m.context)
	}
}

func TestDiffPaneLoadsForSelectedFile(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1"), Alt: true})
	if m.diff.file == nil {
		t.Fatal("diff pane has no file loaded")
	}
	if m.diff.empty() {
		t.Fatal("diff pane is empty")
	}
	view := m.View()
	if !strings.Contains(view, m.diff.file.Path) {
		t.Errorf("diff header does not name the file %q", m.diff.file.Path)
	}
}

func TestQuitKeys(t *testing.T) {
	repo := testRepo(t)
	base := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)

	// Ctrl-Q and Ctrl-C quit from anywhere, including the message buffer.
	for _, k := range []tea.KeyMsg{{Type: tea.KeyCtrlQ}, {Type: tea.KeyCtrlC}} {
		if m := drive(t, base, k); !m.quit {
			t.Errorf("%v did not quit", k)
		}
	}

	// A bare q quits from a list, but must be typable in the message buffer.
	fromList := drive(t, base, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1"), Alt: true})
	if m := drive(t, fromList, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")}); !m.quit {
		t.Error("q did not quit from the unstaged list")
	}
	inCommit := drive(t, base, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("4"), Alt: true})
	inCommit = drive(t, inCommit, tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("q")})
	if inCommit.quit {
		t.Error("q must not quit while the commit message has focus")
	}
	if inCommit.commit.Value() != "q" {
		t.Errorf("commit buffer = %q, want \"q\"", inCommit.commit.Value())
	}
}
