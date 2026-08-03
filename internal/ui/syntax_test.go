package ui

import (
	"strings"
	"testing"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/lipgloss"
	"github.com/obot-platform/difftui/internal/git"
)

// spanText returns the source covered by a span, for readable assertions.
func spanText(line string, s span) string {
	runes := []rune(codeText(line))
	if s.start < 0 || s.end > len(runes) || s.start >= s.end {
		return ""
	}
	return string(runes[s.start:s.end])
}

// tokensOf maps each highlighted fragment of a line to its token category.
func tokensOf(t *testing.T, lines []git.DiffLine, spans [][]span, i int) map[string]chroma.TokenType {
	t.Helper()
	out := map[string]chroma.TokenType{}
	for _, s := range spans[i] {
		out[spanText(lines[i].Text, s)] = s.token
	}
	return out
}

// inCategory reports whether a token is the wanted type or falls under it.
// chroma's hierarchy is three deep: LiteralString's Category is Literal, so a
// plain Category comparison misses it.
func inCategory(token, want chroma.TokenType) bool {
	return token == want || token.SubCategory() == want || token.Category() == want
}

func parseAndHighlight(t *testing.T, path, raw string) ([]git.DiffLine, [][]span) {
	t.Helper()
	lines := git.ParsePatch(raw).Render()
	return lines, highlightLines(path, lines)
}

const goDiff = `diff --git a/x.go b/x.go
--- a/x.go
+++ b/x.go
@@ -1,4 +1,4 @@
 package main
-func old() string { return "before" }
+func shiny() string { return "after" }
 var n = 42
`

func TestHighlightPicksOutKeywordsAndLiterals(t *testing.T) {
	lines, spans := parseAndHighlight(t, "x.go", goDiff)
	if len(spans) != len(lines) {
		t.Fatalf("spans has %d entries, lines has %d", len(spans), len(lines))
	}

	// Find the added line.
	added := -1
	for i, l := range lines {
		if l.Kind == '+' {
			added = i
		}
	}
	if added < 0 {
		t.Fatal("no added line in the rendered diff")
	}

	got := tokensOf(t, lines, spans, added)
	if len(got) == 0 {
		t.Fatalf("the added line got no highlighting: %q", lines[added].Text)
	}
	if tok, ok := got["func"]; !ok || !inCategory(tok, chroma.Keyword) {
		t.Errorf(`"func" token = %v (present=%v), want a Keyword`, tok, ok)
	}
	if tok, ok := got[`"after"`]; !ok || !inCategory(tok, chroma.LiteralString) {
		t.Errorf(`the string literal token = %v (present=%v), want a LiteralString`, tok, ok)
	}
	// The function name is coloured on purpose; punctuation is not.
	if tok, ok := got["shiny"]; !ok || !inCategory(tok, chroma.Name) {
		t.Errorf(`"shiny" token = %v (present=%v), want a Name`, tok, ok)
	}
	// Punctuation is deliberately left in the pane's own foreground.
	for _, plain := range []string{"{", "}", "(", ")"} {
		if _, ok := got[plain]; ok {
			t.Errorf("%q should not be coloured", plain)
		}
	}
}

func TestHighlightCoversBothSidesOfAHunk(t *testing.T) {
	lines, spans := parseAndHighlight(t, "x.go", goDiff)
	for i, l := range lines {
		if !isCodeLine(l.Kind) {
			continue
		}
		if strings.TrimSpace(codeText(l.Text)) == "" {
			continue
		}
		if len(spans[i]) == 0 {
			t.Errorf("line %d (%c) got no spans: %q", i, l.Kind, l.Text)
		}
	}
}

func TestHighlightSpansStayInsideTheLine(t *testing.T) {
	lines, spans := parseAndHighlight(t, "x.go", goDiff)
	for i, l := range lines {
		width := len([]rune(codeText(l.Text)))
		for _, s := range spans[i] {
			if s.start < 0 || s.end > width || s.start >= s.end {
				t.Errorf("line %d %q: span %d..%d out of range 0..%d", i, l.Text, s.start, s.end, width)
			}
		}
	}
}

// A block comment opened on a context line must still be recognised on the
// lines that follow, which is why each side is lexed as one text.
func TestHighlightKeepsLexerStateAcrossLines(t *testing.T) {
	raw := `diff --git a/x.go b/x.go
--- a/x.go
+++ b/x.go
@@ -1,4 +1,4 @@
 /* a comment that
    continues here
    and ends here */
-var a = 1
+var b = 2
`
	lines, spans := parseAndHighlight(t, "x.go", raw)
	for i, l := range lines {
		if !strings.Contains(l.Text, "continues here") {
			continue
		}
		if len(spans[i]) == 0 {
			t.Fatalf("the comment continuation got no spans: %q", l.Text)
		}
		for _, s := range spans[i] {
			if s.token.Category() != chroma.Comment {
				t.Errorf("token %v on %q, want a Comment", s.token, l.Text)
			}
		}
		return
	}
	t.Fatal("did not find the comment continuation line")
}

