package ui

import (
	"encoding/base64"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/discobox-ai/x/selection"
)

// Turning mouse reporting on takes the mouse away from the terminal, and with
// it the click-drag-to-select every user already has. Nothing here is worth
// that trade on its own, so the window gives the gesture back: dragging selects
// text, double click takes a word, triple click a line, Alt drags a rectangle,
// and letting go copies. That is [selection]'s job; this file is the wiring —
// which pane is under the pointer, what a row means there, and where a copy
// goes.
//
// Shift is the escape hatch. Most terminals bypass mouse reporting while it is
// held, so a user who wants their own terminal's selection — to pick up
// something spanning two panes, say — holds Shift and drags, exactly as they
// would over tmux or vim.

// wheelLines is how far one wheel tick scrolls, xterm's and tmux's convention.
const wheelLines = 3

// hWheelStep is how far a sideways wheel tick slides the diff: one tab stop,
// half of what a press of left or right does. A horizontal wheel is usually a
// trackpad under someone's thumb, sending events far faster than a key repeat,
// so the same step would overshoot.
const hWheelStep = tabWidth

// doubleClick is how close together two presses on the same thing must be to
// count as one gesture. It matches [selection]'s own window, which is the
// conventional desktop default; the two are separate only because a title bar
// is not a cell grid and does its own counting.
const doubleClick = 400 * time.Millisecond

// rect is where something was last drawn, in screen cells.
type rect struct{ x, y, w, h int }

func (r rect) contains(x, y int) bool {
	return r.w > 0 && r.h > 0 && x >= r.x && x < r.x+r.w && y >= r.y && y < r.y+r.h
}

// focusReader names the markdown reader in the mouse state. The reader is not
// a pane of the main window and never takes the keyboard focus, so it sits
// outside the focusArea range rather than extending it.
const focusReader focusArea = -1

// mouseState is the text selection the mouse makes.
//
// Only one selection is on screen at a time — a press replaces whatever there
// was, which is how selection has worked since the first one — but each pane
// keeps its own [selection.Model], because the click counter that tells a
// double click from two clicks is part of that model. Sharing one across panes
// would let a press in the diff be counted against the press that came before
// it in a file list, and turn an ordinary click into a word select.
type mouseState struct {
	sels  map[focusArea]*selection.Model
	grids map[focusArea]*paneGrid

	// pane is the pane whose selection is the one on screen, and live whether
	// there is one at all.
	pane focusArea
	live bool

	// A title bar is chrome rather than content, so its double click — the
	// zoom gesture — is counted here rather than by a selection.
	titleAt   focusArea
	titleTime time.Time
}

func newMouseState() *mouseState {
	return &mouseState{
		sels:  map[focusArea]*selection.Model{},
		grids: map[focusArea]*paneGrid{},
	}
}

// bind returns the pane's selection, building it over a grid the first time
// and re-pointing that grid at the pane as it is now. The pane behind an area
// outlives any one selection made in it; the value describing it does not.
func (ms *mouseState) bind(f focusArea, p selectable) *selection.Model {
	g, ok := ms.grids[f]
	if !ok {
		g = newPaneGrid(p)
		ms.grids[f], ms.sels[f] = g, selection.New(g)
	}
	if g.pane != p {
		g.pane = p
		g.reset()
	}
	return ms.sels[f]
}

// active is the selection on screen, and the pane holding it.
func (ms *mouseState) active() (*selection.Model, focusArea, bool) {
	if ms == nil || !ms.live {
		return nil, 0, false
	}
	s := ms.sels[ms.pane]
	if s == nil || !s.Active() {
		return nil, 0, false
	}
	return s, ms.pane, true
}

// dragging is the selection mid-gesture, with the button still down.
func (ms *mouseState) dragging() (*selection.Model, focusArea, bool) {
	if ms == nil || !ms.live {
		return nil, 0, false
	}
	s := ms.sels[ms.pane]
	if s == nil || !s.Dragging() {
		return nil, 0, false
	}
	return s, ms.pane, true
}

