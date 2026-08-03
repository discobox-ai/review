package ui

import "github.com/charmbracelet/bubbles/key"

// keyMap is layered, because the commit message buffer is a text field and
// most of git-gui's accelerators are also readline editing keys.
//
//  1. Repository commands work everywhere, including while typing a message.
//     They avoid every key bubbles' textarea binds for editing, so Ctrl-A,
//     Ctrl-E, Ctrl-K, Ctrl-U, Ctrl-W, Ctrl-D, Ctrl-B, Ctrl-F, Ctrl-N, Ctrl-P
//     and the Alt word motions still edit the message.
//  2. git-gui's own Ctrl accelerators that collide with those editing keys
//     stay bound everywhere *except* inside the buffer, where editing wins.
//  3. Unmodified letters act on a pane's cursor, so they are live only while a
//     list or the diff has focus and would otherwise just be typed.
//
// Three of git-gui's accelerators cannot be delivered by a terminal at all:
// Ctrl-I is Tab, Ctrl-M is Enter, and Ctrl-Q is XON where flow control is on.
type keyMap struct {
	Quit    key.Binding
	QuitAlt key.Binding
	Help    key.Binding
	Rescan  key.Binding
	Stage   key.Binding
	Toggle  key.Binding

	// Repository commands, live everywhere.
	StageChanged key.Binding
	Unstage      key.Binding
	Revert       key.Binding
	SignOff      key.Binding
	Amend        key.Binding
	Commit       key.Binding
	Push         key.Binding
	Merge        key.Binding
	Branch       key.Binding
	Checkout     key.Binding
	LessContext  key.Binding
	MoreContext  key.Binding

	// git-gui's Ctrl accelerators that double as readline editing keys.
	UnstageCtrl key.Binding
	AmendCtrl   key.Binding
	PushCtrl    key.Binding
	BranchCtrl  key.Binding

	// Unmodified keys, only while a list or the diff has focus.
	StageChangedKey key.Binding
	MergeKey        key.Binding
	LessContextKey  key.Binding
	MoreContextKey  key.Binding
	Delete          key.Binding
	Mark            key.Binding
	Visual          key.Binding
	StageLine       key.Binding

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

	Accept key.Binding
	Cancel key.Binding
}

func newKeyMap() keyMap {
	return keyMap{
		// Ctrl-Q is git-gui's accelerator, but it is XON on terminals with
		// flow control still enabled, so Ctrl-C quits too.
		Quit:    key.NewBinding(key.WithKeys("ctrl+q", "ctrl+c"), key.WithHelp("Ctrl-Q", "quit")),
		QuitAlt: key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		Help:    key.NewBinding(key.WithKeys("f1"), key.WithHelp("F1", "help")),
		Rescan:  key.NewBinding(key.WithKeys("f5", "ctrl+r"), key.WithHelp("F5", "rescan")),

		// Ctrl-T is the one editing key deliberately taken: it transposes two
		// characters, which no one reaches for in a commit message, and it is
		// git-gui's Stage To Commit.
		Stage:  key.NewBinding(key.WithKeys("ctrl+t"), key.WithHelp("Ctrl-T", "stage")),
		Toggle: key.NewBinding(key.WithKeys(" ", "enter"), key.WithHelp("Space", "toggle staged")),

		StageChanged: key.NewBinding(key.WithKeys("alt+i"), key.WithHelp("Alt-I", "stage changed")),
		Unstage:      key.NewBinding(key.WithKeys("alt+u"), key.WithHelp("Alt-U", "unstage")),
		Revert:       key.NewBinding(key.WithKeys("ctrl+j"), key.WithHelp("Ctrl-J", "revert changes")),
		SignOff:      key.NewBinding(key.WithKeys("ctrl+s"), key.WithHelp("Ctrl-S", "sign off")),
		Amend:        key.NewBinding(key.WithKeys("alt+e"), key.WithHelp("Alt-E", "amend last commit")),
		Commit:       key.NewBinding(key.WithKeys("ctrl+enter", "alt+c"), key.WithHelp("Alt-C", "commit")),
		Push:         key.NewBinding(key.WithKeys("alt+p"), key.WithHelp("Alt-P", "push")),
		Merge:        key.NewBinding(key.WithKeys("alt+m"), key.WithHelp("Alt-M", "local merge")),
		Branch:       key.NewBinding(key.WithKeys("alt+b"), key.WithHelp("Alt-B", "create branch")),
		Checkout:     key.NewBinding(key.WithKeys("ctrl+o"), key.WithHelp("Ctrl-O", "checkout branch")),
		LessContext:  key.NewBinding(key.WithKeys("alt+-", "alt+_"), key.WithHelp("Alt--", "less context")),
		MoreContext:  key.NewBinding(key.WithKeys("alt+=", "alt++"), key.WithHelp("Alt-=", "more context")),

		// git-gui's keys for the same commands, everywhere but the buffer.
		UnstageCtrl: key.NewBinding(key.WithKeys("ctrl+u"), key.WithHelp("Ctrl-U", "unstage")),
		AmendCtrl:   key.NewBinding(key.WithKeys("ctrl+e"), key.WithHelp("Ctrl-E", "amend last commit")),
		PushCtrl:    key.NewBinding(key.WithKeys("ctrl+p"), key.WithHelp("Ctrl-P", "push")),
		BranchCtrl:  key.NewBinding(key.WithKeys("ctrl+n"), key.WithHelp("Ctrl-N", "create branch")),

		StageChangedKey: key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "stage changed")),
		MergeKey:        key.NewBinding(key.WithKeys("M"), key.WithHelp("M", "local merge")),
		LessContextKey:  key.NewBinding(key.WithKeys("-"), key.WithHelp("-", "less context")),
		MoreContextKey:  key.NewBinding(key.WithKeys("=", "+"), key.WithHelp("=", "more context")),
		Delete:          key.NewBinding(key.WithKeys("D"), key.WithHelp("D", "delete untracked")),
		Mark:            key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "mark file")),
		Visual:          key.NewBinding(key.WithKeys("V"), key.WithHelp("V", "visual line select")),
		StageLine:       key.NewBinding(key.WithKeys("L"), key.WithHelp("L", "stage this line")),

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

		Accept: key.NewBinding(key.WithKeys("enter")),
		Cancel: key.NewBinding(key.WithKeys("esc")),
	}
}
