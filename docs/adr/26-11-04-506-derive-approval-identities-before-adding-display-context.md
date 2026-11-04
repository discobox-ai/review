# 26-11-04-506 — Derive approval identities before adding display context

- **Status**: Proposed
- **Date**: 2026-11-04

## Context

Issue #18 reproduces an approval falling when an unchanged file is read with
another diff context. `internal/cli/cli.go` scans status at context zero and
diff at the requested context. `pkg/ui/model.go` also rebuilds approvals from
the display patch. `internal/review/review.go` hashes changed lines and their
positions for files (`Digest`) and uninterrupted runs (`HunkDigests`). It
excludes context lines, but assumes Git chose the same changed lines.

Git can align repeated fences or blank lines differently at different
contexts. The `AGENTS.md` pair from discobox-ai/discobox#619, merge-base
`e69d18874d38d7ae7614511661acf0e68cc28b23` to head
`df093ee7bb0af34823e1358df4f86a0b0c680fb2`, reproduces this on Git 2.47.3.
Context zero and contexts 1, 3, 5 and 100 give different file and run
identities. Disabling the indentation heuristic or asking for a minimal diff
does not cure this fixture. Changing to histogram or patience does, but is
not a guarantee for every pair of files or Git version.

Approvals are durable and belong to individual reviewers. Unaffected runs
can remain signed off after another run changes. `PruneHunks` prevents a run
that disappeared from becoming approved again when an edit is undone. A
file verdict can be nullified without deleting its digest, author or date.
These semantics must survive the fix.

`pkg/source.Source` supplies display diffs, file contents and a base. Context
is explicitly a hint: an external source can return a host's fixed-context
patch, including a truncated patch (`Scan.Incomplete`). `internal/git.Repo`
and `Snapshot` are local implementations. `pkg/store` stores reviews for
external sources without a Git directory. An identity must not depend on the
host's patch alignment, local Git configuration, or how much context a
reader requested.

Versions 1–3 store digests, not the approved patch, its context, or a content
snapshot. A legacy digest that does not match today's patch cannot tell us
whether the content changed or the alignment changed. Automatic recovery of
every old approval is therefore impossible from that document alone.
`review.Parse` refuses newer schema versions, while `State.Bytes` currently
writes version 2 without a card and version 3 with one; see ADR 26-10-21-407.

## Decision

Recommend one deterministic comparison of a file's two sides, shared by the
CLI and window, with display context applied afterwards. Version both the
identity scheme and its persisted representation. Migrate legacy approvals
only with evidence; retain unprovable claims visibly for the reviewer.

### Canonical comparison and identities

The source must supply a complete, coherent pair of contents and file
metadata for each changed path, measured from the resolved review base to
its new side. Distinguish absent content from empty content and unavailable
content. Include both paths for a rename, file kinds, executable modes and
binary content. A worktree's new side uses the repository's clean/filter
representation, as Git's comparison with a committed blob does; an external
source supplies repository content rather than a rendered text conversion.
Do not normalize whitespace or line endings again in the identity layer.

This is a required source contract, added to the core interface and all its
implementations together, not an optional capability with a display-diff
fallback. The contract must bind the contents and changed-path manifest to
one scan: an external source uses resolved revision IDs; a local scan must
reject or retry a pair observed to move while being collected. Missing,
truncated or inconsistent data cannot be interpreted as an empty change.

Compare the two sides with a deterministic line-based Myers shortest edit
script, preferring deletion on equal-cost choices, without indentation or
presentation heuristics. Line tokens include their terminators, including
an absent final newline. Freeze the precise output with golden fixtures as
part of the identity scheme: a library replacement or optimization must
produce the same script or use a new scheme. A reusable diff primitive
belongs in `github.com/discobox-ai/x`; review-specific identity and verdict
semantics stay in `internal/review`.

For text, a canonical run is a maximal uninterrupted sequence of insertions
and deletions in that script. Its identity hashes its old and new positions,
exact removed and added bytes, and relevant path/kind/mode metadata. Do not
include the whole file's content hash in each run: an unrelated edit must
not invalidate a partial approval whose bytes, coordinates and metadata
remain unchanged. The file identity hashes its
metadata and ordered canonical runs. Binary files and changes with no text
runs use the complete before/after bytes and metadata for file approval and
have no partial text approvals. Mode-only, rename-only, empty-file and
final-newline changes must have identities distinct from no change.

