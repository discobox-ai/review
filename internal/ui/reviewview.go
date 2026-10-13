package ui

import (
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/discobox-ai/review/internal/git"
	"github.com/discobox-ai/review/internal/review"
)

// The review pane: every file's diff in one column, in one scroll, with the
// comments written into it where they were made.
//
// It is one document rather than one file at a time because that is what a
// review is. Reading a change file by file, with a list to go back to between
// each, makes it hard to hold a change in your head and easy to leave a file
// unread without noticing. A single stack can be read from top to bottom, and
// what is left to do is how far down you have got.
//
// The pane is a list of rows, rebuilt whenever anything it draws changes.
// A row is a file's header, a line of diff, or one line of a comment thread —
// and because they are all rows, the cursor, the scrollbar, the mouse and the
// selection have one thing to walk rather than three.

type rowKind uint8

const (
	rowFile   rowKind = iota // the band naming a file, and its verdict
	rowDiff                  // one line of that file's diff
	rowThread                // one line of a comment thread
	rowDraft                 // one line of the comment being written
	rowGap                   // the blank line between two files
	rowEmpty                 // "nothing to review", when there is nothing
)

// threadStyle picks the ink for a thread row: the rule down its left edge is
// the same on every line, so what differs is the text beside it.
type threadStyle uint8

const (
	thBorder threadStyle = iota
	thAuthor
	thBody
	thFoot
	thLoose // the note above a thread no longer beside a line
)

// reviewRow is one drawn line of the pane.
type reviewRow struct {
	kind rowKind
	file int // index into files, -1 when the row belongs to no file
	line int // index into the file's diff lines — or, for rowDraft, into
	//        the lines of the box being written; -1 when it is neither
	thread string // the thread this row belongs to, "" when it is not one
	// comment is which remark of that thread the row draws, as an index into
	// it, or -1 for the rule and the hint around them. It is what lets one
	// remark be deleted out of a conversation rather than all of them.
	comment int
	first   bool   // the first row of a thread, which is what t and T land on
	text    string // the text of a thread or note row
	style   threadStyle
	marked  bool // the line's change has not been committed yet
}

// reviewFile is one file's section of the stack: its diff, already lexed, and
// what the review has to say about it.
type reviewFile struct {
	path string
	from string // the old name, when the file was renamed

	patch *git.Patch
	lines []git.DiffLine
	texts []string   // code text with tabs expanded, parallel to lines
	spans [][]span   // syntax highlighting, parallel to lines
	emph  []emphasis // the characters that changed, parallel to lines
	adds  int
	dels  int

	// digest identifies this change, and is what an approval is a claim
	// about. See [review.Digest].
	digest string

	// blocks is the same for the parts of the change: for each hunk, the
	// digest of every run of changed lines in it. See [review.HunkDigests].
	blocks [][]string

	// state is the file's working tree status, which is what says whether the
	// change has been committed. It is empty while a single commit is being
	// read, where the question does not arise.
	state git.State

	// full is the file as the new side of the diff has it, a line each, and
	// gaps is the stretches of it the diff leaves out: one before each hunk
	// and one after the last, indexed by the hunk that follows. A gap nothing
	// was left out of has no lines and no row. See [reviewFile.addGaps].
	full []string
	gaps []gap

	// rows is the file's lines arranged for drawing, and rowAt maps each line
	// back to the row that shows it. In a unified view that is one row per
	// line; in a split view a row can show two.
	rows  []rowRef
	rowAt []int

	// plain marks a file too big to have been lexed, so that a file without
	// its colours says why rather than looking as though it has none.
	plain bool

	// gone marks a file that is no longer part of the review but still
	// carries comments: the change was reverted, or the base moved past it.
	// It keeps its place in the stack, holding nothing but those remarks,
	// because losing a comment silently is the worst thing this pane could
	// do.
	gone bool

	// pending holds the new-side line numbers whose change is not in a commit
	// yet, keyed by [git.DiffLine.Seam] so that a deletion can be marked as
	// well as an addition.
	pending map[int]bool

	// Filled in from the review before the rows are built. approved and
	// nullified are the reader's own verdict, which is the one the window
	// goes by; others is everybody else whose approval stands, and othersFell
	// everybody else whose approval the file has changed under.
	approved   bool
	nullified  bool
	others     []string
	othersFell []string
	folded     bool
	shut       map[int]bool             // the hunks folded away, by index into the patch
	signed     map[string]bool          // the runs of changed lines approved one hunk at a time
	threads    map[int][]*review.Thread // by index into lines
	loose      []*review.Thread         // in this file, but on no line of it
	open       int
	total      int
}

// rowRef names the diff lines one drawn row shows.
//
// In a unified view that is a single line, held in both fields. In a split
// view it is the two lines the row puts side by side, either of which may be
// missing — a run of removals with nothing added in its place has no right
// hand side, and an insertion has no left.
type rowRef struct{ old, new int }

// buildRows arranges a file's lines into the rows that draw them.
//
// Unified is one row per line, in the order the diff prints them. Split pairs
// each run of removals with the run of additions that follows it, line for
// line, because that is what such a pair is: an edit of those lines in order,
// and the same reading pairEmphasis makes when it picks out the characters
// that changed. Runs of unequal length leave the shorter side blank.
func (f *reviewFile) buildRows(split bool) {
	f.rows = f.rows[:0]
	if cap(f.rowAt) < len(f.lines) {
		f.rowAt = make([]int, len(f.lines))
	}
	f.rowAt = f.rowAt[:len(f.lines)]

	add := func(old, new int) {
		at := len(f.rows)
		f.rows = append(f.rows, rowRef{old: old, new: new})
		if old >= 0 {
			f.rowAt[old] = at
		}
		if new >= 0 {
			f.rowAt[new] = at
		}
	}

	if !split {
		for i := range f.lines {
			add(i, i)
		}
		return
	}

	for i := 0; i < len(f.lines); {
		l := f.lines[i]
		if l.Kind != '-' && l.Kind != '+' {
			add(i, i) // context, and the headers between hunks
			i++
			continue
		}
		// The run of removals here, and the run of additions after it.
		dels := i
		for i < len(f.lines) && f.lines[i].Kind == '-' {
			i++
		}
		adds := i
		for i < len(f.lines) && f.lines[i].Kind == '+' {
			i++
		}
		nDel, nAdd := adds-dels, i-adds
		for k := 0; k < max(nDel, nAdd); k++ {
			old, new := -1, -1
			if k < nDel {
				old = dels + k
			}
			if k < nAdd {
				new = adds + k
			}
			add(old, new)
		}
	}
}

// lineIndex maps a line number on each side of a diff to the index of the diff
// line carrying it, which is how a comment finds the line it was written
// against.
//
// It asks nothing about how the file is drawn. A comment belongs to a line, and
// which row that line is drawn on — or whether it shares one with the line it
// replaced — is a question for whoever is drawing. Keying a remark by the row
// instead was the bug that made a zoom or a resize move every conversation in
// the file: two places worked out the layout separately and only agreed while
// the diff was unified.
func (f *reviewFile) lineIndex() (newAt, oldAt map[int]int) {
	newAt, oldAt = map[int]int{}, map[int]int{}
	for i, l := range f.lines {
		if !isCodeLine(l.Kind) {
			continue
		}
		if l.New > 0 {
			if _, dup := newAt[l.New]; !dup {
				newAt[l.New] = i
			}
		}
		if l.Old > 0 {
			if _, dup := oldAt[l.Old]; !dup {
				oldAt[l.Old] = i
			}
		}
	}
	return newAt, oldAt
}

// primary is the line a row is about: the new side when it has one, since that
// is the code being proposed and what a remark is nearly always about.
func (f *reviewFile) primary(row int) int {
	if row < 0 || row >= len(f.rows) {
		return -1
	}
	if r := f.rows[row]; r.new >= 0 {
		return r.new
	}
	return f.rows[row].old
}

// side is the diff line on one side of a row, or the row's primary line when
// that side is empty — an insertion has nothing on the left of it, and a
// cursor asking for the left of one is still on the row.
func (f *reviewFile) side(row int, old bool) int {
	if row < 0 || row >= len(f.rows) {
		return -1
	}
	r := f.rows[row]
	i := r.new
	if old {
		i = r.old
	}
	if i < 0 {
		return f.primary(row)
	}
	return i
}

// liveSide is the diff line the cursor is on within a row: the half it is
// sitting in when the view is split, and the row's primary line when it is
// not — a unified row draws one line, so there is no side to be on.
//
// It is the pane's answer to "which line is this", and everything that acts on
// the line under the cursor goes through it, so that what a key does is what
// the cursor bar says it will.
func (v *reviewView) liveSide(f *reviewFile, row int) int {
	if !v.splitting() {
		return f.primary(row)
	}
	return f.side(row, v.onOld)
}

// rowLine is the diff line a row is about.
func (f *reviewFile) rowLine(row int) (git.DiffLine, bool) {
	i := f.primary(row)
	if i < 0 || i >= len(f.lines) {
		return git.DiffLine{}, false
	}
	return f.lines[i], true
}

// gap is a stretch of a file the diff does not show, because nothing in it
// changed: the lines between two hunks, or above the first, or below the last.
type gap struct {
	old, new int  // its first line, as each side of the diff numbers it
	n        int  // how many lines it is; none when the hunks meet
	open     bool // the lines are in the stack, rather than counted on one row
}

