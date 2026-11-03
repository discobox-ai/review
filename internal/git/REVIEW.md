# internal/git review notes

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
