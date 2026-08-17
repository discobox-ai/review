package git

import (
	"fmt"
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
	Preamble []string // "diff --git", "index", "---", "+++" and friends
	Hunks    []Hunk
}

// DiffLine is one rendered line of the diff viewer along with enough context
// to stage or revert it.
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
}

// ParsePatch splits a unified diff into its preamble and hunks.
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
	return p
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
// file with no hunks (mode changes, binary files) is kept so the pane is not
// blank.
func (p *Patch) Render() []DiffLine {
	var out []DiffLine
	if len(p.Hunks) == 0 {
		for _, l := range p.Preamble {
			out = append(out, DiffLine{Text: l, Kind: 'h', Hunk: -1, Line: -1})
		}
		return out
	}
	for _, l := range p.Preamble {
		if strings.HasPrefix(l, "diff ") || strings.HasPrefix(l, "index ") ||
			strings.HasPrefix(l, "--- ") || strings.HasPrefix(l, "+++ ") {
			continue
		}
		if strings.TrimSpace(l) == "" {
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
				d.Old, d.New = oldNo, newNo
				oldNo++
				newNo++
			case '-':
				d.Old = oldNo
				oldNo++
			case '+':
				d.New = newNo
				newNo++
			}
			out = append(out, d)
		}
	}
	return out
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
				// A deletion at the very end of the file has no line after it;
				// the seam clamps to the last line there is.
				changed[max(newNo, 1)] = true
			}
		}
	}
	return changed
}

// fileHeader rebuilds the minimal preamble that git apply needs.
func (p *Patch) fileHeader() []string {
	var out []string
	for _, l := range p.Preamble {
		switch {
		case strings.HasPrefix(l, "diff --git "),
			strings.HasPrefix(l, "index "),
			strings.HasPrefix(l, "old mode "),
			strings.HasPrefix(l, "new mode "),
			strings.HasPrefix(l, "new file mode "),
			strings.HasPrefix(l, "deleted file mode "),
			strings.HasPrefix(l, "rename from "),
			strings.HasPrefix(l, "rename to "),
			strings.HasPrefix(l, "--- "),
			strings.HasPrefix(l, "+++ "):
			out = append(out, l)
		}
	}
	return out
}

// HunkPatch builds a patch that contains only the given hunk.
func (p *Patch) HunkPatch(idx int) (string, bool) {
	if idx < 0 || idx >= len(p.Hunks) {
		return "", false
	}
	h := p.Hunks[idx]
	var b strings.Builder
	for _, l := range p.fileHeader() {
		b.WriteString(l + "\n")
	}
	b.WriteString(h.Header + "\n")
	for _, l := range h.Lines {
		b.WriteString(l + "\n")
	}
	return b.String(), true
}

// LinePatch builds a patch containing only the selected body lines of a hunk,
// which is how git-gui stages or unstages individual lines.
//
// When reverse is false the patch is applied forwards: unselected additions
// are dropped and unselected deletions become context. When reverse is true
// the patch will be applied with -R, so the two roles swap.
func (p *Patch) LinePatch(idx int, selected map[int]bool, reverse bool) (string, bool) {
	if idx < 0 || idx >= len(p.Hunks) {
		return "", false
	}
	h := p.Hunks[idx]

	var body []string
	oldCount, newCount := 0, 0
	changed := false
	for i, l := range h.Lines {
		if l == "" {
			continue
		}
		switch l[0] {
		case '\\':
			// "\ No newline at end of file" follows whatever line precedes it.
			if len(body) > 0 {
				body = append(body, l)
			}
			continue
		case ' ':
			body = append(body, l)
			oldCount++
			newCount++
			continue
		}
		keep := selected[i]
		if keep {
			changed = true
			body = append(body, l)
			if l[0] == '-' {
				oldCount++
			} else {
				newCount++
			}
			continue
		}
		// Not selected: either drop the line or demote it to context.
		drop := l[0] == '+'
		if reverse {
			drop = l[0] == '-'
		}
		if drop {
			continue
		}
		body = append(body, " "+l[1:])
		oldCount++
		newCount++
	}
	if !changed {
		return "", false
	}

	var b strings.Builder
	for _, l := range p.fileHeader() {
		b.WriteString(l + "\n")
	}
	fmt.Fprintf(&b, "@@ -%s +%s @@\n",
		rangeSpec(h.OldStart, oldCount), rangeSpec(h.NewStart, newCount))
	for _, l := range body {
		b.WriteString(l + "\n")
	}
	return b.String(), true
}

func rangeSpec(start, count int) string {
	if count == 0 {
		// An empty side is anchored one line earlier by convention.
		if start > 0 {
			start--
		}
		return fmt.Sprintf("%d,0", start)
	}
	return fmt.Sprintf("%d,%d", start, count)
}
