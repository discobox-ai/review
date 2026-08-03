package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/obot-platform/difftui/internal/git"
)

type focusArea int

const (
	focusWorkdir focusArea = iota
	focusIndex
	focusDiff
	focusCommit
)

const (
	defaultContext = 5 // git-gui's gui.diffcontext default
	minContext     = 1
	maxContext     = 50
)

// Model is the git-gui main window.
type Model struct {
	repo *git.Repo
	st   *styles
	keys keyMap

	width, height int
	ready         bool

	workdir *fileList
	index   *fileList
	diff    *diffView
	commit  textarea.Model

	focus    focusArea
	amend    bool
	savedMsg string

	branch  string
	files   []git.File
	context int

	status   string
	statusOK bool
	statusEr bool

	dialog *dialog
	quit   bool
}

// New builds the initial model for a repository.
func New(repo *git.Repo) Model {
	ta := textarea.New()
	ta.Placeholder = "Enter commit message"
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.Prompt = ""

	m := Model{
		repo:    repo,
		st:      newStyles(),
		keys:    newKeyMap(),
		workdir: newFileList(git.SideWorkdir),
		index:   newFileList(git.SideIndex),
		diff:    newDiffView(),
		commit:  ta,
		focus:   focusCommit, // git-gui puts the cursor in the message buffer
		context: defaultContext,
	}
	m.commit.Focus()
	return m
}

// Init starts the first rescan.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.rescanCmd(), textarea.Blink)
}

// ---------------------------------------------------------------------------
// Messages and commands
// ---------------------------------------------------------------------------

type rescanMsg struct {
	files  []git.File
	branch string
	merge  string
	err    error
}

type diffMsg struct {
	file git.File
	side git.Side
	text string
	err  error
}

type opMsg struct {
	text   string
	err    error
	rescan bool
}

func (m Model) rescanCmd() tea.Cmd {
	repo := m.repo
	return func() tea.Msg {
		files, err := repo.Status()
		msg := rescanMsg{files: files, branch: repo.CurrentBranch(), err: err}
		if repo.IsMerging() {
			msg.merge = repo.MergeMessage()
		}
		return msg
	}
}

func (m Model) diffCmd(f git.File, side git.Side) tea.Cmd {
	repo, ctx := m.repo, m.context
	return func() tea.Msg {
		text, err := repo.Diff(f, side, ctx)
		return diffMsg{file: f, side: side, text: text, err: err}
	}
}

// op wraps a git mutation so its result lands in the status bar.
func op(text string, rescan bool, fn func() error) tea.Cmd {
	return func() tea.Msg {
		if err := fn(); err != nil {
			return opMsg{text: text, err: err}
		}
		return opMsg{text: text, rescan: rescan}
	}
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

// Update handles a single message.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		m.layout()
		return m, nil

	case rescanMsg:
		return m.onRescan(msg)

	case diffMsg:
		if msg.err != nil {
			m.diff.setMessage("* " + msg.err.Error())
			return m, nil
		}
		m.diff.setPatch(&msg.file, msg.side, msg.text)
		m.layout()
		return m, nil

	case opMsg:
		if msg.err != nil {
			m.setError(msg.text + ": " + firstLine(msg.err.Error()))
			m.dialog = messageDialog(msg.text+" failed", msg.err.Error(), true)
			return m, m.rescanCmd()
		}
		m.setOK(msg.text)
		if msg.rescan {
			return m, tea.Batch(m.rescanCmd(), m.refreshDiffCmd())
		}
		return m, nil

	case tea.KeyMsg:
		return m.onKey(msg)
	}
	return m, nil
}

func (m Model) onRescan(msg rescanMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.setError(firstLine(msg.err.Error()))
		return m, nil
	}
	m.branch = msg.branch
	m.files = msg.files

	var work, idx []git.File
	for _, f := range msg.files {
		if f.State.Unstaged() {
			work = append(work, f)
		}
		if f.State.Staged() {
			idx = append(idx, f)
		}
	}
	m.workdir.setFiles(work)
	m.index.setFiles(idx)

	if msg.merge != "" && strings.TrimSpace(m.commit.Value()) == "" {
		m.commit.SetValue(strings.TrimRight(msg.merge, "\n"))
	}

	m.layout()
	return m, m.refreshDiffCmd()
}

