package ui

import (
	"errors"
	"os"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/discobox-ai/review/internal/git"
	"github.com/discobox-ai/review/internal/review"
)

func realignmentRepo(t *testing.T) *git.Repo {
	t.Helper()
	dir := t.TempDir()
	gitRun(t, dir, "init", "-q", "-b", "main")
	gitRun(t, dir, "config", "user.name", "Test")
	gitRun(t, dir, "config", "user.email", "test@example.com")
	gitRun(t, dir, "config", "commit.gpgsign", "false")
	for _, revision := range []string{"base", "head"} {
		data, err := os.ReadFile("../../internal/review/testdata/context-alignment/" + revision + ".md")
		if err != nil {
			t.Fatal(err)
		}
		writeFile(t, dir, "AGENTS.md", string(data))
		gitRun(t, dir, "add", "AGENTS.md")
		gitRun(t, dir, "commit", "-qm", revision)
		if revision == "base" {
			gitRun(t, dir, "checkout", "-qb", "feature")
		}
	}
	repo, err := git.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	return repo
}

func TestWindowApprovalSurvivesHistoricalRealignment(t *testing.T) {
	repo := realignmentRepo(t)
	m := modelFor(t, repo)
	m.context = 0
	m = drive(t, m, m.rescanCmd()())
	m = drive(t, m, letterKey("a"))
	first := reviewJSON(t, repo).VerdictOf("AGENTS.md", "Test")
	if first == nil || !first.Approved {
		t.Fatalf("approval failed: %s", m.status)
	}
	digest := first.Digest
	for _, context := range []int{1, 3, 5, 50, 100, 1000, 0} {
		m.context = context
		m = drive(t, m, m.rescanCmd()())
		st := reviewJSON(t, repo)
		if !fileIn(t, m, "AGENTS.md").approved || !st.Approved("AGENTS.md", "Test") || st.VerdictOf("AGENTS.md", "Test").Digest != digest {
			t.Fatalf("context %d lost approval: %s", context, st.Bytes())
		}
	}
	data, err := os.ReadFile(repo.Dir + "/AGENTS.md")
	if err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo.Dir, "AGENTS.md", string(data)+"\nactual edit\n")
	m = drive(t, m, m.rescanCmd()())
	if !reviewJSON(t, repo).Nullified("AGENTS.md", "Test") {
		t.Fatal("actual edit kept approval")
	}
	writeFile(t, repo.Dir, "AGENTS.md", string(data))
	m = drive(t, m, m.rescanCmd()())
	if fileIn(t, m, "AGENTS.md").approved || reviewJSON(t, repo).Approved("AGENTS.md", "Test") {
		t.Fatal("undo revived approval")
	}
}

func TestWindowRejectsApprovalWhenContentMovedAfterDrawing(t *testing.T) {
	m := newTestModel(t)
	path := m.view.paths()[0]
	next, cmd := m.Update(letterKey("a"))
	m = next.(Model)
	if cmd == nil || !m.writing {
		t.Fatal("approval did not schedule verification")
	}
	writeFile(t, m.src.(*git.Repo).Dir, path, "a change the reader has not seen\n")
	// Resizing still runs while the source read/write command is outstanding.
	next, _ = m.Update(tea.WindowSizeMsg{Width: 101, Height: 29})
	m = next.(Model)
	if m.width != 101 || !m.writing {
		t.Fatal("outstanding write blocked resize")
	}
	m = drive(t, m, cmd)
	if data, ok := m.store.ReadStateFile(review.FileName); ok {
		st, err := review.Parse([]byte(data))
		if err != nil {
			t.Fatal(err)
		}
		if st.Approved(path, "Test") {
			t.Fatal("approved unseen contents")
		}
	}
	if m.writing || !strings.Contains(m.status, "read it again") {
		t.Fatalf("write did not reject stale intent: %s", m.status)
	}
}

func TestQueuedApprovalsKeepTheirOrder(t *testing.T) {
	m := newTestModel(t)
	first := m.view.paths()[0]
	next, cmd := m.Update(letterKey("a"))
	m = next.(Model)
	next, _ = m.Update(letterKey("a"))
	m = next.(Model)
	m = drive(t, m, cmd)
	st := reviewJSON(t, m.src.(*git.Repo))
	var approved []string
	for _, f := range st.Files {
		if st.Approved(f.Path, "Test") {
			approved = append(approved, f.Path)
		}
	}
	if len(approved) != 2 || !slices.Contains(approved, first) {
		t.Fatalf("rapid approvals dropped or reordered: %v; %s", approved, m.status)
	}
}

