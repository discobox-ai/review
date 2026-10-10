package ui

import (
	"errors"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/discobox-ai/review/internal/git"
	"github.com/discobox-ai/review/internal/review"
)

type focusArea int

const (
	focusReview  focusArea = iota // every file's diff, in one column
	focusHistory                  // the commits since the base, under the review
	focusCompose                  // the comment being written, under the review
	focusCount
)

const (
	defaultContext = git.DefaultContext
	minContext     = 1
	maxContext     = 50
)

// reloadInterval is how often the review file is checked for changes made
// outside discobox-review.
//
// It is polled rather than watched because polling one small file once a
// second costs nothing measurable and works the same on every filesystem a
// repository might be sitting on — including the network ones where a watch
// quietly never fires. A second is well inside the time it takes to read what
// an agent has written, so the update always looks immediate.
const reloadInterval = time.Second

// maxLexLines caps how much of one file is worth lexing.
//
// Highlighting is the one part of building the stack whose cost grows with the
// size of the change, so there has to be a limit somewhere. It is per file
// rather than over the review as a whole, because you read one file at a time:
// a review being large is no reason for the thirty line file in it to lose its
// colours. This used to be a budget over the whole review, and discobox-review's own
// source tripped it — every file went plain, and nothing said why.
const maxLexLines = 10000

// Model is the review window.
type Model struct {
	repo *git.Repo
	st   *styles
	keys keyMap

	width, height int
	ready         bool

	history *commitList
	view    *reviewView
	editor  textarea.Model

	focus focusArea
	zoom  bool

	// histOpen is the history unfolded under the review, rather than folded
	// to its title. It starts unfolded, and H and the button on its title
	// change it.
	histOpen bool

	// What is being reviewed. baseRef is what the user asked for, empty to
	// let discobox-review work one out; base is what that came to.
	branch  string
	baseRef string
	base    git.Base
	merging bool
	context int

	// basePinned is set once the base has been chosen for this session — on
	// the command line, or with b — and stops the saved review handing it
	// back to whatever it was measured from last time.
	basePinned bool

	// The working tree's own review — every change from the base — kept even
	// while a single commit is being read, since it is the space comments are
	// stored in and the thing approvals are about.
	work   []*reviewFile
	workBy map[string]*reviewFile
	state  map[string]git.State

	// commit is the commit being read, empty for the whole review. Its files
	// and the diffs that carry comments in and out of it are kept for as long
	// as the scan they were taken against.
	commit     string
	commits    map[string][]*reviewFile
	carries    map[string]map[string]*git.Patch
	loading    string // a commit whose diff has been asked for
	gen        int
	folds      map[string]bool // the folds the reader has opened or closed by hand
	rescanning bool

	// hideDone takes the files that are done with off the screen altogether,
	// leaving what is unapproved or still being argued about. Folding puts a
	// file out of the way; this puts it out of the review.
	hideDone bool

	// The review document, and enough of its file to notice somebody else
	// writing one.
	rv     *review.State
	stamp  git.Stamp
	author string
	rvErr  string

	// compose is the comment being written, nil when none is.
	compose *compose

	// count is the digits typed in front of a command, vim's way of saying
	// how many or which one. Only "go to line" uses it so far.
	count string

	// dark is what the terminal says its own background is. The diff bands are
	// backgrounds themselves, so they are the one part of the palette that has
	// to know. It starts true because a terminal that never answers is far
	// more likely to be dark than light.
	dark bool

	status   string
	statusOK bool
	statusWa bool
	statusEr bool

	dialog *dialog
	md     *mdView
	file   *fileView
	quit   bool

	// rects is where each pane was last drawn, title row included, so a click
	// can be resolved to the pane under it. layout fills it in, which keeps
	// the one arithmetic that decides both where a pane is drawn and where it
	// is clicked in a single place.
	rects [focusCount]rect

	// mouse is the text selection the mouse makes, and bar the scrollbar
	// being dragged. Both are pointers because the Model is copied on every
	// update and a gesture in progress has to survive that.
	mouse *mouseState
	bar   *barDrag
}

// compose is a comment being written, and where it will be filed when it is
// posted.
type compose struct {
	title  string
	thread string // the thread being replied to, empty to start a new one
	path   string
	side   string
	line   int
	span   int    // how many lines the remark covers, from a V selection
	at     int    // index into the file's diff lines of the last of them
	text   string // the first line as it read, kept with the thread
	origin *review.Origin
}

// New builds the initial model for a repository.
//
// base is the ref the review is measured from, or empty to take whatever the
// saved review names and, failing that, whatever discobox-review works out.
// author is the name the comments written here are filed under, which the
// caller has already resolved — the window never guesses it.
func New(repo *git.Repo, base, author string) Model {
	ta := textarea.New()
	ta.Placeholder = "Write a comment"
	ta.ShowLineNumbers = false
	ta.CharLimit = 0
	ta.Prompt = ""
	// The box is as tall as what has been written in it, counted in rows on
	// screen rather than in paragraphs: a remark is usually one long sentence
	// with no newline in it anywhere, and a box that only grew when Enter was
	// pressed would hide almost every comment as it was being typed.
	ta.DynamicHeight = true
	ta.MinHeight = 1
	ta.MaxHeight = maxDraftHeight

	return Model{
		repo:       repo,
		st:         newStyles(true),
		keys:       newKeyMap(),
		history:    newCommitList(),
		view:       newReviewView(),
		editor:     ta,
		focus:      focusReview,
		baseRef:    base,
		basePinned: base != "",
		context:    defaultContext,
		workBy:     map[string]*reviewFile{},
		state:      map[string]git.State{},
		commits:    map[string][]*reviewFile{},
		carries:    map[string]map[string]*git.Patch{},
		folds:      map[string]bool{},
		histOpen:   true,
		rv:         review.New(""),
		author:     author,
		mouse:      newMouseState(),
	}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.rescanCmd(), textarea.Blink, reloadTick())
}

type reloadTickMsg struct{}

func reloadTick() tea.Cmd {
	return tea.Tick(reloadInterval, func(time.Time) tea.Msg { return reloadTickMsg{} })
}

// ---------------------------------------------------------------------------
// Messages and commands
// ---------------------------------------------------------------------------

type rescanMsg struct {
	scan  git.Scan
	files []*reviewFile
	err   error
}

// commitFilesMsg is one commit's diff, together with the diffs that carry a
// comment between that commit and the working tree.
type commitFilesMsg struct {
	hash  string
	gen   int
	files []*reviewFile
	carry map[string]*git.Patch
	err   error
}

type opMsg struct {
	text   string
	err    error
	rescan bool
}

// markdownMsg is a file's text arriving for the markdown reader.
type markdownMsg struct {
	title   string
	gen     int
	text    string
	changed map[int]bool
	err     error
}

// fileMsg is a whole file arriving for the reader that v and o open.
type fileMsg struct {
	path    string
	base    string
	side    fileSide
	at      int // the line to open on, from the line the cursor was reading
	gen     int
	text    string
	changed map[int]bool
	err     error
}

// rescanCmd asks git everything, and builds the stack, off the render path.
// Parsing and lexing a whole review is the one expensive thing discobox-review does,
// and doing it here means a big change never makes the window stutter.
func (m Model) rescanCmd() tea.Cmd {
	repo, baseRef, context := m.repo, m.baseRef, m.context
	return func() tea.Msg {
		scan, err := repo.Rescan(baseRef, context)
		if err != nil {
			return rescanMsg{scan: scan, err: err}
		}
		states := map[string]git.State{}
		for _, f := range scan.Files {
			states[f.Path] = f.State
		}
		return rescanMsg{scan: scan, files: buildFiles(scan.Diff, scan.Pending, states)}
	}
}

// commitCmd loads one commit's own diff, and the diffs that carry comments in
// and out of it.
func (m Model) commitCmd(hash string) tea.Cmd {
	repo, gen, context := m.repo, m.gen, m.context
	// Every path this commit touched needs carrying, because a comment can be
	// written against any line of it, and so does every path already carrying
	// one, because those have to be found again in here.
	commented := map[string]bool{}
	for _, t := range m.rv.Threads {
		commented[t.Path] = true
	}
	return func() tea.Msg {
		text, err := repo.CommitDiff(hash, context)
		if err != nil {
			return commitFilesMsg{hash: hash, gen: gen, err: err}
		}
		files := buildFiles(text, "", nil)

		want := make(map[string]bool, len(files)+len(commented))
		for _, f := range files {
			want[f.path] = true
		}
		for p := range commented {
			want[p] = true
		}
		carry := make(map[string]*git.Patch, len(want))
		for p := range want {
			out, err := repo.CarryDiff(hash, p)
			if err != nil {
				continue
			}
			carry[p] = git.ParsePatch(out)
		}
		return commitFilesMsg{hash: hash, gen: gen, files: files, carry: carry}
	}
}

// op wraps a command whose result lands in the status bar.
func op(text string, rescan bool, fn func() error) tea.Cmd {
	return func() tea.Msg {
		if err := fn(); err != nil {
			return opMsg{text: text, err: err}
		}
		return opMsg{text: text, rescan: rescan}
	}
}

// buildFiles turns a diff covering many files into the stack's sections:
// parsed, tab-expanded, lexed and measured, all of it once.
func buildFiles(diff, pending string, states map[string]git.State) []*reviewFile {
	patches := git.ParseMulti(diff)

	// The same tree measured from HEAD says which lines are not in a commit
	// yet. It is keyed by the seam so that a deletion can be marked too.
	uncommitted := map[string]map[int]bool{}
	for _, p := range git.ParseMulti(pending) {
		if p.Path != "" {
			uncommitted[p.Path] = p.ChangedNewLines()
		}
	}

	files := make([]*reviewFile, 0, len(patches))
	for _, p := range patches {
		if p.Path == "" {
			continue
		}
		f := &reviewFile{
			path: p.Path, from: p.From, patch: p,
			lines: p.Render(), digest: review.Digest(p),
			pending: uncommitted[p.Path], state: states[p.Path],
		}
		f.adds, f.dels = p.Stat()
		f.texts = make([]string, len(f.lines))
		for i, l := range f.lines {
			if isCodeLine(l.Kind) {
				f.texts[i] = expandTabs(codeText(l.Text))
			} else {
				f.texts[i] = l.Text
			}
		}
		f.emph = pairEmphasis(f.lines, f.texts)
		f.plain = len(f.lines) > maxLexLines
		files = append(files, f)
	}
	// Git hands the tracked files over in path order and the untracked ones
	// arrive on the end, which puts every new file at the bottom of a review
	// rather than where its name says it belongs. Sorting here is the one
	// place that fixes it.
	sort.SliceStable(files, func(i, j int) bool { return files[i].path < files[j].path })
	lexAll(files)
	return files
}

