package cli

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/discobox-ai/review/internal/git"
	"github.com/discobox-ai/review/internal/review"
)

func realignedRepo(t *testing.T) *git.Repo {
	t.Helper()
	dir := t.TempDir()
	for _, args := range [][]string{{"init", "-q", "-b", "main"}, {"config", "user.name", "Test"}, {"config", "user.email", "test@example.com"}, {"config", "commit.gpgsign", "false"}} {
		gitRun(t, dir, args...)
	}
	for _, side := range []string{"base", "head"} {
		data, err := os.ReadFile(filepath.Join("..", "review", "testdata", "context-alignment", side+".md"))
		if err != nil {
			t.Fatal(err)
		}
		write(t, dir, "AGENTS.md", string(data))
		gitRun(t, dir, "add", "AGENTS.md")
		gitRun(t, dir, "commit", "-qm", side)
		if side == "base" {
			gitRun(t, dir, "checkout", "-qb", "feature")
		}
	}
	repo, err := git.Open(dir)
	if err != nil {
		t.Fatal(err)
	}
	run(t, repo, "base", "main")
	return repo
}

func TestCLIApprovalsSurviveTheReportedHunkRealignment(t *testing.T) {
	repo := realignedRepo(t)
	run(t, repo, "approve", "--by", "reader", "AGENTS.md")
	for _, context := range []int{0, 1, 3, 5, 100, 1000} {
		run(t, repo, "diff", "--raw", "--context", strconv.Itoa(context), "--path", "AGENTS.md")
		status := run(t, repo, "status", "--by", "reader")
		if !strings.Contains(status, "approved    1") || strings.Contains(status, "nullified") {
			t.Fatalf("context %d lost approval: %s", context, status)
		}
	}
	data, err := os.ReadFile(filepath.Join(repo.Dir, "AGENTS.md"))
	if err != nil {
		t.Fatal(err)
	}
	write(t, repo.Dir, "AGENTS.md", string(data)+"\nactual content edit\n")
	if status := run(t, repo, "status", "--by", "reader"); !strings.Contains(status, "nullified   AGENTS.md") {
		t.Fatalf("actual edit did not nullify: %s", status)
	}
	write(t, repo.Dir, "AGENTS.md", string(data))
	if status := run(t, repo, "status", "--by", "reader"); !strings.Contains(status, "approved    0") {
		t.Fatalf("undo revived approval: %s", status)
	}
}

func TestConcurrentApprovalsSurviveReadersAtDifferentContexts(t *testing.T) {
	repo := realignedRepo(t)
	const reviewers = 6
	var wg sync.WaitGroup
	errors := make(chan error, reviewers*3)
	for i := range reviewers {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			name := "reader-" + strconv.Itoa(i)
			for _, args := range [][]string{{"approve", "--by", name, "AGENTS.md"}, {"diff", "--raw", "--context", strconv.Itoa([]int{0, 1, 3, 5, 100, 1000}[i])}, {"status", "--by", name}} {
				var out strings.Builder
				if err := Run(repo, args, strings.NewReader(""), &out); err != nil {
					errors <- err
				}
			}
		}(i)
	}
	wg.Wait()
	close(errors)
	for err := range errors {
		t.Error(err)
	}
	st := stored(t, repo)
	for i := range reviewers {
		if !st.Approved("AGENTS.md", "reader-"+strconv.Itoa(i)) {
			t.Fatalf("concurrent approval dropped: %s", st.Bytes())
		}
	}
}

func TestUnverifiableApprovalDoesNotPartlyWrite(t *testing.T) {
	repo := realignedRepo(t)
	before, _ := repo.ReadStateFile(review.FileName)
	fails(t, repo, "approve", "--by", "reader", "AGENTS.md", "missing")
	after, _ := repo.ReadStateFile(review.FileName)
	if before != after {
		t.Fatalf("failed approval partly wrote the review: %s", after)
	}
}

func TestLegacyClaimsAreShownAsNeedingReview(t *testing.T) {
	repo := realignedRepo(t)
	scan, err := repo.Scan("main", 0)
	if err != nil {
		t.Fatal(err)
	}
	old := review.New("feature")
	old.Base = "main"
	old.Files = []*review.File{{Path: "AGENTS.md", Verdicts: []*review.Verdict{{By: "legacy", Approved: true, Digest: review.LegacyDigest(git.ParseMulti(scan.Diff)[0])}}}}
	if err := repo.WriteStateFile(review.FileName, string(old.Bytes())); err != nil {
		t.Fatal(err)
	}
	status := run(t, repo, "status", "--by", "legacy")
	if !strings.Contains(status, "legacy approval needs review") || !strings.Contains(status, "approved    0") || strings.Contains(status, "nullified") {
		t.Fatalf("legacy claim silently lost or broadened: %s", status)
	}
	run(t, repo, "approve", "--by", "legacy", "AGENTS.md")
	if status := run(t, repo, "status", "--by", "legacy"); strings.Contains(status, "needs review") || !strings.Contains(status, "approved    1") {
		t.Fatalf("explicit replacement did not retire pending claim: %s", status)
	}
}
