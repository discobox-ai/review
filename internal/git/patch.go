package git

import (
	"strconv"
	"strings"
)

// Hunk is a single @@ block of a unified diff.
type Hunk struct {
	Header   string   // the @@ -a,b +c,d @@ line
	OldStart int      // first line number on the "a" side
	NewStart int      // first line number on the "b" side
	Lines    []string // body lines, including their leading ' ', '+', '-' or '\'
}

// Patch is a parsed unified diff for a single file.
type Patch struct {
	Path     string   // the file as the new side names it
	From     string   // the old side's name, when a rename made them differ
	Preamble []string // "diff --git", "index", "---", "+++" and friends
	Hunks    []Hunk
}

// DiffLine is one rendered line of the diff viewer.
type DiffLine struct {
	Text string
	Kind byte // ' ', '+', '-', '\\', '@' for hunk headers, 'h' for the preamble
	Hunk int  // index into Patch.Hunks, or -1
	Line int  // index into Hunk.Lines, or -1

	// Old and New are the line's number on each side of the diff, counted
	// from the hunk header. A line that exists on only one side has 0 for the
	// other, and metadata lines have neither.
	Old int
	New int

	// Seam is where the line sits in the new side's numbering whether or not
	// it exists there: New for a line that does, and for a removed line the
	// number of the line now standing where it stood. It is what lets a
	// deletion be pointed at in the coordinates of the file as it is now,
	// which is the space comments are anchored in.
	Seam int
}

// ParsePatch splits a unified diff for one file into its preamble and hunks.
func ParsePatch(text string) *Patch {
	p := &Patch{}
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	cur := -1
	for _, l := range lines {
		if strings.HasPrefix(l, "@@") {
			old, new := parseHunkHeader(l)
			p.Hunks = append(p.Hunks, Hunk{Header: l, OldStart: old, NewStart: new})
			cur = len(p.Hunks) - 1
			continue
		}
		if cur < 0 {
			p.Preamble = append(p.Preamble, l)
			continue
		}
		p.Hunks[cur].Lines = append(p.Hunks[cur].Lines, l)
	}
	p.Path, p.From = patchPaths(p.Preamble)
	return p
}

// ParseMulti splits the output of a diff covering many files into one patch
// each, in the order git printed them.
//
// This is what makes the review a single document rather than a file at a
// time: one "git diff" against the base is asked for, and the answer is cut
// back up here rather than by running git once per file.
func ParseMulti(text string) []*Patch {
	text = strings.TrimSuffix(text, "\n")
	if strings.TrimSpace(text) == "" {
		return nil
	}
	lines := strings.Split(text, "\n")

	var out []*Patch
	start := -1
	for i, l := range lines {
		if !strings.HasPrefix(l, "diff --git ") && !strings.HasPrefix(l, "diff --no-index ") {
			continue
		}
		if start >= 0 {
			out = append(out, ParsePatch(strings.Join(lines[start:i], "\n")))
		}
		start = i
	}
	if start >= 0 {
		out = append(out, ParsePatch(strings.Join(lines[start:], "\n")))
		return out
	}
	// No file header at all: a diff of one file taken with --no-prefix, or
	// something that is not a diff. Either way it is one patch.
	return []*Patch{ParsePatch(text)}
}

