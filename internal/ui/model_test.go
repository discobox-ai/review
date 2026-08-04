package ui

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

	m = drive(t, m, rkey("s"))
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
	m = drive(t, m, rkey("o"))

	got := m.commit.Value()
	want := "do a thing\n\nSigned-off-by: Test <test@example.com>"
	if got != want {
		t.Errorf("commit buffer =\n%q\nwant\n%q", got, want)
	}

	// A second sign off must not duplicate the trailer.
	m = drive(t, m, rkey("o"))
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
	m = drive(t, m, rkey("o"))

	want := "subject\n\nCo-authored-by: Someone <a@b.c>\nSigned-off-by: Test <test@example.com>"
	if m.commit.Value() != want {
		t.Errorf("commit buffer =\n%q\nwant\n%q", m.commit.Value(), want)
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
	inCompose := drive(t, base, tea.KeyMsg{Type: tea.KeyCtrlJ})
	inCompose = drive(t, inCompose, rkey("q"))
	if inCompose.quit {
		t.Error("q must not quit while the composer is open")
	}
	if inCompose.commit.Value() != "q" {
		t.Errorf("composer = %q, want \"q\"", inCompose.commit.Value())
	}
}

// key builds a plain rune key message.
func rkey(s string) tea.KeyMsg { return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)} }

// altKey builds an Alt-modified rune key message.
func altKey(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s), Alt: true}
}

func newTestModel(t *testing.T) Model {
	t.Helper()
	repo := testRepo(t)
	return drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
}

func TestDownAtEndOfListEntersPaneBelow(t *testing.T) {
	m := drive(t, newTestModel(t), tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1"), Alt: true})
	if len(m.workdir.files) < 2 {
		t.Fatalf("need at least 2 unstaged files, got %d", len(m.workdir.files))
	}

	// Walk to the bottom of the unstaged list; it should not leave yet.
	for i := 0; i < len(m.workdir.files)-1; i++ {
		m = drive(t, m, rkey("j"))
		if m.focus != focusWorkdir {
			t.Fatalf("left the unstaged list early, at row %d", i+1)
		}
	}
	// One more crosses into the staged list, landing at its top.
	m = drive(t, m, rkey("j"))
	if m.focus != focusIndex {
		t.Fatalf("focus = %v, want focusIndex", m.focus)
	}
	if m.index.cursor != 0 {
		t.Errorf("entered the staged list at row %d, want 0", m.index.cursor)
	}
	// The diff should now be showing the staged file.
	if m.diff.file == nil || m.diff.side != git.SideIndex {
		t.Error("moving into the staged list did not load its diff")
	}
}

func TestUpAtTopOfStagedListEntersPaneAbove(t *testing.T) {
	m := drive(t, newTestModel(t), tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("2"), Alt: true})
	if m.focus != focusIndex {
		t.Fatalf("focus = %v, want focusIndex", m.focus)
	}
	m = drive(t, m, rkey("k"))
	if m.focus != focusWorkdir {
		t.Fatalf("focus = %v, want focusWorkdir", m.focus)
	}
	// Coming up from below lands on the last row.
	if want := len(m.workdir.files) - 1; m.workdir.cursor != want {
		t.Errorf("entered the unstaged list at row %d, want %d", m.workdir.cursor, want)
	}
}

func TestUpAtTopOfUnstagedListStays(t *testing.T) {
	m := drive(t, newTestModel(t), tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("1"), Alt: true})
	m = drive(t, m, rkey("k"), rkey("k"))
	if m.focus != focusWorkdir {
		t.Errorf("focus = %v, want to stay on focusWorkdir", m.focus)
	}
	if m.workdir.cursor != 0 {
		t.Errorf("cursor = %d, want 0", m.workdir.cursor)
	}
}

func TestLeftRightCrossColumns(t *testing.T) {
	base := newTestModel(t)
	cases := []struct {
		name  string
		start focusArea
		key   string
		want  focusArea
	}{
		{"unstaged right to diff", focusWorkdir, "l", focusDiff},
		{"staged right to diff", focusIndex, "l", focusDiff},
		{"unstaged left stays", focusWorkdir, "h", focusWorkdir},
		{"staged left stays", focusIndex, "h", focusIndex},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m, cmd := base.setFocus(c.start)
			got := drive(t, m.(Model), rkey(c.key))
			_ = cmd
			if got.focus != c.want {
				t.Errorf("focus = %v, want %v", got.focus, c.want)
			}
		})
	}
}

