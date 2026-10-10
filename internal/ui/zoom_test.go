package ui

import (
	"strings"
	"testing"

	"github.com/charmbracelet/x/ansi"
)

// z gives the focused pane the whole window: nothing else is drawn, and the
// pane is as tall as the terminal has room for.
func TestZoomGivesThePaneTheWholeWindow(t *testing.T) {
	m := drive(t, newTestModel(t), altKey("1"))
	if m.focus != focusReview {
		t.Fatalf("focus = %v, want the review", m.focus)
	}
	short := m.view.height

	m = drive(t, m, letterKey("z"))
	if !m.zoom {
		t.Fatal("z did not zoom")
	}
	if m.view.width != m.width {
		t.Errorf("zoomed review is %d columns, want the full %d", m.view.width, m.width)
	}
	if m.view.height <= short {
		t.Errorf("zoomed review (%d rows) is no taller than it was (%d)", m.view.height, short)
	}

	view := m.frame()
	if strings.Contains(ansi.Strip(view), "History") {
		t.Error("the history is still drawn while zoomed")
	}
	if lines := strings.Split(view, "\n"); len(lines) != m.height {
		t.Errorf("zoomed frame has %d rows, want %d", len(lines), m.height)
	}
	for i, line := range strings.Split(view, "\n") {
		if w := ansi.StringWidth(line); w > m.width {
			t.Fatalf("zoomed row %d is %d columns, wider than the window (%d)", i, w, m.width)
		}
	}

	// And back again.
	m = drive(t, m, letterKey("z"))
	if m.zoom {
		t.Fatal("z did not unzoom")
	}
	if m.view.height != short {
		t.Errorf("unzoomed review is %d rows, want %d back", m.view.height, short)
	}
	if !strings.Contains(ansi.Strip(m.frame()), "History") {
		t.Error("the history did not come back")
	}
}

// With one pane on screen there is nowhere else for the mouse to land either.
func TestZoomLeavesOnlyOnePaneClickable(t *testing.T) {
	m := drive(t, newTestModel(t), altKey("2"), letterKey("z"))
	for f := focusArea(0); f < focusCount; f++ {
		if f == m.focus {
			if m.rects[f].w == 0 {
				t.Errorf("the zoomed pane %v has no rectangle", f)
			}
			continue
		}
		if m.rects[f].w != 0 {
			t.Errorf("%v is still clickable while %v is zoomed", f, m.focus)
		}
	}
}

// Zooming a pane is also going to it: a focus sitting on something invisible
// is how a window ends up ignoring keys for no reason anyone can see. So the
// zoom follows the focus, and z is always the way back out.
func TestTheZoomFollowsTheFocus(t *testing.T) {
	m := drive(t, newTestModel(t), altKey("2"), letterKey("z"))
	if !m.zoom || m.focus != focusHistory {
		t.Fatalf("zoom=%v focus=%v", m.zoom, m.focus)
	}
	m = drive(t, m, altKey("1"))
	if !m.zoom {
		t.Error("moving the focus unzoomed")
	}
	if m.focus != focusReview || m.view.width != m.width {
		t.Errorf("focus=%v review width=%d, want the review with the whole window",
			m.focus, m.view.width)
	}
	m = drive(t, m, letterKey("z"))
	if m.zoom {
		t.Error("z did not hand the window back")
	}
}

// Alt-Z reaches zoom from inside the composer, where z is text.
func TestAltZZoomsTheComposer(t *testing.T) {
	m := newTestModel(t)
	m = onLine(t, m, "foo.txt", '+', "FOUR")
	m = drive(t, m, letterKey("c"))
	if m.compose == nil || m.focus != focusCompose {
		t.Fatalf("compose=%v focus=%v", m.compose, m.focus)
	}
	m = drive(t, m, letterKey("z"))
	if m.zoom {
		t.Error("z zoomed from inside the composer, where it is text")
	}
	if !strings.Contains(m.editor.Value(), "z") {
		t.Errorf("z was not typed into the field: %q", m.editor.Value())
	}
	m = drive(t, m, altKey("z"))
	if !m.zoom || m.focus != focusCompose {
		t.Errorf("Alt-Z: zoom=%v focus=%v", m.zoom, m.focus)
	}
}

// Double clicking a pane's title zooms it, the gesture every tiling window
// manager taught.
func TestDoubleClickOnATitleZooms(t *testing.T) {
	m := newTestModel(t)
	r := m.rects[focusHistory]
	m = drive(t, m, pressAt(r.x+8, r.y), pressAt(r.x+8, r.y)) // past the fold button
	if !m.zoom || m.focus != focusHistory {
		t.Errorf("zoom=%v focus=%v after a double click on the title", m.zoom, m.focus)
	}
}

// A pane that is not on screen cannot be zoomed to.
func TestZoomRefusesAPaneThatIsNotShown(t *testing.T) {
	m := newTestModel(t)
	if m.compose != nil {
		t.Fatal("the composer is open before anything asked for it")
	}
	mdl, _ := m.toggleZoom(focusCompose)
	if mdl.(Model).zoom {
		t.Error("a pane that is not drawn was zoomed")
	}
}