// addGaps puts a row into the file's lines wherever the diff skips part of
// the file, saying how much it skipped. text is the file as the new side has
// it, which is where the skipped lines are read from when a gap is opened.
//
// The row is a line like any other — kind 'g', in no hunk, with the gap's
// index where a hunk's line would have its own — so that the cursor, the
// scrollbar and the selection walk over it without being told about it.
func (f *reviewFile) addGaps(text string) {
	if f.patch == nil || len(f.patch.Hunks) == 0 || text == "" || strings.IndexByte(text, 0) >= 0 {
		return
	}
	f.full = strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	f.gaps = make([]gap, len(f.patch.Hunks)+1)

	oldAt, newAt := 1, 1
	for h, hunk := range f.patch.Hunks {
		oldN, newN := 0, 0
		for _, l := range hunk.Lines {
			if l == "" {
				continue
			}
			switch l[0] {
			case ' ':
				oldN++
				newN++
			case '-':
				oldN++
			case '+':
				newN++
			}
		}
		// A side a hunk has no lines on is numbered by the line before it.
		oldFirst, newFirst := hunk.OldStart, hunk.NewStart
		if oldN == 0 {
			oldFirst++
		}
		if newN == 0 {
			newFirst++
		}
		// Both sides skip the same lines, or the file is not the one the
		// diff was taken from and there is nothing here to trust.
		if n := newFirst - newAt; n > 0 && n == oldFirst-oldAt && newFirst-1 <= len(f.full) {
			f.gaps[h] = gap{old: oldAt, new: newAt, n: n}
		}
		oldAt, newAt = oldFirst+oldN, newFirst+newN
	}
	if n := len(f.full) - newAt + 1; n > 0 {
		f.gaps[len(f.patch.Hunks)] = gap{old: oldAt, new: newAt, n: n}
	}

	// From the end, so that putting one row in does not move the place the
	// next one goes.
	if g := len(f.gaps) - 1; f.gaps[g].n > 0 {
		f.insert(len(f.lines), []git.DiffLine{{Kind: 'g', Hunk: -1, Line: g}}, []string{""}, nil)
	}
	for i := len(f.lines) - 1; i >= 0; i-- {
		if l := f.lines[i]; l.Kind == '@' && l.Hunk < len(f.gaps) && f.gaps[l.Hunk].n > 0 {
			f.insert(i, []git.DiffLine{{Kind: 'g', Hunk: -1, Line: l.Hunk}}, []string{""}, nil)
		}
	}
}

// insert puts lines into the file, keeping everything that runs parallel to
// them in step.
func (f *reviewFile) insert(at int, lines []git.DiffLine, texts []string, spans [][]span) {
	if len(f.spans) == len(f.lines) {
		if spans == nil {
			spans = make([][]span, len(lines))
		}
		f.spans = slices.Insert(f.spans, at, spans...)
	}
	if len(f.emph) == len(f.lines) {
		f.emph = slices.Insert(f.emph, at, make([]emphasis, len(lines))...)
	}
	f.lines = slices.Insert(f.lines, at, lines...)
	f.texts = slices.Insert(f.texts, at, texts...)
}

// gapAt is the gap a line belongs to — its row, or one of the lines it has
// let out — and false for a line of the diff itself.
func (f *reviewFile) gapAt(i int) (int, bool) {
	if i < 0 || i >= len(f.lines) {
		return 0, false
	}
	l := f.lines[i]
	if l.Hunk >= 0 || l.Line < 0 || l.Line >= len(f.gaps) || (l.Kind != 'g' && l.Kind != ' ') {
		return 0, false
	}
	return l.Line, true
}

// gapRow is the index of a gap's own row among the file's lines.
func (f *reviewFile) gapRow(g int) int {
	for i, l := range f.lines {
		if l.Kind == 'g' && l.Line == g {
			return i
		}
	}
	return -1
}

// setGap opens a gap, putting the lines it stands for into the file under its
// row, or shuts it and takes them out again.
//
// They go in as context lines, numbered on both sides, which is all they are:
// the same lines more context would have shown. So a remark can be written
// against one, and finds it again by its number like any other.
func (f *reviewFile) setGap(g int, open bool) {
	if g < 0 || g >= len(f.gaps) || f.gaps[g].n == 0 || f.gaps[g].open == open {
		return
	}
	at := f.gapRow(g)
	if at < 0 {
		return
	}
	gp := &f.gaps[g]
	gp.open = open
	if !open {
		if len(f.spans) == len(f.lines) {
			f.spans = slices.Delete(f.spans, at+1, at+1+gp.n)
		}
		if len(f.emph) == len(f.lines) {
			f.emph = slices.Delete(f.emph, at+1, at+1+gp.n)
		}
		f.lines = slices.Delete(f.lines, at+1, at+1+gp.n)
		f.texts = slices.Delete(f.texts, at+1, at+1+gp.n)
		return
	}
	lines, texts := make([]git.DiffLine, gp.n), make([]string, gp.n)
	for k := range lines {
		// A file with DOS line endings keeps its carriage returns when it is
		// read whole, and one sent to the terminal moves the cursor.
		s := strings.TrimSuffix(f.full[gp.new-1+k], "\r")
		lines[k] = git.DiffLine{
			Text: " " + s, Kind: ' ', Hunk: -1, Line: g,
			Old: gp.old + k, New: gp.new + k, Seam: gp.new + k,
		}
		texts[k] = expandTabs(s)
	}
	var spans [][]span
	// A gap can be most of a file, and this runs where the keys are read.
	if !f.plain && gp.n <= maxLexLines {
		if lexer := lexerFor(f.path); lexer != nil {
			spans = tokeniseLines(lexer, texts)
		}
	}
	f.insert(at+1, lines, texts, spans)
}

// gapText is what a gap's row says: how much of the file it stands for.
func (f *reviewFile) gapText(i int) string {
	g, ok := f.gapAt(i)
	if !ok {
		return ""
	}
	mark := "▸ "
	if f.gaps[g].open {
		mark = "▾ "
	}
	return mark + plural(f.gaps[g].n, "unchanged line", "unchanged lines")
}

// hidden reports whether a line is inside a folded hunk. The hunk's own header
// is not: it is the row the fold leaves behind, and what opens it again.
func (f *reviewFile) hidden(i int) bool {
	if len(f.shut) == 0 || i < 0 || i >= len(f.lines) {
		return false
	}
	l := f.lines[i]
	return l.Kind != '@' && f.shut[l.Hunk]
}

// hunkHead is the index of the header line of the hunk a line belongs to, or
// -1 for a line that belongs to none — the preamble above the first of them.
func (f *reviewFile) hunkHead(i int) int {
	if i < 0 || i >= len(f.lines) || f.lines[i].Hunk < 0 {
		return -1
	}
	for ; i >= 0; i-- {
		if f.lines[i].Kind == '@' {
			return i
		}
	}
	return -1
}

// hunkApproved reports whether a hunk has been signed off on its own: every
// run of changed lines in it is one the review has an approval for.
func (f *reviewFile) hunkApproved(hunk int) bool {
	if len(f.signed) == 0 || hunk < 0 || hunk >= len(f.blocks) || len(f.blocks[hunk]) == 0 {
		return false
	}
	for _, d := range f.blocks[hunk] {
		if !f.signed[d] {
			return false
		}
	}
	return true
}

// hunksApproved counts the hunks signed off on their own, out of how many.
func (f *reviewFile) hunksApproved() (n, of int) {
	for h := range f.blocks {
		if f.hunkApproved(h) {
			n++
		}
	}
	return n, len(f.blocks)
}

// hunkText is what a hunk's header row says. An open one nobody has decided
// about says what git said. A folded one also says what it is holding back,
// and above all whether anyone is still waiting for an answer in there: a fold
// hides the conversation along with the code, and that must not be how a
// comment gets missed.
//
// The verdict is only said while it is news. In an approved file every hunk is
// approved, and the file's own heading has said so.
func (f *reviewFile) hunkText(i int) string {
	l := f.lines[i]
	s := l.Text
	if f.shut[l.Hunk] {
		lines, open := 0, 0
		for j := i + 1; j < len(f.lines) && f.lines[j].Hunk == l.Hunk; j++ {
			if isCodeLine(f.lines[j].Kind) {
				lines++
			}
			for _, t := range f.threads[j] {
				if !t.Resolved {
					open++
				}
			}
		}
		s = "▸ " + s + "  · " + plural(lines, "line", "lines") + " folded"
		if open > 0 {
			s += " · " + plural(open, "open comment", "open comments")
		}
	}
	if !f.approved && f.hunkApproved(l.Hunk) {
		if !f.shut[l.Hunk] {
			s += " "
		}
		s += " · ✓ approved"
	}
	return s
}

// done reports whether there is nothing left to do about a file: signed off,
// with nothing outstanding said about it. It is what decides whether a file
// folds itself away, and what "open them all" steps over.
func (f *reviewFile) done() bool { return f.approved && f.open == 0 }

