package ui

import "github.com/charmbracelet/lipgloss"

// Colors chosen to echo the Tk git-gui palette: lightsalmon for the unstaged
// list, lightgreen for the staged list and gold for the diff header.
var (
	colSalmon   = lipgloss.Color("216")
	colGreen    = lipgloss.Color("120")
	colGold     = lipgloss.Color("220")
	colSky      = lipgloss.Color("153")
	colLavender = lipgloss.Color("183")
	colBlack    = lipgloss.Color("232")
	colGrey     = lipgloss.Color("245")
	colDim      = lipgloss.Color("240")
	colSelBG    = lipgloss.Color("24")
	colSelRange = lipgloss.Color("17")
	colInactive = lipgloss.Color("236")
	colAdd      = lipgloss.Color("77")
	colDel      = lipgloss.Color("203")

	// Diff bands. A changed line carries its colour as a background, and the
	// characters that actually changed carry a stronger shade of it, so both
	// read at once without touching the foreground — which belongs to the
	// syntax highlighting.
	colAddBg     = lipgloss.AdaptiveColor{Dark: "22", Light: "194"}
	colAddEmphBg = lipgloss.AdaptiveColor{Dark: "28", Light: "157"}
	colDelBg     = lipgloss.AdaptiveColor{Dark: "52", Light: "224"}
	colDelEmphBg = lipgloss.AdaptiveColor{Dark: "88", Light: "217"}
	colHunk      = lipgloss.Color("111")
	colWarn      = lipgloss.Color("214")
	colErr       = lipgloss.Color("196")
	colOK        = lipgloss.Color("83")
)

type styles struct {
	branchBar   lipgloss.Style
	branchLabel lipgloss.Style

	titleUnstaged lipgloss.Style
	titleStaged   lipgloss.Style
	titleDiff     lipgloss.Style
	titleCommit   lipgloss.Style
	titleCommits  lipgloss.Style
	titleDim      lipgloss.Style

	// sel is the row the cursor is on, selRange the dimmer band over a visual
	// selection, and selInactive the cursor row in a pane without focus.
	sel         lipgloss.Style
	selRange    lipgloss.Style
	selInactive lipgloss.Style
	commitHead  lipgloss.Style // the working tree row in the branch history

	body     lipgloss.Style
	dimText  lipgloss.Style
	sep      lipgloss.Style
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
	// left edge, because the background is spoken for by the diff.
	cursorBar lipgloss.Style
	selectBar lipgloss.Style
	numberOn  lipgloss.Style

	// The markdown reader's margin: a changed block, and the one n/p last
	// landed on, which is brighter so it stands apart from its neighbours.
	mdMark    lipgloss.Style
	mdMarkCur lipgloss.Style

	button      lipgloss.Style
	buttonFocus lipgloss.Style

	commitLabel lipgloss.Style
	dialog      lipgloss.Style
	dialogTitle lipgloss.Style
	key         lipgloss.Style

	syntax syntaxPalette
}

func newStyles() *styles {
	s := &styles{}
	s.branchBar = lipgloss.NewStyle().Bold(true)
	s.branchLabel = lipgloss.NewStyle().Foreground(colGrey)

	title := lipgloss.NewStyle().Foreground(colBlack).Bold(true).Padding(0, 1)
	s.titleUnstaged = title.Background(colSalmon)
	s.titleStaged = title.Background(colGreen)
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
	s.sep = lipgloss.NewStyle().Foreground(colDim)
	s.statusOK = lipgloss.NewStyle().Foreground(colOK)
	s.statusWA = lipgloss.NewStyle().Foreground(colWarn)
	s.statusER = lipgloss.NewStyle().Foreground(colErr)

	s.diffAdd = lipgloss.NewStyle().Background(colAddBg)
	s.diffAddEmph = lipgloss.NewStyle().Background(colAddEmphBg)
	s.diffDel = lipgloss.NewStyle().Background(colDelBg)
	s.diffDelEmph = lipgloss.NewStyle().Background(colDelEmphBg)
	s.cursorBar = lipgloss.NewStyle().Background(colGold)
	s.selectBar = lipgloss.NewStyle().Background(colSelBG)
	s.numberOn = lipgloss.NewStyle().Foreground(colGold).Bold(true)
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
	s.syntax = newSyntaxPalette()
	return s
}