// refreshDiffCmd reloads the diff for whatever the active list points at.
func (m Model) refreshDiffCmd() tea.Cmd {
	list, side := m.activeList()
	if list == nil {
		return nil
	}
	f := list.current()
	if f == nil {
		m.diff.clear()
		return nil
	}
	return m.diffCmd(*f, side)
}

// activeList returns the list that owns the current diff. When the diff pane
// or commit buffer has focus, the last list that was focused wins.
func (m *Model) activeList() (*fileList, git.Side) {
	switch m.focus {
	case focusIndex:
		return m.index, git.SideIndex
	case focusWorkdir:
		return m.workdir, git.SideWorkdir
	}
	if m.diff.file != nil && m.diff.side == git.SideIndex {
		return m.index, git.SideIndex
	}
	return m.workdir, git.SideWorkdir
}

func (m Model) onKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.dialog != nil {
		cmd, closed := m.dialog.update(msg)
		if closed {
			m.dialog = nil
		}
		return m, cmd
	}

	// Global accelerators, available regardless of focus.
	switch {
	case key.Matches(msg, m.keys.Quit):
		m.quit = true
		return m, tea.Quit
	case key.Matches(msg, m.keys.Rescan):
		m.setOK("Rescanning…")
		return m, m.rescanCmd()
	case key.Matches(msg, m.keys.FocusWorkdir):
		return m.setFocus(focusWorkdir)
	case key.Matches(msg, m.keys.FocusIndex):
		return m.setFocus(focusIndex)
	case key.Matches(msg, m.keys.FocusDiff):
		return m.setFocus(focusDiff)
	case key.Matches(msg, m.keys.FocusCommit):
		return m.setFocus(focusCommit)
	case key.Matches(msg, m.keys.NextPane):
		return m.setFocus((m.focus + 1) % 4)
	case key.Matches(msg, m.keys.PrevPane):
		return m.setFocus((m.focus + 3) % 4)
	case key.Matches(msg, m.keys.Stage):
		return m.doStage()
	case key.Matches(msg, m.keys.Unstage):
		return m.doUnstage()
	case key.Matches(msg, m.keys.Revert):
		return m.doRevert()
	case key.Matches(msg, m.keys.SignOff):
		return m.doSignOff()
	case key.Matches(msg, m.keys.Amend):
		return m.toggleAmend()
	case key.Matches(msg, m.keys.Commit):
		return m.doCommit()
	case key.Matches(msg, m.keys.Push):
		return m.doPush()
	case key.Matches(msg, m.keys.Branch):
		return m.doCreateBranch()
	case key.Matches(msg, m.keys.Checkout):
		return m.doCheckout()
	}

	if m.focus == focusCommit {
		// Only the arrow keys leave the message buffer, and only from the
		// very edge of the text: h/j/k/l have to stay typable here.
		info := m.commit.LineInfo()
		atFirstLine := m.commit.Line() == 0 && info.RowOffset == 0
		switch msg.String() {
		case "up":
			if atFirstLine {
				return m.enterPane(focusDiff, entryBottom)
			}
		case "left":
			if atFirstLine && info.ColumnOffset == 0 {
				return m.enterPane(focusIndex, entryKeep)
			}
		}
		var cmd tea.Cmd
		m.commit, cmd = m.commit.Update(msg)
		return m, cmd
	}

	// Unmodified keys, only while a list or the diff has focus.
	switch {
	case key.Matches(msg, m.keys.QuitAlt):
		m.quit = true
		return m, tea.Quit
	case key.Matches(msg, m.keys.Help):
		m.dialog = textDialog("difftui — keys", helpText())
		return m, nil
	case key.Matches(msg, m.keys.StageChanged):
		return m, op("Stage Changed", true, m.repo.StageChanged)
	case key.Matches(msg, m.keys.Merge):
		return m.doMerge()
	case key.Matches(msg, m.keys.Delete):
		return m.doDelete()
	case key.Matches(msg, m.keys.LessContext):
		return m.changeContext(-1)
	case key.Matches(msg, m.keys.MoreContext):
		return m.changeContext(+1)
	}

	if m.focus == focusDiff {
		return m.diffKey(msg)
	}
	return m.listKey(msg)
}

// Panes are laid out as a two by two grid, so moving off the edge of one lands
// on its neighbour:
//
//	unstaged │ diff
//	─────────┼────────
//	staged   │ commit
//
// paneAbove and friends return the neighbour in a direction, or false when
// there is nothing there.
func paneAbove(f focusArea) (focusArea, bool) {
	switch f {
	case focusIndex:
		return focusWorkdir, true
	case focusCommit:
		return focusDiff, true
	}
	return f, false
}