// draft is a comment being written, and where in the stack the box goes.
//
// It is drawn inline, under the line it is about, rather than in a field at
// the foot of the window. A remark is about a particular line, and writing it
// somewhere else means reading the line in one place and answering it in
// another, with the two never on screen together at the size that matters. The
// box goes where the comment will go, so what you are looking at while you
// write it is what you will be looking at when you read it back.
type draft struct {
	path   string // the file it belongs to
	line   int    // index into that file's diff lines, -1 when it follows a thread
	thread string // the thread being replied to, empty for a new comment

	// lines is the box as it stands, one string per row, already drawn to the
	// width of the pane. The rows in the stack index into it, so the box can
	// be redrawn on every keystroke without the stack being rebuilt.
	lines []string
}

// done reports whether there is nothing left to do about a file: signed off,
func (f *reviewFile) marked(i int) bool {
	if f.pending == nil || i < 0 || i >= len(f.lines) {
		return false
	}
	l := f.lines[i]
	if !isCodeLine(l.Kind) {
		return false
	}
	return f.pending[l.Seam]
}

// reviewView is the pane itself.
type reviewView struct {
	files  []*reviewFile
	byPath map[string]int
	rows   []reviewRow

	// numW and long are sized across every file at once, so that the numbers
	// and the horizontal scroll do not jump as you read down past a file
	// boundary.
	numW int
	long int

	// expanded holds the resolved threads the reader has opened again. A
	// closed thread is one line until asked for, since the point of closing
	// one is to stop having to read it.
	expanded map[string]bool

	// commit is the commit being read, empty for the whole review, and label
	// is what the title says about it.
	commit string
	label  string
	note   string // shown instead of rows when there is nothing to review

	// draft is the comment being written, drawn into the stack at the place it
	// will land. nil when nothing is being written.
	draft *draft

	// split draws the two sides of a change beside each other rather than one
	// above the other. It is what the reader asked for, not what the pane can
	// do: a narrow pane goes on drawing a unified diff, since two columns of
	// forty characters is worse than one of eighty.
	split bool

	// builtAt is the pane width the current rows were laid out for. A comment
	// is wrapped to the pane and whether the diff is split at all is decided
	// by it, so rows built at one width cannot be drawn at another: see
	// reflow, and the two places that change the width without going anywhere
	// near the rows.
	builtAt int

	// asked records that the reader chose the view themselves, which is what
	// makes a pane too narrow to honour it worth explaining. The default is
	// split, and a default that quietly does the best it can with the room it
	// has needs no note; a request that cannot be met does.
	asked bool

	// onOld is the half of a split row the cursor is in: the left, where the
	// line the change took away is drawn. A unified diff gives every line a
	// row of its own, so the cursor picks a side by being on one; a split row
	// draws two lines at once and needs telling which of them it is on. It
	// stays set while the view is unified, so that reading a diff one way and
	// then the other does not quietly move the cursor across.
	onOld bool

	// hidden counts the files left out because the reader has approved them. They are
	// still part of the review, so the tally still counts them; they are just
	// not on the screen.
	hidden int

	// hiddenOpen counts the open comments on those files, which went off the
	// screen with them.
	hiddenOpen int

	cursor  int
	offset  int
	hoffset int
	anchor  int // start of a line selection, or -1

	width  int
	height int // rows for the body, title excluded
}

func newReviewView() *reviewView {
	// Split by default: lining up what a line was against what it became is
	// what most people want to see most of the time, and a change that is an
	// edit rather than an insertion reads far better that way. A pane too
	// narrow for two columns still draws a unified diff, so this is a
	// preference rather than a demand — see splitting.
	return &reviewView{
		split: true, anchor: -1,
		expanded: map[string]bool{}, byPath: map[string]int{},
	}
}

// setFiles replaces the diff the pane is showing, keeping the reader's place
// by file rather than by row: a rescan that adds a hunk higher up must not
// scroll the file you were reading out from under you.
func (v *reviewView) setFiles(files []*reviewFile, commit, label string) {
	a := v.cursorAnchor()
	v.files, v.commit, v.label = files, commit, label
	v.byPath = make(map[string]int, len(files))
	for i, f := range files {
		v.byPath[f.path] = i
	}
	v.anchor = -1
	v.restore(a)
}

// place is where the reader was: a file, how far into it the cursor had got,
// and how far down the pane that file's heading was drawn. All three are told
// in terms of the file rather than of row numbers, which is what lets them
// survive the rows being rebuilt underneath them.
type place struct {
	path   string
	within int
	screen int // rows from the top of the pane to the heading, or -1 for above it
}

// cursorAnchor remembers where the cursor is, for restore to put it back.
func (v *reviewView) cursorAnchor() place {
	if v.cursor < 0 || v.cursor >= len(v.rows) {
		return place{screen: -1}
	}
	r := v.rows[v.cursor]
	if r.file < 0 || r.file >= len(v.files) {
		return place{screen: -1}
	}
	f := v.files[r.file]
	for i := v.cursor; i >= 0; i-- {
		if v.rows[i].kind == rowFile && v.rows[i].file == r.file {
			return place{path: f.path, within: v.cursor - i, screen: max(i-v.offset, -1)}
		}
	}
	return place{path: f.path, screen: -1}
}

// restore puts the cursor back where cursorAnchor found it, as nearly as the
// new rows allow.
func (v *reviewView) restore(a place) {
	v.build()
	if a.path == "" {
		v.clamp()
		return
	}
	fi, ok := v.byPath[a.path]
	if !ok {
		v.clamp()
		return
	}
	for i, r := range v.rows {
		if r.kind != rowFile || r.file != fi {
			continue
		}
		// The cursor stays inside the file it was in. Folding one shortens it
		// to its heading, and a cursor that kept its distance from the heading
		// would land in whatever file came next — so the same key pressed
		// twice would act on two different files.
		last := i
		for last+1 < len(v.rows) && v.rows[last+1].file == fi {
			last++
		}
		v.cursor = min(i+a.within, last)
		// The heading goes back to the height it was drawn at, so what changes
		// on the screen is the file itself: folding it empties the space below
		// its heading and approving it takes that space away, and neither
		// hauls the files above it out of sight. A heading that was above the
		// pane has no height to go back to and comes to the top instead, since
		// a file that starts off the top of the screen reads as having lost
		// your place even when the cursor is right.
		if a.screen < 0 {
			v.offset = i
		} else {
			v.offset = min(max(i-a.screen, 0), v.maxOffset())
		}
		v.clamp()
		return
	}
	v.clamp()
}

// ---------------------------------------------------------------------------
// Building the rows
// ---------------------------------------------------------------------------

// minSplitWidth is the narrowest a pane can be and still be worth splitting.
// It leaves each side about thirty columns of code once the numbers and the
// markers have taken theirs — under that a diff has to be scrolled sideways to
// be read twice, which is worse than reading it once.
//
// The pane is as wide as the window, so this is the terminal's own width: a
// review opens split, and a threshold most terminals fell short of would be a
// default nobody saw.
const minSplitWidth = 80

// splitting reports whether the pane is drawing side by side: asked for, and
// wide enough to be worth it.
// It is measured against the pane's own width rather than a row's, so that it
// cannot answer differently either side of a scrollbar appearing — the rows
// and the comments hung off them have to agree about which mode they are in.
func (v *reviewView) splitting() bool { return v.split && v.width >= minSplitWidth }

// onOldLine reports whether the cursor is really on the old side of the diff:
// asked for, and on a row that has a removed line for it to be on.
//
// It is not the same question as onOld. A row with nothing on its left — a pure
// insertion — draws the cursor on the right whatever was asked, and a row of
// context draws one line in both halves and anchors a remark to the new side
// either way. Naming the old side on those would name a side the next key will
// not act on, which is the one thing this label is for.
func (v *reviewView) onOldLine() bool {
	if !v.onOld || !v.splitting() {
		return false
	}
	_, l, ok := v.line()
	return ok && l.Kind == '-'
}

// pickSide puts the cursor in the half of a split row a column falls in, x
// being measured from the pane's left edge. A unified diff has no halves to
// choose between, so it is left alone.
func (v *reviewView) pickSide(x int) {
	if !v.splitting() {
		return
	}
	left, _ := v.halvesOf(v.rowW())
	v.onOld = x < left
}

// halvesOf is where a split row's two sides fall in a row of a given width:
// the columns each gets, with one between them for the rule.
func (v *reviewView) halvesOf(w int) (left, right int) {
	left = (w - 1) / 2
	return left, max(w-left-1, 0)
}

// threadIndent is how far a comment sits in from the code it is about, in
// columns of content. It is indented rather than boxed across the whole pane
// because a thread is a remark hung off a line, not a section of its own.
const threadIndent = 4

