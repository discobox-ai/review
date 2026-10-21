# 26-10-21-407 — Keep the review card in the review file, and write version 3 only when there is one

- **Status**: Accepted
- **Date**: 2026-10-21

## Context

A review card is the meat of a change, as an agent that has read it whole
picks it out: a few bullets of what changed, the places in the diff that are
its core (`path#L12-L20`), and the commit to review it from. The window shows
the bullets above the stack and reviews the core's hunks before the rest;
`discobox-review card set` takes a card from a file and checks it against a
JSON Schema and against the change.

A card is about one branch's change, like everything else in a review. It has
to move aside when another branch is checked out, be written under the same
lock as comments, be noticed by an open window within the second, and live in
a pull request's own directory (`pkg/store`) as well as in a git directory.

The review file has carried `"version": 2` on every write. A discobox-review
reads a file up to its own version and refuses a newer one, because reading
one and writing it back would drop whatever it did not understand.

## Decision

The card is a `card` field of the review document (`review.State.Card`). It is
set with `discobox-review card set` or by hand, and every program that writes
the review keeps it.

`review.Version` becomes 3. A review is written as version 3 when it has a
card and as version 2 when it has none.

## Alternatives rejected

- **A file of its own beside the review** (`discobox-review.card.json`), which
  an agent could drop in without a command. It would need its own branch
  swap, its own lock discipline, its own polling and its own place in
  `pkg/store`: everything the review file already has, done a second time and
  able to come to disagree with it. Writing the card through `card set` also
  gets it checked before anybody reads it, which dropping a file does not.
- **Version 3 on every write.** The day this ships, every review an older
  binary opens would be refused, card or not, though nothing in a review
  without a card is anything it does not understand.
- **No version bump.** An older binary would read a review with a card,
  ignore the field, and write the review back without it on its next
  approval: the card would be lost without anything saying so.

## Consequences

- An older discobox-review refuses a review with a card and asks to be
  upgraded. It reads every review without one as before.
- What version a review is written as depends on what is in it, so a test or
  a reader that expects a fixed version has to say which kind of review it
  has.
- The card goes wherever the review goes: archived with the branch, kept in a
  pull request's directory, and picked up by an open window.
- The card's shape is part of the review file's format. A card a reader
  cannot parse makes the whole review unreadable, so changing its shape is a
  version change like any other, with old cards read the way version 1
  reviews still are.
