# internal/git review notes

- Compare complete content pairs and their manifest when checking a moving
  worktree; HEAD and timestamps alone cannot detect an edit. Include Git's
  clean representation, absent versus empty sides, modes and link targets;
  missing data must not become an empty comparison. Override submodule-ignore
  configuration when collecting the identity manifest, or an existing change
  looks absent and loses its approvals (issue #18).
- Batch content hashes and object reads instead of spawning Git per file.
  Count commands across differently sized changes, preserve C-quoted filename
  bytes on batch input, and reject incomplete or oversized blob streams.

- A write by rename must never be refused by a lock-free reader. State files
  are read without the lock and replaced whole by a rename; on Windows a file
  opened without `FILE_SHARE_DELETE`, or a rename without POSIX semantics,
  refuses that, and the writer's change is lost. A new way to open or rename
  a state file goes through `System.ReadFile` / `System.Rename`, or does as
  their Windows halves do — `TestAReaderDoesNotRefuseAWrite`,
  `TestAWriteReplacesAFileHeldOpenByAReader`.
- A call that goes to Windows directly instead of through `os` takes its path
  through `longPath`. `os` lifts the old path limit for its own calls only,
  and `ReadStateFile` reads any failure as no file, so a long path that will
  not read is a review written over fresh — `TestAStateFileOnALongPathReads`.

- Use Git's octal byte quoting for paths sent to Git or rendered in patches.
  Go's `strconv.Quote` can emit `\x`/`\u` escapes Git refuses, especially
  control-byte or non-UTF-8 filenames.