func TestArrowKeysNavigateLikeHJKL(t *testing.T) {
	base := newTestModel(t)
	m, _ := base.setFocus(focusWorkdir)
	got := drive(t, m.(Model), tea.KeyMsg{Type: tea.KeyRight})
	if got.focus != focusDiff {
		t.Errorf("right arrow: focus = %v, want focusDiff", got.focus)
	}
	got = drive(t, got, tea.KeyMsg{Type: tea.KeyLeft})
	if got.focus != focusWorkdir {
		t.Errorf("left arrow: focus = %v, want focusWorkdir", got.focus)
	}
	got = drive(t, got, tea.KeyMsg{Type: tea.KeyDown})
	if got.workdir.cursor != 1 {
		t.Errorf("down arrow: cursor = %d, want 1", got.workdir.cursor)
	}
}

func step(m Model, msg tea.Msg) Model {
	next, _ := m.Update(msg)
	return next.(Model)
}

func stateFile(t *testing.T, repo *git.Repo, name string) (string, bool) {
	t.Helper()
	b, err := os.ReadFile(filepath.Join(repo.GitDir, name))
	if err != nil {
		return "", false
	}
	return string(b), true
}

func TestQuitSavesCommitBuffer(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m.commit.SetValue("a message in progress\n")

	m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if !m.quit {
		t.Fatal("Ctrl-Q did not quit")
	}
	got, ok := stateFile(t, repo, git.MsgFile)
	if !ok {
		t.Fatalf("%s was not written", git.MsgFile)
	}
	if got != "a message in progress" {
		t.Errorf("saved message = %q", got)
	}
	// The crash backup must not be left behind after a clean exit.
	if _, ok := stateFile(t, repo, git.BackupFile); ok {
		t.Errorf("%s should have been removed on quit", git.BackupFile)
	}

	// A new session picks the message back up.
	next := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	if next.commit.Value() != "a message in progress" {
		t.Errorf("reopened buffer = %q, want the saved message", next.commit.Value())
	}
}

func TestQuitWithEmptyBufferSavesNothing(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if _, ok := stateFile(t, repo, git.MsgFile); ok {
		t.Error("an empty buffer should not be saved")
	}
}

func TestBackupTickWritesCrashBackup(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m.commit.SetValue("half typed  \n")

	m = step(m, backupTickMsg{})
	got, ok := stateFile(t, repo, git.BackupFile)
	if !ok {
		t.Fatalf("%s was not written", git.BackupFile)
	}
	if got != "half typed" {
		t.Errorf("backup = %q, want trailing whitespace stripped", got)
	}

	// Clearing the buffer removes the backup again.
	m.commit.SetValue("")
	m = step(m, backupTickMsg{})
	if _, ok := stateFile(t, repo, git.BackupFile); ok {
		t.Error("backup should be removed once the buffer is empty")
	}
}

func TestCrashBackupIsRestoredOnStartup(t *testing.T) {
	repo := testRepo(t)
	if err := repo.WriteStateFile(git.BackupFile, "recovered text"); err != nil {
		t.Fatal(err)
	}
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	if m.commit.Value() != "recovered text" {
		t.Errorf("buffer = %q, want the crash backup", m.commit.Value())
	}
}

func TestNewerSavedMessageBeatsStaleBackup(t *testing.T) {
	repo := testRepo(t)
	if err := repo.WriteStateFile(git.BackupFile, "stale backup"); err != nil {
		t.Fatal(err)
	}
	// Make the saved message strictly newer than the backup.
	old := time.Now().Add(-time.Minute)
	if err := os.Chtimes(filepath.Join(repo.GitDir, git.BackupFile), old, old); err != nil {
		t.Fatal(err)
	}
	if err := repo.WriteStateFile(git.MsgFile, "the newer saved message"); err != nil {
		t.Fatal(err)
	}

	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	if m.commit.Value() != "the newer saved message" {
		t.Errorf("buffer = %q, want the newer saved message", m.commit.Value())
	}
	if _, ok := stateFile(t, repo, git.BackupFile); ok {
		t.Error("the stale backup should have been discarded")
	}
}

func TestMergeMessageSeedsBuffer(t *testing.T) {
	repo := testRepo(t)
	if err := repo.WriteStateFile("MERGE_HEAD", "0000000000000000000000000000000000000000\n"); err != nil {
		t.Fatal(err)
	}
	if err := repo.WriteStateFile("MERGE_MSG", "Merge branch 'other'"); err != nil {
		t.Fatal(err)
	}
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	if m.commit.Value() != "Merge branch 'other'" {
		t.Errorf("buffer = %q, want the merge message", m.commit.Value())
	}
	// The composer names the kind of commit being written.
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	if !strings.Contains(m.View(), "Merge Commit Message") {
		t.Errorf("the composer should say Merge Commit Message:\n%s", m.View())
	}
}

