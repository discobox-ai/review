package ui

import (
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"

	"github.com/discobox-ai/review/internal/review"
)

// The review card in the stack. Its summary goes above the files, and the
// files fall into two groups: the core — those the card names a place
// in, with only the hunks those places fall in open — and the rest. a works
// through the core's hunks before anything else.

// cardBanner is what the card puts into the stack.
type cardBanner struct {
	summary  []string
	note     string // what the window says about the card's base
	core     int    // how many of the files, from the first, are the core
	coreHead string
	restHead string
}

// rows are the summary's lines, wrapped to the pane.
func (b *cardBanner) rows(width int) []reviewRow {
	if len(b.summary) == 0 && b.note == "" {
		return nil
	}
	rows := []reviewRow{{kind: rowBanner, file: -1, line: -1, first: true, text: " WHAT CHANGED"}}
	for _, s := range b.summary {
		for i, l := range wrapText(printable(oneLine(s)), max(width-6, 10)) {
			lead := "   • "
			if i > 0 {
				lead = "     "
			}
			rows = append(rows, reviewRow{kind: rowBanner, file: -1, line: -1, text: lead + l})
		}
	}
	if b.note != "" {
		rows = append(rows, reviewRow{kind: rowBanner, file: -1, line: -1, text: "   " + b.note})
	}
	return append(rows, reviewRow{kind: rowBanner, file: -1, line: -1})
}

// coreLinks is the card's core places, while the whole review is on screen:
// a commit's hunks are not the review's.
func (m *Model) coreLinks() []review.Link {
	if m.rv.Card == nil || m.commit != "" {
		return nil
	}
	return m.rv.Card.CoreLinks()
}

// markCore says which of a file's hunks the card's core places fall in: the
// hunks holding a line the change made inside one of them (see
// [review.Link.Holds], which card set holds a card to as well).
func (m *Model) markCore(f *reviewFile) {
	f.core, f.coreHunks = false, nil
	for _, l := range m.coreLinks() {
		if l.Path != f.path {
			continue
		}
		f.core = true
		if f.coreHunks == nil {
			f.coreHunks = map[int]bool{}
		}
		for _, d := range f.lines {
			if d.Hunk >= 0 && l.Holds(d) {
				f.coreHunks[d.Hunk] = true
			}
		}
	}
}

// groupByCard puts the files the card names first, in the order it names
// them, and says what goes above each group. files is what is on the screen,
// and all the whole review: a file f has hidden is still counted, since a
// review that seemed to shrink as it was done would be lying about its size.
func (m *Model) groupByCard(files, all []*reviewFile) ([]*reviewFile, *cardBanner) {
	links := m.coreLinks()
	if len(links) == 0 {
		return files, nil
	}
	rank := map[string]int{}
	for _, l := range links {
		if _, ok := rank[l.Path]; !ok {
			rank[l.Path] = len(rank)
		}
	}
	// The core is a second entry for each file it names, drawing only the
	// core hunks; the rest is every file, whole, the core's included.
	var core []*reviewFile
	for _, f := range files {
		if !f.core {
			continue
		}
		c := m.coreEntry(f)
		core = append(core, c)
	}
	slices.SortStableFunc(core, func(a, b *reviewFile) int { return rank[a.path] - rank[b.path] })
	rest := files
	hunks, approved, restDone := 0, 0, 0
	for _, f := range all {
		if f.approved {
			restDone++
		}
		if !f.core {
			continue
		}
		for h := range f.blocks {
			if !f.coreHunks[h] {
				continue
			}
			hunks++
			if f.approved || f.hunkApproved(h) {
				approved++
			}
		}
	}
	b := &cardBanner{
		core:     len(core),
		coreHead: " CORE · " + itoa(approved) + "/" + plural(hunks, "hunk", "hunks") + " approved",
		restHead: " THE REST · " + itoa(restDone) + "/" + plural(len(all), "file", "files") + " approved",
	}
	if !m.cardHidden {
		c := m.rv.Card
		b.summary = c.Summary
		if c.Base != "" && !m.sameBase(c.Base) {
			b.note = "The card is about the change from " + abbrevHash(c.Base) + "; this review is measured from " + nameOr(m.base.Ref, "nothing yet") + "."
			if m.wt != nil {
				b.note += " b offers it first."
			}
		}
	}
	return append(core, rest...), b
}

