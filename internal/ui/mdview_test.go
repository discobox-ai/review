package ui

import (
	"strings"
	"testing"

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
	raw := "# Title\n\nfirst paragraph\n\nsecond paragraph\n\nthird paragraph\n"
	// "second paragraph" is source line 5. The window is kept short so the
	// jump target is not already pinned to the top by the clamp.
	v := newMdView("x.md", raw, map[int]bool{5: true}, 80, 5)
	if len(v.jumps) != 1 {
		t.Fatalf("jumps = %v, want the one changed block", v.jumps)
	}
	var flagged []string
	for i, l := range v.lines {
		if v.changed[i] && strings.TrimSpace(ansi.Strip(l)) != "" {
			flagged = append(flagged, ansi.Strip(l))
		}
	}
	if len(flagged) == 0 || !strings.Contains(flagged[0], "second paragraph") {
		t.Errorf("marked lines = %q, want the second paragraph", flagged)
	}
	for _, l := range flagged {
		if strings.Contains(l, "first paragraph") || strings.Contains(l, "third paragraph") {
			t.Errorf("an untouched paragraph is marked: %q", l)
		}
	}

	if v.offset != 0 {
		t.Fatalf("reader starts at offset %d", v.offset)
	}
	v.jump(1)
	if v.offset != v.jumps[0] {
		t.Errorf("n went to %d, want %d", v.offset, v.jumps[0])
	}
	v.jump(1) // off the end: stays put
	if v.offset != v.jumps[0] {
		t.Errorf("n off the end moved to %d", v.offset)
	}
	v.moveTo(len(v.lines))
	v.jump(-1)
	if v.offset != v.jumps[0] {
		t.Errorf("p went to %d, want %d", v.offset, v.jumps[0])
	}

	view := v.view(newStyles())
	if !strings.Contains(view, "▍") {
		t.Error("the gutter has no marker")
	}
	if !strings.Contains(view, "n/p changed block") {
		t.Error("the hint does not offer n/p")
	}

	clean := newMdView("x.md", raw, nil, 80, 5)
	if len(clean.jumps) != 0 || strings.Contains(clean.view(newStyles()), "▍") {
		t.Error("an untouched file grew a gutter")
	}
}