func TestCommitTemplateSeedsBuffer(t *testing.T) {
	repo := testRepo(t)
	tmpl := filepath.Join(repo.Dir, ".gitmessage")
	if err := os.WriteFile(tmpl, []byte("# describe your change"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := repo.Git("config", "commit.template", tmpl); err != nil {
		t.Fatal(err)
	}
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	if m.commit.Value() != "# describe your change" {
		t.Errorf("buffer = %q, want the commit template", m.commit.Value())
	}
}

func TestAmendLoadsAndRestoresBuffer(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m.commit.SetValue("work in progress")

	// Ctrl-E turns on amend and pulls in HEAD's message.
	m = drive(t, m, rkey("e"))
	if !m.amend {
		t.Fatal("Ctrl-E did not turn on amend")
	}
	if m.commit.Value() != "initial" {
		t.Errorf("buffer = %q, want HEAD's message", m.commit.Value())
	}
	// Amending is announced in the branch bar, and again by the composer.
	if !strings.Contains(m.View(), "[amending]") {
		t.Errorf("the branch bar should show the amend state:\n%s", m.View())
	}
	opened := drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	if !strings.Contains(opened.View(), "Amend Last Commit") {
		t.Errorf("the composer should say Amend Last Commit:\n%s", opened.View())
	}

	// Turning it back off restores what was being typed.
	m = drive(t, m, rkey("e"))
	if m.amend {
		t.Fatal("Ctrl-E did not turn amend back off")
	}
	if m.commit.Value() != "work in progress" {
		t.Errorf("buffer = %q, want the previous message back", m.commit.Value())
	}
}

// A backup tick while amending used to leave the amend text on disk, because
// the quit path promoted the backup without checking for amend.
func TestAmendMessageIsNotSavedOnQuitAfterABackup(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m = drive(t, m, rkey("e"))
	if !m.amend {
		t.Fatal("amend was not enabled")
	}
	m.commit.SetValue("initial, reworded")

	// The periodic backup fires, and then nothing else is typed, so the
	// buffer is clean with respect to the backup.
	m = step(m, backupTickMsg{})
	if !m.bckExists {
		t.Fatal("the backup tick did not write a backup")
	}
	if m.msgDirty() {
		t.Fatal("the buffer should be clean right after a backup")
	}

	m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})
	if got, ok := stateFile(t, repo, git.MsgFile); ok {
		t.Errorf("an amend message was stashed as the next commit message: %q", got)
	}
	if _, ok := stateFile(t, repo, git.BackupFile); ok {
		t.Error("the backup should be removed on quit")
	}
}

func TestAmendMessageIsNotSavedOnQuit(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m = drive(t, m, rkey("e"))
	if !m.amend {
		t.Fatal("amend was not enabled")
	}
	m.commit.SetValue("reworded HEAD")
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlC})

	if _, ok := stateFile(t, repo, git.MsgFile); ok {
		t.Error("an amend message must not be stashed as the next commit message")
	}
}

func TestStartsFocusedOnFirstUnstagedFile(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	if m.focus != focusWorkdir {
		t.Fatalf("focus = %v, want focusWorkdir", m.focus)
	}
	if m.workdir.cursor != 0 {
		t.Errorf("cursor = %d, want the first file", m.workdir.cursor)
	}
	if m.workdir.current() == nil {
		t.Fatal("no file under the cursor")
	}
	// The first file's diff should already be showing.
	if m.diff.file == nil || m.diff.file.Path != m.workdir.current().Path {
		t.Errorf("diff pane shows %v, want %s", m.diff.file, m.workdir.current().Path)
	}
	// Single letter keys are live immediately, rather than being typed into
	// the message buffer.
	got := drive(t, m, rkey("j"))
	if got.workdir.cursor != 1 {
		t.Errorf("j at startup moved to %d, want 1", got.workdir.cursor)
	}
	if got.commit.Value() != "" {
		t.Errorf("keys leaked into the commit buffer: %q", got.commit.Value())
	}
}

func TestStartupFocusWithNoUnstagedFiles(t *testing.T) {
	repo := testRepo(t)
	// Stage everything, so the unstaged list is empty at startup.
	if err := repo.Stage([]string{"."}); err != nil {
		t.Fatal(err)
	}
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	if len(m.workdir.files) != 0 {
		t.Fatalf("expected an empty unstaged list, got %v", m.workdir.files)
	}
	if m.focus != focusWorkdir {
		t.Errorf("focus = %v, want focusWorkdir", m.focus)
	}
	if strings.TrimSpace(m.View()) == "" {
		t.Error("view is empty")
	}
	// Moving down out of the empty list still reaches the staged one.
	got := drive(t, m, rkey("j"))
	if got.focus != focusIndex {
		t.Errorf("focus = %v, want focusIndex", got.focus)
	}
}