func (ms *mouseState) hasSelection() bool {
	_, _, ok := ms.active()
	return ok
}

// selectionIn is the selection to paint over a pane, or nil for the panes —
// all of them, most of the time — holding none of it.
func (ms *mouseState) selectionIn(f focusArea) *selection.Model {
	s, at, ok := ms.active()
	if !ok || at != f {
		return nil
	}
	return s
}

// clear drops the selection and every decoded row cached under it. The two go
// together: a cached row is only sound for as long as the selection reading it
// still means what it meant, so whatever clears one clears the other.
func (ms *mouseState) clear() {
	if ms == nil {
		return
	}
	for f, s := range ms.sels {
		s.Clear()
		if g := ms.grids[f]; g != nil {
			g.reset()
		}
	}
	ms.live = false
}

// titleDouble reports whether a press on a pane's title is the second of a
// double click on that same title.
func (ms *mouseState) titleDouble(f focusArea) bool {
	now := time.Now()
	double := ms.titleAt == f && now.Sub(ms.titleTime) <= doubleClick
	ms.titleAt, ms.titleTime = f, now
	if double {
		// A third press starts counting again rather than zooming twice.
		ms.titleTime = time.Time{}
	}
	return double
}

// ---------------------------------------------------------------------------
// Hit testing
// ---------------------------------------------------------------------------

// hit resolves a screen position to the pane drawn under it, the rectangle it
// occupies, and the content the mouse can select in it. The content is nil for
// a pane that has none — the message buffer is a text field, and the terminal's
// own Shift-drag remains the way to copy out of it.
func (m *Model) hit(x, y int) (focusArea, rect, selectable, bool) {
	// The reader owns the whole screen while it is up, the way it owns the
	// keys.
	if m.md != nil {
		r := rect{0, 0, m.width, m.height}
		if !r.contains(x, y) {
			return 0, rect{}, nil, false
		}
		return focusReader, r, mdSel{m.md}, true
	}
	for f := focusArea(0); f < focusCount; f++ {
		if m.rects[f].contains(x, y) {
			return f, m.rects[f], m.selectableFor(f), true
		}
	}
	return 0, rect{}, nil, false
}

// rectOf is where an area is drawn. The reader is not in the pane array — it
// is not a pane — and takes the whole screen, so it is answered here rather
// than by an index that would be out of range.
func (m *Model) rectOf(f focusArea) rect {
	if f == focusReader {
		return rect{0, 0, m.width, m.height}
	}
	if f < 0 || f >= focusCount {
		return rect{}
	}
	return m.rects[f]
}

// selectableFor is a pane's content, or nil when it has none to select.
func (m *Model) selectableFor(f focusArea) selectable {
	switch f {
	case focusWorkdir:
		return listSel{m.workdir}
	case focusIndex:
		return listSel{m.index}
	case focusHistory:
		return listSel{m.history}
	case focusCommits:
		return commitsSel{m.commits}
	case focusDiff:
		return diffSel{m.diff, m.st}
	case focusReader:
		if m.md == nil {
			return nil
		}
		return mdSel{m.md}
	}
	return nil
}

// listFor is the file list an area names, or nil for an area that is not one.
func (m *Model) listFor(f focusArea) *fileList {
	switch f {
	case focusWorkdir:
		return m.workdir
	case focusIndex:
		return m.index
	case focusHistory:
		return m.history
	}
	return nil
}

// scrollPane moves a pane's viewport, leaving its cursor where it was.
func (m *Model) scrollPane(f focusArea, delta int) {
	switch f {
	case focusReader:
		if m.md != nil {
			m.md.move(delta)
		}
	case focusWorkdir, focusIndex, focusHistory:
		if l := m.listFor(f); l != nil {
			l.scroll(delta)
		}
	case focusCommits:
		m.commits.scroll(delta)
	case focusDiff:
		m.diff.scroll(delta)
	}
}

// ---------------------------------------------------------------------------
// Routing
// ---------------------------------------------------------------------------

