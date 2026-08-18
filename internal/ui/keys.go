package ui

import "github.com/charmbracelet/bubbles/key"

// keyMap follows one rule:
//
//	a letter   runs a command, in the file lists and the diff.
//	Ctrl       edits text, and is never a command.
//
// Ctrl-Enter opens the commit composer, and inside it Ctrl-Enter asks to
// commit. A terminal sends Ctrl-Enter as 0x0A, byte-identical to Ctrl-J, so
// both are bound. Inside the composer a letter is text; Esc closes it, keeping
// what was typed.
//
// Alt is used only to jump straight to a pane (Alt-1 to Alt-4); Tab does the
// same thing without it, which matters on macOS where Option is not Meta
// unless the terminal is configured for it.
//
// git-gui's own accelerators are mostly Ctrl and a terminal cannot honour
// them: Ctrl-I is Tab, Ctrl-M is Enter, Ctrl-Enter is Ctrl-J, and Ctrl-A,
// Ctrl-E, Ctrl-K, Ctrl-U, Ctrl-W, Ctrl-D, Ctrl-B, Ctrl-F, Ctrl-N, Ctrl-P and
// Ctrl-T all edit text in the message buffer.
type keyMap struct {
	// Live everywhere, including the commit message.
	Quit   key.Binding
	Help   key.Binding
	Commit key.Binding
	Rescan key.Binding

	FocusWorkdir key.Binding
	FocusIndex   key.Binding
	FocusDiff    key.Binding
	FocusCommit  key.Binding
	FocusCommits key.Binding
	NextPane     key.Binding
	PrevPane     key.Binding

	// Commands. A letter each, live in the file lists and the diff.
	StageKey       key.Binding
	UnstageKey     key.Binding
	RevertKey      key.Binding
	SignOffKey     key.Binding
	AmendKey       key.Binding
	EditKey        key.Binding
	MarkdownKey    key.Binding
	PushKey        key.Binding
	MergeKey       key.Binding
	BranchKey      key.Binding
	CheckoutKey    key.Binding
	RescanKey      key.Binding
	HelpKey        key.Binding
	QuitKey        key.Binding
	Toggle         key.Binding
	Visual         key.Binding
	StageLine      key.Binding
	Delete         key.Binding
	RevertHunk     key.Binding
	PopKey         key.Binding
	NextHunk       key.Binding
	PrevHunk       key.Binding
	LessContextKey key.Binding
	MoreContextKey key.Binding

	Up       key.Binding
	Down     key.Binding
	Left     key.Binding
	Right    key.Binding
	PageUp   key.Binding
	PageDown key.Binding
	Home     key.Binding
	End      key.Binding

	Accept key.Binding
	Cancel key.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		Quit: key.NewBinding(key.WithKeys("ctrl+c", "ctrl+q"), key.WithHelp("Ctrl-C", "quit")),
		Help: key.NewBinding(key.WithKeys("f1"), key.WithHelp("F1", "help")),
		// Ctrl-J is what a terminal sends for Ctrl-Enter. This is the only
		// command reachable from inside the message buffer, and the only one
		// worth reaching from there.
		Commit: key.NewBinding(key.WithKeys("ctrl+j", "ctrl+enter"), key.WithHelp("Ctrl-Enter", "write a commit")),
		Rescan: key.NewBinding(key.WithKeys("f5"), key.WithHelp("F5", "rescan")),

		FocusWorkdir: key.NewBinding(key.WithKeys("alt+1"), key.WithHelp("Alt-1", "unstaged")),
		FocusIndex:   key.NewBinding(key.WithKeys("alt+2"), key.WithHelp("Alt-2", "staged")),
		FocusDiff:    key.NewBinding(key.WithKeys("alt+3"), key.WithHelp("Alt-3", "diff")),
		FocusCommit:  key.NewBinding(key.WithKeys("alt+4"), key.WithHelp("Alt-4", "message")),
		FocusCommits: key.NewBinding(key.WithKeys("alt+5"), key.WithHelp("Alt-5", "this branch")),
		NextPane:     key.NewBinding(key.WithKeys("tab"), key.WithHelp("Tab", "next pane")),
		PrevPane:     key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("Shift-Tab", "prev pane")),

		StageKey:    key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "stage")),
		UnstageKey:  key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "unstage")),
		RevertKey:   key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "revert changes")),
		SignOffKey:  key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "sign off")),
		AmendKey:    key.NewBinding(key.WithKeys("A"), key.WithHelp("A", "amend last commit")),
		EditKey:     key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit the file")),
		MarkdownKey: key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "read markdown")),
		PushKey:     key.NewBinding(key.WithKeys("P"), key.WithHelp("P", "push")),
		MergeKey:    key.NewBinding(key.WithKeys("M"), key.WithHelp("M", "local merge")),
		BranchKey:   key.NewBinding(key.WithKeys("N"), key.WithHelp("N", "new branch")),
		CheckoutKey: key.NewBinding(key.WithKeys("K"), key.WithHelp("K", "checkout branch")),
		RescanKey:   key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "rescan")),

		HelpKey:        key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		QuitKey:        key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		Toggle:         key.NewBinding(key.WithKeys(" ", "enter"), key.WithHelp("Space", "stage / unstage")),
		Visual:         key.NewBinding(key.WithKeys("V"), key.WithHelp("V", "visual select")),
		StageLine:      key.NewBinding(key.WithKeys("L"), key.WithHelp("L", "stage this line")),
		Delete:         key.NewBinding(key.WithKeys("D"), key.WithHelp("D", "delete untracked")),
		RevertHunk:     key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "revert hunk")),
		PopKey:         key.NewBinding(key.WithKeys("U"), key.WithHelp("U", "pop commits off")),
		NextHunk:       key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "next hunk")),
		PrevHunk:       key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "previous hunk")),
		LessContextKey: key.NewBinding(key.WithKeys("-"), key.WithHelp("-", "less context")),
		MoreContextKey: key.NewBinding(key.WithKeys("=", "+"), key.WithHelp("=", "more context")),

		Up:       key.NewBinding(key.WithKeys("up", "k")),
		Down:     key.NewBinding(key.WithKeys("down", "j")),
		Left:     key.NewBinding(key.WithKeys("left", "h")),
		Right:    key.NewBinding(key.WithKeys("right", "l")),
		PageUp:   key.NewBinding(key.WithKeys("pgup")),
		PageDown: key.NewBinding(key.WithKeys("pgdown")),
		Home:     key.NewBinding(key.WithKeys("home", "g")),
		End:      key.NewBinding(key.WithKeys("end", "G")),

		Accept: key.NewBinding(key.WithKeys("enter")),
		Cancel: key.NewBinding(key.WithKeys("esc")),
	}
}