func paneBelow(f focusArea) (focusArea, bool) {
	switch f {
	case focusWorkdir:
		return focusIndex, true
	case focusDiff:
		return focusCommit, true
	}
	return f, false
}

func paneLeft(f focusArea) (focusArea, bool) {
	switch f {
	case focusDiff:
		return focusWorkdir, true
	case focusCommit:
		return focusIndex, true
	}
	return f, false
}

func paneRight(f focusArea) (focusArea, bool) {
	switch f {
	case focusWorkdir:
		return focusDiff, true
	case focusIndex:
		return focusCommit, true
	}
	return f, false
}

// entry says where the cursor lands when navigation moves into a pane.
type entry int

const (
	entryKeep entry = iota
	entryTop
	entryBottom
)

// enterPane focuses a neighbouring pane, placing its cursor at the edge the
// move arrived from so that continuing to hold the key keeps going the same
// way.
func (m Model) enterPane(f focusArea, e entry) (tea.Model, tea.Cmd) {
	switch f {
	case focusWorkdir, focusIndex:
		l := m.workdir
		if f == focusIndex {
			l = m.index
		}
		switch e {
		case entryTop:
			l.moveTo(0)
		case entryBottom:
			l.moveTo(len(l.files) - 1)
		}
	case focusDiff:
		switch e {
		case entryTop:
			m.diff.moveTo(0)
		case entryBottom:
			m.diff.moveTo(len(m.diff.lines) - 1)
		}
	}
	return m.setFocus(f)
}

func (m Model) setFocus(f focusArea) (tea.Model, tea.Cmd) {
	m.focus = f
	if f == focusCommit {
		m.commit.Focus()
		return m, textarea.Blink
	}
	m.commit.Blur()
	if f == focusWorkdir || f == focusIndex {
		return m, m.refreshDiffCmd()
	}
	return m, nil
}

func (m Model) listKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	list, side := m.activeList()
	if list == nil {
		return m, nil
	}
	moved := true
	switch {
	case key.Matches(msg, m.keys.Up):
		// At the top of a list, carry on into the pane above.
		if list.cursor <= 0 {
			if t, ok := paneAbove(m.focus); ok {
				return m.enterPane(t, entryBottom)
			}
			return m, nil
		}
		list.move(-1)
	case key.Matches(msg, m.keys.Down):
		if list.cursor >= len(list.files)-1 {
			if t, ok := paneBelow(m.focus); ok {
				return m.enterPane(t, entryTop)
			}
			return m, nil
		}
		list.move(1)
	case key.Matches(msg, m.keys.Left):
		if t, ok := paneLeft(m.focus); ok {
			return m.enterPane(t, entryKeep)
		}
		return m, nil
	case key.Matches(msg, m.keys.Right):
		if t, ok := paneRight(m.focus); ok {
			return m.enterPane(t, entryKeep)
		}
		return m, nil
	case key.Matches(msg, m.keys.PageUp):
		list.move(-max(list.height-1, 1))
	case key.Matches(msg, m.keys.PageDown):
		list.move(max(list.height-1, 1))
	case key.Matches(msg, m.keys.Home):
		list.moveTo(0)
	case key.Matches(msg, m.keys.End):
		list.moveTo(len(list.files) - 1)
	case key.Matches(msg, m.keys.Toggle):
		// git-gui's icon click: stage from the unstaged list, unstage from
		// the staged list.
		if side == git.SideIndex {
			return m.doUnstage()
		}
		return m.doStage()
	case key.Matches(msg, m.keys.Mark):
		list.toggleMark()
		moved = false
	default:
		moved = false
	}
	if moved {
		return m, m.refreshDiffCmd()
	}
	return m, nil
}

