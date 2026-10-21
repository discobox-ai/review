package ui

import (
	"reflect"
	"slices"
	"strings"

	"charm.land/bubbles/v2/key"
)

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
// folds a file in the review and unfolds a closed comment. That is one verb reaching different objects, which is not the same
// as a letter meaning two unrelated things; TestNoTwoCommandsShareAKey keeps
// the difference.
//
// Space discloses, and Enter is the same key: it folds the hunk you are
// reading, from wherever in it you have got to, folds the whole file from the
// file's heading, and opens a resolved conversation. Neither ever writes
// anything — c is the only way a comment begins.
//
// a follows the same rule of reach: the hunk from inside a hunk, the file from
// the file's heading.
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
	CardKey     key.Binding
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
	HistoryKey     key.Binding

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

		FocusReview:  key.NewBinding(key.WithKeys("alt+1"), key.WithHelp("Alt-1", "review")),
		FocusHistory: key.NewBinding(key.WithKeys("alt+2"), key.WithHelp("Alt-2", "history")),
		NextPane:     key.NewBinding(key.WithKeys("tab"), key.WithHelp("Tab", "next pane")),
		PrevPane:     key.NewBinding(key.WithKeys("shift+tab"), key.WithHelp("Shift-Tab", "prev pane")),
		// Alt-Z reaches zoom from inside the composer, where z is text.
		// Everywhere else the letter does it, as z does in tmux.
		Zoom: key.NewBinding(key.WithKeys("alt+z"), key.WithHelp("Alt-Z", "zoom the pane")),

		ApproveKey: key.NewBinding(key.WithKeys("a"), key.WithHelp("a", "approve this hunk, or the file from its heading")),
		ApproveAll: key.NewBinding(key.WithKeys("A"), key.WithHelp("A", "approve every file")),
		// Space folds the hunk or the file the cursor is on; F is the same verb
		// reaching every file at once, the way A is to a. Lowercase f is left alone,
		// since finding something in a long review is what will want it.
		FoldAll: key.NewBinding(key.WithKeys("F"), key.WithHelp("F", "fold or open every file")),
		// f and F are a pair: one takes the finished files off the screen, the
		// other folds what is left. Lower case is the bigger hammer, which is
		// the wrong way round — but f is the letter a filter wants, and F was
		// already the fold's.
		HideDone: key.NewBinding(key.WithKeys("f"), key.WithHelp("f", "hide the files you have approved")),
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
		// i for what the change is about: the review card's summary above
		// the stack, put away and brought back.
		CardKey:     key.NewBinding(key.WithKeys("i"), key.WithHelp("i", "hide or show the review card's summary")),
		CheckoutKey: key.NewBinding(key.WithKeys("K"), key.WithHelp("K", "check out a branch")),
		RescanKey:   key.NewBinding(key.WithKeys("R"), key.WithHelp("R", "rescan")),

		HelpKey: key.NewBinding(key.WithKeys("?"), key.WithHelp("?", "help")),
		QuitKey: key.NewBinding(key.WithKeys("q"), key.WithHelp("q", "quit")),
		// The space bar is named rather than spelled: a key reports the text
		// it produced, and a bare space would be indistinguishable from one
		// typed into a field.
		//
		// Enter is the same key. It used to act where Space disclosed, and on
		// a line of the diff that meant opening a comment box — a second way
		// to start a remark, on the key a hand hits without meaning much by
		// it. c is how a comment begins, and nothing else is.
		Toggle:  key.NewBinding(key.WithKeys("space", "enter"), key.WithHelp("Space", "open / fold")),
		Visual:  key.NewBinding(key.WithKeys("V"), key.WithHelp("V", "visual select")),
		ZoomKey: key.NewBinding(key.WithKeys("z"), key.WithHelp("z", "zoom the pane")),

		NextHunk:       key.NewBinding(key.WithKeys("n"), key.WithHelp("n", "next hunk")),
		PrevHunk:       key.NewBinding(key.WithKeys("p"), key.WithHelp("p", "previous hunk")),
		NextThread:     key.NewBinding(key.WithKeys("t"), key.WithHelp("t", "next open comment")),
		PrevThread:     key.NewBinding(key.WithKeys("T"), key.WithHelp("T", "previous open comment")),
		NextFile:       key.NewBinding(key.WithKeys("]"), key.WithHelp("]", "next file")),
		PrevFile:       key.NewBinding(key.WithKeys("["), key.WithHelp("[", "previous file")),
		LessContextKey: key.NewBinding(key.WithKeys("-", "_"), key.WithHelp("-", "less context")),
		MoreContextKey: key.NewBinding(key.WithKeys("=", "+"), key.WithHelp("=", "more context")),

		// H rather than h, which is the arrow a vim hand uses to run the code
		// back to the margin.
		HistoryKey: key.NewBinding(key.WithKeys("H"), key.WithHelp("H", "show or hide the history")),

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