// The diff pane remembers which list it is showing, so leaving it and coming
// straight back lands on the same file rather than snapping to the other list.
func TestDiffRemembersWhichListItIsShowing(t *testing.T) {
	for _, c := range []struct {
		name  string
		start focusArea
		focus tea.KeyMsg
		side  git.Side
	}{
		{"unstaged", focusWorkdir, altKey("1"), git.SideWorkdir},
		{"staged", focusIndex, altKey("2"), git.SideIndex},
	} {
		t.Run(c.name, func(t *testing.T) {
			repo := testRepo(t)
			m := drive(t, New(repo),
				tea.WindowSizeMsg{Width: 120, Height: 40},
				New(repo).rescanCmd()(),
			)
			m = drive(t, m, c.focus)
			if m.focus != c.start {
				t.Fatalf("focus = %v, want %v", m.focus, c.start)
			}
			list := m.workdir
			if c.start == focusIndex {
				list = m.index
			}
			want := list.currentPath()
			if want == "" {
				t.Fatal("no file selected")
			}

			// Right into the diff: it must be showing this list's side.
			m = drive(t, m, rkey("l"))
			if m.focus != focusDiff {
				t.Fatalf("focus = %v, want focusDiff", m.focus)
			}
			if m.diff.side != c.side {
				t.Errorf("diff side = %v, want %v", m.diff.side, c.side)
			}
			if m.diff.file == nil || m.diff.file.Path != want {
				t.Errorf("diff shows %v, want %s", m.diff.file, want)
			}

			// Left again: back to the same list, same selection.
			m = drive(t, m, rkey("h"))
			if m.focus != c.start {
				t.Errorf("focus = %v, want to return to %v", m.focus, c.start)
			}
			back := m.workdir
			if c.start == focusIndex {
				back = m.index
			}
			if got := back.currentPath(); got != want {
				t.Errorf("selection = %q, want %q", got, want)
			}
		})
	}
}

func TestStatusMessageClearsOnTheNextKey(t *testing.T) {
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)

	// Changing the diff context reports it in the status bar.
	m = drive(t, m, rkey("="))
	if m.status == "" {
		t.Fatal("changing the context set no status message")
	}
	if !strings.Contains(m.View(), m.status) {
		t.Error("the status message is not on screen")
	}
	shown := m.status

	// Any following key press clears it again.
	m = drive(t, m, rkey("j"))
	if m.status != "" {
		t.Errorf("status = %q, want it cleared by the next key", m.status)
	}
	if strings.Contains(m.View(), shown) {
		t.Error("the stale status message is still on screen")
	}
	// With nothing to report, the bar falls back to the key hints.
	if !strings.Contains(m.View(), "Space stage") {
		t.Error("the status bar should fall back to the hints")
	}

	// Repeating the action still shows its own message.
	m = drive(t, m, rkey("="))
	if m.status == "" {
		t.Error("the action's own message was cleared along with the old one")
	}
}

// diffModel returns a model focused on the diff of a file with several
// changed lines in one hunk.
func diffModel(t *testing.T) Model {
	t.Helper()
	repo := testRepo(t)
	writeFile(t, repo.Dir, "foo.txt", "one\nA\nB\nC\nfive\n")
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m = drive(t, m, altKey("1"))
	for m.workdir.current() != nil && m.workdir.current().Path != "foo.txt" {
		m = drive(t, m, rkey("j"))
	}
	m = drive(t, m, rkey("l"))
	if m.focus != focusDiff {
		t.Fatalf("focus = %v, want focusDiff", m.focus)
	}
	return m
}

// cursorTo puts the diff cursor on the first line of the given kind.
func cursorTo(t *testing.T, m Model, kind byte) Model {
	t.Helper()
	for i, l := range m.diff.lines {
		if l.Kind == kind {
			m.diff.moveTo(i)
			return m
		}
	}
	t.Fatalf("no %c line in the diff", kind)
	return m
}

func TestVisualStartsOnTheCurrentLine(t *testing.T) {
	m := cursorTo(t, diffModel(t), '+')
	at := m.diff.cursor

	m = drive(t, m, rkey("V"))
	if !m.diff.visual() {
		t.Fatal("V did not start visual line selection")
	}
	if m.diff.anchor != at || m.diff.cursor != at {
		t.Errorf("anchor/cursor = %d/%d, want both %d", m.diff.anchor, m.diff.cursor, at)
	}
	if !m.diff.inSelection(at) {
		t.Error("the starting line should be selected")
	}
	// The hint line tells the user what is possible now.
	view := m.View()
	if !strings.Contains(view, "VISUAL LINE") {
		t.Error("the status bar should announce visual line mode")
	}
	for _, want := range []string{"extend", "Space", "Esc"} {
		if !strings.Contains(view, want) {
			t.Errorf("the visual hint should mention %q", want)
		}
	}
}

