package ui

import (
	"image/color"

	"charm.land/lipgloss/v2"
)

// Colours chosen to echo the Tk git-gui palette discobox-review grew out of:
// lavender for the history and gold for the review's own heading.
var (
	colGold     = lipgloss.Color("220")
	colSky      = lipgloss.Color("153")
	colLavender = lipgloss.Color("183")
	colBlack    = lipgloss.Color("232")
	colGrey     = lipgloss.Color("245")
	colDim      = lipgloss.Color("240")
	colSelBG    = lipgloss.Color("24")
	colSelRange = lipgloss.Color("17")
	colInactive = lipgloss.Color("236")

	colHunk = lipgloss.Color("111")
	// The review's own colours: the band behind a file's heading, and the
	// blue a comment is written in.
	colBandBg   = lipgloss.Color("237")
	colBandOnBg = lipgloss.Color("24")
	colComment  = lipgloss.Color("153")

	// Amber is "not finished with yet": the margin beside an uncommitted
	// line, an approval that has fallen, and a warning in the status bar all
	// mean the same kind of thing.
	colWarn = lipgloss.Color("214")
	colErr  = lipgloss.Color("196")
	colOK   = lipgloss.Color("83")
)

type styles struct {
	branchBar   lipgloss.Style
	branchLabel lipgloss.Style

	titleDiff    lipgloss.Style
	titleCommit  lipgloss.Style
	titleCommits lipgloss.Style
	titleDim     lipgloss.Style

	// sel is the row the cursor is on, selRange the dimmer band over a visual
	// selection, and selInactive the cursor row in a pane without focus.
	sel         lipgloss.Style
	selRange    lipgloss.Style
	selInactive lipgloss.Style
	commitHead  lipgloss.Style // the working tree row in the branch history

	body     lipgloss.Style
	dimText  lipgloss.Style
	statusOK lipgloss.Style
	statusWA lipgloss.Style
	statusER lipgloss.Style

	diffAdd     lipgloss.Style
	diffAddEmph lipgloss.Style
	diffDel     lipgloss.Style
	diffDelEmph lipgloss.Style
	diffHunk    lipgloss.Style
	diffMeta    lipgloss.Style

	// The cursor and the visual selection live in a one column bar at the
	// left edge, because the background is spoken for by the diff. The bar
	// beside it says the line's change is not committed yet.
	cursorBar  lipgloss.Style
	selectBar  lipgloss.Style
	pendingBar lipgloss.Style
	numberOn   lipgloss.Style

	// A file's heading in the review: its band, how much it changed, and the
	// verdict glyph in front of it.
	fileBand       lipgloss.Style
	fileBandOn     lipgloss.Style
	statAdd        lipgloss.Style
	statDel        lipgloss.Style
	verdictOK      lipgloss.Style
	verdictStale   lipgloss.Style
	verdictComment lipgloss.Style

	// A comment thread: the rule down its left edge, and the three kinds of
	// text hanging off it.
	commentRule   lipgloss.Style
	commentHead   lipgloss.Style
	commentAuthor lipgloss.Style
	commentBody   lipgloss.Style
	commentFoot   lipgloss.Style
	commentLoose  lipgloss.Style

	// The markdown reader's margin: a changed block, and the one n/p last
	// landed on, which is brighter so it stands apart from its neighbours.
	mdMark    lipgloss.Style
	mdMarkCur lipgloss.Style

	// The scrollbars are furniture: a barely there track and a plain grey
	// thumb, taking no colour of their own so they never compete with the
	// pane for attention.
	scrollTrack lipgloss.Style
	scrollThumb lipgloss.Style

	button      lipgloss.Style
	buttonFocus lipgloss.Style

	commitLabel lipgloss.Style
	dialog      lipgloss.Style
	dialogTitle lipgloss.Style
	key         lipgloss.Style

	syntax syntaxPalette
}

// The diff bands are the one part of the palette that has to know whether the
// terminal is light or dark: a band is a background, and a background that
// reads as "green" on one is unreadable on the other. Everything else is a
// foreground colour chosen to work on either.
//
// A changed line carries its colour as a background, and the characters that
// actually changed carry a stronger shade of it, so both read at once without
// touching the foreground — which belongs to the syntax highlighting.
func bands(dark bool) (addBg, addEmphBg, delBg, delEmphBg color.Color) {
	pick := lipgloss.LightDark(dark)
	return pick(lipgloss.Color("194"), lipgloss.Color("22")),
		pick(lipgloss.Color("157"), lipgloss.Color("28")),
		pick(lipgloss.Color("224"), lipgloss.Color("52")),
		pick(lipgloss.Color("217"), lipgloss.Color("88"))
}

