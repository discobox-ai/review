package ui

import "charm.land/bubbles/v2/key"

// keyMap follows one rule:
//
//	a letter   runs a command, in the panes.
//	Ctrl       edits text, and is never a command.
//
// The letters are the review's own verbs — approve, comment, reply, resolve —
// and each one means the same thing wherever it is pressed. Where a command has
// an "all of them" form it is the same letter in upper case: a and A approve,
// Space and F fold.
//
// A key may reach a different object depending on where the cursor is: Space
// opens a file in the list, folds one in the review, and unfolds a closed
// comment. That is one verb reaching different objects, which is not the same
// as a letter meaning two unrelated things; TestNoTwoCommandsShareAKey keeps
// the difference.
//
// Inside the comment composer every letter is text, so none of the commands
// are reachable from it: Ctrl-Enter posts what was written, Esc throws it away.
//
// Alt is used only to jump straight to a pane, and Tab does the same thing
// without it — which matters on macOS, where Option is not Meta unless the
// terminal has been configured for it.
type keyMap struct {
	// Live everywhere, the composer included.
	Quit   key.Binding
	Help   key.Binding
	Post   key.Binding
	Rescan key.Binding

	FocusFiles   key.Binding
	FocusHistory key.Binding
	FocusReview  key.Binding
	NextPane     key.Binding
	PrevPane     key.Binding
	Zoom         key.Binding

	// Commands. A letter each, live in every pane but the composer.
	ApproveKey  key.Binding
	ApproveAll  key.Binding
	FoldAll     key.Binding
	HideDone    key.Binding
	SplitKey    key.Binding
	SideKey     key.Binding
	CommentKey  key.Binding
	ReplyKey    key.Binding
	ResolveKey  key.Binding
	DeleteKey   key.Binding
	BaseKey     key.Binding
	EditKey     key.Binding
	ViewKey     key.Binding
	ViewOldKey  key.Binding
	MarkdownKey key.Binding
	CheckoutKey key.Binding
	RescanKey   key.Binding
	HelpKey     key.Binding
	QuitKey     key.Binding
	Toggle      key.Binding
	Visual      key.Binding
	ZoomKey     key.Binding

	NextHunk       key.Binding
	PrevHunk       key.Binding
	NextThread     key.Binding
	PrevThread     key.Binding
	NextFile       key.Binding
	PrevFile       key.Binding
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
		// Posting a comment, which is the only command reachable from inside
		// the box and the only one worth reaching from there.
		//
		// Ctrl-S is the one that always arrives. Ctrl-Enter is what a hand
		// trained on the web will try first, and it works wherever the
		// terminal encodes it as a line feed — but plenty of terminals send a
		// carriage return for it, which is byte-identical to Enter and has to
		// stay a newline inside a text field. So the box names Ctrl-S, and
		// Ctrl-Enter is there for the terminals that can tell the two apart.
		//
		// Bubble Tea v2 does report it, on a terminal that answers the key
		// disambiguation the view asks for; Ctrl-J is the line feed a few
		// terminals send for the same chord.
		Post: key.NewBinding(
			key.WithKeys("ctrl+s", "ctrl+j", "ctrl+enter", "alt+enter"),
			key.WithHelp("Ctrl-S", "post the comment")),
		Rescan: key.NewBinding(key.WithKeys("f5"), key.WithHelp("F5", "rescan")),

		FocusFiles:   key.NewBinding(key.WithKeys("alt+1"), key.WithHelp("Alt-1", "files")),
		FocusReview:  key.NewBinding(key.WithKeys("alt+2"), key.WithHelp("Alt-2", "review")),
		FocusHistory: key.NewBinding(key.WithKeys("alt+3"), key.WithHelp("Alt-3", "history")),
		NextPane:     key.NewBinding(key.WithKeys("tab"), key.WithHelp("Tab", "next pane")),
		PrevPane:     key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("Shift-Tab", "prev pane")),
		// Alt-Z reaches zoom from inside the composer, where z is text.
		// Everywhere else the letter does it, as z does in tmux.
		Zoom: key.NewBinding(key.WithKeys("alt+z"), key.WithHelp("Alt-Z", "zoom the pane")),

		ApproveKey: key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "approve this file")),
		ApproveAll: key.NewBinding(key.WithKeys("A"), key.WithHelp("A", "approve every file")),
		// Space folds the file the cursor is in; F is the same verb reaching
		// every file at once, the way A is to a. Lowercase f is left alone,
		// since finding something in a long review is what will want it.
		FoldAll: key.NewBinding(key.WithKeys("F"), key.WithHelp("F", "fold or open every file")),
		// f and F are a pair: one takes the finished files off the screen, the
		// other folds what is left. Lower case is the bigger hammer, which is
		// the wrong way round — but f is the letter a filter wants, and F was
		// already the fold's.
		HideDone: key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "hide the files that are done with")),
		// s stands the two sides of a change beside each other. Neither view
		// is right for everything, so it is a key rather than a setting.
		SplitKey: key.NewBinding(key.WithKeys("s"), key.WithHelp("s", "split the diff, or unify it")),
		// S moves the cursor across a split row, between the line the change
		// took away and the line it put there. It is the one thing a unified
		// diff does for free — every line has a row, so the cursor picks a
		// side by being on one — and the one thing a split row has to be told.
		// Not h and l, which run the code sideways: a split half is narrow, so
		// that is wanted more here than anywhere, not less.
		SideKey:    key.NewBinding(key.WithKeys("S"), key.WithHelp("S", "the other side of a split row")),
		CommentKey: key.NewBinding(key.WithKeys("c"), key.WithHelp("c", "comment on this line")),
		ReplyKey:   key.NewBinding(key.WithKeys("r"), key.WithHelp("r", "reply")),
		ResolveKey: key.NewBinding(key.WithKeys("x"), key.WithHelp("x", "resolve / reopen")),
		DeleteKey:  key.NewBinding(key.WithKeys("X"), key.WithHelp("X", "delete a comment")),
		BaseKey:    key.NewBinding(key.WithKeys("b"), key.WithHelp("b", "choose the base")),
		EditKey:    key.NewBinding(key.WithKeys("e"), key.WithHelp("e", "edit the file")),
		// v reads the file as it now stands and o as it stood before, which
		// is the same pair the diff itself is made of.
		ViewKey:     key.NewBinding(key.WithKeys("v"), key.WithHelp("v", "read the whole file")),
		ViewOldKey:  key.NewBinding(key.WithKeys("o"), key.WithHelp("o", "read it as the base has it")),
		MarkdownKey: key.NewBinding(key.WithKeys("m"), key.WithHelp("m", "read markdown")),
		CheckoutKey: key.NewBinding(key.WithKeys("K"), key.WithHelp("K", "check out a branch")),
		RescanKey:   key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "rescan")),

		HelpKey: key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		QuitKey: key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		// The space bar is named rather than spelled: a key reports the text
		// it produced, and a bare space would be indistinguishable from one
		// typed into a field.
		Toggle:  key.NewBinding(key.WithKeys("space", "enter"), key.WithHelp("Space", "open / fold")),
		Visual:  key.NewBinding(key.WithKeys("V"), key.WithHelp("V", "visual select")),
		ZoomKey: key.NewBinding(key.WithKeys("z"), key.WithHelp("z", "zoom the pane")),

		NextHunk:       key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "next hunk")),
		PrevHunk:       key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "previous hunk")),
		NextThread:     key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "next open comment")),
		PrevThread:     key.NewBinding(key.WithKeys("T"), key.WithHelp("T", "previous open comment")),
		NextFile:       key.NewBinding(key.WithKeys("]"), key.WithHelp("]", "next file")),
		PrevFile:       key.NewBinding(key.WithKeys("["), key.WithHelp("[", "previous file")),
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
