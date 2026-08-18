package ui

import (
	"testing"

	"github.com/charmbracelet/bubbles/key"
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
		{"stage", m.StageKey},
		{"unstage", m.UnstageKey},
		{"revert changes", m.RevertKey},
		{"sign off", m.SignOffKey},
		{"amend", m.AmendKey},
		{"edit", m.EditKey},
		{"push", m.PushKey},
		{"merge", m.MergeKey},
		{"new branch", m.BranchKey},
		{"checkout", m.CheckoutKey},
		{"rescan", m.RescanKey},
		{"help", m.HelpKey},
		{"quit", m.QuitKey},
		{"stage or unstage", m.Toggle},
		{"visual select", m.Visual},
		{"stage line", m.StageLine},
		{"delete untracked", m.Delete},
		{"revert hunk", m.RevertHunk},
		{"pop commits", m.PopKey},
		{"next hunk", m.NextHunk},
		{"previous hunk", m.PrevHunk},
		{"less context", m.LessContextKey},
		{"more context", m.MoreContextKey},
	}
}

// No two commands answer to the same key.
//
// A key may act on whatever is under the cursor — Space stages a file in a
// list and a hunk in the diff, V selects in whichever pane you are in — but
// that is one verb reaching different objects. Two unrelated commands sharing
// a letter is the thing this rules out, and it is easy to reintroduce by
// accident: a letter looks free in the pane you are working on while already
// being spoken for somewhere else.
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
			// Enter doubles as the stage key, which is deliberate: it is the
			// same gesture as Space, not a movement.
			if k == "enter" {
				continue
			}
			if movement[k] {
				t.Errorf("%q moves the cursor and also runs %q", k, b.name)
			}
		}
	}
}
