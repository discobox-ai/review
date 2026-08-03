package ui

import "github.com/charmbracelet/bubbles/key"

// keyMap follows one rule, so there is nothing to memorise per command:
//
//	Alt+letter   a command. Works everywhere, including while typing a
//	             commit message.
//	plain key    acts on the focused pane's cursor. Live only in the file
//	             lists and the diff, where it is not text.
//	Ctrl         edits text. difftui binds no Ctrl command at all, so the
//	             message buffer keeps every readline key.
//
// There are exactly three exceptions, all forced by the terminal:
//
//   - Ctrl-C and Ctrl-Q quit, because every terminal program does.
//   - Ctrl-J commits. A terminal sends Ctrl-Enter as 0x0A, which is
//     byte-identical to Ctrl-J, and Ctrl-Enter is the gesture people reach
//     for. Binding Ctrl-J to anything else makes Ctrl-Enter do that instead.
//   - F1 shows help and F5 rescans, as they do in git-gui.
//
// git-gui's own accelerators are mostly Ctrl, and a terminal cannot honour
// them: Ctrl-I is Tab, Ctrl-M is Enter, Ctrl-Enter is Ctrl-J, and Ctrl-A,
// Ctrl-E, Ctrl-K, Ctrl-U, Ctrl-W, Ctrl-D, Ctrl-B, Ctrl-F, Ctrl-N, Ctrl-P and
// Ctrl-T are all readline editing keys in the message buffer. Rather than
// honour some and quietly break others, every command moved to Alt.
type keyMap struct {
	// Commands. Live everywhere.
	Help         key.Binding
	Quit         key.Binding
	Rescan       key.Binding
	Stage        key.Binding
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

	FocusWorkdir key.Binding
	FocusIndex   key.Binding
	FocusDiff    key.Binding
	FocusCommit  key.Binding
	NextPane     key.Binding
	PrevPane     key.Binding

	// Pane keys. Live only in the file lists and the diff.
	//
	// Every command also has a plain letter here, the same letter as its Alt
	// binding. Alt is only strictly needed inside the message buffer, where a
	// letter is text — and on macOS, where Option is not Meta unless the
	// terminal is configured for it.
	StageKey        key.Binding
	StageChangedKey key.Binding
	UnstageKey      key.Binding
	RevertKey       key.Binding
	SignOffKey      key.Binding
	AmendKey        key.Binding
	CommitKey       key.Binding
	PushKey         key.Binding
	MergeKey        key.Binding
	BranchKey       key.Binding
	CheckoutKey     key.Binding
	RescanKey       key.Binding

	HelpKey        key.Binding
	QuitKey        key.Binding
	Toggle         key.Binding
	Mark           key.Binding
	Visual         key.Binding
	StageLine      key.Binding
	Delete         key.Binding
	RevertHunk     key.Binding
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
		Help:   key.NewBinding(key.WithKeys("f1", "alt+h"), key.WithHelp("F1", "help")),
		Quit:   key.NewBinding(key.WithKeys("ctrl+c", "ctrl+q"), key.WithHelp("Ctrl-C", "quit")),
		Rescan: key.NewBinding(key.WithKeys("f5", "alt+r"), key.WithHelp("F5", "rescan")),

		Stage:        key.NewBinding(key.WithKeys("alt+s"), key.WithHelp("Alt-S", "stage")),
		StageChanged: key.NewBinding(key.WithKeys("alt+a"), key.WithHelp("Alt-A", "stage all changed")),
		Unstage:      key.NewBinding(key.WithKeys("alt+u"), key.WithHelp("Alt-U", "unstage")),
		Revert:       key.NewBinding(key.WithKeys("alt+v"), key.WithHelp("Alt-V", "revert changes")),
		SignOff:      key.NewBinding(key.WithKeys("alt+o"), key.WithHelp("Alt-O", "sign off")),
		Amend:        key.NewBinding(key.WithKeys("alt+e"), key.WithHelp("Alt-E", "amend last commit")),
		// Ctrl-J is what a terminal sends for Ctrl-Enter.
		Commit:      key.NewBinding(key.WithKeys("alt+c", "ctrl+j", "ctrl+enter"), key.WithHelp("Alt-C", "commit")),
		Push:        key.NewBinding(key.WithKeys("alt+p"), key.WithHelp("Alt-P", "push")),
		Merge:       key.NewBinding(key.WithKeys("alt+m"), key.WithHelp("Alt-M", "local merge")),
		Branch:      key.NewBinding(key.WithKeys("alt+n"), key.WithHelp("Alt-N", "new branch")),
		Checkout:    key.NewBinding(key.WithKeys("alt+k"), key.WithHelp("Alt-K", "checkout branch")),
		LessContext: key.NewBinding(key.WithKeys("alt+-", "alt+_"), key.WithHelp("Alt--", "less context")),
		MoreContext: key.NewBinding(key.WithKeys("alt+=", "alt++"), key.WithHelp("Alt-=", "more context")),

		FocusWorkdir: key.NewBinding(key.WithKeys("alt+1"), key.WithHelp("Alt-1", "unstaged")),
		FocusIndex:   key.NewBinding(key.WithKeys("alt+2"), key.WithHelp("Alt-2", "staged")),
		FocusDiff:    key.NewBinding(key.WithKeys("alt+3"), key.WithHelp("Alt-3", "diff")),
		FocusCommit:  key.NewBinding(key.WithKeys("alt+4"), key.WithHelp("Alt-4", "message")),
		NextPane:     key.NewBinding(key.WithKeys("tab"), key.WithHelp("Tab", "next pane")),
		PrevPane:     key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("Shift-Tab", "prev pane")),

		StageKey:        key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "stage")),
		StageChangedKey: key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "stage all changed")),
		UnstageKey:      key.NewBinding(key.WithKeys("u"), key.WithHelp("u", "unstage")),
		RevertKey:       key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "revert changes")),
		SignOffKey:      key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "sign off")),
		AmendKey:        key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "amend last commit")),
		CommitKey:       key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "commit")),
		PushKey:         key.NewBinding(key.WithKeys("P"), key.WithHelp("P", "push")),
		MergeKey:        key.NewBinding(key.WithKeys("M"), key.WithHelp("M", "local merge")),
		BranchKey:       key.NewBinding(key.WithKeys("N"), key.WithHelp("N", "new branch")),
		CheckoutKey:     key.NewBinding(key.WithKeys("K"), key.WithHelp("K", "checkout branch")),
		RescanKey:       key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "rescan")),

		HelpKey:        key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		QuitKey:        key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		Toggle:         key.NewBinding(key.WithKeys(" ", "enter"), key.WithHelp("Space", "stage / unstage")),
		Mark:           key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "mark file")),
		Visual:         key.NewBinding(key.WithKeys("V"), key.WithHelp("V", "visual line select")),
		StageLine:      key.NewBinding(key.WithKeys("L"), key.WithHelp("L", "stage this line")),
		Delete:         key.NewBinding(key.WithKeys("D"), key.WithHelp("D", "delete untracked")),
		RevertHunk:     key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "revert hunk")),
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
