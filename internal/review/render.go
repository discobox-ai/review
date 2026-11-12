package review

import (
	"bytes"
	"fmt"
	"slices"
	"strings"

	"github.com/discobox-ai/review/internal/git"
	"github.com/discobox-ai/review/pkg/source"
)

// Compared binds display hunks and their run IDs to one complete content
// pair. A nil Patch means there is no change, not unavailable contents.
type Compared struct {
	Patch    *git.Patch
	Identity Identity
	Hunks    [][]string
	raw      string
}

type canonicalLine struct {
	kind     byte
	text     string
	old, new int // zero-based offsets before this operation
}

func (c comparison) render(context int) (Compared, error) {
	if c.change.Problem != "" {
		return Compared{}, fmt.Errorf("content verification unavailable: %s", c.change.Problem)
	}
	old, newSide := c.change.Old, c.change.New
	if old.Path == newSide.Path && old.Mode == newSide.Mode && old.Present == newSide.Present && bytes.Equal(old.Data, newSide.Data) {
		return Compared{}, nil
	}
	p := &git.Patch{Path: newSide.Path}
	if !newSide.Present {
		p.Path = old.Path
	}
	if old.Present && newSide.Present && old.Path != newSide.Path {
		p.From = old.Path
	}
	a, b := old.Path, newSide.Path
	if !old.Present {
		a = b
	}
	if !newSide.Present {
		b = a
	}
	p.Preamble = append(p.Preamble, "diff --git "+git.QuotePath("a/"+a)+" "+git.QuotePath("b/"+b))
	switch {
	case !old.Present:
		p.Preamble = append(p.Preamble, "new file mode "+newSide.Mode)
	case !newSide.Present:
		p.Preamble = append(p.Preamble, "deleted file mode "+old.Mode)
	case old.Mode != newSide.Mode:
		p.Preamble = append(p.Preamble, "old mode "+old.Mode, "new mode "+newSide.Mode)
	}
	if p.From != "" {
		if bytes.Equal(old.Data, newSide.Data) {
			p.Preamble = append(p.Preamble, "similarity index 100%")
		}
		p.Preamble = append(p.Preamble, "rename from "+git.QuotePath(old.Path), "rename to "+git.QuotePath(newSide.Path))
	}
	out := Compared{Patch: p, Identity: c.identity()}
	if old.Present && newSide.Present && old.Mode != newSide.Mode && (old.Mode == "120000" || newSide.Mode == "120000" || old.Mode == "160000" || newSide.Mode == "160000") {
		// Git applies a kind replacement as a deletion followed by a creation.
		// The review still has one entry and one canonical identity for the pair.
		out.raw = kindReplacement(old, newSide)
	}
	if len(c.runs) == 0 && bytes.Equal(old.Data, newSide.Data) {
		return out, nil
	}
	if old.Mode == "160000" || newSide.Mode == "160000" || bytes.IndexByte(old.Data, 0) >= 0 || bytes.IndexByte(newSide.Data, 0) >= 0 {
		p.Preamble = append(p.Preamble, "Binary files differ")
		return out, nil
	}
	aName, bName := git.QuotePath("a/"+a), git.QuotePath("b/"+b)
	if !old.Present {
		aName = "/dev/null"
	}
	if !newSide.Present {
		bName = "/dev/null"
	}
	p.Preamble = append(p.Preamble, "--- "+aName, "+++ "+bName)
	// A line's terminator is part of its token. SplitAfter's trailing empty
	// piece is not another line, and an empty file has no tokens at all.
	tokens := func(data []byte) []string {
		lines := strings.SplitAfter(string(data), "\n")
		if lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		return lines
	}
	oldLines, newLines := tokens(old.Data), tokens(newSide.Data)
	var lines []canonicalLine
	type interval struct{ start, end, firstRun, lastRun int }
	var intervals []interval
	oi, ni := 0, 0
	equalTo := func(oldEnd, newEnd int) error {
		if oldEnd < oi || newEnd < ni || oldEnd > len(oldLines) || newEnd > len(newLines) || oldEnd-oi != newEnd-ni {
			return fmt.Errorf("invalid canonical run coordinates")
		}
		for oi < oldEnd {
			if oldLines[oi] != newLines[ni] {
				return fmt.Errorf("canonical comparison omitted a change")
			}
			lines = append(lines, canonicalLine{kind: ' ', text: oldLines[oi], old: oi, new: ni})
			oi++
			ni++
		}
		return nil
	}
	for ri, run := range c.runs {
		if len(run.removed)+len(run.added) == 0 {
			return Compared{}, fmt.Errorf("empty canonical run")
		}
		if err := equalTo(run.oldStart, run.newStart); err != nil {
			return Compared{}, err
		}
		if oi+len(run.removed) > len(oldLines) || ni+len(run.added) > len(newLines) || !slices.Equal(oldLines[oi:oi+len(run.removed)], run.removed) || !slices.Equal(newLines[ni:ni+len(run.added)], run.added) {
			return Compared{}, fmt.Errorf("canonical run bytes do not match contents")
		}
		start := len(lines)
		for _, text := range run.removed {
			lines = append(lines, canonicalLine{kind: '-', text: text, old: oi, new: ni})
			oi++
		}
		for _, text := range run.added {
			lines = append(lines, canonicalLine{kind: '+', text: text, old: oi, new: ni})
			ni++
		}
		intervals = append(intervals, interval{start: start, end: len(lines), firstRun: ri, lastRun: ri + 1})
	}
	if err := equalTo(len(oldLines), len(newLines)); err != nil {
		return Compared{}, err
	}
	context = min(max(context, 0), len(lines))
	merged := make([]interval, 0, len(intervals))
	for _, span := range intervals {
		span.start = max(0, span.start-context)
		span.end = min(len(lines), span.end+context)
		if len(merged) > 0 && span.start <= merged[len(merged)-1].end {
			last := &merged[len(merged)-1]
			last.end = max(last.end, span.end)
			last.lastRun = span.lastRun
		} else {
			merged = append(merged, span)
		}
	}
	for _, span := range merged {
		body := lines[span.start:span.end]
		oldCount, newCount := 0, 0
		for _, line := range body {
			if line.kind != '+' {
				oldCount++
			}
			if line.kind != '-' {
				newCount++
			}
		}
		oldStart, newStart := body[0].old, body[0].new
		if oldCount > 0 {
			oldStart++
		}
		if newCount > 0 {
			newStart++
		}
		hunk := git.Hunk{OldStart: oldStart, NewStart: newStart, Header: fmt.Sprintf("@@ -%d,%d +%d,%d @@", oldStart, oldCount, newStart, newCount)}
		for _, line := range body {
			hunk.Lines = append(hunk.Lines, string(line.kind)+strings.TrimSuffix(line.text, "\n"))
			if !strings.HasSuffix(line.text, "\n") {
				hunk.Lines = append(hunk.Lines, `\ No newline at end of file`)
			}
		}
		p.Hunks = append(p.Hunks, hunk)
		out.Hunks = append(out.Hunks, slices.Clone(out.Identity.Runs[span.firstRun:span.lastRun]))
	}
	return out, nil
}