Use full SHA-256 digests of an unambiguous, length-delimited encoding, with
separate file/run domains and an explicit scheme identifier. Display
context, hunk headers, commit hashes, branch names and timestamps are not
inputs. Changing only a base commit's identity without changing the relevant
contents and metadata must not invalidate an approval.

### Display and reconciliation

Render text hunks from the canonical script, merging its runs according to
the requested context and filling unchanged lines from the same content
pair. The window's extra gaps and CLI raw diff use that comparison too.
Host patches may help discover a change, but never determine approval IDs
or which edits a displayed hunk signs off. A displayed hunk is approved
only when all canonical runs it contains are approved. No run outside it
is approved by that action. This avoids inferring equivalence between two
differently aligned edit scripts.

File approvals, hunk approval/promotion, pruning and invalidation all use the
canonical identities. Preserve reviewer ownership and existing no-revival
semantics. Compare fresh coherent data before writing a reconciliation,
under the existing review lock. An incomplete scan or an unavailable pair
reports that approval verification is unavailable; it neither stamps,
nullifies, prunes nor grants approvals for the affected data. An incomplete
changed-path manifest also cannot justify treating an omitted path as gone.
Keep the window's `Apart`/snapshot rule: reconciliation there is in memory,
and never migrates or invalidates the working tree's saved approvals.

### Durable upgrade

Add schema version 4 for the identity scheme, migrated verdicts and retained
legacy claims. Preserve parsing of versions 1–3. Parsing alone does not
rewrite the review. Fresh reconciliation, including one triggered by status
or opening the window, is a write when it records migration evidence or
changes a verdict; it must persist those changes to prevent revival.
On a write requiring the new representation, retain a
copy of the original document in the store before replacing it, under the
same lock; if retaining it fails, fail the upgrade without changing the
original. The store contract must support this in both a Git directory and
`pkg/store`. Keep branch archives lazy: upgrade the branch being used, not
every saved review. Retrying an interrupted upgrade must be idempotent.

A version 4 document records the scheme explicitly and retains it even if
its last approval is withdrawn. Older binaries must refuse it, not read
new digests as legacy ones or write away pending claims. Documents with no
new representation can still be written as version 2 or 3, following ADR
26-10-21-407. Never downgrade a document that has been upgraded.

For each legacy verdict, preserve the author, timestamp, old digest, hunk
IDs and nullification state. Carry an active file approval only if its old
digest can be reproduced by the legacy hash over a complete diff of the
current pair. The verifier may try the former CLI context zero and window
context five, and a complete legacy patch supplied by the source; these are
migration candidates, not the new identity algorithm. Digest equality is
necessary but insufficient: it proves nothing about fields the legacy hash
omitted. Carry the approval into a canonical file ID only with independent
evidence, recorded when that approval was given, that its paths, kinds,
modes, binary bytes and final-newline state match the current pair too.
Today's contents, an approval timestamp, or a conveniently matching commit
are not evidence of an earlier approved worktree.

For a partial approval, require a matching legacy run digest and proof that
its removed/added bytes and both coordinate ranges exactly equal a complete
canonical run, including independent approved-state evidence for metadata
and line terminators omitted by the legacy hash. Never expand a legacy
approval to a larger canonical run or
infer it from overlapping spans, identical text alone, or a file name.
Carry only the runs proved this way. Other valid contexts need not be
exhaustively enumerated: this verifier deliberately makes no promise to
recover every legacy alignment or configuration. Versions 1–3 do not
normally retain the required independent evidence: for those documents,
a matching legacy checksum alone still leaves the claim needing review.
Do not invent a snapshot or infer omitted dimensions from their current
values to make automatic migration succeed.

