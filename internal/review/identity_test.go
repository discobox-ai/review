package review

import (
	"strings"
	"testing"

	"github.com/discobox-ai/review/pkg/source"
)

func TestCanonicalIdentitiesIncludeAllApprovedDimensions(t *testing.T) {
	pair := source.Change{
		Old: source.Side{Path: "f", Mode: "100644", Present: true, Data: []byte("old\n")},
		New: source.Side{Path: "f", Mode: "100644", Present: true, Data: []byte("new\n")},
	}
	run := changeRun{removed: []string{"old\n"}, added: []string{"new\n"}}
	original := (comparison{change: pair, runs: []changeRun{run}}).identity()
	if original.Digest != "47adae880df82a31bce732fa9d49e34b8d2f37b67926821c7c297a31652c2f4b" ||
		original.Runs[0] != "f1c71613e6eb73e080b2334fcd747e888fec2afb36eab098503725a9907383fc" ||
		original.Fingerprint != "4196f93f5e7b1f0bf089cd84abd375a282d9a54127a6bc881a98079ddd41ebbe" {
		t.Fatalf("identity encoding changed without a new scheme: %+v", original)
	}
	if len(original.Digest) != 64 || len(original.Runs) != 1 || len(original.Runs[0]) != 64 || original.Digest == original.Runs[0] || original.Digest == original.Fingerprint {
		t.Fatalf("domain separation: %+v", original)
	}
	for _, dimension := range []string{"path", "mode", "kind", "newline", "coordinate"} {
		t.Run(dimension, func(t *testing.T) {
			changed, changedRun := pair, run
			switch dimension {
			case "path":
				changed.New.Path = "renamed"
			case "mode":
				changed.New.Mode = "100755"
			case "kind":
				changed.New.Mode = "120000"
			case "newline":
				changed.New.Data = []byte("new")
				changedRun.added = []string{"new"}
			case "coordinate":
				changedRun.newStart = 1
			}
			id := (comparison{change: changed, runs: []changeRun{changedRun}}).identity()
			if id.Digest == original.Digest || id.Runs[0] == original.Runs[0] {
				t.Fatalf("ignored %s", dimension)
			}
		})
	}
}

func TestAnUnrelatedEditDoesNotChangeAnotherRunIdentity(t *testing.T) {
	pair := source.Change{Old: source.Side{Path: "f", Mode: "100644", Present: true, Data: []byte("old\nkeep\nlast\n")}, New: source.Side{Path: "f", Mode: "100644", Present: true, Data: []byte("new\nkeep\nlast\n")}}
	first := changeRun{removed: []string{"old\n"}, added: []string{"new\n"}}
	a := (comparison{change: pair, runs: []changeRun{first}}).identity()
	pair.New.Data = []byte("new\nkeep\nLAST\n")
	b := (comparison{change: pair, runs: []changeRun{first, {oldStart: 2, newStart: 2, removed: []string{"last\n"}, added: []string{"LAST\n"}}}}).identity()
	if a.Runs[0] != b.Runs[0] || a.Digest == b.Digest || a.Fingerprint == b.Fingerprint {
		t.Fatalf("run and file identity scopes diverged: %+v %+v", a, b)
	}
}

func TestMetadataAndBinaryChangesHaveDistinctFileIdentities(t *testing.T) {
	seen := map[string]bool{}
	for _, change := range []source.Change{
		{New: source.Side{Path: "f", Mode: "100644", Present: true}},
		{Old: source.Side{Path: "f", Mode: "100644", Present: true}},
		{Old: source.Side{Path: "f", Mode: "100644", Present: true}, New: source.Side{Path: "f", Mode: "100755", Present: true}},
		{Old: source.Side{Path: "f", Mode: "100644", Present: true}, New: source.Side{Path: "g", Mode: "100644", Present: true}},
		{Old: source.Side{Path: "f", Mode: "100644", Present: true, Data: []byte("a\x00")}, New: source.Side{Path: "f", Mode: "100644", Present: true, Data: []byte("b\x00")}},
		{Old: source.Side{Path: "f", Mode: "100644", Present: true, Data: []byte("a\x00")}, New: source.Side{Path: "f", Mode: "100644", Present: true, Data: []byte("c\x00")}},
	} {
		id := (comparison{change: change}).identity()
		if seen[id.Digest] || !strings.ContainsAny(id.Digest, "0123456789abcdef") || len(id.Runs) != 0 {
			t.Fatalf("indistinct metadata/binary identity: %+v", id)
		}
		seen[id.Digest] = true
	}
}