// build lays the files out as rows. It is called whenever anything that
// changes the shape of the pane changes: the diff, the review, the folds, or
// the width, since a comment is wrapped to fit.
func (v *reviewView) build() {
	v.numW, v.long = 0, 0
	for _, f := range v.files {
		v.numW = max(v.numW, numberWidth(f.lines))
		// Only what is drawn can run past the margin: a folded file's long
		// lines are not on screen to be scrolled to, and a bar along the foot
		// of a window showing nothing but headings would be a bar for nothing.
		if !f.folded {
			v.long = max(v.long, longestLine(f.texts))
		}
	}

	rows := make([]reviewRow, 0, 256)
	if len(v.files) == 0 {
		v.rows = append(rows, reviewRow{kind: rowEmpty, file: -1, line: -1})
		v.builtAt = v.width
		return
	}
	wrapAt := max(v.contentWidth()-threadIndent-2, 16)
	split := v.splitting()
	for fi, f := range v.files {
		f.buildRows(split)
		rows = append(rows, reviewRow{kind: rowFile, file: fi, line: -1})
		if f.folded {
			// No gap under a folded file: the gap is what ends a diff, and a
			// run of folded files with nothing between them reads as a list.
			continue
		}
		mine, placed := v.draft != nil && v.draft.path == f.path, false
		for _, t := range f.loose {
			rows = append(rows, v.threadRows(t, fi, -1, wrapAt, true)...)
			if mine && v.draft.thread == t.ID {
				rows, placed = append(rows, v.draftRows(fi)...), true
			}
		}
		// The box is anchored to a line, which survives the pane changing
		// shape; the row that draws it does not.
		draftRow := -1
		if mine && v.draft.thread == "" && v.draft.line >= 0 && v.draft.line < len(f.rowAt) {
			draftRow = f.rowAt[v.draft.line]
		}
		for i := range f.rows {
			// A folded hunk is its header and nothing else: the lines under it
			// go, and the conversations hung off them go with them.
			if f.hidden(f.primary(i)) {
				continue
			}
			rows = append(rows, reviewRow{
				kind: rowDiff, file: fi, line: i, marked: f.marked(f.primary(i)),
			})
			// The lines this row draws, in the order it draws them. A split
			// row draws two and either may have been commented on. A row that
			// draws one line in both halves — a line neither side touched —
			// must not draw its conversation twice with it.
			sides := [2]int{f.rows[i].old, f.rows[i].new}
			if sides[0] == sides[1] {
				sides[1] = -1
			}
			for _, at := range sides {
				if at < 0 {
					continue
				}
				for _, t := range f.threads[at] {
					rows = append(rows, v.threadRows(t, fi, i, wrapAt, false)...)
					if mine && v.draft.thread == t.ID {
						rows, placed = append(rows, v.draftRows(fi)...), true
					}
				}
			}
			if mine && i == draftRow {
				rows, placed = append(rows, v.draftRows(fi)...), true
			}
		}
		// A box whose line is no longer there still has to be somewhere: the
		// end of its file's section, rather than nowhere at all.
		if mine && !placed {
			rows = append(rows, v.draftRows(fi)...)
		}
		rows = append(rows, reviewRow{kind: rowGap, file: fi, line: -1})
	}
	v.rows = rows
	v.builtAt = v.width
}

// spot is where the reader is, told in terms that survive the rows being laid
// out again: a file, and the line of it under the cursor. Row numbers do not
// survive, which is the whole difficulty — the two views draw the same file in
// different numbers of rows, so a cursor left holding its old number lands
// somewhere else, often in another file.
type spot struct {
	path string
	line int  // index into that file's diff lines, -1 when the cursor is elsewhere
	old  bool // that line is numbered on the base's side
	side bool // that line settles which side the cursor is on; a context line does not
	head bool // the cursor is on the file's own heading

	// thread is the conversation the cursor is in. A remark is not a line, so
	// it is found again by its own name rather than by a row number — landing
	// on a different thread would put r on a remark nobody was reading.
	thread string
}

// mark remembers where the reader is, for reflow to put them back.
//
// It must be taken before anything moves the pane's width. cursorLine asks
// which half of a row the cursor is in, and that question is settled by the
// width: asked after the width has moved it answers with the new mode about
// the old rows, and for split becoming unified that quietly slides the cursor
// off a removed line onto the addition that replaced it.
func (v *reviewView) mark() spot {
	s := spot{line: -1}
	r, ok := v.row()
	if !ok || r.file < 0 || r.file >= len(v.files) {
		return s
	}
	f := v.files[r.file]
	s.path = f.path
	s.head = r.kind == rowFile
	s.thread = r.thread
	if r.kind == rowDiff {
		_, s.line, s.old = v.cursorLine()
		// A line neither side touched is drawn in both halves, so it cannot
		// say which half the cursor is in. Only a line that belongs to one
		// side settles it; anything else must leave the reader's own choice
		// standing.
		if s.line >= 0 && s.line < len(f.lines) {
			s.side = f.lines[s.line].Kind == '-' || f.lines[s.line].Kind == '+'
		}
	}
	return s
}

// reflow lays the rows out again when the pane has changed width since they
// were built, and puts the reader back on the line they were on. It does
// nothing when the width has not moved.
//
// The width is set by layout, which knows nothing about rows, and the rows
// decide whether the diff is split. Without this a window dragged across the
// threshold draws split rows through the unified renderer: every removal that
// had an addition paired with it simply vanishes.
func (v *reviewView) reflow(s spot) {
	if v.width == v.builtAt {
		return
	}
	v.build()
	switch {
	case s.thread != "":
		// A conversation is found by its name. Its row number means nothing
		// once the rows have been laid out again, and the row that inherits
		// the number may well be somebody else's remark.
		if i, ok := v.threadRowAt(s.thread); ok {
			v.reveal(i)
			return
		}
	case s.line >= 0:
		// The side comes back with the line, so that a relay across the split
		// threshold does not move the cursor from a removal to its
		// replacement — a different line, on a different side. A line that
		// cannot settle the question leaves the reader's choice alone.
		if s.side {
			v.onOld = s.old
		}
		if v.goToDiffLine(s.path, s.line) {
			return
		}
	case s.head && s.path != "":
		// Arriving at a file puts its heading at the top of the pane, and
		// laying the rows out again must not undo that.
		if v.jumpToFile(s.path) {
			return
		}
	}
	v.clamp()
}

// draftRows are the rows the box being written takes up. They carry only an
// index into it, so redrawing the box on every keystroke costs nothing but the
// box itself.
func (v *reviewView) draftRows(fi int) []reviewRow {
	out := make([]reviewRow, len(v.draft.lines))
	for i := range v.draft.lines {
		out[i] = reviewRow{kind: rowDraft, file: fi, line: i}
	}
	return out
}

// draftSpan is the range of rows the box occupies, so that the whole of it can
// be brought onto the screen rather than just its first line.
func (v *reviewView) draftSpan() (first, last int, ok bool) {
	first = -1
	for i, r := range v.rows {
		if r.kind != rowDraft {
			continue
		}
		if first < 0 {
			first = i
		}
		last = i
	}
	return first, last, first >= 0
}

// threadRows draws one conversation.
//
// A resolved thread is a single line until it is asked for: closing a thread
// is a way of saying it need not be read again, and a pane that went on
// showing it in full would not be honouring that. Everything else is drawn
// out, because a comment nobody can see is a comment nobody will answer.
func (v *reviewView) threadRows(t *review.Thread, fi, line, wrap int, loose bool) []reviewRow {
	row := func(text string, style threadStyle, first bool) reviewRow {
		return reviewRow{
			kind: rowThread, file: fi, line: line, thread: t.ID,
			comment: -1, first: first, text: text, style: style,
		}
	}
	// said is a row drawn from one particular remark, which is the one the
	// cursor is on when it is on that row.
	said := func(text string, style threadStyle, at int) reviewRow {
		r := row(text, style, false)
		r.comment = at
		return r
	}
	if t.Resolved && !v.expanded[t.ID] {
		head := "✓ " + t.ID + " · resolved"
		if n := len(t.Comments); n > 0 {
			head += " · " + plural(n, "comment", "comments")
			head += " · " + firstLine(t.Comments[0].Body)
		}
		return []reviewRow{row(truncate(head, wrap), thFoot, true)}
	}

	var out []reviewRow
	head := t.ID + " · " + plural(len(t.Comments), "comment", "comments")
	if first, last := t.Span(); last > first {
		head += " · lines " + itoa(first) + "–" + itoa(last)
	}
	if loose {
		where := "no longer on a line"
		if t.Line > 0 {
			where = "line " + itoa(t.Line) + " is not in this diff"
		}
		out = append(out, row(head+" · "+where, thLoose, true))
	} else {
		out = append(out, row(head, thBorder, true))
	}
	for at, c := range t.Comments {
		who := c.By
		if who == "" {
			who = "someone"
		}
		out = append(out, said(who+"  "+shortTime(c.At), thAuthor, at))
		for _, l := range wrapText(c.Body, wrap-2) {
			out = append(out, said("  "+l, thBody, at))
		}
	}
	foot := "r reply · x resolve"
	if t.Resolved {
		foot = "resolved · x reopen · o collapse"
	}
	out = append(out, row(foot, thFoot, false))
	return out
}

// wrapText breaks a comment to fit, on spaces where it can and mid-word when
// it must, keeping the author's own line breaks.
func wrapText(s string, w int) []string {
	if w < 8 {
		w = 8
	}
	var out []string
	for _, para := range strings.Split(strings.ReplaceAll(s, "\r\n", "\n"), "\n") {
		if strings.TrimSpace(para) == "" {
			out = append(out, "")
			continue
		}
		line := ""
		for _, word := range strings.Fields(para) {
			switch {
			case line == "":
				line = word
			case width(line)+1+width(word) <= w:
				line += " " + word
			default:
				out = append(out, line)
				line = word
			}
			for width(line) > w {
				// A single word longer than the pane: cut it rather than let
				// it push the rest of the row off the edge.
				runes := []rune(line)
				out = append(out, string(runes[:w]))
				line = string(runes[w:])
			}
		}
		if line != "" {
			out = append(out, line)
		}
	}
	if len(out) == 0 {
		return []string{""}
	}
	return out
}