func TestNoLexerForUnknownExtension(t *testing.T) {
	if lexerFor("notes.zzzz-not-a-language") != nil {
		t.Error("expected no lexer for an unknown extension")
	}
	raw := strings.ReplaceAll(goDiff, "x.go", "notes.zzzz-not-a-language")
	lines, spans := parseAndHighlight(t, "notes.zzzz-not-a-language", raw)
	if len(spans) != len(lines) {
		t.Fatalf("spans has %d entries, lines has %d", len(spans), len(lines))
	}
	for i := range lines {
		if len(spans[i]) != 0 {
			t.Errorf("line %d got spans without a lexer", i)
		}
	}
}

func TestPaletteWalksTheTokenHierarchy(t *testing.T) {
	p := newSyntaxPalette()
	// A subtype the palette does not name should inherit its category.
	if _, ok := p.color(chroma.CommentMultiline); !ok {
		t.Error("CommentMultiline should inherit the Comment colour")
	}
	if _, ok := p.color(chroma.LiteralStringDouble); !ok {
		t.Error("LiteralStringDouble should inherit the LiteralString colour")
	}
	// Plain text has no colour at all.
	if _, ok := p.color(chroma.Text); ok {
		t.Error("plain Text should not be coloured")
	}
	if _, ok := p.color(chroma.NameVariable); ok {
		t.Error("plain variable names should not be coloured")
	}
}

// The rendered line must keep its exact pane width whatever the highlighting
// does, or the pane's right edge frays.
func TestRenderSyntaxLineKeepsWidth(t *testing.T) {
	lines, spans := parseAndHighlight(t, "x.go", goDiff)
	base := lipgloss.NewStyle()
	for _, width := range []int{1, 5, 12, 40, 200} {
		for i, l := range lines {
			if !isCodeLine(l.Kind) {
				continue
			}
			got := renderSyntaxLine(diffGutter(l.Kind), codeText(l.Text), spans[i], base, newSyntaxPalette(), width)
			if w := lipgloss.Width(got); w != width {
				t.Errorf("width %d: line %q rendered %d cells", width, l.Text, w)
			}
		}
	}
}

func TestRenderSyntaxLineKeepsTheText(t *testing.T) {
	lines, spans := parseAndHighlight(t, "x.go", goDiff)
	base := lipgloss.NewStyle()
	for i, l := range lines {
		if !isCodeLine(l.Kind) {
			continue
		}
		got := renderSyntaxLine(diffGutter(l.Kind), codeText(l.Text), spans[i], base, newSyntaxPalette(), 200)
		if !strings.Contains(got, strings.TrimSpace(codeText(l.Text))) {
			t.Errorf("rendered line dropped its text: %q -> %q", l.Text, got)
		}
	}
}

func TestRenderSyntaxLineWithoutPaletteIsPlain(t *testing.T) {
	lines, spans := parseAndHighlight(t, "x.go", goDiff)
	base := lipgloss.NewStyle()
	for i, l := range lines {
		if l.Kind != '+' {
			continue
		}
		gut, code := diffGutter(l.Kind), codeText(l.Text)
		plain := renderSyntaxLine(gut, code, nil, base, nil, 60)
		coloured := renderSyntaxLine(gut, code, spans[i], base, newSyntaxPalette(), 60)
		if lipgloss.Width(plain) != lipgloss.Width(coloured) {
			t.Error("highlighting changed the rendered width")
		}
		return
	}
}

// The gutter gives each side its own column, so an addition always has a space
// between its marker and the code.
func TestDiffGutterColumns(t *testing.T) {
	cases := []struct {
		kind byte
		want string
	}{
		{'+', "+ "},
		{'-', " -"},
		{' ', "  "},
	}
	for _, c := range cases {
		if got := diffGutter(c.kind); got != c.want {
			t.Errorf("diffGutter(%q) = %q, want %q", c.kind, got, c.want)
		}
		if len([]rune(diffGutter(c.kind))) != 2 {
			t.Errorf("diffGutter(%q) is not two columns wide", c.kind)
		}
	}
}

// The rendered line reads "+ code", " -code" or "  code".
func TestRenderedGutterLayout(t *testing.T) {
	lines, spans := parseAndHighlight(t, "x.go", goDiff)
	base := lipgloss.NewStyle()
	seen := map[byte]string{}
	for i, l := range lines {
		if !isCodeLine(l.Kind) {
			continue
		}
		if _, already := seen[l.Kind]; already {
			continue // keep the first line of each kind
		}
		seen[l.Kind] = renderSyntaxLine(diffGutter(l.Kind), codeText(l.Text), spans[i], base, newSyntaxPalette(), 200)
	}
	for kind, want := range map[byte]string{
		'+': "+ func shiny",
		'-': " -func old",
		' ': "  package main",
	} {
		got, ok := seen[kind]
		if !ok {
			t.Errorf("no %c line rendered", kind)
			continue
		}
		if !strings.HasPrefix(got, want) {
			t.Errorf("%c line = %q, want it to start with %q", kind, got, want)
		}
	}
}
