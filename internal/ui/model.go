package ui

import (
	"fmt"
	"strings"
	"time"

	"github.com/charmbracelet/bubbles/key"
	"github.com/charmbracelet/bubbles/textarea"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
	"github.com/discobox-ai/difftui/internal/git"
)

type focusArea int

const (
	focusWorkdir focusArea = iota
	focusIndex
	focusDiff
	focusCount
)

// composeState is the commit composer, which replaces git-gui's always visible
// message buffer. It opens on Ctrl-Enter, remembers what was typed when it is
// closed, and asks before it actually commits.
type composeState int

const (
	composeOff composeState = iota
	composeEdit
	composeConfirm
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

	// Cached repository facts, refreshed by each rescan: the view must not
	// shell out to git while drawing a frame.
	hasHEAD bool
	merging bool

	// The commit buffer is persisted to the git directory, the way git-gui
	// does it. msgBackedUp is the buffer contents as of the last backup, so
	// comparing against it stands in for Tk's "edit modified" flag.
	msgBackedUp string
	bckExists   bool

	status   string
	statusOK bool
	statusEr bool

	dialog *dialog
	quit   bool

	// compose is the commit composer's state; the message itself stays in
	// commit, so it survives the composer being closed and reopened.
	compose    composeState
	composeErr string

	// rescanning marks the transient "Rescanning…" status so it can be
	// cleared when the scan lands, rather than sitting there looking like
	// work still in progress.
	rescanning bool
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
		// git-gui starts with the cursor in the message buffer; starting on
		// the first unstaged file is friendlier in a terminal, where it also
		// makes the single letter keys live straight away.
		focus:   focusWorkdir,
		context: defaultContext,
	}
	m.loadSavedMessage()
	return m
}

// loadSavedMessage restores the commit buffer that a previous session left
// behind. git-gui prefers its periodic backup, unless the message saved on a
// clean exit is newer, in which case the stale backup is discarded and the
// saved message is picked up by the first rescan.
func (m *Model) loadSavedMessage() {
	bck, ok := m.repo.ReadStateFile(git.BackupFile)
	if !ok {
		return
	}
	m.bckExists = true
	if m.repo.StateFileExists(git.MsgFile) {
		if m.repo.StateFileNewer(git.BackupFile, git.MsgFile) {
			_ = m.repo.RemoveStateFile(git.MsgFile)
		} else {
			_ = m.repo.RemoveStateFile(git.BackupFile)
			m.bckExists = false
			return
		}
	}
	m.setMessage(bck)
}

// setMessage replaces the commit buffer and marks it as already backed up.
func (m *Model) setMessage(s string) {
	m.commit.SetValue(normalizeMessage(s))
	m.msgBackedUp = m.commit.Value()
}

// msgDirty reports whether the buffer changed since the last backup.
func (m *Model) msgDirty() bool { return m.commit.Value() != m.msgBackedUp }

// normalizeMessage strips trailing whitespace from every line and trims the
// message, matching what git-gui writes out.
func normalizeMessage(s string) string {
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = strings.TrimRight(l, " \t\r")
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}

// Init starts the first rescan.
func (m Model) Init() tea.Cmd {
	return tea.Batch(m.rescanCmd(), textarea.Blink, backupTick())
}

// backupInterval matches git-gui, which re-writes its crash backup every two
// seconds while the buffer has unsaved edits.
const backupInterval = 2 * time.Second

type backupTickMsg struct{}

func backupTick() tea.Cmd {
	return tea.Tick(backupInterval, func(time.Time) tea.Msg { return backupTickMsg{} })
}

// ---------------------------------------------------------------------------
// Messages and commands
// ---------------------------------------------------------------------------

type rescanMsg struct {
	files   []git.File
	branch  string
	merge   string
	hasHEAD bool
	merging bool
	err     error
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
	repo, amend := m.repo, m.amend
	return func() tea.Msg {
		// git-gui's PARENT: while amending, the index is measured against
		// HEAD's parent, so the staged list shows the whole amended commit.
		files, err := repo.Status(repo.StatusParent(amend))
		msg := rescanMsg{
			files:   files,
			branch:  repo.CurrentBranch(),
			hasHEAD: repo.HasHEAD(),
			merging: repo.IsMerging(),
			err:     err,
		}
		if msg.merging {
			msg.merge = repo.MergeMessage()
		}
		return msg
	}
}

