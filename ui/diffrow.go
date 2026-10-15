package ui

// One line of code, drawn.
//
// The pane above this decides which lines to draw and in what order; this file
// decides what a line looks like once that is settled, and is the one place
// that knows about the cursor bar, the line number column and the +/- gutter.

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/discobox-ai/review/internal/git"
	"github.com/mattn/go-runewidth"
)

// hScrollStep is how far one press of left or right slides the code, two tab
// stops: far enough that walking across a long line does not take a dozen
// presses, near enough that the eye keeps its place.
const hScrollStep = 2 * tabWidth

// lineNumber is the number shown for a line: its own side's, since that is the
// one it exists on.
func lineNumber(l git.DiffLine) int {
	if l.Kind == '-' {
		return l.Old
	}
	return l.New
}

// numberWidth sizes the line number column to the largest number that will be
// printed, so the numbers do not jitter from one file to the next.
func numberWidth(lines []git.DiffLine) int {
	largest := 0
	for _, l := range lines {
		largest = max(largest, max(l.Old, l.New))
	}
	return max(len(itoa(largest)), minNumberWidth)
}

// minNumberWidth keeps the column steady for short files.
const minNumberWidth = 3

// longestLine measures the widest rendered line, which is what the horizontal
// scroll has to cover. Tabs are already expanded, so runes count columns.
func longestLine(texts []string) int {
	longest := 0
	for _, t := range texts {
		longest = max(longest, len([]rune(t)))
	}
	return longest
}

// diffGutter is the two column marker in front of each line of code.
//
// A unified diff puts + and - in the same column, so an added line's code
// starts immediately after the +. Giving each side a column of its own means
// an addition always has a space between the marker and the code, which reads
// more like the file itself; a deletion sits tight against its marker, which
// matters less because deletions are the side you skim.
//
//	"+ added"
//	" -removed"
//	"  context"
func diffGutter(kind byte) string {
	switch kind {
	case '+':
		return "+ "
	case '-':
		return " -"
	default:
		return "  "
	}
}

// diffRow is everything needed to draw one line of code in the review.
type diffRow struct {
	bar     lipgloss.Style // the one column cursor/selection bar at the left
	mark    lipgloss.Style // the one column "not committed yet" bar beside it
	marked  bool           // whether that bar is lit for this line
	number  int            // line number, 0 to leave the column blank
	numW    int            // width of the number column
	numSty  lipgloss.Style
	gutter  string // the two column +/- marker
	code    string // the line's text, tabs already expanded
	spans   []span
	emph    emphasis
	base    lipgloss.Style // the line's band: a background, or none for context
	emphSty lipgloss.Style // the stronger band for the characters that changed
	palette syntaxPalette
	hscroll int // runes of code scrolled off to the left
	width   int
}

// renderDiffRow draws one line as
//
//	[bar][mark][number][ ][gutter][code, padded to the pane width]
//
// resolving four channels at once. The background says what the diff did:
// a band across added and removed lines, a stronger shade over the characters
// that actually changed. The foreground says what the code is, from the
// lexer. The bar at the left edge says where the cursor is and what is
// selected — it lives outside the code because the background it would
// otherwise use is already carrying the diff. And the column beside it says
// whether the change on this line has been committed, which is the one fact
// about a line that neither the diff nor the code can express.
func renderDiffRow(r diffRow) string {
	if r.width <= 0 {
		return ""
	}
	chrome, used := renderDiffChrome(r)
	if used >= r.width {
		return truncateANSI(chrome, r.width)
	}
	return chrome + renderDiffContent(r, r.width-used)
}