// patchPaths reads the file's name off the preamble.
//
// The "+++ b/name" and "--- a/name" lines are preferred over "diff --git"
// because that line runs both names together with nothing but a space between
// them, which is ambiguous the moment a name contains one. A pure deletion has
// no new side, so it falls back to the old one, and a rename is the one case
// where both are worth keeping.
func patchPaths(preamble []string) (path, from string) {
	var oldSide, newSide string
	for _, l := range preamble {
		switch {
		case strings.HasPrefix(l, "--- "):
			oldSide = stripPrefix(unquotePath(strings.TrimPrefix(l, "--- ")), "a/")
		case strings.HasPrefix(l, "+++ "):
			newSide = stripPrefix(unquotePath(strings.TrimPrefix(l, "+++ ")), "b/")
		case strings.HasPrefix(l, "rename from "):
			from = unquotePath(strings.TrimPrefix(l, "rename from "))
		case strings.HasPrefix(l, "rename to "):
			path = unquotePath(strings.TrimPrefix(l, "rename to "))
		}
	}
	if path == "" {
		path = newSide
		if path == "" || path == "/dev/null" {
			path = oldSide
		}
	}
	if from == "" && oldSide != "" && oldSide != "/dev/null" && oldSide != path {
		from = oldSide
	}
	if path == "/dev/null" {
		path = ""
	}
	if path == "" {
		// Nothing but a "diff --git a/x b/x" line: a mode change, or a binary
		// file git described without a body. Take the half after " b/", which
		// is right for every name that does not contain that sequence.
		for _, l := range preamble {
			if !strings.HasPrefix(l, "diff --git ") {
				continue
			}
			body := strings.TrimPrefix(l, "diff --git ")
			if i := strings.LastIndex(body, " b/"); i >= 0 {
				path = unquotePath(body[i+3:])
				from = stripPrefix(unquotePath(body[:i]), "a/")
				if from == path {
					from = ""
				}
			}
		}
	}
	return path, from
}

func stripPrefix(s, prefix string) string {
	if s == "/dev/null" {
		return s
	}
	return strings.TrimPrefix(s, prefix)
}

// unquotePath undoes the C style quoting git applies to a path holding a
// space, a quote or a byte outside ASCII.
func unquotePath(s string) string {
	s = strings.TrimSpace(s)
	if len(s) < 2 || s[0] != '"' || s[len(s)-1] != '"' {
		return s
	}
	if out, err := strconv.Unquote(s); err == nil {
		return out
	}
	return s
}

func parseHunkHeader(h string) (oldStart, newStart int) {
	// @@ -12,7 +12,9 @@ optional section heading
	body := h
	if i := strings.Index(body[2:], "@@"); i >= 0 {
		body = body[:i+4]
	}
	fields := strings.Fields(strings.Trim(body, "@ "))
	for _, f := range fields {
		if len(f) < 2 {
			continue
		}
		num := f[1:]
		if i := strings.IndexByte(num, ','); i >= 0 {
			num = num[:i]
		}
		n, err := strconv.Atoi(num)
		if err != nil {
			continue
		}
		switch f[0] {
		case '-':
			oldStart = n
		case '+':
			newStart = n
		}
	}
	return
}

// Render flattens the patch back into displayable lines with metadata. The
// preamble is dropped the way git-gui hides it, except that the header of a
// file with no hunks (mode changes, binary files) is kept so the section is
// not blank.
func (p *Patch) Render() []DiffLine {
	var out []DiffLine
	if len(p.Hunks) == 0 {
		for _, l := range p.Preamble {
			if isNoiseHeader(l) {
				continue
			}
			out = append(out, DiffLine{Text: l, Kind: 'h', Hunk: -1, Line: -1})
		}
		return out
	}
	for _, l := range p.Preamble {
		if isNoiseHeader(l) || strings.TrimSpace(l) == "" {
			continue
		}
		out = append(out, DiffLine{Text: l, Kind: 'h', Hunk: -1, Line: -1})
	}
	for hi, h := range p.Hunks {
		out = append(out, DiffLine{Text: h.Header, Kind: '@', Hunk: hi, Line: -1})
		// Line numbers run from the hunk header: the old side counts context
		// and removed lines, the new side context and added lines.
		oldNo, newNo := h.OldStart, h.NewStart
		for li, l := range h.Lines {
			kind := byte(' ')
			if l != "" {
				kind = l[0]
			}
			d := DiffLine{Text: l, Kind: kind, Hunk: hi, Line: li}
			switch kind {
			case ' ':
				d.Old, d.New, d.Seam = oldNo, newNo, newNo
				oldNo++
				newNo++
			case '-':
				d.Old = oldNo
				// A deletion at the very end of a file has no line after it,
				// so the seam clamps to the last line there is.
				d.Seam = max(newNo, 1)
				oldNo++
			case '+':
				d.New, d.Seam = newNo, newNo
				newNo++
			}
			out = append(out, d)
		}
	}
	return out
}

