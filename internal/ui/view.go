package ui

import (
	"strings"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// The window: where each pane goes, and what it draws.
//
//	Review
//	────────
//	History
//
// One column. The review is the window, and its headings are the list of what
// changed; the history is a strip under it that folds to its own title. A
// comment being written is not a pane: it is a box drawn into the review at
// the place the comment will land, so there is nothing here to make room for.

// layout recomputes pane geometry.
func (m *Model) layout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	// Whatever shape the window takes, the review's rows were laid out for
	// whatever width it had before. Deferred rather than written at the end
	// because the zoomed window leaves by its own path, and the one that gets
	// forgotten is the one that draws nonsense. The mark is taken here, before
	// the width moves, for the reason mark itself gives.
	keep := m.view.mark()
	defer func() { m.view.reflow(keep) }()
	m.rects = [focusCount]rect{}
	body := max(m.height-2, 4) // branch bar and status bar

	if m.zoom {
		m.layoutZoom(body)
		return
	}
	m.layoutStacked(body)
}

// layoutStacked is the window as one column: the review across the top, and
// under it the history, which is a title and nothing else until it is asked
// for. Unfolded it takes no more than a third of the window, title included,
// and no more than it has to show — but always a row of it, or it would not be
// unfolded, on any window with the room for one.
func (m *Model) layoutStacked(body int) {
	m.history.button = ""
	histH := 0 // the whole strip, title included
	if m.showHistory() {
		m.history.button, histH = "[+] ", 1
		if m.histOpen {
			m.history.button = "[-] "
			histH = 1 + min(max(body/3-1, 1), m.history.rows())
			histH = min(histH, max(body-3, 1))
		}
		if !m.historyFits() {
			// No room for a row of it. It is drawn as folded because it is,
			// and the keyboard cannot be left in something with no rows.
			m.history.button = "[+] "
			if m.focus == focusHistory {
				m.focus = focusReview
			}
		}
	}
	m.history.width, m.history.height = m.width, histH-1
	m.view.width, m.view.height = m.width, max(body-histH-1, 1)
	m.rects[focusReview] = rect{0, 1, m.width, 1 + m.view.height}
	if histH > 0 {
		m.rects[focusHistory] = rect{0, 2 + m.view.height, m.width, histH}
	}
	m.clampPanes()
}

// historyFits reports whether the window has room for a row of history under
// the review, which keeps three rows of its own whatever else is asked for.
func (m *Model) historyFits() bool { return max(m.height-2, 4)-3 >= 2 }

// layoutZoom gives the focused pane the whole window. Nothing else is drawn,
// so nothing else is laid out and nothing else can be clicked.
func (m *Model) layoutZoom(body int) {
	w, h := m.width, max(body-1, 1) // one row goes to the pane's own title
	m.history.button = ""
	switch m.focus {
	case focusHistory:
		m.history.width, m.history.height = w, h
	case focusReview, focusCompose:
		// The comment being written lives inside the review, so zooming it is
		// zooming the review.
		m.view.width, m.view.height = w, h
	}
	m.rects[m.zoomed()] = rect{0, 1, w, body}
	m.clampPanes()
}

// zoomed is the pane the whole window is given over to. The composer has no
// pane of its own, so it stands in for the review it is drawn inside.
func (m *Model) zoomed() focusArea {
	if m.focus == focusCompose {
		return focusReview
	}
	return m.focus
}

func (m *Model) clampPanes() {
	m.history.clamp()
	m.view.clamp()
}

// toggleZoom gives a pane the whole window, or hands the window back.
//
// Zooming a pane is also going to it. With one pane on screen there is nowhere
// else for the keyboard to be, and a focus sitting on something invisible is
// how a window ends up ignoring keys for no reason anyone can see.
func (m Model) toggleZoom(f focusArea) (tea.Model, tea.Cmd) {
	if !m.paneVisible(f) {
		return m, nil
	}
	if m.zoom && m.focus == f {
		m.zoom = false
		m.layout()
		return m, nil
	}
	m.zoom = true
	return m.setFocus(f)
}