func (m Model) diffKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Up):
		if m.diff.cursor <= 0 {
			if t, ok := paneAbove(m.focus); ok {
				return m.enterPane(t, entryBottom)
			}
			return m, nil
		}
		m.diff.move(-1)
	case key.Matches(msg, m.keys.Down):
		if m.diff.cursor >= len(m.diff.lines)-1 {
			if t, ok := paneBelow(m.focus); ok {
				return m.enterPane(t, entryTop)
			}
			return m, nil
		}
		m.diff.move(1)
	case key.Matches(msg, m.keys.Left):
		if t, ok := paneLeft(m.focus); ok {
			return m.enterPane(t, entryKeep)
		}
		return m, nil
	case key.Matches(msg, m.keys.Right):
		if t, ok := paneRight(m.focus); ok {
			return m.enterPane(t, entryKeep)
		}
		return m, nil
	case key.Matches(msg, m.keys.PageUp):
		m.diff.move(-max(m.diff.height-1, 1))
	case key.Matches(msg, m.keys.PageDown):
		m.diff.move(max(m.diff.height-1, 1))
	case key.Matches(msg, m.keys.Home):
		m.diff.moveTo(0)
	case key.Matches(msg, m.keys.End):
		m.diff.moveTo(len(m.diff.lines) - 1)
	case key.Matches(msg, m.keys.Select):
		m.diff.toggleSelect()
	case key.Matches(msg, m.keys.StageLine):
		return m.doStageLines()
	case key.Matches(msg, m.keys.Toggle):
		return m.doStage()
	}
	switch msg.String() {
	case "n":
		m.diff.jumpHunk(1)
	case "p":
		m.diff.jumpHunk(-1)
	case "r":
		return m.doRevertHunk()
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// Actions
// ---------------------------------------------------------------------------

func (m Model) changeContext(delta int) (tea.Model, tea.Cmd) {
	m.context = min(max(m.context+delta, minContext), maxContext)
	m.setOK(fmt.Sprintf("Diff context: %d lines", m.context))
	return m, m.refreshDiffCmd()
}

func (m Model) doStage() (tea.Model, tea.Cmd) {
	if m.focus == focusDiff {
		return m.doHunk(false)
	}
	list, side := m.activeList()
	if side == git.SideIndex {
		// Already staged; git-gui's Ctrl-T on the index list is a no-op.
		return m, nil
	}
	paths := list.targets()
	if len(paths) == 0 {
		return m, nil
	}
	list.clearMarks()
	return m, op(fmt.Sprintf("Staged %s", plural(len(paths), "file", "files")), true,
		func() error { return m.repo.Stage(paths) })
}

func (m Model) doUnstage() (tea.Model, tea.Cmd) {
	if m.focus == focusDiff {
		return m.doHunk(true)
	}
	list := m.index
	if m.focus == focusWorkdir {
		// Nothing to unstage from the working directory list.
		return m, nil
	}
	paths := list.targets()
	if len(paths) == 0 {
		return m, nil
	}
	list.clearMarks()
	return m, op(fmt.Sprintf("Unstaged %s", plural(len(paths), "file", "files")), true,
		func() error { return m.repo.Unstage(paths) })
}

// doHunk stages or unstages the hunk under the diff cursor.
func (m Model) doHunk(forceUnstage bool) (tea.Model, tea.Cmd) {
	if m.diff.patch == nil {
		return m, nil
	}
	h := m.diff.currentHunk()
	patch, ok := m.diff.patch.HunkPatch(h)
	if !ok {
		m.setError("Cursor is not inside a hunk")
		return m, nil
	}
	unstage := m.diff.side == git.SideIndex || forceUnstage
	label := "Staged hunk"
	if unstage {
		label = "Unstaged hunk"
	}
	repo := m.repo
	return m, op(label, true, func() error { return repo.ApplyPatch(patch, true, unstage) })
}

func (m Model) doStageLines() (tea.Model, tea.Cmd) {
	if m.diff.patch == nil {
		return m, nil
	}
	h, sel := m.diff.selection()
	if h < 0 || len(sel) == 0 {
		m.setError("No added or removed lines selected")
		return m, nil
	}
	unstage := m.diff.side == git.SideIndex
	patch, ok := m.diff.patch.LinePatch(h, sel, unstage)
	if !ok {
		m.setError("Nothing to apply")
		return m, nil
	}
	m.diff.anchor = -1
	label := fmt.Sprintf("Staged %s", plural(len(sel), "line", "lines"))
	if unstage {
		label = fmt.Sprintf("Unstaged %s", plural(len(sel), "line", "lines"))
	}
	repo := m.repo
	return m, op(label, true, func() error { return repo.ApplyPatch(patch, true, unstage) })
}

func (m Model) doRevertHunk() (tea.Model, tea.Cmd) {
	if m.diff.patch == nil || m.diff.side == git.SideIndex {
		m.setError("Revert Hunk only applies to unstaged changes")
		return m, nil
	}
	h := m.diff.currentHunk()
	patch, ok := m.diff.patch.HunkPatch(h)
	if !ok {
		return m, nil
	}
	repo := m.repo
	m.dialog = confirmDialog("Revert Hunk",
		"Any unstaged changes in this hunk will be permanently lost by the revert.",
		func(string) tea.Cmd {
			return op("Reverted hunk", true, func() error { return repo.ApplyPatch(patch, false, true) })
		})
	return m, nil
}

func (m Model) doRevert() (tea.Model, tea.Cmd) {
	list, _ := m.activeList()
	paths := list.targets()
	if len(paths) == 0 {
		return m, nil
	}
	// Untracked files have nothing to revert to; git-gui deletes them.
	untracked := true
	for _, p := range paths {
		if st, ok := m.stateOf(p); !ok || st != "_O" {
			untracked = false
			break
		}
	}
	repo := m.repo
	if untracked {
		m.dialog = confirmDialog("Delete Untracked Files",
			fmt.Sprintf("Delete %s from the working directory? This cannot be undone.",
				plural(len(paths), "untracked file", "untracked files")),
			func(string) tea.Cmd {
				return op("Deleted untracked files", true, func() error { return repo.Delete(paths) })
			})
		return m, nil
	}
	m.dialog = confirmDialog("Revert Changes",
		fmt.Sprintf("Revert changes in %s?\n\nAny unstaged changes will be permanently lost by the revert.",
			plural(len(paths), "file", "files")),
		func(string) tea.Cmd {
			return op("Reverted changes", true, func() error { return repo.Revert(paths) })
		})
	return m, nil
}

func (m Model) doDelete() (tea.Model, tea.Cmd) {
	list, _ := m.activeList()
	paths := list.targets()
	if len(paths) == 0 {
		return m, nil
	}
	var untracked []string
	for _, p := range paths {
		if st, ok := m.stateOf(p); ok && st == "_O" {
			untracked = append(untracked, p)
		}
	}
	if len(untracked) == 0 {
		m.setError("Select untracked files to delete")
		return m, nil
	}
	repo := m.repo
	m.dialog = confirmDialog("Delete Untracked Files",
		fmt.Sprintf("Delete %s? This cannot be undone.",
			plural(len(untracked), "untracked file", "untracked files")),
		func(string) tea.Cmd {
			return op("Deleted untracked files", true, func() error { return repo.Delete(untracked) })
		})
	return m, nil
}

func (m *Model) stateOf(path string) (git.State, bool) {
	for _, f := range m.files {
		if f.Path == path {
			return f.State, true
		}
	}
	return "", false
}

func (m Model) doSignOff() (tea.Model, tea.Cmd) {
	line, err := m.repo.SignOff()
	if err != nil {
		m.setError(err.Error())
		m.dialog = messageDialog("Sign Off", err.Error(), true)
		return m, nil
	}
	msg := m.commit.Value()
	if strings.Contains(msg, line) {
		m.setOK("Already signed off")
		return m, nil
	}
	trimmed := strings.TrimRight(msg, "\n")
	switch {
	case trimmed == "":
		msg = line
	case lastLineIsTrailer(trimmed):
		msg = trimmed + "\n" + line
	default:
		msg = trimmed + "\n\n" + line
	}
	m.commit.SetValue(msg)
	m.setOK("Added Signed-off-by trailer")
	return m, nil
}

// lastLineIsTrailer reports whether the message already ends in a trailer
// block, in which case the sign off joins it rather than starting a new
// paragraph. This is what git-gui's do_signoff checks.
func lastLineIsTrailer(msg string) bool {
	lines := strings.Split(msg, "\n")
	last := strings.TrimSpace(lines[len(lines)-1])
	if last == "" {
		return true
	}
	i := strings.Index(last, ": ")
	if i <= 0 {
		return false
	}
	for _, r := range last[:i] {
		if r != '-' && !(r >= 'A' && r <= 'Z') && !(r >= 'a' && r <= 'z') {
			return false
		}
	}
	return true
}

func (m Model) toggleAmend() (tea.Model, tea.Cmd) {
	if !m.repo.HasHEAD() {
		m.setError("There is nothing to amend; no commit exists yet")
		return m, nil
	}
	m.amend = !m.amend
	if m.amend {
		m.savedMsg = m.commit.Value()
		m.commit.SetValue(m.repo.LastCommitMessage())
		m.setOK("Amending the last commit")
	} else {
		m.commit.SetValue(m.savedMsg)
		m.savedMsg = ""
		m.setOK("New commit")
	}
	return m, m.rescanCmd()
}

func (m Model) doCommit() (tea.Model, tea.Cmd) {
	msg := strings.TrimSpace(m.commit.Value())
	if msg == "" {
		m.dialog = messageDialog("Commit",
			"Please supply a commit message.\n\nA good commit message has the following format:\n\n"+
				"- First line: Describe in one sentence what you did.\n"+
				"- Second line: Blank\n"+
				"- Remaining lines: Describe why this change is good.", true)
		return m, nil
	}
	if !m.amend && len(m.index.files) == 0 && !m.repo.IsMerging() {
		m.dialog = messageDialog("Commit",
			"No changes to commit.\n\nYou must stage at least 1 file before you can commit.", true)
		return m, nil
	}
	for _, f := range m.files {
		if f.State.Unmerged() {
			m.dialog = messageDialog("Commit",
				"Unmerged files cannot be committed.\n\nFile "+f.Path+
					" has merge conflicts. You must resolve them and stage the file before committing.", true)
			return m, nil
		}
	}

	repo, amend := m.repo, m.amend
	m.commit.SetValue("")
	m.amend = false
	m.savedMsg = ""
	return m, func() tea.Msg {
		out, err := repo.Commit(msg, amend)
		if err != nil {
			return opMsg{text: "Commit", err: err}
		}
		return opMsg{text: "Created commit: " + firstLine(out), rescan: true}
	}
}

func (m Model) doPush() (tea.Model, tea.Cmd) {
	remotes, err := m.repo.Remotes()
	if err != nil || len(remotes) == 0 {
		m.dialog = messageDialog("Push", "No remotes are configured for this repository.", true)
		return m, nil
	}
	repo, branch := m.repo, m.branch
	choose := func(remote string) tea.Cmd {
		return func() tea.Msg {
			out, err := repo.Push(remote, branch, false, false)
			if err != nil {
				return opMsg{text: "Push to " + remote, err: err}
			}
			return opMsg{text: "Pushed " + branch + " to " + remote + ": " + firstLine(out), rescan: true}
		}
	}
	if len(remotes) == 1 {
		m.dialog = confirmDialog("Push",
			fmt.Sprintf("Push branch %s to %s?", branch, remotes[0]),
			func(string) tea.Cmd { return choose(remotes[0]) })
		return m, nil
	}
	m.dialog = listDialog("Push", "Push branch "+branch+" to which remote?", remotes, choose)
	return m, nil
}

func (m Model) doMerge() (tea.Model, tea.Cmd) {
	if m.repo.IsMerging() {
		repo := m.repo
		m.dialog = confirmDialog("Abort Merge",
			"Abort the merge in progress? Any resolved conflicts will be lost.",
			func(string) tea.Cmd {
				return op("Merge aborted", true, func() error { _, err := repo.AbortMerge(); return err })
			})
		return m, nil
	}
	branches, err := m.repo.Branches()
	if err != nil || len(branches) == 0 {
		m.setError("No branches to merge")
		return m, nil
	}
	var others []string
	for _, b := range branches {
		if b != m.branch {
			others = append(others, b)
		}
	}
	if len(others) == 0 {
		m.setError("No other branch to merge")
		return m, nil
	}
	repo, cur := m.repo, m.branch
	m.dialog = listDialog("Local Merge", "Merge which branch into "+cur+"?", others,
		func(b string) tea.Cmd {
			return func() tea.Msg {
				out, err := repo.Merge(b)
				if err != nil {
					return opMsg{text: "Merge " + b, err: err, rescan: true}
				}
				return opMsg{text: "Merged " + b + ": " + firstLine(out), rescan: true}
			}
		})
	return m, nil
}

func (m Model) doCheckout() (tea.Model, tea.Cmd) {
	branches, err := m.repo.Branches()
	if err != nil || len(branches) == 0 {
		m.setError("No branches available")
		return m, nil
	}
	repo := m.repo
	m.dialog = listDialog("Checkout Branch", "Switch to which branch?", branches,
		func(b string) tea.Cmd {
			return op("Checked out "+b, true, func() error { _, err := repo.Checkout(b); return err })
		})
	return m, nil
}

func (m Model) doCreateBranch() (tea.Model, tea.Cmd) {
	repo := m.repo
	m.dialog = inputDialog("Create Branch", "Name the new branch:", "feature/…",
		func(name string) tea.Cmd {
			return op("Created branch "+name, true, func() error { _, err := repo.CreateBranch(name); return err })
		})
	return m, nil
}

func (m *Model) setOK(s string)    { m.status, m.statusOK, m.statusEr = s, true, false }
func (m *Model) setError(s string) { m.status, m.statusOK, m.statusEr = s, false, true }

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}

