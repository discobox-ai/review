package ui

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/glamour"
	glamourstyles "github.com/charmbracelet/glamour/styles"
)

// mdView is the full screen markdown reader that m opens. It holds the source
// text as well as the rendered lines, because rendering is a function of the
// window width: a resize re-renders rather than re-wrapping styled text.
//
// When the file has uncommitted or in-commit changes, the reader carries them
// as a set of touched source lines and marks the rendered text that came from
// them. Rendered lines do not map one to one onto source lines — that is the
// whole point of rendering — so the source is rendered a block at a time and
// the mark lands on whole blocks: "this paragraph changed" is the honest
// granularity, and the one a reader wants anyway.
type mdView struct {
	title      string
	raw        string
	changedSrc map[int]bool // 1-based source lines the diff touched

	lines []string
	// block is parallel to lines: which touched block each rendered line came
	// from, as an index into jumps, or -1 for an untouched one.
	block  []int
	jumps  []int // first rendered line of each touched block, ascending
	cur    int   // the block n/p last landed on, an index into jumps, or -1
	offset int

	width, height int
}

// mdWrapWidth caps the rendered text at a readable measure, the way glow
// does; a full screen of 200-column prose is not a reader.
const mdWrapWidth = 100

// mdGutter is the marker column, present only when there is anything to mark.
const mdGutter = 2

// mdLead is how many lines above a changed block n/p leave on screen, so the
// change arrives with a little of what came before it rather than flush
// against the title bar.
const mdLead = 3

func newMdView(title, raw string, changedSrc map[int]bool, width, height int) *mdView {
	v := &mdView{title: title, raw: raw, changedSrc: changedSrc, cur: -1}
	v.resize(width, height)
	return v
}

// mdBlock is a run of source lines that renders on its own: a paragraph, a
// heading, a fenced code block. start and end are 1-based and inclusive.
type mdBlock struct {
	start, end int
	text       string
}

// mdBlocks splits markdown source into blocks at blank lines, keeping a
// fenced code block together across the blank lines inside it. Constructs
// that reach across a blank line — a reference link's definition, a list
// item's indented continuation — render less faithfully than a whole-document
// pass would; that is the price of knowing where each rendered line came
// from, and it is paid only at the seams.
func mdBlocks(src string) []mdBlock {
	lines := strings.Split(strings.TrimRight(src, "\n"), "\n")
	var out []mdBlock
	cur := -1 // 0-based first line of the block being gathered, -1 for none
	fence := ""
	for i, l := range lines {
		t := strings.TrimSpace(l)
		if fence != "" {
			if strings.HasPrefix(t, fence) {
				fence = ""
			}
			continue
		}
		if t == "" {
			if cur >= 0 {
				out = append(out, mdBlock{start: cur + 1, end: i, text: strings.Join(lines[cur:i], "\n")})
				cur = -1
			}
			continue
		}
		if cur < 0 {
			cur = i
		}
		if strings.HasPrefix(t, "```") || strings.HasPrefix(t, "~~~") {
			fence = t[:3]
		}
	}
	if cur >= 0 {
		out = append(out, mdBlock{start: cur + 1, end: len(lines), text: strings.Join(lines[cur:], "\n")})
	}
	return out
}