// Unified serializes the verified change for Git. Kind replacements require
// separate delete/create records; the display remains one approval scope.
func (c Compared) Unified() string {
	if c.raw != "" {
		return c.raw
	}
	if c.Patch == nil {
		return ""
	}
	return c.Patch.Unified()
}

func kindReplacement(old, newSide source.Side) string {
	var out strings.Builder
	for _, deleting := range []bool{true, false} {
		side := newSide
		if deleting {
			side = old
		}
		a, b := git.QuotePath("a/"+side.Path), git.QuotePath("b/"+side.Path)
		fmt.Fprintf(&out, "diff --git %s %s\n", a, b)
		operation, from, to, kind := "new", "/dev/null", b, byte('+')
		if deleting {
			operation, from, to, kind = "deleted", a, "/dev/null", '-'
		}
		fmt.Fprintf(&out, "%s file mode %s\n", operation, side.Mode)
		if side.Mode == "160000" {
			side.Data = []byte("Subproject commit " + string(side.Data) + "\n")
		}
		if bytes.IndexByte(side.Data, 0) >= 0 {
			out.WriteString("Binary files differ\n")
			continue
		}
		if len(side.Data) == 0 {
			continue
		}
		fmt.Fprintf(&out, "--- %s\n+++ %s\n", from, to)
		lines := strings.SplitAfter(string(side.Data), "\n")
		if lines[len(lines)-1] == "" {
			lines = lines[:len(lines)-1]
		}
		if deleting {
			fmt.Fprintf(&out, "@@ -1,%d +0,0 @@\n", len(lines))
		} else {
			fmt.Fprintf(&out, "@@ -0,0 +1,%d @@\n", len(lines))
		}
		for _, line := range lines {
			out.WriteByte(kind)
			out.WriteString(line)
			if !strings.HasSuffix(line, "\n") {
				out.WriteString("\n\\ No newline at end of file\n")
			}
		}
	}
	return out.String()
}