If a claim cannot be proved, retain it as **legacy approval needs review**,
not as an active approval and not silently as a content nullification. Name
this condition in CLI status and on the file/hunk in the window. Record a
fingerprint of the complete pair at this first verification attempt; retry
verification only while that fingerprint is unchanged. If the pair moves,
retire the unproved claim to history permanently. Retrying after an undo
must not resurrect it. When no complete pair is available, leave the legacy
record untouched and report verification unavailable instead.

A previously nullified or withdrawn claim never becomes active through
migration, even if an old digest matches again. An explicit reapproval
creates a canonical claim; withdrawing or replacing a pending legacy claim
retires it rather than scheduling another automatic migration. Keep legacy
history separate from active identities so ordinary pruning cannot erase
it and it cannot count toward approval or hunk promotion.

An active legacy file approval with no digest retains its established
meaning: approval of the current complete change, bound on its first
successful verification. Never treat an unknown nonempty digest as this
case. The window's existing version 1 ownership rule for nameless hunk
claims remains, but claiming ownership does not waive identity verification.

## Alternatives rejected

- **Hash display patches while ignoring context lines.** This is today's
  design. Context affects which lines Git considers changed, not just where
  hunk boundaries fall. Sorting changed lines or dropping positions also
  loses ordering or confuses repeated edits.
- **Choose another Git algorithm or disable its heuristics.** This can fix
  one fixture but leaves identity dependent on Git's implementation and the
  external host's patch. It also changes legacy IDs without an upgrade.
- **Take a second fixed-context Git diff for identity only.** This removes
  display context from that invocation, but leaves two edit scripts to map
  to each other. A hunk approval must not sign off unseen edits when the two
  align repeated lines differently. It also cannot serve an external source
  that has no Git process or arbitrary-context diff API.
- **Hash complete base/new blobs for every approval.** Suitable as upgrade
  fingerprints and for binary file approval, but using the pair for every
  text run makes an unrelated edit invalidate all partial approvals. Using
  blobs for text files alone would introduce a second equivalence rule for
  file approval and run promotion.
- **Keep all legacy claims active, or rewrite unmatched digests to today's
  identity.** The saved state cannot distinguish a real edit from a display
  realignment. This would approve changes nobody read or revive a claim
  after an edit was undone.
- **Discard all old approvals, or keep the old schema version.** Discarding
  loses verifiable work and hides the reason for re-review. Keeping version
  2/3 lets an older writer misinterpret new IDs or erase upgrade evidence.

## Consequences

- Context changes become presentation changes. Canonical identities and
  the runs a displayed hunk approves cannot diverge between CLI and window,
  or between local and external sources given the same content pair.
- Sources must provide complete contents, not merely a hosting service's
  patch. Until a source can do so, affected files remain readable but their
  approvals cannot be verified or changed. Fetch and comparison limits must
  report this state rather than fall back to a truncated comparison.
- This intentionally adds a visible re-review state for legacy claims whose
  meaning cannot be proved. Exact recovery is bounded by the evidence the
  older format retained; the original document and per-claim history remain
  available. Once a review is upgraded, older writers require upgrading too.
- Implementation must cover the reported repeated-fence pair across contexts
  0, 1, 3, 5 and a context large enough to merge the file, through both CLI
  and window. It must also prove actual-edit invalidation, survival of an
  unaffected run, no revival after undo, metadata/binary identity, identical
  local/external results, unavailable data, apart views, and idempotent
  upgrades from versions 1–3 with mixed reviewers and partial approvals.
  Upgrade coverage must include an unchanged legacy digest after a mode,
  rename, binary or final-newline edit: these claims stay pending without
  independent approved-state evidence. The implementation adds a review
  note requiring evidence for dimensions omitted by legacy hashes; checksum
  equality must never grant a broader approval.

## Deferred

Exact Go data shapes, the reusable Myers implementation, backup naming and
retention policy, comparison resource limits, and CLI/window wording remain
implementation choices. Set them before shipping the implementation; they
must satisfy the contracts above and be exercised at their public surfaces.
Optimizations that cannot preserve the frozen script require a new identity
scheme and an explicit migration decision. Revisit automatic recovery of
unproved legacy claims only if new evidence can prove them without extending
an approval or reviving an ineligible claim.