// ---------------------------------------------------------------------------
// Layout and view
// ---------------------------------------------------------------------------

// layout recomputes pane geometry, reproducing git-gui's paned window: the
// two file lists stacked on the left, the diff above the commit area on the
// right.
func (m *Model) layout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	body := max(m.height-2, 4) // branch bar and status bar

	leftW := max(min(m.width*34/100, 46), 24)
	if leftW > m.width-30 {
		leftW = max(m.width/3, 12)
	}
	rightW := max(m.width-leftW-1, 10)

	workH := body / 2
	stagedH := body - workH
	m.workdir.width, m.workdir.height = leftW, max(workH-1, 1)
	m.index.width, m.index.height = leftW, max(stagedH-1, 1)

	commitH := min(10, max(body/2, 5))
	diffH := max(body-commitH, 3)
	m.diff.width, m.diff.height = rightW, max(diffH-1, 1)

	taW := max(rightW-buttonColumnWidth-1, 10)
	m.commit.SetWidth(taW)
	m.commit.SetHeight(max(commitH-1, 2))

	m.workdir.clamp()
	m.index.clamp()
	m.diff.clamp()
}

const buttonColumnWidth = 20

// View renders the whole window.
func (m Model) View() string {
	if m.quit {
		return ""
	}
	if !m.ready {
		return "Loading…"
	}

	branchBar := m.viewBranchBar()

	left := lipgloss.JoinVertical(lipgloss.Left,
		m.workdir.view(m.st, m.focus == focusWorkdir),
		m.index.view(m.st, m.focus == focusIndex),
	)
	right := lipgloss.JoinVertical(lipgloss.Left,
		m.diff.view(m.st, m.focus == focusDiff),
		m.viewCommitArea(),
	)

	bodyH := max(m.height-2, 4)
	sep := strings.TrimRight(strings.Repeat("│\n", bodyH), "\n")
	middle := lipgloss.JoinHorizontal(lipgloss.Top, left, m.st.sep.Render(sep), right)

	view := lipgloss.JoinVertical(lipgloss.Left, branchBar, middle, m.viewStatusBar())

	if m.dialog != nil {
		return m.dialog.view(m.st, m.width, m.height)
	}
	return view
}