// View renders the whole window.
//
// The view carries the screen's state as well as its content: the alternate
// screen, the mouse, and the keyboard enhancements discobox-review would like the
// terminal to turn on. In v1 those were program options settled once at
// startup; here they are part of what the model says the screen should be,
// which is the same thing said in the place it belongs.
func (m Model) View() tea.View {
	v := tea.NewView(m.frame())
	v.AltScreen = true
	// Cell motion rather than all motion: the window wants clicks, drags and
	// the wheel, and nothing at all from a pointer moving with no button
	// down. Asking for less means the terminal sends less.
	v.MouseMode = tea.MouseModeCellMotion
	// Nothing beyond the default is asked for, and the default is key
	// disambiguation — which is the whole reason this is here. It is what
	// lets Ctrl-Enter be told apart from Enter on the terminals that can do
	// it. The rest of the protocol, key release events and alternate key
	// codes, is data discobox-review would never read. Terminals that support none of
	// it are no worse off: the keys arrive the way they always did.
	v.KeyboardEnhancements = tea.KeyboardEnhancements{}
	return v
}

// frame is the window as one string, which is all the rest of the drawing code
// has ever had to think about.
func (m Model) frame() string {
	if m.quit {
		return ""
	}
	if !m.ready {
		return "Loading…"
	}

	middle := m.viewZoomed()
	if !m.zoom {
		middle = m.viewPane(focusReview)
		if m.showHistory() {
			middle = lipgloss.JoinVertical(lipgloss.Left, middle, m.viewPane(focusHistory))
		}
	}

	view := lipgloss.JoinVertical(lipgloss.Left,
		m.viewBranchBar(), middle, m.viewStatusBar())

	if m.dialog != nil {
		return m.dialog.view(m.st, m.width, m.height)
	}
	if m.md != nil {
		return paintSelection(m.md.view(m.st), m.mouse.selectionIn(focusReader), mdSel{m.md})
	}
	if m.file != nil {
		return paintSelection(m.file.view(m.st), m.mouse.selectionIn(focusReader), fileSel{m.file, m.st})
	}
	return view
}

// viewPane draws one pane, with whatever the mouse has selected in it painted
// over the top.
func (m Model) viewPane(f focusArea) string {
	focused := m.focus == f
	var out string
	switch f {
	case focusHistory:
		out = m.history.view(m.st, focused)
	case focusReview:
		out = m.view.view(m.st, focused, m.rv)
	default:
		return ""
	}
	p := m.selectableFor(f)
	if p == nil {
		return out
	}
	return paintSelection(out, m.mouse.selectionIn(f), p)
}

// viewZoomed draws the focused pane across the whole window.
func (m Model) viewZoomed() string {
	if !m.zoom {
		return ""
	}
	return m.viewPane(m.zoomed())
}

// barTarget is what a click on the top bar lands on.
type barTarget int

const (
	barNothing barTarget = iota
	barBranch            // the branch name: click it to review another one
	barBase              // where the review is measured from: click it to move it
)

// barSpan is a run of the top bar that can be clicked, in screen columns.
type barSpan struct {
	x, w int
	what barTarget
}

