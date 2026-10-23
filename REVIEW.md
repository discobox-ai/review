# discobox-review review notes

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
