# difftui

A console reimplementation of **git gui**, the Tcl/Tk commit tool that ships
with git. Same window, same panes, same accelerators — driven from a terminal
with [Bubble Tea](https://github.com/charmbracelet/bubbletea) and
[Lip Gloss](https://github.com/charmbracelet/lipgloss).

```
Current Branch: main                                        difftui · F1 help · Ctrl-Q quit
 Unstaged Changes               2 files │ Modified, not staged   File: foo.txt        hunk 1/2
  ✎ foo.txt                             │@@ -1,5 +1,5 @@
  + new.txt                             │ one
                                        │-two
                                        │+TWO
                                        │ three
 Staged Changes (Will Commit)    1 file │ four
  ✔ bar.txt                             │
                                        │Commit Message:              [ ] Amend Last Commit
                                        │[ Rescan        F5 ] the commit message goes here
                                        │[ Stage Changed  i ]
                                        │[ Sign Off      ^S ]
                                        │[ Commit        ^↵ ]
                                        │[ Push          ^P ]
Space stage hunk · v select lines · S stage lines · r revert hunk    U:2  S:1  ctx:5
```

## Install

```sh
go install github.com/obot-platform/difftui@latest
```

Or from a checkout:

```sh
make build     # ./difftui
make install   # $GOBIN/difftui
```

## Run

```sh
difftui           # the repository containing the current directory
difftui ~/src/foo # a specific working tree
```

## Layout

The window reproduces git gui's paned layout exactly:

| Region | Contents |
| --- | --- |
| Top bar | `Current Branch: <name>` |
| Left, upper | **Unstaged Changes** — the working directory vs the index |
| Left, lower | **Staged Changes (Will Commit)** — the index vs HEAD |
| Right, upper | The diff for the selected file, with its status in the header |
| Right, lower | The button column beside the commit message buffer |
| Bottom bar | Status messages, file counts, current diff context |

Files are routed to the two lists using git gui's own rule: a path appears in
the staged list when its index status is neither unmodified nor unmerged, and
in the unstaged list when its working directory status is not unmodified. A
partially staged file (`MM`) therefore shows up in **both**, exactly as it does
in git gui.

The status icons follow git gui's bitmap table:

| Icon | Meaning |
| --- | --- |
| `▪` | Unchanged on this side / staged addition |
| `✎` | Modified in the working directory |
| `✔` | Staged for commit |
| `✘` | Staged for removal |
| `?` | Missing from the working directory |
| `+` | Untracked |
| `⇄` | File type changed |
| `!` | Requires merge resolution |

## Keys

difftui keeps git gui's accelerators wherever a terminal can deliver them.

### Panes

| Key | Action |
| --- | --- |
| `Alt-1` … `Alt-4` | Focus unstaged / staged / diff / commit message |
| `Tab`, `Shift-Tab` | Cycle focus, including the Amend checkbox |
| `←` `↓` `↑` `→`, `h` `j` `k` `l` | Move, crossing into the neighbouring pane at the edges |

The four panes form a grid, and navigation walks it:

```
Unstaged │ Diff
─────────┼────────
Staged   │ Commit
```

Moving down past the last file in **Unstaged Changes** carries on into
**Staged Changes** at its first row; moving up from the top of the staged list
goes back to the bottom of the unstaged one. The same applies on the right,
between the diff and the commit message.

`→` from **either** file list goes to the diff. The diff remembers which list it
is showing — git gui tracks the same thing as `current_diff_side` — so `←` comes
back to *that* list with the same file still selected. Select something staged,
go right to read it, come back left, and you are still on it; likewise for
unstaged. `←` out of the commit message follows the same rule.

On startup the first file in **Unstaged Changes** is focused with its diff
already loaded, so the single letter keys are live immediately. (git gui starts
in the message buffer instead; this is friendlier in a terminal.)

In the commit message buffer only the arrow keys move out, and only from the
very start of the text (`↑` on the first line, `←` at the first column) — so
`h`/`j`/`k`/`l` stay typable there.

### Commit menu

| Key | git gui command |
| --- | --- |
| `F5`, `Ctrl-R` | Rescan |
| `Ctrl-T` | Stage To Commit |
| `i` | Stage Changed Files To Commit |
| `Ctrl-U` | Unstage From Commit |
| `Ctrl-J` | Revert Changes |
| `Ctrl-S` | Sign Off |
| `Ctrl-E` | Amend Last Commit |
| `Ctrl-Enter`, `Alt-C` | Commit |
| `-` / `=` | Show Less / More Context |

### File lists

| Key | Action |
| --- | --- |
| `↑`/`↓`, `k`/`j` | Move, loading the diff as you go; leaves the pane at the ends |
| `←`/`→`, `h`/`l` | `→` to the diff; `←` from the diff returns here |
| `Space` | Stage from the unstaged list, unstage from the staged list |
| `m` | Mark a file, so a command acts on several at once |
| `D` | Delete untracked files |

### Diff pane

| Key | git gui command |
| --- | --- |
| `Space`, `Ctrl-T` | Stage Hunk For Commit |
| `Ctrl-U` | Unstage Hunk From Commit |
| `V` | Visual line select (see below) |
| `L` | Stage / Unstage Line — just the one under the cursor |
| `r` | Revert Hunk |
| `n` / `p` | Next / previous hunk |
| `↑`/`↓`, `k`/`j` | Move by line; `↓` past the end enters the commit message |

### Branch and remote

| Key | Action |
| --- | --- |
| `Ctrl-N` | Create Branch |
| `Ctrl-O` | Checkout Branch |
| `M` | Local Merge, or Abort Merge when one is in progress |
| `Ctrl-P` | Push |
| `F1`, `?` | Help |
| `Ctrl-Q`, `Ctrl-C` | Quit (`q` also quits from a list or the diff) |

### Deliberate differences

Three of git gui's accelerators cannot survive a terminal, so they moved:

- **`Ctrl-I`** (Stage Changed Files To Commit) is byte-identical to `Tab`. It
  is bound to `i` instead, on the lists and the diff pane.
- **`Ctrl-M`** (Local Merge) is byte-identical to `Enter`. It is bound to `M`.
- **`Ctrl-Q`** is XON on terminals that still have flow control enabled, so
  `Ctrl-C` quits as well.

Single letter keys are only live while a list or the diff has focus — the
commit message buffer swallows them so you can type normally.

Since a terminal has no right mouse button, git gui's diff context menu is on
the keyboard: the hunk under the cursor is the one `Space`, `Ctrl-U` and `r`
act on.

### Visual line select

`V` in the diff behaves like vim's visual line mode. It starts with the current
line selected; `↑`/`↓` (or `k`/`j`) extend the selection; `V` again or `Esc`
cancels. `Space` or `Enter` then stages the selected lines — the same keys that
stage in the file lists — or unstages them when the diff is showing the staged
side.

While a selection is open the status bar switches to say so, counting the lines
and listing what you can do, and the cursor stays inside the diff rather than
running off into the next pane.

`L` stages just the line under the cursor, without entering visual mode.

## Syntax highlighting

Diffs are syntax highlighted, using [chroma](https://github.com/alecthomas/chroma)
to pick a lexer from the file's name. Files chroma does not recognise are shown
plain, rather than run through a fallback lexer that would colour nothing.

Two details make it read well rather than just colourful:

- **Each side of a hunk is lexed as one text** — context plus removed lines for
  the old side, context plus added lines for the new one. A lexer is a state
  machine, so fed one line at a time it restarts on every line and anything
  spanning lines (block comments, raw strings) gets coloured as though it began
  where it did not. A hunk is as much context as a diff has.
- **The palette is short and deliberately quiet.** Keywords, types, literals and
  comments are coloured; plain identifiers and punctuation are left in the
  line's own foreground, because colouring everything emphasises nothing. The
  colours also stay clear of the diff's own green and red, so a string never
  lands green-on-green.

Everything a token does not cover keeps the line's add/remove colour, so the
shape of the diff still reads at a glance.

## Amending

The `[ ] Amend Last Commit` checkbox in the commit message header is git gui's.
`Ctrl-E` toggles it from anywhere, or `Tab` to it — it is a focus stop of its
own, sitting between the diff and the message buffer where it is drawn, so
`↑`/`↓` reach it too — and press `Space` or `Enter`. Turning it on loads HEAD's message into the buffer;
turning it off puts back whatever you had been typing. Committing while it is
ticked rewrites HEAD instead of adding a commit, and the label above the buffer
changes to `Amended Commit Message:` so it is obvious which mode you are in.

## Saved state

difftui stashes the commit message buffer in the git directory, the way git gui
does, so quitting and reopening does not lose what you were writing:

| File | Written |
| --- | --- |
| `.git/GITGUI_MSG` | On a clean exit |
| `.git/GITGUI_BCK` | Every two seconds while the buffer has unsaved edits |

These are git gui's own filenames, so the two tools hand the buffer back and
forth: type a message here, quit, open `git gui`, and it is still there.

On startup the backup wins unless the saved message is newer, in which case the
stale backup is dropped. A successful commit deletes both — the message lives in
the commit now. An **amend** message is deliberately not saved on exit, since it
belongs to a commit that already exists.

When there is nothing stashed, the buffer is seeded from the first of
`.git/MERGE_MSG`, `.git/SQUASH_MSG`, or `commit.template` that exists — git
gui's order.

## Multi-file commands

git gui lets you Ctrl-click several files and act on all of them. Here, `m`
marks a file (shown with a `•`); staging, unstaging and reverting then apply to
every marked file. With nothing marked they apply to the file under the cursor.

## What it does

- Stage, unstage and revert whole files
- Stage, unstage and revert individual **hunks**
- Stage and unstage individual **lines** within a hunk
- Commit, amend, and sign off
- Show less/more diff context on the fly
- Create and check out branches, merge, abort a merge, push
- Refuses to commit with unmerged files or an empty message, like git gui
- Handles a repository with no commits yet: the initial-commit diff, and
  unstaging via `rm --cached`

## Layout of the source

| Path | Contents |
| --- | --- |
| `main.go` | Flag parsing and program startup |
| `internal/git/git.go` | Every git invocation, and git gui's file state table |
| `internal/git/patch.go` | Unified diff parsing, and building hunk/line patches |
| `internal/ui/model.go` | The Bubble Tea model: layout, keys, actions |
| `internal/ui/filelist.go` | The two file panes |
| `internal/ui/diffview.go` | The diff pane, hunk cursor and line selection |
| `internal/ui/syntax.go` | Lexing a hunk and the highlighting palette |
| `internal/ui/dialog.go` | The modal layer standing in for git gui's popups |

Hunk and line staging work by rebuilding a minimal patch and feeding it to
`git apply --cached`, reversing it to unstage — the same trick git gui uses.

## Tests

```sh
make test
```

The `internal/git` tests build throwaway repositories and exercise the real
thing: staging a single hunk out of two, unstaging one in reverse, staging one
line out of several, reverting a hunk from the working tree, amending, and
merge conflicts. The `internal/ui` tests drive the model with synthetic key
messages and assert on the rendered frame.