// branchBar draws the top bar and says where its clickable parts are.
//
// The two come out of one function because they are one arithmetic: the bar
// drops its base segment on a narrow window, and a hit test that worked it out
// separately would go on offering to move a base that is not on screen.
func (m Model) branchBar() (string, []barSpan) {
	var spans []barSpan

	const label = "Reviewing: "
	left := m.st.branchLabel.Render(label) + m.st.branchBar.Render(m.branch)
	at := width(label)
	spans = append(spans, barSpan{at, width(m.branch), barBranch})
	at += width(m.branch)

	if m.merging {
		// A merge in progress is the loudest reason a tree is not clean, and
		// it explains states — unmerged files — that nothing else would.
		merging := "[merging]"
		left += "  " + m.st.statusWA.Render(merging)
		at += 2 + width(merging)
	}
	right := m.st.dimText.Render("F1 help  ·  Ctrl-Q quit")

	// The base is the first thing dropped when the bar will not hold all
	// three: the branch is where you are and the right hand side is the way
	// out, where the base is context you can do without.
	if base := m.viewBase(); base != "" {
		if full := left + base; m.width-lipgloss.Width(full)-lipgloss.Width(right) >= 1 {
			left = full
			spans = append(spans, barSpan{at, lipgloss.Width(base), barBase})
		}
	}

	gapW := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gapW < 1 {
		// Truncated: whatever ran off the end cannot be clicked either.
		out := truncateANSI(left, m.width)
		kept := spans[:0]
		for _, s := range spans {
			if s.x+s.w <= m.width {
				kept = append(kept, s)
			}
		}
		return out, kept
	}
	return left + strings.Repeat(" ", gapW) + right, spans
}

func (m Model) viewBranchBar() string {
	bar, _ := m.branchBar()
	return bar
}

// clickTopBar answers a press on the top row. The branch name and the base are
// the two things up there that name a choice, so they are the two things that
// offer to change it — the same choices K and b offer, in the place the answer
// is already written.
func (m Model) clickTopBar(x int) (tea.Model, tea.Cmd) {
	_, spans := m.branchBar()
	for _, s := range spans {
		if x < s.x || x >= s.x+s.w {
			continue
		}
		switch s.what {
		case barBranch:
			return m.doCheckout()
		case barBase:
			return m.doChooseBase()
		}
	}
	return m, nil
}

// viewBase is the base segment of the branch bar: what the review is measured
// from, the commit it and this branch last had in common, and how far each has
// moved since. Behind is called out in the warning colour when there is
// anything to catch up on, since that is the half you can act on.
func (m Model) viewBase() string {
	b := m.base
	if b.Ref == "" {
		return ""
	}
	s := m.st.dimText.Render("  ·  ") + m.st.branchLabel.Render("from ") + b.Ref
	if !b.Auto {
		s += m.st.dimText.Render(" (chosen)")
	}
	if b.Commit != "" {
		s += m.st.dimText.Render(" " + b.Commit)
	}
	s += " " + m.st.dimText.Render("↑"+itoa(b.Ahead))
	behind := "↓" + itoa(b.Behind)
	if b.Behind > 0 {
		return s + " " + m.st.statusWA.Render(behind)
	}
	return s + " " + m.st.dimText.Render(behind)
}

// viewStatusBar carries the two things a review is trying to get to zero:
// what is left to read, and what is left to commit.
func (m Model) viewStatusBar() string {
	var left string
	switch {
	case m.statusEr:
		left = m.st.statusER.Render("✗ " + m.status)
	case m.statusWa:
		left = m.st.statusWA.Render("! " + m.status)
	case m.status != "":
		left = m.st.statusOK.Render(m.status)
	case m.rvErr != "":
		left = m.st.statusWA.Render("! " + m.rvErr)
	default:
		left = m.st.dimText.Render(m.hints())
	}
	right := m.st.dimText.Render(m.scoreboard())
	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right)
	if gap < 1 {
		return truncateANSI(left, m.width)
	}
	return left + strings.Repeat(" ", gap) + right
}