func (m Model) viewBranchBar() string {
	repo := m.repo.Name()
	left := m.st.branchLabel.Render("Current Branch: ") + m.st.branchBar.Render(m.branch)
	right := m.st.dimText.Render(repo + "  ·  F1 help  ·  Ctrl-Q quit")
	gapW := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gapW < 1 {
		return truncateANSI(left, m.width)
	}
	return left + strings.Repeat(" ", gapW) + right
}

// viewCommitArea draws the button column beside the commit message buffer,
// with the amend checkbox in the buffer header, as in git-gui.
func (m Model) viewCommitArea() string {
	rightW := m.diff.width
	label := "Commit Message:"
	switch {
	case m.amend && !m.repo.HasHEAD():
		label = "Amended Initial Commit Message:"
	case m.amend && m.repo.IsMerging():
		label = "Amended Merge Commit Message:"
	case m.amend:
		label = "Amended Commit Message:"
	case m.repo.IsMerging():
		label = "Merge Commit Message:"
	case !m.repo.HasHEAD():
		label = "Initial Commit Message:"
	}
	check := "[ ] Amend Last Commit"
	if m.amend {
		check = "[x] Amend Last Commit"
	}
	labelStyled := m.st.commitLabel.Render(label)
	amendStyled := m.st.dimText.Render(check)
	if m.amend {
		amendStyled = m.st.statusWA.Render(check)
	}
	gap := rightW - lipgloss.Width(labelStyled) - lipgloss.Width(amendStyled)
	header := labelStyled
	if gap > 0 {
		header += strings.Repeat(" ", gap) + amendStyled
	}
	header = truncateANSI(header, rightW)

	buttons := m.viewButtons()
	msg := m.commit.View()
	area := lipgloss.JoinHorizontal(lipgloss.Top, buttons, " ", msg)
	return lipgloss.JoinVertical(lipgloss.Left, header, area)
}

