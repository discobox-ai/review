package cli

import (
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/discobox-ai/review/internal/git"
)

// cardFor is a card about testRepo's change, from main: server.go is five
// lines, and line 4 is the one the branch changed.
func cardFor(t *testing.T, repo *git.Repo) string {
	t.Helper()
	main, ok := repo.CommitOf("main")
	if !ok {
		t.Fatal("no main")
	}
	return `{
  "base": "` + main[:7] + `",
  "summary": ["handler returns what write says"],
  "core": ["server.go#L4", "server.go#L3-L5"]
}`
}

// cardFile writes a card where set can be given it.
func cardFile(t *testing.T, body string) string {
	t.Helper()
	dir := t.TempDir()
	write(t, dir, "card.json", body)
	return filepath.Join(dir, "card.json")
}

// set keeps a card in the review, stamped with who and where, and the card
// command prints it with every link written the way comment takes a place.
func TestCardSetAndShow(t *testing.T) {
	repo := testRepo(t)
	out := run(t, repo, "card", "set", "--by", "claude", cardFile(t, cardFor(t, repo)))
	if !strings.Contains(out, "card: 2 core places") {
		t.Errorf("set said %q", out)
	}
	if strings.Contains(out, "recommends") {
		t.Errorf("set said the card's base is not the review's, though both are main: %q", out)
	}
	c := stored(t, repo).Card
	if c == nil {
		t.Fatal("no card in the review")
	}
	main, _ := repo.CommitOf("main")
	if c.By != "claude" || c.Commit != repo.Head() || c.At == "" || c.Base != main {
		t.Errorf("the card was not stamped: by %q commit %q at %q base %q", c.By, c.Commit, c.At, c.Base)
	}
	shown := run(t, repo, "card")
	for _, want := range []string{"by claude", "- handler returns what write says", "  server.go:4\n", "  server.go:3-5\n"} {
		if !strings.Contains(shown, want) {
			t.Errorf("card does not say %q:\n%s", want, shown)
		}
	}
	var back map[string]any
	if err := json.Unmarshal([]byte(run(t, repo, "card", "--json")), &back); err != nil || back["base"] != main {
		t.Errorf("card --json is not the card: %v %v", back, err)
	}
	run(t, repo, "card", "clear")
	if stored(t, repo).Card != nil {
		t.Error("clear left the card")
	}
}

// A card from standard input is the same card.
func TestCardSetFromStandardInput(t *testing.T) {
	repo := testRepo(t)
	var out strings.Builder
	if err := Run(repo, []string{"card", "set", "--by", "claude", "-"}, strings.NewReader(cardFor(t, repo)), &out); err != nil {
		t.Fatal(err)
	}
	if c := stored(t, repo).Card; c == nil || len(c.Core) != 2 {
		t.Errorf("card = %+v", c)
	}
}

// set refuses a card whose places do not hold, and says where each is.
func TestCardSetRefusesWhatIsNotThere(t *testing.T) {
	repo := testRepo(t)
	good := cardFor(t, repo)
	main, _ := repo.CommitOf("main")
	for name, c := range map[string]struct{ body, want string }{
		"a path not in the change": {
			strings.Replace(good, "server.go#L4", "other.go#L4", 1),
			"core[0]: other.go is not in the change",
		},
		"a line past the end": {
			strings.Replace(good, "server.go#L3-L5", "server.go#L3-L50", 1),
			"core[1]: server.go has 5 lines as it now stands",
		},
		"a ref for a base": {
			strings.Replace(good, main[:7], "main", 1),
			"base: 'main' does not match pattern",
		},
		"a base that is no commit": {
			strings.Replace(good, main[:7], "deadbee", 1),
			"base: deadbee is not a commit in this repository",
		},
		"a range the wrong way round": {
			strings.Replace(good, "server.go#L4", "server.go#L4-L2", 1),
			`core[0]: "server.go#L4-L2" is not a place`,
		},
		"a range with nothing changed in it": {
			strings.Replace(good, "server.go#L4", "server.go#L1-L2", 1),
			"core[0]: server.go:1-2 holds no line the change made",
		},
		"nobody": {
			good, "who wrote it",
		},
	} {
		args := []string{"card", "set", "--by", "claude", cardFile(t, c.body)}
		if name == "nobody" {
			args = []string{"card", "set", cardFile(t, c.body)}
		}
		var out strings.Builder
		err := Run(repo, args, strings.NewReader(""), &out)
		if err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: err = %v, want it to say %q", name, err, c.want)
		}
	}
	if body, ok := repo.ReadStateFile("discobox-review.json"); ok && strings.Contains(body, `"card"`) {
		t.Error("a refused card was kept")
	}
}

