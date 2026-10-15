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

Gotcha: a window opened without `-base` after `discobox-review base main`
still says "from HEAD" (as of 2026-10-15).