// resize renders the markdown for a window size. The dark style is chosen
// outright rather than auto-detected: detection asks the terminal, and the
// terminal belongs to bubbletea while the program runs.
func (v *mdView) resize(width, height int) {
	v.width, v.height = width, height
	room := width - 2
	if len(v.changedSrc) > 0 {
		room -= mdGutter
	}
	wrap := min(max(room, 20), mdWrapWidth)
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle(glamourstyles.DarkStyle),
		glamour.WithWordWrap(wrap),
	)

	blocks := mdBlocks(v.raw)
	v.lines, v.block, v.jumps = nil, nil, nil
	for i, b := range blocks {
		// A blank line between blocks belongs to neither; for marking, each
		// block reaches to the start of the next, so a change landing in the
		// gap — a deleted paragraph's seam — still gets pointed at.
		reach := 1 << 30
		if i+1 < len(blocks) {
			reach = blocks[i+1].start - 1
		}
		hit := false
		for l := b.start; l <= reach && !hit; l++ {
			hit = v.changedSrc[l]
		}

		out := b.text
		if err == nil {
			if rendered, rerr := r.Render(b.text); rerr == nil {
				out = strings.Trim(rendered, "\n")
			}
		}
		if i > 0 {
			v.lines = append(v.lines, "")
			v.block = append(v.block, -1)
		}
		id := -1
		if hit {
			id = len(v.jumps)
			v.jumps = append(v.jumps, len(v.lines))
		}
		for _, l := range strings.Split(out, "\n") {
			v.lines = append(v.lines, l)
			v.block = append(v.block, id)
		}
	}
	if len(v.lines) == 0 {
		v.lines, v.block = []string{""}, []int{-1}
	}
	// A resize re-renders from scratch; the block being looked at survives it
	// as long as there still is one.
	if v.cur >= len(v.jumps) {
		v.cur = len(v.jumps) - 1
	}
	v.clamp()
}

// body is the rows left for text once the title and the hint line are taken.
func (v *mdView) body() int { return max(v.height-2, 1) }

func (v *mdView) move(delta int) {
	v.offset += delta
	v.clamp()
}

func (v *mdView) moveTo(offset int) {
	v.offset = offset
	v.clamp()
}

// jump moves to the next or previous changed block and scrolls so it sits a
// few lines down from the top, with what came before it still in view. The
// block becomes the current one, which the gutter picks out from the rest.
// It runs off either end quietly, like the hunk keys do.
//
// Before the first press there is no current block, so n goes to the first
// block below the top of the screen and p to the last one above it, the way
// the hunk keys read the cursor. After that the two step through the list.
func (v *mdView) jump(delta int) {
	if len(v.jumps) == 0 {
		return
	}
	next := v.cur + delta
	if v.cur < 0 {
		next = -1
		if delta > 0 {
			for i, j := range v.jumps {
				if j > v.offset {
					next = i
					break
				}
			}
		} else {
			for i := len(v.jumps) - 1; i >= 0; i-- {
				if v.jumps[i] < v.offset {
					next = i
					break
				}
			}
		}
	}
	if next < 0 || next >= len(v.jumps) {
		return
	}
	v.cur = next
	v.moveTo(v.jumps[next] - mdLead)
}

func (v *mdView) clamp() {
	v.offset = min(v.offset, max(len(v.lines)-v.body(), 0))
	v.offset = max(v.offset, 0)
}

func (v *mdView) view(st *styles) string {
	body := v.body()
	end := min(v.offset+body, len(v.lines))
	right := fmt.Sprintf("%d–%d of %d", v.offset+1, end, len(v.lines))
	if n := len(v.jumps); n > 0 {
		if v.cur >= 0 {
			right = fmt.Sprintf("change %d of %d · %s", v.cur+1, n, right)
		} else {
			right = plural(n, "changed block", "changed blocks") + " · " + right
		}
	}
	marks := len(v.jumps) > 0
	textW := v.width
	if marks {
		textW -= mdGutter
	}
	var b strings.Builder
	b.WriteString(renderTitle(st.titleDiff, v.title, right, v.width))
	b.WriteString("\n")
	for i := v.offset; i < end; i++ {
		if marks {
			// The block n/p landed on is picked out from the rest, so with
			// several changes on screen it is clear which one is "this one".
			switch {
			case v.block[i] < 0:
				b.WriteString("  ")
			case v.block[i] == v.cur:
				b.WriteString(st.mdMarkCur.Render("▌") + " ")
			default:
				b.WriteString(st.mdMark.Render("▍") + " ")
			}
		}
		b.WriteString(truncateANSI(v.lines[i], textW))
		b.WriteString("\n")
	}
	for i := end - v.offset; i < body; i++ {
		b.WriteString("\n")
	}
	hint := "↑/↓ scroll · PgUp/PgDn · g/G top/bottom · Esc or q close"
	if marks {
		hint = "n/p changed block · " + hint
	}
	b.WriteString(st.dimText.Render(hint))
	return b.String()
}
