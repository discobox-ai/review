package ui

import (
	"testing"

	"charm.land/bubbles/v2/key"
)

// commandBindings is every binding that runs a command, as opposed to moving
// the cursor or answering a dialog. These are the ones a collision would hurt:
// two commands on one letter means the letter does different things depending
// on where you are, which is exactly what the key scheme is trying to avoid.
func commandBindings(m keyMap) []struct {
	name string
	key.Binding
} {
	return []struct {
		name string
		key.Binding
	}{
		{"approve", m.ApproveKey},
		{"approve everything", m.ApproveAll},
		{"fold everything", m.FoldAll},
		{"hide the finished files", m.HideDone},
		{"split the diff", m.SplitKey},
		{"cross a split row", m.SideKey},
		{"comment", m.CommentKey},
		{"reply", m.ReplyKey},
		{"resolve", m.ResolveKey},
		{"delete a comment", m.DeleteKey},
		{"choose the base", m.BaseKey},
		{"edit", m.EditKey},
		{"read the whole file", m.ViewKey},
		{"read the version before", m.ViewOldKey},
		{"read markdown", m.MarkdownKey},
		{"checkout", m.CheckoutKey},
		{"rescan", m.RescanKey},
		{"help", m.HelpKey},
		{"quit", m.QuitKey},
		{"open or fold", m.Toggle},
		{"act on this", m.Act},
		{"visual select", m.Visual},
		{"zoom", m.ZoomKey},
		{"next hunk", m.NextHunk},
		{"previous hunk", m.PrevHunk},
		{"next open comment", m.NextThread},
		{"previous open comment", m.PrevThread},
		{"next file", m.NextFile},
		{"previous file", m.PrevFile},
		{"less context", m.LessContextKey},
		{"more context", m.MoreContextKey},
		{"show or hide the history", m.HistoryKey},
	}
}

// No two commands answer to the same key.
//
// A key may act on whatever is under the cursor — Space opens a file in the
// list, folds one in the review, and unfolds a closed comment — but that is
// one verb reaching different objects. Space discloses and never writes;
// Enter is the one that acts, and on a line of the diff that means a comment. Two unrelated commands sharing a letter
// is the thing this rules out, and it is easy to reintroduce by accident: a
// letter looks free in the pane you are working on while already being spoken
// for somewhere else.
func TestNoTwoCommandsShareAKey(t *testing.T) {
	seen := map[string]string{}
	for _, b := range commandBindings(newKeyMap()) {
		for _, k := range b.Keys() {
			if other, dup := seen[k]; dup {
				t.Errorf("%q is bound to both %q and %q", k, other, b.name)
				continue
			}
			seen[k] = b.name
		}
	}
}

// Every command is reachable by a key, and says so in the help.
func TestEveryCommandIsBoundAndDescribed(t *testing.T) {
	for _, b := range commandBindings(newKeyMap()) {
		if len(b.Keys()) == 0 {
			t.Errorf("%q has no key", b.name)
		}
		h := b.Help()
		if h.Key == "" || h.Desc == "" {
			t.Errorf("%q has no help text: %+v", b.name, h)
		}
	}
}

// The command keys must not collide with the movement keys either, or a
// command would fire while walking a list.
func TestCommandsDoNotCollideWithMovement(t *testing.T) {
	m := newKeyMap()
	movement := map[string]bool{}
	for _, b := range []key.Binding{m.Up, m.Down, m.Left, m.Right, m.PageUp, m.PageDown, m.Home, m.End} {
		for _, k := range b.Keys() {
			movement[k] = true
		}
	}
	for _, b := range commandBindings(m) {
		for _, k := range b.Keys() {
			// Enter acts on the row the cursor is on, which is a command
			// rather than a movement — Space is its milder half.
			if k == "enter" {
				continue
			}
			if movement[k] {
				t.Errorf("%q moves the cursor and also runs %q", k, b.name)
			}
		}
	}
}