func TestVisualExtendsWithUpAndDown(t *testing.T) {
	m := cursorTo(t, diffModel(t), '+')
	start := m.diff.cursor
	m = drive(t, m, rkey("V"), rkey("j"), rkey("j"))
	if m.diff.cursor != start+2 {
		t.Fatalf("cursor = %d, want %d", m.diff.cursor, start+2)
	}
	for i := start; i <= start+2; i++ {
		if !m.diff.inSelection(i) {
			t.Errorf("line %d should be selected", i)
		}
	}
	if m.diff.inSelection(start - 1) {
		t.Error("the line above the anchor should not be selected")
	}
	// Arrow keys behave the same.
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyUp})
	if m.diff.cursor != start+1 {
		t.Errorf("after up, cursor = %d, want %d", m.diff.cursor, start+1)
	}
}

func TestVisualDoesNotLeaveTheDiffPane(t *testing.T) {
	m := diffModel(t)
	m.diff.moveTo(len(m.diff.lines) - 1)
	m = drive(t, m, rkey("V"), rkey("j"), rkey("j"))
	if m.focus != focusDiff {
		t.Errorf("focus = %v: running off the end abandoned the selection", m.focus)
	}
	if !m.diff.visual() {
		t.Error("the selection was dropped")
	}
}

func TestVisualCancels(t *testing.T) {
	for _, k := range []struct {
		name string
		msg  tea.KeyMsg
	}{
		{"Esc", tea.KeyMsg{Type: tea.KeyEsc}},
		{"V again", rkey("V")},
	} {
		t.Run(k.name, func(t *testing.T) {
			m := drive(t, cursorTo(t, diffModel(t), '+'), rkey("V"))
			if !m.diff.visual() {
				t.Fatal("visual mode did not start")
			}
			m = drive(t, m, k.msg)
			if m.diff.visual() {
				t.Errorf("%s did not cancel visual mode", k.name)
			}
			if strings.Contains(m.View(), "VISUAL LINE") {
				t.Error("the visual hint is still showing")
			}
		})
	}
}

func TestVisualClearedByLeavingThePane(t *testing.T) {
	m := drive(t, cursorTo(t, diffModel(t), '+'), rkey("V"))
	m = drive(t, m, altKey("1"))
	if m.diff.visual() {
		t.Error("leaving the diff pane should drop the selection")
	}
}

// Space stages the selection rather than the whole hunk while visual mode is
// open, and the hunk when it is not.
func TestSpaceStagesTheVisualSelection(t *testing.T) {
	m := diffModel(t)
	repo := m.repo

	// Select the first added line only.
	m = cursorTo(t, m, '+')
	m = drive(t, m, rkey("V"))
	m = drive(t, m, rkey(" "))
	m = drive(t, m, New(repo).rescanCmd()())

	staged, err := repo.Git("diff", "--cached")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(staged, "+A") {
		t.Errorf("the selected line was not staged:\n%s", staged)
	}
	if strings.Contains(staged, "+B") || strings.Contains(staged, "+C") {
		t.Errorf("more than the selection was staged:\n%s", staged)
	}
	if m.diff.visual() {
		t.Error("visual mode should end once the selection is staged")
	}
}

func TestSpaceStagesTheHunkWhenNotSelecting(t *testing.T) {
	m := diffModel(t)
	repo := m.repo
	m = drive(t, m, rkey(" "))
	m = drive(t, m, New(repo).rescanCmd()())

	staged, err := repo.Git("diff", "--cached")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"+A", "+B", "+C"} {
		if !strings.Contains(staged, want) {
			t.Errorf("the whole hunk should be staged, missing %s:\n%s", want, staged)
		}
	}
}

func TestLStagesJustTheCursorLine(t *testing.T) {
	m := diffModel(t)
	repo := m.repo
	m = cursorTo(t, m, '+')
	m = drive(t, m, rkey("L"))
	m = drive(t, m, New(repo).rescanCmd()())

	staged, err := repo.Git("diff", "--cached")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(staged, "+A") {
		t.Errorf("the cursor line was not staged:\n%s", staged)
	}
	if strings.Contains(staged, "+B") {
		t.Errorf("L staged more than one line:\n%s", staged)
	}
}

func TestAmendRefreshesTheStagedList(t *testing.T) {
	repo := testRepo(t)
	// Commit the staged file so the working tree is otherwise settled.
	if _, err := repo.Commit("second", false); err != nil {
		t.Fatal(err)
	}
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	if len(m.index.files) != 0 {
		t.Fatalf("nothing should be staged to begin with, got %v", m.index.files)
	}

	m = drive(t, m, rkey("e"))
	if !m.amend {
		t.Fatal("amend did not turn on")
	}
	// The commit being amended touched bar.txt, so it is now staged.
	var staged []string
	for _, f := range m.index.files {
		staged = append(staged, f.Path)
	}
	if len(staged) == 0 {
		t.Fatal("switching to amend did not refresh the staged list")
	}
	found := false
	for _, p := range staged {
		if p == "bar.txt" {
			found = true
		}
	}
	if !found {
		t.Errorf("staged list = %v, want it to contain bar.txt", staged)
	}

	// Switching back returns the lists to the plain view.
	m = drive(t, m, rkey("e"))
	if m.amend {
		t.Fatal("amend did not turn off")
	}
	if len(m.index.files) != 0 {
		t.Errorf("staged list = %v, want it empty again", m.index.files)
	}
}

