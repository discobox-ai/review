package ui

import (
	"strings"
	"testing"

	"github.com/obot-platform/difftui/internal/git"
)

func TestExpandTabs(t *testing.T) {
	cases := []struct{ in, want string }{
		{"no tabs", "no tabs"},
		{"\tone", "    one"},
		{"a\tb", "a   b"},   // to the next stop of 4
		{"abc\td", "abc d"}, // one space left to the stop
		{"abcd\te", "abcd    e"},
		{"\t\tx", "        x"},
	}
	for _, c := range cases {
		if got := expandTabs(c.in); got != c.want {
			t.Errorf("expandTabs(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestChangedSpanNarrowsToTheEdit(t *testing.T) {
	before := "const prefix = \"hello, \""
	after := "const prefix = \"greetings, \""
	oldSpan, newSpan, ok := changedSpan(before, after)
	if !ok {
		t.Fatal("changedSpan found nothing")
	}
	gotOld := string([]rune(before)[oldSpan.start:oldSpan.end])
	gotNew := string([]rune(after)[newSpan.start:newSpan.end])
	if !strings.Contains(gotOld, "hello") || strings.Contains(gotOld, "const") {
		t.Errorf("old span = %q, want just the changed word", gotOld)
	}
	if !strings.Contains(gotNew, "greetings") || strings.Contains(gotNew, "const") {
		t.Errorf("new span = %q, want just the changed word", gotNew)
	}
}

func TestChangedSpanGivesUpWhenMostOfTheLineChanged(t *testing.T) {
	if _, _, ok := changedSpan("completely different", "nothing alike here"); ok {
		t.Error("a wholly rewritten line should not be emphasised")
	}
}

// Emphasis only applies when a run of removals is followed by an equally long
// run of additions: that is an edit. Anything else is a pure insertion or
// deletion, with no counterpart to compare against.
func TestPairEmphasisOnlyPairsEqualRuns(t *testing.T) {
	raw := `diff --git a/x.go b/x.go
--- a/x.go
+++ b/x.go
@@ -1,4 +1,4 @@
 keep
-const prefix = "hello, "
+const prefix = "greetings, "
 keep
`
	lines := git.ParsePatch(raw).Render()
	texts := make([]string, len(lines))
	for i, l := range lines {
		texts[i] = codeText(l.Text)
	}
	emph := pairEmphasis(lines, texts)
	for i, l := range lines {
		switch l.Kind {
		case '+', '-':
			if emph[i].empty() {
				t.Errorf("line %q got no emphasis", l.Text)
			}
		default:
			if !emph[i].empty() {
				t.Errorf("line %q should not be emphasised", l.Text)
			}
		}
	}

	// A pure insertion has no counterpart.
	rawAdd := `diff --git a/x.go b/x.go
--- a/x.go
+++ b/x.go
@@ -1,2 +1,3 @@
 keep
+brand new line
 keep
`
	lines = git.ParsePatch(rawAdd).Render()
	texts = make([]string, len(lines))
	for i, l := range lines {
		texts[i] = codeText(l.Text)
	}
	for i, e := range pairEmphasis(lines, texts) {
		if !e.empty() {
			t.Errorf("line %q should not be emphasised on a pure insertion", lines[i].Text)
		}
	}
}