func (m Model) onMouse(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	if m.mouse == nil {
		m.mouse = newMouseState()
	}
	// A dialog owns the screen while it is up, so nothing behind it takes the
	// mouse either.
	if m.dialog != nil {
		return m, nil
	}
	switch {
	case tea.MouseEvent(msg).IsWheel():
		return m.mouseWheel(msg)
	case msg.Action == tea.MouseActionMotion:
		return m.mouseDrag(msg)
	case msg.Action == tea.MouseActionRelease:
		return m.mouseRelease()
	case msg.Action != tea.MouseActionPress:
		return m, nil
	case msg.Button == tea.MouseButtonRight:
		// The Windows-terminal gesture: right clicking a showing selection
		// copies it. With nothing selected it does nothing, since the other
		// half of that gesture is paste and there is nothing here to paste
		// into.
		return m.copySelection()
	case msg.Button == tea.MouseButtonLeft:
		return m.mousePress(msg)
	}
	return m, nil
}

// mouseWheel scrolls whatever is under the pointer, without moving the focus
// to it. Reading ahead in a pane is not the same as going to work in it.
func (m Model) mouseWheel(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	f, _, _, ok := m.hit(msg.X, msg.Y)
	if !ok {
		return m, nil
	}
	switch msg.Button {
	case tea.MouseButtonWheelUp:
		m.scrollPane(f, -wheelLines)
	case tea.MouseButtonWheelDown:
		m.scrollPane(f, wheelLines)
	case tea.MouseButtonWheelLeft:
		m.scrollPaneH(f, -hWheelStep)
	case tea.MouseButtonWheelRight:
		m.scrollPaneH(f, hWheelStep)
	}
	return m, nil
}

// scrollPaneH slides a pane sideways. Only the diff has anywhere to go: it is
// the one pane that draws lines too long for it rather than wrapping them.
func (m *Model) scrollPaneH(f focusArea, delta int) {
	if f == focusDiff {
		m.diff.scrollH(delta)
	}
}

func (m Model) mousePress(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	f, r, p, ok := m.hit(msg.X, msg.Y)
	if !ok {
		m.mouse.clear()
		return m, nil
	}

	// The title bar is chrome: clicking it takes the focus, and clicking it
	// twice zooms the pane, the gesture every tiling window manager taught.
	if msg.Y == r.y && f != focusReader {
		m.mouse.clear()
		if m.mouse.titleDouble(f) {
			return m.toggleZoom(f)
		}
		return m.setFocus(f)
	}

	if p == nil {
		m.mouse.clear()
		if f == focusReader {
			return m, nil
		}
		return m.setFocus(f)
	}

	row := msg.Y - r.y - 1
	if row < 0 || row >= p.contentRows() {
		return m, nil
	}
	line := p.contentOffset() + row
	col := msg.X - r.x - p.contentX()

	if first, count := p.contentLines(); line >= first+count {
		// Empty space below the last row. Take the focus, but leave the cursor
		// where it is: clicking past the end of a list is not choosing the
		// last thing in it.
		m.mouse.clear()
		if f == focusReader {
			return m, nil
		}
		return m.setFocus(f)
	}

	// Clicking a row is how you get to it: the focus moves to the pane and the
	// cursor to the row, which is what a click does in git-gui too. It is also
	// where a drag starts, and the two do not fight — a selection is not a
	// selection until it has moved off the cell it began on, so a plain click
	// leaves nothing highlighted behind it.
	mdl, cmd := m.clickRow(f, line)
	m = mdl

	m.mouse.clear()
	sel := m.mouse.bind(f, p)
	m.mouse.pane, m.mouse.live = f, true
	sel.MouseDown(line, col, msg.Alt)
	return m, cmd
}

