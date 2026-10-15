package ui

import (
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/discobox-ai/review/internal/git"
	"github.com/discobox-ai/review/internal/review"
	"github.com/mattn/go-runewidth"
)

func TestPrintable(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"plain text", "plain text"},
		{"tab\tkept", "tab\tkept"},
		{"héllo, 世界", "héllo, 世界"},
		{"\x1b[31mred\x1b[0m", "␛[31mred␛[0m"},
		{"\x1b]8;;https://evil.example\x07click\x1b]8;;\x07", "␛]8;;https://evil.example␇click␛]8;;␇"},
		{"a\rb\bc", "a␍b␈c"},
		{"one\ntwo", "one␊two"},
		{"del\x7f", "del␡"},
		{"c1 \u009b2J", "c1 �2J"},
		{"bad \xff byte", "bad � byte"},
	} {
		got := printable(c.in)
		if got != c.want {
			t.Errorf("printable(%q) = %q, want %q", c.in, got, c.want)
		}
		if strings.ContainsAny(got, "\x1b\x07\r\b\n") {
			t.Errorf("printable(%q) still has a control character in it: %q", c.in, got)
		}
	}
}

// One column for every character it replaces, so that nothing measured in
// columns moves.
func TestPrintableKeepsTheWidth(t *testing.T) {
	in := "a\x1bb\x07c\x7fd\u009be"
	if got := runewidth.StringWidth(printable(in)); got != len([]rune(in)) {
		t.Errorf("%q is %d columns, want %d", printable(in), got, len([]rune(in)))
	}
}

func TestPrintableLinesKeepsTheLines(t *testing.T) {
	got := printableLines("one\r\ntwo\x1b[2J\nthree")
	if want := "one\ntwo␛[2J\nthree"; got != want {
		t.Errorf("got %q, want %q", got, want)
	}
}

// A Windows line's carriage return is not shown on every line of the file:
// it is not part of what the line says.
func TestPrintableCodeDropsTheLineEnd(t *testing.T) {
	if got := printableCode("text\r"); got != "text" {
		t.Errorf("got %q", got)
	}
	if got := printableCode("a\rb\r"); got != "a␍b" {
		t.Errorf("got %q", got)
	}
}

// A remark in the review is drawn as text: one written with escape sequences
// in it — by an agent, by anybody who can write the file — cannot reach the
// terminal through the window.
func TestARemarkIsDrawnAsText(t *testing.T) {
	m := modelFor(t, testRepo(t))
	err := review.Update(m.src.(*git.Repo), m.branch, 0, func(st *review.State) error {
		th := st.Start(&review.Thread{ID: "t1\x1b]52;c;aWQ=\x07", Path: "foo.txt", Line: 2}, "agent\x1b[2J",
			"look \x1b]8;;https://evil.example\x07here\x1b]8;;\x07\nand \x1b]52;c;aGk=\x07 this")
		th.Comments[0].At = "\x1b]52;c;dGltZQ==\x07"
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	m.pickUpChanges()
	view := m.View().Content
	if !strings.Contains(view, "␛]8;;https://evil.example␇here") {
		t.Fatalf("the remark is not on screen as text:\n%s", view)
	}
	for _, bad := range []string{"\x1b]", "\x07", "\x1b[2J"} {
		if strings.Contains(view, bad) {
			t.Errorf("the window drew %q from the review file", bad)
		}
	}
}

// A list shows its items printable, and hands back the one chosen as it was
// given: a branch whose name is not UTF-8 is still that branch.
func TestAListHandsBackWhatItWasGiven(t *testing.T) {
	var chosen string
	d := listDialog("Check out a branch", "Which?", []string{"caf\xe9"}, func(s string) tea.Cmd {
		chosen = s
		return nil
	})
	if view := d.view(newStyles(DefaultTheme(), true), 80, 24); !strings.Contains(view, "caf�") {
		t.Errorf("the item is not drawn printable:\n%s", view)
	}
	d.update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if chosen != "caf\xe9" {
		t.Errorf("chose %q, want the name as it was given", chosen)
	}
}

// The markdown reader's links are text: the address is printed beside the
// words, and nothing the terminal would follow is drawn.
func TestMarkdownLinksAreText(t *testing.T) {
	v := newMdView("README.md", "See [the docs](https://docs.example/page) here.\n", nil, 100, 20)
	view := v.view(newStyles(DefaultTheme(), true))
	if strings.Contains(view, "\x1b]8;") {
		t.Errorf("the reader drew a hyperlink:\n%q", view)
	}
	if !strings.Contains(ansi.Strip(view), "https://docs.example/page") {
		t.Errorf("the reader lost the link's address:\n%s", ansi.Strip(view))
	}
}
