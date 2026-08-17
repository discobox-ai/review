package ui

import (
	"fmt"
	"path/filepath"
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
	focusHistory // the selected commit's files, in place of the two above
	focusCommits // the branch's own commits
	focusDiff
	focusCommit
	focusCount
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
	history *fileList // the selected commit's files, shown in place of the two above
	commits *commitList
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
	// parent is the revision the scan was taken against, and the one every
	// diff taken from it has to use. Keeping it means moving the cursor does
	// not ask git for it again on every file.
	parent string
	// base is where the branch stands against the remote's default branch,
	// shown next to the branch name. Its Ref is empty when there is nothing
	// to measure against.
	base git.Base

	// diffs holds the text of every diff fetched since the last rescan, so
	// walking back over files already looked at costs nothing. gen counts the
	// scans: it is part of the key, so a diff can never outlive the scan it
	// was taken against. want is the diff the pane should be showing, which
	// decides whether one arriving is still the one to draw.
	diffs     map[diffID]string
	diffBytes int
	gen       int
	want      diffID
	// histories is the same idea for the file lists of the commits looked at
	// since the last rescan. A commit's contents cannot change under us, but
	// the branch can be rewritten, so they go with the scan all the same.
	histories map[string][]git.File

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

	// md is the full screen markdown reader, on screen while non-nil. It sits
	// over everything the way a dialog does, and takes the keys the same way.
	md *mdView

	// composeOpen is whether the commit message is on screen. Nothing opens it
	// but Ctrl-Enter, so the diff keeps the whole right column until the
	// moment you say you are writing a commit.
	composeOpen bool

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
		history: newFileList(git.SideCommit),
		commits: newCommitList(),
		diff:    newDiffView(),
		commit:  ta,
		// git-gui starts with the cursor in the message buffer; starting on
		// the first unstaged file is friendlier in a terminal, where it also
		// makes the single letter keys live straight away.
		focus:     focusWorkdir,
		context:   defaultContext,
		diffs:     map[diffID]string{},
		histories: map[string][]git.File{},
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
	scan git.Scan
	err  error
}

// diffID names a diff by everything that decides its text: the file, the side
// it is taken from, how much context, and the scan it belongs to. Two requests
// with the same key have the same answer, which is what makes them worth
// keeping.
type diffID struct {
	path    string
	side    git.Side
	context int
	commit  string // the commit being looked at, empty for the working tree
	gen     int
}

type diffMsg struct {
	file git.File
	key  diffID
	text string
	err  error
}

type opMsg struct {
	text   string
	err    error
	rescan bool
}

// historyMsg is the file list of a commit being looked at. It carries the hash
// it was asked for, so a list arriving after the cursor has moved on is
// dropped rather than shown against the wrong commit.
type historyMsg struct {
	hash  string
	gen   int
	files []git.File
	err   error
}

// markdownMsg is a file's text arriving for the markdown reader. It carries
// the scan generation so a reader is never opened over a window that has
// moved on to a different scan.
type markdownMsg struct {
	title   string
	gen     int
	text    string
	changed map[int]bool // source lines the diff touched, for the gutter marks
	err     error
}

// committedMsg is the result of the commit itself, kept apart from opMsg
// because what follows a commit — emptying the buffer, leaving amend mode —
// must happen only when one was really made.
type committedMsg struct {
	out string
	err error
}

func (m Model) rescanCmd() tea.Cmd {
	// git-gui's PARENT: while amending, the index is measured against HEAD's
	// parent, so the staged list shows the whole amended commit. Rescan works
	// it out along with everything else the scan needs.
	repo, amend := m.repo, m.amend
	return func() tea.Msg {
		scan, err := repo.Rescan(amend)
		return rescanMsg{scan: scan, err: err}
	}
}

func (m Model) diffCmd(f git.File, key diffID) tea.Cmd {
	repo, parent := m.repo, m.parent
	return func() tea.Msg {
		var text string
		var err error
		if key.commit != "" {
			text, err = repo.CommitDiff(key.commit, f, key.context)
		} else {
			text, err = repo.Diff(f, key.side, key.context, parent)
		}
		return diffMsg{file: f, key: key, text: text, err: err}
	}
}

