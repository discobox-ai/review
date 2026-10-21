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
