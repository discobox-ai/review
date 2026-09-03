package git

import (
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
	if p.Path != "foo.txt" || p.From != "" {
		t.Errorf("paths = %q / %q, want foo.txt and no rename", p.Path, p.From)
	}
}

func TestPatchStat(t *testing.T) {
	adds, dels := ParsePatch(sample).Stat()
	if adds != 3 || dels != 2 {
		t.Errorf("Stat() = +%d −%d, want +3 −2", adds, dels)
	}
}

func TestRenderMetadata(t *testing.T) {
	lines := ParsePatch(sample).Render()
	// The "diff --git"/"index"/"---"/"+++" preamble is hidden: the paths are
	// already in the section heading and the blob hashes say nothing.
	if lines[0].Kind != '@' {
		t.Fatalf("first rendered line is %q (kind %c), want the hunk header",
			lines[0].Text, lines[0].Kind)
	}
}

// A file with no hunks — a mode change, a binary file — still renders its
// header, so that its section is not blank.
func TestRenderKeepsAHeaderWithNoHunks(t *testing.T) {
	p := ParsePatch("diff --git a/x b/x\nold mode 100644\nnew mode 100755\n")
	lines := p.Render()
	if len(lines) == 0 {
		t.Fatal("a patch with no hunks rendered nothing")
	}
	found := false
	for _, l := range lines {
		if l.Kind == 'h' && l.Text == "new mode 100755" {
			found = true
		}
	}
	if !found {
		t.Errorf("mode change not rendered: %+v", lines)
	}
	if p.Path != "x" {
		t.Errorf("path = %q, want x", p.Path)
	}
}

// ---------------------------------------------------------------------------
// Splitting one diff into many files
// ---------------------------------------------------------------------------

const multi = `diff --git a/a.txt b/a.txt
index 111..222 100644
--- a/a.txt
+++ b/a.txt
@@ -1 +1 @@
-alpha
+ALPHA
diff --git a/b/nested.go b/b/nested.go
new file mode 100644
--- /dev/null
+++ b/b/nested.go
@@ -0,0 +1,2 @@
+package b
+
diff --git a/old name.txt b/new name.txt
similarity index 90%
rename from old name.txt
rename to new name.txt
--- a/old name.txt
+++ b/new name.txt
@@ -1 +1 @@
-x
+y
diff --git a/gone.txt b/gone.txt
deleted file mode 100644
--- a/gone.txt
+++ /dev/null
@@ -1 +0,0 @@
-bye
`

func TestParseMultiSplitsByFile(t *testing.T) {
	patches := ParseMulti(multi)
	if len(patches) != 4 {
		t.Fatalf("got %d patches, want 4", len(patches))
	}
	want := []struct{ path, from string }{
		{"a.txt", ""},
		{"b/nested.go", ""},
		{"new name.txt", "old name.txt"},
		{"gone.txt", ""},
	}
	for i, w := range want {
		if patches[i].Path != w.path || patches[i].From != w.from {
			t.Errorf("patch %d = %q / %q, want %q / %q",
				i, patches[i].Path, patches[i].From, w.path, w.from)
		}
	}
	// Each patch keeps only its own hunks.
	if got, _ := patches[0].Stat(); got != 1 {
		t.Errorf("a.txt adds = %d, want 1", got)
	}
	if got, _ := patches[1].Stat(); got != 2 {
		t.Errorf("nested.go adds = %d, want 2", got)
	}
}

func TestParseMultiOnNothing(t *testing.T) {
	if got := ParseMulti(""); got != nil {
		t.Errorf("ParseMulti(\"\") = %v, want nil", got)
	}
	if got := ParseMulti("   \n"); got != nil {
		t.Errorf("ParseMulti(blank) = %v, want nil", got)
	}
}

// A path git had to quote — one with a space or a byte outside ASCII — comes
// back as the name it really is rather than with the quotes still on it.
func TestParseMultiUnquotesPaths(t *testing.T) {
	text := "diff --git \"a/dir/a b.txt\" \"b/dir/a b.txt\"\n" +
		"--- \"a/dir/a b.txt\"\n+++ \"b/dir/a b.txt\"\n" +
		"@@ -1 +1 @@\n-x\n+y\n"
	p := ParseMulti(text)
	if len(p) != 1 || p[0].Path != "dir/a b.txt" {
		t.Fatalf("path = %q, want dir/a b.txt", p[0].Path)
	}
}

// ---------------------------------------------------------------------------
// Line numbers, and the seam a deletion leaves
// ---------------------------------------------------------------------------

