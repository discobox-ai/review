# internal/git design

- `System` is the boundary: every git invocation and every file access goes
  through it, and `Local` is the real one. `system.go` holds `Local`; the
  calls each kind of system spells its own way — reading a file, renaming one
  over another, taking a lock — are in the `system_*.go` files beside it.
  `TestSystemIsTheOnlyBoundary` fails on a call that reaches outside the
  process anywhere else.
- State files (`StateDir`) are written under a lock and read without one. A
  write goes to a temporary file and is renamed over the old one, so a reader
  has all of the old file or all of the new. That holds only while a reader
  never stands in the rename's way. On Windows that takes both halves in
  `system_windows.go`: the read opens sharing delete, and the rename uses
  POSIX semantics, which replaces a file that is open (`MoveFileEx` refuses
  to, however it is shared).