// shortTime keeps the date off a timestamp that is already in a list of them.
func shortTime(at string) string {
	if len(at) >= 16 && at[10] == 'T' {
		return at[:10] + " " + at[11:16]
	}
	return at
}

// ---------------------------------------------------------------------------
// Where the cursor is
// ---------------------------------------------------------------------------

func (v *reviewView) empty() bool { return len(v.files) == 0 }

func (v *reviewView) row() (reviewRow, bool) {
	if v.cursor < 0 || v.cursor >= len(v.rows) {
		return reviewRow{}, false
	}
	return v.rows[v.cursor], true
}

// file is the file the cursor is somewhere inside, which is what the
// file-level commands act on. Every row but the "nothing to review" one
// belongs to a file, so the cursor is never nowhere.
func (v *reviewView) file() (*reviewFile, bool) {
	r, ok := v.row()
	if !ok || r.file < 0 || r.file >= len(v.files) {
		return nil, false
	}
	return v.files[r.file], true
}

// line is the line of diff under the cursor, and false when the cursor is on
// something else — a file header, a comment. On a split row it is the line in
// the half the cursor is in, which is the one the cursor bar is drawn under.
func (v *reviewView) line() (*reviewFile, git.DiffLine, bool) {
	r, ok := v.row()
	if !ok || r.kind != rowDiff || r.file < 0 {
		return nil, git.DiffLine{}, false
	}
	f := v.files[r.file]
	at := v.liveSide(f, r.line)
	if at < 0 || at >= len(f.lines) {
		return nil, git.DiffLine{}, false
	}
	return f, f.lines[at], true
}

// commentTarget is what a comment would be written against: the line under the
// cursor, or the run of lines a V selection covers.
//
// It reports the line the remark is anchored to, how many lines it covers, and
// the index into the file's diff lines of the last of them — which is where the
// box, and later the thread, is drawn, since a remark reads as being about what
// came above it. A line index rather than a row index, because the rows are
// rebuilt whenever the pane changes shape and a diff that splits itself
// half way through a comment being written must not take the box with it.
//
// Each row is read at the side the cursor is on, so a split row anchors the
// remark to the half the cursor bar is drawn under rather than to whichever of
// its two lines the pane happens to think of first.
//
// A selection can run over both sides of a diff at once — the lines a change
// took away and the ones it put in their place — but a remark has to be
// anchored on one of them. The first line's side wins, and the span counts only
// the lines that share it, so "these three added lines" and "these two removed
// ones" both come out as a block rather than as a muddle of the two.
func (v *reviewView) commentTarget() (f *reviewFile, first git.DiffLine, span, last int, ok bool) {
	lo, hi := v.cursor, v.cursor
	if v.anchor >= 0 {
		lo, hi = min(v.anchor, v.cursor), max(v.anchor, v.cursor)
	}

	found := false
	var lastLine git.DiffLine
	for i := lo; i <= hi && i < len(v.rows); i++ {
		r := v.rows[i]
		if r.kind != rowDiff || r.file < 0 {
			continue
		}
		if f == nil {
			f = v.files[r.file]
		} else if v.files[r.file] != f {
			continue // a selection never spans two files
		}
		at := v.liveSide(f, r.line)
		if at < 0 || at >= len(f.lines) {
			continue
		}
		l := f.lines[at]
		if !isCodeLine(l.Kind) {
			continue
		}
		if !found {
			first, lastLine, last, found = l, l, at, true
			continue
		}
		if sameSide(l, first) {
			lastLine, last = l, at
		}
	}
	if !found {
		return nil, git.DiffLine{}, 0, 0, false
	}
	return f, first, max(sideNumber(lastLine)-sideNumber(first)+1, 1), last, true
}

// sameSide reports whether two lines are numbered in the same space: the file
// as it is now, or the file as the base has it.
func sameSide(a, b git.DiffLine) bool { return (a.Kind == '-') == (b.Kind == '-') }

// sideNumber is a line's number on its own side, which is the one it exists on.
func sideNumber(l git.DiffLine) int {
	if l.Kind == '-' {
		return l.Old
	}
	return l.New
}

// thread is the conversation the cursor is in, or "".
func (v *reviewView) thread() string {
	r, ok := v.row()
	if !ok {
		return ""
	}
	return r.thread
}

// comment is the one remark the cursor is on, as a thread and an index into
// it. It is false on the rule above a conversation and the hint below it,
// which belong to the whole of it rather than to any one thing said.
func (v *reviewView) comment() (string, int, bool) {
	r, ok := v.row()
	if !ok || r.kind != rowThread || r.comment < 0 {
		return "", 0, false
	}
	return r.thread, r.comment, true
}

// ---------------------------------------------------------------------------
// Moving about
// ---------------------------------------------------------------------------

func (v *reviewView) move(delta int) { v.cursor += delta; v.clamp() }

func (v *reviewView) moveTo(i int) { v.cursor = i; v.clamp() }

// scroll moves the viewport without moving the cursor, the way a wheel does.
func (v *reviewView) scroll(delta int) {
	v.offset = min(max(v.offset+delta, 0), v.maxOffset())
}

func (v *reviewView) clamp() {
	v.hoffset = min(max(v.hoffset, 0), v.maxHOffset())
	v.cursor = min(max(v.cursor, 0), max(len(v.rows)-1, 0))
	rows := v.bodyRows()
	if rows <= 0 {
		return
	}
	if v.cursor < v.offset {
		v.offset = v.cursor
	}
	if v.cursor >= v.offset+rows {
		v.offset = v.cursor - rows + 1
	}
	v.offset = min(max(v.offset, 0), v.maxOffset())
}

func (v *reviewView) maxOffset() int { return listMaxOffset(len(v.rows), v.bodyRows()) }

// revealLead is how much of what came before a jumped-to line is left on
// screen above it. Landing a line hard against the top of the pane tells you
// nothing about what it follows, and in a diff that is half of what a line
// means. It matches the lead the markdown reader gives its own jumps.
const revealLead = 3

// reveal brings a row onto the screen with a little of what came before it. A
// row already on screen only takes the cursor: scrolling the pane out from
// under someone to show them something they were already looking at is how a
// jump ends up feeling like a lurch.
func (v *reviewView) reveal(i int) {
	i = min(max(i, 0), max(len(v.rows)-1, 0))
	v.cursor = i
	if i < v.offset || i >= v.offset+v.bodyRows() {
		v.offset = min(max(i-revealLead, 0), v.maxOffset())
	}
	v.clamp()
}

// revealSpan brings a run of rows onto the screen, keeping the top of it in
// view when the whole run will not fit — the box being written is worth less
// than the line it is about, so the line is the half that stays.
func (v *reviewView) revealSpan(first, last int) {
	rows := v.bodyRows()
	if rows <= 0 {
		return
	}
	if last >= v.offset+rows {
		v.offset = min(last-rows+1, v.maxOffset())
	}
	if first < v.offset {
		v.offset = first
	}
	v.offset = min(max(v.offset, 0), v.maxOffset())
}

// lineRow is the row holding a numbered line of a file as it now stands, and
// the nearest one below it when the diff does not show that line at all —
// most of a file is not in its own diff, so "line 24" usually means "as near
// to line 24 as this diff gets".
func (v *reviewView) lineRow(fi, want int) (row int, exact bool, ok bool) {
	best, bestNo := -1, 0
	for i, r := range v.rows {
		if r.kind != rowDiff || r.file != fi || r.line < 0 {
			continue
		}
		l, ok := v.files[fi].rowLine(r.line)
		if !ok || l.New <= 0 || !isCodeLine(l.Kind) {
			continue
		}
		if l.New == want {
			return i, true, true
		}
		// The nearest line at or after the one asked for, and failing that the
		// last one before it.
		if l.New > want && (bestNo < want || l.New < bestNo) {
			best, bestNo = i, l.New
		} else if l.New < want && bestNo < l.New && bestNo < want {
			best, bestNo = i, l.New
		}
	}
	if best < 0 {
		return 0, false, false
	}
	return best, false, true
}

// top puts a row at the top of the pane with the cursor on it, which is what
// jumping to a file means: the file starts where you are looking.
func (v *reviewView) top(i int) {
	v.cursor = min(max(i, 0), max(len(v.rows)-1, 0))
	v.offset = min(v.cursor, v.maxOffset())
	v.clamp()
}

// jumpToFile puts a file's header at the top of the pane.
func (v *reviewView) jumpToFile(path string) bool {
	i, ok := v.fileRow(path)
	if !ok {
		return false
	}
	v.top(i)
	return true
}

// fileRow is the row a file's heading is drawn on.
func (v *reviewView) fileRow(path string) (int, bool) {
	fi, ok := v.byPath[path]
	if !ok {
		return 0, false
	}
	for i, r := range v.rows {
		if r.kind == rowFile && r.file == fi {
			return i, true
		}
	}
	return 0, false
}