// The diff shown for a staged file follows the same parent.
func TestAmendRefreshesTheStagedDiff(t *testing.T) {
	repo := testRepo(t)
	if _, err := repo.Commit("second", false); err != nil {
		t.Fatal(err)
	}
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m = drive(t, m, rkey("e"))
	m = drive(t, m, altKey("2"))
	if m.focus != focusIndex {
		t.Fatalf("focus = %v, want focusIndex", m.focus)
	}
	if m.diff.file == nil {
		t.Fatal("no diff loaded for the staged file")
	}
	var text []string
	for _, l := range m.diff.lines {
		text = append(text, l.Text)
	}
	joined := strings.Join(text, "\n")
	if !strings.Contains(joined, "beta") {
		t.Errorf("the amend diff should show the commit's own content:\n%s", joined)
	}
}

// Toggling amend only changes what the index is compared against. It must
// never touch the index itself, or work staged before the toggle would be
// silently rewritten.
func TestAmendToggleLeavesTheIndexAlone(t *testing.T) {
	repo := testRepo(t)
	if _, err := repo.Commit("second", false); err != nil {
		t.Fatal(err)
	}
	// Stage something new on top of that commit.
	writeFile(t, repo.Dir, "foo.txt", "one\nTHREE\nthree\nfour\nfive\n")
	if err := repo.Stage([]string{"foo.txt"}); err != nil {
		t.Fatal(err)
	}
	before, err := repo.Git("diff", "--cached", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(before, "+THREE") {
		t.Fatalf("setup failed, nothing staged:\n%s", before)
	}

	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m = drive(t, m, rkey("e"))
	m = drive(t, m, rkey("e"))
	if m.amend {
		t.Fatal("amend should be off again")
	}

	after, err := repo.Git("diff", "--cached", "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	if after != before {
		t.Errorf("toggling amend changed the index:\nbefore:\n%s\nafter:\n%s", before, after)
	}
}

// composeModel opens the commit composer with some text already typed.
func composeModel(t *testing.T, text string) Model {
	t.Helper()
	repo := testRepo(t)
	m := drive(t, New(repo),
		tea.WindowSizeMsg{Width: 120, Height: 40},
		New(repo).rescanCmd()(),
	)
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	if m.compose != composeEdit {
		t.Fatalf("compose = %v, want composeEdit", m.compose)
	}
	m.commit.SetValue(text)
	return m
}

func TestNoCtrlCommandsOutsideTheBuffer(t *testing.T) {
	m := newTestModel(t)
	if m.compose != composeOff {
		t.Fatal("expected the composer to be closed")
	}
	for _, k := range []tea.KeyMsg{
		{Type: tea.KeyCtrlE},
		{Type: tea.KeyCtrlU},
		{Type: tea.KeyCtrlP},
		{Type: tea.KeyCtrlN},
		{Type: tea.KeyCtrlT},
	} {
		got := drive(t, m, k)
		if got.amend {
			t.Errorf("%v toggled amend", k)
		}
		if got.dialog != nil {
			t.Errorf("%v opened the %q dialog", k, got.dialog.title)
		}
		if got.quit {
			t.Errorf("%v quit", k)
		}
	}
}

func TestEveryCommandHasALetter(t *testing.T) {
	cases := []struct {
		name   string
		letter string
		setup  func(*testing.T, *git.Repo)
		check  func(Model) bool
	}{
		{"amend", "e", nil, func(m Model) bool { return m.amend }},
		{"sign off", "o", nil, func(m Model) bool { return strings.Contains(m.commit.Value(), "Signed-off-by:") }},
		{"push", "P", nil, func(m Model) bool { return m.dialog != nil && strings.Contains(m.dialog.title, "Push") }},
		{"merge", "M",
			// Merging needs somewhere to merge from.
			func(t *testing.T, r *git.Repo) {
				if _, err := r.Git("branch", "other"); err != nil {
					t.Fatal(err)
				}
			},
			func(m Model) bool { return m.dialog != nil && strings.Contains(m.dialog.title, "Merge") }},
		{"new branch", "N", nil, func(m Model) bool { return m.dialog != nil && strings.Contains(m.dialog.title, "Branch") }},
		{"checkout", "K", nil, func(m Model) bool { return m.dialog != nil && strings.Contains(m.dialog.title, "Checkout") }},
		{"revert", "v", nil, func(m Model) bool { return m.dialog != nil && strings.Contains(m.dialog.title, "Revert") }},
	}
	for _, c := range cases {
		build := func(t *testing.T) Model {
			t.Helper()
			repo := testRepo(t)
			if c.setup != nil {
				c.setup(t, repo)
			}
			return drive(t, New(repo),
				tea.WindowSizeMsg{Width: 120, Height: 40},
				New(repo).rescanCmd()(),
			)
		}
		t.Run(c.name+" letter", func(t *testing.T) {
			if m := drive(t, build(t), rkey(c.letter)); !c.check(m) {
				t.Errorf("the bare letter %q did not run %s", c.letter, c.name)
			}
		})

	}
}

func TestCursorAndSelectionUseTheEdgeBar(t *testing.T) {
	m := cursorTo(t, diffModel(t), '+')
	st := m.st

	// The bar column is always present, so rows never shift sideways.
	widths := map[int]bool{}
	for _, row := range strings.Split(m.View(), "\n") {
		widths[lipgloss.Width(row)] = true
	}
	if len(widths) != 1 {
		t.Errorf("rows have differing widths: %v", widths)
	}

	before := m.View()
	m = drive(t, m, rkey("V"), rkey("j"))
	after := m.View()
	if before == after {
		t.Fatal("selecting changed nothing on screen")
	}
	widths = map[int]bool{}
	for _, row := range strings.Split(after, "\n") {
		widths[lipgloss.Width(row)] = true
	}
	if len(widths) != 1 {
		t.Errorf("selecting changed row widths: %v", widths)
	}

	// The two bar colours are distinct, so cursor and selection read apart.
	if st.cursorBar.GetBackground() == st.selectBar.GetBackground() {
		t.Error("the cursor and selection bars are the same colour")
	}
	// And neither is a diff band colour, so they cannot be confused with one.
	for _, band := range []lipgloss.Style{st.diffAdd, st.diffAddEmph, st.diffDel, st.diffDelEmph} {
		if st.cursorBar.GetBackground() == band.GetBackground() {
			t.Error("the cursor bar shares a colour with a diff band")
		}
	}
}

// Bands must not leak into the file lists or the commit area.
func TestBandsAreConfinedToTheDiff(t *testing.T) {
	m := diffModel(t)
	view := strings.Split(m.View(), "\n")
	if len(view) < 4 {
		t.Fatal("view too short")
	}
	// The branch bar and the status bar carry no diff colours.
	for _, i := range []int{0, len(view) - 1} {
		for _, band := range []string{"48;5;22m", "48;5;52m", "48;5;28m", "48;5;88m"} {
			if strings.Contains(view[i], band) {
				t.Errorf("row %d has a diff band: %q", i, view[i])
			}
		}
	}
}

func TestCtrlEnterOpensTheComposer(t *testing.T) {
	m := newTestModel(t)
	if m.compose != composeOff {
		t.Fatal("the composer should start closed")
	}
	// It is the only way in: there is no commit letter any more.
	if got := drive(t, m, rkey("c")); got.compose != composeOff {
		t.Error("a bare c opened the composer")
	}

	m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	if m.compose != composeEdit {
		t.Fatalf("compose = %v, want composeEdit", m.compose)
	}
	view := m.View()
	if !strings.Contains(view, "Commit Message") {
		t.Errorf("the composer is not on screen:\n%s", view)
	}
	if !strings.Contains(view, "Ctrl-Enter to commit") {
		t.Error("the composer should say how to commit")
	}
}

// Esc closes the composer and keeps the message, so looking at the diff again
// costs nothing.
func TestComposerRemembersTheMessage(t *testing.T) {
	m := composeModel(t, "half written")
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.compose != composeOff {
		t.Fatal("Esc did not close the composer")
	}
	if strings.Contains(m.View(), "half written") {
		t.Error("the composer is still on screen")
	}
	// Reopening brings it back.
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	if m.commit.Value() != "half written" {
		t.Errorf("composer = %q, want the message kept", m.commit.Value())
	}
}

// Committing takes two deliberate steps: Ctrl-Enter to ask, then y.
func TestComposerCommitsInTwoSteps(t *testing.T) {
	m := composeModel(t, "a real commit")
	repo := m.repo

	m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	if m.compose != composeConfirm {
		t.Fatalf("compose = %v, want composeConfirm", m.compose)
	}
	view := m.View()
	if !strings.Contains(view, "yes") || !strings.Contains(view, "no") {
		t.Errorf("the confirmation should offer y/n:\n%s", view)
	}
	// Nothing has happened yet.
	if got := repo.LastCommitMessage(); got == "a real commit" {
		t.Fatal("the commit was made before it was confirmed")
	}

	// n goes back to editing, with the message intact.
	back := drive(t, m, rkey("n"))
	if back.compose != composeEdit {
		t.Errorf("n left compose = %v, want composeEdit", back.compose)
	}
	if back.commit.Value() != "a real commit" {
		t.Errorf("n lost the message: %q", back.commit.Value())
	}

	// y commits.
	m = drive(t, m, rkey("y"))
	if m.compose != composeOff {
		t.Errorf("compose = %v, want it closed after committing", m.compose)
	}
	if got := repo.LastCommitMessage(); got != "a real commit" {
		t.Errorf("HEAD message = %q", got)
	}
	if m.commit.Value() != "" {
		t.Errorf("the composer kept %q after committing", m.commit.Value())
	}
	if _, ok := stateFile(t, repo, git.MsgFile); ok {
		t.Error("the stashed message should be gone once committed")
	}
}

// Esc from the confirmation returns to editing rather than closing.
func TestComposerConfirmationCanBeBackedOutOf(t *testing.T) {
	m := composeModel(t, "not yet")
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	if m.compose != composeConfirm {
		t.Fatal("did not reach the confirmation")
	}
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyEsc})
	if m.compose != composeEdit {
		t.Errorf("compose = %v, want composeEdit", m.compose)
	}
}

