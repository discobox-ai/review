package ui

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/alecthomas/chroma/v2"
	"github.com/charmbracelet/lipgloss"
	"github.com/muesli/termenv"
	"github.com/obot-platform/difftui/internal/git"
)

// Tests run without a terminal, where lipgloss resolves to no colour at all
// and every style renders as bare text. Pinning a profile makes the rendering
// deterministic, and is the only way to assert on the diff's bands.
func init() {
	lipgloss.SetColorProfile(termenv.ANSI256)
	lipgloss.SetHasDarkBackground(true)
}

// plainText strips the styling from a rendered row, for assertions about the
// text itself rather than its colours.
func plainText(rendered string) string {
	text, _ := bgAt(rendered)
	return string(text)
}

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
	texts := make([]string, len(lines))
	for i, l := range lines {
		if isCodeLine(l.Kind) {
			texts[i] = expandTabs(codeText(l.Text))
		} else {
			texts[i] = l.Text
		}
	}
	return lines, highlightLines(path, lines, texts)
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
			got := renderDiffRow(diffRow{number: lineNumber(l), numW: 3, numSty: base,
				gutter: diffGutter(l.Kind), code: expandTabs(codeText(l.Text)),
				spans: spans[i], base: base, emphSty: base,
				palette: newSyntaxPalette(), width: width})
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
		got := renderDiffRow(diffRow{number: lineNumber(l), numW: 3, numSty: base,
			gutter: diffGutter(l.Kind), code: expandTabs(codeText(l.Text)),
			spans: spans[i], base: base, emphSty: base,
			palette: newSyntaxPalette(), width: 200})
		if !strings.Contains(plainText(got), strings.TrimSpace(codeText(l.Text))) {
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
		row := diffRow{number: lineNumber(l), numW: 3, numSty: base, gutter: diffGutter(l.Kind),
			code: expandTabs(codeText(l.Text)), base: base, emphSty: base, width: 60}
		plain := renderDiffRow(row)
		row.spans, row.palette = spans[i], newSyntaxPalette()
		coloured := renderDiffRow(row)
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
		seen[l.Kind] = renderDiffRow(diffRow{numW: 0,
			gutter: diffGutter(l.Kind), code: expandTabs(codeText(l.Text)),
			spans: spans[i], base: base, emphSty: base,
			palette: newSyntaxPalette(), width: 200})
	}
	// Each row opens with the one column cursor/selection bar, blank here,
	// then the gutter.
	for kind, want := range map[byte]string{
		'+': " + func shiny",
		'-': "  -func old",
		' ': "   package main",
	} {
		got, ok := seen[kind]
		if !ok {
			t.Errorf("no %c line rendered", kind)
			continue
		}
		if !strings.HasPrefix(plainText(got), want) {
			t.Errorf("%c line = %q, want it to start with %q", kind, got, want)
		}
	}
}

// bgAt returns the background colour in force at each rune of a rendered row,
// by replaying the SGR sequences. It is how the band tests check that the
// diff's colour, the emphasis and the syntax foreground all land together.
func bgAt(rendered string) (text []rune, bg []string) {
	cur := ""
	for i := 0; i < len(rendered); {
		if rendered[i] == 0x1b {
			end := strings.IndexByte(rendered[i:], 'm')
			if end < 0 {
				break
			}
			seq := rendered[i : i+end+1]
			switch {
			case strings.Contains(seq, "[0m"):
				cur = ""
			case strings.Contains(seq, "48;5;"):
				n := seq[strings.Index(seq, "48;5;")+5:]
				n = n[:strings.IndexAny(n, ";m")]
				cur = n
			case strings.Contains(seq, "[49m"):
				cur = ""
			}
			i += end + 1
			continue
		}
		r, size := utf8.DecodeRuneInString(rendered[i:])
		text = append(text, r)
		bg = append(bg, cur)
		i += size
	}
	return text, bg
}

