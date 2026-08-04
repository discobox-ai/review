# difftui

A console reimplementation of **git gui**, the Tcl/Tk commit tool that ships
with git. Same window, same panes, same accelerators — driven from a terminal
with [Bubble Tea](https://github.com/charmbracelet/bubbletea) and
[Lip Gloss](https://github.com/charmbracelet/lipgloss).

```
Current Branch: main                                        difftui · F1 help · Ctrl-Q quit
 Unstaged Changes               2 files │ Modified, not staged   File: foo.txt        hunk 1/2
  ✎ foo.txt                             │@@ -1,5 +1,5 @@
  + new.txt                             │  one
                                        │ -two
                                        │+ TWO
                                        │  three
 Staged Changes (Will Commit)    1 file │  four
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

### One rule

Every command is **one letter**, pressed in a file list or the diff.

| | | | |
| --- | --- | --- | --- |
| `s` | stage | `u` | unstage |
| `a` | stage all changed | `v` | revert changes |
| `c` | commit | `e` | amend last commit |
| `o` | sign off | `R` | rescan (also `F5`) |
| `P` | push | `M` | local merge / abort merge |
| `N` | new branch | `K` | checkout branch |
| `-` | less diff context | `=` | more diff context |

In the commit message a letter is just text, so press `Tab` to leave the buffer
first. **`Ctrl-Enter` commits from anywhere** — the one command worth having
without leaving. `Ctrl-C` or `q` quits, `F1` or `?` shows the keys.

**Ctrl is never a command.** The commit message keeps every readline key:
`Ctrl-A`, `Ctrl-E`, `Ctrl-K`, `Ctrl-U`, `Ctrl-W`, `Ctrl-D`, `Ctrl-B`, `Ctrl-F`,
`Ctrl-N`, `Ctrl-P`, `Ctrl-T`.

### Panes

| Key | Action |
| --- | --- |
| `Tab`, `Shift-Tab` | Cycle focus, including the Amend checkbox |
| `Alt-1` … `Alt-4` | Jump straight to unstaged / staged / diff / message |
| `←` `↓` `↑` `→`, `h` `j` `k` `l` | Move, crossing into the neighbouring pane at the edges |

On startup the first file in **Unstaged Changes** is focused with its diff
already loaded, so the command letters are live immediately.

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
back to *that* list with the same file still selected.

In the commit message buffer only the arrow keys move out, and only from the
very start of the text (`↑` on the first line, `←` at the first column) — so
`h`/`j`/`k`/`l` stay typable there.

### On a file

| Key | Action |
| --- | --- |
| `Space` | Stage, or unstage from the staged list |
| `m` | Mark a file, so a command acts on several at once |
| `D` | Delete untracked files |

### In the diff

| Key | Action |
| --- | --- |
| `Space` | Stage the hunk under the cursor |
| `V` | Visual line select (see below) |
| `L` | Stage just the line under the cursor |
| `r` | Revert the hunk |
| `n` / `p` | Next / previous hunk |

On the staged side each of these unstages instead.

#### Visual line select

`V` behaves like vim's visual line mode. It starts with the current line
selected; `↑`/`↓` (or `k`/`j`) extend the selection; `V` again or `Esc` cancels.
`Space` or `Enter` then stages the selected lines — the same key that stages in
the file lists.

While a selection is open the status bar switches to say so, counting the lines,
and the cursor stays inside the diff rather than running off into the next pane.

### Why letters, and not git gui's Ctrl keys

A terminal cannot deliver most of them:

- **`Ctrl-I`** is byte-identical to `Tab`.
- **`Ctrl-M`** is byte-identical to `Enter`.
- **`Ctrl-Enter`** arrives as **`Ctrl-J`** — which is why `Ctrl-J` commits here.
- **`Ctrl-A`, `-E`, `-K`, `-U`, `-W`, `-D`, `-B`, `-F`, `-N`, `-P`, `-T`** are
  all readline editing keys in the message buffer.

Honouring some and silently breaking the others is worse than moving them all,
so commands are bare letters.

Letters also need no modifier, which sidesteps macOS: Option is not Meta there
unless the terminal is configured for it, so an `Alt`-based scheme would arrive
as accented characters. (Option never maps to Cmd either, and Cmd is intercepted
by the terminal itself, so it cannot be bound at all.) The only `Alt` bindings
left are `Alt-1` … `Alt-4` for jumping straight to a pane, and `Tab` does the
same thing without them.

## How a diff is drawn

Four things are shown at once, on channels that do not fight each other.

**The background is the diff.** An added line is a green band, a removed line a
red one, running to the right margin so changes read as solid blocks. Context
lines have no band at all.

**A stronger shade marks what actually changed.** When a run of removed lines is
followed by an equally long run of added ones, that is an edit of those lines in
order, and the characters that differ get a brighter shade of the same colour:

```
  6  -    const prefix = "hello, "         ← "hello" on a brighter red
  6 +     const prefix = "greetings, "     ← "greetings" on a brighter green
```

Anything else is a pure insertion or deletion, where there is no counterpart to
compare against and nothing to mark. It is also dropped when more than half the
line changed — at that point the whole line is the change, and marking it says
nothing.

**The foreground is the code**, syntax highlighted with
[chroma](https://github.com/alecthomas/chroma). Two details make it read well
rather than just colourful:

- Each side of a hunk is lexed as one text — context plus removed lines for the
  old side, context plus added lines for the new one. A lexer is a state
  machine, so fed one line at a time it restarts on every line and anything
  spanning lines (block comments, raw strings) is coloured as though it began
  where it did not.
- The palette is short and deliberately quiet. Keywords, types, literals and
  comments are coloured; plain identifiers and punctuation keep the default
  foreground, because colouring everything emphasises nothing. The colours also
  stay clear of the bands, so a string never lands green-on-green.

Files chroma does not recognise are shown plain, rather than run through a
fallback lexer that would colour nothing.

**The cursor and the selection live in a one column bar at the left edge**,
outside the code. The background is already carrying the diff, so a cursor
highlight drawn that way would fight it: gold marks the cursor row, blue marks
the rows in a `V` selection, and the bar column is always present so rows never
shift sideways.

### The gutter

A unified diff puts `+` and `-` in the same column, so an added line's code
starts immediately after its marker. difftui gives each side a column of its
own:

```
  context
 -removed
+ added
```

An addition always has a space between the marker and the code, so the line
reads like the file itself rather than like a patch. A deletion sits tight
against its marker instead — which costs little, since the removed side is the
one you skim.

### Line numbers and tabs

Each line is numbered on its own side — the old number for a removal, the new
one for an addition — in a column sized to the largest number in the diff so it
does not jitter.

Tabs are expanded to four columns before anything measures, tokenises or
highlights a line, so the syntax offsets, the emphasis offsets and the padding
all count the same columns. It also stops a tab rendering as an unpainted gap
in the middle of a band.

## Amending

The `[ ] Amend Last Commit` checkbox in the commit message header is git gui's.
`Ctrl-E` toggles it from anywhere, or `Tab` to it — it is a focus stop of its
own, sitting between the diff and the message buffer where it is drawn, so
`↑`/`↓` reach it too — and press `Space` or `Enter`.

Switching to amend also changes what the file lists mean, the way git gui does
it. git gui keeps a `PARENT` alongside `HEAD`: normally they are the same, but
while amending `PARENT` becomes HEAD's *own* parent, and the staged list is the
index measured against it. So **Staged Changes** shows everything the rewritten
commit will contain — the original commit's files plus anything you add — rather
than appearing empty. The staged diff and unstaging follow the same revision, so
unstaging a file there drops it out of the amended commit instead of resetting
it to the commit being rewritten.

Toggling amend never touches the index; it only changes what the index is
compared against, so anything staged beforehand survives the switch. Turning it on loads HEAD's message into the buffer;
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
| `internal/git/git.go` | Every git invocation, the file state table and the scan |
| `internal/git/patch.go` | Unified diff parsing, and building hunk/line patches |
| `internal/ui/model.go` | The Bubble Tea model: layout, keys, actions |
| `internal/ui/filelist.go` | The two file panes |
| `internal/ui/diffview.go` | The diff pane, hunk cursor, line selection and row drawing |
| `internal/ui/syntax.go` | Lexing a hunk and the syntax palette |
| `internal/ui/emphasis.go` | Tabs, and finding the characters that changed |
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
