package ui_test

import (
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/discobox-ai/review/source"
	"github.com/discobox-ai/review/ui"
)

// These are written from outside the package, the way a program embedding the
// window would: nothing in them can reach past the exported API.

// prSource is a pull request as a source sees one: a fixed diff between two
// commits, the commits themselves, and a file's text at either end. No working
// tree and nowhere to keep a review, so it is neither a Worktree nor a Store.
type prSource struct {
	incomplete string
}

const prDiff = `diff --git a/hello.go b/hello.go
index 1111111..2222222 100644
--- a/hello.go
+++ b/hello.go
@@ -1,3 +1,3 @@
 package hello

-func Hello() string { return "hi" }
+func Hello() string { return "hello" }
`

const prHead = "package hello\n\nfunc Hello() string { return \"hello\" }\n"

func (s prSource) Scan(base string, context int) (source.Scan, error) {
	return source.Scan{
		Branch:  "pr/42",
		HasHEAD: true,
		Base:    source.Base{Ref: "main", SHA: "1111111aaaaaaa", Commit: "1111111"},
		Commits: []source.Commit{{
			Hash: "2222222bbbbbbb", Short: "2222222",
			Subject: "Say hello properly", Author: "someone", When: "an hour ago",
		}},
		Diff:       prDiff,
		Incomplete: s.incomplete,
	}, nil
}

func (prSource) CommitDiff(hash string, context int) (string, error) { return prDiff, nil }

func (prSource) FileContent(commit, path string) (string, error) {
	if path != "hello.go" {
		return "", errors.New("no such file")
	}
	return prHead, nil
}

// host is somebody else's program with the window in it: it lays the window
// out under a line of its own, keeps a leader key, and takes the window away
// when it is closed.
type host struct {
	review ui.Model
	closed bool
	leader int
	asked  bool
	saw    string
}

func (h host) Init() tea.Cmd { return h.review.Init() }

func (h host) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case ui.ClosedMsg:
		h.closed = true
		return h, tea.Quit
	case tea.WindowSizeMsg:
		h.review = h.review.SetSize(msg.Width, msg.Height-1).SetOrigin(0, 1)
		return h, nil
	case tea.KeyPressMsg:
		if msg.String() == "ctrl+a" && !h.review.Typing() {
			h.leader++
			return h, nil
		}
	}
	next, cmd := h.review.Update(msg)
	h.review = next.(ui.Model)

	// Once the change is on screen, the person at it presses the leader key
	// and then closes the window.
	if view := h.review.View().Content; !h.asked && strings.Contains(view, "hello.go") {
		h.asked, h.saw = true, view
		return h, tea.Batch(cmd,
			send(tea.KeyPressMsg{Code: 'a', Mod: tea.ModCtrl}),
			send(tea.KeyPressMsg{Code: 'q', Text: "q"}))
	}
	return h, cmd
}

func (h host) View() tea.View {
	return tea.NewView("the console\n" + h.review.View().Content)
}

func send(msg tea.Msg) tea.Cmd { return func() tea.Msg { return msg } }

// The window runs inside another program from a source that is not Git at
// all, and closing it hands control back rather than ending that program.
func TestEmbeddedInAnotherProgram(t *testing.T) {
	m := ui.New(prSource{}, ui.Options{
		Embedded: true, Author: "reader", Reserved: []string{"ctrl+a"},
	})
	p := tea.NewProgram(host{review: m},
		tea.WithInput(nil), tea.WithOutput(io.Discard), tea.WithWindowSize(120, 30))

	done := make(chan tea.Model, 1)
	go func() {
		final, err := p.Run()
		if err != nil {
			t.Error(err)
		}
		done <- final
	}()
	var final tea.Model
	select {
	case final = <-done:
	case <-time.After(10 * time.Second):
		p.Kill()
		t.Fatal("the program never ended: the window did not hand back on q")
	}

	h := final.(host)
	if !h.closed {
		t.Fatal("the program ended without being told the window closed")
	}
	if h.leader != 1 {
		t.Errorf("the host's leader key reached the host %d times, want 1", h.leader)
	}
	for _, want := range []string{"pr/42", "hello.go", "from main"} {
		if !strings.Contains(h.saw, want) {
			t.Errorf("the window does not show %q:\n%s", want, h.saw)
		}
	}
}