func TestRenderLineNumbers(t *testing.T) {
	lines := ParsePatch(sample).Render()
	type want struct {
		text          string
		old, new, sea int
	}
	// The sample's first hunk starts at 1/1, the second at 10/11.
	expect := []want{
		{" one", 1, 1, 1},
		{"-two", 2, 0, 2},
		{"+TWO", 0, 2, 2},
		{"+two and a half", 0, 3, 3},
		{" three", 3, 4, 4},
		{" ten", 10, 11, 11},
		{"-eleven", 11, 0, 12},
		{"+ELEVEN", 0, 12, 12},
	}
	var got []want
	for _, l := range lines {
		if l.Kind == '@' || l.Kind == 'h' {
			continue
		}
		got = append(got, want{l.Text, l.Old, l.New, l.Seam})
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

// A deletion at the very end of a file has no line after it, so its seam
// clamps to the last line there is rather than running off the end.
func TestSeamAtTheEndOfAFile(t *testing.T) {
	p := ParsePatch("--- a/x\n+++ b/x\n@@ -1,2 +1,0 @@\n-one\n-two\n")
	for _, l := range p.Render() {
		if l.Kind == '-' && l.Seam < 1 {
			t.Errorf("seam of %q = %d, want at least 1", l.Text, l.Seam)
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

// ---------------------------------------------------------------------------
// Carrying a line number across a diff
// ---------------------------------------------------------------------------

func TestMapOldToNew(t *testing.T) {
	p := ParsePatch(sample)
	cases := []struct {
		old, new int
		ok       bool
	}{
		{1, 1, true},   // context before the first change
		{2, 0, false},  // "two" was removed: nothing on the new side
		{3, 4, true},   // context after one deletion and two additions
		{9, 10, true},  // between the hunks, moved by the running offset
		{10, 11, true}, // context in the second hunk
		{11, 0, false}, // "eleven" was removed
		{12, 13, true}, // past the last hunk
		{0, 0, false},  // there is no line zero
	}
	for _, c := range cases {
		got, ok := p.MapOldToNew(c.old)
		if got != c.new || ok != c.ok {
			t.Errorf("MapOldToNew(%d) = %d, %v; want %d, %v", c.old, got, ok, c.new, c.ok)
		}
	}
}

func TestMapNewToOld(t *testing.T) {
	p := ParsePatch(sample)
	cases := []struct {
		new, old int
		ok       bool
	}{
		{1, 1, true},
		{2, 0, false}, // "TWO" was added: it has no old side
		{3, 0, false}, // and neither has "two and a half"
		{4, 3, true},
		{11, 10, true},
		{12, 0, false}, // "ELEVEN" was added
		{13, 12, true},
	}
	for _, c := range cases {
		got, ok := p.MapNewToOld(c.new)
		if got != c.old || ok != c.ok {
			t.Errorf("MapNewToOld(%d) = %d, %v; want %d, %v", c.new, got, ok, c.old, c.ok)
		}
	}
}

// A line number carried out and back again is the line it started as, for
// every line that survives the diff at all.
func TestCarryingIsReversible(t *testing.T) {
	p := ParsePatch(sample)
	for old := 1; old <= 15; old++ {
		mid, ok := p.MapOldToNew(old)
		if !ok {
			continue
		}
		back, ok := p.MapNewToOld(mid)
		if !ok || back != old {
			t.Errorf("old %d → new %d → old %d (ok=%v)", old, mid, back, ok)
		}
	}
}

// An empty diff carries every line straight through: nothing moved.
func TestMappingThroughAnEmptyDiff(t *testing.T) {
	p := ParsePatch("")
	for _, n := range []int{1, 7, 1000} {
		if got, ok := p.MapOldToNew(n); !ok || got != n {
			t.Errorf("MapOldToNew(%d) = %d, %v; want %d, true", n, got, ok, n)
		}
	}
}

// ---------------------------------------------------------------------------
// File states
// ---------------------------------------------------------------------------

func TestStateReadsWhatIsLeftToDo(t *testing.T) {
	cases := []struct {
		state     State
		committed bool
		pending   string
	}{
		{"__", true, ""},
		{"_O", false, "untracked"},
		{"M_", false, "staged"},
		{"MM", false, "part staged"},
		{"_M", false, "uncommitted"},
		{"_D", false, "deleted"},
		{"UU", false, "unmerged"},
	}
	for _, c := range cases {
		if got := c.state.Committed(); got != c.committed {
			t.Errorf("%s.Committed() = %v, want %v", c.state, got, c.committed)
		}
		if got := c.state.Pending(); got != c.pending {
			t.Errorf("%s.Pending() = %q, want %q", c.state, got, c.pending)
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