// renderDiffChrome draws the columns in front of the code — the cursor bar and
// the line number — and reports how many columns they took.
//
// It is split out because those columns are the pane's own furniture rather
// than the diff: the mouse selection starts after them, so that dragging over
// a diff yields the diff rather than a column of line numbers nobody wants
// pasted anywhere.
func renderDiffChrome(r diffRow) (string, int) {
	var b strings.Builder
	// Both bar columns are always drawn, blank when they have nothing to say,
	// so rows never shift sideways.
	b.WriteString(r.bar.Render(" "))
	used := 1
	if used < r.width {
		glyph := " "
		if r.marked {
			glyph = pendingGlyph
		}
		b.WriteString(r.mark.Render(glyph))
		used++
	}
	if used < r.width && r.numW > 0 {
		numText := strings.Repeat(" ", r.numW)
		if r.number > 0 {
			n := itoa(r.number)
			if len(n) < r.numW {
				n = strings.Repeat(" ", r.numW-len(n)) + n
			}
			numText = n
		}
		b.WriteString(r.numSty.Render(numText + " "))
		used += runewidth.StringWidth(numText) + 1
	}
	return b.String(), used
}

// diffChromeWidth is how many columns renderDiffChrome takes, without going to
// the trouble of rendering it. Every row in the review pane draws the same
// chrome, code or not, so that one number describes the whole pane: it is
// where the content starts, which is where a mouse selection starts too.
func diffChromeWidth(numW, width int) int {
	if width <= 1 {
		return min(width, 1)
	}
	if numW > 0 && width > 2 {
		return 2 + numW + 1
	}
	return 2
}

// pendingGlyph fills the "not committed yet" column. It is a solid half block
// so that a run of uncommitted lines reads as one bar down the margin rather
// than as a column of separate marks.
const pendingGlyph = "\u258d"

// renderDiffContent draws the +/- marker and the line of code into exactly
// avail columns, padding or truncating to fill them.
//
// The marker is on this side of the split rather than with the chrome because
// it is the diff: a run of changed lines copied without it is no longer one.
func renderDiffContent(r diffRow, avail int) string {
	if avail <= 0 {
		return ""
	}
	var b strings.Builder
	used := 0

	// The gutter stays put and the code slides under it, so the +/- marker and
	// the line number keep saying which line is which however far right the
	// pane has been scrolled.
	code := []rune(r.code)
	if r.hscroll > 0 {
		code = code[min(r.hscroll, len(code)):]
	}
	gutW := len([]rune(r.gutter))
	runes := append([]rune(r.gutter), code...)
	// Spans and the emphasis are offsets into the whole code line, so what
	// scrolled off shifts them left; the gutter is where they stop.
	offset := gutW - r.hscroll
	fg := make([]color.Color, len(runes))
	for _, s := range r.spans {
		value, ok := r.palette.color(s.token)
		if !ok {
			continue
		}
		for at := max(s.start+offset, gutW); at < s.end+offset && at < len(runes); at++ {
			fg[at] = value
		}
	}
	changed := make([]bool, len(runes))
	if !r.emph.empty() {
		for at := max(r.emph.start+offset, gutW); at < r.emph.end+offset && at < len(runes); at++ {
			changed[at] = true
		}
	}

	type appearance struct {
		fg      color.Color
		changed bool
	}
	var buf []rune
	var current appearance
	flush := func() {
		if len(buf) == 0 {
			return
		}
		style := r.base
		if current.changed {
			style = r.emphSty
		}
		if current.fg != nil {
			style = style.Foreground(current.fg)
		}
		b.WriteString(style.Render(string(buf)))
		buf = buf[:0]
	}

	truncated := false
	for at, ch := range runes {
		w := runewidth.RuneWidth(ch)
		if used+w > avail {
			truncated = true
			break
		}
		next := appearance{fg: fg[at], changed: changed[at]}
		if next != current {
			flush()
			current = next
		}
		buf = append(buf, ch)
		used += w
	}
	flush()
	if truncated && used < avail {
		b.WriteString(r.base.Render("…"))
		used++
	}
	// The band runs to the right margin, so a changed line reads as a solid
	// block rather than as ragged text.
	if used < avail {
		b.WriteString(r.base.Render(strings.Repeat(" ", avail-used)))
	}
	return b.String()
}

func itoa(i int) string {
	if i == 0 {
		return "0"
	}
	neg := i < 0
	if neg {
		i = -i
	}
	var b [20]byte
	p := len(b)
	for i > 0 {
		p--
		b[p] = byte('0' + i%10)
		i /= 10
	}
	if neg {
		p--
		b[p] = '-'
	}
	return string(b[p:])
}
