package skill

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestTheEmbedIsTheSkill fails when the skill in .agents and the copy the
// binary carries differ: run `go tool task generate` and commit the result.
func TestTheEmbedIsTheSkill(t *testing.T) {
	want, err := os.ReadFile(filepath.Join("..", "..", ".agents", "skills", "discobox-review", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if Source() != string(want) {
		t.Fatal("internal/skill/SKILL.md differs from .agents/skills/discobox-review/SKILL.md; run `go tool task generate`")
	}
}

// The stamp lands inside the frontmatter, and the source has no metadata
// block of its own for it to collide with.
func TestStampedCarriesTheVersionInTheFrontmatter(t *testing.T) {
	if strings.Contains(frontmatter(t, Source()), "\nmetadata:") {
		t.Fatal("the skill has a metadata block of its own; stamp would write a second one")
	}
	got := Stamped("v1.2.3")
	fm := frontmatter(t, got)
	if !strings.HasSuffix(fm, "\nmetadata:\n  version: \"v1.2.3\"\n") {
		t.Fatalf("frontmatter does not end with the version:\n%s", fm)
	}
	if !strings.HasPrefix(fm, "---\nname: discobox-review\n") {
		t.Fatalf("frontmatter lost its name:\n%s", fm)
	}
	if strings.Replace(got, "metadata:\n  version: \"v1.2.3\"\n", "", 1) != Source() {
		t.Fatal("stamping changed more than the version line")
	}
}

func TestStampQuotesTheVersion(t *testing.T) {
	got := stamp("---\nname: x\n---\nbody\n", `a"b\c`)
	want := "---\nname: x\nmetadata:\n  version: \"a\\\"b\\\\c\"\n---\nbody\n"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

func TestStampLeavesTextWithoutFrontmatter(t *testing.T) {
	for _, text := range []string{"# no frontmatter\n", "---\nname: x\nnever closed\n"} {
		if got := stamp(text, "v1"); got != text {
			t.Errorf("stamp(%q) = %q, want it unchanged", text, got)
		}
	}
}

// frontmatter is text up to and including the newline before its closing
// fence.
func frontmatter(t *testing.T, text string) string {
	t.Helper()
	end := strings.Index(text[len("---\n"):], "\n---\n")
	if !strings.HasPrefix(text, "---\n") || end < 0 {
		t.Fatalf("no frontmatter in:\n%.200s", text)
	}
	return text[:len("---\n")+end+1]
}
