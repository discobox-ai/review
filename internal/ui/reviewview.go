package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
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

	// state is the file's working tree status, which is what says whether the
	// change has been committed. It is empty while a single commit is being
	// read, where the question does not arise.
	state git.State

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

	// Filled in from the review before the rows are built.
	approved  bool
	nullified bool
	folded    bool
	threads   map[int][]*review.Thread // by index into lines
	loose     []*review.Thread         // in this file, but on no line of it
	open      int
	total     int
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

	// hidden counts the files left out because they are done with. They are
	// still part of the review, so the tally still counts them; they are just
	// not on the screen.
	hidden int

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
		// to two rows, and a cursor that kept its distance from the heading
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

// foldHit is how much of a file's heading, in columns of content, is the
// disclosure marker rather than the name: the glyph and the space after it.
const foldHit = 2

// minSplitWidth is the narrowest a pane can be and still be worth splitting.
// It leaves each side about thirty columns of code once the numbers and the
// markers have taken theirs — under that a diff has to be scrolled sideways to
// be read twice, which is worse than reading it once.
//
// It is measured against the pane rather than the window, and the list and the
// history take about forty-seven columns before this one gets any: eighty here
// is a terminal around a hundred and thirty. That is the number this is chosen
// against, since a review opens split and a threshold most terminals fall
// short of would be a default nobody saw. z zooms the pane over the whole
// window, which is the other way to find the room.
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
		v.long = max(v.long, longestLine(f.texts))
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
			rows = append(rows, reviewRow{kind: rowGap, file: fi, line: -1})
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
// The width is set by layout, which runs on a great deal more than a resize —
// taking the focus moves it, because the left column gives up its space on a
// narrow window. Nothing there knows about rows, and the rows decide whether
// the diff is split, so without this a Tab on an eighty-column terminal draws
// split rows through the unified renderer: every removal that had an addition
// paired with it simply vanishes.
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
		// Arriving at a file puts its heading at the top of the pane, and the
		// relay that follows must not undo that: → in the list jumps to the
		// file before the focus moves, which is what changes the width.
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

// pathAt is the file the cursor is in, for keeping the list beside the pane in
// step with what is being read.
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
// here. It went off the screen because it was finished with, and a tally that
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
	chrome, used := v.chrome(r, st, rowW, cursor, selected)
	if used >= rowW {
		return truncateANSI(chrome, rowW)
	}
	return chrome + v.content(i, st, rowW-used)
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
	fill := st.body
	if r.kind == rowFile {
		fill = st.fileBand
		if cursor {
			fill = st.fileBandOn
		}
	}
	return bar.Render(" ") + fill.Render(strings.Repeat(" ", w-1)), w
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
		return v.fileHeader(v.files[r.file], st, style, avail)
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
// changed, and every verdict standing against it.
//
// The tail is worked out before the name, and the name gives up room to it.
// Everything in the tail is a thing to do something about — a comment waiting
// for an answer, an approval that has fallen — where the name is also in the
// list beside this pane, three columns to the left. A heading that dropped its
// verdicts to fit a long path would be hiding the half that matters.
func (v *reviewView) fileHeader(f *reviewFile, st *styles, band lipgloss.Style, avail int) string {
	fold := "▾ " // foldHit columns wide: see the click that lands on it
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
	// front, since the end of a path is the half that names the file.
	tail := f.headerTail(max(avail-width(fold+name)-2, 0))
	if tail == "" && f.speaks() {
		tail = f.headerTail(max(avail/2, 0))
	}

	left := fold + name
	if tail != "" {
		left = fold + elideLeft(name, max(avail-width(tail)-width(fold)-2, 1))
	}
	text := left
	if tail != "" && width(left)+width(tail)+2 <= avail {
		text = left + strings.Repeat(" ", avail-width(left)-width(tail)-1) + tail + " "
	}
	return band.Render(pad(text, avail))
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
					words = append(words, p.short)
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
		style := st.diffMeta
		if l.Kind == '@' {
			style = st.diffHunk
		}
		return diffRow{
			bar: bar, mark: mark, marked: marked,
			numW: v.numW, numSty: numSty,
			gutter: "", code: l.Text, base: style, emphSty: style,
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