// cursorLine is the line of diff the cursor is on, as a file and an index into
// that file's lines, together with which side of the diff it is numbered on.
// It is what a change of view has to preserve, and is told in terms that
// survive the rows being built again underneath it.
//
// The index is -1 when the cursor is not on a line at all — a heading, a
// comment — which is a place the pane keeps by other means.
func (v *reviewView) cursorLine() (path string, at int, old bool) {
	path = v.pathAt()
	r, ok := v.row()
	if !ok || r.kind != rowDiff || r.file < 0 {
		return path, -1, false
	}
	f := v.files[r.file]
	at = v.liveSide(f, r.line)
	if at < 0 || at >= len(f.lines) {
		return path, -1, false
	}
	return path, at, f.lines[at].Kind == '-'
}

// goToDiffLine puts the cursor on the row drawing one line of one file, and
// reports whether there was one.
//
// It reveals rather than tops: a line already on the screen stays where it is.
// Changing how a diff is drawn should not scroll it, and landing the line hard
// against the top would throw away everything it follows — which in a diff is
// half of what a line means.
func (v *reviewView) goToDiffLine(path string, at int) bool {
	i, ok := v.diffRowAt(path, at)
	if !ok {
		return false
	}
	v.reveal(i)
	return true
}

// pathAt is the file the cursor is in.
func (v *reviewView) pathAt() string {
	if f, ok := v.file(); ok {
		return f.path
	}
	return ""
}

// step walks to the next row matching a test, in either direction, and reports
// whether it found one.
func (v *reviewView) step(dir int, want func(reviewRow) bool) bool {
	for i := v.cursor + dir; i >= 0 && i < len(v.rows); i += dir {
		if want(v.rows[i]) {
			v.top(i)
			return true
		}
	}
	return false
}

// threadRowAt is the first row of a conversation, which is where the cursor
// goes once a comment has been posted: the reply and resolve keys then act on
// what was just written.
func (v *reviewView) threadRowAt(id string) (int, bool) {
	for i, r := range v.rows {
		if r.kind == rowThread && r.thread == id && r.first {
			return i, true
		}
	}
	return 0, false
}

// diffRowAt is the row drawing one line of one file. The line is an index into
// the file's diff lines; the row it is drawn on is a split view's business, and
// is looked up rather than assumed to be the same number.
func (v *reviewView) diffRowAt(path string, line int) (int, bool) {
	fi, ok := v.byPath[path]
	if !ok {
		return 0, false
	}
	f := v.files[fi]
	if line < 0 || line >= len(f.rowAt) {
		return 0, false
	}
	want := f.rowAt[line]
	for i, r := range v.rows {
		if r.kind == rowDiff && r.file == fi && r.line == want {
			return i, true
		}
	}
	return 0, false
}

func (v *reviewView) jumpFile(dir int) bool {
	return v.step(dir, func(r reviewRow) bool { return r.kind == rowFile })
}

func (v *reviewView) jumpHunk(dir int) bool {
	return v.step(dir, func(r reviewRow) bool {
		if r.kind != rowDiff || r.file < 0 {
			return false
		}
		f := v.files[r.file]
		l, ok := f.rowLine(r.line)
		return ok && l.Kind == '@'
	})
}

// jumpThread walks to the next open conversation, which is the one thing in a
// review that is still asking for something.
func (v *reviewView) jumpThread(dir int, st *review.State) bool {
	return v.step(dir, func(r reviewRow) bool {
		if r.kind != rowThread || !r.first {
			return false
		}
		t := st.Thread(r.thread)
		return t != nil && !t.Resolved
	})
}

// ---------------------------------------------------------------------------
// Selection
// ---------------------------------------------------------------------------

func (v *reviewView) toggleVisual() {
	if v.anchor >= 0 {
		v.anchor = -1
		return
	}
	v.anchor = v.cursor
}

func (v *reviewView) visual() bool { return v.anchor >= 0 }
func (v *reviewView) clearVisual() { v.anchor = -1 }
func (v *reviewView) inSelection(i int) bool {
	if v.anchor < 0 {
		return false
	}
	return i >= min(v.anchor, v.cursor) && i <= max(v.anchor, v.cursor)
}

// ---------------------------------------------------------------------------
// Geometry
// ---------------------------------------------------------------------------

func (v *reviewView) bars() (vert, horz bool) {
	for i := 0; i < 3; i++ {
		vv := scrolls(len(v.rows), v.rowsWith(horz), v.width)
		hh := v.long > v.codeWidthWith(vv) && v.height > 1 && v.width > 1
		if vv == vert && hh == horz {
			break
		}
		vert, horz = vv, hh
	}
	return vert, horz
}

func (v *reviewView) rowsWith(horz bool) int {
	if horz {
		return max(v.height-1, 0)
	}
	return max(v.height, 0)
}

func (v *reviewView) rowWidth(vert bool) int {
	if vert {
		return max(v.width-1, 0)
	}
	return max(v.width, 0)
}

func (v *reviewView) codeWidthWith(vert bool) int {
	w := v.rowWidth(vert)
	if v.splitting() {
		w, _ = v.halvesOf(w)
	}
	return max(w-diffChromeWidth(v.numW, w)-len(diffGutter(' ')), 0)
}

func (v *reviewView) bodyRows() int {
	_, horz := v.bars()
	return v.rowsWith(horz)
}

func (v *reviewView) rowW() int {
	vert, _ := v.bars()
	return v.rowWidth(vert)
}

// chromeW is the columns every row spends before its content, whatever kind of
// row it is. Headers and comments draw the same blank columns the code draws
// its cursor bar and line numbers in, so that one number describes where the
// content of this pane begins — which is what the mouse selection needs.
func (v *reviewView) chromeW() int { return diffChromeWidth(v.numW, v.rowW()) }

func (v *reviewView) contentWidth() int { return max(v.rowW()-v.chromeW(), 0) }

func (v *reviewView) codeWidth() int {
	vert, _ := v.bars()
	return v.codeWidthWith(vert)
}

func (v *reviewView) maxHOffset() int {
	if v.codeWidth() <= 0 {
		return 0
	}
	return max(v.long-v.codeWidth(), 0)
}

// scrollH slides the code sideways, reporting whether it moved.
func (v *reviewView) scrollH(delta int) bool {
	want := min(max(v.hoffset+delta, 0), v.maxHOffset())
	if want == v.hoffset {
		return false
	}
	v.hoffset = want
	return true
}

// ---------------------------------------------------------------------------
// Drawing
// ---------------------------------------------------------------------------

// titleLabel is the left of the pane's title, and where in it the word naming
// the way the diff is drawn falls: `unified` or `split`.
//
// That word is the one part of the title that names a choice, so it is the one
// part that offers to change it — the same rule the top bar follows with the
// branch and the base. It is named even when it is the ordinary one, because a
// mode nothing ever mentions is a mode nobody finds; saying `unified` is how
// the reader learns there is anything else.
//
// The label and the span come out of one function because they are one
// arithmetic, for the same reason branchBar works that way: worked out in two
// places they drift apart the first time either changes.
//
// The columns are counted from the pane's left edge and include the one cell
// the title style pads with.
func (v *reviewView) titleLabel() (label string, modeX, modeW int) {
	label = "Review"
	if v.label != "" {
		label = v.label
	}
	label += " · "

	mode := "unified"
	if v.splitting() {
		mode = "split"
	}
	modeX, modeW = width(label)+1, width(mode)
	label += mode

	switch {
	case v.asked && v.split && !v.splitting():
		// Asked for and remembered, but not drawn. The status line says so at
		// the moment of asking and is gone by the next keystroke; this stays,
		// because otherwise the pane looks as though it ignored the request.
		label += " · too narrow to split"
	case v.onOldLine():
		// Which half the cursor is in is not always visible: a row with
		// nothing on the left draws the bar on the right whichever side is
		// live, so the title says which side the next key will act on.
		label += " · old side"
	}
	if v.hidden > 0 {
		label += " · " + itoa(v.hidden) + " hidden"
	}
	return label, modeX, modeW
}

// modeHit reports whether a column of the pane's title is on the word naming
// the way the diff is drawn. A word that ran off the end of a narrow title is
// not offered, the same care branchBar takes: nothing invisible is clickable.
func (v *reviewView) modeHit(x int) bool {
	_, at, w := v.titleLabel()
	if at+w > v.width-1 {
		return false
	}
	return x >= at && x < at+w
}

func (v *reviewView) view(st *styles, focused bool, rv *review.State) string {
	titleStyle := st.titleDiff
	if !focused {
		titleStyle = st.titleDim
	}
	label, _, _ := v.titleLabel()
	right := v.tally(rv)
	if v.hoffset > 0 {
		right = "col " + itoa(v.hoffset+1) + "   " + right
	}
	header := renderTitle(titleStyle, label, right, v.width)

	vert, horz := v.bars()
	rowW, body := v.rowWidth(vert), v.rowsWith(horz)

	rows := make([]string, 0, body)
	for i := v.offset; i < len(v.rows) && len(rows) < body; i++ {
		rows = append(rows, v.render(i, st, rowW, i == v.cursor && focused, v.inSelection(i)))
	}
	for len(rows) < body {
		rows = append(rows, strings.Repeat(" ", max(rowW, 0)))
	}
	if vert {
		attachVScroll(st, rows, vertTrack, len(v.rows), v.offset, v.maxOffset())
	}
	out := append([]string{header}, rows...)
	if horz {
		out = append(out, v.hbar(st, vert))
	}
	return lipgloss.JoinVertical(lipgloss.Left, out...)
}