func nameOr(s, or string) string {
	if s == "" {
		return or
	}
	return s
}

// nextCore moves to the next core hunk still waiting for the reader, from
// where they are on, round to the top: the core is read before the rest.
func (m *Model) nextCore(order []string, at, hunk int) bool {
	for i := 0; i <= len(order); i++ {
		p := order[(at+i)%len(order)]
		f := m.shown(p)
		if f == nil || !f.coreView || f.approved {
			continue
		}
		from := 0
		if i == 0 {
			from = hunk + 1
		}
		for h := max(from, 0); h < len(f.blocks); h++ {
			if !f.coreHunks[h] || len(f.blocks[h]) == 0 || f.hunkApproved(h) {
				continue
			}
			if i > 0 || hunk < 0 {
				if f.folded {
					m.folds[p] = false
					m.rebuild()
				}
				m.view.jumpToFile(p)
			}
			return m.toHunk(f, h)
		}
	}
	return false
}

// doCard puts the card's summary away, or brings it back.
func (m Model) doCard() (tea.Model, tea.Cmd) {
	if m.rv.Card == nil {
		m.setOK("No review card: discobox-review card set <file> keeps one")
		return m, nil
	}
	m.cardHidden = !m.cardHidden
	m.rebuild()
	return m, nil
}

// sameBase reports whether the review is measured from where base names: by
// the same name, or, for a commit, from that commit.
func (m *Model) sameBase(base string) bool {
	if base == m.base.Ref || base == m.baseRef {
		return true
	}
	return len(base) >= 7 && m.base.SHA != "" && strings.HasPrefix(m.base.SHA, base)
}

func oneLine(s string) string { return strings.Join(strings.Fields(s), " ") }

// coreKey is what the core's entry for a file is known by in the stack.
func coreKey(path string) string { return "core\x00" + path }

// coreEntry is the core's view of a file: a second entry for it in the stack,
// keyed apart, with lines and gaps of its own so that opening something in one
// of the two leaves the other as it was. It is made afresh on every rebuild,
// from the file's own entry, and given back what the reader had opened in it.
func (m *Model) coreEntry(f *reviewFile) *reviewFile {
	c := *f
	c.key, c.coreView = coreKey(f.path), true
	c.lines, c.texts = slices.Clone(f.lines), slices.Clone(f.texts)
	c.spans, c.emph = slices.Clone(f.spans), slices.Clone(f.emph)
	c.gaps = slices.Clone(f.gaps)
	// Laid out afresh rather than into the file's own rows.
	c.rows, c.rowAt = nil, nil
	keys, moved := gapKeys(m.commit, &c), false
	for g := range c.gaps {
		if g < len(keys) {
			if want := m.gapsOpen[keys[g]]; c.gaps[g].open != want {
				c.setGap(g, want)
				moved = true
			}
		}
	}
	if moved {
		// The file's comments are hung off its lines by where each is among
		// them, and opening a gap in one entry and not the other moves them.
		// So they are hung again here, by the line each is: one on a line
		// this entry has shut away goes under the heading.
		type at struct {
			kind           byte
			old, new, hunk int
			line           int
		}
		where := make(map[at]int, len(c.lines))
		for i, l := range c.lines {
			where[at{l.Kind, l.Old, l.New, l.Hunk, l.Line}] = i
		}
		c.threads, c.loose = map[int][]*review.Thread{}, slices.Clone(f.loose)
		for i, ts := range f.threads {
			l := f.lines[i]
			if j, ok := where[at{l.Kind, l.Old, l.New, l.Hunk, l.Line}]; ok {
				c.threads[j] = append(c.threads[j], ts...)
			} else {
				c.loose = append(c.loose, ts...)
			}
		}
	}
	c.shut = m.shutFor(&c)
	// Open until it is done with: it is what the review is read for.
	c.folded = c.done()
	if v, ok := m.folds[c.key]; ok {
		c.folded = v
	}
	return &c
}
