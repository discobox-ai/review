package ui

import (
	"errors"
	"fmt"

	tea "charm.land/bubbletea/v2"
	"github.com/discobox-ai/review/internal/git"
	"github.com/discobox-ai/review/internal/review"
)

// A mutation owns the user's intention, not a copy of the displayed document.
// Its callback runs against the latest document under the store lock.
type reviewMutation struct {
	apply           func(*review.State) error
	expected        map[string]review.Identity
	requireComplete bool
	automatic       bool
	after, failed   func(*Model) tea.Cmd
}

type reviewWriteMsg struct {
	gen     int
	id      uint64
	branch  string
	state   *review.State
	err     error
	op      reviewMutation
	refresh bool
}

type deferredReviewInput struct {
	msg     tea.Msg
	branch  string
	gen     int
	focus   focusArea
	compose *compose
}
type replayReviewInputMsg struct{}

func (m *Model) queueReviewInput(msg tea.Msg) bool {
	if m.replayingReview || !m.writing && len(m.deferredReview) == 0 {
		return false
	}
	switch k := msg.(type) {
	case tea.KeyPressMsg:
		if k.String() == "ctrl+c" || k.String() == "q" && m.focus != focusCompose && m.dialog == nil {
			return false
		}
	case tea.MouseMsg, approveAllMsg, deleteMsg, baseChosenMsg:
	default:
		return false
	}
	m.deferredReview = append(m.deferredReview, deferredReviewInput{msg, m.branch, m.gen, m.focus, m.compose})
	return true
}
func (m *Model) replayReviewInput() tea.Cmd {
	if m.writing || len(m.deferredReview) == 0 || m.reviewReplayScheduled {
		return nil
	}
	m.reviewReplayScheduled = true
	return func() tea.Msg { return replayReviewInputMsg{} }
}

func (m *Model) mutate(op reviewMutation) tea.Cmd {
	if m.store == nil {
		m.setError("This source has no review store")
		if op.failed != nil {
			return op.failed(m)
		}
		return nil
	}
	if m.writing {
		if op.automatic {
			m.pendingReconcile = true
		}
		if !op.automatic {
			m.setError("A review write is already in progress")
			if op.failed != nil {
				return op.failed(m)
			}
		}
		return nil
	}
	m.writing = true
	m.writeID++
	id, src, store, branch, gen := m.writeID, m.src, m.store, m.branch, m.gen
	baseRef, context, apart, pinned, found, author := m.baseRef, m.context, m.apart, m.basePinned, m.baseFound(), m.author
	shown := m.reading
	if !op.automatic {
		m.setOK("Saving review…")
	}
	return func() tea.Msg {
		var saved *review.State
		refresh := false
		wait := lockWait
		if op.automatic {
			wait = 0
		}
		err := review.Update(store, branch, wait, func(st *review.State) error {
			need := op.automatic || len(op.expected) > 0 || pinned && found && !apart && st.Base != baseRef
			if need {
				base := st.Base
				if pinned || apart {
					base = baseRef
				}
				scan, err := src.Scan(base, context)
				if err != nil {
					refresh = true
					return err
				}
				if scan.Branch != branch {
					refresh = true
					return errors.New("the branch changed; read the change again")
				}
				if scan.Base.Err != "" && !apart {
					refresh = true
					return errors.New(scan.Base.Err)
				}
				reading := review.CompareScan(src, scan, context)
				refresh = scan.Revision != shown.Scan.Revision || scan.Base.SHA != shown.Scan.Base.SHA || reading.Complete != shown.Complete || len(reading.Identities) != len(shown.Identities)
				for path, identity := range reading.Identities {
					if identity.Fingerprint != shown.Identities[path].Fingerprint {
						refresh = true
					}
				}
				if op.requireComplete && !reading.Complete {
					refresh = true
					return errors.New("approval verification unavailable for the complete change")
				}
				for path, want := range op.expected {
					got := reading.Identities[path]
					if want.Digest == "" || got.Digest == "" {
						refresh = true
						return fmt.Errorf("%s: approval verification unavailable", path)
					}
					if got.Digest != want.Digest {
						refresh = true
						return fmt.Errorf("%s changed; read it again before approving", path)
					}
				}
				if !apart {
					st.ClaimLegacyHunks(author, reading.Identities)
					st.ReconcileCanonical(reading.Identities, reading.Complete)
				}
			}
			if op.apply != nil {
				if err := op.apply(st); err != nil {
					return err
				}
			}
			if !apart && pinned && found {
				st.Base = baseRef
			}
			saved = st
			return nil
		})
		return reviewWriteMsg{gen: gen, id: id, branch: branch, state: saved, err: err, op: op, refresh: refresh}
	}
}