// scoreboard is how far off a finished review is: files still to approve,
// files still to commit, and conversations still open. It says "clean" only
// when all three are nothing, which is the state the whole window is aimed at.
func (m Model) scoreboard() string {
	unapproved, uncommitted := 0, 0
	for _, f := range m.work {
		if !m.rv.Approved(f.path) {
			unapproved++
		}
		if f.state.Pending() != "" {
			uncommitted++
		}
	}
	open := m.rv.Open()
	if unapproved == 0 && uncommitted == 0 && open == 0 && len(m.work) > 0 {
		return "clean · reviewed · ctx:" + itoa(m.context)
	}
	var parts []string
	if unapproved > 0 {
		parts = append(parts, itoa(unapproved)+" to read")
	}
	if uncommitted > 0 {
		parts = append(parts, itoa(uncommitted)+" to commit")
	}
	if open > 0 {
		parts = append(parts, itoa(open)+" open")
	}
	parts = append(parts, "ctx:"+itoa(m.context))
	return strings.Join(parts, " · ")
}

func (m Model) hints() string {
	switch m.focus {
	case focusHistory:
		if _, ok := m.history.selected(); ok {
			return "↑/↓ pick a commit · ↑ to the top row for the whole review · ? keys"
		}
		return "The whole review, from " + m.base.Ref + " · ↓ one commit at a time · b change the base"
	case focusCompose:
		// Every letter is text here, so the command letters are not offered.
		return "Writing a comment · " + postHint + " · Tab leaves it open"
	}

	if m.count != "" {
		return "go to line " + m.count + " · g or G jumps · Esc cancels"
	}
	if m.view.visual() {
		n := 1 + max(m.view.cursor, m.view.anchor) - min(m.view.cursor, m.view.anchor)
		return "VISUAL  " + plural(n, "row", "rows") + " · ↑/↓ extend · V or Esc cancel"
	}
	r, ok := m.view.row()
	switch {
	case ok && r.kind == rowThread:
		if _, _, ok := m.view.comment(); ok {
			return "r reply · x resolve · X delete this remark · t next open comment"
		}
		return "r reply · x resolve · X delete the whole thread · t next open"
	case ok && r.kind == rowFile:
		// Short enough to leave the scoreboard its room on an ordinary
		// window: this is the row the window opens on.
		if m.showHistory() {
			return "a approve · Space fold · F all · H history · " + m.splitHint() + " · ? keys"
		}
		return "a approve · Space fold · F fold them all · " + m.splitHint() + " · ] next file · ? keys"
	}
	// The hints run out of room before the scoreboard does, and what runs off
	// the end is the tail — so the ones worth keeping are said early.
	hint := "c comment · a approve the hunk · " + m.splitHint() +
		" · n/p hunk · Space fold it · t open comments · ] file · -/= context"
	if m.view.splitting() {
		// Crossing a split row only means anything while there are two sides
		// to cross between, so it is offered there and nowhere else. This one
		// does ask what was drawn rather than what was asked for.
		hint = "c comment · a approve the hunk · " + m.splitHint() +
			" · S other side · n/p hunk · Space fold it · t open comments · ] file"
	}
	if m.commit != "" {
		hint = "Reading one commit · c comments on the review · Alt-2 then ↑ for all of it"
	}
	return hint
}

// splitHint names what s would do next rather than what the pane is doing now.
// A key's hint is an offer, and "s split" while already split reads as a
// description of the screen instead of as something to press.
//
// It follows the preference rather than what the pane managed to draw. The two
// differ on a pane too narrow to split, and there "s split" would be an offer
// to do the thing already asked for: pressing it turns the preference off,
// reports "Unified diff", and changes nothing on the screen.
func (m Model) splitHint() string {
	if m.view.split {
		return "s unify"
	}
	return "s split"
}