// An added line is a green band, with a stronger green over just the
// characters that changed, while the syntax colours stay in the foreground.
func TestChangedCharactersGetAStrongerBand(t *testing.T) {
	raw := `diff --git a/x.go b/x.go
--- a/x.go
+++ b/x.go
@@ -1,2 +1,2 @@
-const prefix = "hello, "
+const prefix = "greetings, "
`
	lines, spans := parseAndHighlight(t, "x.go", raw)
	st := newStyles()

	for _, c := range []struct {
		kind           byte
		band, stronger string
		word           string
	}{
		{'+', "22", "28", "greetings"},
		{'-', "52", "88", "hello"},
	} {
		var row string
		for i, l := range lines {
			if l.Kind != c.kind {
				continue
			}
			base, emph := st.diffAdd, st.diffAddEmph
			if c.kind == '-' {
				base, emph = st.diffDel, st.diffDelEmph
			}
			row = renderDiffRow(diffRow{
				bar: st.body, number: lineNumber(l), numW: 3, numSty: st.dimText,
				gutter: diffGutter(l.Kind), code: expandTabs(codeText(l.Text)),
				spans: spans[i], emph: pairEmphasis(lines, textsOf(lines))[i],
				base: base, emphSty: emph, palette: st.syntax, width: 60,
			})
		}
		if row == "" {
			t.Fatalf("no %c line rendered", c.kind)
		}

		text, bg := bgAt(row)
		joined := string(text)
		// The band covers the code and runs to the margin.
		if !strings.Contains(joined, "const prefix") {
			t.Fatalf("%c row lost its text: %q", c.kind, joined)
		}
		var banded, stronger int
		for i := range text {
			switch bg[i] {
			case c.band:
				banded++
			case c.stronger:
				stronger++
			}
		}
		if banded == 0 {
			t.Errorf("%c line has no %s band", c.kind, c.band)
		}
		if stronger == 0 {
			t.Errorf("%c line has no %s emphasis", c.kind, c.stronger)
		}
		// The changed word, and only it, carries the stronger shade.
		at := strings.Index(joined, c.word)
		if at < 0 {
			t.Fatalf("%c line is missing %q", c.kind, c.word)
		}
		for i := at; i < at+len([]rune(c.word)); i++ {
			if bg[i] != c.stronger {
				t.Errorf("%c line: %q at %d has bg %q, want %q", c.kind, string(text[i]), i, bg[i], c.stronger)
			}
		}
		if bg[at-1] != c.band {
			t.Errorf("%c line: the character before %q has bg %q, want the plain band %q",
				c.kind, c.word, bg[at-1], c.band)
		}
		// Syntax highlighting survives on top of the band.
		if !strings.Contains(row, "38;5;") {
			t.Errorf("%c line lost its syntax foreground", c.kind)
		}
	}
}

// A context line has no band at all, so unchanged code reads as plain text.
func TestContextLinesHaveNoBand(t *testing.T) {
	lines, spans := parseAndHighlight(t, "x.go", goDiff)
	st := newStyles()
	for i, l := range lines {
		if l.Kind != ' ' {
			continue
		}
		row := renderDiffRow(diffRow{
			bar: st.body, number: lineNumber(l), numW: 3, numSty: st.dimText,
			gutter: diffGutter(l.Kind), code: expandTabs(codeText(l.Text)),
			spans: spans[i], base: st.body, emphSty: st.body,
			palette: st.syntax, width: 60,
		})
		if strings.Contains(row, "48;5;") {
			t.Errorf("context line %q has a background: %q", l.Text, row)
		}
		return
	}
}

// textsOf expands every line the way the diff pane does.
func textsOf(lines []git.DiffLine) []string {
	out := make([]string, len(lines))
	for i, l := range lines {
		if isCodeLine(l.Kind) {
			out[i] = expandTabs(codeText(l.Text))
		} else {
			out[i] = l.Text
		}
	}
	return out
}