// lexAll highlights every file, several at a time.
//
// One file's colours have nothing to do with another's, and this already runs
// off the render path, so the only thing standing between a review and its
// syntax is wall clock — which is what more than one core is for. discobox-review's
// own source goes from about nine tenths of a second to under two.
func lexAll(files []*reviewFile) {
	var wg sync.WaitGroup
	slot := make(chan struct{}, max(runtime.GOMAXPROCS(0), 1))
	for _, f := range files {
		if f.plain {
			continue
		}
		wg.Add(1)
		go func(f *reviewFile) {
			defer wg.Done()
			slot <- struct{}{}
			defer func() { <-slot }()
			f.spans = highlightLines(f.path, f.lines, f.texts)
		}(f)
	}
	wg.Wait()
}

// ---------------------------------------------------------------------------
// Update
// ---------------------------------------------------------------------------

func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
		m.ready = true
		// Every row is re-wrapped at the new width, so the lines a selection
		// names are no longer the lines it was made over.
		m.mouse.clear()
		m.layout()
		m.view.build()
		m.view.clamp()
		if m.md != nil {
			m.md.resize(m.width, m.height)
		}
		if m.file != nil {
			m.file.resize(m.width, m.height)
		}
		return m, nil

	case tea.MouseMsg:
		return m.onMouse(msg)

	case tea.BackgroundColorMsg:
		// The terminal has said what colour it is. Only the diff bands care,
		// but they are most of what the pane draws, so the palette is built
		// again rather than patched.
		if dark := msg.IsDark(); dark != m.dark {
			m.dark, m.st = dark, newStyles(dark)
		}
		return m, nil

	case rescanMsg:
		return m.onRescan(msg)

	case commitFilesMsg:
		return m.onCommitFiles(msg)

	case markdownMsg:
		return m.onMarkdown(msg)

	case fileMsg:
		return m.onFile(msg)

	case fileLineMsg:
		if m.file != nil {
			m.file.goToLine(msg.line)
		}
		return m, nil

	case reloadTickMsg:
		return m, tea.Batch(m.pickUpChanges(), reloadTick())

	case approveAllMsg:
		// As with one file: deciding about it hands its fold back to the rule,
		// so what has just been signed off goes away.
		undo := m.keepFolds(msg.paths...)
		for _, p := range msg.paths {
			delete(m.folds, p)
		}
		if !m.mutate(func(st *review.State) {
			for _, p := range msg.paths {
				st.Approve(p, msg.digests[p], msg.by)
			}
		}) {
			undo()
			return m, nil
		}
		m.setOK("Approved " + plural(len(msg.paths), "file", "files"))
		return m, nil

	case deleteMsg:
		if msg.comment < 0 {
			if m.mutate(func(st *review.State) { st.Remove(msg.id) }) {
				m.setOK("Deleted " + msg.id)
			}
			return m, nil
		}
		gone := false
		if !m.mutate(func(st *review.State) {
			_, gone = st.RemoveComment(msg.id, msg.comment)
		}) {
			return m, nil
		}
		if gone {
			m.setOK("Deleted the last comment, and " + msg.id + " with it")
		} else {
			m.setOK("Deleted a comment from " + msg.id)
		}
		return m, nil

	case baseChosenMsg:
		return m.onBaseChosen(msg.ref)

	case opMsg:
		if msg.err != nil {
			m.setError(msg.text + ": " + firstLine(msg.err.Error()))
			m.dialog = messageDialog(msg.text+" failed", msg.err.Error(), true)
			return m, m.rescanCmd()
		}
		m.setOK(msg.text)
		if msg.rescan {
			return m, m.rescanCmd()
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.onKey(msg)
	}
	return m, nil
}

func (m Model) doQuit() (tea.Model, tea.Cmd) {
	m.quit = true
	return m, tea.Quit
}

// onRescan takes delivery of a fresh reading of the repository.
func (m Model) onRescan(msg rescanMsg) (tea.Model, tea.Cmd) {
	m.mouse.clear()
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
	m.merging = msg.scan.Merging
	m.base = msg.scan.Base
	if msg.scan.Base.Err != "" {
		m.setError(msg.scan.Base.Err)
	}

	m.work = msg.files
	m.workBy = make(map[string]*reviewFile, len(msg.files))
	for _, f := range msg.files {
		m.workBy[f.path] = f
	}
	m.state = map[string]git.State{}
	for _, f := range msg.scan.Files {
		m.state[f.Path] = f.State
	}

	// Everything taken against the last scan is out of date: a commit's diff
	// belongs to a branch that may have been rewritten, and the diffs that
	// carry comments certainly belong to a working tree that has moved.
	m.gen++
	m.commits = map[string][]*reviewFile{}
	m.carries = map[string]map[string]*git.Patch{}
	m.loading = ""

	m.history.base = m.base.Ref
	m.history.setCommits(msg.scan.Commits)
	if _, ok := m.history.selected(); !ok {
		m.commit = ""
	}

	m.adoptReview()
	m.rebuild()

	if !m.paneVisible(m.focus) {
		m.focus = focusReview
		m.editor.Blur()
	}
	m.layout()
	return m, m.selectedCommitCmd()
}