func (m Model) onReviewWrite(msg reviewWriteMsg) (tea.Model, tea.Cmd) {
	if msg.id != m.writeID {
		return m, nil
	}
	m.writing = false
	// A stamp obtained after unlocking could belong to a later writer. Force
	// the next poll to read rather than associating it with this older state.
	m.stamp = git.Stamp{}
	var follow tea.Cmd
	moved := msg.gen != m.gen
	pending := m.pendingReconcile
	m.pendingReconcile = false
	if msg.branch != m.branch {
		return m, tea.Batch(m.scan(), m.replayReviewInput())
	}
	if msg.err != nil {
		if msg.op.failed != nil {
			follow = msg.op.failed(&m)
		}
		if !msg.op.automatic || !errors.Is(msg.err, git.ErrLocked) {
			m.setError("Saving review: " + firstLine(msg.err.Error()))
		}
	} else {
		if moved {
			// A newer scan may already have read a later writer's document. Never
			// replace it with this command's older snapshot or revive a fallen claim.
			base := m.readReview()
			if !m.basePinned && base != m.baseRef {
				m.baseRef = base
				msg.refresh = true
			}
		} else {
			m.rv = msg.state
		}
		if m.apart {
			m.reconcileApart()
		}
		m.rebuild()
		if msg.op.after != nil && !moved {
			follow = msg.op.after(&m)
		}
	}
	if pending || moved {
		follow = tea.Batch(follow, m.reconcile(true))
	}
	if msg.refresh {
		follow = tea.Batch(follow, m.scan())
	}
	if m.baseToSave && !m.writing {
		follow = tea.Batch(follow, m.scan())
	}
	return m, tea.Batch(follow, m.replayReviewInput())
}

func (m *Model) reconcileApart() {
	if m.rv == nil {
		return
	}
	m.rv.ClaimLegacyHunks(m.author, m.reading.Identities)
	m.rv.ReconcileCanonical(m.reading.Identities, false)
}
func (m *Model) reconcile(fresh bool) tea.Cmd {
	if m.rv == nil {
		return nil
	}
	if m.apart {
		m.reconcileApart()
		return nil
	}
	probe, err := review.Parse(m.rv.Bytes())
	if err != nil {
		return nil
	}
	claimed := probe.ClaimLegacyHunks(m.author, m.reading.Identities)
	changed := probe.ReconcileCanonical(m.reading.Identities, m.reading.Complete)
	if !claimed && !changed {
		return nil
	}
	if !fresh {
		return m.scan()
	}
	if !m.baseFound() {
		return nil
	}
	return m.mutate(reviewMutation{automatic: true})
}

func (m *Model) standing(path, by string, id review.Identity) bool {
	v := m.rv.VerdictOf(path, by)
	return id.Digest != "" && v != nil && v.Scheme == review.IdentityScheme && v.Approved && v.Digest == id.Digest
}

// Main-review gaps come from the same coherent content pair as its changes.
func addReadingGaps(reading review.Reading, files []*reviewFile) {
	by := map[string]*reviewFile{}
	for _, f := range files {
		by[f.path] = f
	}
	for _, change := range reading.Contents.Files {
		side := change.New
		f := by[side.Path]
		if f == nil || !f.verifiable || !side.Present || side.Mode != "100644" && side.Mode != "100755" || len(side.Data) > maxGapBytes || f.patch == nil || len(f.patch.Hunks) == 0 {
			continue
		}
		f.addGaps(string(side.Data))
	}
}
