# internal/git design

- `System` is the boundary: every git invocation and every file access goes
  through it, and `Local` is the real one. `system.go` holds `Local`; the
  calls each kind of system spells its own way — reading a file, renaming one
  over another, taking a lock — are in the `system_*.go` files beside it.
  `TestSystemIsTheOnlyBoundary` fails on a call that reaches outside the
  process anywhere else.
- `ReadContents` collects complete before/after bytes and modes, comparing
  two full worktree reads and rejecting a moved HEAD or branch. Snapshots
  read immutable objects. Worktree bytes go through Git's clean filters;
  `hash-object -w` retains immutable blobs without changing the index or
  worktree. Symlinks retain their target bytes, never the target's contents.
  Regular worktree paths are hashed together through `hash-object
  --stdin-paths`; object sizes and bodies use `cat-file` batches. Limit a blob
  to 16 MiB and held bodies to 64 MiB per reading; unavailable pairs remain
  named in the complete manifest. Pin rename/submodule-ignore settings.
- State files (`StateDir`) are written under a lock and read without one. A
  write goes to a temporary file and is renamed over the old one, so a reader
  has all of the old file or all of the new. That holds only while a reader
  never stands in the rename's way. On Windows that takes both halves in
  `system_windows.go`: the read opens sharing delete, and the rename uses
  POSIX semantics, which replaces a file that is open (`MoveFileEx` refuses
  to, however it is shared).