func (m Model) viewButtons() string {
	type btn struct{ label, accel string }
	btns := []btn{
		{"Rescan", "F5"},
		{"Stage Changed", "i"},
		{"Sign Off", "^S"},
		{"Commit", "^↵"},
		{"Push", "^P"},
	}
	w := buttonColumnWidth
	rows := make([]string, 0, len(btns))
	for _, b := range btns {
		// Measure display cells: "^↵" is two bytes wider than it looks.
		gap := w - 4 - width(b.label) - width(b.accel)
		if gap < 1 {
			gap = 1
		}
		text := "[ " + b.label + strings.Repeat(" ", gap) + m.st.key.Render(b.accel) + " ]"
		rows = append(rows, truncateANSI(text, w))
	}
	for len(rows) < max(m.commit.Height(), 0) {
		rows = append(rows, strings.Repeat(" ", w))
	}
	return lipgloss.JoinVertical(lipgloss.Left, rows...)
}

func (m Model) viewStatusBar() string {
	var left string
	switch {
	case m.statusEr:
		left = m.st.statusER.Render("✗ " + m.status)
	case m.status != "":
		left = m.st.statusOK.Render(m.status)
	default:
		left = m.st.dimText.Render(m.hints())
	}
	right := m.st.dimText.Render(fmt.Sprintf("U:%d  S:%d  ctx:%d",
		len(m.workdir.files), len(m.index.files), m.context))
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return truncateANSI(left, m.width)
	}
	return left + strings.Repeat(" ", gap) + right
}

