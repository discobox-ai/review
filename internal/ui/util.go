package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/lipgloss"
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