// historyCmd loads the files a commit changed.
func (m Model) historyCmd(hash string) tea.Cmd {
	repo, gen := m.repo, m.gen
	return func() tea.Msg {
		files, err := repo.CommitFiles(hash)
		return historyMsg{hash: hash, gen: gen, files: files, err: err}
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
		if m.md != nil {
			m.md.resize(m.width, m.height)
		}
		return m, nil

	case rescanMsg:
		return m.onRescan(msg)

	case diffMsg:
		return m.onDiff(msg)

	case historyMsg:
		return m.onHistory(msg)

	case markdownMsg:
		return m.onMarkdown(msg)

	case committedMsg:
		return m.onCommitted(msg)

	case opMsg:
		if msg.err != nil {
			m.setError(msg.text + ": " + firstLine(msg.err.Error()))
			m.dialog = messageDialog(msg.text+" failed", msg.err.Error(), true)
			return m, m.rescanCmd()
		}
		m.setOK(msg.text)
		if msg.rescan {
			// The rescan refreshes the diff itself once it lands, which is
			// the only point the diff can be right again.
			return m, m.rescanCmd()
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
	m.branch = msg.scan.Branch
	m.files = msg.scan.Files
	m.hasHEAD = msg.scan.HasHEAD
	m.merging = msg.scan.Merging
	m.parent = msg.scan.Parent
	m.base = msg.scan.Base

	// The working tree has moved under every diff taken against the last scan,
	// so they all go. Counting the scan retires the ones still in flight.
	m.gen++
	m.diffs, m.diffBytes = map[diffID]string{}, 0
	m.histories = map[string][]git.File{}

	var work, idx []git.File
	for _, f := range msg.scan.Files {
		if f.State.Unstaged() {
			work = append(work, f)
		}
		if f.State.Staged() {
			idx = append(idx, f)
		}
	}
	m.workdir.setFiles(work)
	m.index.setFiles(idx)
	m.commits.setCommits(msg.scan.Commits)

	// The cursor cannot stay in a pane that is no longer there: unstaging the
	// last file takes the message buffer away, and a commit that was pushed or
	// rewritten takes the history with it.
	if !m.paneVisible(m.focus) {
		m.focus = focusDiff
		m.commit.Blur()
	}

	m.loadMessageChain(msg.scan.Merge)

	m.layout()
	cmd := m.onCommitSelected()
	return m, cmd
}

// browsing reports whether the cursor in the branch history is on a commit
// rather than on the working tree. It is the switch the whole left column
// turns on: the two working lists give way to that commit's files.
func (m *Model) browsing() bool {
	_, ok := m.commits.selected()
	return ok
}

// showCommits reports whether the branch has any commits of its own to list.
// With none — level with the base, or no base at all — the pane is not there.
func (m *Model) showCommits() bool { return len(m.commits.commits) > 0 }

// onHistory takes delivery of a commit's file list, and shows it only if the
// cursor is still on that commit.
func (m Model) onHistory(msg historyMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.gen {
		return m, nil
	}
	c, ok := m.commits.selected()
	if !ok || c.Hash != msg.hash {
		return m, nil
	}
	if msg.err != nil {
		m.setError("Reading " + c.Short + ": " + firstLine(msg.err.Error()))
		return m, nil
	}
	m.histories[msg.hash] = msg.files
	m.history.label = "Files in " + c.Short
	m.history.setFiles(msg.files)
	m.layout()
	cmd := m.refreshDiffCmd()
	return m, cmd
}

// onDiff takes delivery of a diff. Moving the cursor quickly leaves several in
// flight at once, and they need not come back in the order they were asked
// for, so what arrives is filed under its own key and drawn only if it is
// still the one the pane is waiting for. The rest are not wasted: the cursor
// coming back to them finds them already here.
func (m Model) onDiff(msg diffMsg) (tea.Model, tea.Cmd) {
	if msg.key.gen != m.gen {
		// Taken against a scan that has since been replaced.
		return m, nil
	}
	if msg.err != nil {
		if msg.key == m.want {
			m.diff.setMessage("* " + msg.err.Error())
		}
		return m, nil
	}
	m.keepDiff(msg.key, msg.text)
	if msg.key == m.want {
		m.diff.setPatch(msg.file, msg.key, msg.text)
	}
	return m, nil
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

// diffBudget caps what is kept between rescans. A diff is text, and a few
// hundred of them are nothing, but a tree with thousands of changed files
// would mount up: past the budget the lot is dropped and filled again.
const diffBudget = 8 << 20

// keepDiff files a diff under its key, within the budget.
func (m *Model) keepDiff(key diffID, text string) {
	if _, dup := m.diffs[key]; dup {
		return
	}
	if m.diffBytes+len(text) > diffBudget {
		m.diffs = map[diffID]string{}
		m.diffBytes = 0
	}
	m.diffs[key] = text
	m.diffBytes += len(text)
}

// refreshDiffCmd brings the diff pane up to date with whatever the active list
// points at, and runs git only when it has to. The diff already on screen is
// left alone — scrolled where it was, and all — and one already taken since
// the last rescan is redrawn from what was kept. Only a file not looked at yet
// costs a git.
func (m *Model) refreshDiffCmd() tea.Cmd {
	list, side := m.activeList()
	var f *git.File
	if list != nil {
		f = list.current()
	}
	if f == nil {
		m.want = diffID{}
		m.diff.clear()
		return nil
	}
	key := diffID{path: f.Path, side: side, context: m.context, gen: m.gen}
	if c, ok := m.commits.selected(); ok {
		key.commit = c.Hash
	}
	m.want = key
	if m.diff.shown == key {
		return nil
	}
	if text, ok := m.diffs[key]; ok {
		m.diff.setPatch(*f, key, text)
		m.diff.clamp()
		return nil
	}
	return m.diffCmd(*f, key)
}

// activeList returns the list that owns the current diff. When the diff pane
// or commit buffer has focus, the last list that was focused wins.
func (m *Model) activeList() (*fileList, git.Side) {
	if m.browsing() {
		return m.history, git.SideCommit
	}
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

	// The markdown reader owns the screen, so it owns the keys.
	if m.md != nil {
		return m.markdownKey(msg)
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
		return m.doCommit()
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
	case key.Matches(msg, m.keys.FocusCommits):
		if !m.showCommits() {
			m.setError("This branch has no commits of its own")
			return m, nil
		}
		return m.setFocus(focusCommits)
	case key.Matches(msg, m.keys.FocusCommit):
		// Alt-4 means "take me to the message", so it brings it up if it is
		// not already. That is not a second way in behind your back: the
		// message stays hidden until you ask for it, and this is asking.
		// Alt-5 refuses when the branch has no commits because that pane has
		// nothing to show; this one always has something to show.
		m.composeOpen = true
		return m.setFocus(focusCommit)
	case key.Matches(msg, m.keys.NextPane):
		return m.setFocus(m.stepPane(1))
	case key.Matches(msg, m.keys.PrevPane):
		return m.setFocus(m.stepPane(-1))
	}

	// In the message buffer every letter is text, so the command letters
	// below are not reachable from it: Tab or Esc leaves first.
	if m.focus == focusCommit {
		return m.commitKey(msg)
	}

	// U acts on the commit selected in the branch history, from wherever you
	// press it, so the key means one thing everywhere. It is taken before the
	// guard below because it is the one change history is allowed to make.
	if key.Matches(msg, m.keys.PopKey) {
		return m.doPop()
	}

	// History is read only: the commands that change the working tree or the
	// index have nothing to act on while an earlier commit is on screen.
	if m.browsing() {
		switch {
		case key.Matches(msg, m.keys.Toggle),
			key.Matches(msg, m.keys.StageKey),
			key.Matches(msg, m.keys.StageChangedKey),
			key.Matches(msg, m.keys.UnstageKey),
			key.Matches(msg, m.keys.RevertKey),
			key.Matches(msg, m.keys.Delete),
			key.Matches(msg, m.keys.StageLine),
			key.Matches(msg, m.keys.RevertHunk):
			c, _ := m.commits.selected()
			m.setError("Looking at " + c.Short + "; select the working tree to change anything")
			return m, nil
		}
	}

	// Commands. A letter each, live in the file lists and the diff.
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
	case key.Matches(msg, m.keys.EditKey):
		return m.doEdit()
	case key.Matches(msg, m.keys.MarkdownKey):
		return m.doMarkdown()
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

	switch m.focus {
	case focusDiff:
		return m.diffKey(msg)
	case focusCommits:
		return m.commitsKey(msg)
	}
	return m.listKey(msg)
}

// commitsKey drives the branch history. Moving the cursor changes which
// version of the branch the rest of the window is showing, so every move ends
// in a reload of the pane above and the diff beside it.
func (m Model) commitsKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	moved := true
	switch {
	case key.Matches(msg, m.keys.Up):
		if m.commits.cursor <= 0 {
			if t, ok := m.paneAbove(m.focus); ok {
				return m.enterPane(t, entryBottom)
			}
			return m, nil
		}
		m.commits.move(-1)
	case key.Matches(msg, m.keys.Down):
		if m.commits.cursor >= m.commits.rows()-1 {
			return m, nil
		}
		m.commits.move(1)
	case key.Matches(msg, m.keys.Left):
		if t, ok := m.paneLeft(focusDiff); ok {
			return m.enterPane(t, entryKeep)
		}
		return m, nil
	case key.Matches(msg, m.keys.Right):
		if t, ok := m.paneRight(m.focus); ok {
			return m.enterPane(t, entryKeep)
		}
		return m, nil
	case key.Matches(msg, m.keys.PageUp):
		m.commits.move(-max(m.commits.height-1, 1))
	case key.Matches(msg, m.keys.PageDown):
		m.commits.move(max(m.commits.height-1, 1))
	case key.Matches(msg, m.keys.Home):
		m.commits.moveTo(0)
	case key.Matches(msg, m.keys.End):
		m.commits.moveTo(m.commits.rows() - 1)
	default:
		moved = false
	}
	if !moved {
		return m, nil
	}
	cmd := m.onCommitSelected()
	return m, cmd
}

// onCommitSelected reloads the left column and the diff for whatever the
// history cursor now points at: a commit's own files, or the working tree.
func (m *Model) onCommitSelected() tea.Cmd {
	m.layout()
	if c, ok := m.commits.selected(); ok {
		files, known := m.histories[c.Hash]
		if !known {
			// Nothing to show until the list arrives; the diff would otherwise
			// sit there belonging to whatever was on screen before.
			m.history.label = ""
			m.history.setFiles(nil)
			m.diff.clear()
			return m.historyCmd(c.Hash)
		}
		m.history.label = "Files in " + c.Short
		m.history.setFiles(files)
		m.layout()
		return m.refreshDiffCmd()
	}
	m.history.label = ""
	m.history.setFiles(nil)
	return m.refreshDiffCmd()
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
// leftPanes is the left column from top to bottom, holding only what is on
// screen: the working lists or the commit's files in their place, and the
// branch history under them when the branch has commits of its own.
func (m *Model) leftPanes() []focusArea {
	var out []focusArea
	if m.browsing() {
		out = append(out, focusHistory)
	} else {
		out = append(out, focusWorkdir, focusIndex)
	}
	if m.showCommits() {
		out = append(out, focusCommits)
	}
	return out
}

// paneAbove is a method because the left column's contents vary.
func (m *Model) paneAbove(f focusArea) (focusArea, bool) {
	if f == focusCommit {
		return focusDiff, true
	}
	panes := m.leftPanes()
	for i, p := range panes {
		if p == f && i > 0 {
			return panes[i-1], true
		}
	}
	return f, false
}

func (m *Model) paneBelow(f focusArea) (focusArea, bool) {
	if f == focusDiff {
		if m.composeOpen {
			return focusCommit, true
		}
		return f, false
	}
	panes := m.leftPanes()
	for i, p := range panes {
		if p == f && i+1 < len(panes) {
			return panes[i+1], true
		}
	}
	return f, false
}

// paneVisible reports whether a pane is on screen at all, which decides what
// Tab and the Alt keys can reach.
func (m *Model) paneVisible(f focusArea) bool {
	switch f {
	case focusWorkdir, focusIndex:
		return !m.browsing()
	case focusHistory:
		return m.browsing()
	case focusCommits:
		return m.showCommits()
	case focusCommit:
		return m.composeOpen
	}
	return true
}

// stepPane is Tab and Shift-Tab, walking over whatever is not on screen.
func (m *Model) stepPane(delta int) focusArea {
	f := m.focus
	for i := 0; i < int(focusCount); i++ {
		f = (f + focusArea(delta) + focusCount) % focusCount
		if m.paneVisible(f) {
			return f
		}
	}
	return m.focus
}

// paneLeft returns to whichever file list owns the diff currently on show,
// rather than to a fixed pane. git-gui tracks the same thing as
// current_diff_side, so leaving the diff and coming back keeps you on the file
// you were looking at.
func (m *Model) paneLeft(f focusArea) (focusArea, bool) {
	switch f {
	case focusDiff, focusCommit:
		if m.browsing() {
			return focusHistory, true
		}
		if _, side := m.activeList(); side == git.SideIndex {
			return focusIndex, true
		}
		return focusWorkdir, true
	}
	return f, false
}

// paneRight sends both lists to the diff: it is the pane you want next from
// either one. The commit buffer is below the diff, reached with down.
// paneRight sends the lists to the diff, and the branch history to whatever it
// has selected: the commit's files when it is on a commit, the diff when it is
// on the working tree. Right therefore chains down the column — history, then
// files, then the diff — which is the only way into the files pane by arrow,
// since leaving the history upwards means passing its top row and so leaving
// the commit behind.
func (m *Model) paneRight(f focusArea) (focusArea, bool) {
	switch f {
	case focusCommits:
		if m.browsing() {
			return focusHistory, true
		}
		return focusDiff, true
	case focusWorkdir, focusIndex, focusHistory:
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
	case focusWorkdir, focusIndex, focusHistory:
		l := m.workdir
		switch f {
		case focusIndex:
			l = m.index
		case focusHistory:
			l = m.history
		}
		switch e {
		case entryTop:
			l.moveTo(0)
		case entryBottom:
			l.moveTo(len(l.files) - 1)
		}
	case focusCommits:
		switch e {
		case entryTop:
			m.commits.moveTo(0)
		case entryBottom:
			m.commits.moveTo(m.commits.rows() - 1)
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
	// A selection belongs to the pane it was made in and to the moment it was
	// made in, so leaving that pane ends it.
	if f != focusDiff {
		m.diff.clearVisual()
	}
	if f != focusWorkdir {
		m.workdir.clearVisual()
	}
	if f != focusIndex {
		m.index.clearVisual()
	}
	m.layout()
	if f == focusCommit {
		m.commit.Focus()
		return m, textarea.Blink
	}
	m.commit.Blur()
	if f == focusWorkdir || f == focusIndex {
		cmd := m.refreshDiffCmd()
		return m, cmd
	}
	return m, nil
}

func (m Model) listKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	list, side := m.activeList()
	if list == nil {
		return m, nil
	}
	// While a selection is open the cursor stays inside the list: running off
	// the end into the next pane would abandon the selection mid-gesture. The
	// diff pane holds its own selection the same way.
	if list.visual() {
		switch {
		case key.Matches(msg, m.keys.Up):
			list.move(-1)
			cmd := m.refreshDiffCmd()
			return m, cmd
		case key.Matches(msg, m.keys.Down):
			list.move(1)
			cmd := m.refreshDiffCmd()
			return m, cmd
		case key.Matches(msg, m.keys.Cancel):
			list.clearVisual()
			return m, nil
		}
	}

	moved := true
	switch {
	case key.Matches(msg, m.keys.Up):
		// At the top of a list, carry on into the pane above.
		if list.cursor <= 0 {
			if t, ok := m.paneAbove(m.focus); ok {
				return m.enterPane(t, entryBottom)
			}
			return m, nil
		}
		list.move(-1)
	case key.Matches(msg, m.keys.Down):
		if list.cursor >= len(list.files)-1 {
			if t, ok := m.paneBelow(m.focus); ok {
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
		if t, ok := m.paneRight(m.focus); ok {
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
		// the staged list. With a selection open it takes the whole of it.
		if side == git.SideIndex {
			return m.doUnstage()
		}
		return m.doStage()
	case key.Matches(msg, m.keys.Visual):
		list.toggleVisual()
		moved = false
	default:
		moved = false
	}
	if moved {
		cmd := m.refreshDiffCmd()
		return m, cmd
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
			if t, ok := m.paneAbove(m.focus); ok {
				return m.enterPane(t, entryBottom)
			}
			return m, nil
		}
		m.diff.move(-1)
	case key.Matches(msg, m.keys.Down):
		if m.diff.cursor >= len(m.diff.lines)-1 {
			if t, ok := m.paneBelow(m.focus); ok {
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
		if t, ok := m.paneRight(m.focus); ok {
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
	cmd := m.refreshDiffCmd()
	return m, cmd
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
	list.clearVisual()
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
	list.clearVisual()
	repo, parent := m.repo, m.parent
	return m, op(fmt.Sprintf("Unstaged %s", plural(len(paths), "file", "files")), true,
		func() error { return repo.Unstage(paths, parent) })
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

// fileInView is the file the window is about right now — the one the diff is
// showing — and the commit it is being looked at in, empty for the working
// tree. It is what both e and m act on, so each means the same thing from
// either list and from the diff itself.
func (m *Model) fileInView() (*git.File, string, git.Side) {
	list, side := m.activeList()
	f := list.current()
	var commit string
	if c, ok := m.commits.selected(); ok {
		commit = c.Hash
	}
	return f, commit, side
}

// doEdit opens the file in view in the user's editor, handing it the terminal
// until it exits. While an earlier commit is on screen the editor gets that
// commit's version of the file, as a read-only copy: history reads well in an
// editor, it just cannot be written there.
func (m Model) doEdit() (tea.Model, tea.Cmd) {
	f, commit, _ := m.fileInView()
	if f == nil {
		m.setError("No file to edit")
		return m, nil
	}
	editor, tmp, err := m.repo.EditAt(commit, f.Path)
	if err != nil {
		m.setError(err.Error())
		return m, nil
	}
	return m, tea.Exec(editor, editorClosed(m.repo, f.Path, tmp))
}

// editorClosed is what happens once the editor exits. For a working tree file
// the rescan is not optional: the editor may well have changed the file, and
// the scan is how the lists and the diff catch up with it. A commit's file
// was only a read-only copy, so there is nothing to rescan — only the copy to
// sweep up.
//
// It is a function of its own rather than a closure inside doEdit because
// bubbletea runs it in the runtime rather than through Update, which puts it
// out of reach of a test driving the model directly.
func editorClosed(repo *git.Repo, path, tmp string) func(error) tea.Msg {
	return func(err error) tea.Msg {
		repo.RemoveTempCopy(tmp)
		if err != nil {
			return opMsg{text: "Edit " + path, err: err}
		}
		if tmp != "" {
			return opMsg{text: "Closed " + path}
		}
		return opMsg{text: "Edited " + path, rescan: true}
	}
}

// isMarkdown reports whether a path names a markdown file, which is the one
// kind the reader knows how to render.
func isMarkdown(path string) bool {
	switch strings.ToLower(filepath.Ext(path)) {
	case ".md", ".markdown", ".mdown", ".mkd":
		return true
	}
	return false
}

// doMarkdown opens the file in view in the full screen markdown reader. It
// reads whichever version the window is showing: the working tree's, or the
// selected commit's — reading is the one thing history is happy to allow.
func (m Model) doMarkdown() (tea.Model, tea.Cmd) {
	f, commit, side := m.fileInView()
	if f == nil {
		m.setError("No file to read")
		return m, nil
	}
	if !isMarkdown(f.Path) {
		m.setError(f.Path + " is not a markdown file")
		return m, nil
	}
	title := f.Path
	if c, ok := m.commits.selected(); ok {
		title += " @ " + c.Short
	}
	repo, gen, parent, file := m.repo, m.gen, m.parent, *f
	return m, func() tea.Msg {
		text, err := repo.FileContent(commit, file.Path)
		if err != nil {
			return markdownMsg{title: title, gen: gen, err: err}
		}
		// The same diff the pane shows, taken again at minimal context, marks
		// the changed lines in the margin. Failing to take it only costs the
		// marks, never the text.
		var diff string
		if commit != "" {
			diff, _ = repo.CommitDiff(commit, file, 1)
		} else {
			diff, _ = repo.Diff(file, side, 1, parent)
		}
		var changed map[int]bool
		if diff != "" {
			changed = git.ParsePatch(diff).ChangedNewLines()
		}
		return markdownMsg{title: title, gen: gen, text: text, changed: changed}
	}
}

// onMarkdown puts the reader up once the text arrives, unless the window has
// been rescanned out from under the request.
func (m Model) onMarkdown(msg markdownMsg) (tea.Model, tea.Cmd) {
	if msg.gen != m.gen {
		return m, nil
	}
	if msg.err != nil {
		m.setError(msg.title + ": " + firstLine(msg.err.Error()))
		return m, nil
	}
	m.md = newMdView(msg.title, msg.text, msg.changed, m.width, m.height)
	return m, nil
}

// markdownKey drives the reader: scrolling, and the keys that put it away.
func (m Model) markdownKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m.doQuit()
	case key.Matches(msg, m.keys.Cancel),
		key.Matches(msg, m.keys.QuitKey),
		key.Matches(msg, m.keys.MarkdownKey):
		m.md = nil
		return m, nil
	case key.Matches(msg, m.keys.Up):
		m.md.move(-1)
	case key.Matches(msg, m.keys.Down):
		m.md.move(1)
	case key.Matches(msg, m.keys.PageUp):
		m.md.move(-max(m.md.body()-1, 1))
	case key.Matches(msg, m.keys.PageDown):
		m.md.move(max(m.md.body()-1, 1))
	case key.Matches(msg, m.keys.Home):
		m.md.moveTo(0)
	case key.Matches(msg, m.keys.End):
		m.md.moveTo(len(m.md.lines))
	case key.Matches(msg, m.keys.NextHunk):
		m.md.jump(1)
	case key.Matches(msg, m.keys.PrevHunk):
		m.md.jump(-1)
	}
	return m, nil
}

// closeCompose puts the message away, keeping what was typed for next time.
// Tab only moves the cursor off it; Esc is what dismisses it.
func (m Model) closeCompose() (tea.Model, tea.Cmd) {
	m.composeOpen = false
	return m.setFocus(focusDiff)
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

// commitKey drives the message buffer. It is a full text editor, so every
// readline key belongs to it and every letter is text. Only Esc and an arrow
// at the very edge of the text walk out, which keeps h/j/k/l typable.
func (m Model) commitKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if key.Matches(msg, m.keys.Cancel) {
		return m.closeCompose()
	}
	info := m.commit.LineInfo()
	atFirstLine := m.commit.Line() == 0 && info.RowOffset == 0
	switch msg.String() {
	case "up":
		if atFirstLine {
			if t, ok := m.paneAbove(focusCommit); ok {
				return m.enterPane(t, entryBottom)
			}
		}
	case "left":
		if atFirstLine && info.ColumnOffset == 0 {
			if t, ok := m.paneLeft(focusCommit); ok {
				return m.enterPane(t, entryKeep)
			}
		}
	}
	var cmd tea.Cmd
	m.commit, cmd = m.commit.Update(msg)
	return m, cmd
}

// commitProblem reports why the commit cannot be made, or "" when it can.
// git-gui refuses the same three cases.
// The three are short because they are read in the status bar, which has one
// line and shares it with the file counts.
//
// They are checked in the order you would have to fix them: something to
// commit, then something to commit it as. Reporting an empty message to
// somebody who has staged nothing would send them to the message buffer, which
// is not even on screen, to solve the wrong problem.
func (m Model) commitProblem() string {
	for _, f := range m.files {
		if f.State.Unmerged() {
			return "Unmerged: " + f.Path + " has conflicts to resolve and stage first"
		}
	}
	if !m.committable() {
		return "Nothing staged to commit"
	}
	if m.messageEmpty() {
		return "Enter a commit message"
	}
	return ""
}

// messageEmpty reports whether the buffer holds nothing worth committing.
func (m *Model) messageEmpty() bool { return strings.TrimSpace(m.commit.Value()) == "" }

// doCommit is the whole of Ctrl-Enter, which escalates one step per press:
//
//  1. the message is not up      → put it up and put the cursor in it
//  2. it is up but not focused   → put the cursor back in it
//  3. it is up and focused       → ask, and then commit on y
//
// Each press does one thing, and the press that commits is always made from
// the message itself, so there is no way to commit without having just looked
// at what you wrote.
func (m Model) doCommit() (tea.Model, tea.Cmd) {
	if !m.composeOpen {
		m.composeOpen = true
		return m.setFocus(focusCommit)
	}
	if m.focus != focusCommit {
		return m.setFocus(focusCommit)
	}
	if problem := m.commitProblem(); problem != "" {
		// The status bar rather than a dialog: the message is on screen and
		// the cursor is already in it, so a box would cover the one thing that
		// needs doing.
		m.setError(problem)
		return m, nil
	}
	question := fmt.Sprintf("Commit %s to %s?",
		plural(len(m.index.files), "staged file", "staged files"), m.branch)
	if m.amend {
		question = "Rewrite the last commit on " + m.branch + "?"
	}
	repo, amend := m.repo, m.amend
	message := normalizeMessage(m.commit.Value())
	m.dialog = confirmDialog("Commit", question, func(string) tea.Cmd {
		return func() tea.Msg {
			out, err := repo.Commit(message, amend)
			if err != nil {
				return committedMsg{err: err}
			}
			// The message is in the commit now, so the stashed copies go.
			_ = repo.RemoveStateFile(git.MsgFile)
			_ = repo.RemoveStateFile(git.BackupFile)
			return committedMsg{out: out}
		}
	})
	return m, nil
}

// onCommitted empties the buffer once the commit has actually been made, and
// leaves it alone when it has not, so nothing typed is lost to a failure.
func (m Model) onCommitted(msg committedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil {
		m.setError("Commit: " + firstLine(msg.err.Error()))
		m.dialog = messageDialog("Commit failed", msg.err.Error(), true)
		return m, m.rescanCmd()
	}
	m.setMessage("")
	m.bckExists = false
	m.amend = false
	m.savedMsg = ""
	m.composeOpen = false
	if m.focus == focusCommit {
		m.focus = focusDiff
		m.commit.Blur()
	}
	m.layout()
	m.setOK("Created commit: " + firstLine(msg.out))
	return m, m.rescanCmd()
}

// doPop takes the selected commit and everything after it off the branch,
// leaving their changes staged. Selecting the oldest of a run and popping is
// how you undo the whole run at once.
func (m Model) doPop() (tea.Model, tea.Cmd) {
	c, ok := m.commits.selected()
	if !ok {
		m.setError("Select a commit first; the working tree has nothing to pop")
		return m, nil
	}
	repo := m.repo
	parent, ok := repo.Parent(c.Hash)
	if !ok {
		m.setError(c.Short + " has no parent to reset to")
		return m, nil
	}
	// The cursor sits one row below the working tree for the newest commit, so
	// its position is exactly how many commits come off.
	popped := plural(m.commits.cursor, "commit", "commits")
	m.dialog = confirmDialog("Reset",
		fmt.Sprintf("Pop %s off %s, back to %s?\n\n"+
			"Everything they contained stays staged, and the working tree is "+
			"not touched. Nothing is lost: the commits remain in the reflog.",
			popped, m.branch, abbrevHash(parent)),
		func(string) tea.Cmd {
			return op("Popped "+popped+" off "+m.branch, true,
				func() error { return repo.ResetSoft(parent) })
		})
	return m, nil
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
	if m.leftCollapsed() {
		leftW, rightW = 0, m.width
	}

	// The branch history takes the bottom of the left column, no more than a
	// third of it and no more than it has to show, so a single commit does not
	// leave a hole. What is left goes to the working lists, or to the selected
	// commit's files in their place.
	listsH := body
	if m.showCommits() {
		commitsH := min(max(body/3, 4), m.commits.rows()+1)
		listsH = max(body-commitsH, 3)
		m.commits.width, m.commits.height = leftW, max(body-listsH-1, 1)
	}
	if m.browsing() {
		m.history.width, m.history.height = leftW, max(listsH-1, 1)
	} else {
		workH := listsH / 2
		stagedH := listsH - workH
		m.workdir.width, m.workdir.height = leftW, max(workH-1, 1)
		m.index.width, m.index.height = leftW, max(stagedH-1, 1)
	}

	// The message sits under the diff, and only once it has been opened.
	commitH := 0
	if m.composeOpen {
		commitH = min(10, max(body/2, 5))
	}
	diffH := max(body-commitH, 3)
	m.diff.width, m.diff.height = rightW, max(diffH-1, 1)
	if commitH > 0 {
		// One column narrower than the pane: viewCommitArea insets it to line
		// up with the title.
		m.commit.SetWidth(max(rightW-1, 10))
		m.commit.SetHeight(max(commitH-1, 2))
	}

	m.workdir.clamp()
	m.index.clamp()
	m.history.clamp()
	m.commits.clamp()
	m.diff.clamp()
}

// narrowWidth is where the window stops being wide enough for both columns.
// Below it a pane roughly a third of the screen wide leaves the diff too
// narrow to read code in, which is the one thing the window is for.
const narrowWidth = 100

// leftCollapsed reports whether the left column gives up its space entirely.
//
// It does so only on a narrow window, and only while the focus is on the right
// hand column, so moving right reads as "show me this properly" and moving
// left brings the lists straight back. On a wide window nothing moves, because
// nothing needs to.
func (m *Model) leftCollapsed() bool {
	if m.width >= narrowWidth {
		return false
	}
	return m.focus == focusDiff || m.focus == focusCommit
}

// committable reports whether there is anything to make a commit out of:
// something staged, an amend, or a merge being concluded. An amend with
// nothing staged is still a commit.
//
// This is no longer what decides whether the message is on screen — Ctrl-Enter
// decides that — only whether a commit could be made at all.
func (m *Model) committable() bool {
	return len(m.index.files) > 0 || m.amend || m.merging
}

// View renders the whole window.
func (m Model) View() string {
	if m.quit {
		return ""
	}
	if !m.ready {
		return "Loading…"
	}

	var stack []string
	if m.browsing() {
		stack = append(stack, m.history.view(m.st, m.focus == focusHistory))
	} else {
		stack = append(stack,
			m.workdir.view(m.st, m.focus == focusWorkdir),
			m.index.view(m.st, m.focus == focusIndex),
		)
	}
	if m.showCommits() {
		stack = append(stack, m.commits.view(m.st, m.focus == focusCommits))
	}
	left := lipgloss.JoinVertical(lipgloss.Left, stack...)
	right := m.diff.view(m.st, m.focus == focusDiff)
	if m.composeOpen {
		right = lipgloss.JoinVertical(lipgloss.Left, right, m.viewCommitArea())
	}

	middle := right
	if !m.leftCollapsed() {
		bodyH := max(m.height-2, 4)
		sep := strings.TrimRight(strings.Repeat("│\n", bodyH), "\n")
		middle = lipgloss.JoinHorizontal(lipgloss.Top, left, m.st.sep.Render(sep), right)
	}

	view := lipgloss.JoinVertical(lipgloss.Left,
		m.viewBranchBar(), middle, m.viewStatusBar())

	if m.dialog != nil {
		return m.dialog.view(m.st, m.width, m.height)
	}
	if m.md != nil {
		return m.md.view(m.st)
	}
	return view
}

// viewCommitArea draws the message buffer under the diff: a title saying which
// kind of commit this will be, and the field itself. git-gui puts a column of
// buttons beside it — Rescan, Stage Changed, Sign Off, Commit, Push — but each
// was only a label for a key that already exists, so the field takes the whole
// width instead.
func (m Model) viewCommitArea() string {
	label := "Commit Message"
	switch {
	case m.amend && !m.hasHEAD:
		label = "Amended Initial Commit Message"
	case m.amend && m.merging:
		label = "Amended Merge Commit Message"
	case m.amend:
		label = "Amend Last Commit"
	case m.merging:
		label = "Merge Commit Message"
	case !m.hasHEAD:
		label = "Initial Commit Message"
	}
	style := m.st.titleCommit
	if m.focus != focusCommit {
		style = m.st.titleDim
	}
	right := plural(len(m.index.files), "file", "files") + " staged"
	if m.amend && len(m.index.files) == 0 {
		right = "amending"
	}
	header := renderTitle(style, label, right, m.diff.width)
	// The field is inset one column so the text starts under its own title
	// rather than against the separator.
	field := lipgloss.NewStyle().PaddingLeft(1).Render(m.commit.View())
	return lipgloss.JoinVertical(lipgloss.Left, header, field)
}

func (m Model) viewBranchBar() string {
	repo := m.repo.Name()
	left := m.st.branchLabel.Render("Current Branch: ") + m.st.branchBar.Render(m.branch)
	if m.amend {
		left += "  " + m.st.statusWA.Render("[amending]")
	}
	right := m.st.dimText.Render(repo + "  ·  F1 help  ·  Ctrl-Q quit")

	// The base is the first thing dropped when the bar will not hold all
	// three: the branch is where you are and the right hand side is the way
	// out, where the base is context you can do without.
	if base := m.viewBase(); base != "" {
		if full := left + base; m.width-lipgloss.Width(full)-lipgloss.Width(right) >= 1 {
			left = full
		}
	}

	gapW := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gapW < 1 {
		return truncateANSI(left, m.width)
	}
	return left + strings.Repeat(" ", gapW) + right
}

// viewBase is the merge base segment of the branch bar: the remote branch this
// one is measured against, the commit they last had in common, and how far
// each has moved since. Behind is called out in the warning colour when there
// is anything to catch up on, since that is the half you can act on.
func (m Model) viewBase() string {
	b := m.base
	if b.Ref == "" {
		return ""
	}
	s := m.st.dimText.Render("  ·  ") + m.st.branchLabel.Render("base: ") + b.Ref
	if b.Commit != "" {
		s += m.st.dimText.Render(" " + b.Commit)
	}
	s += " " + m.st.dimText.Render("↑"+itoa(b.Ahead))
	behind := "↓" + itoa(b.Behind)
	if b.Behind > 0 {
		return s + " " + m.st.statusWA.Render(behind)
	}
	return s + " " + m.st.dimText.Render(behind)
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

// visualHint is the status line while a file selection is open, matching the
// one the diff pane shows for a line selection.
func visualHint(n int, verb string) string {
	return fmt.Sprintf("VISUAL  %s · ↑/↓ extend · Space %s · V or Esc cancel",
		plural(n, "file", "files"), verb)
}

func (m Model) hints() string {
	switch m.focus {
	case focusCommits:
		if _, ok := m.commits.selected(); ok {
			return "↑/↓ pick a commit · → its files · U pop it and everything after · ? keys"
		}
		return "↑/↓ pick a commit to look at · → the diff · ? keys"
	case focusHistory:
		c, _ := m.commits.selected()
		return "Looking at " + c.Short + " · → the diff · U pop it off · Alt-5 back to the working tree"
	case focusWorkdir:
		if m.workdir.visual() {
			return visualHint(m.workdir.selected(), "stage")
		}
		return "Space stage · s stage · a stage all · v revert · V select · e amend · Ctrl-Enter commit · ? keys"
	case focusIndex:
		if m.index.visual() {
			return visualHint(m.index.selected(), "unstage")
		}
		return "Space unstage · u unstage · a stage all · V select · e amend · Ctrl-Enter commit · ? keys"
	case focusDiff:
		if m.diff.visual() {
			n := 1 + max(m.diff.cursor, m.diff.anchor) - min(m.diff.cursor, m.diff.anchor)
			return fmt.Sprintf("VISUAL LINE  %s · ↑/↓ extend · Space stage · V or Esc cancel",
				plural(n, "line", "lines"))
		}
		hint := "Space stage hunk · V select lines · L stage line · r revert · n/p hunk · -/= context"
		if c, ok := m.commits.selected(); ok {
			// None of the staging keys do anything against history.
			hint = "Looking at " + c.Short + " · n/p hunk · -/= context · Alt-5 back to the working tree"
		}
		if m.leftCollapsed() {
			// The lists are off screen entirely, so what brings them back goes
			// first, where a narrow window will not truncate it away.
			hint = "← lists · " + hint
		}
		return hint
	default:
		// Every letter is text here, so ? and q are not offered.
		return "Ctrl-Enter commits · Esc closes · Tab leaves it open · F1 keys"
	}
}

func helpText() string {
	return strings.Join([]string{
		"Every command is one letter, pressed in a file list or the diff.",
		"",
		"    s  stage              u  unstage",
		"    a  stage all changed  v  revert changes",
		"    o  sign off           A  amend last commit",
		"    e  edit the file in $EDITOR",
		"    m  read a markdown file, rendered, full screen",
		"    R  rescan (also F5)   U  pop commits off this branch",
		"    -  /  =              less / more diff context",
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
		"  Ctrl-Enter escalates one step per press, from any pane:",
		"",
		"    1st   brings the message up and puts the cursor in it",
		"    2nd   asks \"Commit N staged files to <branch>?\"",
		"    y     makes the commit",
		"",
		"  Nothing else puts the message on screen: not staging, not an",
		"  amend, not a merge. Until you ask for it the diff has the",
		"  whole right column.",
		"",
		"  If the cursor has wandered off the message, Ctrl-Enter only",
		"  brings it back — so the press that asks is always made from",
		"  the message, with what you wrote in front of you.",
		"",
		"  Esc puts it away and keeps what you typed; Tab only moves the",
		"  cursor off it. Alt-4 goes to the message, bringing it up first",
		"  if it is not already there.",
		"",
		"  It is a full text field, so every readline key works: Ctrl-A,",
		"  Ctrl-E, Ctrl-K, Ctrl-U, Ctrl-W and the rest — and every letter",
		"  is text, so the command letters are not reachable until you",
		"  leave.",
		"",
		"  With nothing written it says so in the status bar rather than",
		"  in a box, and the cursor is already where the fix is typed. It",
		"  will not commit with nothing staged, or with a file still",
		"  unmerged, and says which in the same place.",
		"",
		"  Sign off with o and amend with A: both change what the buffer",
		"  holds. Whatever is typed survives quitting difftui.",
		"",
		"───────────────────────────────────────────────────────────────",
		"Panes",
		"",
		"  Tab / Shift-Tab  cycle",
		"  Alt-1 unstaged   Alt-2 staged   Alt-3 diff",
		"  Alt-4 message    Alt-5 this branch",
		"",
		"  Arrows or h/j/k/l move. Moving off the edge of a pane enters",
		"  the next:",
		"",
		"      Unstaged    │ Diff",
		"      Staged      │",
		"      ────────────┼─────────",
		"      This Branch │ Message",
		"",
		"  Right from either list opens the diff; left comes back to",
		"  whichever list the diff is showing, still on the same file.",
		"",
		"  Under 100 columns there is not room for both, so moving right",
		"  gives the diff the whole window and the left column folds",
		"  away. Left brings it back. Wider than that, nothing moves.",
		"  Down from the diff reaches the message, and up leaves it again",
		"  — but only from the first line, so the arrows stay typable.",
		"",
		"───────────────────────────────────────────────────────────────",
		"This Branch",
		"",
		"  When the branch is ahead of its base, a third pane lists the",
		"  commits it adds, newest first, under a row for the working",
		"  tree. That row is where everything above normally lives.",
		"",
		"  Pick a commit and the two file lists give way to what it",
		"  changed; the diff beside them is that commit's own change to",
		"  the file, against its parent. Pick the working tree again and",
		"  the lists come back.",
		"",
		"  Right walks down the chain: this branch, then the commit's",
		"  files, then the diff. It is the way in, since going up past",
		"  the top row means dropping the commit.",
		"",
		"  U pops the selected commit, and everything committed after",
		"  it, back off the branch. Their changes stay staged and the",
		"  working tree is not touched, so committing two things and",
		"  wanting both back is: select the older one, press U. It asks",
		"  first, and nothing is lost either way — the commits are still",
		"  in the reflog.",
		"",
		"  Otherwise history is read only. Nothing that would change the",
		"  tree or the index — s, u, a, v, D, L, r, Space — does",
		"  anything while an earlier commit is on screen; each says so",
		"  rather than acting. Reading is still allowed: e opens the",
		"  commit's version of the file as a read-only copy, and m reads",
		"  it in the markdown reader.",
		"",
		"───────────────────────────────────────────────────────────────",
		"On a file, beyond the commands above",
		"",
		"  e       open it in $EDITOR, or vi. The whole terminal goes to",
		"          the editor until it exits, and difftui rescans after,",
		"          since the file may have changed. From a commit the",
		"          editor gets that commit's version as a read-only copy,",
		"          so history can be read there but never rewritten.",
		"",
		"  m       read a markdown file, rendered, filling the screen —",
		"          the working tree's copy, or the selected commit's.",
		"          Arrows and PgUp/PgDn scroll; Esc, q or m again closes.",
		"          When the file has changes, the touched blocks carry a",
		"          ▍ marker in the margin and n/p hop between them.",
		"",
		"  Space   stage, or unstage from the staged list. The cursor",
		"          holds its row rather than following the file, so the",
		"          row becomes the next file down and Space three times",
		"          stages the next three.",
		"  V       select files, like vim: up/down extend, Space stages",
		"          the lot, V or Esc cancels",
		"  D       delete untracked files",
		"",
		"  s, u, v and D all take the selection when there is one, and",
		"  the file under the cursor when there is not.",
		"",
		"───────────────────────────────────────────────────────────────",
		"In the diff, beyond the commands above",
		"",
		"  Space   stage the hunk under the cursor",
		"  V       select whole lines, the same gesture as in the lists:",
		"          up/down extend, Space stages them, V or Esc cancels",
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
