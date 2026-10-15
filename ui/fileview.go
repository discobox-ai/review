package ui

import (
	"strings"

	"github.com/discobox-ai/review/internal/git"
)

// fileView is the full screen reader that v opens: a whole file, rather than
// the parts of it a diff has something to say about.
//
// A review is read as a diff because a diff is what is being proposed, but
// sooner or later a line raises a question the diff cannot answer — what is
// this function called from, what does the block above it do, is that variable
// shadowed. Answering it by leaving for an editor and coming back loses your
// place; answering it here does not.
//
// It draws with the same row renderer the review does, so the line numbers,
// the syntax colours and the margin are the ones already on screen — and the
// lines the review touches are marked in that margin, so the diff you came
// from is still findable in the file you came to.
type fileView struct {
	path string
	side fileSide

	// base is what the other side is called, for the title and for the key
	// that switches to it. It is empty when there is no other side to go to.
	base string

	lines   []git.DiffLine // one per line of the file, all of them context
	texts   []string       // the same, with tabs expanded
	spans   [][]span
	numW    int
	long    int
	changed map[int]bool // lines the review touches

	// jumps are the first line of each run of touched lines, which n and p
	// walk, and cur is the one they last landed on.
	jumps []int
	cur   int

	cursor  int
	offset  int
	hoffset int

	width, height int
}

// fileSide is which version of a file is being read.
type fileSide int

const (
	// sideNow is the file as the review's new side has it: the working tree,
	// or the commit being read.
	sideNow fileSide = iota
	// sideBefore is the file as the old side has it: the base, or the parent
	// of the commit being read.
	sideBefore
)

// fileLead is how many lines above a touched run n and p leave on screen, so
// the change arrives with a little of what came before it.
const fileLead = 3

// newFileView lays a file out for reading. changed is the set of lines the
// review touches, in this side's own numbering.
func newFileView(path, base string, side fileSide, text string, changed map[int]bool, width, height int) *fileView {
	v := &fileView{path: path, base: base, side: side, changed: changed, cur: -1}

	body := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	v.lines = make([]git.DiffLine, len(body))
	v.texts = make([]string, len(body))
	for i, l := range body {
		// Every line is context: this is the file, not a change to it. The
		// row renderer then draws it the way it draws an unchanged line of
		// diff, which is exactly right.
		//
		// They are all one hunk, numbered from the top, because that is what
		// the highlighter reads: it lexes a hunk's worth of lines as a single
		// text, and a whole file is the largest and most correct such text
		// there is. Left unnumbered they are skipped, and the file comes out
		// grey.
		v.lines[i] = git.DiffLine{Text: " " + l, Kind: ' ', Hunk: 0, Line: i,
			Old: i + 1, New: i + 1, Seam: i + 1}
		v.texts[i] = expandTabs(l)
	}
	v.spans = highlightLines(path, v.lines, v.texts)
	v.numW = numberWidth(v.lines)
	v.long = longestLine(v.texts)

	// Where the touched runs begin, so that n and p have somewhere to go.
	for i := range v.lines {
		if changed[i+1] && (i == 0 || !changed[i]) {
			v.jumps = append(v.jumps, i)
		}
	}
	v.resize(width, height)
	return v
}

func (v *fileView) resize(width, height int) {
	v.width, v.height = width, height
	v.clamp()
}

// body is how many lines of the file are on screen, once the title and the
// hint have taken their rows.
func (v *fileView) body() int { return max(v.height-2, 1) }

func (v *fileView) move(delta int) { v.cursor += delta; v.clamp() }

func (v *fileView) moveTo(i int) { v.cursor = i; v.clamp() }

// scroll moves the viewport without moving the cursor, the way a wheel does.
func (v *fileView) scroll(delta int) {
	v.offset = min(max(v.offset+delta, 0), listMaxOffset(len(v.lines), v.body()))
}

func (v *fileView) scrollH(delta int) {
	v.hoffset = min(max(v.hoffset+delta, 0), max(v.long-v.codeWidth(), 0))
}

func (v *fileView) codeWidth() int {
	return max(v.width-diffChromeWidth(v.numW, v.width)-len(diffGutter(' ')), 0)
}

func (v *fileView) clamp() {
	v.cursor = min(max(v.cursor, 0), max(len(v.lines)-1, 0))
	v.hoffset = min(max(v.hoffset, 0), max(v.long-v.codeWidth(), 0))
	rows := v.body()
	if rows <= 0 {
		return
	}
	if v.cursor < v.offset {
		v.offset = v.cursor
	}
	if v.cursor >= v.offset+rows {
		v.offset = v.cursor - rows + 1
	}
	v.offset = min(max(v.offset, 0), listMaxOffset(len(v.lines), rows))
}