func TestWindowShowsUnprovedLegacyApprovalSeparately(t *testing.T) {
	repo := testRepo(t)
	st := review.New(repo.CurrentBranch())
	st.Base = "main"
	st.Files = []*review.File{{Path: "beta.go", Verdicts: []*review.Verdict{{By: "agent", Approved: true, Digest: "old checksum"}}}}
	if err := repo.WriteStateFile(review.FileName, string(st.Bytes())); err != nil {
		t.Fatal(err)
	}
	m := modelFor(t, repo)
	if f := fileIn(t, m, "beta.go"); f.approved || len(f.others) > 0 || !slices.Equal(f.legacy, []string{"agent"}) {
		t.Fatalf("legacy claim presented as approval: %+v", f)
	}
	if !strings.Contains(plain(m), "legacy approval needs review") {
		t.Fatalf("legacy status missing: %s", plain(m))
	}
}

// A source scan can finish while a completed write's message is waiting in
// the event queue. That scan must still invalidate A after it observes B.
func TestInterveningScanReconcilesAfterDelayedWriteResult(t *testing.T) {
	m := newTestModel(t)
	path := m.view.paths()[0]
	repo := m.src.(*git.Repo)
	before, err := os.ReadFile(repo.Dir + "/" + path)
	if err != nil {
		t.Fatal(err)
	}
	next, cmd := m.Update(letterKey("a"))
	m = next.(Model)
	result := cmd() // Save A, but delay its delivery to the window.
	writeFile(t, repo.Dir, path, "B: actual edit\n")
	m = drive(t, m, m.rescanCmd()())
	if !m.writing || !m.pendingReconcile {
		t.Fatal("scan lost pending reconciliation")
	}
	m = drive(t, m, result)
	if !reviewJSON(t, repo).Nullified(path, "Test") {
		t.Fatal("intervening edit was never reconciled")
	}
	writeFile(t, repo.Dir, path, string(before))
	m = drive(t, m, m.rescanCmd()())
	if fileIn(t, m, path).approved || reviewJSON(t, repo).Approved(path, "Test") {
		t.Fatal("undo revived approval after intervening scan")
	}
}

func TestDelayedCompletionCannotOverwriteANewerNullification(t *testing.T) {
	m := newTestModel(t)
	repo := m.src.(*git.Repo)
	path := m.view.paths()[0]
	before, err := os.ReadFile(repo.Dir + "/" + path)
	if err != nil {
		t.Fatal(err)
	}
	next, cmd := m.Update(letterKey("a"))
	m = next.(Model)
	result := cmd()
	writeFile(t, repo.Dir, path, "B: actual edit\n")
	if err := review.Update(repo, m.branch, lockWait, func(st *review.State) error {
		scan, err := repo.Scan("main", 0)
		if err != nil {
			return err
		}
		reading := review.CompareScan(repo, scan, 0)
		st.ReconcileCanonical(reading.Identities, reading.Complete)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	writeFile(t, repo.Dir, path, string(before))
	m = drive(t, m, m.rescanCmd()())
	m = drive(t, m, result)
	if fileIn(t, m, path).approved || !reviewJSON(t, repo).Nullified(path, "Test") {
		t.Fatal("old completion revived a newer nullification")
	}
}

func TestFailedWriteStillReconcilesInterveningScan(t *testing.T) {
	m := newTestModel(t)
	repo := m.src.(*git.Repo)
	path := m.view.paths()[0]
	m = drive(t, m, letterKey("a"))
	if !reviewJSON(t, repo).Approved(path, "Test") {
		t.Fatal("initial approval failed")
	}
	// A reply can fail because another writer deleted its thread. That failure
	// must not discard an unrelated file's pending approval verification.
	cmd := m.mutate(reviewMutation{apply: func(*review.State) error { return errors.New("thread no longer exists") }})
	result := cmd()
	writeFile(t, repo.Dir, path, "B: actual edit\n")
	m = drive(t, m, m.rescanCmd()())
	m = drive(t, m, result)
	if !reviewJSON(t, repo).Nullified(path, "Test") {
		t.Fatal("failed user operation dropped pending reconciliation")
	}
	if !strings.Contains(m.status, "thread no longer exists") {
		t.Fatalf("lost foreground failure: %s", m.status)
	}
}
