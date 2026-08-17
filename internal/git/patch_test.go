package git

import (
	"strings"
	"testing"
)

const sample = `diff --git a/foo.txt b/foo.txt
index 1234567..89abcde 100644
--- a/foo.txt
+++ b/foo.txt
@@ -1,3 +1,4 @@
 one
-two
+TWO
+two and a half
 three
@@ -10,2 +11,2 @@
 ten
-eleven
+ELEVEN
`

func TestParsePatch(t *testing.T) {
	p := ParsePatch(sample)
	if len(p.Hunks) != 2 {
		t.Fatalf("got %d hunks, want 2", len(p.Hunks))
	}
	if p.Hunks[0].OldStart != 1 || p.Hunks[0].NewStart != 1 {
		t.Errorf("hunk 0 starts = %d/%d, want 1/1", p.Hunks[0].OldStart, p.Hunks[0].NewStart)
	}
	if p.Hunks[1].OldStart != 10 || p.Hunks[1].NewStart != 11 {
		t.Errorf("hunk 1 starts = %d/%d, want 10/11", p.Hunks[1].OldStart, p.Hunks[1].NewStart)
	}
	if len(p.Hunks[0].Lines) != 5 {
		t.Errorf("hunk 0 has %d lines, want 5", len(p.Hunks[0].Lines))
	}
}

func TestHunkPatch(t *testing.T) {
	p := ParsePatch(sample)
	got, ok := p.HunkPatch(1)
	if !ok {
		t.Fatal("HunkPatch(1) not ok")
	}
	if strings.Contains(got, "TWO") {
		t.Error("hunk 1 patch leaked lines from hunk 0")
	}
	for _, want := range []string{"--- a/foo.txt", "+++ b/foo.txt", "@@ -10,2 +11,2 @@", "+ELEVEN"} {
		if !strings.Contains(got, want) {
			t.Errorf("patch missing %q:\n%s", want, got)
		}
	}
}

func TestRenderMetadata(t *testing.T) {
	p := ParsePatch(sample)
	lines := p.Render()
	// The "diff --git"/"index"/"---"/"+++" preamble is hidden, as git-gui does.
	if lines[0].Kind != '@' {
		t.Fatalf("first rendered line is %q (kind %c), want a hunk header", lines[0].Text, lines[0].Kind)
	}
	var adds, dels, hunks int
	for _, l := range lines {
		switch l.Kind {
		case '+':
			adds++
		case '-':
			dels++
		case '@':
			hunks++
		}
	}
	if adds != 3 || dels != 2 || hunks != 2 {
		t.Errorf("adds=%d dels=%d hunks=%d, want 3/2/2", adds, dels, hunks)
	}
}

func TestLinePatchForward(t *testing.T) {
	p := ParsePatch(sample)
	// Stage only "+TWO" (body index 1 is "-two", 2 is "+TWO").
	got, ok := p.LinePatch(0, map[int]bool{2: true}, false)
	if !ok {
		t.Fatal("LinePatch not ok")
	}
	want := strings.Join([]string{
		"@@ -1,3 +1,4 @@",
		" one",
		" two",
		"+TWO",
		" three",
	}, "\n")
	if !strings.Contains(got, want) {
		t.Errorf("got:\n%s\nwant it to contain:\n%s", got, want)
	}
}

func TestLinePatchForwardDeletion(t *testing.T) {
	p := ParsePatch(sample)
	// Stage only the "-two" deletion: the additions are dropped.
	got, ok := p.LinePatch(0, map[int]bool{1: true}, false)
	if !ok {
		t.Fatal("LinePatch not ok")
	}
	want := strings.Join([]string{
		"@@ -1,3 +1,2 @@",
		" one",
		"-two",
		" three",
	}, "\n")
	if !strings.Contains(got, want) {
		t.Errorf("got:\n%s\nwant it to contain:\n%s", got, want)
	}
}

