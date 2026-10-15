package ui

import (
	"strings"
	"unicode/utf8"
)

// Text from outside the window is drawn as text, and nothing else.
//
// Everything the window shows that it did not write itself — a diff, a file,
// a commit's subject, a branch name, a remark in the review — came from
// somebody, and a pull request's somebody is anyone at all. A terminal reads
// an escape sequence in that text as an instruction: to color, to clear the
// screen, to link a word to a site it does not name, to write to the
// clipboard. A carriage return or a backspace moves the cursor back over what
// was drawn. So every control character is drawn as the picture Unicode keeps
// for it — ESC as ␛ — one column for one, so that nothing measured in columns
// or lines moves. A tab stays a tab, for expandTabs to deal with.

// printable is one line of text from outside the window, made safe to draw: a
// line break in it is shown, not taken.
func printable(s string) string { return controlPictures(s, false) }

// printableLines is text from outside the window that runs to several lines,
// made safe to draw. Its line breaks are kept, a Windows one as one break.
func printableLines(s string) string {
	return controlPictures(strings.ReplaceAll(s, "\r\n", "\n"), true)
}

// printableCode is a line of a file — from a diff or the file itself — made
// safe to draw. The carriage return a Windows line ends in is not part of what
// the line says, and is dropped rather than shown on every line of the file.
func printableCode(s string) string {
	return controlPictures(strings.TrimSuffix(s, "\r"), false)
}

func controlPictures(s string, lines bool) string {
	if printableAlready(s, lines) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s) + 8)
	for _, r := range s {
		switch {
		case r == '\t', r == '\n' && lines:
			b.WriteRune(r)
		case r < 0x20:
			b.WriteRune(0x2400 + r) // ␀ … ␟
		case r == 0x7f:
			b.WriteRune('␡')
		case r >= 0x80 && r <= 0x9f:
			// The C1 controls, which some terminals take as ESC and a
			// letter: 0x9b is a CSI on its own. They have no pictures.
			b.WriteRune(utf8.RuneError)
		default:
			// A byte that is not UTF-8 arrives here as RuneError and goes
			// out as one, which is also how a terminal would show it —
			// but now it is one character wide by every measure.
			b.WriteRune(r)
		}
	}
	return b.String()
}

// printableAlready reports whether s has nothing in it to change: printable ASCII,
// tabs, line breaks where they are kept, and well formed UTF-8 with no C1
// control in it. Nearly every line of every file is, so it is what is checked
// first.
func printableAlready(s string, lines bool) bool {
	ascii := true
	for i := 0; i < len(s); i++ {
		c := s[i]
		switch {
		case c == '\t', c == '\n' && lines:
		case c < 0x20, c == 0x7f:
			return false
		case c >= 0x80:
			ascii = false
		}
	}
	if ascii {
		return true
	}
	if !utf8.ValidString(s) {
		return false
	}
	for _, r := range s {
		if r >= 0x80 && r <= 0x9f {
			return false
		}
	}
	return true
}
