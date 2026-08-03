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
| `Tab`, `Shift-Tab` | Cycle panes |

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
| `↑`/`↓`, `k`/`j` | Move, loading the diff as you go |
| `Space` | Stage from the unstaged list, unstage from the staged list |
| `m` | Mark a file, so a command acts on several at once |
| `D` | Delete untracked files |

### Diff pane

| Key | git gui command |
| --- | --- |
| `Space`, `Ctrl-T` | Stage Hunk For Commit |
| `Ctrl-U` | Unstage Hunk From Commit |
| `v` | Start or clear a line selection |
| `S` | Stage / Unstage Lines |
| `r` | Revert Hunk |
| `n` / `p` | Next / previous hunk |

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
act on, and `v` starts the line selection that `S` stages.

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
