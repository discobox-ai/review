package review

import (
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"github.com/discobox-ai/review/internal/git"
	"github.com/discobox-ai/review/pkg/source"
)

func TestCanonicalDisplayContextDoesNotRedefineApprovalRuns(t *testing.T) {
	old := "```\nold\n```\nkeep\n```\nlast\n```\n"
	newText := "```\nnew\n```\nkeep\n```\nLAST\n```\n"
	c := comparison{change: source.Change{
		Old: source.Side{Path: "f with spaces.md", Mode: "100644", Present: true, Data: []byte(old)},
		New: source.Side{Path: "f with spaces.md", Mode: "100644", Present: true, Data: []byte(newText)},
	}, runs: []changeRun{
		{oldStart: 1, newStart: 1, removed: []string{"old\n"}, added: []string{"new\n"}},
		{oldStart: 5, newStart: 5, removed: []string{"last\n"}, added: []string{"LAST\n"}},
	}}
	want := c.identity()
	for _, context := range []int{0, 1, 3, 5, 100, int(^uint(0) >> 1)} {
		got, err := c.render(context)
		if err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(got.Identity, want) {
			t.Fatal("context changed identity")
		}
		var runs []string
		for _, h := range got.Hunks {
			runs = append(runs, h...)
		}
		if !reflect.DeepEqual(runs, want.Runs) {
			t.Fatalf("context %d changed hunk approval scope: %v", context, runs)
		}
		if context < 2 && len(got.Patch.Hunks) != 2 || context >= 2 && len(got.Patch.Hunks) != 1 {
			t.Fatalf("context %d: %d hunks", context, len(got.Patch.Hunks))
		}
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "f with spaces.md"), []byte(old), 0o644); err != nil {
			t.Fatal(err)
		}
		var raw strings.Builder
		for _, line := range got.Patch.Preamble {
			raw.WriteString(line + "\n")
		}
		for _, h := range got.Patch.Hunks {
			raw.WriteString(h.Header + "\n")
			for _, line := range h.Lines {
				raw.WriteString(line + "\n")
			}
		}
		if _, err := (git.Local{}).Git(dir, []byte(raw.String()), "apply", "--unidiff-zero"); err != nil {
			t.Fatalf("context %d produced invalid patch: %v\n%s", context, err, raw.String())
		}
		data, err := os.ReadFile(filepath.Join(dir, "f with spaces.md"))
		if err != nil || string(data) != newText {
			t.Fatalf("context %d applied wrong contents: %q, %v", context, data, err)
		}
	}
}

func TestCanonicalDisplayPreservesAbsentFinalNewline(t *testing.T) {
	c := comparison{change: source.Change{New: source.Side{Path: "new", Mode: "100644", Present: true, Data: []byte("new")}}, runs: []changeRun{{added: []string{"new"}}}}
	got, err := c.render(0)
	if err != nil {
		t.Fatal(err)
	}
	h := got.Patch.Hunks[0]
	if h.Header != "@@ -0,0 +1,1 @@" || !reflect.DeepEqual(h.Lines, []string{"+new", `\ No newline at end of file`}) {
		t.Fatalf("newline or addition coordinates lost: %+v", h)
	}
	if len(got.Hunks) != 1 || len(got.Hunks[0]) != 1 {
		t.Fatal("run lost from displayed hunk")
	}
}

func TestCanonicalDisplayRejectsInvalidOrUnavailableData(t *testing.T) {
	for _, c := range []comparison{
		{change: source.Change{Problem: "truncated"}},
		{change: source.Change{New: source.Side{Present: true, Data: []byte("different\n")}}},
		{change: source.Change{New: source.Side{Present: true, Data: []byte("new\n")}}, runs: []changeRun{{}}},
		{change: source.Change{New: source.Side{Present: true, Data: []byte("new\n")}}, runs: []changeRun{{added: []string{"wrong\n"}}}},
	} {
		if _, err := c.render(5); err == nil {
			t.Fatal("accepted invalid comparison")
		}
	}
}

func TestRawPatchPreservesGitQuotedFilenameBytes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Windows disallows control bytes in filenames")
	}
	for _, path := range []string{"escape\x1b.md", "unicode-🦊.md", "line\nname", string([]byte{'b', 0xff})} {
		change := source.Change{Old: source.Side{Path: path, Mode: "100644", Present: true, Data: []byte("before\n")}, New: source.Side{Path: path, Mode: "100644", Present: true, Data: []byte("after\n")}}
		compared, err := Compare(change, 3)
		if err != nil {
			t.Fatal(err)
		}
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, path), change.Old.Data, 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := (git.Local{}).Git(dir, []byte(compared.Unified()), "apply"); err != nil {
			t.Fatalf("path %q: %v\n%s", path, err, compared.Unified())
		}
		data, err := os.ReadFile(filepath.Join(dir, path))
		if err != nil || string(data) != "after\n" {
			t.Fatalf("path %q: %q, %v", path, data, err)
		}
	}
}

func TestRawKindReplacementAppliesBothDirections(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("creating symlinks requires Windows privileges")
	}
	for _, same := range []bool{true, false} {
		old := source.Side{Path: "link", Mode: "100644", Present: true, Data: []byte("target")}
		next := source.Side{Path: "link", Mode: "120000", Present: true, Data: []byte("target")}
		if !same {
			next.Data = []byte("different-target")
		}
		for _, reverse := range []bool{false, true} {
			a, b := old, next
			if reverse {
				a, b = b, a
			}
			compared, err := Compare(source.Change{Old: a, New: b}, 0)
			if err != nil {
				t.Fatal(err)
			}
			dir := t.TempDir()
			path := filepath.Join(dir, "link")
			if a.Mode == "120000" {
				err = os.Symlink(string(a.Data), path)
			} else {
				err = os.WriteFile(path, a.Data, 0o644)
			}
			if err != nil {
				t.Fatal(err)
			}
			if _, err := (git.Local{}).Git(dir, []byte(compared.Unified()), "apply", "--unidiff-zero"); err != nil {
				t.Fatalf("reverse %v, same %v: %v\n%s", reverse, same, err, compared.Unified())
			}
			var data []byte
			if b.Mode == "120000" {
				var target string
				target, err = os.Readlink(path)
				data = []byte(target)
			} else {
				data, err = os.ReadFile(path)
			}
			if err != nil || string(data) != string(b.Data) {
				t.Fatalf("wrong kind/content: %q, %v", data, err)
			}
		}
	}
}