func (m Model) diffCmd(f git.File, side git.Side) tea.Cmd {
	repo, ctx, amend := m.repo, m.context, m.amend
	return func() tea.Msg {
		text, err := repo.Diff(f, side, ctx, repo.StatusParent(amend))
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

	case backupTickMsg:
		m.backupMessage()
		return m, backupTick()

	case tea.KeyMsg:
		return m.onKey(msg)
	}
	return m, nil
}

// backupMessage writes the periodic crash backup of the commit buffer.
func (m *Model) backupMessage() {
	if !m.msgDirty() && !m.bckExists {
		return
	}
	switch body := normalizeMessage(m.commit.Value()); {
	case body == "":
		if m.bckExists {
			_ = m.repo.RemoveStateFile(git.BackupFile)
			m.bckExists = false
		}
	case m.msgDirty():
		if err := m.repo.WriteStateFile(git.BackupFile, body); err == nil {
			m.bckExists = true
		}
	}
	m.msgBackedUp = m.commit.Value()
}

// saveOnQuit stashes the commit buffer so the next session starts where this
// one left off. An amend message is deliberately not kept: it belongs to a
// commit that already exists.
func (m *Model) saveOnQuit() {
	if m.amend {
		// git-gui only applies this check when the buffer changed since its
		// last backup, so quitting mid-amend can promote the backup into the
		// next session's message. Applying it uniformly is the small fix.
		_ = m.repo.RemoveStateFile(git.MsgFile)
		_ = m.repo.RemoveStateFile(git.BackupFile)
		m.bckExists = false
		return
	}
	switch {
	case m.bckExists && !m.msgDirty():
		// Nothing changed since the last backup, so promote it.
		if err := m.repo.RenameStateFile(git.BackupFile, git.MsgFile); err == nil {
			m.bckExists = false
		}
	case m.msgDirty():
		if body := normalizeMessage(m.commit.Value()); body != "" {
			_ = m.repo.WriteStateFile(git.MsgFile, body)
		} else {
			_ = m.repo.RemoveStateFile(git.MsgFile)
		}
	}
	if m.bckExists {
		_ = m.repo.RemoveStateFile(git.BackupFile)
		m.bckExists = false
	}
}

// doQuit saves the commit buffer on the way out.
func (m Model) doQuit() (tea.Model, tea.Cmd) {
	m.saveOnQuit()
	m.quit = true
	return m, tea.Quit
}

func (m Model) onRescan(msg rescanMsg) (tea.Model, tea.Cmd) {
	// Clear the transient "Rescanning…" note now the scan has landed. A
	// message from a command that triggered the rescan, such as "Staged 1
	// file", is left alone: it is reporting the command, not the scan.
	if m.rescanning {
		m.rescanning = false
		if m.status == "Rescanning…" {
			m.clearStatus()
		}
	}
	if msg.err != nil {
		m.setError(firstLine(msg.err.Error()))
		return m, nil
	}
	m.branch = msg.branch
	m.files = msg.files
	m.hasHEAD = msg.hasHEAD
	m.merging = msg.merging

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

	m.loadMessageChain(msg.merge)

	m.layout()
	return m, m.refreshDiffCmd()
}