// opened is the window over a source, sized and scanned, without a program
// around it: Init's commands are run once each, and what they answer handed
// back in, the way a program would.
func opened(t *testing.T, src source.Source, opts ui.Options) ui.Model {
	t.Helper()
	m := ui.New(src, opts).SetSize(120, 30)
	for _, msg := range answers(m.Init()) {
		next, _ := m.Update(msg)
		m = next.(ui.Model)
	}
	if !strings.Contains(m.View().Content, "hello.go") {
		t.Fatalf("the scan never arrived:\n%s", m.View().Content)
	}
	return m
}

// answers runs a command, and every command a batch of it holds, and returns
// what they sent.
func answers(cmd tea.Cmd) []tea.Msg {
	if cmd == nil {
		return nil
	}
	msg := cmd()
	if batch, ok := msg.(tea.BatchMsg); ok {
		var out []tea.Msg
		for _, c := range batch {
			out = append(out, answers(c)...)
		}
		return out
	}
	return []tea.Msg{msg}
}

func press(m ui.Model, keys ...tea.KeyPressMsg) ui.Model {
	for _, k := range keys {
		next, _ := m.Update(k)
		m = next.(ui.Model)
	}
	return m
}

func letter(s string) tea.KeyPressMsg { return tea.KeyPressMsg{Code: []rune(s)[0], Text: s} }

// With no working tree there is no branch to check out, no editor to open and
// no base to choose; with no store there is nothing to approve and nowhere to
// comment. None of it is offered — not in the help, not in the hints, and not
// by the keys.
func TestASourceWithoutAWorkingTreeOffersNoneOfIt(t *testing.T) {
	m := opened(t, prSource{}, ui.Options{Author: "reader"})

	help := press(m.SetSize(120, 200), letter("?")).View().Content
	for _, gone := range []string{
		"check out another branch", "edit the file", "choose the base",
		"comment on this line", "approve every file", "reply to this comment",
		"resolve / reopen", "click the branch name", ".git/discobox-review.json",
		"An approval is of a change", "What is not committed yet",
	} {
		if strings.Contains(help, gone) {
			t.Errorf("the help still offers %q", gone)
		}
	}
	for _, kept := range []string{"read the whole file", "fold every file", "next / previous hunk", "The mouse"} {
		if !strings.Contains(help, kept) {
			t.Errorf("the help has lost %q, which needs nothing the source lacks", kept)
		}
	}

	// Down into the diff, where the hints are about the line.
	m = press(m, letter(" "), letter("j"), letter("j"))
	if bar := lastLine(m.View().Content); strings.Contains(bar, "c comment") || strings.Contains(bar, "a approve") {
		t.Errorf("the status bar offers what the source cannot do: %q", bar)
	}

	// Each from a fresh window: a copy of the model shares its panes with
	// the one it was copied from, so the window before has to be read before.
	for _, k := range []string{"c", "a", "A", "K", "e", "b", "t", "f"} {
		m := press(opened(t, prSource{}, ui.Options{Author: "reader"}), letter(" "), letter("j"), letter("j"))
		before := m.View().Content
		if after := press(m, letter(k)).View().Content; after != before {
			t.Errorf("%s did something on a source without the means to:\n%s", k, after)
		}
	}
}

// With no review there is nothing approved or open to count, and neither the
// review's title nor a file's heading counts it.
func TestAReadingCountsNoApprovals(t *testing.T) {
	m := opened(t, prSource{}, ui.Options{Author: "reader"})
	if view := m.View().Content; strings.Contains(view, "approved") {
		t.Errorf("a review that cannot be approved counts approvals:\n%s", view)
	}
	// Nor does a file's heading carry a verdict that can never change.
	for _, l := range strings.Split(ansi.Strip(m.View().Content), "\n") {
		if strings.Contains(l, "hello.go") && strings.ContainsAny(l, "○◆✓◈") {
			t.Errorf("a heading carries a verdict nobody can give: %q", l)
		}
	}
}

// Embedded, closing the window is not quitting anything, and the bar says so.
func TestEmbeddedClosingIsCalledClosing(t *testing.T) {
	m := opened(t, prSource{}, ui.Options{Author: "reader", Embedded: true})
	top, _, _ := strings.Cut(m.View().Content, "\n")
	if !strings.Contains(top, "Ctrl-Q close") || strings.Contains(top, "quit") {
		t.Errorf("the top bar of an embedded window: %q", top)
	}
}