// tally is the right hand end of the title: how much of the review is done.
//
// A file hidden by the filter is still part of the review and still counted
// here. It went off the screen because it was approved, and a tally that
// forgot it would report a review shrinking as it was completed.
func (v *reviewView) tally(rv *review.State) string {
	total := len(v.files) + v.hidden
	if total == 0 {
		return ""
	}
	approved := v.hidden // everything hidden is approved: that is why it went
	for _, f := range v.files {
		if f.approved {
			approved++
		}
	}
	s := itoa(approved) + "/" + itoa(total) + " approved"
	if rv != nil {
		if open := rv.Open(); open > 0 {
			s += " · " + plural(open, "open comment", "open comments")
		}
	}
	return s
}

func (v *reviewView) hbar(st *styles, vert bool) string {
	track := v.rowWidth(vert)
	lo, size := thumbSpan(track, v.long, v.codeWidthWith(vert), v.hoffset, v.maxHOffset())
	var b strings.Builder
	for i := 0; i < track; i++ {
		b.WriteString(scrollCell(st, i >= lo && i < lo+size, horzTrack, thumbGlyph))
	}
	if vert {
		b.WriteString(" ")
	}
	return b.String()
}

// render draws one row: the chrome every row shares, then its own content.
func (v *reviewView) render(i int, st *styles, rowW int, cursor, selected bool) string {
	if i < 0 || i >= len(v.rows) {
		return strings.Repeat(" ", max(rowW, 0))
	}
	r := v.rows[i]
	if r.kind == rowDiff {
		if v.splitting() {
			return v.renderSplit(r, st, rowW, cursor, selected)
		}
		return renderDiffRow(v.diffRowFor(r, st, rowW, cursor, selected))
	}
	if r.kind == rowFile {
		return v.headingRow(r, st, rowW, cursor, selected)
	}
	chrome, used := v.chrome(r, st, rowW, cursor, selected)
	if used >= rowW {
		return truncateANSI(chrome, rowW)
	}
	return chrome + v.content(i, st, rowW-used)
}

// headingRow draws a file's heading, which is the one row that does not keep
// to the pane's chrome: it has no line number to line up under, so it starts
// straight after the cursor bar, verdict first — so that read down the left
// edge, the headings are the list of what changed and where each file stands.
func (v *reviewView) headingRow(r reviewRow, st *styles, rowW int, cursor, selected bool) string {
	if rowW <= 0 {
		return ""
	}
	bar, band := st.body, st.fileBand
	switch {
	case cursor:
		bar, band = st.cursorBar, st.fileBandOn
	case selected:
		bar = st.selectBar
	}
	return bar.Render(" ") + v.fileHeader(v.files[r.file], st, band, rowW-1)
}

// chrome draws the columns in front of every non-code row. They are blank —
// there is no line number to give a comment — but they are drawn all the same,
// so that a comment lines up under the code it is about.
func (v *reviewView) chrome(r reviewRow, st *styles, rowW int, cursor, selected bool) (string, int) {
	w := min(v.chromeW(), max(rowW, 0))
	if w <= 0 {
		return "", 0
	}
	bar := st.body
	switch {
	case cursor:
		bar = st.cursorBar
	case selected:
		bar = st.selectBar
	}
	return bar.Render(" ") + st.body.Render(strings.Repeat(" ", w-1)), w
}

// content is a row's own text, exactly avail cells wide. It is the half of the
// row a mouse selection copies, which is why it is drawn on its own.
func (v *reviewView) content(i int, st *styles, avail int) string {
	if avail <= 0 || i < 0 || i >= len(v.rows) {
		return ""
	}
	r := v.rows[i]
	switch r.kind {
	case rowFile:
		style := st.fileBand
		if i == v.cursor {
			style = st.fileBandOn
		}
		// The heading starts left of where the pane's content does — see
		// contentXAt — so it is laid out for the room it really has and then
		// cut to the width asked for, with nothing put in place of what was
		// cut: what is highlighted has to be the cells that are copied. The
		// selection's grid is one width for the whole pane, so the last few
		// columns of a heading, which run past it, cannot be dragged over.
		lead := max(v.chromeW()-1, 0)
		return ansi.Truncate(v.fileHeader(v.files[r.file], st, style, avail+lead), avail, "")
	case rowThread:
		return v.threadRow(r, st, avail)
	case rowDraft:
		if v.draft == nil || r.line < 0 || r.line >= len(v.draft.lines) {
			return strings.Repeat(" ", avail)
		}
		return fitANSI(v.draft.lines[r.line], avail)
	case rowEmpty:
		return st.dimText.Render(pad(v.note, avail))
	}
	return strings.Repeat(" ", avail)
}

// fileHeader is the band that starts a file's section: its name, how much it
// changed, and every verdict standing against it. It is everything right of
// the cursor bar, and opens with the file's verdict and then the disclosure
// marker.
//
// The tail is worked out before the name, and the name gives up room to it.
// Everything in the tail is a thing to do something about — a comment waiting
// for an answer, an approval that has fallen — and a heading that dropped its
// verdicts to fit a long path would be hiding the half that matters.
func (v *reviewView) fileHeader(f *reviewFile, st *styles, band lipgloss.Style, avail int) string {
	fold := "▾ "
	if f.folded {
		fold = "▸ "
	}
	name := f.path
	if f.from != "" {
		name = f.from + " → " + f.path
	}

	// The tail gets whatever the whole name leaves. Only when that is not
	// enough for anything at all, and only when the file has something the
	// reader has to act on, does the name give way instead — elided from the
	// middle, so both which file it is and where it sits survive the cut.
	verdict, theirs := f.verdict(), f.theirVerdict()
	lead := verdict + " " + theirs + "  " + fold

	tail := f.headerTail(max(avail-width(lead+name)-2, 0))
	if tail == "" && f.speaks() {
		tail = f.headerTail(max(avail/2, 0))
	}

	left := lead + name
	if tail != "" {
		left = lead + elideMiddle(name, max(avail-width(tail)-width(lead)-2, 1))
	}
	text, right := left, ""
	if tail != "" && width(left)+width(tail)+2 <= avail {
		text += strings.Repeat(" ", avail-width(left)-width(tail)-1)
		right = tail + " "
	}
	text = pad(text, avail-width(right))
	if !strings.HasPrefix(text, lead) {
		return band.Render(text + right) // too narrow for the glyph to be the glyph
	}
	// The verdicts stand in front of the band rather than on it, a space
	// clear: on the band their colour has the band's to fight with.
	return f.verdictStyle(st).Render(verdict) + " " + f.theirStyle(st).Render(theirs) + " " +
		band.Render(strings.TrimPrefix(text, verdict+" "+theirs+" ")) + statInk(st, band, right)
}

// statInk draws the tail of a heading, with how much was added in green and
// how much was removed in red — the colours those lines have further down.
func statInk(st *styles, band lipgloss.Style, tail string) string {
	var b strings.Builder
	for i, word := range strings.Split(tail, " ") {
		if i > 0 {
			b.WriteString(band.Render(" "))
		}
		ink := band
		for sign, sty := range map[string]lipgloss.Style{"+": st.statAdd, "−": st.statDel} {
			n, signed := strings.CutPrefix(word, sign)
			if signed && n != "" && strings.Trim(n, "0123456789") == "" {
				ink = band.Foreground(sty.GetForeground())
			}
		}
		if word != "" {
			b.WriteString(ink.Render(word))
		}
	}
	return b.String()
}

// verdict is the first glyph in front of a file's name: where the file stands
// with the reader. Everything a review is trying to get to the bottom of is
// in this column, so it is read down the headings rather than across them.
//
// It is the reader's own verdict and nobody else's. What an agent made of the
// file is in the column beside it — see [reviewFile.theirVerdict] — so that a
// file somebody else has approved still reads here as one to be read.
func (f *reviewFile) verdict() string {
	switch {
	case f.approved:
		return "✓"
	case f.nullified:
		return "◈"
	case f.open > 0:
		return "◆"
	}
	return "○"
}

// verdictStyle is the ink the verdict is drawn in.
func (f *reviewFile) verdictStyle(st *styles) lipgloss.Style {
	switch {
	case f.approved:
		return st.verdictOK
	case f.nullified:
		return st.verdictStale
	case f.open > 0:
		return st.verdictComment
	}
	return st.dimText
}

// reviewerNames is a list of reviewers as a heading says it. A verdict can be
// written by hand without a name, and that one is said to be nobody's rather
// than left as a gap where a name should be.
func reviewerNames(names []string) string {
	out := make([]string, len(names))
	for i, n := range names {
		if out[i] = n; n == "" {
			out[i] = "nobody named"
		}
	}
	return strings.Join(out, ", ")
}

