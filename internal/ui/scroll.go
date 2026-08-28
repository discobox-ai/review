package ui

// The scrollbars: a light rule for the track and a solid block for the thumb,
// both in grey, so they read as furniture rather than as part of the pane.
//
// The lists use no track at all. They sit in the left column, where the pane
// separator is already a rule down the very edge the bar wants, and two rules
// side by side read as a border rather than as a scrollbar — so the thumb
// slides along the separator instead. The diff has nothing but the window edge
// there, so it draws its own.
const (
	vertTrack  = "│"
	horzTrack  = "─"
	blankTrack = " "
	thumbGlyph = "█"
)

func scrollCell(st *styles, thumb bool, track, block string) string {
	if thumb {
		return st.scrollThumb.Render(block)
	}
	return st.scrollTrack.Render(track)
}

// thumbSpan sizes and places the thumb: as much of the track as the window is
// of the whole, at the same fraction along. It is kept at least one cell long,
// and off the ends of the track unless the view really is at one of them, so
// that "nearly at the top" never reads as "at the top".
func thumbSpan(track, total, window, offset, maxOffset int) (lo, size int) {
	if track <= 0 {
		return 0, 0
	}
	if total <= window || maxOffset <= 0 {
		return 0, track
	}
	size = max(track*window/total, 1)
	if size >= track {
		size = track - 1
	}
	lo = (track - size) * offset / maxOffset
	if lo <= 0 && offset > 0 {
		lo = 1
	}
	if lo+size >= track && offset < maxOffset {
		lo = track - size - 1
	}
	return max(lo, 0), size
}

// attachVScroll hangs the vertical scrollbar off the right edge of a pane's
// rows, which have already been drawn a column narrower to make room. The
// track is as many cells as there are rows, so where the thumb sits in it is
// where the view sits in the whole.
func attachVScroll(st *styles, rows []string, track string, total, offset, maxOffset int) {
	lo, size := thumbSpan(len(rows), total, len(rows), offset, maxOffset)
	for i := range rows {
		rows[i] += scrollCell(st, i >= lo && i < lo+size, track, thumbGlyph)
	}
}

// scrolls reports whether a pane of rows rows holding total entries has
// anything to scroll, and so whether it wants a bar at all: one that could
// never move says nothing and costs a column.
func scrolls(total, rows, width int) bool {
	return total > rows && rows > 0 && width > 1
}

// listMaxOffset is the first row that can sit at the top of a pane with the
// last row still on screen.
func listMaxOffset(total, rows int) int {
	return max(total-rows, 0)
}
