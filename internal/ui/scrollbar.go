package ui

// Dragging a scrollbar.
//
// A bar that only reports where you are is half a bar. These are drawn where
// every scrollbar is drawn and they are the width every scrollbar is, so a
// hand that has used one anywhere else arrives expecting to be able to take
// hold of the thumb and pull — and a pane that lets you look but not touch is
// the sort of thing that reads as broken rather than as minimal.

// barAxis is which of a pane's two bars a drag has hold of.
type barAxis int

const (
	barNone barAxis = iota
	barVert
	barHorz
)

// barDrag is a bar being pulled: which pane, which bar, where along the track
// it was taken hold of, and where the pane was at that moment.
//
// The drag is measured as a distance from those two, rather than by working
// out afresh each time where the pointer says the thumb now is. A track is
// shorter than the thing it is a map of, so the two directions of that sum do
// not round trip: re-deriving the offset would shift the pane by a row or two
// the instant the thumb was touched, before the hand had moved at all. Which
// is the single most irritating way to get a scrollbar wrong.
type barDrag struct {
	pane focusArea
	axis barAxis
	from int // where along the track the pointer took hold
	at   int // where the pane was scrolled to then
}

// bar describes one of a pane's scrollbars: where it is drawn and what it is a
// map of. ok is false for a pane that is not wearing that bar.
type bar struct {
	// pos is the screen row or column the bar itself occupies, and lo/length
	// the span of body rows or columns it runs along.
	pos    int
	lo     int
	length int

	// total is how much there is, window how much of it is on screen, and
	// offset where the window is: what [thumbSpan] needs.
	total  int
	window int
	offset int
	max    int
}

// vbar is a pane's vertical scrollbar, drawn down its right edge.
func (m *Model) vbar(f focusArea) (bar, bool) {
	r := m.rectOf(f)
	if r.w <= 1 || r.h <= 1 {
		return bar{}, false
	}
	b := bar{pos: r.x + r.w - 1, lo: r.y + 1}
	switch f {
	case focusFiles:
		if !m.files.scrolls() {
			return bar{}, false
		}
		b.length, b.total = m.files.height, len(m.files.entries)
		b.offset, b.max = m.files.offset, m.files.maxOffset()
	case focusHistory:
		if !m.history.scrolls() {
			return bar{}, false
		}
		b.length, b.total = m.history.height, m.history.rows()
		b.offset, b.max = m.history.offset, m.history.maxOffset()
	case focusReview:
		if vert, _ := m.view.bars(); !vert {
			return bar{}, false
		}
		b.length, b.total = m.view.bodyRows(), len(m.view.rows)
		b.offset, b.max = m.view.offset, m.view.maxOffset()
	default:
		return bar{}, false
	}
	// The track is as many cells as there are rows, which is what
	// attachVScroll draws it against.
	b.window = b.length
	if b.length <= 0 || b.max <= 0 {
		return bar{}, false
	}
	return b, true
}

// hbar is the review's horizontal scrollbar, drawn along its foot. It is the
// one pane that draws lines too long for it rather than wrapping them, so it
// is the only one with a second bar.
func (m *Model) hbar(f focusArea) (bar, bool) {
	if f != focusReview {
		return bar{}, false
	}
	vert, horz := m.view.bars()
	if !horz {
		return bar{}, false
	}
	r := m.rectOf(f)
	if r.w <= 1 || r.h <= 1 {
		return bar{}, false
	}
	b := bar{
		pos:    r.y + r.h - 1,
		lo:     r.x,
		length: m.view.rowWidth(vert),
		total:  m.view.long,
		window: m.view.codeWidthWith(vert),
		offset: m.view.hoffset,
		max:    m.view.maxHOffset(),
	}
	if b.length <= 0 || b.max <= 0 {
		return bar{}, false
	}
	return b, true
}

// at reports whether a screen position lands on the bar, and how far along its
// track that is.
func (b bar) at(x, y int, axis barAxis) (int, bool) {
	along, across := y, x
	if axis == barHorz {
		along, across = x, y
	}
	if across != b.pos || along < b.lo || along >= b.lo+b.length {
		return 0, false
	}
	return along - b.lo, true
}

