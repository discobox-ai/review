package ui

import (
	"strings"

	"github.com/obot-platform/difftui/internal/git"
)

// tabWidth is what a tab expands to in the diff pane. Tabs are expanded before
// anything measures, tokenises or highlights a line, so syntax offsets,
// emphasis offsets and padding all count the same columns — and so a tab under
// the cursor does not render as an unstyled gap in the highlight.
const tabWidth = 4

// expandTabs replaces tabs with spaces to the next tab stop.
func expandTabs(text string) string {
	if !strings.Contains(text, "\t") {
		return text
	}
	var out strings.Builder
	column := 0
	for _, r := range text {
		if r == '\t' {
			spaces := tabWidth - column%tabWidth
			out.WriteString(strings.Repeat(" ", spaces))
			column += spaces
			continue
		}
		out.WriteRune(r)
		column++
	}
	return out.String()
}

// emphasis is the rune range of a line that actually changed, when its
// counterpart on the other side makes that knowable.
type emphasis struct {
	start int
	end   int
}

func (e emphasis) empty() bool { return e.end <= e.start }

// maxEmphasisFraction stops the highlight when most of the line changed. At
// that point the whole line is the change, and marking it says nothing.
const maxEmphasisFraction = 0.5

// pairEmphasis finds, for each line, the span within it that differs from its
// counterpart.
//
// A run of removed lines immediately followed by an equally long run of added
// lines is an edit of those lines, in order; anything else is an insertion or a
// deletion, where there is no counterpart and nothing to highlight.
func pairEmphasis(lines []git.DiffLine, texts []string) []emphasis {
	out := make([]emphasis, len(lines))
	for i := 0; i < len(lines); {
		removedStart := i
		for i < len(lines) && lines[i].Kind == '-' {
			i++
		}
		removed := i - removedStart

		addedStart := i
		for i < len(lines) && lines[i].Kind == '+' {
			i++
		}
		added := i - addedStart

		if removed > 0 && removed == added {
			for offset := 0; offset < removed; offset++ {
				before, after := texts[removedStart+offset], texts[addedStart+offset]
				oldSpan, newSpan, ok := changedSpan(before, after)
				if !ok {
					continue
				}
				out[removedStart+offset] = oldSpan
				out[addedStart+offset] = newSpan
			}
		}
		if removed == 0 && added == 0 {
			i++
		}
	}
	return out
}

// changedSpan narrows a pair of lines to the part between their common prefix
// and common suffix.
func changedSpan(before, after string) (emphasis, emphasis, bool) {
	oldRunes, newRunes := []rune(before), []rune(after)
	if len(oldRunes) == 0 || len(newRunes) == 0 {
		return emphasis{}, emphasis{}, false
	}
	prefix := 0
	for prefix < len(oldRunes) && prefix < len(newRunes) && oldRunes[prefix] == newRunes[prefix] {
		prefix++
	}
	suffix := 0
	for suffix < len(oldRunes)-prefix && suffix < len(newRunes)-prefix &&
		oldRunes[len(oldRunes)-1-suffix] == newRunes[len(newRunes)-1-suffix] {
		suffix++
	}
	oldSpan := emphasis{start: prefix, end: len(oldRunes) - suffix}
	newSpan := emphasis{start: prefix, end: len(newRunes) - suffix}
	if oldSpan.empty() && newSpan.empty() {
		return emphasis{}, emphasis{}, false
	}
	oldChanged := float64(oldSpan.end-oldSpan.start) / float64(len(oldRunes))
	newChanged := float64(newSpan.end-newSpan.start) / float64(len(newRunes))
	if oldChanged > maxEmphasisFraction || newChanged > maxEmphasisFraction {
		return emphasis{}, emphasis{}, false
	}
	return oldSpan, newSpan, true
}
