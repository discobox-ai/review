package ui

import (
	"fmt"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/mattn/go-runewidth"
)

// pad truncates s to w display cells, padding with spaces when it is shorter.
func pad(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return runewidth.FillRight(runewidth.Truncate(s, w, "…"), w)
}

// truncate shortens s to w display cells without padding. It must only be
// used on plain text; use truncateANSI once styles have been applied.
func truncate(s string, w int) string {
	if w <= 0 {
		return ""
	}
	return runewidth.Truncate(s, w, "…")
}

// truncateANSI shortens a string that already carries styling, measuring
// display cells rather than bytes.
func truncateANSI(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if lipgloss.Width(s) <= w {
		return s
	}
	return ansi.Truncate(s, w, "…")
}

// fitANSI makes a string that already carries styling exactly w cells wide,
// truncating or padding as it has to.
func fitANSI(s string, w int) string {
	if w <= 0 {
		return ""
	}
	switch got := lipgloss.Width(s); {
	case got > w:
		return ansi.Truncate(s, w, "…")
	case got < w:
		return s + strings.Repeat(" ", w-got)
	}
	return s
}

// width reports the display width of plain text.
func width(s string) int { return runewidth.StringWidth(s) }

// renderTitle draws a pane title bar with an optional right aligned suffix.
func renderTitle(style lipgloss.Style, left, right string, width int) string {
	inner := width - 2 // the styles pad one cell on each side
	if inner < 1 {
		inner = 1
	}
	text := left
	if right != "" && runewidth.StringWidth(left)+runewidth.StringWidth(right)+2 <= inner {
		gap := inner - runewidth.StringWidth(left) - runewidth.StringWidth(right)
		text = left + strings.Repeat(" ", gap) + right
	}
	return style.Width(width).Render(truncate(text, inner))
}

func plural(n int, one, many string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, one)
	}
	return fmt.Sprintf("%d %s", n, many)
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

// elideMiddle shortens a path from the middle, keeping both ends. Which file
// it is lives at the end of a path and where in the tree it sits lives at the
// front, so a cut at either end throws one of those away; the part a reader
// can fill in is the run of directories between them.
//
// The cut is made at a slash whenever there is room for the name it stands in
// front of, so what follows the ellipsis is a whole path segment:
//
//	internal/ui/filelist.go  →  inter…/filelist.go
//
// "inter…/filelist.go" reads as a file at the end of a path it does not have
// room to name, where "inter…filelist.go" reads as a name with a hole in the
// middle of it. Only when the name will not fit even on its own do the two
// ends fall back to sharing what there is between them.
func elideMiddle(s string, w int) string {
	if w <= 0 {
		return ""
	}
	if width(s) <= w {
		return s
	}
	if w == 1 {
		return "…"
	}
	runes := []rune(s)

	// Where the tail starts. The slash goes with the name, and w-2 leaves the
	// ellipsis its column and the front at least one of its own.
	tail := len(runes)
	if i := strings.LastIndexByte(s, '/'); i >= 0 && width(s[i:]) <= w-2 {
		tail = len([]rune(s[:i]))
	} else {
		for used, tailW := 0, w/2; tail > 0; tail-- {
			if used += runewidth.RuneWidth(runes[tail-1]); used > tailW {
				break
			}
		}
	}

	// The front takes whatever the tail leaves. It always stops short of the
	// tail: a name that fitted whole was never elided in the first place.
	headW := w - 1 - width(string(runes[tail:]))
	head := 0
	for used := 0; head < tail; head++ {
		if used += runewidth.RuneWidth(runes[head]); used > headW {
			break
		}
	}
	return string(runes[:head]) + "…" + string(runes[tail:])
}