func TestLinePatchReverse(t *testing.T) {
	p := ParsePatch(sample)
	// Unstaging "+TWO": unselected '-' lines are dropped, unselected '+'
	// lines become context so they stay in the index.
	got, ok := p.LinePatch(0, map[int]bool{2: true}, true)
	if !ok {
		t.Fatal("LinePatch not ok")
	}
	want := strings.Join([]string{
		"@@ -1,3 +1,4 @@",
		" one",
		"+TWO",
		" two and a half",
		" three",
	}, "\n")
	if !strings.Contains(got, want) {
		t.Errorf("got:\n%s\nwant it to contain:\n%s", got, want)
	}
}

func TestLinePatchNothingSelected(t *testing.T) {
	p := ParsePatch(sample)
	if _, ok := p.LinePatch(0, map[int]bool{0: true}, false); ok {
		t.Error("selecting only a context line should not produce a patch")
	}
}

func TestStateRouting(t *testing.T) {
	cases := []struct {
		state            State
		staged, unstaged bool
	}{
		{"_O", false, true},
		{"M_", true, false},
		{"MM", true, true},
		{"_M", false, true},
		{"A_", true, false},
		{"UU", false, true},
		{"D_", true, false},
		{"_D", false, true},
	}
	for _, c := range cases {
		if got := c.state.Staged(); got != c.staged {
			t.Errorf("%s.Staged() = %v, want %v", c.state, got, c.staged)
		}
		if got := c.state.Unstaged(); got != c.unstaged {
			t.Errorf("%s.Unstaged() = %v, want %v", c.state, got, c.unstaged)
		}
	}
}

func TestMapState(t *testing.T) {
	cases := []struct {
		x, y byte
		want State
	}{
		{'?', '?', "_O"},
		{'M', ' ', "M_"},
		{' ', 'M', "_M"},
		{'M', 'M', "MM"},
		{'A', ' ', "A_"},
		{'U', 'U', "UU"},
		{'A', 'A', "UU"},
		{'D', 'D', "UU"},
		{'R', ' ', "R_"},
	}
	for _, c := range cases {
		if got := mapState(c.x, c.y); got != c.want {
			t.Errorf("mapState(%c,%c) = %s, want %s", c.x, c.y, got, c.want)
		}
	}
}

func TestRenderLineNumbers(t *testing.T) {
	lines := ParsePatch(sample).Render()
	type want struct {
		text     string
		old, new int
	}
	// The sample's first hunk starts at 1/1, the second at 10/11.
	expect := []want{
		{" one", 1, 1},
		{"-two", 2, 0},
		{"+TWO", 0, 2},
		{"+two and a half", 0, 3},
		{" three", 3, 4},
		{" ten", 10, 11},
		{"-eleven", 11, 0},
		{"+ELEVEN", 0, 12},
	}
	var got []want
	for _, l := range lines {
		if l.Kind == '@' || l.Kind == 'h' {
			continue
		}
		got = append(got, want{l.Text, l.Old, l.New})
	}
	if len(got) != len(expect) {
		t.Fatalf("got %d code lines, want %d: %v", len(got), len(expect), got)
	}
	for i, w := range expect {
		if got[i] != w {
			t.Errorf("line %d = %+v, want %+v", i, got[i], w)
		}
	}
}

// ChangedNewLines names the new-side lines the diff touched: additions mark
// themselves, and a deletion marks the seam it left behind.
func TestChangedNewLines(t *testing.T) {
	got := ParsePatch(sample).ChangedNewLines()
	// "two" was deleted where new line 2 now stands, "TWO" and "two and a
	// half" are new lines 2 and 3, "eleven" deleted at the seam of 12, and
	// "ELEVEN" added as 12.
	want := map[int]bool{2: true, 3: true, 12: true}
	if len(got) != len(want) {
		t.Fatalf("ChangedNewLines = %v, want %v", got, want)
	}
	for k := range want {
		if !got[k] {
			t.Errorf("line %d not marked; got %v", k, got)
		}
	}
	if len(ParsePatch("* No differences detected.\n").ChangedNewLines()) != 0 {
		t.Error("a diff with no hunks marked something")
	}
}