// loadMessageChain fills an untouched commit buffer from the first source
// that has something, in git-gui's order: the message saved on the last exit,
// then a merge, a squash, and finally commit.template.
func (m *Model) loadMessageChain(mergeMsg string) {
	if m.bckExists || m.amend {
		return
	}
	if m.msgDirty() && strings.TrimSpace(m.commit.Value()) != "" {
		return
	}
	if saved, ok := m.repo.ReadStateFile(git.MsgFile); ok {
		m.setMessage(saved)
		return
	}
	if mergeMsg != "" {
		m.setMessage(mergeMsg)
		return
	}
	if s := m.repo.SquashMessage(); s != "" {
		m.setMessage(s)
		return
	}
	if t := m.repo.CommitTemplate(); t != "" {
		m.setMessage(t)
	}
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

	if m.compose != composeOff {
		return m.composeKey(msg)
	}

	// A status message lasts until the next key press. Clearing it here
	// rather than after dispatch means an action that sets its own message
	// still gets to show it.
	m.clearStatus()

	// The only keys that work everywhere, message buffer included.
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m.doQuit()
	case key.Matches(msg, m.keys.Help):
		m.dialog = textDialog("difftui — keys", helpText())
		return m, nil
	case key.Matches(msg, m.keys.Commit):
		return m.openCompose()
	case key.Matches(msg, m.keys.Rescan):
		m.setOK("Rescanning…")
		m.rescanning = true
		return m, m.rescanCmd()
	case key.Matches(msg, m.keys.FocusWorkdir):
		return m.setFocus(focusWorkdir)
	case key.Matches(msg, m.keys.FocusIndex):
		return m.setFocus(focusIndex)
	case key.Matches(msg, m.keys.FocusDiff):
		return m.setFocus(focusDiff)
	case key.Matches(msg, m.keys.NextPane):
		return m.setFocus((m.focus + 1) % focusCount)
	case key.Matches(msg, m.keys.PrevPane):
		return m.setFocus((m.focus + focusCount - 1) % focusCount)
	}

	// Commands. A letter each, live in the file lists and the diff. In the
	// message buffer a letter is text, so these are not reachable there:
	// press Tab to leave the buffer first.
	switch {
	case key.Matches(msg, m.keys.QuitKey):
		return m.doQuit()
	case key.Matches(msg, m.keys.HelpKey):
		m.dialog = textDialog("difftui — keys", helpText())
		return m, nil
	case key.Matches(msg, m.keys.StageKey):
		return m.doStage()
	case key.Matches(msg, m.keys.StageChangedKey):
		return m, op("Staged all changed files", true, m.repo.StageChanged)
	case key.Matches(msg, m.keys.UnstageKey):
		return m.doUnstage()
	case key.Matches(msg, m.keys.RevertKey):
		return m.doRevert()
	case key.Matches(msg, m.keys.SignOffKey):
		return m.doSignOff()
	case key.Matches(msg, m.keys.AmendKey):
		return m.toggleAmend()
	case key.Matches(msg, m.keys.LessContextKey):
		return m.changeContext(-1)
	case key.Matches(msg, m.keys.MoreContextKey):
		return m.changeContext(+1)
	case key.Matches(msg, m.keys.PushKey):
		return m.doPush()
	case key.Matches(msg, m.keys.MergeKey):
		return m.doMerge()
	case key.Matches(msg, m.keys.BranchKey):
		return m.doCreateBranch()
	case key.Matches(msg, m.keys.CheckoutKey):
		return m.doCheckout()
	case key.Matches(msg, m.keys.RescanKey):
		m.setOK("Rescanning…")
		m.rescanning = true
		return m, m.rescanCmd()
	case key.Matches(msg, m.keys.Delete):
		return m.doDelete()
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
	if f == focusIndex {
		return focusWorkdir, true
	}
	return f, false
}

func paneBelow(f focusArea) (focusArea, bool) {
	if f == focusWorkdir {
		return focusIndex, true
	}
	return f, false
}

// paneLeft returns to whichever file list owns the diff currently on show,
// rather than to a fixed pane. git-gui tracks the same thing as
// current_diff_side, so leaving the diff and coming back keeps you on the file
// you were looking at.
func (m *Model) paneLeft(f focusArea) (focusArea, bool) {
	switch f {
	case focusDiff:
		if _, side := m.activeList(); side == git.SideIndex {
			return focusIndex, true
		}
		return focusWorkdir, true
	}
	return f, false
}