func helpText() string {
	return strings.Join([]string{
		"Discobox Review reviews a branch. It reads a repository and never writes one:",
		"nothing here stages, commits, or rewrites anything.",
		"",
		"The review is every change between the merge base and the working",
		"tree — staged, unstaged and untracked alike — so what you are reading",
		"is the whole of what this branch is proposing.",
		"",
		"───────────────────────────────────────────────────────────────",
		"Commands",
		"",
		"    a  approve this hunk — or, on a file's heading, the file",
		"    A  approve every file",
		"    F  fold every file, or open them all again",
		"    H  show the history under the review, or hide it again",
		"    f  hide the files that are done with, and show them again",
		"    s  unify the diff, or split it into its two sides again",
		"       (it opens split; clicking the word in the title does the same)",
		"    S  cross a split row, to the line the change took away",
		"    c  comment on this line   r  reply to this comment",
		"    x  resolve / reopen it    X  delete a comment",
		"",
		"    b  choose the base — a ref, or a commit out of your own",
		"       history, or anything git can name",
		"    K  check out another branch",
		"    V  select a run of lines, then c to comment on the block",
		"    v  read the whole file      o  read it as the base has it",
		"    e  edit the file in $EDITOR",
		"    m  read a markdown file, rendered, full screen",
		"    R  rescan (also F5)       -  /  =   less / more context",
		"    z  zoom the pane to the whole window (Alt-Z anywhere)",
		"",
		"    Space  fold the hunk you are in, from anywhere in it, and open it",
		"           again. On a file's heading it folds the file, and on a",
		"           resolved comment it opens that. It never writes anything.",
		"           On a row counting unchanged lines the diff left out, it",
		"           shows them, and puts them away again.",
		"    Enter  the same as Space. A comment begins with c, and only c.",
		"",
		"    Ctrl-S       post the comment being written",
		"    Ctrl-C or q  quit         F1 or ?  these keys",
		"",
		"───────────────────────────────────────────────────────────────",
		"Getting about",
		"",
		"    Tab / Shift-Tab  cycle panes",
		"    Alt-1 review     Alt-2 history",
		"",
		"    24g     line 24 of the file the cursor is in",
		"    n / p   next / previous hunk",
		"    t / T   next / previous open comment",
		"    ] / [   next / previous file",
		"    g / G   the top / the bottom",
		"",
		"  The window opens as the list of what changed: every file folded",
		"  to its heading, which says where the file stands (○ unread,",
		"  ◆ a comment open, ✓ approved, ◈ approved and since changed),",
		"  how much it changed, and what is left to commit. Space opens",
		"  the one the cursor is on, and F opens them all.",
		"",
		"  Under the review is the history: the commits this branch has",
		"  made, under a first row standing for all of them at once.",
		"  Picking a commit narrows the review to that step. H folds the",
		"  history down to its title and opens it again.",
		"",
		"───────────────────────────────────────────────────────────────",
		"Approving",
		"",
		"  An approval is of a change, not of a name. What goes into the",
		"  review file with it is a digest of that file's whole change from",
		"  the base, so editing the file afterwards nullifies the approval",
		"  and the file comes back marked ◈ rather than ✓.",
		"",
		"  Turning the diff context up or down does not nullify anything:",
		"  the digest counts only the lines that changed.",
		"",
		"  Approving a file folds it away again, so what is left open is",
		"  what is left to do. Space unfolds it, and F folds the lot — or",
		"  opens the lot, when they are already folded.",
		"",
		"  Opening them again steps over the ones that are done with, since",
		"  \"show me everything\" means everything there is still something",
		"  to do about. An approved file with an open comment on it is not",
		"  done with, so it comes back like the rest.",
		"",
		"  f goes further and takes those files off the screen altogether,",
		"  leaving what is unapproved or still being argued about. The",
		"  counts still count them: a hidden file is finished, not gone.",
		"",
		"───────────────────────────────────────────────────────────────",
		"Comments",
		"",
		"  c writes one against the line under the cursor, r answers one,",
		"  and x closes it. A closed comment collapses to a single line;",
		"  Space opens it again.",
		"",
		"  X deletes what the cursor is on: one remark when it is on one,",
		"  and the whole conversation when it is on the rule above or the",
		"  hint below. It asks first. Resolving is usually what you want —",
		"  it keeps the record of what was asked and answered.",
		"",
		"  The box is drawn where the comment will land — inline, under",
		"  the line it is about — so what you are looking at while you",
		"  write it is what you will be looking at when you read it back.",
		"  Ctrl-S posts it, Esc throws it away.",
		"",
		"  Ctrl-Enter posts too, in the terminals that can tell it apart",
		"  from Enter. Many cannot: they send the same byte for both, and",
		"  inside a text field that has to stay a newline. Ctrl-S is the",
		"  one that always arrives.",
		"",
		"  V starts a selection and the arrows extend it, the way visual",
		"  line mode does in vim; c then comments on the whole block. It",
		"  is anchored to the first line and drawn under the last.",
		"",
		"  Comments are stored against the working tree. Writing one while",
		"  reading a single commit carries it into those coordinates there",
		"  and then, so going back to the whole review finds it beside the",
		"  same code. One that cannot be carried — the line is no longer",
		"  in the working tree — is kept all the same, under its file's",
		"  heading rather than beside a line that is not there.",
		"",
		"───────────────────────────────────────────────────────────────",
		"Split and unified",
		"",
		"  s stands the two sides of a change beside each other: what a",
		"  line was on the left, what it became on the right. Each run of",
		"  removals is paired with the run of additions after it, and a",
		"  pure insertion has nothing on its left.",
		"",
		"  Neither view is right for everything, which is why it is a key",
		"  rather than a setting. Below 80 columns the pane draws unified",
		"  anyway, and splits itself once there is room.",
		"",
		"───────────────────────────────────────────────────────────────",
		"Reading a whole file",
		"",
		"  A diff is what is being proposed, but sooner or later a line",
		"  raises a question the diff cannot answer. v opens the whole",
		"  file, at the line you were reading, with the lines the review",
		"  touches marked down the margin.",
		"",
		"  o opens the other side: the file as the base has it. Inside",
		"  the reader v and o swap between them and keep the line, so",
		"  \"what did this used to say\" is one keystroke. n/p hop between",
		"  the changed runs, e goes straight to $EDITOR, and Esc or q",
		"  closes.",
		"",
		"───────────────────────────────────────────────────────────────",
		"What is not committed yet",
		"",
		"  An amber bar down the margin marks every line whose change is",
		"  not in a commit. The file's heading says so in words — staged,",
		"  uncommitted, untracked — and the status bar counts them.",
		"",
		"  A finished review is: every file approved, every comment",
		"  resolved, and nothing left in the margin. The status bar says",
		"  \"clean · reviewed\" when you are there.",
		"",
		"───────────────────────────────────────────────────────────────",
		"The review file",
		"",
		"  There are subcommands for all of this too — discobox-review diff, list,",
		"  comment, reply, resolve, delete, approve — so an agent can read",
		"  the change and act on it without a window. The line numbers",
		"  discobox-review diff prints are the ones discobox-review comment takes.",
		"  discobox-review help lists them.",
		"",
		"  Everything above is kept in .git/discobox-review.json, which is",
		"  meant to be read and written by other things — an agent that",
		"  answers your comments, or approves what it has fixed. Discobox Review",
		"  notices the file changing and picks it up within a second, and",
		"  re-reads it before every write of its own, so both of you can",
		"  work on the same review at once.",
		"",
		"───────────────────────────────────────────────────────────────",
		"The mouse",
		"",
		"  Click a row to go to it, wheel to scroll, drag to select, and",
		"  let go to copy — over OSC 52, so the copy lands on the machine",
		"  you are sitting at. Double click a title to zoom that pane.",
		"  Shift-drag gives your terminal's own selection back.",
		"",
		"  Click a file's heading to fold it or open it, the [-] on the",
		"  history's title to fold that, and drag a scrollbar to scroll",
		"  the pane it belongs to.",
		"",
		"  On the top bar, click the branch name to check out another",
		"  branch, and the \"from …\" beside it to choose the base — the",
		"  same choices K and b offer.",
	}, "\n")
}