// Everything wrong with a card is said at once: a base that is no commit, and
// the places as well.
func TestCardSetSaysEverythingAtOnce(t *testing.T) {
	repo := testRepo(t)
	main, _ := repo.CommitOf("main")
	body := strings.Replace(strings.Replace(cardFor(t, repo), main[:7], "deadbee", 1), "server.go#L4", "other.go#L4", 1)
	var out strings.Builder
	err := Run(repo, []string{"card", "set", "--by", "claude", cardFile(t, body)}, strings.NewReader(""), &out)
	if err == nil || !strings.Contains(err.Error(), "base: deadbee") || !strings.Contains(err.Error(), "core[0]: other.go") {
		t.Errorf("err = %v, want both the base and the place", err)
	}
}

// What the schema refuses is said with what is wrong with the places.
func TestCardSetSaysTheSchemaAndThePlacesAtOnce(t *testing.T) {
	repo := testRepo(t)
	body := `{"base": "main", "summary": ["x"], "core": ["other.go#L1"], "extra": 1}`
	var out strings.Builder
	err := Run(repo, []string{"card", "set", "--by", "claude", cardFile(t, body)}, strings.NewReader(""), &out)
	for _, want := range []string{"base: 'main' does not match", "'extra' not allowed", "core[0]: other.go is not in the change"} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to say %q", err, want)
		}
	}
}

// set says when the card would have the review measured from another commit,
// and leaves the review's base alone.
func TestCardSetLeavesTheBaseAlone(t *testing.T) {
	repo := testRepo(t)
	main, _ := repo.CommitOf("main")
	head := repo.Head()
	// Something not committed, so that measured from HEAD there is still a
	// change for the card to be about.
	write(t, repo.Dir, "server.go", "package main\n\nfunc handler() error {\n\treturn write(nil)\n}\n")
	out := run(t, repo, "card", "set", "--by", "claude", cardFile(t, strings.Replace(cardFor(t, repo), main[:7], head[:7], 1)))
	if !strings.Contains(out, "discobox-review base "+head) {
		t.Errorf("set did not say how to follow the card's base: %q", out)
	}
	if st := stored(t, repo); st.Base != "main" {
		t.Errorf("set moved the base to %q", st.Base)
	}
}

// The schema is printed whole, as JSON, for whatever is asked to write a card.
func TestCardSchema(t *testing.T) {
	var doc map[string]any
	if err := json.Unmarshal([]byte(run(t, testRepo(t), "card", "schema")), &doc); err != nil {
		t.Fatal(err)
	}
	if doc["$id"] == nil || doc["properties"] == nil {
		t.Errorf("card schema printed %v", doc)
	}
}

// A place holds a line the change made, or it is refused: a deletion's seam is
// no such line, and the lines it took away are named on the old side — the
// rule the window finds the core's hunks by.
func TestCardSetAndADeletion(t *testing.T) {
	repo := testRepo(t)
	main, _ := repo.CommitOf("main")
	// Line 4 taken out altogether, so the new side has only its seam.
	write(t, repo.Dir, "server.go", "package main\n\nfunc handler() error {\n}\n")
	card := func(at string) string {
		return `{"base": "` + main[:7] + `", "summary": ["x"], "core": ["` + at + `"]}`
	}
	var out strings.Builder
	err := Run(repo, []string{"card", "set", "--by", "claude", cardFile(t, card("server.go#L3-L4"))}, strings.NewReader(""), &out)
	if err == nil || !strings.Contains(err.Error(), "holds no line the change made; what it removed there is server.go?side=old#L4") {
		t.Errorf("a place on a deletion's seam: err = %v", err)
	}
	// Lines the change left alone are refused without a hint: it took nothing
	// out of them.
	err = Run(repo, []string{"card", "set", "--by", "claude", cardFile(t, card("server.go#L1-L2"))}, strings.NewReader(""), &out)
	if err == nil || strings.Contains(err.Error(), "removed") {
		t.Errorf("a place on unchanged lines: err = %v", err)
	}
	run(t, repo, "card", "set", "--by", "claude", cardFile(t, card("server.go?side=old#L4")))
}

// A file the branch deleted is all removals: named whole it is a core place,
// and named by lines on its new side it has none.
func TestCardSetAndADeletedFile(t *testing.T) {
	repo := testRepo(t)
	main, _ := repo.CommitOf("main")
	gitRun(t, repo.Dir, "rm", "-q", "server.go")
	card := func(at string) string {
		return `{"base": "` + main[:7] + `", "summary": ["x"], "core": ["` + at + `"]}`
	}
	run(t, repo, "card", "set", "--by", "claude", cardFile(t, card("server.go")))
	var out strings.Builder
	if err := Run(repo, []string{"card", "set", "--by", "claude", cardFile(t, card("server.go#L1"))}, strings.NewReader(""), &out); err == nil || !strings.Contains(err.Error(), "has no lines as it now stands") {
		t.Errorf("lines of a deleted file's new side: err = %v", err)
	}
}