// clickRow puts the focus on a pane and its cursor on a row, and returns
// whatever that change of mind asks the rest of the window to reload.
func (m Model) clickRow(f focusArea, line int) (Model, tea.Cmd) {
	switch f {
	case focusReader:
		// The reader has no cursor; a click there only starts a selection.
		return m, nil
	case focusWorkdir, focusIndex, focusHistory:
		if l := m.listFor(f); l != nil {
			l.moveTo(line)
		}
	case focusCommits:
		m.commits.moveTo(line)
	case focusDiff:
		m.diff.moveTo(line)
	}

	mdl, cmd := m.setFocus(f)
	m = mdl.(Model)
	switch f {
	case focusHistory:
		// setFocus reloads the diff for the working lists but not for the
		// commit's files, which the keyboard path refreshes itself.
		cmd = tea.Batch(cmd, m.refreshDiffCmd())
	case focusCommits:
		// Moving this cursor is what changes which version of the branch the
		// whole left column is looking at.
		cmd = tea.Batch(cmd, m.onCommitSelected())
	}
	return m, cmd
}

// mouseDrag extends the gesture, scrolling the pane when the drag runs past
// its top or bottom edge so a selection can reach past what is on screen.
func (m Model) mouseDrag(msg tea.MouseMsg) (tea.Model, tea.Cmd) {
	sel, f, ok := m.mouse.dragging()
	if !ok {
		return m, nil
	}
	p := m.selectableFor(f)
	if p == nil {
		return m, nil
	}
	r := m.rectOf(f)
	row, rows := msg.Y-r.y-1, p.contentRows()
	switch {
	case row < 0:
		m.scrollPane(f, row)
		row = 0
	case row >= rows:
		m.scrollPane(f, row-rows+1)
		row = rows - 1
	}
	sel.MouseDrag(p.contentOffset()+row, msg.X-r.x-p.contentX())
	return m, nil
}

// mouseRelease ends the gesture. A gesture that selected something copies at
// the moment the button comes up, the way X11's primary selection does; the
// highlight stays until the next press.
func (m Model) mouseRelease() (tea.Model, tea.Cmd) {
	sel, _, ok := m.mouse.dragging()
	if !ok {
		return m, nil
	}
	text, copied := sel.MouseUp()
	if !copied {
		m.mouse.live = false
		return m, nil
	}
	m.setOK(copiedMessage(text))
	return m, copyToClipboard(text)
}

// copySelection copies the selection on screen and drops it.
func (m Model) copySelection() (tea.Model, tea.Cmd) {
	sel, _, ok := m.mouse.active()
	if !ok {
		return m, nil
	}
	text := sel.Text()
	m.mouse.clear()
	if text == "" {
		return m, nil
	}
	m.setOK(copiedMessage(text))
	return m, copyToClipboard(text)
}

func copiedMessage(text string) string {
	if n := strings.Count(text, "\n") + 1; n > 1 {
		return "Copied " + plural(n, "line", "lines")
	}
	return "Copied " + plural(len([]rune(text)), "character", "characters")
}

// ---------------------------------------------------------------------------
// The clipboard
// ---------------------------------------------------------------------------

// clipboard is where copies go. It is a variable so that tests can read them
// back; nothing in the program replaces it.
var clipboard io.Writer = os.Stdout

// clipboardMu serializes copies against each other.
var clipboardMu sync.Mutex

// copyToClipboard puts text on the clipboard with OSC 52.
//
// OSC 52 is the terminal's own copy sequence, and the only one that works from
// where this program sits. A copy travels out the same pipe the text on screen
// came down, so it lands on the clipboard of the machine the user is actually
// looking at — over ssh, inside a container, through tmux — rather than on
// whatever clipboard the host running git happens to have. It is also what
// Bubble Tea v2 does for tea.SetClipboard; v1 has no such command, so the
// sequence is written here.
//
// Writing it around the renderer's back is safe in a way that writing most
// escapes would not be: OSC 52 neither moves the cursor nor changes the pen,
// so a frame that lands either side of it is unaffected.
func copyToClipboard(text string) tea.Cmd {
	return func() tea.Msg {
		clipboardMu.Lock()
		defer clipboardMu.Unlock()
		fmt.Fprint(clipboard, osc52(text))
		return nil
	}
}

func osc52(text string) string {
	return "\x1b]52;c;" + base64.StdEncoding.EncodeToString([]byte(text)) + "\a"
}
