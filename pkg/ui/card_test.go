package ui

import (
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"

	"github.com/discobox-ai/review/internal/git"
	"github.com/discobox-ai/review/internal/review"
)

// keepCard writes a card, and a base, into a review the way card set does.
func keepCard(t *testing.T, repo *git.Repo, base string, c *review.Card) {
	t.Helper()
	err := review.Update(repo, repo.CurrentBranch(), time.Second, func(st *review.State) error {
		st.Base, st.Card = base, c
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

// coreRepo is foo.txt with three hunks, of which the card names the first and
// the last as the core, and bar.txt, which it does not name.
func coreRepo(t *testing.T) (*git.Repo, Model) {
	t.Helper()
	repo := changedRepo(t, 60, map[int]string{4: "FOUR", 30: "THIRTY", 56: "FIFTY-SIX"})
	keepCard(t, repo, "main", &review.Card{
		Summary: []string{"Shout three lines", "Leave bar alone"},
		Core:    []string{"foo.txt#L4", "foo.txt#L56"},
	})
	return repo, modelFor(t, repo)
}

// entry is the stack's entry for a file: the core's view of it, or its own.
func entry(t *testing.T, m Model, path string, core bool) (int, *reviewFile) {
	t.Helper()
	key := path
	if core {
		key = coreKey(path)
	}
	i, ok := m.view.byPath[key]
	if !ok {
		t.Fatalf("no entry %q in the stack", key)
	}
	return i, m.view.files[i]
}

// heads is the text of the hunk headers drawn for one entry.
func heads(m Model, fi int) []string {
	var out []string
	for _, r := range m.view.rows {
		if r.kind != rowDiff || r.file != fi {
			continue
		}
		if l, ok := m.view.files[fi].rowLine(r.line); ok && l.Kind == '@' {
			out = append(out, l.Text)
		}
	}
	return out
}

// onHead puts the cursor on an entry's nth hunk header.
func onHead(t *testing.T, m Model, fi, n int) Model {
	t.Helper()
	seen := 0
	for i, r := range m.view.rows {
		if r.kind != rowDiff || r.file != fi {
			continue
		}
		if l, ok := m.view.files[fi].rowLine(r.line); ok && l.Kind == '@' {
			if seen == n {
				m.view.cursor = i
				return m
			}
			seen++
		}
	}
	t.Fatalf("entry %d has no hunk header %d", fi, n)
	return m
}

func rowsOf(m Model, kind rowKind) []reviewRow {
	var out []reviewRow
	for _, r := range m.view.rows {
		if r.kind == kind {
			out = append(out, r)
		}
	}
	return out
}

// The stack opens with what changed, then the core, then every file whole.
func TestTheCardSortsTheStack(t *testing.T) {
	_, m := coreRepo(t)
	if got := m.view.paths(); strings.Join(got, " ") != coreKey("foo.txt")+" bar.txt foo.txt" {
		t.Fatalf("the stack is %q", got)
	}
	var banner []string
	for _, r := range rowsOf(m, rowBanner) {
		banner = append(banner, strings.TrimSpace(r.text))
	}
	for _, want := range []string{"WHAT CHANGED", "• Shout three lines", "CORE · 0/2 hunks approved", "THE REST · 0/2 files approved"} {
		if !strings.Contains(strings.Join(banner, "\n"), want) {
			t.Errorf("the stack does not say %q:\n%s", want, strings.Join(banner, "\n"))
		}
	}
	core, _ := entry(t, m, "foo.txt", true)
	own, _ := entry(t, m, "foo.txt", false)
	if n := len(heads(m, core)); n != 2 {
		t.Errorf("the core draws %d of foo.txt's hunks, want its two core ones", n)
	}
	if n := len(heads(m, own)); n != 3 {
		t.Errorf("the rest draws %d of foo.txt's hunks, want all three", n)
	}
	m = drive(t, m, letterKey("i"))
	if len(rowsOf(m, rowBanner)) != 2 {
		t.Error("i did not put the summary away, leaving the groups' headings")
	}
}

// What lies between two core hunks is one row, saying how much of it changed,
// and opens to the hunks and the unchanged lines between them.
func TestAStretchOpensToWhatIsBetween(t *testing.T) {
	_, m := coreRepo(t)
	core, _ := entry(t, m, "foo.txt", true)
	var at int
	found := false
	for i, r := range m.view.rows {
		if r.kind == rowStretch && r.file == core {
			if !strings.Contains(r.text, "2 changed, ") {
				t.Errorf("the stretch says %q", r.text)
			}
			at, found = i, true
		}
	}
	if !found {
		t.Fatal("no stretch between the core hunks")
	}
	m.view.cursor = at
	m = drive(t, m, letterKey(" "))
	if n := len(heads(m, core)); n != 3 {
		t.Fatalf("opened, the stretch shows %d hunks in all, want the one between too", n)
	}
	if m.view.rows[m.view.cursor].kind != rowStretch {
		t.Error("the cursor left the stretch it opened")
	}
	m = drive(t, m, letterKey(" "))
	if n := len(heads(m, core)); n != 2 {
		t.Errorf("shut again, the core shows %d hunks", n)
	}
}

// a goes through the core's hunks before anything else, and the core's heading
// says when they are done.
func TestAApprovesTheCoreFirst(t *testing.T) {
	_, m := coreRepo(t)
	core, _ := entry(t, m, "foo.txt", true)
	m = onHead(t, m, core, 0)
	m = drive(t, m, letterKey("a"))
	if l, ok := cursorDiffLine(m, "foo.txt"); !ok || l.Kind != '@' || l.Hunk != 2 || m.view.keyAt() != coreKey("foo.txt") {
		t.Fatalf("after the first core hunk the cursor is on %+v in %q, want the last core hunk", l, m.view.keyAt())
	}
	m = drive(t, m, letterKey("a"))
	if got := m.view.pathAt(); got != "bar.txt" {
		t.Errorf("after the core the cursor is in %q, want the rest's first file", got)
	}
	_, c := entry(t, m, "foo.txt", true)
	_, own := entry(t, m, "foo.txt", false)
	if c.verdict() != "✓" || own.verdict() == "✓" {
		t.Errorf("verdicts: core %q, rest %q; want the core done and the file not", c.verdict(), own.verdict())
	}
	if !strings.Contains(m.view.card.coreHead, "2/2 hunks") {
		t.Errorf("the core says %q", m.view.card.coreHead)
	}
}

// The two entries of a file fold apart, and the cursor stays in the one it was
// in.
func TestTheTwoEntriesOfAFileFoldApart(t *testing.T) {
	_, m := coreRepo(t)
	i, _ := m.view.fileRow(coreKey("foo.txt"))
	m.view.cursor = i
	m = drive(t, m, letterKey(" "))
	_, c := entry(t, m, "foo.txt", true)
	_, own := entry(t, m, "foo.txt", false)
	if !c.folded || own.folded {
		t.Errorf("folded: core %v, rest %v; want only the core", c.folded, own.folded)
	}
	if m.view.keyAt() != coreKey("foo.txt") {
		t.Errorf("the cursor went to %q", m.view.keyAt())
	}

	_, m = coreRepo(t)
	fi, _ := entry(t, m, "foo.txt", false)
	m = onHead(t, m, fi, 0)
	m = drive(t, m, letterKey(" "))
	_, c = entry(t, m, "foo.txt", true)
	_, own = entry(t, m, "foo.txt", false)
	if !own.shut[0] || c.shut[0] {
		t.Errorf("the rest's first hunk folded %v, the core's %v; want only the rest's", own.shut[0], c.shut[0])
	}
	if m.view.keyAt() != "foo.txt" {
		t.Errorf("the cursor went to %q", m.view.keyAt())
	}
}

// A comment being written is drawn once, in the entry it was begun in.
func TestTheCommentBoxIsDrawnOnce(t *testing.T) {
	_, m := coreRepo(t)
	core, _ := entry(t, m, "foo.txt", true)
	for i, r := range m.view.rows {
		if r.kind != rowDiff || r.file != core {
			continue
		}
		if l, ok := m.view.files[core].rowLine(r.line); ok && l.Kind == '+' && l.New == 4 {
			m.view.cursor = i
			break
		}
	}
	m = drive(t, m, letterKey("c"))
	files := map[int]bool{}
	for _, r := range rowsOf(m, rowDraft) {
		files[r.file] = true
	}
	if len(files) != 1 || !files[core] {
		t.Errorf("the box is drawn in entries %v, want only the core's %d", files, core)
	}
}

// A card about the change from another commit says so, and b offers that
// commit first.
func TestBOffersTheCardsBase(t *testing.T) {
	repo := testRepo(t)
	from, ok := repo.CommitOf("HEAD~1")
	if !ok {
		t.Fatal("no HEAD~1")
	}
	keepCard(t, repo, "main", &review.Card{Summary: []string{"x"}, Core: []string{"beta.go"}, Base: from})
	m := modelFor(t, repo)
	if !strings.Contains(m.view.card.note, "b offers it first") {
		t.Errorf("the card says %q", m.view.card.note)
	}
	m = drive(t, m, letterKey("b"))
	if m.dialog == nil || !strings.HasPrefix(m.dialog.items[0], cardBase) {
		t.Fatalf("b offers %v", m.dialog)
	}
	cmd := m.dialog.action(m.dialog.items[0])
	m.dialog = nil
	m = runCmd(t, m, cmd)
	if short := abbrevHash(from); m.baseRef != short || reviewJSON(t, repo).Base != short {
		t.Errorf("the review is measured from %q, saved as %q; want %s", m.baseRef, reviewJSON(t, repo).Base, short)
	}
	if m.view.card.note != "" {
		t.Errorf("measured from the card's base, the card still says %q", m.view.card.note)
	}
}

// onStretch puts the cursor on the core entry's stretch.
func onStretch(t *testing.T, m Model) Model {
	t.Helper()
	core, _ := entry(t, m, "foo.txt", true)
	for i, r := range m.view.rows {
		if r.kind == rowStretch && r.file == core {
			m.view.cursor = i
			return m
		}
	}
	t.Fatal("no stretch")
	return m
}

// a on a stretch approves nothing: the hunks it stands for have not been
// read. Nor do the comment keys take it for a conversation.
func TestAStretchIsNotApprovedUnseen(t *testing.T) {
	repo, m := coreRepo(t)
	m = onStretch(t, m)
	m = drive(t, m, letterKey("a"))
	if reviewJSON(t, repo).Approved("foo.txt", "Test") || len(reviewJSON(t, repo).ApprovedHunks("foo.txt", "Test")) > 0 {
		t.Error("a on a stretch approved what it stands for")
	}
	if !strings.Contains(m.status, "not core") {
		t.Errorf("status = %q", m.status)
	}
	m = onStretch(t, m)
	m = drive(t, m, letterKey("r"))
	if strings.Contains(m.status, "no longer there") {
		t.Errorf("r on a stretch took it for a conversation: %q", m.status)
	}
}

// A resize puts the cursor back in the entry it was in, not the file's other.
func TestAResizeKeepsTheCursorInItsEntry(t *testing.T) {
	_, m := coreRepo(t)
	core, _ := entry(t, m, "foo.txt", true)
	m = onHead(t, m, core, 1)
	m = drive(t, m, tea.WindowSizeMsg{Width: 70, Height: 40})
	if m.view.keyAt() != coreKey("foo.txt") {
		t.Errorf("after a resize the cursor is in %q", m.view.keyAt())
	}
}

// Approving a file from its core heading hands both its entries' folds back
// to the rule, so after F opened everything both fold away.
func TestApprovingAFileFoldsBothEntries(t *testing.T) {
	_, m := coreRepo(t)
	m = drive(t, m, letterKey("F"), letterKey("F"))
	i, _ := m.view.fileRow(coreKey("foo.txt"))
	m.view.cursor = i
	m = drive(t, m, letterKey("a"))
	_, c := entry(t, m, "foo.txt", true)
	_, own := entry(t, m, "foo.txt", false)
	if !c.folded || !own.folded {
		t.Errorf("approved, folded: core %v, rest %v; want both", c.folded, own.folded)
	}
}

// f hides the files approved, and the groups go on counting them.
func TestTheGroupsCountWhatFHides(t *testing.T) {
	_, m := coreRepo(t)
	i, _ := m.view.fileRow("bar.txt")
	m.view.cursor = i
	m = drive(t, m, letterKey("a"), letterKey("f"))
	if !strings.Contains(m.view.card.restHead, "1/2 files") {
		t.Errorf("with bar.txt approved and hidden, the rest says %q", m.view.card.restHead)
	}
}

// A file named whole is every hunk of it, its removals included: a file the
// branch deleted is a core file like any other.
func TestAWholeDeletedFileIsCore(t *testing.T) {
	repo := changedRepo(t, 10, map[int]string{4: "FOUR"})
	gitRun(t, repo.Dir, "rm", "-q", "-f", "bar.txt")
	keepCard(t, repo, "main", &review.Card{Summary: []string{"x"}, Core: []string{"bar.txt"}})
	m := modelFor(t, repo)
	_, c := entry(t, m, "bar.txt", true)
	if len(c.coreHunks) == 0 {
		t.Error("the deleted file has no core hunk")
	}
}

// The title counts a core file once, and the card stays on the screen when the
// base leaves nothing to review: its word on the base is wanted then most.
func TestTheTitleCountsACoreFileOnce(t *testing.T) {
	_, m := coreRepo(t)
	if got := m.view.tally(m.rv); !strings.HasPrefix(got, "0/2 approved") {
		t.Errorf("tally = %q, want the review's two files", got)
	}
}

func TestTheCardStaysWithNothingToReview(t *testing.T) {
	repo := changedRepo(t, 10, map[int]string{4: "FOUR"})
	gitRun(t, repo.Dir, "commit", "-qam", "all of it")
	main, _ := repo.CommitOf("main")
	keepCard(t, repo, "HEAD", &review.Card{Summary: []string{"x"}, Core: []string{"foo.txt"}, Base: main})
	m := New(repo, Options{Author: "Test", Store: repo, Base: "HEAD"})
	m = drive(t, m, tea.WindowSizeMsg{Width: 140, Height: 40}, m.rescanCmd()())
	if len(m.view.files) != 0 {
		t.Fatalf("measured from HEAD the review has %d files", len(m.view.files))
	}
	if m.view.card == nil || m.view.card.note == "" || len(rowsOf(m, rowBanner)) == 0 {
		t.Errorf("with nothing to review the card says nothing: %+v", m.view.card)
	}
}