// onCommitFiles takes delivery of a commit's diff, and shows it only if the
// history cursor is still on that commit.
func (m Model) onCommitFiles(msg commitFilesMsg) (tea.Model, tea.Cmd) {
	m.mouse.clear()
	if msg.gen != m.gen {
		return m, nil
	}
	if m.loading == msg.hash {
		m.loading = ""
	}
	if msg.err != nil {
		m.setError("Reading " + abbrevHash(msg.hash) + ": " + firstLine(msg.err.Error()))
		return m, nil
	}
	m.commits[msg.hash] = msg.files
	m.carries[msg.hash] = msg.carry
	if m.commit == msg.hash {
		m.rebuild()
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// The review file
// ---------------------------------------------------------------------------

// adoptReview picks up the review of whatever branch is checked out, moving
// the one already there aside when it belongs to a different branch.
//
// A review is of a branch, so switching branches has to switch reviews. Doing
// it by moving files rather than by keying them inside one means the live file
// is always at one path, holding one review, in the flattest shape an agent
// could ask for — which is the whole reason the state is a file at all.
func (m *Model) adoptReview() {
	if err := review.Adopt(m.repo, m.branch, lockWait); err != nil {
		// What is at the path may be another branch's review still, so it is
		// left unread; the next scan asks again.
		m.setError("Reading the review: " + firstLine(err.Error()))
		m.keepOwnReview()
		return
	}
	if saved := m.readReview(); !m.basePinned {
		m.baseRef = saved
	}
	m.reconcile()
}

// readReview loads the review file and reports the base it names, which is not
// always the one on screen: it is how a review picked up in the next session,
// or handed over by an agent, says where it is measured from. Adopting it is
// the caller's business, because a base the user has just chosen must not be
// undone by the re-read that goes with saving it.
//
// A file that will not parse leaves what is already on screen alone and says
// so: an agent caught halfway through writing one is a moment, not a reason to
// lose a review.
func (m *Model) readReview() (base string) {
	data, ok := m.repo.ReadStateFile(review.FileName)
	if !ok {
		m.rv, m.rvErr = review.New(m.branch), ""
		m.stamp = git.Stamp{}
		return m.baseRef
	}
	st, err := review.Parse([]byte(data))
	if err != nil {
		m.rvErr = "the review file will not parse: " + firstLine(err.Error())
		return m.baseRef
	}
	if !st.Of(m.branch) {
		// Another branch's review, swapped in by something that has seen a
		// checkout this window has not scanned yet. It is not this window's
		// to show; the scan that notices the checkout asks for the right one.
		m.keepOwnReview()
		return m.baseRef
	}
	m.rv, m.rvErr = st, ""
	m.stamp = m.repo.StateFileStamp(review.FileName)
	return m.rv.Base
}

// lockWait is how long the window waits for the review's lock when the person
// at it has asked for something. The window cannot draw or take a key while
// it waits, so this is short: long enough to queue behind commands that each
// hold the lock for one read and one write, and not long enough to look
// stopped when something is sitting on it.
const lockWait = 2 * time.Second

// keepOwnReview is what happens to the screen when this branch's review could
// not be read: it keeps the review it has, if that is this branch's, and
// otherwise shows none. The one it must not keep is the last branch's. Threads
// are numbered from one in every review, so a reply to t2 on a screen showing
// that review would land on this branch's t2.
func (m *Model) keepOwnReview() {
	if m.rv != nil && m.rv.Branch == m.branch {
		return
	}
	m.rv, m.stamp = review.New(m.branch), git.Stamp{}
	m.rvErr = "this branch's review could not be read"
}

// update is every write the window makes: the review is read again, handed to
// change, and saved if that left it different, all under the review's lock —
// see [review.Update], which the commands write through as well. When it
// fails, nothing has changed anywhere.
//
// That is what lets the window and any number of commands work on the same
// review at the same time. All of them write the file whole, so a change has
// to be applied to what is on disk right now — rather than to what was on disk
// when the window was last drawn — and nothing else may write between that
// read and this write, or a comment somebody added a moment ago is quietly
// dropped.
func (m *Model) update(wait time.Duration, change func(*review.State)) error {
	var now *review.State
	err := review.Update(m.repo, m.branch, wait, func(st *review.State) error {
		change(st)
		// A base chosen here, in this session, is the window's to say. Any
		// other is the file's: one set by a command a moment ago, which the
		// poll has not got to yet, must not be put back by this write.
		if m.basePinned {
			st.Base = m.baseRef
		}
		now = st
		return nil
	})
	if err != nil {
		return err
	}
	m.rv, m.rvErr = now, ""
	m.stamp = m.repo.StateFileStamp(review.FileName)
	if !m.basePinned && now.Base != m.baseRef {
		// The base moved under the window. Every diff on screen is measured
		// from the old one, which is the poll's business to put right: with
		// no stamp to recognise the file by, the next tick takes it for new.
		m.stamp = git.Stamp{}
	}
	return nil
}

// mutate changes the review at the asking of whoever is at the window, and
// reports whether the change was saved. When it was not, it was not made
// either: the status bar says why, and the caller has nothing to announce.
func (m *Model) mutate(fn func(*review.State)) bool {
	if err := m.update(lockWait, fn); err != nil {
		m.setError("Saving the review: " + firstLine(err.Error()))
		return false
	}
	m.rebuild()
	return true
}

// pickUpChanges is the poll: it notices the review file being written by
// something that is not discobox-review and takes the change straight onto the screen.
func (m *Model) pickUpChanges() tea.Cmd {
	if !m.ready {
		return nil
	}
	if stamp := m.repo.StateFileStamp(review.FileName); stamp == m.stamp {
		return nil
	}
	base := m.readReview()
	moved := !m.basePinned && base != m.baseRef
	if moved {
		m.baseRef = base
	}
	m.reconcile()
	m.rebuild()
	if moved {
		// The base is what every diff on screen was taken against, so a
		// review that names a different one has to be read again from git.
		m.rescanning = true
		return m.rescanCmd()
	}
	return nil
}

// reconcile checks the approvals against the changes now in front of the
// reviewer, and says so when one has fallen.
func (m *Model) reconcile() {
	if len(m.work) == 0 {
		return
	}
	digests := make(map[string]string, len(m.work))
	for _, f := range m.work {
		digests[f.path] = f.digest
	}
	// Asked of a copy, so that the screen changes only once the verdict is
	// saved: one that could not be written must not be shown as if it stood.
	probe, err := review.Parse(m.rv.Bytes())
	if err != nil {
		return
	}
	changed, nullified := probe.Reconcile(digests)
	if !changed {
		return
	}
	// What fell, fell from the copy on screen. The file may have moved on
	// since that was read, so the same verdict is given again on what is
	// there now, and that is what is saved.
	//
	// Nobody asked for this write, so it does not make them wait for it: if
	// something else has the lock this instant, the stamp is forgotten and the
	// next tick reads the file as new and comes back here. Any other failure
	// is not going to mend itself in a second, and is said once.
	err = m.update(0, func(st *review.State) { st.Reconcile(digests) })
	switch {
	case errors.Is(err, git.ErrLocked):
		m.stamp = git.Stamp{}
		return
	case err != nil:
		m.setError("Saving the review: " + firstLine(err.Error()))
		return
	}
	switch len(nullified) {
	case 0:
	case 1:
		m.setWarn(nullified[0] + " changed since it was approved")
	default:
		m.setWarn(plural(len(nullified), "approval", "approvals") + " nullified: files changed since they were read")
	}
}

// ---------------------------------------------------------------------------
// Building what is on screen
// ---------------------------------------------------------------------------

// rebuild puts the review document together with the diff and hands the result
// to the panes. It is the one place that decides what is on screen, so
// anything that changes either half — a rescan, a comment, an agent's edit,
// a fold — ends here.
func (m *Model) rebuild() {
	files, label := m.work, "Review"
	if m.commit != "" {
		if c, ok := m.history.selected(); ok {
			label = "Commit " + c.Short + "  " + c.Subject
		}
		files = m.commits[m.commit]
		if files == nil {
			// Nothing to show until the diff arrives; what was on screen
			// belongs to a different commit.
			files = nil
		}
	} else if m.base.Ref != "" {
		label = "Review · from " + m.base.Ref
	}

	files = m.place(files)
	// A file that is done with — approved, with nothing outstanding — is left
	// out entirely while the filter is on. The count of them goes with the
	// panes, so that what they say about the review as a whole stays true
	// about the review as a whole.
	hidden := 0
	if m.hideDone {
		kept := files[:0]
		for _, f := range files {
			if f.done() {
				hidden++
				continue
			}
			kept = append(kept, f)
		}
		files = kept
	}

	m.view.setFiles(files, m.commit, label)
	m.view.hidden = hidden
	m.view.note = m.emptyNote()
	m.layout()
}

// emptyNote is what the review pane says when it has nothing to show, which is
// either the end of the job or the beginning of it.
func (m *Model) emptyNote() string {
	if m.hideDone && len(m.work) > 0 {
		return "Nothing left to read: every file is approved and every comment answered.\n" +
			"Press f to see them all again."
	}
	if m.commit != "" {
		if m.loading != "" {
			return "Reading the commit…"
		}
		return "This commit changed nothing."
	}
	if m.base.Ref == "" {
		return "Nothing to review."
	}
	return "Nothing to review: this branch matches " + m.base.Ref + "."
}

// place hangs every comment off the line it belongs to.
//
// Comments are stored against the working tree, which is not the space a
// single commit's diff is drawn in, so reading a commit carries each one back
// through the diff between that commit and the working tree. One that will not
// carry is not dropped: it becomes one of the file's loose remarks, shown
// under its heading rather than beside a line that is not there.
func (m *Model) place(files []*reviewFile) []*reviewFile {
	carry := m.carries[m.commit]
	byPath := map[string]*reviewFile{}
	out := make([]*reviewFile, 0, len(files))
	for _, f := range files {
		f.threads, f.loose = map[int][]*review.Thread{}, nil
		f.total, f.open = m.rv.CountFor(f.path)
		f.approved = m.rv.Approved(f.path)
		f.nullified = m.rv.Nullified(f.path)
		f.folded = m.foldedFor(f)
		if m.commit != "" {
			// The state of the working tree says nothing about a commit that
			// is already made.
			f.state = ""
		} else {
			f.state = m.state[f.path]
		}
		byPath[f.path] = f
		out = append(out, f)
	}

	for _, f := range out {
		// By line, not by row: how the file is drawn is none of this
		// function's business, and a remark keyed by a row moves the moment
		// anything relays the rows out — a resize, a zoom — without coming
		// back through here.
		newAt, oldAt := f.lineIndex()
		for _, t := range m.rv.ThreadsFor(f.path) {
			side := t.Side
			first, last := t.Span()
			if m.commit != "" {
				// Threads live in the working tree's numbering; this view is
				// in the commit's.
				var ok bool
				first, ok = review.CarryBack(carry[f.path], side, first)
				if !ok {
					f.loose = append(f.loose, t)
					continue
				}
				last, _ = review.CarryBack(carry[f.path], side, last)
				side = review.SideNew
			}
			at := newAt
			if side == review.SideOld {
				at = oldAt
			}
			// A remark about a block of lines is drawn under the last of them,
			// which is where it reads as being about what came above it. If
			// that line is not in this diff, the first one will do.
			idx, ok := at[last]
			if !ok {
				idx, ok = at[first]
			}
			if !ok || !t.Anchored() {
				f.loose = append(f.loose, t)
				continue
			}
			f.threads[idx] = append(f.threads[idx], t)
		}
	}

	// A comment can outlive the change it was about: the file may have been
	// put back, or the base moved past it. Losing it silently would be the
	// worst thing this window could do, so the file keeps its place in the
	// stack with nothing but its remarks in it.
	var gone []*reviewFile
	for _, t := range m.rv.Threads {
		if byPath[t.Path] != nil {
			continue
		}
		f := byPath["\x00"+t.Path]
		if f == nil {
			f = &reviewFile{path: t.Path, gone: true, threads: map[int][]*review.Thread{}}
			f.total, f.open = m.rv.CountFor(t.Path)
			f.folded = m.foldedFor(f)
			byPath["\x00"+t.Path] = f
			gone = append(gone, f)
		}
		f.loose = append(f.loose, t)
	}
	sort.Slice(gone, func(i, j int) bool { return gone[i].path < gone[j].path })
	return append(out, gone...)
}

// foldedFor decides whether a file starts folded.
//
// Every file does: the window opens as the list of what changed, each file a
// heading that says how much and where it stands, and reading one is opening
// it. Anything the reader has folded or unfolded by hand overrides that — and
// approving a file hands its fold back to this rule, which is how a file that
// is done with goes away again.
func (m *Model) foldedFor(f *reviewFile) bool {
	if v, ok := m.folds[f.path]; ok {
		return v
	}
	return true
}

// doToggleHistory unfolds the strip of history under the review, or folds it
// back to its title. Folding it while the cursor is in it hands the keys back
// to the review, since a focus on something that is not drawn is a window
// that has stopped answering.
func (m Model) doToggleHistory() (tea.Model, tea.Cmd) {
	if !m.showHistory() {
		m.setError("This branch has no commits of its own")
		return m, nil
	}
	if !m.zoom && !m.historyFits() {
		// No row to draw it in, folded or not. Flipping it would change
		// nothing on screen now and something later, when the room came
		// back, so the key says why instead.
		m.setWarn("The window is too short to show the history")
		return m, nil
	}
	if m.zoom && m.focus != focusHistory {
		// The review has the whole window, so the history is not on screen
		// whatever histOpen says, and the key is asking to see it.
		m.histOpen = true
	} else {
		m.histOpen = !m.histOpen
	}
	if !m.histOpen && m.focus == focusHistory {
		m, _ = m.takeFocus(focusReview)
	}
	m.zoom = false
	m.layout()
	return m, nil
}

// abbrevHash shortens a hash for a message, matching what the history shows.
func abbrevHash(hash string) string {
	if len(hash) > 7 {
		return hash[:7]
	}
	return hash
}

// ---------------------------------------------------------------------------
// Keys
// ---------------------------------------------------------------------------

func (m Model) onKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	if m.dialog != nil {
		cmd, closed := m.dialog.update(msg)
		if closed {
			m.dialog = nil
		}
		return m, cmd
	}

	// A mouse selection is dropped by Esc before Esc means anything else, so
	// nobody is ever stuck behind a highlight.
	if m.mouse.hasSelection() && key.Matches(msg, m.keys.Cancel) {
		m.mouse.clear()
		return m, nil
	}

	// A full screen reader owns the screen, so it owns the keys.
	if m.md != nil {
		return m.markdownKey(msg)
	}
	if m.file != nil {
		return m.fileKey(msg)
	}

	// A status message lasts until the next key press. Clearing it here rather
	// than after dispatch means an action that sets its own message still gets
	// to show it.
	m.clearStatus()

	switch {
	case key.Matches(msg, m.keys.Quit):
		return m.doQuit()
	case key.Matches(msg, m.keys.Help):
		m.dialog = textDialog("Discobox Review — keys", helpText())
		return m, nil
	case key.Matches(msg, m.keys.Post):
		return m.doPost()
	case key.Matches(msg, m.keys.Rescan):
		return m.doRescan()
	case key.Matches(msg, m.keys.FocusReview):
		return m.setFocus(focusReview)
	case key.Matches(msg, m.keys.FocusHistory):
		if !m.showHistory() {
			m.setError("This branch has no commits of its own")
			return m, nil
		}
		// Going to a history that is folded away is asking to see it.
		m.histOpen = true
		return m.setFocus(focusHistory)
	case key.Matches(msg, m.keys.NextPane):
		return m.setFocus(m.stepPane(1))
	case key.Matches(msg, m.keys.PrevPane):
		return m.setFocus(m.stepPane(-1))
	case key.Matches(msg, m.keys.Zoom):
		return m.toggleZoom(m.focus)
	}

	// In the composer every letter is text, so none of the commands below are
	// reachable from it: Esc leaves first.
	if m.focus == focusCompose {
		return m.composeKey(msg)
	}

	// A count in front of a command, the way vim takes one. Any other key
	// ends it, so it is taken and cleared here and handed to whatever runs
	// next; only g and G do anything with it.
	count := m.count
	m.count = ""
	if m.focus == focusReview {
		if d, ok := digit(msg); ok && (d != '0' || count != "") {
			m.count = count + string(d)
			return m, nil
		}
	}

	switch {
	case key.Matches(msg, m.keys.QuitKey):
		return m.doQuit()
	case key.Matches(msg, m.keys.HelpKey):
		m.dialog = textDialog("Discobox Review — keys", helpText())
		return m, nil
	case key.Matches(msg, m.keys.RescanKey):
		return m.doRescan()
	case key.Matches(msg, m.keys.ApproveKey):
		return m.doApprove()
	case key.Matches(msg, m.keys.ApproveAll):
		return m.doApproveAll()
	case key.Matches(msg, m.keys.FoldAll):
		return m.doFoldAll()
	case key.Matches(msg, m.keys.HideDone):
		return m.doHideDone()
	case key.Matches(msg, m.keys.SplitKey):
		return m.doSplit()
	case key.Matches(msg, m.keys.SideKey):
		return m.doSide()
	case key.Matches(msg, m.keys.CommentKey):
		return m.doComment()
	case key.Matches(msg, m.keys.ReplyKey):
		return m.doReply()
	case key.Matches(msg, m.keys.ResolveKey):
		return m.doResolve()
	case key.Matches(msg, m.keys.DeleteKey):
		return m.doDelete()
	case key.Matches(msg, m.keys.BaseKey):
		return m.doChooseBase()
	case key.Matches(msg, m.keys.EditKey):
		return m.doEdit()
	case key.Matches(msg, m.keys.ViewKey):
		return m.doViewFile(sideNow)
	case key.Matches(msg, m.keys.ViewOldKey):
		return m.doViewFile(sideBefore)
	case key.Matches(msg, m.keys.MarkdownKey):
		return m.doMarkdown()
	case key.Matches(msg, m.keys.CheckoutKey):
		return m.doCheckout()
	case key.Matches(msg, m.keys.LessContextKey):
		return m.changeContext(-1)
	case key.Matches(msg, m.keys.MoreContextKey):
		return m.changeContext(+1)
	case key.Matches(msg, m.keys.HistoryKey):
		return m.doToggleHistory()
	}

	switch m.focus {
	case focusReview:
		return m.reviewKey(msg, count)
	case focusHistory:
		return m.historyKey(msg)
	}
	return m, nil
}

func (m Model) doRescan() (tea.Model, tea.Cmd) {
	m.setOK("Rescanning…")
	m.rescanning = true
	return m, m.rescanCmd()
}

// historyKey drives the commits. Moving the cursor changes what the review
// pane is showing: the whole change, or one step of it.
func (m Model) historyKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	moved := true
	switch {
	case key.Matches(msg, m.keys.Up):
		if m.history.cursor <= 0 {
			// Off the top of the history is back into the review, where
			// the reader was: the cursor there has not moved.
			return m.setFocus(focusReview)
		}
		m.history.move(-1)
	case key.Matches(msg, m.keys.Down):
		if m.history.cursor >= m.history.rows()-1 {
			return m, nil
		}
		m.history.move(1)
	case key.Matches(msg, m.keys.PageUp):
		m.history.move(-max(m.history.height-1, 1))
	case key.Matches(msg, m.keys.PageDown):
		m.history.move(max(m.history.height-1, 1))
	case key.Matches(msg, m.keys.Home):
		m.history.moveTo(0)
	case key.Matches(msg, m.keys.End):
		m.history.moveTo(m.history.rows() - 1)
	case key.Matches(msg, m.keys.Right), key.Matches(msg, m.keys.Toggle),
		key.Matches(msg, m.keys.Act):
		return m.setFocus(focusReview)
	case key.Matches(msg, m.keys.ZoomKey):
		return m.toggleZoom(m.focus)
	default:
		moved = false
	}
	if !moved {
		return m, nil
	}
	return m, m.selectedCommitCmd()
}