// onThumb reports whether a point along the track is on the thumb itself
// rather than on the empty track either side of it.
func (b bar) onThumb(along int) bool {
	lo, size := thumbSpan(b.length, b.total, b.window, b.offset, b.max)
	return along >= lo && along < lo+size
}

// travel is how far the thumb can move along the track.
func (b bar) travel() int {
	_, size := thumbSpan(b.length, b.total, b.window, b.offset, b.max)
	return b.length - size
}

// centreOn is the offset that puts the middle of the thumb where the pointer
// is, which is what a press on the bare track means: take me roughly there.
func (b bar) centreOn(along int) int {
	_, size := thumbSpan(b.length, b.total, b.window, b.offset, b.max)
	travel := b.length - size
	if travel <= 0 {
		return b.offset
	}
	want := min(max(along-size/2, 0), travel)
	// Rounded rather than truncated, so the foot of the track really does
	// reach the last row.
	return min(max((want*b.max+travel/2)/travel, 0), b.max)
}

// scrollBy is where the pane lands once the pointer has moved from `from` to
// `along`, having taken hold while the pane was at `at`.
func (b bar) scrollBy(from, at, along int) int {
	travel := b.travel()
	if travel <= 0 {
		return at
	}
	return min(max(at+(along-from)*b.max/travel, 0), b.max)
}

// barAt finds the bar under a screen position, if any.
func (m *Model) barAt(x, y int) (focusArea, barAxis, bar, bool) {
	for f := focusArea(0); f < focusCount; f++ {
		if m.rects[f].w == 0 {
			continue
		}
		if b, ok := m.hbar(f); ok {
			if _, on := b.at(x, y, barHorz); on {
				return f, barHorz, b, true
			}
		}
		if b, ok := m.vbar(f); ok {
			if _, on := b.at(x, y, barVert); on {
				return f, barVert, b, true
			}
		}
	}
	return 0, barNone, bar{}, false
}

// scrollTo puts a pane's viewport at an offset, without moving its cursor —
// dragging a bar is reading ahead, the same gesture the wheel makes.
func (m *Model) scrollTo(f focusArea, axis barAxis, offset int) {
	if axis == barHorz {
		if f == focusReview {
			m.view.hoffset = min(max(offset, 0), m.view.maxHOffset())
		}
		return
	}
	switch f {
	case focusFiles:
		m.files.offset = min(max(offset, 0), m.files.maxOffset())
	case focusHistory:
		m.history.offset = min(max(offset, 0), m.history.maxOffset())
	case focusReview:
		m.view.offset = min(max(offset, 0), m.view.maxOffset())
	}
}

// alongOf is how far a screen position is along a bar's track.
func alongOf(b bar, axis barAxis, x, y int) int {
	if axis == barHorz {
		return x - b.lo
	}
	return y - b.lo
}

// grabBar takes hold of a bar under a press. A press on the bare track jumps
// the pane to roughly where it landed; a press on the thumb moves nothing
// until the hand does.
func (m *Model) grabBar(x, y int) bool {
	f, axis, b, ok := m.barAt(x, y)
	if !ok {
		return false
	}
	along := alongOf(b, axis, x, y)
	at := b.offset
	if !b.onThumb(along) {
		at = b.centreOn(along)
		m.scrollTo(f, axis, at)
	}
	m.bar = &barDrag{pane: f, axis: axis, from: along, at: at}
	return true
}

// dragBar follows the pointer while a bar is held.
func (m *Model) dragBar(x, y int) bool {
	if m.bar == nil {
		return false
	}
	f, axis := m.bar.pane, m.bar.axis
	var b bar
	var ok bool
	if axis == barHorz {
		b, ok = m.hbar(f)
	} else {
		b, ok = m.vbar(f)
	}
	if !ok {
		m.bar = nil
		return false
	}
	// The pointer is free to leave the bar while it is held — every scrollbar
	// works that way, and letting go of the thumb because the hand wandered a
	// column sideways would make a long drag impossible.
	m.scrollTo(f, axis, b.scrollBy(m.bar.from, m.bar.at, alongOf(b, axis, x, y)))
	return true
}
