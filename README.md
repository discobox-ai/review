# difftui

A console reimplementation of **git gui**, the Tcl/Tk commit tool that ships
with git. Same window, same panes, same accelerators — driven from a terminal
with [Bubble Tea](https://github.com/charmbracelet/bubbletea) and
[Lip Gloss](https://github.com/charmbracelet/lipgloss).

```
Current Branch: main                                        difftui · F1 help · Ctrl-Q quit
 Unstaged Changes               2 files │ Modified, not staged   File: foo.txt   hunk 1/1
  ✎ foo.txt                             │    @@ -1,5 +1,5 @@
  + new.txt                             │  1   one
                                        │  2  -two
                                        │  2 + TWO
                                        │  3   three
 Staged Changes (Will Commit)    1 file │  4   four
  ✔ bar.txt                             │  5   five
                                        │
                                        │
                                        │
Space stage · s stage · a stage all · v revert · m mark      U:2  S:1  ctx:5
```

## Install

```sh
go install github.com/discobox-ai/difftui@latest
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
| Top bar | `Current Branch: <name>`, and `[amending]` when that is on |
| Left, upper | **Unstaged Changes** — the working directory vs the index |
| Left, lower | **Staged Changes (Will Commit)** — the index vs HEAD |
| Right | The diff for the selected file, with its status in the header |
| Bottom bar | Status messages, file counts, current diff context |

git gui keeps a commit message buffer and a column of buttons on screen at all
times. Here the message is a composer that opens on `Ctrl-Enter` (see
[Committing](#committing)) and the buttons are just keys, so the diff gets the
whole right hand column.

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
| `o` | sign off | `e` | amend last commit |
| `R` | rescan (also `F5`) | `-` / `=` | less / more diff context |
| `P` | push | `M` | local merge / abort merge |
| `N` | new branch | `K` | checkout branch |

`Ctrl-Enter` writes a commit. `Ctrl-C` or `q` quits, `F1` or `?` shows the keys.

**Ctrl is never a command.** The commit composer keeps every readline key:
`Ctrl-A`, `Ctrl-E`, `Ctrl-K`, `Ctrl-U`, `Ctrl-W`, `Ctrl-D`, `Ctrl-B`, `Ctrl-F`,
`Ctrl-N`, `Ctrl-P`, `Ctrl-T`.

### Committing

`Ctrl-Enter` opens the message composer. It is a full text field, so every
readline key edits it.

- **`Ctrl-Enter` again** asks *"Commit N staged files to `<branch>`?"*, answered
  `y` or `n`. Committing is two deliberate steps; nothing happens on the first.
- **`Esc`** closes the composer and **keeps what you typed**, so going back to
  read the diff costs nothing. It is still there when you reopen — and it
  survives quitting difftui entirely, see [Saved state](#saved-state).
- **`n`** or `Esc` at the question returns to editing rather than closing.

Sign off with `o` and switch to amend with `e` *before* opening it; both change
the message the composer will show. When amending, the top bar reads
`[amending]` and the composer is titled `Amend Last Commit`.

The three things git gui refuses — an empty message, nothing staged, unmerged
files — are reported inside the composer rather than as another box on top of
it, and clear as soon as you carry on typing.

### Panes

| Key | Action |
| --- | --- |
| `Tab`, `Shift-Tab` | Cycle focus |
| `Alt-1` … `Alt-3` | Jump straight to unstaged / staged / diff |
| `←` `↓` `↑` `→`, `h` `j` `k` `l` | Move, crossing into the neighbouring pane at the edges |

On startup the first file in **Unstaged Changes** is focused with its diff
already loaded, so the command letters are live immediately.

The three panes form a grid, and navigation walks it:

```
Unstaged │
─────────┤ Diff
Staged   │
```

Moving down past the last file in **Unstaged Changes** carries on into
**Staged Changes** at its first row; moving up from the top of the staged list
goes back to the bottom of the unstaged one.

`→` from **either** file list goes to the diff. The diff remembers which list it
is showing — git gui tracks the same thing as `current_diff_side` — so `←` comes
back to *that* list with the same file still selected.

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
left are `Alt-1` … `Alt-3` for jumping straight to a pane, and `Tab` does the
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

`e` switches to amending. The top bar then reads `[amending]`, and the composer
is titled `Amend Last Commit`.

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
| `internal/git/system.go` | The whole system boundary: running git, and file access |
| `internal/git/git.go` | Every git invocation, the file state table and the scan |
| `internal/git/patch.go` | Unified diff parsing, and building hunk/line patches |
| `internal/ui/model.go` | The Bubble Tea model: layout, keys, actions |
| `internal/ui/keys.go` | Every binding, and the one rule they follow |
| `internal/ui/filelist.go` | The two file panes |
| `internal/ui/diffview.go` | The diff pane, hunk cursor, line selection and row drawing |
| `internal/ui/syntax.go` | Lexing a hunk and the syntax palette |
| `internal/ui/emphasis.go` | Tabs, and finding the characters that changed |
| `internal/ui/dialog.go` | The modal layer standing in for git gui's popups |
| `internal/ui/theme.go` | The palette, echoing Tk git gui's colours |
| `internal/ui/util.go` | Width-aware padding and truncation |

Hunk and line staging work by rebuilding a minimal patch and feeding it to
`git apply --cached`, reversing it to unstage — the same trick git gui uses.

### The system boundary

Everything difftui does outside its own process goes through one interface,
`git.System` in `internal/git/system.go`: running git, and the few files it
reads directly because git has no command for them (`MERGE_MSG`, the commit
template, its own saved commit message). `Local` is the implementation that
runs git as a child process and reads from the local disk, and `git.OpenWith`
takes any other.

A test walks the whole module and fails if anything outside `system.go` runs a
command or touches a file, so the boundary cannot erode by accident. Another
drives a repository entirely through a fake `System` to show the interface is
sufficient on its own.

Process I/O — `os.Stderr`, `os.Exit` in `main.go` — is deliberately not behind
it: it is how difftui talks to its own terminal, not to a repository.

## Tests

```sh
make test
```

The `internal/git` tests build throwaway repositories and exercise the real
thing: staging a single hunk out of two, unstaging one in reverse, staging one
line out of several, reverting a hunk from the working tree, amending, and
merge conflicts. The `internal/ui` tests drive the model with synthetic key
messages and assert on the rendered frame.

## License

Apache License 2.0 — see [LICENSE](LICENSE).