// lacks is what the window's source cannot do, and so what the window must
// not offer.
type lacks struct {
	review   bool // nowhere to keep a review: nothing to approve or comment on
	worktree bool // no working tree: no branch to change, editor or base to pick
}

// without is the key map with the commands the source cannot do switched off,
// and the keys reserved by whatever the window is drawn in taken out of every
// binding that has them. A binding left with no keys at all is switched off
// too, since a binding with none is otherwise only one nothing presses.
//
// A switched off binding matches nothing and is left out of the help and the
// hints, which is the whole of how a command goes missing: nothing that acts
// on one has to ask.
func (k keyMap) without(l lacks, reserved []string) keyMap {
	if l.review {
		for _, b := range []*key.Binding{
			&k.ApproveKey, &k.ApproveAll, &k.CommentKey, &k.ReplyKey,
			&k.ResolveKey, &k.DeleteKey, &k.Post, &k.Visual,
			// Walking the open comments, and hiding what has been
			// approved, have nothing to act on either.
			&k.NextThread, &k.PrevThread, &k.HideDone,
		} {
			b.SetEnabled(false)
		}
	}
	if l.worktree {
		for _, b := range []*key.Binding{&k.BaseKey, &k.EditKey, &k.CheckoutKey} {
			b.SetEnabled(false)
		}
	}
	if len(reserved) == 0 {
		return k
	}
	for _, b := range k.all() {
		keys := slices.DeleteFunc(slices.Clone(b.Keys()), func(s string) bool {
			return slices.Contains(reserved, s)
		})
		if len(keys) == len(b.Keys()) {
			continue
		}
		enabled := b.Enabled()
		b.SetKeys(keys...)
		b.SetEnabled(enabled && len(keys) > 0)
	}
	return k
}

// all is every binding in the map.
func (k *keyMap) all() []*key.Binding {
	v := reflect.ValueOf(k).Elem()
	out := make([]*key.Binding, 0, v.NumField())
	for _, field := range v.Fields() {
		if b, ok := reflect.TypeAssert[*key.Binding](field.Addr()); ok {
			out = append(out, b)
		}
	}
	return out
}

// state reads a key the help or a hint names — spelled the way they spell it,
// "c", "Space", "Ctrl-S" — and reports whether it is a key of the map's at all
// and, if so, whether a binding still switched on has it. An arrow or a count
// is not a key of the map's.
func (k *keyMap) state(name string) (known, on bool) {
	// "Ctrl-S" is ctrl+s, but "-" on its own is the minus key and "K" the
	// letter K, not k.
	want := name
	if len(name) > 1 {
		want = strings.ReplaceAll(strings.ToLower(name), "-", "+")
	}
	full := newKeyMap()
	for _, b := range full.all() {
		if slices.Contains(b.Keys(), want) {
			known = true
			break
		}
	}
	for _, b := range k.all() {
		if b.Enabled() && slices.Contains(b.Keys(), want) {
			return known, true
		}
	}
	return known, false
}
