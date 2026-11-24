// Package skill is the discobox-review agent skill, carried in the binary.
//
// The skill is written in .agents/skills/discobox-review/SKILL.md, where an
// agent working on this repository reads it. go:embed cannot reach outside
// the package, so `go generate` copies that file here, and a test fails when
// the copy and the file differ. A binary installed with `go install` then
// brings the skill that goes with it, and prints it with `discobox-review
// skill`.
package skill

import (
	_ "embed"
	"strings"
)

//go:generate go run ../cmd/copyfile ../../.agents/skills/discobox-review/SKILL.md SKILL.md

//go:embed SKILL.md
var source string

// Source is the skill exactly as it is written in the repository.
func Source() string { return source }

// Stamped is the skill with version added to its frontmatter, as
// metadata.version, so a copy written out somewhere says which
// discobox-review it was written for.
func Stamped(version string) string {
	return stamp(source, version)
}

// stamp puts a metadata block at the end of text's frontmatter: before the
// second "---" line. text without frontmatter is returned unchanged.
func stamp(text, version string) string {
	const fence = "---\n"
	if !strings.HasPrefix(text, fence) {
		return text
	}
	end := strings.Index(text[len(fence):], "\n"+fence)
	if end < 0 {
		return text
	}
	end += len(fence) + 1
	return text[:end] + "metadata:\n  version: " + quote(version) + "\n" + text[end:]
}

// quote writes version as a YAML double-quoted string, so a version that
// looks like a number or holds a colon stays a string.
func quote(s string) string {
	r := strings.NewReplacer(`\`, `\\`, `"`, `\"`)
	return `"` + r.Replace(s) + `"`
}