// isNoiseHeader reports whether a preamble line is machinery rather than
// something a reader wants: the paths are already in the section title and the
// blob hashes say nothing at all.
func isNoiseHeader(l string) bool {
	return strings.HasPrefix(l, "diff ") || strings.HasPrefix(l, "index ") ||
		strings.HasPrefix(l, "--- ") || strings.HasPrefix(l, "+++ ")
}

// Stat counts the lines the patch adds and removes, which is what a file's row
// in the list reports.
func (p *Patch) Stat() (adds, dels int) {
	for _, h := range p.Hunks {
		for _, l := range h.Lines {
			if l == "" {
				continue
			}
			switch l[0] {
			case '+':
				adds++
			case '-':
				dels++
			}
		}
	}
	return adds, dels
}

// ChangedNewLines reports which line numbers on the new side of the diff were
// touched. An added line marks its own number. A removed line has no number on
// the new side, so it marks the line now standing where it stood — the seam is
// the one place a reader of the new text can be pointed at.
func (p *Patch) ChangedNewLines() map[int]bool {
	changed := map[int]bool{}
	for _, h := range p.Hunks {
		newNo := h.NewStart
		for _, l := range h.Lines {
			kind := byte(' ')
			if l != "" {
				kind = l[0]
			}
			switch kind {
			case ' ':
				newNo++
			case '+':
				changed[newNo] = true
				newNo++
			case '-':
				changed[max(newNo, 1)] = true
			}
		}
	}
	return changed
}

// ---------------------------------------------------------------------------
// Carrying a line number across a diff
// ---------------------------------------------------------------------------

// MapOldToNew carries a line number from the old side of the patch to the new
// one, reporting false when the line does not survive — it was removed, or
// rewritten, and so has no counterpart to point at.
//
// This is how a comment written against one commit is re-anchored onto the
// working tree, and how one anchored to the working tree is found again inside
// a commit. Everything outside the hunks moves by the running offset, and
// everything inside one is looked up line by line, since only context lines
// exist on both sides.
func (p *Patch) MapOldToNew(old int) (int, bool) { return p.mapLine(old, false) }

// MapNewToOld is MapOldToNew in the other direction.
func (p *Patch) MapNewToOld(new int) (int, bool) { return p.mapLine(new, true) }

func (p *Patch) mapLine(n int, reverse bool) (int, bool) {
	if n <= 0 {
		return 0, false
	}
	offset := 0
	for _, h := range p.Hunks {
		from, to := h.OldStart, h.NewStart
		fromKind, toKind := byte('-'), byte('+')
		if reverse {
			from, to = h.NewStart, h.OldStart
			fromKind, toKind = '+', '-'
		}

		// A hunk with nothing at all on the side being mapped from is a pure
		// insertion into it, and git anchors those one line early: the header
		// names the line the insertion goes after rather than a line the hunk
		// contains. So the line named by the header is still before the hunk,
		// where every other hunk's first line is inside it.
		if hunkSide(h, fromKind) == 0 {
			if n <= from {
				return n + offset, true
			}
		} else if n < from {
			return n + offset, true
		}

		fromNo, toNo := from, to
		for _, l := range h.Lines {
			kind := byte(' ')
			if l != "" {
				kind = l[0]
			}
			switch kind {
			case ' ':
				if fromNo == n {
					return toNo, true
				}
				fromNo++
				toNo++
			case fromKind:
				if fromNo == n {
					// The line the caller asked about is one this diff took
					// away: there is nothing on the other side to point at.
					return 0, false
				}
				fromNo++
			case toKind:
				toNo++
			}
		}
		// How far the two sides drifted apart over this hunk, which is what
		// every line after it moves by. Counting what the body consumed rather
		// than subtracting the two running numbers is what keeps the arithmetic
		// right when one side of the hunk is empty.
		offset += (toNo - to) - (fromNo - from)
	}
	return n + offset, true
}

// hunkSide counts the lines a hunk has on one side: context plus the lines
// carrying kind.
func hunkSide(h Hunk, kind byte) int {
	n := 0
	for _, l := range h.Lines {
		if l == "" {
			continue
		}
		if l[0] == ' ' || l[0] == kind {
			n++
		}
	}
	return n
}