// selectedCommitCmd switches the review to whatever the history has picked,
// and asks for its diff when it has not been read yet.
func (m *Model) selectedCommitCmd() tea.Cmd {
	hash := ""
	if c, ok := m.history.selected(); ok {
		hash = c.Hash
	}
	m.commit = hash
	m.mouse.clear()
	if hash != "" && m.commits[hash] == nil {
		m.loading = hash
		m.rebuild()
		return m.commitCmd(hash)
	}
	m.rebuild()
	return nil
}

// digit reports whether a key press is a single digit and nothing else. A key
// carries the text it produced, so a digit is a key whose text is one digit,
// with no modifier held.
func digit(msg tea.KeyPressMsg) (rune, bool) {
	if msg.Mod != 0 || len([]rune(msg.Text)) != 1 {
		return 0, false
	}
	if r := []rune(msg.Text)[0]; r >= '0' && r <= '9' {
		return r, true
	}
	return 0, false
}

// reviewKey drives the stack itself. count is the digits typed in front of the
// key, empty when there were none.
func (m Model) reviewKey(msg tea.KeyPressMsg, count string) (tea.Model, tea.Cmd) {
	// While a visual selection is open the cursor stays inside the pane:
	// running off the end would abandon the gesture halfway.
	if m.view.visual() {
		switch {
		case key.Matches(msg, m.keys.Up):
			m.view.move(-1)
			return m, nil
		case key.Matches(msg, m.keys.Down):
			m.view.move(1)
			return m, nil
		case key.Matches(msg, m.keys.Left):
			m.view.scrollH(-hScrollStep)
			return m, nil
		case key.Matches(msg, m.keys.Right):
			m.view.scrollH(hScrollStep)
			return m, nil
		case key.Matches(msg, m.keys.Cancel):
			m.view.clearVisual()
			return m, nil
		}
	}

	switch {
	case key.Matches(msg, m.keys.Up):
		if m.view.cursor <= 0 {
			return m, nil
		}
		m.view.move(-1)
	case key.Matches(msg, m.keys.Down):
		if m.view.cursor >= len(m.view.rows)-1 {
			return m, nil
		}
		m.view.move(1)
	case key.Matches(msg, m.keys.Left):
		m.view.scrollH(-hScrollStep)
		return m, nil
	case key.Matches(msg, m.keys.Right):
		m.view.scrollH(hScrollStep)
		return m, nil
	case key.Matches(msg, m.keys.PageUp):
		m.view.move(-max(m.view.bodyRows()-1, 1))
	case key.Matches(msg, m.keys.PageDown):
		m.view.move(max(m.view.bodyRows()-1, 1))
	case key.Matches(msg, m.keys.Home):
		// 24g goes to line 24, as 24G does in vim. Bare, they are still the
		// two ends of the review.
		if n, ok := lineCount(count); ok {
			return m.gotoLine(n)
		}
		m.view.moveTo(0)
	case key.Matches(msg, m.keys.End):
		if n, ok := lineCount(count); ok {
			return m.gotoLine(n)
		}
		m.view.moveTo(len(m.view.rows) - 1)
	case key.Matches(msg, m.keys.NextHunk):
		m.view.jumpHunk(1)
	case key.Matches(msg, m.keys.PrevHunk):
		m.view.jumpHunk(-1)
	case key.Matches(msg, m.keys.NextFile):
		m.view.jumpFile(1)
	case key.Matches(msg, m.keys.PrevFile):
		m.view.jumpFile(-1)
	case key.Matches(msg, m.keys.NextThread):
		m.unfoldOpenThreads()
		if !m.view.jumpThread(1, m.rv) {
			m.setOK("No open comment below here")
		}
	case key.Matches(msg, m.keys.PrevThread):
		m.unfoldOpenThreads()
		if !m.view.jumpThread(-1, m.rv) {
			m.setOK("No open comment above here")
		}
	case key.Matches(msg, m.keys.Visual):
		m.view.toggleVisual()
		return m, nil
	case key.Matches(msg, m.keys.ZoomKey):
		return m.toggleZoom(m.focus)
	case key.Matches(msg, m.keys.Toggle):
		return m.doDisclose()
	case key.Matches(msg, m.keys.Act):
		return m.doAct()
	default:
		return m, nil
	}
	return m, nil
}