// jump walks to the next or previous run of lines the review touches, landing
// a little above it so the change arrives with some of its context.
func (v *fileView) jump(delta int) {
	if len(v.jumps) == 0 {
		return
	}
	next := v.cur + delta
	if v.cur < 0 {
		// Not on one yet: take the first run at or after where we are looking.
		next = 0
		for i, at := range v.jumps {
			if at >= v.offset {
				next = i
				break
			}
		}
		if delta < 0 {
			next = max(next-1, 0)
		}
	}
	v.cur = min(max(next, 0), len(v.jumps)-1)
	v.cursor = v.jumps[v.cur]
	v.offset = min(max(v.cursor-fileLead, 0), listMaxOffset(len(v.lines), v.body()))
	v.clamp()
}

// goToLine puts a numbered line of the file on screen, which is how coming
// here from a line of the diff keeps your place.
func (v *fileView) goToLine(n int) {
	if n <= 0 {
		return
	}
	v.cursor = min(n-1, max(len(v.lines)-1, 0))
	v.offset = min(max(v.cursor-fileLead, 0), listMaxOffset(len(v.lines), v.body()))
	v.clamp()
}

// title says which file, and which of its two versions.
func (v *fileView) title() string {
	if v.side == sideBefore {
		if v.base != "" {
			return v.path + "  ·  as " + v.base + " has it"
		}
		return v.path + "  ·  before"
	}
	return v.path
}

func (v *fileView) view(st *styles) string {
	body := v.body()
	end := min(v.offset+body, len(v.lines))

	right := itoa(v.offset+1) + "–" + itoa(end) + " of " + itoa(len(v.lines))
	if n := len(v.jumps); n > 0 {
		if v.cur >= 0 {
			right = "change " + itoa(v.cur+1) + " of " + itoa(n) + " · " + right
		} else {
			right = plural(n, "changed run", "changed runs") + " · " + right
		}
	}
	if v.hoffset > 0 {
		right = "col " + itoa(v.hoffset+1) + " · " + right
	}

	var b strings.Builder
	b.WriteString(renderTitle(st.titleDiff, v.title(), right, v.width))
	b.WriteString("\n")
	for i := v.offset; i < end; i++ {
		b.WriteString(renderDiffRow(v.rowFor(i, st, i == v.cursor)))
		b.WriteString("\n")
	}
	for i := end - v.offset; i < body; i++ {
		b.WriteString(strings.Repeat(" ", max(v.width, 0)))
		b.WriteString("\n")
	}
	b.WriteString(st.dimText.Render(pad(v.hint(), v.width)))
	return b.String()
}

func (v *fileView) hint() string {
	parts := []string{"↑/↓ scroll", "g/G top/bottom"}
	if len(v.jumps) > 0 {
		parts = append([]string{"n/p changed run"}, parts...)
	}
	if v.base != "" {
		if v.side == sideNow {
			parts = append(parts, "o the version "+v.base+" has")
		} else {
			parts = append(parts, "v the version on disk")
		}
	}
	parts = append(parts, "e edit", "Esc or q close")
	return strings.Join(parts, " · ")
}

// rowFor draws one line of the file. The margin marks the lines the review
// touches, using the same column the review marks an uncommitted line in —
// which is the point: the two are the same margin, saying "look here" in the
// same place.
func (v *fileView) rowFor(i int, st *styles, cursor bool) diffRow {
	bar := st.body
	if cursor {
		bar = st.cursorBar
	}
	numSty := st.dimText
	if cursor {
		numSty = st.numberOn
	}
	mark, marked := st.body, v.changed[i+1]
	if marked {
		mark = st.mdMark
		if v.cur >= 0 && v.cur < len(v.jumps) && v.inRun(i, v.jumps[v.cur]) {
			mark = st.mdMarkCur
		}
	}
	return diffRow{
		bar: bar, mark: mark, marked: marked,
		number: i + 1, numW: v.numW, numSty: numSty,
		gutter: diffGutter(' '), code: codeAt(v.texts, i),
		spans: spanAt(v.spans, i), base: st.body, emphSty: st.body,
		palette: st.syntax, hscroll: v.hoffset, width: v.width,
	}
}

// inRun reports whether a line belongs to the run of touched lines that starts
// at from, so the one n and p last landed on can be picked out from the rest.
func (v *fileView) inRun(i, from int) bool {
	if i < from || !v.changed[i+1] {
		return false
	}
	for at := from; at <= i; at++ {
		if !v.changed[at+1] {
			return false
		}
	}
	return true
}
