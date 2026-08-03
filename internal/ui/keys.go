package ui

import "github.com/charmbracelet/bubbles/key"

// keyMap mirrors git-gui's accelerators wherever a terminal can deliver them.
// Ctrl-I and Ctrl-M are indistinguishable from Tab and Enter on a terminal, so
// those two commands ("Stage Changed Files To Commit" and "Local Merge") are
// reachable through their unmodified letters while a list or the diff has
// focus, and through the on-screen buttons.
type keyMap struct {
	Quit    key.Binding
	QuitAlt key.Binding
	Help    key.Binding
	Rescan  key.Binding
	Stage   key.Binding
	Unstage key.Binding
	Toggle  key.Binding

	StageChanged key.Binding
	Revert       key.Binding
	SignOff      key.Binding
	Amend        key.Binding
	Commit       key.Binding
	Push         key.Binding
	Merge        key.Binding

	LessContext key.Binding
	MoreContext key.Binding

	FocusWorkdir key.Binding
	FocusIndex   key.Binding
	FocusDiff    key.Binding
	FocusCommit  key.Binding
	NextPane     key.Binding
	PrevPane     key.Binding

	Up       key.Binding
	Down     key.Binding
	Left     key.Binding
	Right    key.Binding
	PageUp   key.Binding
	PageDown key.Binding
	Home     key.Binding
	End      key.Binding

	Mark      key.Binding
	Visual    key.Binding
	StageLine key.Binding

	Branch   key.Binding
	Checkout key.Binding
	Delete   key.Binding

	Accept key.Binding
	Cancel key.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		// Ctrl-Q is git-gui's accelerator, but it is XON on terminals with
		// flow control still enabled, so Ctrl-C quits too.
		Quit:    key.NewBinding(key.WithKeys("ctrl+q", "ctrl+c"), key.WithHelp("Ctrl-Q", "quit")),
		QuitAlt: key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		Help:    key.NewBinding(key.WithKeys("f1", "?"), key.WithHelp("F1", "help")),
		Rescan:  key.NewBinding(key.WithKeys("f5", "ctrl+r"), key.WithHelp("F5", "rescan")),

		Stage:   key.NewBinding(key.WithKeys("ctrl+t"), key.WithHelp("Ctrl-T", "stage")),
		Unstage: key.NewBinding(key.WithKeys("ctrl+u"), key.WithHelp("Ctrl-U", "unstage")),
		Toggle:  key.NewBinding(key.WithKeys(" ", "enter"), key.WithHelp("Space", "toggle staged")),

		StageChanged: key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "stage changed")),
		Revert:       key.NewBinding(key.WithKeys("ctrl+j"), key.WithHelp("Ctrl-J", "revert changes")),
		SignOff:      key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("Ctrl-S", "sign off")),
		Amend:        key.NewBinding(key.WithKeys("ctrl+e"), key.WithHelp("Ctrl-E", "amend last commit")),
		Commit:       key.NewBinding(key.WithKeys("ctrl+enter", "alt+c"), key.WithHelp("Ctrl-Enter", "commit")),
		Push:         key.NewBinding(key.WithKeys("ctrl+p"), key.WithHelp("Ctrl-P", "push")),
		Merge:        key.NewBinding(key.WithKeys("M"), key.WithHelp("M", "local merge")),

		LessContext: key.NewBinding(key.WithKeys("ctrl+_", "ctrl+-", "-"), key.WithHelp("-", "less context")),
		MoreContext: key.NewBinding(key.WithKeys("ctrl+=", "=", "+"), key.WithHelp("=", "more context")),

		FocusWorkdir: key.NewBinding(key.WithKeys("alt+1"), key.WithHelp("Alt-1", "unstaged")),
		FocusIndex:   key.NewBinding(key.WithKeys("alt+2"), key.WithHelp("Alt-2", "staged")),
		FocusDiff:    key.NewBinding(key.WithKeys("alt+3"), key.WithHelp("Alt-3", "diff")),
		FocusCommit:  key.NewBinding(key.WithKeys("alt+4"), key.WithHelp("Alt-4", "message")),
		NextPane:     key.NewBinding(key.WithKeys("tab"), key.WithHelp("Tab", "next pane")),
		PrevPane:     key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("Shift-Tab", "prev pane")),

		Up:       key.NewBinding(key.WithKeys("up", "k")),
		Down:     key.NewBinding(key.WithKeys("down", "j")),
		Left:     key.NewBinding(key.WithKeys("left", "h")),
		Right:    key.NewBinding(key.WithKeys("right", "l")),
		PageUp:   key.NewBinding(key.WithKeys("pgup", "ctrl+b")),
		PageDown: key.NewBinding(key.WithKeys("pgdown", "ctrl+f")),
		Home:     key.NewBinding(key.WithKeys("home", "g")),
		End:      key.NewBinding(key.WithKeys("end", "G")),

		Mark:      key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "mark file")),
		Visual:    key.NewBinding(key.WithKeys("V"), key.WithHelp("V", "visual line select")),
		StageLine: key.NewBinding(key.WithKeys("L"), key.WithHelp("L", "stage this line")),

		Branch:   key.NewBinding(key.WithKeys("ctrl+n"), key.WithHelp("Ctrl-N", "create branch")),
		Checkout: key.NewBinding(key.WithKeys("ctrl+o"), key.WithHelp("Ctrl-O", "checkout branch")),
		Delete:   key.NewBinding(key.WithKeys("D"), key.WithHelp("D", "delete untracked")),

		Accept: key.NewBinding(key.WithKeys("enter")),
		Cancel: key.NewBinding(key.WithKeys("esc")),
	}
}
