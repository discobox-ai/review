# discobox-review review notes

- Never migrate a legacy checksum into a broader canonical approval without
  independent evidence for paths, kinds, modes, binary bytes and line
  terminators: versions 1–3 did not hash all those dimensions. Keep unproved
  claims separately, retire them permanently when their pair changes, and
  retain the original document before replacing it (issue #18).

- In `pkg/ui`, take what an approval moves on from (`m.view.paths()`) before
  `mutate`, not after. With `f` on, the file just approved has left
  `m.view.files` by the time `mutate` returns, and an `advance` that looks it
  up there finds nothing and stays put — `TestAMovesOnWithApprovedFilesHidden`.
- In `pkg/ui`, every `rebuild` puts the cursor's file heading back where it
  was, and at the top of the pane when it had scrolled off. Any command that
  reshapes something inside a file and rebuilds — approving a hunk, opening
  one — has to hold the row it is about in place itself (`reviewView.hold`),
  or the pane jumps back to the heading. Missed twice in one change:
  `TestApprovingAHunkLeavesThePaneWhereItIs`,
  `TestOpeningTheNextHunkLeavesThePaneWhereItIs`.
- In `pkg/ui`, the first scan goes out before the review file is read, so a
  base the file names arrives after it. `onRescan` scans again when the scan
  it took was measured from somewhere other than `m.baseRef`; anything that
  reads the review for the first time elsewhere has to do the same, or a
  window opens measured from the source's own guess —
  `TestTheWindowMeasuresFromTheSavedBase`.
- In `pkg/ui`, a file in the review card's core is two entries in the stack.
  Anything that finds an entry again after the rows are laid out — the
  cursor (`cursorAnchor`, and `mark` across a resize), a fold, a hunk's or a
  gap's open state, the comment box — goes by `reviewFile.id()`, not the
  path; only what acts on the review uses the path, and what resets a fold
  for a verdict resets both entries'. Going by the path lands on the rest's
  entry. Missed in three places in one change:
  `TestTheTwoEntriesOfAFileFoldApart`, `TestAResizeKeepsTheCursorInItsEntry`,
  `TestApprovingAFileFoldsBothEntries`.
- In `pkg/ui`, a new kind of row is checked against every key that reaches
  the cursor's row — `a`, `c`, `r`, `x`, `X`, `Space` — and carries what it
  needs in a field of its own, not one another kind means something by. A
  stretch first approved the hunks it hides and passed for a conversation:
  `TestAStretchIsNotApprovedUnseen`.
- In `pkg/ui`, a base is written into the review only once a scan measured
  from it has found it (`baseFound`): the base `b` chooses is saved when its
  scan lands, not before, and one given on the command line with its first
  write after. Judging it by whatever the last scan said judges the base
  before it — `TestABaseNotFoundIsNotSaved`.
- In `pkg/ui`, a window over a source that is not the review's own tree
  (`Options.Apart`, `-head`) judges approvals on screen and never saves the
  verdict, nor its base: the tree is what the commands measure approvals
  against, and a verdict saved from another tree strikes down what holds
  there — `TestAWindowApartDoesNotStrikeTheTreesApprovals`. A write that
  reads approvals back inside `mutate` has to judge them as the screen does
  first: an approval of another digest counts for nothing here, and a run
  the commit does not show is kept, or one approved hunk approves the file —
  `TestAHunkApprovedApartLeavesTheRestOfTheFile`.

- Keep a review card's summary visible even when its core is empty: tests
  and documentation changes have no production-code core to name. Decide
  whether to draw the card by its presence, not by its core links.
- Derive approval identities and display hunks from the same complete content
  pair, never the host's patch; repeated lines realign with Git context. Keep
  unavailable identities distinct from empty changes, and require a complete
  manifest before retiring omitted paths. Legacy checksums cannot prove
  metadata or terminators they never encoded; preserve history and require
  review rather than granting broader approval.
- Verify an approval against fresh contents while holding the review lock,
  and apply its intention to the latest document. UI approval source reads and lock
  waits run off the render thread. A deferred approval must be rejected if
  the displayed identity moved, and pending input must retain its order.
- Associate a review stamp with the bytes actually read. A stamp obtained
  after unlocking may belong to a later writer; retaining it beside an older
  state would make the next poll miss that writer's changes.
- Parse quoted `diff --git` paths even without text hunks: canonical binary
  and mode-only patches have no `+++` line to recover their names from.

- Serialize file-kind replacements as delete/create records in raw patches.
  A regular-file/symlink transition is not a chmod, even when both store the
  same bytes; keep one canonical approval scope in the display.

- An automatic reconciliation discovered during a UI write must run after
  that write completes. A delayed result must not replace a newer scan's
  saved nullification with its older approval snapshot; otherwise undo can
  revive approval, especially when polling is disabled by an embedding host.