// unfoldOpenThreads opens every folded file somebody is still waiting for an
// answer in. A file opens folded, so its conversations are not rows until it
// is read — and t, which goes looking for the next one, would otherwise walk
// straight past the files it was asked about and report that there were none.
func (m *Model) unfoldOpenThreads() {
	changed := false
	for _, f := range m.view.files {
		if f.folded && f.open > 0 {
			m.folds[f.path], changed = false, true
		}
	}
	if changed {
		m.rebuild()
	}
}

// lineCount reads a typed count, refusing one too large to be a line number in
// anything anybody would review.
func lineCount(count string) (int, bool) {
	if count == "" || len(count) > 9 {
		return 0, false
	}
	n, err := strconv.Atoi(count)
	if err != nil || n <= 0 {
		return 0, false
	}
	return n, true
}

// gotoLine jumps to a numbered line of the file the cursor is in, counted in
// the file as it now stands — which is the space every comment and every
// compiler error names a line in.
//
// Most of a file is not in its own diff, so a line that is not on screen
// anywhere lands on the nearest one that is, and says so rather than pretending
// it went where it was asked.
func (m Model) gotoLine(n int) (tea.Model, tea.Cmd) {
	r, ok := m.view.row()
	if !ok || r.file < 0 {
		m.setError("Nothing to go to")
		return m, nil
	}
	f := m.view.files[r.file]
	if f.folded {
		// A line of a folded file is not drawn anywhere to go to, and asking
		// for one by number is asking to see it.
		m.folds[f.path] = false
		m.rebuild()
	}
	row, exact, found := m.view.lineRow(r.file, n)
	if !found {
		m.setError(f.path + " has no line " + itoa(n) + " in this diff")
		return m, nil
	}
	// lineRow can only find a line on the new side, so that is the side the
	// cursor lands on. Leaving it where it was would put the bar under the
	// removed line in the left half while the status named the line on the
	// right — the same mismatch between the line named and the line acted on
	// that this pane exists to avoid.
	m.view.onOld = false
	m.view.reveal(row)
	if !exact {
		at := 0
		if l, ok := m.view.files[r.file].rowLine(m.view.rows[row].line); ok {
			at = l.New
		}
		m.setWarn(f.path + " line " + itoa(n) + " is not in the diff; nearest is " + itoa(at))
	} else {
		m.setOK(f.path + ":" + itoa(n))
	}
	return m, nil
}

// doToggle is Space in the review: it opens or shuts whatever the cursor is
// on. One verb, three objects — a file's section, a closed conversation, and a
// line, which has nothing to open and so offers the thing you would want next.
func (m Model) doAct() (tea.Model, tea.Cmd) {
	r, ok := m.view.row()
	if !ok {
		return m, nil
	}
	switch {
	case r.kind == rowFile:
		return m.doFold()
	case r.kind == rowThread:
		id := r.thread
		t := m.rv.Thread(id)
		if t != nil && t.Resolved {
			m.view.expanded[id] = !m.view.expanded[id]
			m.view.build()
			m.view.clamp()
			return m, nil
		}
		return m.doReply()
	}
	return m.doComment()
}

// doDisclose opens and shuts what the cursor is on, and makes nothing new.
//
// That is the whole of the difference from doAct, and the reason for it: the
// two were one key, so reading down a file and pressing Space — which folds a
// file and unfolds a closed conversation — opened a comment box on every line
// of the diff. A key for looking at things should not start writing them.
//
// A file's section is the file, wherever in it you have got to. Space folds it
// from a line two hundred rows down as readily as from its heading, because
// having to scroll back up to the heading to put a file away is the sort of
// errand a reader should not be sent on. The one thing that is not the file is
// a resolved conversation, which has a disclosure of its own.
func (m Model) doDisclose() (tea.Model, tea.Cmd) {
	r, ok := m.view.row()
	if !ok {
		return m, nil
	}
	if r.kind == rowThread {
		id := r.thread
		if t := m.rv.Thread(id); t != nil && t.Resolved {
			m.view.expanded[id] = !m.view.expanded[id]
			m.view.build()
			m.view.clamp()
			return m, nil
		}
	}
	return m.doFold()
}

// doFold opens or shuts the file the cursor is in. The rebuild leaves the
// file's heading at the height it was drawn at, so folding empties the space
// below the heading and unfolding fills it again, with the heading itself and
// everything above it staying where they were.
func (m Model) doFold() (tea.Model, tea.Cmd) {
	f, ok := m.view.file()
	if !ok {
		return m, nil
	}
	m.folds[f.path] = !f.folded
	m.rebuild()
	return m, nil
}

// foldFile discloses one file by name, for the mouse: the cursor is on the
// heading that was clicked, but a name says which file was meant however the
// rows have moved since the button went down.
func (m Model) foldFile(path string) (tea.Model, tea.Cmd) {
	i, ok := m.view.byPath[path]
	if !ok {
		return m, nil
	}
	m.folds[path] = !m.view.files[i].folded
	m.rebuild()
	return m, nil
}

// doHideDone takes the finished files off the screen, or puts them back.
//
// Folding a file gets it out of the way and leaves it in the review, which is
// right while you are still reading. This is for the other half of the job:
// once most of a change has been signed off, what is left is the list of
// things still wanting something, and everything else is noise between them.
func (m Model) doHideDone() (tea.Model, tea.Cmd) {
	m.hideDone = !m.hideDone
	path := m.view.pathAt()
	m.rebuild()
	if m.hideDone {
		// The file being read may have been one of the ones that went.
		if !m.view.jumpToFile(path) {
			m.view.clamp()
		}
		if n := m.view.hidden; n == 0 {
			m.setOK("Nothing is finished with yet · f shows every file again")
		} else {
			m.setOK("Hiding " + plural(n, "finished file", "finished files") + " · f shows them again")
		}
		return m, nil
	}
	m.view.jumpToFile(path)
	m.setOK("Showing every file")
	return m, nil
}

// doSplit draws the two sides of the change beside each other, or one above
// the other again.
//
// Neither is the right way round for everything: a unified diff reads as one
// text and is what a patch is, while a split one lines up what a line was
// against what it became, which is what you want when the change is an edit
// rather than an insertion. So it is a key rather than a setting.
func (m Model) doSplit() (tea.Model, tea.Cmd) {
	// The line being read is what the reader is keeping their place in, so it
	// is what survives the change of view — not merely the file it is in. The
	// rows are numbered differently either side of this, which is exactly why
	// the line has to be remembered as a line and looked up again afterwards.
	path, at, wasOld := m.view.cursorLine()
	m.view.split, m.view.asked = !m.view.split, true
	// A line the cursor was on in a unified diff is on one side or the other,
	// and in a split one it is in the half for that side. Carrying the side
	// over is what stops s moving the cursor from the removed line to the
	// added one that replaced it, which would be a different line entirely.
	if at >= 0 {
		m.view.onOld = wasOld
	}
	m.view.clearVisual()
	m.rebuild()
	if at < 0 || !m.view.goToDiffLine(path, at) {
		m.view.jumpToFile(path)
	}
	switch {
	case !m.view.split:
		m.setOK("Unified diff")
	case m.view.splitting():
		m.setOK("Split diff")
	default:
		// Asked for and remembered, but not drawn: the pane will split itself
		// as soon as it is wide enough.
		m.setWarn("Split diff needs " + itoa(minSplitWidth) + " columns; showing unified until then")
	}
	return m, nil
}

// doSide moves the cursor across a split row, to the other of the two lines it
// draws. It is what makes a comment on a removed line reachable while split: a
// unified diff gives that line a row of its own to sit on, and a split one does
// not, so the side has to be said rather than pointed at.
func (m Model) doSide() (tea.Model, tea.Cmd) {
	if !m.view.splitting() {
		m.setWarn("S picks a side of a split row; this diff is unified (s splits it)")
		return m, nil
	}
	m.view.onOld = !m.view.onOld
	// A selection is a run of lines on one side, so moving across sides ends
	// it rather than quietly changing what it covers.
	m.view.clearVisual()
	if m.view.onOld {
		m.setOK("The old side: the lines this change took away")
	} else {
		m.setOK("The new side: the lines this change proposes")
	}
	return m, nil
}

// doFoldAll shuts every file, or opens every file when they are already shut.
//
// One key for both because they are the same want at two moments: a long
// review folded down to its headings is the map of it, and that is worth
// reaching whether you are coming from having everything open or from having
// read your way down to nothing. Anything still open decides which way it
// goes, so the first press always tidies.
//
// Opening them again steps over the ones that are done with — approved, with
// nothing outstanding on them. "Show me everything" plainly means everything
// there is still something to do about, and undoing that would hand back the
// pile the approving was clearing. So those are left to the rule, which folds
// them, and only the rest are opened.
func (m Model) doFoldAll() (tea.Model, tea.Cmd) {
	if len(m.view.files) == 0 {
		return m, nil
	}
	fold := false
	for _, f := range m.view.files {
		if !f.folded {
			fold = true
			break
		}
	}
	path := m.view.pathAt()

	kept := 0
	for _, f := range m.view.files {
		switch {
		case fold:
			m.folds[f.path] = true
		case f.done():
			delete(m.folds, f.path)
			kept++
		default:
			m.folds[f.path] = false
		}
	}
	// Unless there is nothing else left, in which case the second press has to
	// do something: a review where every file is done with would otherwise
	// have a key that looks broken.
	if !fold && kept == len(m.view.files) {
		for _, f := range m.view.files {
			m.folds[f.path] = false
		}
		kept = 0
	}

	m.rebuild()
	// The file you were reading is the one to still be looking at, whichever
	// way it went.
	m.view.jumpToFile(path)
	switch {
	case fold:
		m.setOK("Folded " + plural(len(m.view.files), "file", "files"))
	case kept > 0:
		m.setOK("Opened " + plural(len(m.view.files)-kept, "file", "files") +
			" · " + itoa(kept) + " approved left folded")
	default:
		m.setOK("Opened " + plural(len(m.view.files), "file", "files"))
	}
	return m, nil
}

// ---------------------------------------------------------------------------
// Panes
// ---------------------------------------------------------------------------

// showHistory reports whether the branch has any commits of its own to list.
func (m *Model) showHistory() bool { return len(m.history.commits) > 0 }

