package ui

import (
	"errors"
	"maps"
	"path/filepath"
	"runtime"
	"slices"
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
	"github.com/discobox-ai/review/pkg/source"
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
// colors. This used to be a budget over the whole review, and discobox-review's own
// source tripped it — every file went plain, and nothing said why.
const maxLexLines = 10000

// Model is the review window.
type Model struct {
	// src is the change under review, and carrier and wt what it can do
	// besides: carry a comment through a commit, and change the working
	// tree. Each is nil when the source cannot. store is where the review is
	// kept, nil when it is kept nowhere.
	src     source.Source
	carrier source.Carrier
	store   source.Store
	wt      source.Worktree

	// embedded is the window drawn inside somebody else's program, which
	// closing hands back to rather than ending. poll is how often the review
	// is looked at for changes made elsewhere, and none at all when it is
	// zero.
	embedded bool
	poll     time.Duration

	// originX and originY are where the window's top left corner is on the
	// terminal, which the mouse reports its positions against.
	originX, originY int

	theme Theme
	st    *styles
	keys  keyMap

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

	// incomplete is the source's word that the diff is not the whole of the
	// change, kept for as long as the scan that said it.
	incomplete string

	// basePinned is set once the base has been chosen for this session — on
	// the command line, or with b — and stops the saved review handing it
	// back to whatever it was measured from last time.
	basePinned bool
	// measured is the base the last scan was asked to measure from, and
	// baseToSave a base chosen with b that is written once a scan has found
	// it: until then nothing says it names anything.
	measured   string
	baseToSave bool
	// apart is a source that is not the tree the review is of (Options.Apart).
	apart bool

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
	folds      map[string]bool  // the folds the reader has opened or closed by hand
	hunks      map[hunkKey]bool // the hunks folded away inside files that are open
	gapsOpen   map[hunkKey]bool // the unchanged stretches between hunks that have been opened
	rescanning bool

	// rescanAgain is a scan asked for while one was out: what it is to read
	// may have moved since that one began, so another goes once it is in.
	// See [Model.scan].
	rescanAgain bool

	// hideDone takes the files the reader has approved off the screen
	// altogether, leaving what they have yet to. Folding puts a file out of
	// the way; this puts it out of the review.
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

	// cardHidden puts the review card's summary away, leaving the files in
	// the groups it sorts them into.
	cardHidden bool
	file       *fileView
	quit       bool

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

// Options are what the window is told when it is made, beyond the source
// it reads.
type Options struct {
	// Base is the ref the review is measured from, or empty to take whatever
	// the saved review names and, failing that, whatever the source works
	// out.
	Base string

	// Author is the name the comments and approvals written here are filed
	// under, which the caller has already resolved — the window never
	// guesses it.
	Author string

	// Store is where the review is kept: its comments, its approvals, the
	// base. The window over a Git working tree is given the repository; a
	// window over a pull request, a directory of its own (see pkg/store).
	// With none, the change is read with no review over it — nothing to
	// approve, nowhere to comment — and the window offers neither. Nor does
	// it with no Author: an approval or a remark is somebody's, and the
	// window will not file one under nobody.
	Store source.Store

	// Embedded is the window drawn inside another program. Closing it sends
	// [ClosedMsg] for that program to act on, rather than ending it.
	Embedded bool

	// Poll is how often the review is checked for changes made outside the
	// window: a second when it is zero, and never when it is negative. It is
	// only ever the review that is polled — never the source's diff, which is
	// read again when [RescanMsg] arrives.
	Poll time.Duration

	// Reserved are keys the window must leave alone, because the program it
	// is drawn in has them: a leader key, say. They are taken out of every
	// binding that has them, and out of the help with it.
	Reserved []string

	// Theme is the palette, [DefaultTheme] when it is the zero Theme.
	Theme Theme

	// Apart is a source that is not the tree the review is of: one commit of
	// a branch (discobox-review -head), read over the review its working
	// tree keeps. The window judges the review's approvals against what it
	// shows, on screen only: the working tree is what the commands measure
	// them against, and a verdict reached on another tree, saved, would
	// strike down what holds there. Nor does it save the base it was given.
	// What the person at it does — an approval, a comment — is saved as ever.
	Apart bool
}

// New builds the window over a source.
//
// What it offers follows from what it is given. With [Options.Store] and an
// [Options.Author] the review is kept there; missing either, the change is
// read with no review over it: nothing to approve and nowhere to comment, and
// the window offers neither. A source that is also a [source.Worktree] can
// change branch, open a file in the editor and be measured from any base; one
// that is not has those keys left out, of the help as well.
//
// The window reads its source first from [Model.Init], and every later
// reading waits for that one: a program embedding the window runs Init's
// command, as a Bubble Tea program does with the model it is given.
func New(src source.Source, opts Options) Model {
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

	theme := opts.Theme.filled()
	poll := opts.Poll
	switch {
	case poll == 0:
		poll = reloadInterval
	case poll < 0:
		poll = 0
	}

	m := Model{
		src:        src,
		embedded:   opts.Embedded,
		poll:       poll,
		theme:      theme,
		st:         newStyles(theme, true),
		history:    newCommitList(),
		view:       newReviewView(),
		editor:     ta,
		focus:      focusReview,
		baseRef:    opts.Base,
		basePinned: opts.Base != "",
		apart:      opts.Apart,
		context:    defaultContext,
		workBy:     map[string]*reviewFile{},
		state:      map[string]git.State{},
		commits:    map[string][]*reviewFile{},
		carries:    map[string]map[string]*git.Patch{},
		folds:      map[string]bool{},
		histOpen:   true,
		hunks:      map[hunkKey]bool{},
		gapsOpen:   map[hunkKey]bool{},
		rv:         review.New(""),
		author:     opts.Author,
		store:      opts.Store,
		rescanning: true, // Init's
		mouse:      newMouseState(),
	}
	if m.author == "" {
		m.store = nil
	}
	m.carrier, _ = src.(source.Carrier)
	m.wt, _ = src.(source.Worktree)
	m.keys = newKeyMap().without(m.missing(), opts.Reserved)
	m.view.readOnly = m.store == nil
	return m
}

// errNoStore is what a write fails with when the window keeps no review. The
// window offers no command that writes one, so it is never seen but by
// something that reached past the keys.
var errNoStore = errors.New("nothing here keeps a review")

// offerer is [Model.offer] for a reader's hint line, which is one run of
// hints separated the way the status bar's are.
func (m Model) offerer() func(string) string {
	return func(hints string) string { return m.offer(hints, " · ") }
}

// missing is what the source cannot do, as the commands that would do it.
func (m Model) missing() lacks {
	return lacks{review: m.store == nil, worktree: m.wt == nil}
}

func (m Model) Init() tea.Cmd {
	return tea.Batch(m.rescanCmd(), textarea.Blink, m.reloadTick())
}

type reloadTickMsg struct{}

// reloadTick waits for the next look at the review. There is nothing to look
// at without a store, and nothing is asked for when polling is off.
func (m Model) reloadTick() tea.Cmd {
	if m.store == nil || m.poll <= 0 {
		return nil
	}
	return tea.Tick(m.poll, func(time.Time) tea.Msg { return reloadTickMsg{} })
}

// ClosedMsg is what an embedded window sends when it is closed — q, Ctrl-C —
// in place of ending the program. Whatever is drawing it takes it away.
type ClosedMsg struct{}

// RescanMsg has the window read its source again, as R does. A program
// embedding the window sends it when it knows the source has moved — a pull
// request's head has — since the window polls only its review, and never the
// source.
type RescanMsg struct{}

// Step is [Model.Update] for a program holding the window as a Model rather
// than as a [tea.Model]: the same, with the window handed back as what it is.
func (m Model) Step(msg tea.Msg) (Model, tea.Cmd) {
	next, cmd := m.Update(msg)
	return next.(Model), cmd //nolint:errcheck // Update always returns a Model
}

// SetSize gives the window the room it has to draw in. It is the same as
// sending it a [tea.WindowSizeMsg], for a program that lays the window out
// inside its own.
func (m Model) SetSize(width, height int) Model {
	next, _ := m.Step(tea.WindowSizeMsg{Width: width, Height: height})
	return next
}

// SetOrigin says where on the terminal the window's top left corner is drawn,
// for a program that lays it out anywhere but there. The mouse reports where
// it is on the terminal, and the window takes this off before it looks: a
// press outside the window is not its to answer.
func (m Model) SetOrigin(x, y int) Model {
	m.originX, m.originY = x, y
	return m
}

// Typing reports whether keys are going into a text field — a comment being
// written, a dialog's input — where every letter is text. A program holding
// keys of its own can let them through to the window while it is.
func (m Model) Typing() bool {
	return m.focus == focusCompose || (m.dialog != nil && m.dialog.typing())
}

// ---------------------------------------------------------------------------
// Messages and commands
// ---------------------------------------------------------------------------

type rescanMsg struct {
	scan  git.Scan
	files []*reviewFile
	err   error

	// base is the ref the scan was asked to measure from, empty for the
	// source's own choice.
	base string
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

// scan reads the source again, unless a reading is already out. Two readings
// out at once could come back in either order, and the one asked for first —
// of a pull request's old head, say — would be the one left on screen. So a
// scan asked for while one is out waits, and one goes once that is in,
// however many were asked for meanwhile.
//
// Init's scan is the first one out: [New] starts with rescanning set for it.
func (m *Model) scan() tea.Cmd {
	if m.rescanning {
		m.rescanAgain = true
		return nil
	}
	m.rescanning = true
	return m.rescanCmd()
}

// rescanCmd asks git everything, and builds the stack, off the render path.
// Parsing and lexing a whole review is the one expensive thing discobox-review does,
// and doing it here means a big change never makes the window stutter.
func (m Model) rescanCmd() tea.Cmd {
	src, baseRef, context := m.src, m.baseRef, m.context
	return func() tea.Msg {
		scan, err := src.Scan(baseRef, context)
		scan = drawable(scan)
		if err != nil {
			return rescanMsg{scan: scan, err: err, base: baseRef}
		}
		states := map[string]git.State{}
		for _, f := range scan.Files {
			states[f.Path] = f.State
		}
		files := buildFiles(scan.Diff, scan.Pending, states)
		addGaps(src, "", files)
		return rescanMsg{scan: scan, files: files, base: baseRef}
	}
}

// commitCmd loads one commit's own diff, and the diffs that carry comments in
// and out of it.
func (m Model) commitCmd(hash string) tea.Cmd {
	src, carrier, gen, context := m.src, m.carrier, m.gen, m.context
	// Every path this commit touched needs carrying, because a comment can be
	// written against any line of it, and so does every path already carrying
	// one, because those have to be found again in here.
	commented := map[string]bool{}
	for _, t := range m.rv.Threads {
		commented[t.Path] = true
	}
	return func() tea.Msg {
		text, err := src.CommitDiff(hash, context)
		if err != nil {
			return commitFilesMsg{hash: hash, gen: gen, err: err}
		}
		files := buildFiles(text, "", nil)
		addGaps(src, hash, files)
		if carrier == nil {
			return commitFilesMsg{hash: hash, gen: gen, files: files}
		}

		want := make(map[string]bool, len(files)+len(commented))
		for _, f := range files {
			want[f.path] = true
		}
		for p := range commented {
			want[p] = true
		}
		carry := make(map[string]*git.Patch, len(want))
		for p := range want {
			out, err := carrier.CarryDiff(hash, p)
			if err != nil {
				continue
			}
			carry[p] = git.ParsePatch(out)
		}
		return commitFilesMsg{hash: hash, gen: gen, files: files, carry: carry}
	}
}

// drawable is a scan with what the window only shows made printable: the
// base's names, the commits, the source's word on an incomplete diff. What is
// used to ask the source anything — the branch the review is kept under, the
// hashes — is left as the source said it, and made printable where it is
// drawn.
func drawable(s source.Scan) source.Scan {
	s.Base.Ref, s.Base.Commit, s.Base.Err = printable(s.Base.Ref), printable(s.Base.Commit), printable(s.Base.Err)
	s.Incomplete = printable(s.Incomplete)
	commits := make([]source.Commit, len(s.Commits))
	for i, c := range s.Commits {
		c.Short, c.Subject, c.Author, c.When = printable(c.Short), printable(c.Subject), printable(c.Author), printable(c.When)
		commits[i] = c
	}
	s.Commits = commits
	return s
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
			blocks:  review.HunkDigests(p),
			pending: uncommitted[p.Path], state: states[p.Path],
		}
		f.adds, f.dels = p.Stat()
		f.texts = make([]string, len(f.lines))
		for i, l := range f.lines {
			if isCodeLine(l.Kind) {
				f.texts[i] = expandTabs(printableCode(codeText(l.Text)))
			} else {
				f.texts[i] = printable(l.Text)
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

// maxGapBytes is the largest file whose unchanged lines are offered. Every
// changed file under it is held in memory whole for as long as its diff is.
const maxGapBytes = 4 << 20

// addGaps reads each file as the diff's new side has it — the working tree, or
// a commit — and marks the stretches of it the diff leaves out. A file that
// cannot be read has none marked, and is drawn the way it always was.
//
// The files are read several at a time. From a working tree that hardly
// matters; from a source on the far side of a network, one file after
// another is a round trip each, and a review of forty files took seconds.
func addGaps(src source.Source, commit string, files []*reviewFile) {
	var wg sync.WaitGroup
	slot := make(chan struct{}, gapReaders)
	for _, f := range files {
		// Nothing is read for a file with no hunks — a binary, a change of
		// mode — since it has no gaps to count and may be very large.
		if f.patch == nil || len(f.patch.Hunks) == 0 || noNewSide(f.patch) {
			continue
		}
		wg.Add(1)
		go func(f *reviewFile) {
			defer wg.Done()
			slot <- struct{}{}
			defer func() { <-slot }()
			// The text is kept, to open a gap from without reading again, so
			// a file too big to be worth keeping goes without its gaps.
			if text, err := src.FileContent(commit, f.path); err == nil && len(text) <= maxGapBytes {
				f.addGaps(text)
			}
		}(f)
	}
	wg.Wait()
}

// gapReaders is how many files addGaps reads at once: enough to hide a
// network's round trips, few enough not to look like a flood to a service
// that limits how fast it is asked.
const gapReaders = 8

// noNewSide reports whether there is no file to read the unchanged lines of a
// patch from: it is of a symbolic link, whose content is the name it points
// at, so that reading it from the working tree would read the file at the
// other end instead; or it deletes the file — and a file replaced by a link is
// told as exactly that, a deletion and then a new link under the same name.
//
// Git says both on lines of its own in front of the diff. They are matched by
// how they begin, since the lines that name the file end in whatever the file
// is called.
func noNewSide(p *git.Patch) bool {
	for _, l := range p.Preamble {
		if strings.HasPrefix(l, "deleted file mode ") {
			return true
		}
		for _, mode := range []string{"index ", "new file mode ", "old mode ", "new mode "} {
			if strings.HasPrefix(l, mode) && strings.HasSuffix(l, " 120000") {
				return true
			}
		}
	}
	return false
}

// lexAll highlights every file, several at a time.
//
// One file's colors have nothing to do with another's, and this already runs
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
		msg, inside := m.local(msg)
		if !inside {
			return m, nil
		}
		return m.onMouse(msg)

	case tea.BackgroundColorMsg:
		// The terminal has said what color it is. Only the diff bands care,
		// but they are most of what the pane draws, so the palette is built
		// again rather than patched.
		if dark := msg.IsDark(); dark != m.dark {
			m.dark, m.st = dark, newStyles(m.theme, dark)
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
		return m, tea.Batch(m.pickUpChanges(), m.reloadTick())

	case RescanMsg:
		cmd := m.scan()
		return m, cmd

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
				st.SetHunks(p, msg.by, msg.hunks[p])
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
			cmd := m.scan()
			return m, cmd
		}
		m.setOK(msg.text)
		if msg.rescan {
			cmd := m.scan()
			return m, cmd
		}
		return m, nil

	case tea.KeyPressMsg:
		return m.onKey(msg)
	}
	return m, nil
}

// doQuit closes the window: the program with it when the window is the
// program, and otherwise just the window, by telling whatever it is drawn in.
func (m Model) doQuit() (tea.Model, tea.Cmd) {
	if m.embedded {
		return m, func() tea.Msg { return ClosedMsg{} }
	}
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
	var again tea.Cmd
	if m.rescanAgain {
		m.rescanAgain = false
		again = m.scan()
	}
	if msg.err != nil {
		m.setError(firstLine(msg.err.Error()))
		return m, again
	}

	m.branch = msg.scan.Branch
	m.merging = msg.scan.Merging
	m.base = msg.scan.Base
	m.measured = msg.base
	m.incomplete = msg.scan.Incomplete
	if msg.scan.Base.Err != "" {
		m.setError(msg.scan.Base.Err)
	}

	m.work = msg.files
	m.reopenGaps("", msg.files)
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
	if m.baseToSave && msg.base == m.baseRef {
		// The base chosen with b, now that a scan has measured from it.
		m.baseToSave = false
		if m.baseFound() && m.mutate(func(*review.State) {}) {
			if m.baseRef == "" {
				m.setOK("Base: whatever this branch is built on")
			} else {
				m.setOK("Base: " + m.baseRef)
			}
		}
	}
	if !m.basePinned && m.baseRef != msg.base {
		// The review names a base this scan was not measured from — the
		// first scan of a session goes out before the review is read, and
		// a command may have moved it since — so it is read again from
		// there. It does not loop: the next one is measured from it.
		again = tea.Batch(again, m.scan())
	}
	m.rebuild()

	if !m.paneVisible(m.focus) {
		m.focus = focusReview
		m.editor.Blur()
	}
	m.layout()
	return m, tea.Batch(m.selectedCommitCmd(), again)
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
	m.reopenGaps(msg.hash, msg.files)
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
	if m.store == nil {
		// Nowhere a review is kept: the change is read with none over it.
		m.rv, m.rvErr = review.New(m.branch), ""
		return
	}
	if err := review.Adopt(m.store, m.branch, lockWait); err != nil {
		// What is at the path may be another branch's review still, so it is
		// left unread; the next scan asks again.
		m.setError("Reading the review: " + firstLine(err.Error()))
		m.keepOwnReview()
		return
	}
	if saved := m.readReview(); !m.basePinned {
		m.baseRef = saved
	}
	m.reconcile(true)
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
	data, ok := m.store.ReadStateFile(review.FileName)
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
	m.stamp = m.store.StateFileStamp(review.FileName)
	return m.rv.Base
}

// baseFound reports whether the base on screen is the one asked for, and the
// source found it: only such a base is written into the review.
func (m *Model) baseFound() bool {
	return m.measured == m.baseRef && m.base.Err == ""
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
	if m.store == nil {
		return errNoStore
	}
	var now *review.State
	err := review.Update(m.store, m.branch, wait, func(st *review.State) error {
		change(st)
		// A base chosen here, in this session, is the window's to say. Any
		// other is the file's: one set by a command a moment ago, which the
		// poll has not got to yet, must not be put back by this write. Nor
		// is one the source could not find, which would leave the review
		// measured from something nothing else here can read either: so it
		// waits for a scan that was measured from it and found it.
		if m.basePinned && !m.apart && m.baseFound() {
			st.Base = m.baseRef
		}
		now = st
		return nil
	})
	if err != nil {
		return err
	}
	m.rv, m.rvErr = now, ""
	m.stamp = m.store.StateFileStamp(review.FileName)
	if m.apart {
		// What was read back is the file's own verdict, not this tree's.
		m.reconcile(true)
	}
	if !m.basePinned && now.Base != m.baseRef {
		// The base moved under the window. Every diff on screen is measured
		// from the old one, which is the poll's business to put right: with
		// no stamp to recognize the file by, the next tick takes it for new.
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
	if !m.ready || m.store == nil {
		return nil
	}
	if stamp := m.store.StateFileStamp(review.FileName); stamp == m.stamp {
		return nil
	}
	base := m.readReview()
	moved := !m.basePinned && base != m.baseRef
	if moved {
		m.baseRef = base
	}
	// An approval that has to be judged — stamped, or struck down — is
	// judged against git as it is now, not as it was when this window last
	// looked. Whatever wrote the review may well have been answering an edit
	// to the tree, which nothing has told the window about: the diff on
	// screen is the one from before it, and going by that would strike down
	// an approval of the file as it stands for not being of the file as it
	// was. So the tree is read again first, and the scan's arrival does the
	// judging.
	stale := m.reconcile(false)
	m.rebuild()
	if moved || stale {
		// The base is what every diff on screen was taken against, so a
		// review that names a different one has to be read again from git
		// too.
		return m.scan()
	}
	return nil
}

// reconcile checks the approvals against the changes now in front of the
// reviewer, and says so when one has fallen.
//
// fresh says the changes in front of the reviewer have just been read from
// git. When they have not, nothing is decided: it reports whether there is
// anything to decide, and the caller reads git again before coming back.
func (m *Model) reconcile(fresh bool) (undecided bool) {
	if len(m.work) == 0 || m.store == nil {
		return false
	}
	digests := make(map[string]string, len(m.work))
	for _, f := range m.work {
		digests[f.path] = f.digest
	}
	// The same for the hunks: an approval of a run of lines that is no longer
	// in the change is dropped, rather than left to match again if the edit
	// that removed it is ever undone.
	present := make(map[string]map[string]bool, len(m.work))
	for _, f := range m.work {
		runs := map[string]bool{}
		for _, d := range slices.Concat(f.blocks...) {
			runs[d] = true
		}
		present[f.path] = runs
	}
	author := m.author
	verdict := func(st *review.State) (changed bool, nullified []string) {
		changed, nullified = st.Reconcile(digests)
		// Hunks signed off before a sign-off carried a name are this
		// reader's: see [review.State.ClaimHunks].
		if st.ClaimHunks(author) {
			changed = true
		}
		for path, runs := range present {
			if st.PruneHunks(path, runs) {
				changed = true
			}
		}
		return changed, nullified
	}
	// Asked of a copy, so that the screen changes only once the verdict is
	// saved: one that could not be written must not be shown as if it stood.
	probe, err := review.Parse(m.rv.Bytes())
	if err != nil {
		return false
	}
	changed, nullified := verdict(probe)
	if !changed {
		return false
	}
	if m.apart {
		// Shown, never saved: see Options.Apart.
		m.rv = probe
		return false
	}
	if !fresh {
		return true
	}
	// What fell, fell from the copy on screen. The file may have moved on
	// since that was read, so the same verdict is given again on what is
	// there now, and that is what is saved.
	//
	// Nobody asked for this write, so it does not make them wait for it: if
	// something else has the lock this instant, the stamp is forgotten and the
	// next tick reads the file as new and comes back here. Any other failure
	// is not going to mend itself in a second, and is said once.
	err = m.update(0, func(st *review.State) { verdict(st) })
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
	return false
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
	// A file the reader has approved is left out entirely while the filter is
	// on. The count of them goes with the panes, so that what they say about
	// the review as a whole stays true about the review as a whole.
	//
	// Their approval and nothing else decides it. Not anybody else's: what
	// is being hidden is what this reader has read. And not whether a
	// comment on the file is still open, which is what keeps a file from
	// folding: most open comments are a reviewer and an author still talking,
	// and a reader who has signed the file off has asked not to be shown it.
	// The title goes on counting the open ones, hidden or not.
	// The card's tallies are of the whole review, hidden files and all.
	all := slices.Clone(files)
	hidden, hiddenOpen := 0, 0
	if m.hideDone {
		kept := files[:0]
		for _, f := range files {
			if f.approved {
				hidden++
				hiddenOpen += f.open
				continue
			}
			kept = append(kept, f)
		}
		files = kept
	}

	files, m.view.card = m.groupByCard(files, all)
	m.view.setFiles(files, m.commit, label)
	m.view.hidden, m.view.hiddenOpen = hidden, hiddenOpen
	m.view.note = m.emptyNote()
	m.layout()
}

// emptyNote is what the review pane says when it has nothing to show, which is
// either the end of the job or the beginning of it.
func (m *Model) emptyNote() string {
	if m.hideDone && len(m.work) > 0 {
		return "Nothing left to read: you have approved every file.\n" +
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
		// The verdict a file is read by is the reader's own. Anybody else's
		// is shown beside it and decides nothing: a file an agent approved
		// is still one the person at the window has not read.
		f.approved = m.rv.Approved(f.path, m.author)
		f.nullified = m.rv.Nullified(f.path, m.author)
		mine := func(by string) bool { return by == m.author }
		f.others = slices.DeleteFunc(m.rv.Approvers(f.path), mine)
		f.othersFell = slices.DeleteFunc(m.rv.Fallen(f.path), mine)
		f.folded = m.foldedFor(f)
		f.signed = nil
		if m.commit == "" {
			// A hunk of one commit is not a hunk of the review, so there is
			// nothing of the kind to show while a commit is being read.
			f.signed = m.rv.ApprovedHunks(f.path, m.author)
		}
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
		// Last, because whether a hunk folds depends on what is said in it.
		m.markCore(f)
		f.shut = m.shutFor(f)
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

// hunkKey names a hunk in terms that outlast the diff being drawn again: the
// file, the diff it is a hunk of, and the first line it changes, numbered on
// both sides. Its index would not do, nor its header — asking for more or less
// context renumbers the one and rewrites the other, and merges hunks besides,
// while the first change in a hunk stays the line it was.
type hunkKey struct {
	commit, path string
	old, new     int
}

// hunkKeysIn names each of a file's hunks, by index into its patch.
func hunkKeysIn(commit string, f *reviewFile) []hunkKey {
	if f.patch == nil {
		return nil
	}
	keys := make([]hunkKey, len(f.patch.Hunks))
	named := -1
	for _, l := range f.lines {
		if l.Hunk > named && l.Hunk < len(keys) && (l.Kind == '+' || l.Kind == '-') {
			keys[l.Hunk] = hunkKey{commit: commit, path: f.id(), old: l.Old, new: l.New}
			named = l.Hunk
		}
	}
	return keys
}

// runKeys names, for each of a file's hunks, the runs of changed lines in it,
// each by the first line it changes. A fold made by hand is kept against these
// rather than against the hunk, for the reason an approval is: turn the context
// up until two hunks join and the hunk on screen is one nobody folded, half of
// which may never have been read.
func (m *Model) runKeys(f *reviewFile) [][]hunkKey {
	if f.patch == nil {
		return nil
	}
	keys := make([][]hunkKey, len(f.patch.Hunks))
	in := false
	for _, l := range f.lines {
		if l.Hunk < 0 || l.Hunk >= len(keys) {
			in = false
			continue
		}
		switch l.Kind {
		case '+', '-':
			if !in {
				keys[l.Hunk] = append(keys[l.Hunk], hunkKey{commit: m.commit, path: f.id(), old: l.Old, new: l.New})
				in = true
			}
		case ' ', '@':
			in = false
		}
	}
	return keys
}

// setHunkFold folds a hunk by hand, or opens it: every run in it, so that the
// hunk is whichever was asked for at any context.
func (m *Model) setHunkFold(f *reviewFile, hunk int, shut bool) {
	keys := m.runKeys(f)
	if hunk < 0 || hunk >= len(keys) {
		return
	}
	for _, k := range keys[hunk] {
		m.hunks[k] = shut
	}
}

// gapKeys names each of a file's gaps: one is known by the hunk that follows
// it, and the one after the last hunk by there being none.
func gapKeys(commit string, f *reviewFile) []hunkKey {
	return append(hunkKeysIn(commit, f), hunkKey{commit: commit, path: f.id(), old: -1, new: -1})
}

// reopenGaps opens, in a diff that has just been read, the gaps the reader had
// open in the one it replaces.
func (m *Model) reopenGaps(commit string, files []*reviewFile) {
	if len(m.gapsOpen) == 0 {
		return
	}
	for _, f := range files {
		if len(f.gaps) == 0 {
			continue
		}
		for g, k := range gapKeys(commit, f) {
			if m.gapsOpen[k] {
				f.setGap(g, true)
			}
		}
	}
}

// shutFor is the hunks of a file that are folded away.
//
// It is the file's rule one size down: a hunk that has been approved and has
// nothing outstanding said in it is done with, and folds. What the reader has
// folded or opened by hand overrides that. Inside an approved file the rule
// stands aside — the whole file has folded already, and somebody who opens it
// again wants to read it, not a list of its hunks.
//
// It is asked run by run, as an approval is, and a hunk is shut only when every
// run in it is: more context can join a hunk somebody folded to one they have
// not read, and the fold of the one must not take the other off the screen.
func (m *Model) shutFor(f *reviewFile) map[int]bool {
	if f.patch == nil || (len(m.hunks) == 0 && len(f.signed) == 0) {
		return nil
	}
	open := map[int]bool{}
	for at, ts := range f.threads {
		for _, t := range ts {
			if !t.Resolved && at < len(f.lines) {
				open[f.lines[at].Hunk] = true
			}
		}
	}
	var shut map[int]bool
	for h, runs := range m.runKeys(f) {
		all := len(runs) > 0
		for i, k := range runs {
			v, ok := m.hunks[k]
			if !ok {
				v = !f.approved && !open[h] && i < len(f.blocks[h]) && f.signed[f.blocks[h][i]]
			}
			if !v {
				all = false
				break
			}
		}
		if all {
			if shut == nil {
				shut = map[int]bool{}
			}
			shut[h] = true
		}
	}
	return shut
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
		m.dialog = textDialog("Discobox Review — keys", m.helpText())
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
		if !m.zoom && !m.historyFits() {
			// Nowhere to go: the layout would send the focus straight back.
			m.setWarn("The window is too short to show the history")
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
		m.dialog = textDialog("Discobox Review — keys", m.helpText())
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
	case key.Matches(msg, m.keys.CardKey):
		return m.doCard()
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
	cmd := m.scan()
	return m, cmd
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
	case key.Matches(msg, m.keys.Right), key.Matches(msg, m.keys.Toggle):
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
			m.setOK("No open comment below here" + m.hiddenOpenNote())
		}
	case key.Matches(msg, m.keys.PrevThread):
		m.unfoldOpenThreads()
		if !m.view.jumpThread(-1, m.rv) {
			m.setOK("No open comment above here" + m.hiddenOpenNote())
		}
	case key.Matches(msg, m.keys.Visual):
		m.view.toggleVisual()
		return m, nil
	case key.Matches(msg, m.keys.ZoomKey):
		return m.toggleZoom(m.focus)
	case key.Matches(msg, m.keys.Toggle):
		return m.doDisclose()
	default:
		return m, nil
	}
	return m, nil
}

// unfoldOpenThreads opens every folded file somebody is still waiting for an
// answer in, and every hunk folded by hand inside one. A file opens folded, so
// its conversations are not rows until it is read — and t, which goes looking
// for the next one, would otherwise walk straight past the files it was asked
// about and report that there were none.
func (m *Model) unfoldOpenThreads() {
	changed := false
	for _, f := range m.view.files {
		if f.open == 0 {
			continue
		}
		if f.folded {
			m.folds[f.id()], changed = false, true
		}
		for at, ts := range f.threads {
			if at >= len(f.lines) || !f.shut[f.lines[at].Hunk] {
				continue
			}
			if slices.ContainsFunc(ts, func(t *review.Thread) bool { return !t.Resolved }) {
				m.setHunkFold(f, f.lines[at].Hunk, false)
				changed = true
			}
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
	row, exact, found := m.showLine(f.id(), n, false)
	f = m.view.files[r.file]
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

// showLine opens whatever is keeping a numbered line of a file off the
// screen — the file's fold, the fold of the hunk it is in, and with gaps the
// unchanged stretch it is in between two hunks — and says which row draws it,
// or the nearest that does: see [reviewView.lineRow].
//
// The line is counted in the file as it now stands.
func (m *Model) showLine(path string, n int, gaps bool) (row int, exact, ok bool) {
	file := func() (int, *reviewFile) {
		fi, ok := m.view.byPath[path]
		if !ok {
			return -1, nil
		}
		return fi, m.view.files[fi]
	}
	fi, f := file()
	if f == nil {
		return 0, false, false
	}
	if f.folded {
		// A line of a folded file is not drawn anywhere to go to, and asking
		// for one by number is asking to see it.
		m.folds[f.id()] = false
		m.rebuild()
		if fi, f = file(); f == nil {
			return 0, false, false
		}
	}
	// The same for a folded hunk the line is in.
	for _, l := range f.lines {
		if l.New == n && (l.Kind == '+' || l.Kind == ' ') && l.Hunk >= 0 && f.shut[l.Hunk] {
			m.setHunkFold(f, l.Hunk, false)
			m.rebuild()
			if fi, f = file(); f == nil {
				return 0, false, false
			}
			break
		}
	}
	if gaps {
		keys := gapKeys(m.commit, f)
		for g, gp := range f.gaps {
			if gp.open || gp.n == 0 || n < gp.new || n >= gp.new+gp.n || g >= len(keys) {
				continue
			}
			m.gapsOpen[keys[g]] = true
			f.setGap(g, true)
			m.rebuild()
			if fi, f = file(); f == nil {
				return 0, false, false
			}
			break
		}
	}
	return m.view.lineRow(fi, n)
}

// doDisclose is Space, and Enter: it opens and shuts what the cursor is on, and
// makes nothing new. A key for looking at things should not start writing
// them, which is why a comment begins with c and with nothing else.
//
// What it reaches is the smallest thing the cursor is in. Inside a hunk that
// is the hunk, from any line of it; on a file's heading it is the file; and on
// a resolved conversation it is the conversation, which has a disclosure of
// its own. A row that is in no hunk — the note above a comment that has lost
// its line — falls back to the file.
func (m Model) doDisclose() (tea.Model, tea.Cmd) {
	r, ok := m.view.row()
	if !ok {
		return m, nil
	}
	if r.kind == rowStretch {
		// What lies between two core hunks opens, and shuts again, in place.
		m.view.stretchOpen[r.stretch] = !m.view.stretchOpen[r.stretch]
		i := m.view.cursor
		m.view.build()
		m.view.cursor = min(i, max(len(m.view.rows)-1, 0))
		m.view.clamp()
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
	if f, head, ok := m.cursorHunk(); ok {
		return m.doFoldHunk(f, head)
	}
	if f, g, ok := m.cursorGap(); ok {
		return m.doGap(f, g)
	}
	return m.doFold()
}

// cursorGap is the gap the cursor is in: on its row, on one of the lines it
// has let out, or in a conversation hung off one of those.
func (m *Model) cursorGap() (*reviewFile, int, bool) {
	r, ok := m.view.row()
	if !ok || r.file < 0 || (r.kind != rowDiff && r.kind != rowThread) {
		return nil, 0, false
	}
	f := m.view.files[r.file]
	g, ok := f.gapAt(f.primary(r.line))
	return f, g, ok
}

// doGap shows the lines between two hunks that the diff left out, or puts
// them away again. The cursor ends on the gap's own row either way, so the
// same key undoes it.
func (m Model) doGap(f *reviewFile, g int) (tea.Model, tea.Cmd) {
	// The box being written is anchored to a line by where the line is among
	// the file's lines, which is what this is about to change.
	if m.compose != nil {
		m.setWarn("Post or discard the comment being written first")
		return m, nil
	}
	keys := gapKeys(m.commit, f)
	if g >= len(keys) {
		return m, nil
	}
	open := !f.gaps[g].open
	if open {
		m.gapsOpen[keys[g]] = true
	} else {
		delete(m.gapsOpen, keys[g])
	}
	// The core's view of a file keeps gaps of its own: see Model.coreEntry.
	f.setGap(g, open)
	path := f.id()
	m.rebuild()
	m.view.goToDiffLine(path, f.gapRow(g))
	return m, nil
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
	m.folds[f.id()] = !f.folded
	m.rebuild()
	return m, nil
}

// cursorHunk is the hunk the cursor is in, as its file and the index of its
// header among that file's lines. A conversation hung off a line of a hunk is
// in the hunk; a file's heading is not.
func (m *Model) cursorHunk() (*reviewFile, int, bool) {
	r, ok := m.view.row()
	if !ok || r.file < 0 || (r.kind != rowDiff && r.kind != rowThread) {
		return nil, 0, false
	}
	f := m.view.files[r.file]
	head := f.hunkHead(f.primary(r.line))
	return f, head, head >= 0
}

// doFoldHunk opens or shuts one hunk and leaves the rest of the file as it
// was. The cursor ends on the header — the one row a folded hunk still has, so
// the same key pressed again opens it.
func (m Model) doFoldHunk(f *reviewFile, head int) (tea.Model, tea.Cmd) {
	hunk := f.lines[head].Hunk
	m.setHunkFold(f, hunk, !f.shut[hunk])
	path := f.id()
	m.rebuild()
	m.view.goToDiffLine(path, head)
	return m, nil
}

// doApproveHunk signs off one hunk, or takes that back.
//
// What is recorded is the runs of changed lines in it, so the approval is of
// those lines whatever the context around them is set to. Signing off the last
// hunk of a file signs off the file, and taking one hunk back out of an
// approved file leaves the others standing — "all of it but this".
func (m Model) doApproveHunk(f *reviewFile, head int) (tea.Model, tea.Cmd) {
	hunk := f.lines[head].Hunk
	if hunk >= len(f.blocks) || len(f.blocks[hunk]) == 0 {
		return m, nil
	}
	// Deciding about a hunk hands its fold back to the rule, as it does for a
	// file — and gets it back if the decision is not saved.
	was := map[hunkKey]bool{}
	if keys := m.runKeys(f); hunk < len(keys) {
		for _, k := range keys[hunk] {
			if v, set := m.hunks[k]; set {
				was[k] = v
			}
			delete(m.hunks, k)
		}
	}
	undo := func() { maps.Copy(m.hunks, was) }
	path, digest, author := f.path, f.digest, m.author
	key := f.id()
	withdraw := f.approved || f.hunkApproved(hunk)
	order := m.view.paths()
	row, _ := m.view.diffRowAt(key, head)
	screen := row - m.view.offset

	// The decision is made against the review as it is read back inside
	// mutate, not as it was last drawn: something else — an agent, another
	// window — may have signed off or taken back a different hunk since, and
	// writing this window's picture of the file over that would undo it.
	//
	// Run by run, not hunk by hunk: at this context a hunk may be two that
	// were apart when one of them was signed off, and that half is still
	// signed off though the hunk as drawn is not.
	all := f.blocks
	left, promoted := 0, false
	apart := m.apart
	if !m.mutate(func(st *review.State) {
		approved, signed := st.Approved(path, author), st.ApprovedHunks(path, author)
		var keep []string
		if apart {
			// What the file says is the tree's (Options.Apart): an approval of
			// another digest is no approval here, and a run this commit does
			// not show is the tree's to keep.
			if v := st.VerdictOf(path, author); v != nil && v.Digest != "" && v.Digest != digest {
				approved = false
			}
			shown := map[string]bool{}
			for _, d := range slices.Concat(all...) {
				shown[d] = true
			}
			for d := range signed {
				if !shown[d] {
					keep = append(keep, d)
				}
			}
		}
		for h, blocks := range all {
			whole := true
			for _, d := range blocks {
				ok := approved || signed[d]
				if h == hunk {
					ok = !withdraw
				}
				if ok {
					keep = append(keep, d)
				} else {
					whole = false
				}
			}
			if !whole {
				left++
			}
		}
		switch {
		case withdraw:
			// Only a file that is approved has an approval to take back; one
			// whose approval already fell keeps saying that it did.
			if approved {
				st.Unapprove(path, author)
			}
		case left == 0:
			st.Approve(path, digest, author)
			promoted = true
		}
		st.SetHunks(path, author, keep)
	}) {
		undo()
		return m, nil
	}
	switch {
	case withdraw:
		m.setOK("Approval withdrawn from this hunk of " + path)
	case promoted:
		// Deciding about a file hands its fold back to the rule too, in
		// both of its entries when the card's core has it.
		delete(m.folds, path)
		delete(m.folds, coreKey(path))
		m.rebuild()
		m.setOK("Approved " + path)
	default:
		m.setOK("Approved a hunk of " + path + " · " + plural(left, "hunk", "hunks") + " to go")
	}
	// The rows were laid out again around the file's heading, which may be
	// far above the pane. It is the hunk that changed shape, so it is the
	// hunk's header that keeps its place on the screen.
	m.view.hold(key, head, screen)
	if withdraw || !m.advance(order, key, hunk) {
		m.view.goToDiffLine(key, head)
	}
	return m, nil
}

// advance takes the cursor on from what was just approved to the next thing
// of the same size still waiting for the reader's verdict, so that a pressed
// again and again reads the review from top to bottom. After a hunk that is
// the next hunk nobody has signed off, in this file or a later one; after a
// file, the heading of the next file. Whatever it lands on is opened if it was
// folded, since it is about to be read.
//
// Arriving in another file puts its heading at the top of the pane, which is
// where reading a file starts. A hunk in the same file is scrolled to only
// when it is off the screen: the hunks before it were just read, and hauling
// them out of sight every time one folds throws away the file they make sense
// of.
//
// order is the files as they were drawn before the approval, since hiding
// what is approved may have taken the one just approved off the screen. Past
// the last file it goes on from the top, and reports false when there is
// nothing left to go to.
func (m *Model) advance(order []string, path string, hunk int) bool {
	at := slices.Index(order, path)
	if at < 0 {
		return false
	}
	// The core of the change is read before the rest of it.
	if m.commit == "" && m.nextCore(order, at, hunk) {
		return true
	}
	// A commit's hunks are not the review's, so there only files are next.
	byHunk := hunk >= 0 && m.commit == ""
	// The core's view of a file draws only its core hunks, so the rest of
	// them are reached in the file's own entry, under the rest.
	if f := m.shown(path); f != nil && byHunk && !f.approved && !f.coreView {
		if h, ok := f.nextHunk(hunk + 1); ok {
			return m.toHunk(f, h)
		}
	}
	for i := 1; i <= len(order); i++ {
		p := order[(at+i)%len(order)]
		f := m.shown(p)
		if f == nil || f.approved || f.gone || f.coreView {
			continue
		}
		h, ok := f.nextHunk(0)
		// Coming round to the file just worked in is not arriving in it: the
		// hunks above stay where they are, as they would going down.
		if p == path {
			return byHunk && ok && m.toHunk(f, h)
		}
		if f.folded {
			m.folds[p] = false
			m.rebuild()
		}
		if !m.view.jumpToFile(p) {
			return false
		}
		// A file with no hunk to sign off — a binary one, say — is still
		// waiting, and its heading is where a approves it. Having got this
		// far the cursor has moved whether or not the hunk is found.
		if byHunk && ok {
			m.toHunk(f, h)
		}
		return true
	}
	return false
}

// shown is the file of that name on the screen, or nil when it is not there.
func (m *Model) shown(path string) *reviewFile {
	if i, ok := m.view.byPath[path]; ok {
		return m.view.files[i]
	}
	return nil
}

// nextHunk is the first of a file's hunks from one on that is still waiting
// to be approved.
func (f *reviewFile) nextHunk(from int) (int, bool) {
	for h := max(from, 0); h < len(f.blocks); h++ {
		if len(f.blocks[h]) > 0 && !f.hunkApproved(h) {
			return h, true
		}
	}
	return 0, false
}

// toHunk puts the cursor on a hunk's header, opening the hunk if it was folded
// by hand. A header already on the screen is not scrolled to.
func (m *Model) toHunk(f *reviewFile, h int) bool {
	head := slices.IndexFunc(f.lines, func(l git.DiffLine) bool { return l.Kind == '@' && l.Hunk == h })
	if head < 0 {
		return false
	}
	if f.shut[h] {
		// Opening it lays the rows out again around the file's heading, so
		// the header is held where it was drawn.
		row, _ := m.view.diffRowAt(f.id(), head)
		screen := row - m.view.offset
		m.setHunkFold(f, h, false)
		m.rebuild()
		m.view.hold(f.id(), head, screen)
	}
	return m.view.goToDiffLine(f.id(), head)
}

// foldFile discloses one file by name, for the mouse: the cursor is on the
// heading that was clicked, but a name says which file was meant however the
// rows have moved since the button went down.
func (m Model) foldFile(path string) (tea.Model, tea.Cmd) {
	i, ok := m.view.byPath[path]
	if !ok {
		return m, nil
	}
	m.folds[m.view.files[i].id()] = !m.view.files[i].folded
	m.rebuild()
	return m, nil
}

// hiddenOpenNote is what t and T add when they find nothing: that there are
// open comments they cannot reach, in files f has taken off the screen.
//
// f hides a file on the reader's approval alone, a comment still open on it or
// not, and t walks the rows there are. Without this the title would count a
// comment that the one key for finding comments said was not there.
//
// It counts what f took away and nothing else. A comment can be off the
// screen for other reasons — it is on a file the commit being read did not
// touch — and f again would not bring that one back.
func (m *Model) hiddenOpenNote() string {
	hidden := m.view.hiddenOpen
	if hidden <= 0 {
		return ""
	}
	return " · " + plural(hidden, "open comment", "open comments") + " in files f has hidden"
}

// doHideDone takes the files the reader has approved off the screen, or puts
// them back.
//
// Folding a file gets it out of the way and leaves it in the review, which is
// right while you are still reading. This is for the other half of the job:
// once most of a change has been signed off, what is left is the list of
// files still to read, and everything else is noise between them.
func (m Model) doHideDone() (tea.Model, tea.Cmd) {
	m.hideDone = !m.hideDone
	path := m.view.keyAt()
	m.rebuild()
	if m.hideDone {
		// The file being read may have been one of the ones that went.
		if !m.view.jumpToFile(path) {
			m.view.clamp()
		}
		if n := m.view.hidden; n == 0 {
			m.setOK("You have approved nothing yet · f shows every file again")
		} else {
			m.setOK("Hiding " + plural(n, "file", "files") + " you have approved · f shows them again")
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
	path := m.view.keyAt()

	kept := 0
	for _, f := range m.view.files {
		switch {
		case fold:
			m.folds[f.id()] = true
		case f.done():
			delete(m.folds, f.id())
			kept++
		default:
			m.folds[f.id()] = false
		}
	}
	// Unless there is nothing else left, in which case the second press has to
	// do something: a review where every file is done with would otherwise
	// have a key that looks broken.
	if !fold && kept == len(m.view.files) {
		for _, f := range m.view.files {
			m.folds[f.id()] = false
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
	for range int(focusCount) {
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
	cmd := m.scan()
	return m, cmd
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
	// From inside a hunk it is the hunk that is signed off, and from the
	// file's heading the file — the same reach Space has. A commit's hunks
	// are not the review's, so there it is the file from anywhere.
	if m.focus == focusReview && m.commit == "" {
		if f, head, ok := m.cursorHunk(); ok && !f.gone {
			return m.doApproveHunk(f, head)
		}
	}
	if _, _, ok := m.cursorGap(); ok && m.focus == focusReview && m.commit == "" {
		m.setWarn("Nothing changed on these lines — a on the file's heading approves the file")
		return m, nil
	}
	// The hunks a stretch stands for are not on the screen, and approving
	// them unseen is not reading them.
	if r, ok := m.view.row(); ok && r.kind == rowStretch && m.focus == focusReview {
		m.setWarn("These hunks are not core · Space shows them, and a approves them one at a time")
		return m, nil
	}
	path, ok := m.target()
	if !ok {
		m.setError("No file to approve")
		return m, nil
	}
	author := m.author
	order, key := m.view.paths(), m.view.keyAt()
	if m.rv.Approved(path, author) {
		// Withdrawing an approval is having more to say about the file, so it
		// opens rather than going back to how the window started.
		undo := m.keepFolds(path, coreKey(path))
		m.folds[path] = false
		if _, core := m.view.byPath[coreKey(path)]; core {
			m.folds[coreKey(path)] = false
		}
		if m.mutate(func(st *review.State) { st.Unapprove(path, author) }) {
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
	// Both of its entries, when the card's core has it.
	undo := m.keepFolds(path, coreKey(path))
	delete(m.folds, path)
	delete(m.folds, coreKey(path))
	digest := f.digest
	// The hunks go in with the file, so that if the file is edited and the
	// approval falls, the hunks the edit did not touch are still signed off.
	// An approval given from the command line records none, and falls whole.
	all := slices.Concat(f.blocks...)
	if m.mutate(func(st *review.State) {
		st.Approve(path, digest, author)
		st.SetHunks(path, author, all)
	}) {
		m.setOK("Approved " + path)
		m.advance(order, key, -1)
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
		if !m.rv.Approved(f.path, m.author) {
			pending = append(pending, f.path)
		}
	}
	if len(pending) == 0 {
		m.setOK("You have already approved every file")
		return m, nil
	}
	digests, hunks := map[string]string{}, map[string][]string{}
	for _, p := range pending {
		digests[p] = m.workBy[p].digest
		hunks[p] = slices.Concat(m.workBy[p].blocks...)
	}
	author := m.author
	m.dialog = confirmDialog("Approve everything",
		"Approve "+plural(len(pending), "file", "files")+" in one go?\n\n"+
			"Each approval is of the change as it stands now, so anything\n"+
			"edited afterwards comes back unapproved.",
		func(string) tea.Cmd {
			return func() tea.Msg { return approveAllMsg{paths: pending, digests: digests, hunks: hunks, by: author} }
		})
	return m, nil
}

type approveAllMsg struct {
	paths   []string
	digests map[string]string
	hunks   map[string][]string
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

	where := printable(f.path) + ":" + itoa(c.line)
	switch {
	case c.line == 0:
		where = printable(f.path) + " (no longer a line in the working tree)"
	case c.span > 1:
		where = printable(f.path) + ":" + itoa(c.line) + "–" + itoa(c.line+c.span-1)
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
		title: "Reply to " + printable(t.ID) + " on " + printable(t.Path), thread: t.ID, path: t.Path, at: -1,
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
	// The box goes into the entry the cursor is in: a file in the card's
	// core is in the stack twice, and the box is drawn once.
	key := m.view.keyAt()
	if key == "" {
		key = c.path
	}
	m.view.draft = &draft{path: c.path, key: key, line: line, thread: c.thread}
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
// line being commented on, and its neighbors, to write about it.
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
	for l := range strings.SplitSeq(m.editor.View(), "\n") {
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
		if i, ok := m.view.diffRowAt(d.key, d.line); ok {
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
	var saved bool
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
	if m.wt == nil {
		return m, nil
	}
	items := []string{autoBase}
	// The commit the review card says to review from comes first, when the
	// review is not measured from it already: it is the one somebody who
	// read the change whole chose.
	card := ""
	if c := m.rv.Card; c != nil && c.Base != "" && !m.sameBase(c.Base) {
		card = cardBase + abbrevHash(c.Base)
		items = []string{card, autoBase}
	}
	items = append(items, m.wt.Refs()...)
	items = append(items, commitBase, otherBase)
	m.dialog = listDialog("Review base",
		"Measure this branch from which ref?\n"+
			"The review is everything between its merge base with that ref\n"+
			"and the working tree.",
		items, func(choice string) tea.Cmd {
			if card != "" && choice == card {
				// Short, as the commit picker names a commit: the base is
				// what the bar shows.
				choice = abbrevHash(m.rv.Card.Base)
			}
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
	commits, err := m.wt.Recent(recentCommits)
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
	cardBase   = "The review card's base  "
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
	// It is saved once the scan it asks for has found it (onRescan): one that
	// names nothing is said there and never written. Unsaved, it still holds
	// for this session, which is what was asked for first.
	m.baseToSave = true
	cmd := m.scan()
	return m, cmd
}

func (m Model) doCheckout() (tea.Model, tea.Cmd) {
	if m.wt == nil {
		return m, nil
	}
	branches, err := m.wt.Branches()
	if err != nil || len(branches) == 0 {
		m.setError("No branches available")
		return m, nil
	}
	wt := m.wt
	m.dialog = listDialog("Check out a branch",
		"Review which branch instead?", branches,
		func(b string) tea.Cmd {
			return op("Checked out "+b, true, func() error { _, err := wt.Checkout(b); return err })
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
	if m.wt == nil {
		return m, nil
	}
	if path == "" {
		m.setError("No file to edit")
		return m, nil
	}
	editor, tmp, err := m.wt.EditAt(commit, path)
	if err != nil {
		m.setError(err.Error())
		return m, nil
	}
	return m, tea.Exec(editor, editorClosed(m.wt, path, tmp))
}

// editorClosed is what happens once the editor exits. For a working tree file
// the rescan is not optional: the editor may well have changed the file, and
// the scan is how the review catches up with it — including nullifying an
// approval of what it used to say.
//
// It is a function of its own rather than a closure inside doEdit because
// bubbletea runs it in the runtime rather than through Update, which puts it
// out of reach of a test driving the model directly.
func editorClosed(wt source.Worktree, path, tmp string) func(error) tea.Msg {
	return func(err error) tea.Msg {
		wt.RemoveTempCopy(tmp)
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
	src, gen := m.src, m.gen
	return m, func() tea.Msg {
		text, err := src.FileContent(commit, path)
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

	src, gen, want := m.src, m.gen, path
	return m, func() tea.Msg {
		text, err := src.FileContent(at, want)
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
	m.file.offer = m.offerer()
	if m.wt == nil {
		// No disk: what the change leads to is its head.
		m.file.now = "at the head"
	}
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
		line = carryLine(f, m.file.side, line)
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
func carryLine(f *reviewFile, from fileSide, line int) int {
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
	m.md.offer = m.offerer()
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

func (m *Model) clearStatus() { m.status, m.statusOK, m.statusEr, m.statusWa = "", false, false, false }

// The status bar says things about paths, refs and errors from outside the
// window, so what it is handed is made printable on the way in.
func (m *Model) setOK(s string) {
	m.status, m.statusOK, m.statusEr, m.statusWa = printable(s), true, false, false
}
func (m *Model) setWarn(s string) {
	m.status, m.statusOK, m.statusEr, m.statusWa = printable(s), false, false, true
}
func (m *Model) setError(s string) {
	m.status, m.statusOK, m.statusEr, m.statusWa = printable(s), false, true, false
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