func (m Model) hints() string {
	switch m.focus {
	case focusWorkdir:
		return "hjkl/arrows move · Space stage · Ctrl-J revert · m mark · i stage changed"
	case focusIndex:
		return "hjkl/arrows move · Space unstage · Ctrl-U unstage · m mark"
	case focusDiff:
		return "Space stage hunk · v select lines · S stage lines · r revert hunk · n/p hunk · -/= context"
	default:
		return "Ctrl-Enter commit · Ctrl-S sign off · Ctrl-E amend · Alt-1..4 panes · F1 help"
	}
}

func helpText() string {
	return strings.Join([]string{
		"Panes",
		"  Alt-1  Unstaged Changes      Alt-2  Staged Changes",
		"  Alt-3  Diff                  Alt-4  Commit Message",
		"  Tab / Shift-Tab  cycle panes",
		"",
		"  The panes form a grid, and the arrow keys (or h/j/k/l) walk it:",
		"",
		"      Unstaged │ Diff",
		"      ─────────┼────────",
		"      Staged   │ Commit",
		"",
		"  Up or down past the end of a list carries on into the pane above",
		"  or below it, and left/right cross between the two columns. In the",
		"  commit message buffer only the arrow keys move out, and only from",
		"  the very start of the text, so h/j/k/l stay typable.",
		"",
		"Commit",
		"  F5, Ctrl-R   Rescan",
		"  Ctrl-T       Stage To Commit",
		"  i            Stage Changed Files To Commit",
		"  Ctrl-U       Unstage From Commit",
		"  Ctrl-J       Revert Changes",
		"  Ctrl-S       Sign Off",
		"  Ctrl-E       Amend Last Commit",
		"  Ctrl-Enter, Alt-C   Commit",
		"  - / =        Show less / more diff context",
		"",
		"File lists",
		"  Up/Down, j/k  move (loads the diff)",
		"  Space         stage from the unstaged list, unstage from the staged list",
		"  m             mark a file so commands act on several at once",
		"  D             delete untracked files",
		"",
		"Diff pane",
		"  Space, Ctrl-T stage the hunk under the cursor",
		"  Ctrl-U        unstage the hunk under the cursor",
		"  v             start or clear a line selection",
		"  S             stage or unstage the selected lines",
		"  r             revert the hunk (unstaged side only)",
		"  n / p         next / previous hunk",
		"",
		"Branch and remote",
		"  Ctrl-N  Create Branch    Ctrl-O  Checkout Branch",
		"  M       Local Merge / Abort Merge",
		"  Ctrl-P  Push",
		"",
		"  Ctrl-Q, Ctrl-C  Quit  (q also quits from a list or the diff)",
		"",
		"Ctrl-I and Ctrl-M cannot be told apart from Tab and Enter in a",
		"terminal, so \"Stage Changed\" is bound to i and \"Local Merge\" to M.",
	}, "\n")
}
