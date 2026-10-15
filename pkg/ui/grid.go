package ui

import (
	"strings"

	"charm.land/lipgloss/v2"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/discobox-ai/x/selection"
)

// A pane offers its text to the mouse as content coordinates: an absolute line
// index that names the same line for as long as that line exists, and columns
// counted from the first column of content rather than from the pane's left
// edge.
//
// The distinction is what keeps a dragged selection worth pasting. Panes draw
// chrome down their left edge — the diff's cursor bar and line numbers, a file
// list's indent and status icon, the reader's changed-block markers — and none
// of it is text anyone means to copy. It is drawn to be looked at, not taken.
// So the grid starts after it, and contentX puts the offset back when a span
// is painted onto the screen.
//
// The +/- column of a diff is on the content side of that line, because it is
// the diff: a range of changed lines copied without it is no longer a diff.
type selectable interface {
	// contentX is how many columns of chrome the pane draws before its
	// content starts.
	contentX() int

	// contentWidth is how many columns of content one row holds.
	contentWidth() int

	// contentLines is the absolute range of lines that exists:
	// [first, first+count).
	contentLines() (first, count int)

	// contentRow renders one absolute line's content the way it is drawn,
	// styling included, exactly contentWidth cells wide. Reading the same
	// text that is on screen is what keeps what you see and what you copy the
	// same thing.
	contentRow(line int) string

	// contentOffset is the absolute line index of the pane's first body row,
	// which is what turns a screen row into a line.
	contentOffset() int

	// contentRows is how many body rows the pane draws below its title. It
	// bounds both the rows a selection may be painted onto and the rows a
	// click can land on, so that whatever a pane draws under its body — the
	// reader's hint line — is not mistaken for content.
	contentRows() int
}

// contentXAt is contentX for one line, for the pane whose rows do not all
// start their content in the same column.
func contentXAt(p selectable, line int) int {
	if s, ok := p.(interface{ contentXAt(line int) int }); ok {
		return s.contentXAt(line)
	}
	return p.contentX()
}

// paneGrid adapts a pane to [selection.Grid] by rendering a row and reading
// the cells back out of it.
//
// Going through the rendered row rather than around it is the point. The row
// is the only place the pane's own truncation, padding and wide-rune handling
// have all been applied, so cells read from it are exactly the cells on
// screen; anything reconstructed alongside would be a second implementation
// of the same layout, free to disagree with the first.
type paneGrid struct {
	pane selectable

	// One decoded row is kept, because that is the access pattern: extracting
	// a line asks for every column of it in turn, and a span asks for one
	// column of each line. The cache is dropped whenever the selection is,
	// which is whenever the content underneath it may have moved — see
	// [mouseState.clear].
	line   int
	cached uv.Line
	valid  bool
}

func newPaneGrid(p selectable) *paneGrid { return &paneGrid{pane: p, line: -1} }

// reset drops the decoded row.
func (g *paneGrid) reset() { g.valid, g.line = false, -1 }

func (g *paneGrid) Width() int { return max(g.pane.contentWidth(), 0) }

func (g *paneGrid) Lines() (int, int) { return g.pane.contentLines() }

// Wrapped is always false: every pane here truncates a line too long for it
// rather than wrapping it, so no line is ever the continuation of another.
func (g *paneGrid) Wrapped(int) bool { return false }

func (g *paneGrid) Cell(line, col int) *uv.Cell {
	width := g.Width()
	if col < 0 || col >= width {
		return nil
	}
	first, count := g.pane.contentLines()
	if line < first || line >= first+count {
		return nil
	}
	if !g.valid || g.line != line {
		g.cached = decodeRow(g.pane.contentRow(line), width)
		g.line, g.valid = line, true
	}
	if col >= len(g.cached) {
		return nil
	}
	return &g.cached[col]
}

// decodeRow turns a rendered row back into the cells it draws, padded or
// truncated to exactly width. A wide rune becomes its glyph in the primary
// cell and a zero cell in each continuation column, which is the shape
// [selection] snaps endpoints and highlights against.
func decodeRow(row string, width int) uv.Line {
	if width <= 0 {
		return nil
	}
	scr := uv.NewScreenBuffer(width, 1)
	uv.NewStyledString(row).Draw(&scr, scr.Bounds())
	return scr.Line(0)
}

// highlightSpan repaints the columns [from, to] of a rendered row, both ends
// inclusive, and returns the row again. The row is measured rather than sized
// by its caller, because a pane may draw past its content — the scrollbar
// column — and a highlight must not truncate what it does not own.
//
// The highlight is applied to the row's cells rather than spliced into its
// escape sequences: the pane has already decided what color every cell is,
// and reverse video composes with whatever that turned out to be — a diff's
// colored band, a syntax-highlighted identifier, the cursor bar — without
// needing to know any of it. It is also what every terminal does for its own
// selection, so a selection here looks like a selection.
func highlightSpan(row string, from, to int) string {
	width := lipgloss.Width(row)
	if width <= 0 || to < from {
		return row
	}
	line := decodeRow(row, width)
	for col := max(from, 0); col <= to && col < len(line); col++ {
		c := &line[col]
		// A wide rune's continuation column stays as it is: the glyph and the
		// style both live in the primary cell, which covers both columns.
		if c.IsZero() {
			continue
		}
		c.Style.Attrs ^= uv.AttrReverse
	}
	return strings.TrimSuffix(line.Render(), "\n")
}

// paintSelection draws the selection over a pane's rendered view: the string a
// pane's view method returned, one title row followed by its body rows.
//
// It is a no-op for a pane holding none of the selection, which is every pane
// but one and, most of the time, all of them — so the ordinary frame costs a
// single comparison and no per-cell work at all.
func paintSelection(view string, sel *selection.Model, pane selectable) string {
	if sel == nil || !sel.Active() {
		return view
	}
	rows := strings.Split(view, "\n")
	offset := pane.contentOffset()
	// Row 0 is the pane's title, and rows past its body are whatever it draws
	// under them — the reader's hint line — so only the body is painted.
	last := min(len(rows)-1, pane.contentRows())
	touched := false
	for j := 1; j <= last; j++ {
		start, end, ok := sel.Span(offset + j - 1)
		if !ok {
			continue
		}
		x0 := contentXAt(pane, offset+j-1)
		rows[j] = highlightSpan(rows[j], x0+start, x0+end)
		touched = true
	}
	if !touched {
		return view
	}
	return strings.Join(rows, "\n")
}
