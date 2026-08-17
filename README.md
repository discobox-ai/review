# difftui

A console reimplementation of **git gui**, the Tcl/Tk commit tool that ships
with git. Same window, same panes, same accelerators — driven from a terminal
with [Bubble Tea](https://github.com/charmbracelet/bubbletea) and
[Lip Gloss](https://github.com/charmbracelet/lipgloss).

Ctrl-Enter has been pressed once here, which is the only thing that brings the
commit message up:

```
Current Branch: main  ·  base: origin/main 80e97d7 ↑3 ↓0    difftui · F1 help · Ctrl-Q quit
 Unstaged Changes      2 files │ Modified, not staged   File: foo.txt              hunk 1/1
  ✎ foo.txt                    │     @@ -1,5 +1,5 @@
  + new.txt                    │   1   one
                               │   2  -two
                               │   2 + TWO
                               │   3   three
                               │   4   four
                               │   5   five
                               │
 Staged Changes (Will Commit)  │
  ✔ bar.txt                    │ Commit Message                               1 file staged
                               │ Rename two to TWO
                               │
 This Branch         3 commits │
  ● working tree               │
  51ae6f4 Document it and drop…│
  5d7241d Add the beta module  │
Ctrl-Enter commits · Tab or Esc leaves the message · F1 keys                 U:2  S:1  ctx:5
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
| Top bar | `Current Branch: <name>`, the [merge base](#the-merge-base), and `[amending]` when that is on |
| Left, upper | **Unstaged Changes** — the working directory vs the index |
| Left, middle | **Staged Changes (Will Commit)** — the index vs HEAD |
| Left, lower | **This Branch** — the commits this branch adds, when it is ahead of its base |
| Right, upper | The diff for the selected file, with its status in the header |
| Right, lower | The commit message, once `Ctrl-Enter` has opened it |
| Bottom bar | Status messages, file counts, current diff context |

### This Branch

When the branch is ahead of its [base](#the-merge-base), a third pane on the
left lists the commits it adds, newest first, under a row standing for the
working tree:

```
 This Branch         3 commits
  ● working tree
  51ae6f4 Document it and drop…
  5d7241d Add the beta module
  929ddbd Rename two to TWO
```

The working tree row is where the rest of the window normally lives: the two
file lists above it, staging, committing. Pick a commit instead and those two
lists give way to one holding what that commit changed, and the diff beside
them becomes that commit's own change to the file — against its parent, so it
reads as *what did this commit do* rather than what the branch looks like so
far:

```
 Files in 51ae6f4      2 files │ Modified in 51ae6f4   File: README.md
  ✎ README.md                  │     @@ -1 +1,2 @@
  ✘ alpha.go                   │   1   x
                               │   2 + y
```

Pick the working tree again and the lists come back, still on the file they
were on.

`Alt-5` jumps to the pane. `→` walks down the chain — this branch, then the
commit's files, then the diff — and it is the way in, since going *up* past the
top row means dropping the commit and returning to the working tree.

#### Popping commits back off

`U` on a commit resets the branch to that commit's **parent**, taking it and
everything committed after it off the branch. It is a soft reset: their changes
land in **Staged Changes** and the working tree is not touched.

So the common case — *"I made two commits and want both back"* — is: select the
**older** of the two and press `U`. It asks first, naming how many come off and
where the branch lands, and nothing is lost either way since the commits stay
in the reflog.

```
 This Branch            2 commits      Pop 2 commits off main, back to 7875f80?
  ● working tree
  547b64d Add the beta module          Everything they contained stays staged,
▸ 9d5d79a Rename two to TWO            and the working tree is not touched.
```

Anything you had staged before stays staged, and anything unstaged stays
unstaged; the popped work simply joins it in the index.

**Otherwise history is read only.** `s`, `u`, `a`, `v`, `e`, `D`, `L`, `r` and
`Space` all have nothing to act on while an earlier commit is on screen, so
they say so rather than acting on the working tree behind your back.

`U` works from any pane, since it acts on whatever the branch history has
selected rather than on the pane you happen to be in. From a file list, where
the history cursor is necessarily on the working tree row, it says there is no
commit to pop.

Costs one `git log` per rescan, and only when the branch is ahead. Selecting a
commit costs two commands — its file list and the first diff — and none at all
to go back to one already looked at, or to the working tree.

### The merge base

Next to the branch name the top bar shows where the branch stands against the
ref it is built on:

```
base: origin/main 80e97d7 ↑2 ↓1
```

| Part | Meaning |
| --- | --- |
| `origin/main` | The ref the branch is measured against |
| `80e97d7` | The merge base — the commit the two last had in common |
| `↑2` | Commits on HEAD since the base |
| `↓1` | Commits on the ref since the base, in amber when there are any |

The merge base is where this branch left that one, so `↑` is how much of the
history is yours and `↓` is how much has landed on the base since. Behind is
called out because it is the half you can act on.

#### Which ref

Whatever the branch tracks, and otherwise the remote's default branch:

1. **`@{upstream}`** — `branch.<name>.remote` and `branch.<name>.merge`, what
   `git branch -u` sets.
2. **`<remote>/HEAD`** — the remote's own default branch, `origin` first. Set
   by `git clone` or `git remote set-head`.
3. **`<remote>/main`, then `<remote>/master`** — for a remote that never
   recorded a `HEAD`, such as a single-branch clone or one added by hand.
4. Nothing, in which case the bar simply omits the base.

Tracking comes first because it is what the branch was *told* to build on
rather than a guess at where it probably started, and because it is the ref the
rest of git already reports — `git status`, `git branch -vv`, and the target of
a bare `git push` or `git pull` all name the same one. A tool that disagreed
with those about where the branch stands would be worse than one that said
nothing, since you would have to remember which to believe.

The fallback matters for a branch you have not pushed yet: it tracks nothing,
so the base becomes the fork point from trunk, which is what you would want to
see there anyway.

Note that against a tracked branch the merge base is usually just the last
commit you and the remote agree on, so `↑↓` carries most of the meaning — it is
"what is unpushed, what is unpulled". Against trunk, the merge base is your
fork point and the commit itself is worth reading.

#### Cost

Three git commands per rescan on a tracked branch — `rev-parse @{upstream}`,
`merge-base`, `rev-list` — taking a rescan from 8 commands to 11, and to 12 on
an untracked branch, where the upstream lookup fails before the fallback runs.
Nothing is added to moving around the file lists, which is the path that has to
stay quick.

The base is also the first thing dropped when the terminal is too narrow to
hold the whole bar, since the branch name and the way out matter more.

git gui keeps the message buffer and a column of buttons on screen at all
times. The buffer is here too, but only when there is a commit to write — see
[Committing](#committing). The buttons are gone: Rescan, Stage Changed, Sign
Off, Commit and Push were each a label for a key that already exists, so the
field takes the whole width instead.

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

Every command is **one letter**, pressed in a file list or the diff — and no
two commands share one. A key may act on whatever is under the cursor: `Space`
stages a file in a list and a hunk in the diff, `V` selects in whichever pane
you are in, `e` edits whichever file is on screen. That is one verb reaching
different objects. What you will not find is a letter that means two unrelated
things depending on where you are; `TestNoTwoCommandsShareAKey` keeps it that
way.

Lowercase acts on the working tree and the index, uppercase on the repository.

| | | | |
| --- | --- | --- | --- |
| `s` | stage | `u` | unstage |
| `a` | stage all changed | `v` | revert changes |
| `o` | sign off | `A` | amend last commit |
| `e` | edit the file in `$EDITOR` | `m` | read a markdown file, rendered |
| `R` | rescan (also `F5`) | `U` | pop commits off this branch |
| `-` / `=` | less / more diff context | | |
| `P` | push | `M` | local merge / abort merge |
| `N` | new branch | `K` | checkout branch |

`Ctrl-Enter` writes a commit. `Ctrl-C` or `q` quits, `F1` or `?` shows the keys.

**Ctrl is never a command.** The message buffer keeps every readline key:
`Ctrl-A`, `Ctrl-E`, `Ctrl-K`, `Ctrl-U`, `Ctrl-W`, `Ctrl-D`, `Ctrl-B`, `Ctrl-F`,
`Ctrl-N`, `Ctrl-P`, `Ctrl-T`.

### Committing

`Ctrl-Enter` escalates one step per press, from any pane:

| Press | What happens |
| --- | --- |
| 1st | The message comes up under the diff, with the cursor in it |
| 2nd | *"Commit N staged files to `<branch>`?"* |
| `y` | The commit is made |

**Nothing puts the message on screen unless you ask** — not staging, not an
amend, not a merge. Until then the diff has the whole right column. `Ctrl-Enter`
and `Alt-4` are both ways of asking; everything else leaves it alone.

If the cursor has wandered off the message, `Ctrl-Enter` only brings it back.
So the press that asks is always made from the message itself, with what you
wrote in front of you, and there is no way to commit without having just looked
at it.

- **`Esc`** puts it away and **keeps what you typed**, so going back to read the
  diff costs nothing. It survives quitting difftui too, see
  [Saved state](#saved-state). **`Tab`** only moves the cursor off it, leaving
  it up. **`Alt-4`** goes to the message, bringing it up first if it is not
  already there — asking for the pane is asking for the pane.
- **`n`** or `Esc` at the question backs out with the message intact and still
  on screen.
- The buffer is emptied, and put away, only once a commit has actually been
  made, so a failure never costs you what you wrote.

It is a full text field, so every readline key edits it and **every letter is
text**: the command letters are out of reach until you leave.

Sign off with `o` and switch to amend with `A`; both change what the buffer
holds whether or not it is on screen. Its title says which kind of commit this
will be — `Commit Message`, `Amend Last Commit`, `Merge Commit Message`,
`Initial Commit Message` — and when amending the top bar reads `[amending]` as
well.

The three things git gui refuses — an empty message, nothing staged, unmerged
files — are reported **in the status bar**, never as a dialog. A dialog would
cover the message buffer, which is the one thing that needs doing about it.

With nothing written, the second press says `Enter a commit message` and leaves
the cursor where it already is, in the buffer, so the answer to the complaint is
simply to type; it clears itself on the first keystroke.

They are checked in the order you would have to fix them — something to commit,
then something to commit it as — so staging nothing and writing nothing reports
`Nothing staged to commit` rather than the message you have not written yet.

### Panes

| Key | Action |
| --- | --- |
| `Tab`, `Shift-Tab` | Cycle focus |
| `Alt-1` … `Alt-5` | Jump straight to unstaged / staged / diff / message / this branch |
| `←` `↓` `↑` `→`, `h` `j` `k` `l` | Move, crossing into the neighbouring pane at the edges |

On startup the first file in **Unstaged Changes** is focused with its diff
already loaded, so the command letters are live immediately.

The panes form a grid, and navigation walks it:

```
Unstaged    │ Diff
Staged      │
────────────┼─────────
This Branch │ Message
```

Moving down past the last file in **Unstaged Changes** carries on into
**Staged Changes** at its first row; moving up from the top of the staged list
goes back to the bottom of the unstaged one.

`→` from **either** file list goes to the diff. The diff remembers which list it
is showing — git gui tracks the same thing as `current_diff_side` — so `←` comes
back to *that* list with the same file still selected.

**Under 100 columns the left column folds away** when the focus moves to the
diff or the message: a pane taking a third of a narrow window leaves the diff
too tight to read code in, which is the one thing the window is for. `→` gives
the diff the whole width, `←` brings the lists back, and the status bar says so
while they are away. At 100 columns or wider nothing moves.

```
80 columns, on a list                    80 columns, after →
─────────────────────────────────────    ─────────────────────────────────────
 Unstaged Changes   1 file │ Modified…    Modified, not staged   File: server.go
  ✎ server.go              │   3   func     3   func handler(w http.ResponseWri…
                           │   4  -    r     4  -    return nil
                           │   4 +     r     4 +     return writeJSON(w, http.S…
```

`↓` past the end of the diff reaches the message buffer, when there is one. Out
of it, only `↑` from the first line and `←` from the first column walk away —
everywhere else in the text the arrows move the cursor, as they must. `Esc`
leaves from anywhere. `Tab` steps over the buffer entirely when it is not on
screen.

### On a file

| Key | Action |
| --- | --- |
| `Space` / `Enter` | Stage, or unstage from the staged list |
| `e` | Open it in `$EDITOR` (see below) |
| `m` | Read a markdown file, rendered, full screen (see below) |
| `V` | Visual select (see below) |
| `D` | Delete untracked files |

Staging takes the file out of the list, and **the cursor holds its row** rather
than following the file into the other list. That row now holds the next file
down, so `Space` `Space` `Space` stages the next three — press it from the
second file and the first is left alone. On the last file there is no next one,
so the cursor comes to rest on the one above.

A rescan that changes nothing leaves the cursor on the file it was on, so `R`
and the automatic rescans after each command never move it about.

#### Editing a file

`e` opens the file in view in **`$EDITOR`**, or in `vi` when that is unset.
`$EDITOR` may carry arguments of its own — `code -w`, `emacsclient -nw` — and
they are passed through. `VISUAL` and git's `core.editor` are not consulted.

The whole terminal goes to the editor until it exits, and difftui rescans when
it comes back, so the lists and the diff pick up whatever you did. It means the
same thing from either file list and from the diff, since all three are showing
the same file.

Only the **working tree** can be edited. A file selected from an earlier commit
still opens — the editor is the most familiar place to read one — but what it
gets is that commit's version, copied to a **read-only** temporary file, so
history can be read there and never rewritten. The copy is swept up when the
editor exits. A working-tree path that is not on disk — a file staged for
deletion, say — is refused, since opening it would create it rather than
edit it.

#### Reading markdown

`m` opens the file in view in a full screen markdown reader, rendered with
[glamour](https://github.com/charmbracelet/glamour). Like `e`, it acts on
whichever version the window is showing: the working tree's copy, or the
selected commit's when one is picked in This Branch — reading is the one thing
history is happy to allow. Arrows and `PgUp`/`PgDn` scroll, `g`/`G` jump to
either end, and `Esc`, `q` or `m` again puts it away. A file that is not
markdown (`.md`, `.markdown`, `.mdown`, `.mkd`) is refused with a note rather
than rendered as gibberish.

When the file has changes — the same ones the diff pane would show — the
touched text carries a **`▍` marker** in the left margin, and `n`/`p` hop to
the next and previous marked spot, the same keys that walk hunks in the diff.
It is not a diff: just a "look here" in the margin of the rendered page.
Rendered lines do not map one-to-one onto source lines, so the marker lands on
whole blocks — a changed line marks its paragraph, which is the honest
granularity and the one a reader wants anyway. (To keep that map, the source
is rendered a block at a time; constructs that reach across a blank line, such
as reference-style link definitions, render less faithfully than a
whole-document pass would.) A deleted block has no lines of its own to mark,
so the seam it left behind is marked instead.

### In This Branch

| Key | Action |
| --- | --- |
| `→` | The commit's files, then the diff |
| `U` | Pop this commit and everything after it off the branch |

### In the diff

| Key | Action |
| --- | --- |
| `Space` | Stage the hunk under the cursor |
| `V` | Visual line select (see below) |
| `L` | Stage just the line under the cursor |
| `r` | Revert the hunk |
| `n` / `p` | Next / previous hunk |

On the staged side each of these unstages instead.

#### Visual select

`V` behaves like vim's visual line mode, and works the same way in the file
lists and in the diff. It starts with the current row selected; `↑`/`↓` (or
`k`/`j`) extend the selection; `V` again or `Esc` cancels. `Space` or `Enter`
then acts on the whole selection — staging the selected files in the unstaged
list, unstaging them in the staged list, and staging the selected lines in the
diff.

While a selection is open the cursor stays in its pane: running off the end
would abandon the gesture halfway. Leaving the pane another way — `Tab`, `Alt-1`
to `Alt-3`, `←`/`→` — cancels it, as does a rescan, since the rows it covered
now hold different files.

The two panes draw it differently, for one reason. In the lists nothing else is
using the row background, so the selection is a band across the row, in a dimmer
shade of the cursor's own. In the diff the background is already carrying the
diff, so the cursor and the selection move out to a one column bar at the left
edge. Same gesture, different ink.

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

`A` switches to amending. The top bar then reads `[amending]`, and the buffer
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

git gui lets you Ctrl-click several files and act on all of them. Here, `V`
selects a run of them, the same gesture the diff uses to select a run of lines:
staging, unstaging, reverting and deleting then apply to every file in the
selection. With nothing selected they apply to the file under the cursor.

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