// A source that was handed only part of its diff says so, and the window says
// so where it stays on screen.
func TestAnIncompleteDiffSaysSo(t *testing.T) {
	m := opened(t, prSource{incomplete: "GitHub sent 300 of 412 files"}, ui.Options{Author: "reader"})
	top, _, _ := strings.Cut(m.View().Content, "\n")
	if !strings.Contains(top, "incomplete: GitHub sent 300 of 412 files") {
		t.Errorf("the top bar does not say the diff is incomplete: %q", top)
	}
}

// A theme handed in is the one drawn with, and a colour left out of it is
// the default's.
func TestATheme(t *testing.T) {
	def := ui.DefaultTheme()
	if def.Accent == nil || def.OK == nil {
		t.Fatal("the default theme leaves colours out")
	}
	m := opened(t, prSource{}, ui.Options{Author: "reader", Theme: ui.Theme{Accent: def.OK}})
	if m.View().Content == opened(t, prSource{}, ui.Options{Author: "reader"}).View().Content {
		t.Error("a different accent drew the same window")
	}
}

func lastLine(s string) string {
	s = strings.TrimRight(s, "\n")
	return s[strings.LastIndex(s, "\n")+1:]
}

// A key the host reserves is the host's: the window does not answer it, and
// neither its help nor its hints offer it.
func TestAReservedKeyIsLeftAlone(t *testing.T) {
	reserved := func() ui.Model {
		m := opened(t, prSource{}, ui.Options{Author: "reader", Reserved: []string{"s"}})
		return press(m, letter(" "), letter("j"), letter("j"))
	}
	m := reserved()
	before := m.View().Content
	if bar := lastLine(before); strings.Contains(bar, "s split") || strings.Contains(bar, "s unify") {
		t.Errorf("the status bar offers the host's key: %q", bar)
	}
	if after := press(m, letter("s")).View().Content; after != before {
		t.Errorf("s, which the host reserved, changed the window:\n%s", after)
	}
	help := press(reserved().SetSize(120, 200), letter("?")).View().Content
	if strings.Contains(help, "unify the diff") {
		t.Error("the help offers the host's key")
	}

	// Without the reservation, the same key does split the diff, so the
	// test above is about the reservation and not about s.
	free := press(opened(t, prSource{}, ui.Options{Author: "reader"}), letter(" "), letter("j"), letter("j"))
	if was := free.View().Content; press(free, letter("s")).View().Content == was {
		t.Fatal("s does nothing even when it is not reserved")
	}
}

// A window drawn below the top of the terminal reads the mouse from where it
// is drawn: a click on a line lands on that line, and a click above it, on the
// host's own line, is not the window's.
func TestTheMouseIsReadFromWhereTheWindowIs(t *testing.T) {
	// A fresh window for every click: a copy of the model shares its panes
	// with the one it was copied from, so a click on one moves both.
	fresh := func() ui.Model {
		return press(opened(t, prSource{}, ui.Options{Author: "reader"}), letter(" "))
	}
	before := fresh().View().Content
	row := -1
	for i, l := range strings.Split(before, "\n") {
		if strings.Contains(ansi.Strip(l), `return "hello"`) {
			row = i
			break
		}
	}
	if row < 0 {
		t.Fatal("no line to click")
	}
	click := func(m ui.Model, y int) string {
		next, _ := m.Update(tea.MouseClickMsg{X: 10, Y: y, Button: tea.MouseLeft})
		next, _ = next.(ui.Model).Update(tea.MouseReleaseMsg{X: 10, Y: y, Button: tea.MouseLeft})
		return next.(ui.Model).View().Content
	}

	there := click(fresh(), row)
	if there == before {
		t.Fatal("clicking the line did nothing, so the test proves nothing")
	}
	if got := click(fresh().SetOrigin(0, 1), row+1); got != there {
		t.Errorf("a click on the line, one row down the terminal, did not land on it:\n%s", got)
	}
	if got := click(fresh().SetOrigin(0, 1), 0); got != before {
		t.Errorf("a click on the host's line reached the window:\n%s", got)
	}
}

