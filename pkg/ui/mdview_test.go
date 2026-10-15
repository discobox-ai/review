package ui

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// mdBlocks splits at blank lines, and a fenced code block holds together
// across the blank lines inside it.
func TestMdBlocks(t *testing.T) {
	src := strings.Join([]string{
		"# Title",     // 1
		"",            // 2
		"First para",  // 3
		"still first", // 4
		"",            // 5
		"```go",       // 6
		"a := 1",      // 7
		"",            // 8
		"b := 2",      // 9
		"```",         // 10
		"",            // 11
		"Last para",   // 12
	}, "\n")
	got := mdBlocks(src)
	want := []struct{ start, end int }{{1, 1}, {3, 4}, {6, 10}, {12, 12}}
	if len(got) != len(want) {
		t.Fatalf("got %d blocks, want %d: %+v", len(got), len(want), got)
	}
	for i, w := range want {
		if got[i].start != w.start || got[i].end != w.end {
			t.Errorf("block %d = %d–%d, want %d–%d", i, got[i].start, got[i].end, w.start, w.end)
		}
	}
	if got[2].text != "```go\na := 1\n\nb := 2\n```" {
		t.Errorf("the fence was split: %q", got[2].text)
	}
}

// A change marks its whole block, the blocks are jumpable in both directions,
// and an untouched file has no gutter at all.
func TestMdViewMarksAndJumps(t *testing.T) {
	// Three changed paragraphs out of five, at source lines 5, 9 and 13, with
	// enough text that the jumps have somewhere to scroll to.
	raw := "# Title\n\nfirst paragraph\n\nsecond paragraph\n\nthird paragraph\n\n" +
		"fourth paragraph\n\nfifth paragraph\n\nsixth paragraph\n\nseventh paragraph\n"
	v := newMdView("x.md", raw, map[int]bool{5: true, 9: true, 13: true}, 80, 6)
	if len(v.jumps) != 3 {
		t.Fatalf("jumps = %v, want three changed blocks", v.jumps)
	}
	var flagged []string
	for i, l := range v.lines {
		if v.block[i] >= 0 && strings.TrimSpace(ansi.Strip(l)) != "" {
			flagged = append(flagged, ansi.Strip(l))
		}
	}
	if len(flagged) != 3 || !strings.Contains(flagged[0], "second paragraph") ||
		!strings.Contains(flagged[1], "fourth paragraph") || !strings.Contains(flagged[2], "sixth paragraph") {
		t.Errorf("marked lines = %q, want the second, fourth and sixth paragraphs", flagged)
	}

	// Nothing is current until n or p says so; the marks are all alike.
	if v.cur != -1 || v.offset != 0 {
		t.Fatalf("reader starts at cur %d offset %d", v.cur, v.offset)
	}
	if strings.Contains(v.view(newStyles(DefaultTheme(), true)), "▌") {
		t.Error("a block is picked out before n or p was pressed")
	}

	// n lands on the first block, three lines above it so some of what came
	// before is on screen — clamped to the top when there is not that much.
	v.jump(1)
	if v.cur != 0 || v.offset != max(v.jumps[0]-mdLead, 0) {
		t.Errorf("n: cur %d offset %d, want block 0 at %d", v.cur, v.offset, max(v.jumps[0]-mdLead, 0))
	}
	view := v.view(newStyles(DefaultTheme(), true))
	if !strings.Contains(view, "▌") || !strings.Contains(view, "change 1 of 3") {
		t.Errorf("the current block is not picked out:\n%s", ansi.Strip(view))
	}
	v.jump(1)
	if v.cur != 1 || v.offset != v.jumps[1]-mdLead {
		t.Errorf("second n: cur %d offset %d, want block 1 at %d", v.cur, v.offset, v.jumps[1]-mdLead)
	}
	// The current block's marker differs from the others' on the same screen.
	for line := range strings.SplitSeq(v.view(newStyles(DefaultTheme(), true)), "\n") {
		plain := ansi.Strip(line)
		switch {
		case strings.Contains(plain, "fourth paragraph") && !strings.HasPrefix(plain, "▌"):
			t.Errorf("the current block does not carry the current mark: %q", plain)
		case strings.Contains(plain, "sixth paragraph") && !strings.HasPrefix(plain, "▍"):
			t.Errorf("another block carries the wrong mark: %q", plain)
		}
	}
	v.jump(1)
	v.jump(1) // off the end: stays put
	if v.cur != 2 {
		t.Errorf("n off the end moved to block %d", v.cur)
	}
	v.jump(-1)
	if v.cur != 1 || v.offset != v.jumps[1]-mdLead {
		t.Errorf("p: cur %d offset %d, want block 1 at %d", v.cur, v.offset, v.jumps[1]-mdLead)
	}

	if !strings.Contains(v.view(newStyles(DefaultTheme(), true)), "n/p changed block") {
		t.Error("the hint does not offer n/p")
	}

	clean := newMdView("x.md", raw, nil, 80, 6)
	if len(clean.jumps) != 0 || strings.ContainsAny(clean.view(newStyles(DefaultTheme(), true)), "▍▌") {
		t.Error("an untouched file grew a gutter")
	}
}

// A document is rendered in a moment, not in seconds.
//
// It used to take three of them. Each block's search for a touched line ran to
// the start of the next block, and the last block had no next block — so its
// bound was a number picked to mean "the end", and the search walked to it one
// line number at a time. A long file spent a billion map lookups deciding that
// its last paragraph was unchanged.
func TestReaderRendersQuickly(t *testing.T) {
	var b strings.Builder
	for i := range 400 {
		fmt.Fprintf(&b, "## Heading %d\n\nA paragraph of prose about %d.\n\n", i, i)
	}
	raw := b.String()

	start := time.Now()
	v := newMdView("long.md", raw, map[int]bool{5: true}, 100, 40)
	took := time.Since(start)
	if len(v.lines) == 0 {
		t.Fatal("nothing rendered")
	}
	if took > 2*time.Second {
		t.Errorf("rendering %d blocks took %v", 400, took)
	}

	// And a window that only changed height does not render again at all.
	start = time.Now()
	v.resize(100, 20)
	if again := time.Since(start); again > took/4 {
		t.Errorf("a height-only resize took %v, against %v to render", again, took)
	}
}

// The marker still lands on the block the change touched, wherever it is —
// including the last one, which is the block the bound was there for.
func TestReaderMarksTheLastBlock(t *testing.T) {
	raw := "# One\n\nFirst paragraph.\n\nLast paragraph.\n"
	// Line 5 is the last paragraph.
	v := newMdView("x.md", raw, map[int]bool{5: true}, 80, 20)
	if len(v.jumps) != 1 {
		t.Fatalf("jumps = %v, want the last block marked", v.jumps)
	}
	// And a change past every block still reaches the last one.
	v = newMdView("x.md", raw, map[int]bool{6: true}, 80, 20)
	if len(v.jumps) != 1 {
		t.Errorf("jumps = %v, want the last block to reach the end", v.jumps)
	}
}