func (m *Model) paneVisible(f focusArea) bool {
	switch f {
	case focusHistory:
		// Unfolded, and with a row to be on: a window too short for any of
		// it draws the title alone, which is nowhere to put a cursor.
		return m.showHistory() && m.histOpen && m.history.height > 0
	case focusCompose:
		return m.compose != nil
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

func (m Model) setFocus(f focusArea) (tea.Model, tea.Cmd) {
	m, cmd := m.takeFocus(f)
	m.layout()
	return m, cmd
}

// takeFocus is setFocus without laying the window out again. Laying out brings
// every pane's cursor back on screen, which is wrong for a gesture that has
// only just scrolled a pane away from it.
func (m Model) takeFocus(f focusArea) (Model, tea.Cmd) {
	m.focus = f
	// A selection belongs to the pane it was made in and to the moment it was
	// made in, so leaving that pane ends it.
	if f != focusReview {
		m.view.clearVisual()
	}
	if f == focusCompose {
		m.editor.Focus()
		return m, textarea.Blink
	}
	m.editor.Blur()
	return m, nil
}

// ---------------------------------------------------------------------------
// The review's own commands
// ---------------------------------------------------------------------------

func (m Model) changeContext(delta int) (tea.Model, tea.Cmd) {
	want := min(max(m.context+delta, minContext), maxContext)
	if want == m.context {
		return m, nil
	}
	m.context = want
	m.setOK("Diff context: " + itoa(m.context) + " lines")
	return m, m.rescanCmd()
}

// target is the file a file-level command acts on: the one the review cursor
// is in.
func (m *Model) target() (string, bool) {
	if f, ok := m.view.file(); ok {
		return f.path, true
	}
	return "", false
}

// doApprove signs off a file, or takes the signature back.
//
// What is approved is the change, not the name: the digest of the file's whole
// change from the base goes in with it, so that editing the file afterwards
// nullifies the approval rather than leaving a tick against something nobody
// read. That holds whichever view the key was pressed in, since there is only
// one review and a commit is a part of it.
func (m Model) doApprove() (tea.Model, tea.Cmd) {
	path, ok := m.target()
	if !ok {
		m.setError("No file to approve")
		return m, nil
	}
	if m.rv.Approved(path) {
		// Withdrawing an approval is having more to say about the file, so it
		// opens rather than going back to how the window started.
		undo := m.keepFolds(path)
		m.folds[path] = false
		if m.mutate(func(st *review.State) { st.Unapprove(path) }) {
			m.setOK("Approval withdrawn from " + path)
		} else {
			undo()
		}
		return m, nil
	}
	f := m.workBy[path]
	if f == nil {
		m.setError(path + " is not part of this review")
		return m, nil
	}
	// Approving a file hands its fold back to the rule every file starts
	// with, so one approved after F opened everything still goes away.
	undo := m.keepFolds(path)
	delete(m.folds, path)
	digest, author := f.digest, m.author
	if m.mutate(func(st *review.State) { st.Approve(path, digest, author) }) {
		m.setOK("Approved " + path)
	} else {
		undo()
	}
	return m, nil
}

// keepFolds remembers how the reader left some files folded, and hands back
// what puts that right again. A fold changes with a verdict, and the rows are
// laid out as the verdict is saved — so the fold is set first, and undone if
// the save did not happen: a verdict that was not recorded moved nothing.
func (m *Model) keepFolds(paths ...string) func() {
	type fold struct{ folded, set bool }
	was := make(map[string]fold, len(paths))
	for _, p := range paths {
		v, ok := m.folds[p]
		was[p] = fold{v, ok}
	}
	return func() {
		for p, f := range was {
			if f.set {
				m.folds[p] = f.folded
			} else {
				delete(m.folds, p)
			}
		}
	}
}

// doApproveAll signs off everything still outstanding, which is worth having
// for the review that turns out to be all noise — and worth asking about
// first, since it is the one key here that can say a lot at once.
func (m Model) doApproveAll() (tea.Model, tea.Cmd) {
	var pending []string
	for _, f := range m.work {
		if !m.rv.Approved(f.path) {
			pending = append(pending, f.path)
		}
	}
	if len(pending) == 0 {
		m.setOK("Every file is already approved")
		return m, nil
	}
	digests := map[string]string{}
	for _, p := range pending {
		digests[p] = m.workBy[p].digest
	}
	author := m.author
	m.dialog = confirmDialog("Approve everything",
		"Approve "+plural(len(pending), "file", "files")+" in one go?\n\n"+
			"Each approval is of the change as it stands now, so anything\n"+
			"edited afterwards comes back unapproved.",
		func(string) tea.Cmd {
			return func() tea.Msg { return approveAllMsg{paths: pending, digests: digests, by: author} }
		})
	return m, nil
}

type approveAllMsg struct {
	paths   []string
	digests map[string]string
	by      string
}

// doComment starts a conversation about the line under the cursor.
func (m Model) doComment() (tea.Model, tea.Cmd) {
	f, l, span, last, ok := m.view.commentTarget()
	if !ok {
		m.setError("Put the cursor on a line of the diff to comment on it")
		return m, nil
	}
	c := &compose{
		path: f.path, span: span, at: last,
		text: strings.TrimRight(codeText(l.Text), "\r"),
	}

	if m.commit == "" {
		// The review's own coordinates: the working tree for a line that is
		// in it, the base for one this change took away.
		if l.Kind == '-' {
			c.side, c.line = review.SideOld, l.Old
		} else {
			c.side, c.line = review.SideNew, l.New
		}
	} else {
		// A run of lines in a commit need not still be a run in the working
		// tree, so a remark carried out of one is anchored to a single line.
		c.span = 1
		// A remark made against one commit belongs to the review as a whole,
		// so it is carried into the working tree's numbering here, once, at
		// the moment it is written. A deleted line has no line of its own out
		// there, so it is carried by its seam — the place in the file where it
		// used to be — which is where a reader would look for it.
		carried, ok := review.Carry(m.carries[m.commit][f.path], review.SideNew, l.Seam)
		c.side, c.line = review.SideNew, carried
		if !ok {
			c.line = 0
		}
		side := review.SideNew
		line := l.New
		if l.Kind == '-' {
			side, line = review.SideOld, l.Old
		}
		c.origin = &review.Origin{Commit: m.commit, Side: side, Line: line}
	}

	where := f.path + ":" + itoa(c.line)
	switch {
	case c.line == 0:
		where = f.path + " (no longer a line in the working tree)"
	case c.span > 1:
		where = f.path + ":" + itoa(c.line) + "–" + itoa(c.line+c.span-1)
	}
	c.title = "Comment on " + where
	m.view.clearVisual()
	return m.openCompose(c)
}

// doReply adds to the conversation the cursor is in.
func (m Model) doReply() (tea.Model, tea.Cmd) {
	id := m.view.thread()
	if id == "" {
		m.setError("Put the cursor on a comment to reply to it")
		return m, nil
	}
	t := m.rv.Thread(id)
	if t == nil {
		m.setError("That comment is no longer there")
		return m, nil
	}
	return m.openCompose(&compose{
		title: "Reply to " + t.ID + " on " + t.Path, thread: t.ID, path: t.Path, at: -1,
	})
}

// openCompose puts an empty box into the stack at the place the comment will
// land, and the keyboard into it.
func (m Model) openCompose(c *compose) (tea.Model, tea.Cmd) {
	m.compose = c
	m.editor.SetValue("")
	m.editor.CursorStart()

	line := -1
	if c.thread == "" {
		line = c.at
	}
	m.view.draft = &draft{path: c.path, line: line, thread: c.thread}
	m.refreshDraft()
	if first, last, ok := m.view.draftSpan(); ok {
		m.view.cursor = first
		// A little of the code above the box, since that is what the comment
		// is about and what it should be written while looking at.
		m.view.revealSpan(max(first-draftLead, 0), last)
	}
	return m.setFocus(focusCompose)
}

// draftLead is how many rows above the box are kept on screen: enough of the
// line being commented on, and its neighbours, to write about it.
const draftLead = 4

// draftTextWidth is how wide the field inside the box is: the pane's content,
// less the indent the box shares with a comment thread and the box's own
// border and padding.
func (m *Model) draftTextWidth() int {
	return max(m.view.contentWidth()-threadIndent-3, 8)
}

// refreshDraft redraws the box. The rows in the stack carry only an index into
// it, so a keystroke costs the box and nothing else — unless the box changed
// height, when the stack has to be laid out again around it.
func (m *Model) refreshDraft() {
	d := m.view.draft
	if d == nil {
		return
	}
	// Setting the width re-measures the height, since how many rows the text
	// takes depends on how wide they are allowed to be.
	m.editor.SetWidth(m.draftTextWidth())

	before := len(d.lines)
	d.lines = m.drawDraft()
	if len(d.lines) != before {
		m.view.build()
		if first, last, ok := m.view.draftSpan(); ok {
			m.view.cursor = min(max(m.view.cursor, first), last)
			m.view.revealSpan(max(first-draftLead, 0), last)
		}
		m.view.clamp()
	}
}

// postHint names the key that posts, and it names the one that arrives in
// every terminal rather than the one a hand reaches for first. See the Post
// binding for why those are not the same key.
const postHint = "Ctrl-S posts · Esc discards"

// maxDraftHeight is how tall the box grows before it starts scrolling inside
// itself. Past that it is covering more of the diff than the remark is worth
// having beside it.
const maxDraftHeight = 10

// drawDraft renders the box: a rule round the field, its title saying where
// the comment will land, and its foot saying how to post it.
func (m *Model) drawDraft() []string {
	w := max(m.view.contentWidth()-threadIndent, 12)
	title := "Comment"
	if m.compose != nil {
		title = m.compose.title
	}

	out := []string{m.draftEdge(w, "╭", title, m.st.commentHead, "╮")}
	for _, l := range strings.Split(m.editor.View(), "\n") {
		out = append(out, strings.Repeat(" ", threadIndent)+
			m.st.commentRule.Render("│ ")+
			fitANSI(l, max(w-3, 0))+
			m.st.commentRule.Render("│"))
	}
	out = append(out, m.draftEdge(w, "╰", postHint, m.st.commentFoot, "╯"))
	return out
}

// draftEdge is the top or bottom of the box: a corner, a word about what the
// box is for, and the rule running out to the far corner.
func (m *Model) draftEdge(w int, corner, label string, ink lipgloss.Style, end string) string {
	room := max(w-5, 1)
	label = truncate(label, room)
	fill := max(room-width(label), 0)
	rule := m.st.commentRule
	return strings.Repeat(" ", threadIndent) +
		rule.Render(corner+"─ ") + ink.Render(label) +
		rule.Render(" "+strings.Repeat("─", fill)+end)
}

// closeDraft takes the box out of the stack and puts the cursor somewhere
// sensible: on the thread just written, when there is one, and otherwise back
// on the line the box was about.
func (m *Model) closeDraft(thread string) {
	d := m.view.draft
	m.view.draft = nil
	m.compose = nil
	m.editor.SetValue("")
	m.rebuild()
	switch {
	case thread != "":
		if i, ok := m.view.threadRowAt(thread); ok {
			m.view.reveal(i)
			return
		}
	case d != nil && d.line >= 0:
		if i, ok := m.view.diffRowAt(d.path, d.line); ok {
			m.view.reveal(i)
			return
		}
	}
	m.view.clamp()
}

// doPost files what has been written. It is the only command reachable from
// inside the composer, where every letter is text.
func (m Model) doPost() (tea.Model, tea.Cmd) {
	if m.compose == nil {
		m.setError("Nothing to post — press c on a line to write a comment")
		return m, nil
	}
	body := strings.TrimSpace(m.editor.Value())
	if body == "" {
		// The answer to "you have written nothing" is to type, and the cursor
		// is already where that happens.
		m.setError("Write something first")
		return m.setFocus(focusCompose)
	}
	c, author := m.compose, m.author
	id := c.thread
	saved := false
	if c.thread != "" {
		there := false
		saved = m.mutate(func(st *review.State) { there = st.Reply(c.thread, author, body) })
		if saved && !there {
			// Deleted by something else while the reply was being written.
			// The reply stays in the box: it is the only copy of it.
			m.setError(c.thread + " is no longer there to reply to")
			return m.setFocus(focusCompose)
		}
	} else {
		saved = m.mutate(func(st *review.State) {
			t := st.Start(&review.Thread{
				Path: c.path, Side: c.side, Line: c.line, Lines: c.span,
				Text: c.text, Origin: c.origin,
			}, author, body)
			id = t.ID
		})
	}
	if !saved {
		// What was written is still in the box, to be posted again.
		return m.setFocus(focusCompose)
	}
	if c.thread != "" {
		m.setOK("Replied to " + c.thread)
	} else {
		m.setOK("Commented on " + c.path + " as " + id)
	}
	m.closeDraft(id)
	return m.setFocus(focusReview)
}

// doResolve closes the conversation the cursor is in, or opens it again.
func (m Model) doResolve() (tea.Model, tea.Cmd) {
	id := m.view.thread()
	if id == "" {
		m.setError("Put the cursor on a comment to resolve it")
		return m, nil
	}
	t := m.rv.Thread(id)
	if t == nil {
		m.setError("That comment is no longer there")
		return m, nil
	}
	want := !t.Resolved
	if !m.mutate(func(st *review.State) { st.SetResolved(id, want) }) {
		return m, nil
	}
	if want {
		delete(m.view.expanded, id)
		m.setOK("Resolved " + id)
	} else {
		m.setOK("Reopened " + id)
	}
	return m, nil
}

// doDelete throws away what the cursor is on: one remark when it is on one,
// and the whole conversation when it is on the rule above or the hint below.
//
// Resolving is what you normally want — it keeps the record of what was asked
// and answered. This is for the thing written in the wrong place, or the reply
// that turned out to be about something else.
func (m Model) doDelete() (tea.Model, tea.Cmd) {
	id := m.view.thread()
	if id == "" {
		m.setError("Put the cursor on a comment to delete it")
		return m, nil
	}
	t := m.rv.Thread(id)
	if t == nil {
		return m, nil
	}

	// One remark out of several. Taking the last one would leave an anchor on
	// a line with nothing to say, so that is the whole conversation either way
	// and the question says so.
	if at, i, ok := m.view.comment(); ok && at == id && len(t.Comments) > 1 {
		c := t.Comments[i]
		body := "Delete this one remark from " + id + " on " + t.Path + "?\n\n" +
			"  " + firstLine(c.Body) + "\n\n" +
			plural(len(t.Comments)-1, "comment", "comments") + " left in the conversation."
		m.dialog = confirmDialog("Delete a comment", body, func(string) tea.Cmd {
			return func() tea.Msg { return deleteMsg{id: id, comment: i} }
		})
		return m, nil
	}

	body := "Delete " + id + " on " + t.Path + " and the " +
		plural(len(t.Comments), "comment", "comments") + " in it?\n\n" +
		"Resolving it instead keeps the record."
	m.dialog = confirmDialog("Delete a conversation", body, func(string) tea.Cmd {
		return func() tea.Msg { return deleteMsg{id: id, comment: -1} }
	})
	return m, nil
}

// deleteMsg is the answer to the question: a whole thread when comment is -1,
// and one remark of it otherwise.
type deleteMsg struct {
	id      string
	comment int
}

// doChooseBase picks what the review is measured from.
func (m Model) doChooseBase() (tea.Model, tea.Cmd) {
	items := []string{autoBase}
	items = append(items, m.repo.Refs()...)
	items = append(items, commitBase, otherBase)
	m.dialog = listDialog("Review base",
		"Measure this branch from which ref?\n"+
			"The review is everything between its merge base with that ref\n"+
			"and the working tree.",
		items, func(choice string) tea.Cmd {
			return func() tea.Msg { return baseChosenMsg{ref: choice} }
		})
	return m, nil
}

// recentCommits is how many of the branch's own commits the picker offers.
// Far enough back to reach the start of a piece of work, and not so far that
// the list stops being a list.
const recentCommits = 60

// doChooseCommitBase offers the branch's own history as the base.
//
// A ref is the right answer when there is one, but the point you want to
// measure from is often somewhere in your own commits — "review what I did
// after this" — and that point has no name but its hash.
func (m Model) doChooseCommitBase() (tea.Model, tea.Cmd) {
	commits, err := m.repo.Recent(recentCommits)
	if err != nil || len(commits) == 0 {
		m.setError("No commits to measure from")
		return m, nil
	}
	items := make([]string, 0, len(commits))
	for _, c := range commits {
		items = append(items, commitItem(c))
	}
	m.dialog = listDialog("Review from a commit",
		"Measure from which commit?\n"+
			"The review becomes everything after it: what that commit left\n"+
			"behind, up to the working tree.",
		items, func(choice string) tea.Cmd {
			// The hash leads the row, so it is what comes back off it.
			hash, _, _ := strings.Cut(choice, " ")
			return func() tea.Msg { return baseChosenMsg{ref: hash} }
		})
	return m, nil
}

// commitItem is one row of the commit picker. The hash comes first because it
// is both what identifies the commit and what is read back off the choice.
func commitItem(c git.Commit) string {
	return c.Short + "  " + pad(c.When, 14) + "  " + c.Subject
}

const (
	autoBase   = "(let Discobox Review choose)"
	commitBase = "Pick a commit…"
	otherBase  = "Other ref…"
)

type baseChosenMsg struct{ ref string }

// onBaseChosen takes a new base and reads the whole review again against it.
//
// The base goes into the review file, so it survives the session and an agent
// reading the file knows what the comments are measured from. Approvals are
// left alone: they are claims about a change, and a change measured from
// somewhere else is a different change, so the digests settle it on the next
// scan without anything here having to guess.
func (m Model) onBaseChosen(ref string) (tea.Model, tea.Cmd) {
	switch ref {
	case autoBase:
		m.baseRef = ""
	case commitBase:
		return m.doChooseCommitBase()
	case otherBase:
		m.dialog = inputDialog("Review base", "Name a branch, tag or commit:", "origin/main",
			func(s string) tea.Cmd {
				s = strings.TrimSpace(s)
				return func() tea.Msg { return baseChosenMsg{ref: s} }
			})
		return m, nil
	case "":
		return m, nil
	default:
		m.baseRef = ref
	}
	m.basePinned = true
	// Unsaved, the base still holds for this session, which is what was asked
	// for first: it is written with the next thing that is, and meanwhile the
	// status bar says the saving failed.
	if m.mutate(func(*review.State) {}) {
		if m.baseRef == "" {
			m.setOK("Base: whatever this branch is built on")
		} else {
			m.setOK("Base: " + m.baseRef)
		}
	}
	m.rescanning = true
	return m, m.rescanCmd()
}

func (m Model) doCheckout() (tea.Model, tea.Cmd) {
	branches, err := m.repo.Branches()
	if err != nil || len(branches) == 0 {
		m.setError("No branches available")
		return m, nil
	}
	repo := m.repo
	m.dialog = listDialog("Check out a branch",
		"Review which branch instead?", branches,
		func(b string) tea.Cmd {
			return op("Checked out "+b, true, func() error { _, err := repo.Checkout(b); return err })
		})
	return m, nil
}

// ---------------------------------------------------------------------------
// The composer
// ---------------------------------------------------------------------------

func (m Model) composeKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Cancel):
		m.closeDraft("")
		m.setOK("Comment discarded")
		return m.setFocus(focusReview)
	case key.Matches(msg, m.keys.NextPane):
		return m.setFocus(focusReview)
	}
	var cmd tea.Cmd
	m.editor, cmd = m.editor.Update(msg)
	m.refreshDraft()
	return m, cmd
}