// newStyles builds the palette. dark says what the terminal's own background
// is, which discobox-review asks the terminal for and is told once it answers.
func newStyles(dark bool) *styles {
	s := &styles{}
	colAddBg, colAddEmphBg, colDelBg, colDelEmphBg := bands(dark)
	s.branchBar = lipgloss.NewStyle().Bold(true)
	s.branchLabel = lipgloss.NewStyle().Foreground(colGrey)

	title := lipgloss.NewStyle().Foreground(colBlack).Bold(true).Padding(0, 1)
	s.titleDiff = title.Background(colGold)
	s.titleCommit = title.Background(colSky)
	s.titleCommits = title.Background(colLavender)
	s.titleDim = lipgloss.NewStyle().Foreground(colGrey).Background(colInactive).Padding(0, 1)

	s.sel = lipgloss.NewStyle().Background(colSelBG)
	s.selRange = lipgloss.NewStyle().Background(colSelRange)
	s.commitHead = lipgloss.NewStyle().Bold(true)
	s.selInactive = lipgloss.NewStyle().Background(colInactive)

	s.body = lipgloss.NewStyle()
	s.dimText = lipgloss.NewStyle().Foreground(colDim)
	s.statusOK = lipgloss.NewStyle().Foreground(colOK)
	s.statusWA = lipgloss.NewStyle().Foreground(colWarn)
	s.statusER = lipgloss.NewStyle().Foreground(colErr)

	s.diffAdd = lipgloss.NewStyle().Background(colAddBg)
	s.diffAddEmph = lipgloss.NewStyle().Background(colAddEmphBg)
	s.diffDel = lipgloss.NewStyle().Background(colDelBg)
	s.diffDelEmph = lipgloss.NewStyle().Background(colDelEmphBg)
	s.cursorBar = lipgloss.NewStyle().Background(colGold)
	s.selectBar = lipgloss.NewStyle().Background(colSelBG)
	s.pendingBar = lipgloss.NewStyle().Foreground(colWarn)
	s.numberOn = lipgloss.NewStyle().Foreground(colGold).Bold(true)

	s.fileBand = lipgloss.NewStyle().Background(colBandBg).Bold(true)
	s.fileBandOn = lipgloss.NewStyle().Background(colBandOnBg).Bold(true)
	s.statAdd = lipgloss.NewStyle().Foreground(colOK)
	s.statDel = lipgloss.NewStyle().Foreground(colErr)
	s.verdictOK = lipgloss.NewStyle().Foreground(colOK).Bold(true)
	s.verdictStale = lipgloss.NewStyle().Foreground(colWarn).Bold(true)
	s.verdictComment = lipgloss.NewStyle().Foreground(colComment).Bold(true)

	s.commentRule = lipgloss.NewStyle().Foreground(colDim)
	s.commentHead = lipgloss.NewStyle().Foreground(colComment).Bold(true)
	s.commentAuthor = lipgloss.NewStyle().Foreground(colGold)
	s.commentBody = lipgloss.NewStyle().Foreground(colGrey)
	s.commentFoot = lipgloss.NewStyle().Foreground(colDim)
	s.commentLoose = lipgloss.NewStyle().Foreground(colWarn).Bold(true)
	s.scrollTrack = lipgloss.NewStyle().Foreground(colInactive)
	s.scrollThumb = lipgloss.NewStyle().Foreground(colGrey)
	s.diffHunk = lipgloss.NewStyle().Foreground(colHunk).Bold(true)
	s.diffMeta = lipgloss.NewStyle().Foreground(colDim)

	s.mdMark = lipgloss.NewStyle().Foreground(colHunk)
	s.mdMarkCur = lipgloss.NewStyle().Foreground(colGold).Bold(true)

	s.button = lipgloss.NewStyle().Foreground(colGrey).
		Border(lipgloss.RoundedBorder()).BorderForeground(colDim).Padding(0, 1)
	s.buttonFocus = lipgloss.NewStyle().Bold(true).
		Border(lipgloss.RoundedBorder()).BorderForeground(colGold).Padding(0, 1)

	s.commitLabel = lipgloss.NewStyle().Bold(true)
	s.dialog = lipgloss.NewStyle().
		Border(lipgloss.RoundedBorder()).BorderForeground(colGold).Padding(1, 2)
	s.dialogTitle = lipgloss.NewStyle().Foreground(colGold).Bold(true)
	s.key = lipgloss.NewStyle().Foreground(colGold)
	s.syntax = newSyntaxPalette(dark)
	return s
}
