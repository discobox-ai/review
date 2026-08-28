package ui

import (
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/x/ansi"
)

// letterKey is one letter pressed, the way a command reaches a pane.
func letterKey(s string) tea.KeyMsg {
	return tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(s)}
}

// z gives the focused pane the whole window: nothing else is drawn, and the
// pane is as wide as the terminal.
func TestZoomGivesThePaneTheWholeWindow(t *testing.T) {
	m := drive(t, newTestModel(t), altKey("3"))
	if m.focus != focusDiff {
		t.Fatalf("focus = %v, want the diff", m.focus)
	}
	narrow := m.diff.width

	m = drive(t, m, letterKey("z"))
	if !m.zoom {
		t.Fatal("z did not zoom")
	}
	if m.diff.width != m.width {
		t.Errorf("zoomed diff is %d columns, want the full %d", m.diff.width, m.width)
	}
	if m.diff.width <= narrow {
		t.Errorf("zoomed diff (%d) is no wider than it was (%d)", m.diff.width, narrow)
	}

	view := m.View()
	if strings.Contains(view, "Unstaged Changes") {
		t.Error("the left column is still drawn while zoomed")
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
	if m.diff.width != narrow {
		t.Errorf("unzoomed diff is %d columns, want %d back", m.diff.width, narrow)
	}
	if !strings.Contains(m.View(), "Unstaged Changes") {
		t.Error("the left column did not come back")
	}
}

// Only the zoomed pane can be clicked, because it is the only one drawn.
func TestZoomLeavesOnlyOnePaneClickable(t *testing.T) {
	m := drive(t, newTestModel(t), altKey("3"), letterKey("z"))
	for f := focusArea(0); f < focusCount; f++ {
		if f == focusDiff {
			continue
		}
		if m.rects[f].w != 0 {
			t.Errorf("%v still has a rectangle while the diff is zoomed", f)
		}
	}
	if m.rects[focusDiff].w != m.width {
		t.Errorf("zoomed diff rect is %d wide, want %d", m.rects[focusDiff].w, m.width)
	}
}

// Zooming a pane is also going to it: with one pane on screen there is nowhere
// else for the keyboard to be.
func TestZoomingAnotherPaneMovesTheFocus(t *testing.T) {
	m := drive(t, newTestModel(t), altKey("1"), letterKey("z"))
	if !m.zoom || m.focus != focusWorkdir {
		t.Fatalf("zoom = %v, focus = %v, want the unstaged list zoomed", m.zoom, m.focus)
	}
	// Alt-3 while zoomed hands the window to the diff rather than showing a
	// pane the keyboard has left.
	m = drive(t, m, altKey("3"))
	if m.focus != focusDiff {
		t.Fatalf("focus = %v, want the diff", m.focus)
	}
	if !m.zoom || m.rects[focusDiff].w != m.width {
		t.Error("the zoom did not follow the focus")
	}
}

// Alt-Z reaches zoom from inside the message buffer, where z is text.
func TestAltZZoomsTheMessageBuffer(t *testing.T) {
	m := drive(t, newTestModel(t), tea.KeyMsg{Type: tea.KeyCtrlJ})
	if m.focus != focusCommit || !m.composeOpen {
		t.Fatalf("focus = %v, composeOpen = %v, want the message buffer", m.focus, m.composeOpen)
	}

	m = drive(t, m, altKey("z"))
	if !m.zoom {
		t.Fatal("Alt-Z did not zoom the message buffer")
	}
	if strings.Contains(m.View(), "Unstaged Changes") {
		t.Error("the left column is still drawn while the message buffer is zoomed")
	}

	// And z itself is still text in there.
	m = drive(t, m, letterKey("z"))
	if !strings.Contains(m.commit.Value(), "z") {
		t.Errorf("z was taken as a command in the message buffer; value = %q", m.commit.Value())
	}
}

// Double clicking a pane's title zooms it, the gesture a tiling window manager
// taught everyone.
func TestDoubleClickOnATitleZooms(t *testing.T) {
	m := newTestModel(t)
	r := m.rects[focusWorkdir]

	m = drive(t, m, pressAt(r.x+2, r.y))
	if m.zoom {
		t.Fatal("one click on the title zoomed")
	}
	if m.focus != focusWorkdir {
		t.Fatalf("focus = %v, want the title's own pane", m.focus)
	}

	m = drive(t, m, pressAt(r.x+2, r.y))
	if !m.zoom {
		t.Fatal("the double click did not zoom")
	}
	if m.workdir.width != m.width {
		t.Errorf("zoomed list is %d columns, want %d", m.workdir.width, m.width)
	}
}

// A pane that is not on screen cannot be zoomed onto it.
func TestZoomRefusesAPaneThatIsNotShown(t *testing.T) {
	m := newTestModel(t)
	if m.composeOpen {
		t.Skip("the message buffer is already open")
	}
	mdl, _ := m.toggleZoom(focusCommit)
	if got := mdl.(Model); got.zoom {
		t.Error("zoomed a pane that is not drawn")
	}
}