// ---------------------------------------------------------------------------
// Reading a file
// ---------------------------------------------------------------------------

// fileInView is the file every "open this" command acts on, and the commit it
// should be read at.
func (m *Model) fileInView() (string, string, *reviewFile) {
	path, ok := m.target()
	if !ok {
		return "", "", nil
	}
	var f *reviewFile
	for _, cand := range m.view.files {
		if cand.path == path {
			f = cand
			break
		}
	}
	return path, m.commit, f
}

// doEdit opens the file in view in the user's editor, handing it the terminal
// until it exits. While a commit is on screen the editor gets that commit's
// version, as a read-only copy: history reads well in an editor, it just
// cannot be written there.
func (m Model) doEdit() (tea.Model, tea.Cmd) {
	path, commit, _ := m.fileInView()
	if path == "" {
		m.setError("No file to edit")
		return m, nil
	}
	editor, tmp, err := m.repo.EditAt(commit, path)
	if err != nil {
		m.setError(err.Error())
		return m, nil
	}
	return m, tea.Exec(editor, editorClosed(m.repo, path, tmp))
}

// editorClosed is what happens once the editor exits. For a working tree file
// the rescan is not optional: the editor may well have changed the file, and
// the scan is how the review catches up with it — including nullifying an
// approval of what it used to say.
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