// The three refusals git-gui makes are reported inside the composer rather
// than as another modal on top of it.
func TestComposerRefusals(t *testing.T) {
	t.Run("empty message", func(t *testing.T) {
		m := composeModel(t, "   ")
		m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
		if m.compose != composeEdit {
			t.Errorf("compose = %v, want to stay in composeEdit", m.compose)
		}
		if !strings.Contains(m.composeErr, "commit message") {
			t.Errorf("composeErr = %q", m.composeErr)
		}
		if !strings.Contains(m.View(), "commit message") {
			t.Error("the refusal is not shown in the composer")
		}
		if m.dialog != nil {
			t.Error("a dialog was stacked on top of the composer")
		}
	})

	t.Run("nothing staged", func(t *testing.T) {
		repo := testRepo(t)
		// Unstage the fixture's one staged file.
		if err := repo.Unstage([]string{"bar.txt"}, repo.StatusParent(false)); err != nil {
			t.Fatal(err)
		}
		m := drive(t, New(repo),
			tea.WindowSizeMsg{Width: 120, Height: 40},
			New(repo).rescanCmd()(),
		)
		m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
		m.commit.SetValue("a message")
		m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
		if m.compose != composeEdit {
			t.Errorf("compose = %v, want to stay in composeEdit", m.compose)
		}
		if !strings.Contains(m.composeErr, "Stage at least one file") {
			t.Errorf("composeErr = %q", m.composeErr)
		}
	})

	t.Run("typing clears the refusal", func(t *testing.T) {
		m := composeModel(t, "")
		m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
		if m.composeErr == "" {
			t.Fatal("expected a refusal")
		}
		m = drive(t, m, rkey("x"))
		if m.composeErr != "" {
			t.Errorf("composeErr = %q, want it cleared once typing resumed", m.composeErr)
		}
	})
}