// theirVerdict is the second glyph in front of a file's name: where the file
// stands with everybody else. One approval is enough for the tick, since the
// column answers "has anyone else been through this", and the heading's tail
// says who.
func (f *reviewFile) theirVerdict() string {
	switch {
	case len(f.others) > 0:
		return "✓"
	case len(f.othersFell) > 0:
		return "◈"
	}
	return "·"
}

// theirStyle is the ink that second glyph is drawn in: the colours of the
// first, without its weight, since it is the reader's own that is the news.
func (f *reviewFile) theirStyle(st *styles) lipgloss.Style {
	switch {
	case len(f.others) > 0:
		return st.verdictOK.Bold(false)
	case len(f.othersFell) > 0:
		return st.verdictStale.Bold(false)
	}
	return st.dimText
}

// speaks reports whether a file's heading has something on it the reader has
// to do something about, as against the arithmetic of how much changed. It is
// what decides whether the name is worth shortening to make room.
func (f *reviewFile) speaks() bool {
	return f.total > 0 || f.approved || f.nullified || f.gone
}

// headerTail is everything a file's heading says beyond its name, fitted into
// the room there is.
//
// It gives way in a fixed order — how much changed, then what is left to
// commit, then the verdict, and last of all the comments — because that is the
// order the facts are worth losing in. A count of people waiting for an answer
// is the last thing to go, and it goes to a glyph and a number before it goes
// away.
func (f *reviewFile) headerTail(avail int) string {
	type part struct{ long, short string }
	var parts []part
	add := func(long, short string) {
		if long != "" {
			parts = append(parts, part{long, short})
		}
	}

	if f.plain {
		add("plain", "plain")
	}
	if f.adds > 0 {
		add("+"+itoa(f.adds), "+"+itoa(f.adds))
	}
	if f.dels > 0 {
		add("−"+itoa(f.dels), "−"+itoa(f.dels))
	}
	switch {
	case f.gone:
		add("no longer changed", "gone")
	default:
		add(f.state.Pending(), f.state.Pending())
	}
	switch {
	case f.approved:
		add("✓ approved", "✓")
	case f.nullified:
		add("◈ approval nullified", "◈")
	}
	if n, of := f.hunksApproved(); n > 0 && !f.approved {
		add("✓ "+itoa(n)+"/"+itoa(of)+" hunks", "✓"+itoa(n)+"/"+itoa(of))
	}
	// Whose the other approvals are. Its short spelling is nothing: the
	// column in front of the name has already said that there is one.
	switch {
	case len(f.others) > 0:
		add("✓ by "+reviewerNames(f.others), "")
	case len(f.othersFell) > 0:
		add("◈ by "+reviewerNames(f.othersFell), "")
	}
	switch {
	case f.open > 0:
		add(plural(f.open, "open comment", "open comments"), "◆"+itoa(f.open))
	case f.total > 0:
		add(plural(f.total, "comment", "comments"), "✓"+itoa(f.total))
	}

	// Drop from the front — the least it costs to lose — and fall back to the
	// shorter spelling of whatever is left before dropping any more of it.
	for drop := 0; drop < len(parts); drop++ {
		for _, short := range []bool{false, true} {
			var words []string
			for _, p := range parts[drop:] {
				if short {
					if p.short != "" {
						words = append(words, p.short)
					}
				} else {
					words = append(words, p.long)
				}
			}
			sep := " · "
			if short {
				sep = " "
			}
			if joined := strings.Join(words, sep); width(joined) <= avail {
				return joined
			}
		}
	}
	return ""
}

// threadRow draws one line of a conversation: a rule down the left, then the
// text, in ink that says which part of the thread it is.
func (v *reviewView) threadRow(r reviewRow, st *styles, avail int) string {
	indent := min(threadIndent, avail)
	rule, ink := "│", st.commentBody
	switch r.style {
	case thBorder:
		rule, ink = "┌", st.commentHead
	case thAuthor:
		ink = st.commentAuthor
	case thFoot:
		rule, ink = "└", st.commentFoot
	case thLoose:
		rule, ink = "┌", st.commentLoose
	}
	if collapsedThread(r) {
		rule = " "
	}
	body := pad(r.text, max(avail-indent-2, 0))
	return strings.Repeat(" ", indent) + st.commentRule.Render(rule+" ") + ink.Render(body)
}

// collapsedThread reports whether a row is the single line a closed thread
// collapses to. It wants no rule beside it, having nothing above or below it
// to join up with.
func collapsedThread(r reviewRow) bool {
	return r.kind == rowThread && r.style == thFoot && r.first
}

// renderSplit draws a row with the two sides of the change beside each other:
// what the line was on the left, what it became on the right.
//
// A hunk header belongs to neither side, so it runs the whole width — and so
// does the rule between the two, which is drawn rather than left as a gap so
// that a blank half reads as "nothing here" rather than as the end of the row.
//
// The cursor bar goes on the half the cursor is in rather than across both,
// because on a row that changed a line the two halves are two different lines
// and a key is about to act on one of them. Where the row draws the same line
// twice — a line neither side touched — both halves light, since there is no
// choice being made and a bar down half an unchanged line would look like one.
func (v *reviewView) renderSplit(r reviewRow, st *styles, rowW int, cursor, selected bool) string {
	f := v.files[r.file]
	if l, ok := f.rowLine(r.line); ok && !isCodeLine(l.Kind) {
		return renderDiffRow(v.diffRowFor(r, st, rowW, cursor, selected))
	}
	ref := f.rows[r.line]
	live := v.liveSide(f, r.line)
	leftW, rightW := v.halvesOf(rowW)
	return v.half(f, ref.old, st, leftW, cursor && ref.old == live, selected) +
		st.commentRule.Render("│") +
		v.half(f, ref.new, st, rightW, cursor && ref.new == live, selected)
}

// half draws one side of a split row, or an empty column where the change has
// nothing on that side — a line it only added, or only took away.
func (v *reviewView) half(f *reviewFile, i int, st *styles, w int, cursor, selected bool) string {
	if i < 0 {
		return renderDiffRow(diffRow{
			bar: barStyle(st, cursor, selected), mark: st.body,
			numW: v.numW, numSty: st.dimText,
			gutter: diffGutter(' '), base: st.body, emphSty: st.body,
			width: w,
		})
	}
	return renderDiffRow(v.diffRowForLine(f, i, st, w, cursor, selected))
}

// barStyle is the one column bar at the left of a row: gold for the cursor,
// blue for a selection, and nothing at all the rest of the time.
func barStyle(st *styles, cursor, selected bool) lipgloss.Style {
	switch {
	case cursor:
		return st.cursorBar
	case selected:
		return st.selectBar
	}
	return st.body
}

// diffRowFor describes one line of code. Only the two bars depend on where the
// cursor is; everything else is a function of the line, which is what lets a
// row be rendered for a line that is not currently on screen — the mouse
// selection reads rows either side of the viewport.
func (v *reviewView) diffRowFor(r reviewRow, st *styles, rowW int, cursor, selected bool) diffRow {
	f := v.files[r.file]
	return v.diffRowForLine(f, f.primary(r.line), st, rowW, cursor, selected)
}

// diffRowForLine describes one line of one file, at a given width — which in a
// split view is half the row.
func (v *reviewView) diffRowForLine(f *reviewFile, i int, st *styles, rowW int, cursor, selected bool) diffRow {
	if i < 0 || i >= len(f.lines) {
		return diffRow{bar: barStyle(st, cursor, selected), numW: v.numW,
			numSty: st.dimText, base: st.body, emphSty: st.body, width: rowW}
	}
	l := f.lines[i]

	bar := barStyle(st, cursor, selected)
	mark, marked := st.body, f.marked(i)
	if marked {
		mark = st.pendingBar
	}
	numSty := st.dimText
	if cursor {
		numSty = st.numberOn
	}

	if !isCodeLine(l.Kind) {
		style, text := st.diffMeta, l.Text
		switch l.Kind {
		case '@':
			style, text = st.diffHunk, f.hunkText(i)
		case 'g':
			style, text = st.dimText, f.gapText(i)
		}
		return diffRow{
			bar: bar, mark: mark, marked: marked,
			numW: v.numW, numSty: numSty,
			gutter: "", code: text, base: style, emphSty: style,
			hscroll: v.hoffset, width: rowW,
		}
	}

	base, emphSty := st.body, st.body
	switch l.Kind {
	case '+':
		base, emphSty = st.diffAdd, st.diffAddEmph
	case '-':
		base, emphSty = st.diffDel, st.diffDelEmph
	}
	return diffRow{
		bar: bar, mark: mark, marked: marked,
		number: lineNumber(l), numW: v.numW, numSty: numSty,
		gutter: diffGutter(l.Kind), code: codeAt(f.texts, i),
		spans: spanAt(f.spans, i), emph: emphAt(f.emph, i),
		base: base, emphSty: emphSty,
		palette: st.syntax, hscroll: v.hoffset, width: rowW,
	}
}

func codeAt(texts []string, i int) string {
	if i < 0 || i >= len(texts) {
		return ""
	}
	return texts[i]
}

func spanAt(spans [][]span, i int) []span {
	if i < 0 || i >= len(spans) {
		return nil
	}
	return spans[i]
}

func emphAt(e []emphasis, i int) emphasis {
	if i < 0 || i >= len(e) {
		return emphasis{}
	}
	return e[i]
}