// doMarkdown opens the file in view in the full screen markdown reader, at
// whichever version the window is showing.
func (m Model) doMarkdown() (tea.Model, tea.Cmd) {
	path, commit, f := m.fileInView()
	if path == "" {
		m.setError("No file to read")
		return m, nil
	}
	if !isMarkdown(path) {
		m.setError(path + " is not a markdown file")
		return m, nil
	}
	title := path
	if commit != "" {
		title += " @ " + abbrevHash(commit)
	}
	// The margin marks come out of the diff already on screen, so reading a
	// file costs one command rather than two.
	var changed map[int]bool
	if f != nil && f.patch != nil {
		changed = f.patch.ChangedNewLines()
	}
	repo, gen := m.repo, m.gen
	return m, func() tea.Msg {
		text, err := repo.FileContent(commit, path)
		if err != nil {
			return markdownMsg{title: title, gen: gen, err: err}
		}
		return markdownMsg{title: title, gen: gen, text: text, changed: changed}
	}
}

// doViewFile opens the whole file the cursor is in, on one side or the other
// of the change.
//
// It opens at the line being read, so coming here from a line of the diff
// lands on that line in the file rather than at the top of it: the question
// the diff raised is about the code around this line, and this is the answer
// to it.
func (m Model) doViewFile(side fileSide) (tea.Model, tea.Cmd) {
	path, commit, f := m.fileInView()
	if path == "" {
		m.setError("No file to read")
		return m, nil
	}

	// The two sides of the change, named the way the review names them: the
	// working tree and the base, or a commit and its parent.
	at, base := "", m.base.Ref
	if commit != "" {
		at, base = commit, abbrevHash(commit)+"^"
	}
	if side == sideBefore {
		at = m.base.SHA
		if commit != "" {
			at = commit + "^"
		}
	}

	// Where in the file to open, and which of its lines the review touches,
	// both counted on the side being read.
	line, changed := m.viewAnchor(f, side)

	repo, gen, want := m.repo, m.gen, path
	return m, func() tea.Msg {
		text, err := repo.FileContent(at, want)
		if err != nil {
			return fileMsg{path: want, side: side, gen: gen, err: err}
		}
		return fileMsg{
			path: want, base: base, side: side, at: line,
			gen: gen, text: text, changed: changed,
		}
	}
}

// viewAnchor is the line the reader should open on and the lines of that side
// the review touches, both read off the diff already on screen — so opening
// the file costs one command and no second look at the change.
func (m *Model) viewAnchor(f *reviewFile, side fileSide) (int, map[int]bool) {
	changed := map[int]bool{}
	if f == nil {
		return 0, changed
	}
	for _, l := range f.lines {
		switch {
		case side == sideBefore && l.Kind == '-':
			changed[l.Old] = true
		case side == sideNow && l.Kind == '+':
			changed[l.New] = true
		}
	}

	line := 0
	if _, l, ok := m.view.line(); ok {
		if side == sideBefore {
			line = l.Old
		} else {
			line = l.New
			if line == 0 {
				// A removed line has no line of its own on this side; the
				// seam is where it used to be, which is where to look.
				line = l.Seam
			}
		}
	}
	return line, changed
}

// onFile puts the reader up once the file arrives.
func (m Model) onFile(msg fileMsg) (tea.Model, tea.Cmd) {
	m.mouse.clear()
	if msg.gen != m.gen {
		return m, nil
	}
	if msg.err != nil {
		m.setError(msg.path + ": " + firstLine(msg.err.Error()))
		return m, nil
	}
	m.file = newFileView(msg.path, msg.base, msg.side, msg.text, msg.changed, m.width, m.height)
	m.file.goToLine(msg.at)
	return m, nil
}

// fileKey drives the reader: scrolling, hopping between the runs the review
// touched, switching sides, and the ways out.
func (m Model) fileKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m.doQuit()
	case key.Matches(msg, m.keys.Cancel), key.Matches(msg, m.keys.QuitKey):
		m.mouse.clear()
		m.file = nil
		return m, nil
	case key.Matches(msg, m.keys.ViewKey):
		if m.file.side == sideNow {
			// Reading it already; v again puts it away, the way m does.
			m.mouse.clear()
			m.file = nil
			return m, nil
		}
		return m.switchSide(sideNow)
	case key.Matches(msg, m.keys.ViewOldKey):
		if m.file.side == sideBefore {
			m.mouse.clear()
			m.file = nil
			return m, nil
		}
		return m.switchSide(sideBefore)
	case key.Matches(msg, m.keys.EditKey):
		// Reading a file and then editing it is one thought, so it does not
		// need to be two trips through the window.
		m.file = nil
		return m.doEdit()
	case key.Matches(msg, m.keys.Up):
		m.file.move(-1)
	case key.Matches(msg, m.keys.Down):
		m.file.move(1)
	case key.Matches(msg, m.keys.Left):
		m.file.scrollH(-hScrollStep)
	case key.Matches(msg, m.keys.Right):
		m.file.scrollH(hScrollStep)
	case key.Matches(msg, m.keys.PageUp):
		m.file.move(-max(m.file.body()-1, 1))
	case key.Matches(msg, m.keys.PageDown):
		m.file.move(max(m.file.body()-1, 1))
	case key.Matches(msg, m.keys.Home):
		m.file.moveTo(0)
	case key.Matches(msg, m.keys.End):
		m.file.moveTo(len(m.file.lines) - 1)
	case key.Matches(msg, m.keys.NextHunk):
		m.file.jump(1)
	case key.Matches(msg, m.keys.PrevHunk):
		m.file.jump(-1)
	}
	return m, nil
}

// switchSide reads the other version of the file being read, keeping the line
// as nearly as the two sides allow: the point of the gesture is to see what
// this line used to say.
func (m Model) switchSide(side fileSide) (tea.Model, tea.Cmd) {
	m.mouse.clear()
	line := m.file.cursor + 1
	if f := m.fileFor(m.file.path); f != nil {
		line = carryLine(f, m.file.side, side, line)
	}
	m.file = nil
	mdl, cmd := m.doViewFile(side)
	return mdl, tea.Batch(cmd, func() tea.Msg { return fileLineMsg{line} })
}

// fileLineMsg puts the reader on a line once the other side has arrived.
type fileLineMsg struct{ line int }

// carryLine moves a line number from one side of a file's change to the other,
// using the diff already on screen.
//
// A line both sides have is carried exactly. A line only one side has was part
// of an edit, and its counterpart is the line it replaced: the nth line the
// change added stands where the nth line it removed used to, which is the
// pairing the diff itself draws one above the other. Failing that — a pure
// insertion, with nothing on the other side to have replaced — it falls back
// to the last line before it that both sides have, which is where a reader
// would start looking.
func carryLine(f *reviewFile, from, to fileSide, line int) int {
	side := func(l git.DiffLine) (here, there int) {
		if from == sideBefore {
			return l.Old, l.New
		}
		return l.New, l.Old
	}

	best, hunk, rank := 0, -1, -1
	for _, l := range f.lines {
		if !isCodeLine(l.Kind) {
			continue
		}
		here, there := side(l)
		if here == line {
			if there > 0 {
				return there // both sides have it
			}
			hunk = l.Hunk
			break
		}
		if here > 0 && there > 0 {
			best = there
		}
	}
	if hunk < 0 {
		return best
	}

	// Where the line stands among the ones its side of this hunk added, and
	// what stands at the same place among the ones the other side lost.
	var mine, theirs []int
	for _, l := range f.lines {
		if l.Hunk != hunk || !isCodeLine(l.Kind) {
			continue
		}
		switch here, there := side(l); {
		case here > 0 && there == 0:
			if here == line {
				rank = len(mine)
			}
			mine = append(mine, here)
		case there > 0 && here == 0:
			theirs = append(theirs, there)
		}
	}
	switch {
	case rank < 0 || len(theirs) == 0:
		return best
	case rank < len(theirs):
		return theirs[rank]
	}
	return theirs[len(theirs)-1]
}

// fileFor is a file's section of the stack, by path.
func (m *Model) fileFor(path string) *reviewFile {
	for _, f := range m.view.files {
		if f.path == path {
			return f
		}
	}
	return nil
}

func (m Model) onMarkdown(msg markdownMsg) (tea.Model, tea.Cmd) {
	m.mouse.clear()
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
func (m Model) markdownKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch {
	case key.Matches(msg, m.keys.Quit):
		return m.doQuit()
	case key.Matches(msg, m.keys.Cancel),
		key.Matches(msg, m.keys.QuitKey),
		key.Matches(msg, m.keys.MarkdownKey):
		m.mouse.clear()
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

// ---------------------------------------------------------------------------
// Status
// ---------------------------------------------------------------------------

func (m *Model) clearStatus()   { m.status, m.statusOK, m.statusEr, m.statusWa = "", false, false, false }
func (m *Model) setOK(s string) { m.status, m.statusOK, m.statusEr, m.statusWa = s, true, false, false }
func (m *Model) setWarn(s string) {
	m.status, m.statusOK, m.statusEr, m.statusWa = s, false, false, true
}
func (m *Model) setError(s string) {
	m.status, m.statusOK, m.statusEr, m.statusWa = s, false, true, false
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
