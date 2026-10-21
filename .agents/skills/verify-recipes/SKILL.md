---
name: verify-recipes
description: How to build and drive discobox-review to verify a change — the CLI subcommands and the TUI window in tmux, over a throwaway repository.
---

# Verifying discobox-review

Build: `go tool task build` → `build/discobox-review`.

A throwaway repository with a branch to review:

```bash
D=$(mktemp -d) && cd "$D" && git init -q -b main && git config user.name T \
  && git config user.email t@e && git config commit.gpgsign false \
  && printf 'a\nb\nc\nd\ne\n' > f.go && git add . && git commit -qm init \
  && git checkout -qb feat && printf 'a\nB2\nB3\nc\nd\nnew\ne\n' > f.go && git commit -qam change
```

With no upstream the base resolves to `HEAD` and there is nothing to review:
pass `-base main` to the window, or `discobox-review base main` for the CLI.

- **CLI:** `comment --by rev f.go:2 "…"`, `list`, `reply --by author t1 "…"`,
  `show t1`, `approve --by rev f.go`, `status --by rev`. `--by` is required.
- **Window:** `tmux -L vrf new-session -d -x 120 -y 24 "cd $D && discobox-review -by me -base main"`,
  then `tmux -L vrf send-keys -t 0 …` and `tmux -L vrf capture-pane -p -t 0`.
  Space opens a file, `s` toggles split/unified, `a` approves, Ctrl-Q quits.
  The CLI sees what the window wrote (`status --by me`).

A window opened without `-base` measures from the base the review saved
(`discobox-review base main`).

- **Review card:** `card set --by claude card.json` (or `-` for stdin) with
  `{"base": "<7+ hex of git merge-base HEAD main>", "summary": ["…"], "core":
  ["f.go#L4"]}`; `card`, `card --json`, `card schema`, `card clear`. A file
  of 60 numbered lines with three edits far apart gives core hunks with a
  `▸ N changed, M unchanged lines` stretch between them. Keep `card.json`
  outside the repo, or it shows up in the review as untracked.
- The installed `/usr/local/bin/discobox-review` may predate a change to the
  review file's version and refuse it ("wants upgrading"): drive
  `build/discobox-review`.
