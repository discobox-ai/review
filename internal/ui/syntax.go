package ui

import (
	"image/color"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/alecthomas/chroma/v2"
	"github.com/alecthomas/chroma/v2/lexers"
	"github.com/discobox-ai/difftui/internal/git"
)

// span is a run of one line that the lexer identified as a single token, in
// rune offsets into the line's code text (the diff's +/-/space marker column
// excluded).
type span struct {
	start int
	end   int
	token chroma.TokenType
}

// lexerFor picks a lexer from the file's name, or nil when nothing matches.
// Nothing matching means no highlighting rather than a fallback lexer, since a
// fallback returns the whole text as one plain token: a tokenise pass that
// produces exactly what not highlighting produces.
func lexerFor(path string) chroma.Lexer {
	lexer := lexers.Match(path)
	if lexer == nil {
		return nil
	}
	// Coalescing merges adjacent tokens of the same type, turning a line of
	// code into a handful of spans instead of dozens.
	return chroma.Coalesce(lexer)
}

// highlightLines tokenises a rendered diff and returns the spans for each line.
//
// Each side of a hunk is lexed as one text — context plus removed lines for the
// old side, context plus added lines for the new one — rather than a line at a
// time. A lexer is a state machine: fed single lines it restarts in the initial
// state on every one, and anything spanning lines (block comments, multi-line
// strings, raw strings) is coloured as though it began where it did not.
// Feeding it a whole side keeps that state right within the hunk, which is as
// much context as a diff has.
func highlightLines(path string, lines []git.DiffLine, texts []string) [][]span {
	out := make([][]span, len(lines))
	lexer := lexerFor(path)
	if lexer == nil {
		return out
	}

	// Group the code lines by the hunk they belong to, keeping file order.
	var order []int
	byHunk := map[int][]int{}
	for i, l := range lines {
		if l.Hunk < 0 || l.Line < 0 || !isCodeLine(l.Kind) {
			continue
		}
		if _, seen := byHunk[l.Hunk]; !seen {
			order = append(order, l.Hunk)
		}
		byHunk[l.Hunk] = append(byHunk[l.Hunk], i)
	}

	for _, h := range order {
		for _, side := range []byte{'-', '+'} {
			var indexes []int
			var sideTexts []string
			for _, i := range byHunk[h] {
				if k := lines[i].Kind; k == ' ' || k == side {
					indexes = append(indexes, i)
					sideTexts = append(sideTexts, texts[i])
				}
			}
			if len(indexes) == 0 {
				continue
			}
			spans := tokeniseLines(lexer, sideTexts)
			for offset, i := range indexes {
				// A context line belongs to both sides and is tokenised
				// twice; the two agree, so the second pass overwrites the
				// first harmlessly.
				out[i] = spans[offset]
			}
		}
	}
	return out
}

// isCodeLine reports whether a diff line carries source rather than metadata.
func isCodeLine(kind byte) bool {
	return kind == ' ' || kind == '+' || kind == '-'
}

// codeText drops the leading +/-/space marker, leaving the source itself.
func codeText(s string) string {
	if s == "" {
		return ""
	}
	return s[1:]
}

// tokeniseLines lexes lines as one text and cuts the tokens back up along the
// line boundaries they came from.
func tokeniseLines(lexer chroma.Lexer, lines []string) [][]span {
	out := make([][]span, len(lines))
	iterator, err := lexer.Tokenise(nil, strings.Join(lines, "\n"))
	if err != nil {
		return out
	}
	line, column := 0, 0
	for _, token := range iterator.Tokens() {
		// A token's value can carry newlines — a block comment is one token —
		// so it is split back into the lines it covers.
		for i, part := range strings.Split(token.Value, "\n") {
			if i > 0 {
				line, column = line+1, 0
			}
			if line >= len(out) {
				return out
			}
			width := len([]rune(part))
			if width > 0 && !isPlainToken(token.Type) {
				out[line] = append(out[line], span{start: column, end: column + width, token: token.Type})
			}
			column += width
		}
	}
	return out
}

// isPlainToken reports whether a token should be left in the pane's own
// foreground. Ordinary text, whitespace and punctuation are most of a line, and
// colouring them is what makes highlighting look noisy.
func isPlainToken(token chroma.TokenType) bool {
	switch token {
	case chroma.Text, chroma.TextWhitespace, chroma.None, chroma.Punctuation, chroma.Error:
		return true
	}
	return false
}

// syntaxPalette is the foreground colour for each token category.
//
// It is a short hand-picked list rather than one of chroma's own styles because
// these colours have to stay legible against the diff's own green and red. A
// style tuned for a plain terminal will happily paint a string green, which
// then lands on top of an added line.
//
// Plain identifiers are deliberately absent, so they render in the pane's own
// foreground: colouring every name as well as every keyword, type, literal and
// comment leaves almost nothing uncoloured, and a line where everything is
// emphasised emphasises nothing.
type syntaxPalette map[chroma.TokenType]color.Color

func newSyntaxPalette(dark bool) syntaxPalette {
	pick := lipgloss.LightDark(dark)
	adaptive := map[chroma.TokenType][2]string{
		// token:              {dark, light}
		chroma.Keyword:          {"141", "90"}, // purple, clear of both bands
		chroma.NameFunction:     {"111", "26"}, // blue
		chroma.NameClass:        {"117", "30"},
		chroma.NameBuiltin:      {"117", "30"},
		chroma.KeywordType:      {"117", "30"},
		chroma.LiteralString:    {"180", "94"}, // tan, never green
		chroma.LiteralStringDoc: {"180", "94"},
		chroma.LiteralNumber:    {"116", "24"},
		chroma.Comment:          {"245", "244"}, // grey, deliberately quiet
		chroma.CommentPreproc:   {"245", "244"},
		chroma.NameAttribute:    {"116", "24"},
		chroma.NameTag:          {"111", "26"},
		chroma.GenericHeading:   {"111", "26"},
		chroma.GenericStrong:    {"141", "90"},
	}
	palette := make(syntaxPalette, len(adaptive))
	for token, pair := range adaptive {
		palette[token] = pick(lipgloss.Color(pair[1]), lipgloss.Color(pair[0]))
	}
	return palette
}

// color resolves a token to its colour, walking up chroma's token hierarchy so
// that every subtype of a category the palette does not name explicitly —
// LiteralStringHeredoc under LiteralString, CommentMultiline under Comment —
// still picks up that category's colour.
func (p syntaxPalette) color(token chroma.TokenType) (color.Color, bool) {
	for _, candidate := range []chroma.TokenType{token, token.SubCategory(), token.Category()} {
		if value, ok := p[candidate]; ok {
			return value, true
		}
	}
	return nil, false
}