// The composer is a full text field: letters are text and readline keys edit.
func TestComposerIsATextField(t *testing.T) {
	m := composeModel(t, "")
	typed := "sauveoRPMNKc"
	for _, r := range typed {
		m = drive(t, m, rkey(string(r)))
	}
	if m.commit.Value() != typed {
		t.Errorf("composer = %q, want %q", m.commit.Value(), typed)
	}
	if m.dialog != nil {
		t.Fatalf("typing opened the %q dialog", m.dialog.title)
	}

	// Ctrl-A then Ctrl-K clears the line, as readline would.
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlA})
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlK})
	if m.commit.Value() != "" {
		t.Errorf("composer = %q, want Ctrl-A Ctrl-K to have cleared it", m.commit.Value())
	}
}

// Sign off and amend are run before opening the composer, and both show up in
// the message it presents.
func TestSignOffAndAmendFeedTheComposer(t *testing.T) {
	m := newTestModel(t)
	m = drive(t, m, rkey("o"))
	m = drive(t, m, tea.KeyMsg{Type: tea.KeyCtrlJ})
	if !strings.Contains(m.commit.Value(), "Signed-off-by:") {
		t.Errorf("composer = %q, want the sign off", m.commit.Value())
	}
	if !strings.Contains(m.View(), "Signed-off-by:") {
		t.Error("the sign off is not visible in the composer")
	}
}

// The diff now owns the whole right column, so there is nothing below it.
func TestDiffHasNoPaneBelow(t *testing.T) {
	m := diffModel(t)
	m.diff.moveTo(len(m.diff.lines) - 1)
	got := drive(t, m, rkey("j"))
	if got.focus != focusDiff {
		t.Errorf("focus = %v, want to stay on the diff", got.focus)
	}
	if _, ok := paneBelow(focusDiff); ok {
		t.Error("the diff should have no pane below it")
	}
	if _, ok := paneAbove(focusDiff); ok {
		t.Error("the diff should have no pane above it")
	}
}
