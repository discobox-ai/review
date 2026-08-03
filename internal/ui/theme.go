package ui

import "github.com/charmbracelet/lipgloss"

// Colors chosen to echo the Tk git-gui palette: lightsalmon for the unstaged
// list, lightgreen for the staged list and gold for the diff header.
var (
	colSalmon   = lipgloss.Color("216")
	colGreen    = lipgloss.Color("120")
	colGold     = lipgloss.Color("220")
	colBlack    = lipgloss.Color("232")
	colGrey     = lipgloss.Color("245")
	colDim      = lipgloss.Color("240")
	colSelBG    = lipgloss.Color("24")
	colInactive = lipgloss.Color("236")
	colAdd      = lipgloss.Color("77")
	colDel      = lipgloss.Color("203")
	colHunk     = lipgloss.Color("111")
	colWarn     = lipgloss.Color("214")
	colErr      = lipgloss.Color("196")
	colOK       = lipgloss.Color("83")
)

type styles struct {
	branchBar   lipgloss.Style
	branchLabel lipgloss.Style

	titleUnstaged lipgloss.Style
	titleStaged   lipgloss.Style
	titleDiff     lipgloss.Style
	titleDim      lipgloss.Style

	sel         lipgloss.Style
	selInactive lipgloss.Style

	body     lipgloss.Style
	dimText  lipgloss.Style
	sep      lipgloss.Style
	statusOK lipgloss.Style
	statusWA lipgloss.Style
	statusER lipgloss.Style

	diffAdd  lipgloss.Style
	diffDel  lipgloss.Style
	diffHunk lipgloss.Style
	diffMeta lipgloss.Style

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
	s.titleDim = lipgloss.NewStyle().Foreground(colGrey).Background(colInactive).Padding(0, 1)

	s.sel = lipgloss.NewStyle().Background(colSelBG)
	s.selInactive = lipgloss.NewStyle().Background(colInactive)

	s.body = lipgloss.NewStyle()
	s.dimText = lipgloss.NewStyle().Foreground(colDim)
	s.sep = lipgloss.NewStyle().Foreground(colDim)
	s.statusOK = lipgloss.NewStyle().Foreground(colOK)
	s.statusWA = lipgloss.NewStyle().Foreground(colWarn)
	s.statusER = lipgloss.NewStyle().Foreground(colErr)

	s.diffAdd = lipgloss.NewStyle().Foreground(colAdd)
	s.diffDel = lipgloss.NewStyle().Foreground(colDel)
	s.diffHunk = lipgloss.NewStyle().Foreground(colHunk).Bold(true)
	s.diffMeta = lipgloss.NewStyle().Foreground(colDim)

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