// paneRight sends both lists to the diff: it is the pane you want next from
// either one. The commit buffer is below the diff, reached with down.
func paneRight(f focusArea) (focusArea, bool) {
	switch f {
	case focusWorkdir, focusIndex:
		return focusDiff, true
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
	if f != focusDiff {
		m.diff.clearVisual()
	}
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
		if t, ok := m.paneLeft(m.focus); ok {
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
	// While a visual selection is open the cursor stays inside the diff:
	// running off the end would abandon the selection mid-gesture.
	if m.diff.visual() {
		switch {
		case key.Matches(msg, m.keys.Up):
			m.diff.move(-1)
			return m, nil
		case key.Matches(msg, m.keys.Down):
			m.diff.move(1)
			return m, nil
		case key.Matches(msg, m.keys.Cancel):
			m.diff.clearVisual()
			return m, nil
		case key.Matches(msg, m.keys.Toggle):
			// Space and Enter stage what is selected, as in the lists.
			return m.doStageLines()
		}
	}

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
		if t, ok := m.paneLeft(m.focus); ok {
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
	case key.Matches(msg, m.keys.Visual):
		m.diff.toggleVisual()
	case key.Matches(msg, m.keys.StageLine):
		return m.doStageLines()
	case key.Matches(msg, m.keys.Toggle):
		return m.doStage()
	}
	switch {
	case key.Matches(msg, m.keys.NextHunk):
		m.diff.jumpHunk(1)
	case key.Matches(msg, m.keys.PrevHunk):
		m.diff.jumpHunk(-1)
	case key.Matches(msg, m.keys.RevertHunk):
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
	repo, amend := m.repo, m.amend
	return m, op(fmt.Sprintf("Unstaged %s", plural(len(paths), "file", "files")), true,
		func() error { return repo.Unstage(paths, repo.StatusParent(amend)) })
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
	if !m.hasHEAD {
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

// openCompose brings up the commit composer, with whatever message was last
// typed still in it.
func (m Model) openCompose() (tea.Model, tea.Cmd) {
	m.compose = composeEdit
	m.composeErr = ""
	m.layoutCompose()
	m.commit.Focus()
	return m, textarea.Blink
}

// closeCompose puts the composer away, keeping the message for next time.
func (m Model) closeCompose() (tea.Model, tea.Cmd) {
	m.compose = composeOff
	m.composeErr = ""
	m.commit.Blur()
	return m, nil
}

// composeKey drives the composer. The message field is a full text editor, so
// only Ctrl-Enter and Esc are taken from it.
func (m Model) composeKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, m.keys.Quit) {
		return m.doQuit()
	}

	if m.compose == composeConfirm {
		switch strings.ToLower(msg.String()) {
		case "y":
			return m.doCommit()
		case "n", "esc", "q":
			m.compose = composeEdit
			return m, nil
		}
		return m, nil
	}

	switch {
	case key.Matches(msg, m.keys.Commit):
		if problem := m.commitProblem(); problem != "" {
			m.composeErr = problem
			return m, nil
		}
		m.composeErr = ""
		m.compose = composeConfirm
		return m, nil
	case key.Matches(msg, m.keys.Cancel):
		return m.closeCompose()
	}

	m.composeErr = ""
	var cmd tea.Cmd
	m.commit, cmd = m.commit.Update(msg)
	return m, cmd
}

// commitProblem reports why the commit cannot be made, or "" when it can.
// git-gui refuses the same three cases.
func (m Model) commitProblem() string {
	if strings.TrimSpace(m.commit.Value()) == "" {
		return "Please supply a commit message. A good one says in its first " +
			"line what you did, leaves the second blank, and explains why below."
	}
	for _, f := range m.files {
		if f.State.Unmerged() {
			return "Unmerged files cannot be committed. " + f.Path +
				" has conflicts: resolve them and stage the file first."
		}
	}
	if !m.amend && len(m.index.files) == 0 && !m.merging {
		return "No changes to commit. Stage at least one file first."
	}
	return ""
}

// doCommit makes the commit. The composer has already checked it can be made.
func (m Model) doCommit() (tea.Model, tea.Cmd) {
	message := normalizeMessage(m.commit.Value())
	repo, amend := m.repo, m.amend

	m.commit.SetValue("")
	m.commit.Blur()
	m.amend = false
	m.savedMsg = ""
	m.msgBackedUp = m.commit.Value()
	m.bckExists = false
	m.compose = composeOff
	m.composeErr = ""

	return m, func() tea.Msg {
		out, err := repo.Commit(message, amend)
		if err != nil {
			return opMsg{text: "Commit", err: err}
		}
		// The message is now in the commit, so drop the stashed copies.
		_ = repo.RemoveStateFile(git.MsgFile)
		_ = repo.RemoveStateFile(git.BackupFile)
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
	if m.merging {
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

func (m *Model) clearStatus()      { m.status, m.statusOK, m.statusEr = "", false, false }
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

	m.diff.width, m.diff.height = rightW, max(body-1, 1)

	m.layoutCompose()
	m.workdir.clamp()
	m.index.clamp()
	m.diff.clamp()
}

// composeWidth is how wide the commit composer's box is drawn.
func (m *Model) composeWidth() int {
	return min(max(m.width-8, 30), 80)
}

// layoutCompose sizes the message field inside the composer box.
func (m *Model) layoutCompose() {
	inner := max(m.composeWidth()-6, 10)
	m.commit.SetWidth(inner)
	m.commit.SetHeight(max(min(m.height-14, 10), 3))
}

// View renders the whole window.
func (m Model) View() string {
	if m.quit {
		return ""
	}
	if !m.ready {
		return "Loading…"
	}

	left := lipgloss.JoinVertical(lipgloss.Left,
		m.workdir.view(m.st, m.focus == focusWorkdir),
		m.index.view(m.st, m.focus == focusIndex),
	)
	right := m.diff.view(m.st, m.focus == focusDiff)

	bodyH := max(m.height-2, 4)
	sep := strings.TrimRight(strings.Repeat("│\n", bodyH), "\n")
	middle := lipgloss.JoinHorizontal(lipgloss.Top, left, m.st.sep.Render(sep), right)

	view := lipgloss.JoinVertical(lipgloss.Left,
		m.viewBranchBar(), middle, m.viewStatusBar())

	if m.dialog != nil {
		return m.dialog.view(m.st, m.width, m.height)
	}
	if m.compose != composeOff {
		return m.viewCompose()
	}
	return view
}

// viewCompose draws the commit composer: the message being written, and either
// how to commit it or the question asked before doing so.
func (m Model) viewCompose() string {
	boxWidth := m.composeWidth()
	inner := max(boxWidth-6, 10)

	title := "Commit Message"
	switch {
	case m.amend && !m.hasHEAD:
		title = "Amended Initial Commit Message"
	case m.amend:
		title = "Amend Last Commit"
	case m.merging:
		title = "Merge Commit Message"
	case !m.hasHEAD:
		title = "Initial Commit Message"
	}

	var b strings.Builder
	b.WriteString(m.st.dialogTitle.Render(truncate(title, inner)))
	b.WriteString("\n")
	b.WriteString(m.st.dimText.Render(truncate(fmt.Sprintf("on %s · %s staged",
		m.branch, plural(len(m.index.files), "file", "files")), inner)))
	b.WriteString("\n\n")
	b.WriteString(m.commit.View())
	b.WriteString("\n")

	if m.composeErr != "" {
		b.WriteString("\n")
		for _, l := range wrap(m.composeErr, inner) {
			b.WriteString(m.st.statusER.Render(l) + "\n")
		}
	}

	b.WriteString("\n")
	switch m.compose {
	case composeConfirm:
		question := fmt.Sprintf("Commit %s to %s?",
			plural(len(m.index.files), "staged file", "staged files"), m.branch)
		if m.amend {
			question = "Rewrite the last commit on " + m.branch + "?"
		}
		b.WriteString(truncate(question, inner) + "\n")
		b.WriteString(m.st.key.Render("y") + " yes   " + m.st.key.Render("n") + " no")
	default:
		b.WriteString(m.st.dimText.Render(truncate(
			"Ctrl-Enter to commit · Esc closes and keeps the message", inner)))
	}

	box := m.st.dialog.Width(boxWidth).Render(b.String())
	return lipgloss.Place(m.width, m.height, lipgloss.Center, lipgloss.Center, box)
}

func (m Model) viewBranchBar() string {
	repo := m.repo.Name()
	left := m.st.branchLabel.Render("Current Branch: ") + m.st.branchBar.Render(m.branch)
	if m.amend {
		left += "  " + m.st.statusWA.Render("[amending]")
	}
	right := m.st.dimText.Render(repo + "  ·  F1 help  ·  Ctrl-Q quit")
	gapW := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gapW < 1 {
		return truncateANSI(left, m.width)
	}
	return left + strings.Repeat(" ", gapW) + right
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
		return "Space stage · s stage · a stage all · v revert · m mark · Ctrl-Enter commit · ? keys"
	case focusIndex:
		return "Space unstage · u unstage · a stage all · m mark · Ctrl-Enter commit · ? keys"
	case focusDiff:
		if m.diff.visual() {
			n := 1 + max(m.diff.cursor, m.diff.anchor) - min(m.diff.cursor, m.diff.anchor)
			return fmt.Sprintf("VISUAL LINE  %s · ↑/↓ extend · Space stage · V or Esc cancel",
				plural(n, "line", "lines"))
		}
		return "Space stage hunk · V select lines · L stage line · r revert · n/p hunk · -/= context"
	default:
		return "Ctrl-Enter writes a commit message · ? keys"
	}
}

func helpText() string {
	return strings.Join([]string{
		"Every command is one letter, pressed in a file list or the diff.",
		"",
		"    s  stage              u  unstage",
		"    a  stage all changed  v  revert changes",
		"    o  sign off           e  amend last commit",
		"    R  rescan (also F5)   -  /  =   less / more diff context",
		"    P  push               M  local merge / abort merge",
		"    N  new branch         K  checkout branch",
		"",
		"    Ctrl-Enter            write a commit message",
		"    Ctrl-C or q           quit",
		"    F1 or ?               these keys",
		"",
		"───────────────────────────────────────────────────────────────",
		"Committing",
		"",
		"  Ctrl-Enter opens the message composer. Type the message there —",
		"  it is a full text field, so every readline key works: Ctrl-A,",
		"  Ctrl-E, Ctrl-K, Ctrl-U, Ctrl-W and the rest.",
		"",
		"  Ctrl-Enter again asks to commit, and answers y or n.",
		"  Esc closes the composer and keeps what you typed for next time,",
		"  so nothing is lost by looking at the diff again first.",
		"",
		"  Sign off with o and amend with e before opening it: both change",
		"  the message the composer will show.",
		"",
		"───────────────────────────────────────────────────────────────",
		"Panes",
		"",
		"  Tab / Shift-Tab  cycle",
		"  Alt-1 unstaged   Alt-2 staged   Alt-3 diff",
		"",
		"  Arrows or h/j/k/l move. Moving off the edge of a pane enters",
		"  the next:",
		"",
		"      Unstaged │",
		"      ─────────┤ Diff",
		"      Staged   │",
		"",
		"  Right from either list opens the diff; left comes back to",
		"  whichever list the diff is showing, still on the same file.",
		"",
		"───────────────────────────────────────────────────────────────",
		"On a file, beyond the commands above",
		"",
		"  Space   stage, or unstage from the staged list",
		"  m       mark a file, so a command acts on several at once",
		"  D       delete untracked files",
		"",
		"───────────────────────────────────────────────────────────────",
		"In the diff, beyond the commands above",
		"",
		"  Space   stage the hunk under the cursor",
		"  V       select whole lines, like vim: up/down extend, Space",
		"          stages them, V or Esc cancels",
		"  L       stage just the line under the cursor",
		"  r       revert the hunk        n / p  next / previous hunk",
		"",
		"  On the staged side each of these unstages instead.",
		"",
		"───────────────────────────────────────────────────────────────",
		"Why letters, and not git gui's Ctrl keys",
		"",
		"  A terminal cannot deliver most of them. Ctrl-I is Tab, Ctrl-M is",
		"  Enter, and Ctrl-Enter arrives as Ctrl-J. The rest — Ctrl-A, -E,",
		"  -K, -U, -W, -D, -B, -F, -N, -P, -T — all edit text, so a Ctrl",
		"  command layer would break the composer.",
		"",
		"  Letters need no modifier at all, which also avoids macOS, where",
		"  Option is not Meta unless the terminal is set up for it. Only",
		"  Alt-1 to Alt-3 use it, and Tab does the same thing.",
		"",
		"  Press Esc to close.",
	}, "\n")
}