// A drag that leaves the window still ends: let go over the host's line, and
// the window is let go of all the same — the next click in it is a click, not
// more of the drag.
func TestADragLetGoOutsideTheWindowEnds(t *testing.T) {
	fresh := func() ui.Model {
		return press(opened(t, prSource{}, ui.Options{Author: "reader"}), letter(" ")).SetOrigin(0, 1)
	}
	update := func(m ui.Model, msgs ...tea.Msg) ui.Model {
		for _, msg := range msgs {
			next, _ := m.Update(msg)
			m = next.(ui.Model)
		}
		return m
	}
	click := func(y int) []tea.Msg {
		return []tea.Msg{
			tea.MouseClickMsg{X: 10, Y: y, Button: tea.MouseLeft},
			tea.MouseReleaseMsg{X: 10, Y: y, Button: tea.MouseLeft},
		}
	}

	// A plain click on row 8 of the terminal, for comparison.
	want := update(fresh(), click(8)...).View().Content

	// The same click, after a drag from row 6 up and off the top that was
	// let go of over the host's line.
	m := update(fresh(),
		tea.MouseClickMsg{X: 10, Y: 6, Button: tea.MouseLeft},
		tea.MouseMotionMsg{X: 10, Y: 0, Button: tea.MouseLeft},
		tea.MouseReleaseMsg{X: 10, Y: 0, Button: tea.MouseLeft},
	)
	// Letting go is what copies a selection, so the copy is the proof that
	// the release arrived.
	if bar := lastLine(m.View().Content); !strings.Contains(bar, "Copied") {
		t.Errorf("letting go over the host's line did not end the drag: %q", bar)
	}
	// The status bar says what the last thing did, so the window above it is
	// what is compared.
	got := update(m, click(8)...).View().Content
	if above(got) != above(want) {
		t.Errorf("the drag let go of outside the window was still going:\n%s\n---want---\n%s", got, want)
	}
}

// above is a frame less its status bar.
func above(s string) string {
	s = strings.TrimRight(s, "\n")
	return s[:strings.LastIndex(s, "\n")]
}

// countingSource is a pull request that counts how often it is read.
type countingSource struct {
	prSource
	scans *int
}

func (s countingSource) Scan(base string, context int) (source.Scan, error) {
	*s.scans++
	return s.prSource.Scan(base, context)
}

// A host that says the source moved while a scan is out — the window's first,
// as likely as not, since a pull request's head can move while the page is
// still opening — gets a scan once that one is in. Not two at once, whose
// answers could come back in either order, and not one for every time it
// said so.
func TestRescansAskedForWhileOneIsOutGoOnceItIsIn(t *testing.T) {
	scans := 0
	m := ui.New(countingSource{scans: &scans}, ui.Options{Author: "reader"}).SetSize(120, 30)
	first := m.Init()

	for range 3 {
		next, cmd := m.Update(ui.RescanMsg{})
		m = next.(ui.Model)
		if cmd != nil {
			t.Fatal("a scan went out while the first was still out")
		}
	}
	var after []tea.Cmd
	for _, msg := range answers(first) {
		next, cmd := m.Update(msg)
		m = next.(ui.Model)
		after = append(after, cmd)
	}
	if scans != 1 {
		t.Fatalf("%d scans before the first came in, want 1", scans)
	}
	answers(tea.Batch(after...))
	if scans != 2 {
		t.Errorf("%d scans in all, want 2", scans)
	}
}

// The reader a file opens in has its own hint line, and it offers no more
// than the window does.
func TestTheFileReaderOffersNoEditor(t *testing.T) {
	m := opened(t, prSource{}, ui.Options{Author: "reader"})
	m = press(m, letter(" "), letter("j"), letter("j"))
	next, cmd := m.Update(letter("v"))
	m = next.(ui.Model)
	if cmd == nil {
		t.Fatal("v asked for nothing")
	}
	next, _ = m.Update(cmd())
	reader := next.(ui.Model).View().Content
	if !strings.Contains(reader, "Hello") {
		t.Fatalf("the file never opened:\n%s", reader)
	}
	if bar := lastLine(reader); strings.Contains(bar, "e edit") || strings.Contains(bar, "on disk") {
		t.Errorf("the reader offers what the source has not got: %q", bar)
	}
}
